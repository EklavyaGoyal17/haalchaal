package outbound

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/alerts/rules"
	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/extract"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
)

// SendSummary is the send_summary job handler: the daily summary for one
// family member, or the "details shortly" message when the report needs a
// human first. Private notes are checked again here, right before sending.
func (s *Service) SendSummary(ctx context.Context, j jobs.Job) error {
	if j.Payload.CallID == nil || j.Payload.FamilyMemberID == nil {
		return jobs.Permanent(errors.New("send_summary without call_id or family_member_id"))
	}
	q := db.New(s.Pool)
	call, err := q.GetCall(ctx, *j.Payload.CallID)
	if err != nil {
		return err
	}
	st, err := q.GetCallGuardState(ctx, db.GetCallGuardStateParams{CallID: call.ID, Now: s.Clock.Now()})
	if err != nil {
		return err
	}
	if st.ParentStatus != "active" {
		// Paused or stopped: the calls_paused alert tells the family; no
		// more routine messages about the parent.
		return nil
	}
	if !slices.Contains(st.ConsentKinds, "share_with_family") {
		s.Log.Info("summary not sent: no share_with_family consent", "call_id", call.ID)
		return nil
	}
	p, err := q.GetParent(ctx, call.ParentID)
	if err != nil {
		return err
	}
	m, err := q.GetMember(ctx, *j.Payload.FamilyMemberID)
	if err != nil || m.AccountID != p.AccountID || m.WhatsappOptInAt == nil {
		return nil
	}
	rep, err := q.GetCallReport(ctx, call.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Permanent(fmt.Errorf("call %s has no report", call.ID))
	}
	if err != nil {
		return err
	}

	name := clean(p.PreferredName)
	msg := notify.TemplateMessage{Language: templateLanguage(m.Language)}
	if rep.SchemaVersion != extract.SchemaVersion {
		msg.Template, msg.Params = notify.TplCallPending, []string{name}
	} else {
		plain, err := s.Keyring.Decrypt(rep.ReportEnc, calls.AADReport)
		if err != nil {
			return jobs.Permanent(fmt.Errorf("decrypt report: %w", err))
		}
		r, err := extract.Parse(string(plain))
		if err != nil {
			return jobs.Permanent(fmt.Errorf("stored report invalid: %w", err))
		}
		summary, _, err := extract.SafeSummary(r, p.PreferredName, m.Language)
		if err != nil {
			return err
		}
		if note := s.watchNote(ctx, call.ID, m.Language); note != "" && r.AnsweredBy == "parent" {
			summary += " " + note
		}
		if len(r.RedFlags) > 0 || len(r.ScamSignals) > 0 {
			summary += " " + alertNote(m.Language)
		}
		if extract.LeaksPrivate(summary, r.PrivateNotes) { // belt and braces
			if summary, err = extract.FallbackSummary(r, p.PreferredName, m.Language); err != nil {
				return err
			}
		}
		if strings.TrimSpace(summary) == "" {
			msg.Template, msg.Params = notify.TplCallPending, []string{name}
		} else {
			msg.Template, msg.Params = notify.TplDailySummary, []string{name, clean(summary)}
		}
	}
	dk := "summary:" + call.ID.String() + ":" + m.ID.String()
	return s.deliver(ctx, db.UpsertNotificationParams{
		CallID: &call.ID, FamilyMemberID: &m.ID, RecipientKind: alerts.RecipientFamily, Template: msg.Template, DedupeKey: &dk,
	}, m.PhoneE164, msg)
}

// watchNote mentions watch-level trends raised by this call (SPEC §9: low
// mood or poor sleep three calls in a row appear in the next summary).
func (s *Service) watchNote(ctx context.Context, callID uuid.UUID, lang string) string {
	as, err := db.New(s.Pool).ListWatchAlertsForCall(ctx, &callID)
	if err != nil {
		return ""
	}
	var parts []string
	for _, a := range as {
		switch a.Category {
		case rules.CategoryLowMood, rules.CategoryPoorSleep:
			parts = append(parts, phrase(lang, "watch:"+a.Category))
		}
	}
	return strings.Join(parts, " ")
}

// AdminNotice is the admin_notice job handler: tell every admin about a
// system problem (invalid extraction, a failed dial, a dead-lettered job).
func (s *Service) AdminNotice(ctx context.Context, j jobs.Job) error {
	if j.Payload.Reason == "" {
		return jobs.Permanent(errors.New("admin_notice without reason"))
	}
	ref := "system"
	if j.Payload.ParentID != nil {
		ref = parentRef(j.Payload.ParentID)
	}
	what := "System notice: " + j.Payload.Reason
	var firstErr error
	for i, phone := range s.AdminPhones {
		dk := "admin_notice:" + j.Payload.Reason + ":" + j.Payload.Recipient + ":" + fmt.Sprint(i)
		err := s.deliver(ctx, db.UpsertNotificationParams{RecipientKind: alerts.RecipientAdmin, Template: notify.TplAdminAlert, DedupeKey: &dk},
			phone, notify.TemplateMessage{Template: notify.TplAdminAlert, Language: "en", Params: []string{clean(what), ref, s.reviewURL()}})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(s.AdminPhones) == 0 {
		s.Log.Error("admin notice with no ADMIN_ALERT_PHONES configured", "reason", j.Payload.Reason)
	}
	return firstErr
}

// DeadLetter is the worker's dead-letter hook: a safety job that failed for
// good is reported to admins (SPEC §5).
func (s *Service) DeadLetter(ctx context.Context, kind string, id int64, p jobs.Payload) {
	s.Log.Error("job dead-lettered", "job_id", id, "kind", kind)
	switch kind {
	case jobs.KindSendAlert, jobs.KindEscalateAlert, jobs.KindProcessCall, jobs.KindPlaceCall:
	default:
		return
	}
	spec := alerts.AdminNoticeSpec(alerts.NoticeDeadLetter+"_"+kind, p.ParentID, fmt.Sprint(id), s.Clock.Now())
	if _, _, err := jobs.Enqueue(ctx, s.Pool, spec); err != nil {
		s.Log.Error("enqueue dead-letter notice", "job_id", id, "error", err)
	}
}

// alertNote points the family to the separate alert about the same call, so
// a calm summary is never read as "all is well".
func alertNote(lang string) string { return phrase(lang, "alert_note") }
