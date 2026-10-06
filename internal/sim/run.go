package sim

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/app"
	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/extract"
	"github.com/EklavyaGoyal17/haalchaal/internal/httpapi"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
	fakenotify "github.com/EklavyaGoyal17/haalchaal/internal/notify/fake"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice/fake"
)

// Result is what a run produced, for printing and for golden checks.
type Result struct {
	Scenario        string          `json:"scenario"`
	ParentID        uuid.UUID       `json:"parent_id"`
	Attempts        []string        `json:"attempts"`
	SlotStatus      string          `json:"slot_status"`
	ParentStatus    string          `json:"parent_status"`
	Alerts          []ExpectedAlert `json:"alerts"`
	Dials           int             `json:"dials"`
	ReplayUnchanged bool            `json:"replay_unchanged"`
	Report          json.RawMessage `json:"report,omitempty"`
	ReportVersion   string          `json:"report_version,omitempty"`
	NeedsReview     *bool           `json:"needs_review,omitempty"`
	Prompts         []string        `json:"-"` // rendered agent prompts, in dial order
	FollowUps       []string        `json:"follow_ups_saved,omitempty"`
	TranscriptsKept int             `json:"transcripts_kept"`
	Messages        []SentMessage   `json:"messages"`
	Acknowledged    []string        `json:"acknowledged,omitempty"` // categories of acknowledged alerts
}

// SentMessage is an outbound WhatsApp message, with the recipient as a role.
type SentMessage struct {
	To       string `json:"to"`
	Template string `json:"template"`
	Body     string `json:"body"`
}

// Runner holds what a run needs.
type Runner struct {
	Pool   *pgxpool.Pool
	Config config.Config
	Log    *slog.Logger
	// Start is the local date of the scenario day; zero means 6 Oct 2026.
	Start time.Time
	// Extractor overrides the configured extractor (safety tests).
	Extractor extract.Extractor
	// CallsDisabled runs with CALLS_ENABLED=false (safety tests).
	CallsDisabled bool
}

type posted struct {
	path string
	body []byte
	sig  string // signature header name
}

type run struct {
	r        *Runner
	sc       Scenario
	runID    string
	app      *app.App
	clk      *clock.Fake
	voice    *fake.Provider
	handler  http.Handler
	worker   *jobs.Worker
	parentID uuid.UUID
	tz       string
	posts    []posted
	seq      int
	msgr     *fakenotify.Messenger
	ph       simPhones
}

// Run replays one scenario on a fresh simulated family.
func (r *Runner) Run(ctx context.Context, sc Scenario) (Result, error) {
	if r.Config.AppEnv == config.EnvProd {
		return Result{}, errors.New("simcall never runs in prod")
	}
	rn := &run{r: r, sc: sc, runID: uuid.NewString()[:8]}
	start := r.Start
	if start.IsZero() {
		start = time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	}
	rn.clk = clock.NewFake(start.AddDate(0, 0, -len(sc.History)))
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	rn.voice = fake.New(fmt.Sprintf("%x", secret))

	phones := rn.phones()
	rn.ph = phones
	gate := safety.Gate{Env: config.EnvDev, CallsEnabled: !r.CallsDisabled, DevAllowlist: phones.all()}
	cfg := r.Config
	cfg.VoiceProvider, cfg.WhatsAppProvider = "fake", "fake"
	cfg.AdminAlertPhones = []string{phones.admin}
	rn.msgr = fakenotify.New(fmt.Sprintf("%x", secret)+"-wa", "verify", nil)
	a, err := app.New(cfg, r.Pool, r.Log, app.Options{Clock: rn.clk, Voice: rn.voice, Messenger: rn.msgr, Gate: &gate, Extractor: r.Extractor})
	if err != nil {
		return Result{}, err
	}
	rn.app = a
	if err := rn.createFamily(ctx, phones); err != nil {
		return Result{}, err
	}
	rn.worker = a.NewWorker()
	rn.handler = (&httpapi.Server{Log: r.Log, Voice: a.Voice, Calls: a.Calls, Messenger: a.Messenger, Outbound: a.Outbound}).Handler()

	days := append(append([]HistoryDay(nil), sc.History...), HistoryDay{Attempts: sc.Attempts})
	var slotID uuid.UUID
	for i, d := range days {
		date := start.AddDate(0, 0, i-len(sc.History))
		if slotID, err = rn.runDay(ctx, date, d.Attempts); err != nil {
			return Result{}, fmt.Errorf("day %d: %w", i+1, err)
		}
	}

	if err := rn.afterCalls(ctx, sc); err != nil {
		return Result{}, err
	}
	res, err := rn.result(ctx, slotID)
	if err != nil {
		return res, err
	}
	before, err := rn.snapshot(ctx)
	if err != nil {
		return res, err
	}
	for _, p := range rn.posts {
		if err := rn.postSigned(p, false); err != nil {
			return res, fmt.Errorf("replay: %w", err)
		}
	}
	if err := rn.drain(ctx); err != nil {
		return res, err
	}
	after, err := rn.snapshot(ctx)
	if err != nil {
		return res, err
	}
	res.ReplayUnchanged = before == after
	return res, nil
}

func (rn *run) runDay(ctx context.Context, date time.Time, attempts []Attempt) (uuid.UUID, error) {
	q := db.New(rn.r.Pool)
	w, err := rn.window(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	first, err := scheduler.InstantAt(date, w.Earliest(), rn.tz)
	if err != nil {
		return uuid.Nil, err
	}
	if first.After(rn.clk.Now()) {
		rn.clk.Set(first)
	}
	if _, err := rn.app.Scheduler.Tick(ctx); err != nil {
		return uuid.Nil, err
	}
	var slotID uuid.UUID
	if err := rn.r.Pool.QueryRow(ctx, `SELECT id FROM call_slots WHERE parent_id = $1 AND local_date = $2`,
		rn.parentID, pgtype.Date{Time: date, Valid: true}).Scan(&slotID); err != nil {
		return uuid.Nil, fmt.Errorf("no slot created: %w", err)
	}

	for i, att := range attempts {
		if err := rn.drain(ctx); err != nil {
			return slotID, err
		}
		cs, err := q.ListCallsForSlot(ctx, slotID)
		if err != nil {
			return slotID, err
		}
		if len(cs) <= i {
			break
		}
		c := cs[i]
		if c.Status == calls.StatusScheduled && c.ScheduledFor.After(rn.clk.Now()) {
			rn.clk.Set(c.ScheduledFor)
			if err := rn.drain(ctx); err != nil {
				return slotID, err
			}
			if c, err = q.GetCall(ctx, c.ID); err != nil {
				return slotID, err
			}
		}
		if c.Status != calls.StatusDialing {
			break // a guard blocked it
		}
		if err := rn.playAttempt(c, att); err != nil {
			return slotID, err
		}
	}
	return slotID, rn.drain(ctx)
}

type step struct {
	after int
	ev    *EventSpec
	tool  *ToolSpec
}

func (rn *run) playAttempt(c db.Call, att Attempt) error {
	if c.ProviderCallID == nil {
		return errors.New("dialing call has no provider id")
	}
	pcid := *c.ProviderCallID
	dialAt := rn.clk.Now()
	var steps []step
	for i := range att.Events {
		steps = append(steps, step{after: att.Events[i].AfterSec, ev: &att.Events[i]})
	}
	for i := range att.ToolCalls {
		steps = append(steps, step{after: att.ToolCalls[i].AfterSec, tool: &att.ToolCalls[i]})
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].after < steps[j].after })

	var turns []fake.WireTurn
	for _, t := range att.Transcript {
		turns = append(turns, fake.WireTurn(t))
	}
	lastAfter := 0
	for _, s := range steps {
		rn.clk.Set(dialAt.Add(time.Duration(s.after) * time.Second))
		lastAfter = s.after
		if s.tool != nil {
			body, _ := json.Marshal(fake.WireTool{EventID: rn.eventID(), ProviderCallID: pcid, CallID: c.ID.String(), Args: s.tool.Args})
			if err := rn.post("/v1/voice/tools/"+s.tool.Tool, body, true); err != nil {
				return err
			}
			continue
		}
		we := fake.WireEvent{EventID: rn.eventID(), ProviderCallID: pcid, CallID: c.ID.String(), Type: s.ev.Type, At: rn.clk.Now(),
			DurationSec: s.ev.DurationSec, CostPaise: s.ev.CostPaise}
		if s.ev.Type == "completed" && att.TranscriptOnCompleted {
			we.Transcript = turns
		}
		if err := rn.postEvents(we); err != nil {
			return err
		}
	}
	if len(turns) > 0 && !att.TranscriptOnCompleted {
		rn.clk.Set(dialAt.Add(time.Duration(lastAfter+20) * time.Second))
		if err := rn.postEvents(fake.WireEvent{EventID: rn.eventID(), ProviderCallID: pcid, CallID: c.ID.String(),
			Type: "transcript_ready", At: rn.clk.Now(), Transcript: turns}); err != nil {
			return err
		}
	}
	return nil
}

func (rn *run) eventID() string {
	rn.seq++
	return fmt.Sprintf("sim-%s-%d", rn.runID, rn.seq)
}

func (rn *run) postEvents(evs ...fake.WireEvent) error {
	body, err := json.Marshal(map[string]any{"events": evs})
	if err != nil {
		return err
	}
	return rn.post("/v1/webhooks/voice/fake", body, true)
}

func (rn *run) post(path string, body []byte, record bool) error {
	return rn.postSigned(posted{path: path, body: body, sig: fake.SignatureHeader}, record)
}

func (rn *run) postSigned(p posted, record bool) error {
	req := httptest.NewRequest(http.MethodPost, p.path, bytes.NewReader(p.body))
	if p.sig == notify.MetaSignatureHeader {
		req.Header.Set(p.sig, rn.msgr.Sign(p.body))
	} else {
		req.Header.Set(p.sig, rn.voice.Sign(p.body))
	}
	rec := httptest.NewRecorder()
	rn.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return fmt.Errorf("POST %s: %d %s", p.path, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if record {
		rn.posts = append(rn.posts, p)
	}
	return nil
}

// afterCalls runs queued jobs (escalation steps, summaries) forward in time,
// pressing acknowledgement buttons when the scenario says so.
func (rn *run) afterCalls(ctx context.Context, sc Scenario) error {
	start := rn.clk.Now()
	horizon := sc.RunMinutes
	if horizon == 0 {
		horizon = 180
	}
	acks := append([]AckSpec(nil), sc.Acks...)
	sort.SliceStable(acks, func(i, j int) bool { return acks[i].AfterMin < acks[j].AfterMin })
	for _, a := range acks {
		if err := rn.runUntil(ctx, start.Add(time.Duration(a.AfterMin)*time.Minute)); err != nil {
			return err
		}
		if err := rn.ack(ctx, a); err != nil {
			return err
		}
	}
	return rn.runUntil(ctx, start.Add(time.Duration(horizon)*time.Minute))
}

// runUntil advances the clock job by job, never past until.
func (rn *run) runUntil(ctx context.Context, until time.Time) error {
	for range 500 {
		if err := rn.drain(ctx); err != nil {
			return err
		}
		var next *time.Time
		if err := rn.r.Pool.QueryRow(ctx, `SELECT min(run_at) FROM jobs WHERE status = 'queued' AND payload->>'parent_id' = $1::text`,
			rn.parentID).Scan(&next); err != nil {
			return err
		}
		if next == nil || next.After(until) {
			if until.After(rn.clk.Now()) {
				rn.clk.Set(until)
			}
			return rn.drain(ctx)
		}
		if next.After(rn.clk.Now()) {
			rn.clk.Set(*next)
		}
	}
	return errors.New("jobs kept coming")
}

func (rn *run) ack(ctx context.Context, a AckSpec) error {
	var alertID uuid.UUID
	if err := rn.r.Pool.QueryRow(ctx, `SELECT id FROM alerts WHERE parent_id = $1 AND category = $2 ORDER BY created_at DESC LIMIT 1`,
		rn.parentID, a.Category).Scan(&alertID); err != nil {
		return fmt.Errorf("ack: no %s alert: %w", a.Category, err)
	}
	from := rn.ph.stranger
	if a.Phone != "stranger" {
		if a.Member < 1 || a.Member > len(rn.ph.members) {
			return fmt.Errorf("ack: no member %d", a.Member)
		}
		from = rn.ph.members[a.Member-1]
	}
	body := notify.MetaInboundBody(notify.InboundEvent{Kind: notify.KindButton, From: from, Payload: notify.AckPayload(alertID.String()), MessageID: "wamid.in." + rn.eventID()})
	return rn.postSigned(posted{path: "/v1/webhooks/whatsapp", body: body, sig: notify.MetaSignatureHeader}, true)
}

// drain runs every job that is due now.
func (rn *run) drain(ctx context.Context) error {
	for range 1000 {
		ran, err := rn.worker.RunOnce(ctx, "sim")
		if err != nil {
			return err
		}
		if !ran {
			return nil
		}
	}
	return errors.New("job queue did not drain")
}

func (rn *run) window(ctx context.Context) (scheduler.Window, error) {
	p, err := db.New(rn.r.Pool).GetParent(ctx, rn.parentID)
	if err != nil {
		return scheduler.Window{}, err
	}
	return scheduler.WindowFromPG(p.CallTimeLocal, p.WindowStart, p.WindowEnd)
}

func (rn *run) result(ctx context.Context, slotID uuid.UUID) (Result, error) {
	q := db.New(rn.r.Pool)
	res := Result{Scenario: rn.sc.Name, ParentID: rn.parentID, Dials: len(rn.voice.Calls()), Alerts: []ExpectedAlert{}}
	cs, err := q.ListCallsForSlot(ctx, slotID)
	if err != nil {
		return res, err
	}
	for _, c := range cs {
		res.Attempts = append(res.Attempts, c.Status)
		if _, err := q.GetTranscript(ctx, c.ID); err == nil {
			res.TranscriptsKept++
		}
		rep, err := q.GetCallReport(ctx, c.ID)
		if err != nil {
			continue
		}
		plain, err := rn.app.Keyring.Decrypt(rep.ReportEnc, calls.AADReport)
		if err != nil {
			return res, err
		}
		res.Report, res.ReportVersion = plain, rep.SchemaVersion
		nr := rep.NeedsReview
		res.NeedsReview = &nr
	}
	for _, c := range rn.voice.Calls() {
		res.Prompts = append(res.Prompts, c.SystemPrompt)
	}
	res.Messages = []SentMessage{}
	for _, m := range rn.msgr.Sent() {
		res.Messages = append(res.Messages, SentMessage{To: rn.ph.label(m.To), Template: m.Template, Body: m.Body})
	}

	mems, err := q.ListActiveFollowUps(ctx, db.ListActiveFollowUpsParams{ParentID: rn.parentID, Now: ptr(rn.clk.Now()), MaxItems: 10})
	if err != nil {
		return res, err
	}
	for _, m := range mems {
		if m.SourceCallID == nil {
			continue
		}
		f, err := rn.app.Keyring.DecryptString(m.ContentEnc, calls.AADMemory)
		if err != nil {
			return res, err
		}
		res.FollowUps = append(res.FollowUps, f)
	}
	slot, err := q.GetSlot(ctx, slotID)
	if err != nil {
		return res, err
	}
	res.SlotStatus = slot.Status
	p, err := q.GetParent(ctx, rn.parentID)
	if err != nil {
		return res, err
	}
	res.ParentStatus = p.Status
	as, err := q.ListAlertsForParent(ctx, rn.parentID)
	if err != nil {
		return res, err
	}
	for _, a := range as {
		res.Alerts = append(res.Alerts, ExpectedAlert{Type: a.Type, Category: a.Category, Source: a.Source})
		if a.Status == "acknowledged" {
			res.Acknowledged = append(res.Acknowledged, a.Category)
		}
	}
	return res, nil
}

// snapshot captures the state a replay must not change.
func (rn *run) snapshot(ctx context.Context) (string, error) {
	var s string
	err := rn.r.Pool.QueryRow(ctx, `
		SELECT concat_ws('|',
		  (SELECT string_agg(id::text || status || coalesce(end_reason, '') || coalesce(provider_call_id, ''), ',' ORDER BY id) FROM calls WHERE parent_id = $1),
		  (SELECT string_agg(id::text || status, ',' ORDER BY id) FROM call_slots WHERE parent_id = $1),
		  (SELECT string_agg(id::text || type || status || escalation_step, ',' ORDER BY id) FROM alerts WHERE parent_id = $1),
		  (SELECT count(*) FROM jobs WHERE payload->>'parent_id' = $1::text),
		  (SELECT count(*) FROM transcripts t JOIN calls c ON c.id = t.call_id WHERE c.parent_id = $1),
		  (SELECT count(*) FROM call_reports r JOIN calls c ON c.id = r.call_id WHERE c.parent_id = $1),
		  (SELECT count(*) FROM notifications n LEFT JOIN alerts a ON a.id = n.alert_id LEFT JOIN calls c ON c.id = n.call_id
		     WHERE a.parent_id = $1 OR c.parent_id = $1),
		  (SELECT status FROM parents WHERE id = $1))`, rn.parentID).Scan(&s)
	return s, err
}

// Check compares a result with a scenario's expectations and returns every
// mismatch.
func Check(exp Expected, res Result) []string {
	var out []string
	if exp.Attempts != nil && !slices.Equal(exp.Attempts, res.Attempts) {
		out = append(out, fmt.Sprintf("attempts = %v, want %v", res.Attempts, exp.Attempts))
	}
	if exp.SlotStatus != "" && exp.SlotStatus != res.SlotStatus {
		out = append(out, fmt.Sprintf("slot status = %s, want %s", res.SlotStatus, exp.SlotStatus))
	}
	if exp.ParentStatus != "" && exp.ParentStatus != res.ParentStatus {
		out = append(out, fmt.Sprintf("parent status = %s, want %s", res.ParentStatus, exp.ParentStatus))
	}
	key := func(a ExpectedAlert) string { return a.Type + "/" + a.Category }
	var got, want []string
	for _, a := range res.Alerts {
		got = append(got, key(a))
	}
	for _, a := range exp.Alerts {
		want = append(want, key(a))
		if a.Source != "" {
			found := false
			for _, g := range res.Alerts {
				if key(g) == key(a) && g.Source == a.Source {
					found = true
				}
			}
			if !found {
				out = append(out, fmt.Sprintf("alert %s: source is not %s", key(a), a.Source))
			}
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		out = append(out, fmt.Sprintf("alerts = %v, want %v", got, want))
	}
	if !res.ReplayUnchanged {
		out = append(out, "replaying the same events changed state")
	}
	if exp.NeedsReview != nil && (res.NeedsReview == nil || *res.NeedsReview != *exp.NeedsReview) {
		out = append(out, fmt.Sprintf("needs_review = %v, want %v", deref(res.NeedsReview), *exp.NeedsReview))
	}
	if exp.Report != nil {
		out = append(out, checkReport(*exp.Report, res.Report)...)
	}
	if exp.TranscriptDeleted && res.TranscriptsKept > 0 {
		out = append(out, fmt.Sprintf("%d transcripts kept, want none", res.TranscriptsKept))
	}
	var summary struct {
		FamilySummary string `json:"family_summary"`
	}
	_ = json.Unmarshal(res.Report, &summary)
	for _, bad := range exp.MustNotSend {
		if strings.Contains(strings.ToLower(summary.FamilySummary), strings.ToLower(bad)) {
			out = append(out, fmt.Sprintf("family summary contains %q", bad))
		}
		for _, m := range res.Messages {
			if strings.Contains(strings.ToLower(m.Body), strings.ToLower(bad)) {
				out = append(out, fmt.Sprintf("%s message to %s contains %q", m.Template, m.To, bad))
			}
		}
	}
	if exp.NoMessages && len(res.Messages) > 0 {
		out = append(out, fmt.Sprintf("%d messages sent, want none", len(res.Messages)))
	}
	if exp.Messages != nil {
		out = append(out, checkMessages(exp.Messages, res.Messages)...)
	}
	if exp.Acknowledged != nil && !slices.Equal(exp.Acknowledged, res.Acknowledged) {
		out = append(out, fmt.Sprintf("acknowledged = %v, want %v", res.Acknowledged, exp.Acknowledged))
	}
	return out
}

// checkMessages matches every expected message to a distinct sent one, in
// order, and reports extra messages.
func checkMessages(exp []ExpectedMessage, got []SentMessage) []string {
	var out []string
	if len(exp) != len(got) {
		var g []string
		for _, m := range got {
			g = append(g, m.To+"/"+m.Template)
		}
		out = append(out, fmt.Sprintf("%d messages sent, want %d: %v", len(got), len(exp), g))
	}
	for i, e := range exp {
		if i >= len(got) {
			break
		}
		m := got[i]
		if m.To != e.To || m.Template != e.Template {
			out = append(out, fmt.Sprintf("message %d = %s/%s, want %s/%s", i+1, m.To, m.Template, e.To, e.Template))
			continue
		}
		for _, c := range e.Contains {
			if !strings.Contains(m.Body, c) {
				out = append(out, fmt.Sprintf("message %d (%s) does not contain %q: %s", i+1, m.Template, c, m.Body))
			}
		}
	}
	return out
}

// checkReport compares each key of the expected subset with the stored report.
func checkReport(exp, got json.RawMessage) []string {
	if len(got) == 0 {
		return []string{"no report was stored"}
	}
	var want, have map[string]json.RawMessage
	if err := json.Unmarshal(exp, &want); err != nil {
		return []string{"expected report is not an object: " + err.Error()}
	}
	if err := json.Unmarshal(got, &have); err != nil {
		return []string{"stored report is not an object: " + err.Error()}
	}
	var out []string
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if canon(want[k]) != canon(have[k]) {
			out = append(out, fmt.Sprintf("report.%s = %s, want %s", k, canon(have[k]), canon(want[k])))
		}
	}
	return out
}

func canon(b json.RawMessage) string {
	if len(b) == 0 {
		return "<missing>"
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

func ptr[T any](v T) *T { return &v }

// randomPhone returns a +91 9xxxxxxxxx number for a simulated person.
func randomPhone() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
	return fmt.Sprintf("+919%09d", n.Int64())
}
