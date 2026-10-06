package calls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/alerts/rules"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/extract"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// AADReport binds call reports to their column.
const AADReport = "call_reports.report_enc"

// SchemaInvalid marks a stored report whose model output never validated;
// report_enc then holds {"invalid_raw": "..."} for admin review.
const SchemaInvalid = "invalid"

// ConsentScriptVersion identifies the first-call consent wording.
const ConsentScriptVersion = "agent_system.tmpl/v1"

// ErrCallNotFinished makes process_call retry until the call has ended.
var ErrCallNotFinished = errors.New("call has not ended yet")

// processed is everything decided outside the transaction.
type processed struct {
	report   extract.Report // merged, safe to store
	invalid  *extract.InvalidOutputError
	findings []rules.Finding
	model    string
}

// ProcessCall is the process_call job handler (SPEC §8): extract a report,
// merge keyword hits, raise alerts, save follow-ups, settle the slot and
// queue the family summaries. It is idempotent: the report row is written
// in the same transaction as everything else, and a second run stops there.
func (s *Service) ProcessCall(ctx context.Context, j jobs.Job) error {
	if j.Payload.CallID == nil {
		return jobs.Permanent(errors.New("process_call without call_id"))
	}
	callID := *j.Payload.CallID
	q := db.New(s.Pool)
	call, err := q.GetCall(ctx, callID)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Permanent(fmt.Errorf("call %s not found", callID))
	}
	if err != nil {
		return err
	}
	if !Terminal(call.Status) {
		return ErrCallNotFinished
	}
	if done, err := q.CallReportExists(ctx, callID); err != nil || done {
		return err
	}
	tr, err := q.GetTranscript(ctx, callID)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Permanent(fmt.Errorf("call %s has no transcript", callID))
	}
	if err != nil {
		return err
	}
	raw, err := s.Keyring.Decrypt(tr.TurnsEnc, AADTranscript)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("decrypt transcript: %w", err))
	}
	turns, err := voice.UnmarshalTurns(raw)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("decode transcript: %w", err))
	}
	now := s.Clock.Now()
	cc, parent, err := LoadContext(ctx, q, s.Keyring, call.ParentID, now)
	if err != nil {
		return err
	}
	members, err := q.ListFamilyMembers(ctx, parent.AccountID)
	if err != nil {
		return err
	}
	familyLang := "en"
	if len(members) > 0 {
		familyLang = members[0].Language
	}
	in := extract.Input{Transcript: turns, PreferredName: parent.PreferredName, FollowUps: cc.FollowUps, FamilyLanguage: familyLang}
	for _, m := range cc.MedicinesDue {
		in.MedicinesDue = append(in.MedicinesDue, m.Name+" ("+m.Timing+")")
	}

	p := processed{model: s.Extractor.Name()}
	report, err := s.Extractor.Extract(ctx, in)
	hits := alerts.DefaultDetector.Detect(extract.ParentTexts(turns))
	if inv, ok := extract.IsInvalidOutput(err); ok {
		p.invalid = inv
		// No model report: alert on keyword hits alone. Safety wins.
		report = extract.Report{}
		p.findings = rules.Evaluate(report, hits, nil)
	} else if errors.Is(err, extract.ErrNoTranscript) {
		return jobs.Permanent(err)
	} else if err != nil {
		return fmt.Errorf("extract: %w", err)
	} else {
		history, err := s.recentReports(ctx, q, call, now)
		if err != nil {
			return err
		}
		p.findings = rules.Evaluate(report, hits, history)
		p.report = mergeKeywordFlags(report, hits)
		if p.report.AnsweredBy == "someone_else" {
			stripHealthData(&p.report)
		}
		summary, fallback, err := extract.SafeSummary(p.report, parent.PreferredName, familyLang)
		if err != nil {
			return err
		}
		if fallback {
			s.Log.Warn("family summary replaced by fallback", "call_id", callID)
		}
		p.report.FamilySummary = summary
	}

	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return s.saveProcessed(ctx, tx, call, parent.AccountID, p, hits, now)
	})
}

func (s *Service) saveProcessed(ctx context.Context, tx pgx.Tx, call db.Call, accountID uuid.UUID, p processed, hits []alerts.Hit, now time.Time) error {
	q := db.New(tx)
	var body []byte
	var err error
	version, needsReview, conf := extract.SchemaVersion, p.report.NeedsReview() || len(hits) > 0, float32(p.report.Confidence)
	if p.invalid != nil {
		version, needsReview, conf = SchemaInvalid, true, 0
		body, err = json.Marshal(map[string]string{"invalid_raw": p.invalid.Raw, "error": p.invalid.Err.Error()})
	} else {
		body, err = json.Marshal(p.report)
	}
	if err != nil {
		return err
	}
	enc, err := s.Keyring.Encrypt(body, AADReport)
	if err != nil {
		return err
	}
	redFlags := 0
	for _, f := range p.findings {
		if f.Type == alerts.TypeEmergency || (f.Type == alerts.TypeUrgent && f.Source != alerts.SourceRule && f.Category != alerts.CategoryStopRequested) {
			redFlags++
		}
	}
	n, err := q.InsertCallReport(ctx, db.InsertCallReportParams{
		CallID: call.ID, SchemaVersion: version, ReportEnc: enc, RedFlagCount: int32(redFlags),
		Confidence: conf, NeedsReview: needsReview, Model: p.model, CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("insert report: %w", err)
	}
	if n == 0 {
		return nil // processed by an earlier run
	}

	for _, f := range p.findings {
		if _, err := alerts.RaiseForCall(ctx, tx, s.Keyring, alerts.Finding{
			ParentID: call.ParentID, CallID: call.ID, Type: f.Type, Category: f.Category, Source: f.Source, Detail: f.Detail,
		}, now); err != nil {
			return err
		}
	}
	if p.invalid != nil {
		s.Log.Error("extraction output invalid; report needs review", "call_id", call.ID)
		if _, _, err := jobs.Enqueue(ctx, tx, alerts.AdminNoticeSpec(alerts.NoticeExtractionInvalid, &call.ParentID, call.ID.String(), now)); err != nil {
			return err
		}
	}
	r := p.report

	if r.CallPreferences.StopRequested {
		if err := PauseParent(ctx, tx, call.ParentID, "paused", ReasonStopRequested, "system", now); err != nil {
			return err
		}
	}
	if p.invalid == nil && r.AnsweredBy == "parent" {
		exp := now.Add(s.FollowUpTTL)
		for _, f := range r.FollowUps {
			enc, err := s.Keyring.EncryptString(f, AADMemory)
			if err != nil {
				return err
			}
			if _, err := q.InsertMemory(ctx, db.InsertMemoryParams{
				ParentID: call.ParentID, Kind: "follow_up", ContentEnc: enc, SourceCallID: &call.ID, ExpiresAt: &exp,
			}); err != nil {
				return err
			}
		}
	}

	usable := p.invalid != nil || !r.Unusable()
	if call.Status == StatusCompleted && usable && p.invalid == nil && r.AnsweredBy == "parent" {
		if err := s.firstCallConsent(ctx, tx, call, r, now); err != nil {
			return err
		}
	}
	if r.AnsweredBy == "someone_else" && len(p.findings) == 0 {
		// Nothing safety-relevant: keep no record of what was said.
		if _, err := q.DeleteTranscript(ctx, call.ID); err != nil {
			return err
		}
	}

	if call.Status != StatusCompleted {
		return nil // a dropped call: retries were already decided
	}
	if !usable {
		ended := now
		if call.EndedAt != nil {
			ended = *call.EndedAt
		}
		return s.afterUnanswered(ctx, tx, call, ended)
	}
	if _, err := q.SetSlotStatus(ctx, db.SetSlotStatusParams{ID: call.SlotID, Status: "completed"}); err != nil {
		return err
	}
	members, err := q.ListOptedInMembers(ctx, accountID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if _, _, err := jobs.Enqueue(ctx, tx, SummarySpec(call.ParentID, call.ID, m.ID, now)); err != nil {
			return err
		}
	}
	return nil
}

// SummarySpec is the send_summary job for one family member.
func SummarySpec(parentID, callID, memberID uuid.UUID, runAt time.Time) jobs.Spec {
	return jobs.Spec{
		Kind:      jobs.KindSendSummary,
		DedupeKey: "send_summary:" + callID.String() + ":" + memberID.String(),
		Payload:   jobs.Payload{ParentID: &parentID, CallID: &callID, FamilyMemberID: &memberID},
		RunAt:     runAt,
	}
}

// firstCallConsent marks the first call done and, when the parent stayed on
// and did not ask to stop, records their voice confirmation of consent.
func (s *Service) firstCallConsent(ctx context.Context, tx pgx.Tx, call db.Call, r extract.Report, now time.Time) error {
	q := db.New(tx)
	p, err := q.GetParent(ctx, call.ParentID)
	if err != nil || p.FirstCallDoneAt != nil {
		return err
	}
	if err := q.MarkFirstCallDone(ctx, db.MarkFirstCallDoneParams{ID: call.ParentID, At: &now}); err != nil {
		return err
	}
	if r.CallPreferences.StopRequested {
		return nil
	}
	ev := call.ID.String()
	_, err = q.RecordConsent(ctx, db.RecordConsentParams{
		ParentID: call.ParentID, Kind: "calls", GivenBy: "parent", Method: "first_call_voice",
		TextVersion: ConsentScriptVersion, EvidenceRef: &ev, GivenAt: now,
	})
	return err
}

func (s *Service) recentReports(ctx context.Context, q *db.Queries, call db.Call, now time.Time) ([]extract.Report, error) {
	rows, err := q.ListRecentReports(ctx, db.ListRecentReportsParams{ParentID: call.ParentID, ExcludeCallID: call.ID, Before: now, MaxItems: 2})
	if err != nil {
		return nil, err
	}
	var out []extract.Report
	for _, row := range rows {
		b, err := s.Keyring.Decrypt(row.ReportEnc, AADReport)
		if err != nil {
			return nil, fmt.Errorf("decrypt report: %w", err)
		}
		r, err := extract.Parse(string(b))
		if err != nil {
			continue
		}
		if r.AnsweredBy != "parent" || r.Unusable() {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// mergeKeywordFlags adds keyword red-flag hits the model missed to the
// report, so the stored report shows every reason an alert was raised.
func mergeKeywordFlags(r extract.Report, hits []alerts.Hit) extract.Report {
	have := map[string]bool{}
	for _, f := range r.RedFlags {
		have[f.Category] = true
	}
	for _, h := range hits {
		if h.Category == "scam" || have[h.Category] {
			continue
		}
		sev := "emergency"
		if alerts.RedFlagSeverity(h.Category) == alerts.TypeUrgent {
			sev = "urgent"
		}
		r.RedFlags = append(r.RedFlags, extract.RedFlag{Category: h.Category, Severity: sev, Quote: h.Quote})
		have[h.Category] = true
	}
	return r
}

// stripHealthData clears everything about the parent's health when someone
// else answered. Red flags and scam signals stay: safety wins.
func stripHealthData(r *extract.Report) {
	r.Mood, r.Sleep, r.Appetite = "unknown", "unknown", "unknown"
	r.Medicines, r.Pain = []extract.MedicineTaken{}, []extract.Pain{}
	r.FollowUps, r.PrivateNotes, r.MessagesForFamily = []string{}, []string{}, []string{}
	r.FamilySummary = ""
}
