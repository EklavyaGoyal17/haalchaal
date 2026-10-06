package outbound

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
)

// InboundResult says what HandleInbound did.
type InboundResult string

const (
	InboundDuplicate    InboundResult = "duplicate"
	InboundAcknowledged InboundResult = "acknowledged"
	InboundStored       InboundResult = "stored"
	InboundStatus       InboundResult = "status"
)

// HandleInbound records one verified WhatsApp event (SPEC §10): an "I'm on
// it" press from someone in the alert's family acknowledges the alert; any
// other message is stored encrypted for the admin review queue; delivery
// statuses update the notification. There are no automatic replies.
func (s *Service) HandleInbound(ctx context.Context, ev notify.InboundEvent) (InboundResult, error) {
	res := InboundStored
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		now := s.Clock.Now()
		_, err := q.InsertWebhookEvent(ctx, db.InsertWebhookEventParams{Provider: "whatsapp", EventID: ev.EventID, ReceivedAt: now})
		if errors.Is(err, pgx.ErrNoRows) {
			res = InboundDuplicate
			return nil
		}
		if err != nil {
			return fmt.Errorf("record inbound event: %w", err)
		}
		if ev.Kind == notify.KindStatus {
			res = InboundStatus
			switch ev.Payload {
			case "delivered", "read", "failed":
				_, err := q.UpdateNotificationDelivery(ctx, db.UpdateNotificationDeliveryParams{Status: ev.Payload, ProviderMessageID: &ev.MessageID})
				return err
			}
			return nil
		}

		members, err := q.ListMembersByPhone(ctx, ev.From)
		if err != nil {
			return err
		}
		if ev.Kind == notify.KindButton {
			if id, ok := notify.ParseAck(ev.Payload); ok {
				if alertID, err := uuid.Parse(id); err == nil {
					for _, m := range members {
						row, err := q.AcknowledgeAlert(ctx, db.AcknowledgeAlertParams{AlertID: alertID, MemberID: &m.ID, At: &now})
						if errors.Is(err, pgx.ErrNoRows) {
							continue
						}
						if err != nil {
							return err
						}
						res = InboundAcknowledged
						s.Log.Info("alert acknowledged", "alert_id", row.ID, "member_id", m.ID)
						return audit.Write(ctx, tx, "family:"+m.ID.String(), "alert_acknowledged", "alert", row.ID.String(), nil)
					}
				}
			}
		}

		var memberID *uuid.UUID
		if len(members) > 0 {
			memberID = &members[0].ID
		}
		var body []byte
		if ev.Payload != "" {
			if body, err = s.Keyring.EncryptString(ev.Payload, AADInbound); err != nil {
				return err
			}
		}
		kind := notify.KindText
		if ev.Kind == notify.KindButton {
			kind = notify.KindButton
		}
		_, err = q.InsertInboundMessage(ctx, db.InsertInboundMessageParams{
			FamilyMemberID: memberID, FromE164: ev.From, Kind: kind, BodyEnc: body, ReceivedAt: now,
		})
		return err
	})
	return res, err
}
