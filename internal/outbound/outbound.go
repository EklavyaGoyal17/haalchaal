// Package outbound sends everything that leaves the system as a message:
// family summaries, alert escalation steps and admin notices, and handles
// inbound WhatsApp events (acknowledgements, replies, delivery statuses).
// Every send passes the safety gate and writes a notifications row.
package outbound

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/logging"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Service holds what sending needs.
type Service struct {
	Pool        *pgxpool.Pool
	Clock       clock.Clock
	Log         *slog.Logger
	Keyring     *crypto.Keyring
	Messenger   notify.Messenger
	Gate        safety.Gate
	AdminPhones []string
	Timeouts    alerts.Timeouts
	ReviewURL   string // link in admin messages, e.g. https://admin.example/admin/review
}

// AADInbound binds inbound message bodies to their column.
const AADInbound = "inbound_messages.body_enc"

// deliver sends a message at least once and at most once per dedupe key once
// it has gone out. A blocked contact is recorded and not retried; a vendor
// error is recorded and returned so the job retries.
func (s *Service) deliver(ctx context.Context, p db.UpsertNotificationParams, to string, msg notify.TemplateMessage) error {
	q := db.New(s.Pool)
	p.CreatedAt = s.Clock.Now()
	n, err := q.UpsertNotification(ctx, p)
	if err != nil {
		return fmt.Errorf("record notification: %w", err)
	}
	switch n.Status {
	case "sent", "delivered", "read":
		return nil
	}
	log := s.Log.With("notification_id", n.ID, "template", msg.Template, "to", logging.MaskPhone(to))
	if err := s.Gate.AllowMessage(to); err != nil {
		reason := "blocked: " + err.Error()
		if mErr := q.MarkNotificationFailed(ctx, db.MarkNotificationFailedParams{ID: n.ID, Error: &reason}); mErr != nil {
			return mErr
		}
		log.Info("message blocked by safety gate", "reason", err.Error())
		return nil
	}
	msg.To = to
	id, err := s.Messenger.SendTemplate(ctx, msg)
	if err != nil {
		reason := "send failed"
		if mErr := q.MarkNotificationFailed(ctx, db.MarkNotificationFailedParams{ID: n.ID, Error: &reason}); mErr != nil {
			return mErr
		}
		return fmt.Errorf("send %s: %w", msg.Template, err)
	}
	now := s.Clock.Now()
	if err := q.MarkNotificationSent(ctx, db.MarkNotificationSentParams{ID: n.ID, ProviderMessageID: &id, SentAt: &now}); err != nil {
		return fmt.Errorf("mark sent: %w", err)
	}
	log.Info("message sent")
	return nil
}

// templateLanguage maps a member language to an approved template language.
// Templates exist in English and Hindi.
func templateLanguage(lang string) string {
	if lang == "hi" {
		return "hi"
	}
	return "en"
}

// parentRef is a short, non-identifying reference for admin messages.
func parentRef(id fmt.Stringer) string {
	s := id.String()
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func clean(s string) string { return notify.CleanParam(strings.TrimSpace(s)) }
