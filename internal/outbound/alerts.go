package outbound

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
)

// closed reports whether an alert needs no more escalation.
func closed(status string) bool {
	return status == "acknowledged" || status == "resolved" || status == "false_alarm"
}

// EscalateAlert is the escalate_alert job handler: it plans one step,
// enqueues a send_alert job per recipient, and schedules the next step. It
// stops as soon as anyone acknowledges.
func (s *Service) EscalateAlert(ctx context.Context, j jobs.Job) error {
	if j.Payload.AlertID == nil || j.Payload.Step == nil {
		return jobs.Permanent(errors.New("escalate_alert without alert_id or step"))
	}
	alertID, step := *j.Payload.AlertID, *j.Payload.Step
	q := db.New(s.Pool)
	a, err := q.GetAlert(ctx, alertID)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Permanent(fmt.Errorf("alert %s not found", alertID))
	}
	if err != nil {
		return err
	}
	if closed(a.Status) {
		return nil
	}
	chain := j.Payload.Chain
	if chain != "" && chain != a.Type {
		return nil // superseded by the chain of an upgraded alert
	}
	if chain == "" {
		chain = a.Type
	}
	p, err := q.GetParent(ctx, a.ParentID)
	if err != nil {
		return err
	}
	members, err := q.ListOptedInMembers(ctx, p.AccountID)
	if err != nil {
		return err
	}
	aud := alerts.Audience{LocalContact: p.LocalContactPhoneE164 != nil, Admins: len(s.AdminPhones)}
	for _, m := range members {
		aud.Members = append(aud.Members, m.ID)
	}
	plan := alerts.PlanStep(a.Type, a.Category, aud, step, s.Timeouts)
	now := s.Clock.Now()

	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, t := range plan.Targets {
			stepCopy := step
			spec := jobs.Spec{
				Kind:        jobs.KindSendAlert,
				DedupeKey:   "send_alert:" + alertID.String() + ":" + strconv.Itoa(step) + ":" + t.Key() + ":" + chain,
				Payload:     jobs.Payload{ParentID: &a.ParentID, AlertID: &alertID, Step: &stepCopy, Recipient: t.Key(), Chain: chain},
				RunAt:       now,
				MaxAttempts: alerts.SafetyMaxAttempts,
			}
			if _, _, err := jobs.Enqueue(ctx, tx, spec); err != nil {
				return err
			}
		}
		if len(plan.Targets) > 0 {
			if err := db.New(tx).MarkAlertNotified(ctx, db.MarkAlertNotifiedParams{ID: alertID, Step: int32(step)}); err != nil {
				return err
			}
		}
		if err := audit.Write(ctx, tx, audit.ActorSystem, "alert_escalation_step", "alert", alertID.String(), audit.Details{
			"step": strconv.Itoa(step), "chain": chain, "recipients": strconv.Itoa(len(plan.Targets)),
		}); err != nil {
			return err
		}
		if !plan.Last {
			if _, _, err := jobs.Enqueue(ctx, tx, alerts.EscalationSpec(a.ParentID, alertID, chain, step+1, now.Add(plan.Next))); err != nil {
				return err
			}
		}
		return nil
	})
}

// SendAlert is the send_alert job handler: one message to one recipient.
func (s *Service) SendAlert(ctx context.Context, j jobs.Job) error {
	if j.Payload.AlertID == nil || j.Payload.Step == nil || j.Payload.Recipient == "" {
		return jobs.Permanent(errors.New("send_alert without alert_id, step or recipient"))
	}
	q := db.New(s.Pool)
	a, err := q.GetAlert(ctx, *j.Payload.AlertID)
	if err != nil {
		return err
	}
	key := j.Payload.Recipient
	isAdmin := strings.HasPrefix(key, "admin:")
	// Family and follow-up steps stop once someone has acknowledged; the
	// first admin message always goes, for review.
	if closed(a.Status) && !(isAdmin && strings.HasSuffix(key, ":alert")) && !strings.HasSuffix(key, ":review") {
		return nil
	}
	p, err := q.GetParent(ctx, a.ParentID)
	if err != nil {
		return err
	}
	detail := ""
	if len(a.DetailEnc) > 0 {
		if detail, err = s.Keyring.DecryptString(a.DetailEnc, alerts.AADDetail); err != nil {
			return jobs.Permanent(fmt.Errorf("decrypt alert detail: %w", err))
		}
	}

	row := db.UpsertNotificationParams{AlertID: &a.ID, CallID: a.CallID}
	var to, lang string
	switch {
	case strings.HasPrefix(key, "member:"):
		id, err := uuid.Parse(strings.TrimPrefix(key, "member:"))
		if err != nil {
			return jobs.Permanent(err)
		}
		m, err := q.GetMember(ctx, id)
		if err != nil || m.AccountID != p.AccountID || m.WhatsappOptInAt == nil {
			s.Log.Warn("alert recipient not reachable", "alert_id", a.ID, "member_id", id)
			return nil
		}
		to, lang, row.FamilyMemberID, row.RecipientKind = m.PhoneE164, m.Language, &m.ID, alerts.RecipientFamily
	case key == "local_contact":
		if p.LocalContactPhoneE164 == nil {
			return nil
		}
		to, lang, row.RecipientKind = *p.LocalContactPhoneE164, p.Language, alerts.RecipientLocalContact
	case isAdmin:
		parts := strings.Split(key, ":")
		i, err := strconv.Atoi(parts[1])
		if err != nil || i < 0 || i >= len(s.AdminPhones) || len(parts) != 3 {
			return jobs.Permanent(fmt.Errorf("bad admin recipient %q", key))
		}
		to, lang, row.RecipientKind = s.AdminPhones[i], "en", alerts.RecipientAdmin
	default:
		return jobs.Permanent(fmt.Errorf("bad recipient %q", key))
	}

	msg := s.alertMessage(ctx, a, p, detail, key, lang)
	dk := "alert:" + a.ID.String() + ":" + strconv.Itoa(*j.Payload.Step) + ":" + key + ":" + msg.Template
	row.DedupeKey, row.Template = &dk, msg.Template
	return s.deliver(ctx, row, to, msg)
}

// alertMessage chooses the template and parameters for one recipient.
// Quotes appear only in red-flag and scam alerts to family members; the
// local contact gets the category only, and admins get no health text.
func (s *Service) alertMessage(ctx context.Context, a db.Alert, p db.Parent, detail, key, lang string) notify.TemplateMessage {
	name := clean(p.PreferredName)
	msg := notify.TemplateMessage{Language: templateLanguage(lang)}
	ack := []notify.QuickReply{{Title: notify.AckTitle, Payload: notify.AckPayload(a.ID.String())}}

	if strings.HasPrefix(key, "admin:") {
		purpose := key[strings.LastIndex(key, ":")+1:]
		what := strings.ToUpper(a.Type) + " " + a.Category
		switch purpose {
		case "unacknowledged":
			what = "UNACKNOWLEDGED " + what + ": no family member has responded"
		case "review":
			what = "Review needed: " + a.Category + " (not sent to family)"
		}
		msg.Template = notify.TplAdminAlert
		msg.Params = []string{clean(what), parentRef(p.ID), s.reviewURL()}
		return msg
	}

	family := strings.HasPrefix(key, "member:")
	switch {
	case a.Type == alerts.TypeMissedCalls:
		msg.Template = notify.TplMissedCalls
		msg.Params = []string{name, s.attemptsText(ctx, a, lang)}
	case a.Category == alerts.CategoryStopRequested:
		msg.Template = notify.TplCallsPaused
		msg.Params = []string{name}
	case a.Type == alerts.TypeEmergency:
		msg.Template, msg.Buttons = notify.TplAlertEmergency, ack
		said := categoryPhrase(a.Category, lang)
		if family && detail != "" {
			said = quote(detail)
		}
		msg.Params = []string{name, clean(said)}
	case a.Type == alerts.TypeScam:
		msg.Template, msg.Buttons = notify.TplAlertScam, ack
		msg.Params = []string{name, scamPhrase(a.Category, lang)}
	default:
		msg.Template, msg.Buttons = notify.TplAlertUrgent, ack
		msg.Params = []string{name, urgentReason(a.Category, detail, family, lang)}
	}
	return msg
}

func (s *Service) reviewURL() string {
	if s.ReviewURL != "" {
		return s.ReviewURL
	}
	return "/admin/review"
}

func (s *Service) attemptsText(ctx context.Context, a db.Alert, lang string) string {
	if a.SlotID == nil {
		return phrase(lang, "attempts_all")
	}
	cs, err := db.New(s.Pool).ListCallsForSlot(ctx, *a.SlotID)
	if err != nil || len(cs) == 0 {
		return phrase(lang, "attempts_all")
	}
	if len(cs) == 1 {
		return phrase(lang, "attempts_one")
	}
	return phrasef(lang, "attempts_n", len(cs))
}

func categoryPhrase(c, lang string) string {
	if _, ok := phrases["en"][c]; ok && !strings.Contains(c, ":") {
		return phrase(lang, c)
	}
	return phrase(lang, "other")
}

func scamPhrase(c, lang string) string {
	if _, ok := phrases["en"]["scam:"+c]; ok {
		return phrase(lang, "scam:"+c)
	}
	return phrase(lang, "scam:other")
}

func urgentReason(category, detail string, family bool, lang string) string {
	switch category {
	case "distressed":
		return phrase(lang, "distressed")
	case "missed_medicine":
		if family && detail != "" {
			return clean(phrasef(lang, "missed_medicine_fmt", detail))
		}
		return phrase(lang, "missed_medicine")
	}
	if family && detail != "" {
		return clean(phrasef(lang, "said_fmt", quote(detail)))
	}
	return categoryPhrase(category, lang)
}

// quote trims a quote for a message: bounded, without trailing punctuation
// that would sit awkwardly before the closing quote mark.
func quote(s string) string {
	return strings.TrimRight(strings.TrimSpace(alerts.Truncate(s, alerts.MaxQuote)), ".,;!?।")
}
