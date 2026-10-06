//go:build integration

package calls_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice/fake"
)

const parentPhone = "+919811112222"

func ist(h, m int) time.Time {
	return time.Date(2026, 10, 6, h, m, 0, 0, time.FixedZone("IST", 19800)).UTC()
}

type env struct {
	pool   *pgxpool.Pool
	clk    *clock.Fake
	voice  *fake.Provider
	svc    *calls.Service
	worker *jobs.Worker
	fam    testdb.Created
}

func setup(t *testing.T, gate safety.Gate, fam testdb.Family) *env {
	t.Helper()
	pool := testdb.New(t)
	k, _ := crypto.GenerateKey()
	kr, _ := crypto.ParseKeyring("1:"+k, "1")
	e := &env{pool: pool, clk: clock.NewFake(ist(10, 0)), voice: fake.New("s")}
	e.svc = &calls.Service{Pool: pool, Clock: e.clk, Log: testdb.Log(), Keyring: kr, Voice: e.voice, Gate: gate,
		RetryOffsets: []time.Duration{15 * time.Minute, 45 * time.Minute}, TranscriptRetention: 365 * 24 * time.Hour}
	e.worker = &jobs.Worker{DB: pool, Clock: e.clk, Log: testdb.Log()}
	e.worker.Handle(jobs.KindPlaceCall, e.svc.PlaceCall)
	if fam.Phone == "" {
		fam.Phone = parentPhone
	}
	e.fam = testdb.CreateFamily(t, pool, fam)
	if _, err := (&scheduler.Scheduler{Pool: pool, Clock: e.clk, Log: testdb.Log(), Provider: "fake"}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

func okGate() safety.Gate {
	return safety.Gate{Env: config.EnvDev, CallsEnabled: true, DevAllowlist: []string{parentPhone}}
}

func (e *env) drain(t *testing.T) {
	t.Helper()
	for {
		ran, err := e.worker.RunOnce(context.Background(), "t")
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
}

func (e *env) calls(t *testing.T) []db.Call {
	t.Helper()
	var out []db.Call
	rows, err := e.pool.Query(context.Background(), `SELECT id, attempt_no, status, coalesce(end_reason, '') FROM calls WHERE parent_id = $1 ORDER BY attempt_no`, e.fam.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c db.Call
		var reason string
		if err := rows.Scan(&c.ID, &c.AttemptNo, &c.Status, &reason); err != nil {
			t.Fatal(err)
		}
		c.EndReason = &reason
		out = append(out, c)
	}
	return out
}

func TestGuardBlocksWhenCallsDisabled(t *testing.T) {
	e := setup(t, safety.Gate{Env: config.EnvDev, DevAllowlist: []string{parentPhone}}, testdb.Family{})
	e.drain(t)
	cs := e.calls(t)
	if len(cs) != 1 || cs[0].Status != calls.StatusCancelled || *cs[0].EndReason != "calls_disabled" {
		t.Fatalf("calls = %+v", cs)
	}
	if n := len(e.voice.Calls()); n != 0 {
		t.Fatalf("dialled %d times with CALLS_ENABLED=false", n)
	}
	var slot string
	_ = e.pool.QueryRow(context.Background(), `SELECT status FROM call_slots WHERE parent_id = $1`, e.fam.ParentID).Scan(&slot)
	if slot != "cancelled" {
		t.Fatalf("slot %s", slot)
	}
	var audits int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'call_blocked'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audit rows %d", audits)
	}
}

func TestGuardBlocksUnlistedNumberAndProdWithoutAck(t *testing.T) {
	for name, g := range map[string]safety.Gate{
		"not allowlisted": {Env: config.EnvDev, CallsEnabled: true},
		"prod no ack":     {Env: config.EnvProd, CallsEnabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, g, testdb.Family{})
			e.drain(t)
			if n := len(e.voice.Calls()); n != 0 {
				t.Fatalf("dialled %d times", n)
			}
		})
	}
}

func TestGuardBlocksWhenParentPausedAfterScheduling(t *testing.T) {
	e := setup(t, okGate(), testdb.Family{})
	if _, err := e.pool.Exec(context.Background(), `UPDATE parents SET status = 'paused' WHERE id = $1`, e.fam.ParentID); err != nil {
		t.Fatal(err)
	}
	e.drain(t)
	if cs := e.calls(t); cs[0].Status != calls.StatusCancelled || *cs[0].EndReason != calls.ReasonParentNotActive {
		t.Fatalf("calls = %+v", cs)
	}
}

func TestGuardBlocksOutsideWindowAtDialTime(t *testing.T) {
	e := setup(t, okGate(), testdb.Family{})
	e.clk.Set(ist(20, 5)) // the job ran late, after window_end
	e.drain(t)
	if cs := e.calls(t); cs[0].Status != calls.StatusCancelled || *cs[0].EndReason != calls.ReasonOutsideWindow {
		t.Fatalf("calls = %+v", cs)
	}
	if len(e.voice.Calls()) != 0 {
		t.Fatal("dialled outside the window")
	}
}

func TestPlaceCallRendersPromptAndNeverDialsTwice(t *testing.T) {
	e := setup(t, okGate(), testdb.Family{})
	e.drain(t)
	reqs := e.voice.Calls()
	if len(reqs) != 1 || reqs[0].To != parentPhone || reqs[0].Language != "hi" || reqs[0].SystemPrompt == "" {
		t.Fatalf("requests = %+v", reqs)
	}
	cs := e.calls(t)
	// Re-running the job (as after a reaped lock) must not dial again.
	id := cs[0].ID
	if err := e.svc.PlaceCall(context.Background(), jobs.Job{Payload: jobs.Payload{CallID: &id}}); err != nil {
		t.Fatal(err)
	}
	if len(e.voice.Calls()) != 1 {
		t.Fatal("dialled twice")
	}
}

func TestStartCallFailureFailsAttemptAndRetries(t *testing.T) {
	e := setup(t, okGate(), testdb.Family{})
	e.voice.FailNextStart(errors.New("caller number rejected"))
	e.drain(t)
	cs := e.calls(t)
	if len(cs) != 2 || cs[0].Status != calls.StatusFailed || *cs[0].EndReason != calls.ReasonStartFailed || cs[1].Status != calls.StatusScheduled {
		t.Fatalf("calls = %+v", cs)
	}
}

func TestSweepStaleDialing(t *testing.T) {
	e := setup(t, okGate(), testdb.Family{})
	e.drain(t)
	e.clk.Advance(calls.StaleAfter + time.Minute)
	n, err := e.svc.SweepStale(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("swept %d, err %v", n, err)
	}
	cs := e.calls(t)
	if cs[0].Status != calls.StatusFailed || *cs[0].EndReason != calls.ReasonNoEvents || len(cs) != 2 {
		t.Fatalf("calls = %+v", cs)
	}
}

func TestEventsForUnknownCallAndProviderMismatch(t *testing.T) {
	e := setup(t, okGate(), testdb.Family{})
	e.drain(t)
	ctx := context.Background()
	_, err := e.svc.ApplyEvent(ctx, "fake", voice.Event{EventID: "x1", ProviderCallID: "nope", Type: voice.EventRinging})
	if !errors.Is(err, calls.ErrUnknownCall) {
		t.Fatalf("err = %v", err)
	}
	// The unknown event was not recorded, so a later retry can still apply.
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM webhook_events`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d webhook events recorded", n)
	}
	cs := e.calls(t)
	// Our call id with a different provider name is not ours.
	_, err = e.svc.ApplyEvent(ctx, "other", voice.Event{EventID: "x2", CallID: cs[0].ID.String(), Type: voice.EventRinging})
	if !errors.Is(err, calls.ErrUnknownCall) {
		t.Fatalf("provider mismatch err = %v", err)
	}
	// Our call id with a provider call id that disagrees is rejected.
	_, err = e.svc.ApplyEvent(ctx, "fake", voice.Event{EventID: "x3", CallID: cs[0].ID.String(), ProviderCallID: "fake-other", Type: voice.EventRinging})
	if !errors.Is(err, calls.ErrUnknownCall) {
		t.Fatalf("id mismatch err = %v", err)
	}
	if _, err := e.svc.ApplyEvent(ctx, "fake", voice.Event{EventID: "x4", CallID: uuid.NewString(), Type: voice.EventRinging}); !errors.Is(err, calls.ErrUnknownCall) {
		t.Fatalf("random id err = %v", err)
	}
}

func TestTranscriptEncryptedAndProcessQueued(t *testing.T) {
	e := setup(t, okGate(), testdb.Family{})
	e.drain(t)
	ctx := context.Background()
	cs := e.calls(t)
	pcid, _ := e.voice.ProviderCallID(cs[0].ID.String())
	turns := []voice.Turn{{Speaker: "parent", Text: "seene mein dard", Offset: time.Second}}
	for i, ev := range []voice.Event{
		{EventID: "a", Type: voice.EventAnswered},
		{EventID: "b", Type: voice.EventCompleted, DurationSec: 100, CostPaise: 900},
		{EventID: "c", Type: voice.EventTranscriptReady, Transcript: turns},
	} {
		ev.ProviderCallID = pcid
		if res, err := e.svc.ApplyEvent(ctx, "fake", ev); err != nil || res != calls.EventApplied {
			t.Fatalf("event %d: %v %v", i, res, err)
		}
	}
	var raw []byte
	if err := e.pool.QueryRow(ctx, `SELECT turns_enc FROM transcripts WHERE call_id = $1`, cs[0].ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || containsBytes(raw, []byte("seene")) {
		t.Fatal("transcript stored in plaintext")
	}
	got, err := e.svc.Keyring.Decrypt(raw, calls.AADTranscript)
	if err != nil {
		t.Fatal(err)
	}
	back, _ := voice.UnmarshalTurns(got)
	if len(back) != 1 || back[0].Text != "seene mein dard" || back[0].Offset != time.Second {
		t.Fatalf("turns = %+v", back)
	}
	var jobsN int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'process_call'`).Scan(&jobsN)
	if jobsN != 1 {
		t.Fatalf("process_call jobs = %d", jobsN)
	}
	var dur, cost int64
	_ = e.pool.QueryRow(ctx, `SELECT duration_sec, cost_paise FROM calls WHERE id = $1`, cs[0].ID).Scan(&dur, &cost)
	if dur != 100 || cost != 900 {
		t.Fatalf("usage %d %d", dur, cost)
	}
}

func TestToolFindingDefaults(t *testing.T) {
	tests := []struct {
		tc       voice.ToolCall
		typ, cat string
	}{
		{voice.ToolCall{Tool: voice.ToolReportRedFlag, Args: map[string]string{"category": "fall", "severity": "urgent"}}, "urgent", "fall"},
		{voice.ToolCall{Tool: voice.ToolReportRedFlag, Args: map[string]string{"category": "fall"}}, "emergency", "fall"},
		{voice.ToolCall{Tool: voice.ToolReportRedFlag, Args: map[string]string{"category": "self_harm", "severity": "urgent"}}, "emergency", "self_harm"},
		{voice.ToolCall{Tool: voice.ToolReportRedFlag, Args: map[string]string{"category": "'; DROP TABLE"}}, "emergency", "other"},
		{voice.ToolCall{Tool: voice.ToolReportScam, Args: map[string]string{"pattern": "agency_threat"}}, "scam", "agency_threat"},
		{voice.ToolCall{Tool: voice.ToolReportScam, Args: map[string]string{"pattern": "weird"}}, "scam", "scam_other"},
		{voice.ToolCall{Tool: voice.ToolReportStopRequest}, "urgent", "stop_requested"},
	}
	for _, tt := range tests {
		f, err := calls.ToolFinding(tt.tc)
		if err != nil || f.Type != tt.typ || f.Category != tt.cat {
			t.Errorf("%+v -> %+v %v", tt.tc, f, err)
		}
	}
	if _, err := calls.ToolFinding(voice.ToolCall{Tool: "transfer_money"}); !errors.Is(err, calls.ErrUnknownTool) {
		t.Error("unknown tool accepted")
	}
}

func containsBytes(b, sub []byte) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}
