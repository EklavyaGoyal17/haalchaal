//go:build integration

package vapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	fakeextract "github.com/EklavyaGoyal17/haalchaal/internal/extract/fake"
	"github.com/EklavyaGoyal17/haalchaal/internal/httpapi"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice/vapi"
)

const (
	secret      = "integration-secret-0123456789abcdef"
	parentPhone = "+919811113333"
)

// stubAPI stands in for api.vapi.ai and records what was sent.
type stubAPI struct {
	mu      sync.Mutex
	creates []map[string]any
	deletes []string
}

func (s *stubAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/call":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.creates = append(s.creates, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"vapi-call-1","status":"queued"}`)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/call/"):
		s.deletes = append(s.deletes, strings.TrimPrefix(r.URL.Path, "/call/"))
		_, _ = io.WriteString(w, `{}`)
	default:
		http.NotFound(w, r)
	}
}

func TestVapiCallEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool := testdb.New(t)
	k, _ := crypto.GenerateKey()
	kr, _ := crypto.ParseKeyring("1:"+k, "1")
	clk := clock.NewFake(time.Date(2026, 10, 6, 4, 30, 0, 0, time.UTC)) // 10:00 IST

	api := &stubAPI{}
	apiSrv := httptest.NewServer(api)
	defer apiSrv.Close()
	p, err := vapi.New(vapi.Options{APIKey: "k", WebhookSecret: secret, AssistantID: "asst", PhoneNumberID: "pn", BaseURL: apiSrv.URL, CostPaisePerUSD: 8500})
	if err != nil {
		t.Fatal(err)
	}
	svc := &calls.Service{Pool: pool, Clock: clk, Log: testdb.Log(), Keyring: kr, Voice: p, Extractor: fakeextract.Extractor{},
		Gate:         safety.Gate{Env: config.EnvDev, CallsEnabled: true, DevAllowlist: []string{parentPhone}},
		RetryOffsets: []time.Duration{15 * time.Minute, 45 * time.Minute}, TranscriptRetention: 365 * 24 * time.Hour}
	worker := &jobs.Worker{DB: pool, Clock: clk, Log: testdb.Log()}
	worker.Handle(jobs.KindPlaceCall, svc.PlaceCall)
	worker.Handle(jobs.KindProcessCall, svc.ProcessCall)
	drain := func() {
		t.Helper()
		for {
			ran, err := worker.RunOnce(ctx, "t")
			if err != nil {
				t.Fatal(err)
			}
			if !ran {
				return
			}
		}
	}

	fam := testdb.CreateFamily(t, pool, testdb.Family{Phone: parentPhone})
	if _, err := (&scheduler.Scheduler{Pool: pool, Clock: clk, Log: testdb.Log(), Provider: p.Name()}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	drain()

	if len(api.creates) != 1 {
		t.Fatalf("Vapi create calls: %d", len(api.creates))
	}
	var callID, status, providerID string
	if err := pool.QueryRow(ctx, `SELECT id::text, status, coalesce(provider_call_id,'') FROM calls WHERE parent_id = $1`, fam.ParentID).Scan(&callID, &status, &providerID); err != nil {
		t.Fatal(err)
	}
	if status != calls.StatusDialing || providerID != "vapi-call-1" {
		t.Fatalf("call %s %s", status, providerID)
	}
	ov := api.creates[0]["assistantOverrides"].(map[string]any)
	if ov["metadata"].(map[string]any)[vapi.MetadataCallID] != callID {
		t.Fatalf("metadata %v", ov["metadata"])
	}
	if prompt, _ := ov["variableValues"].(map[string]any)["system_prompt"].(string); !strings.Contains(prompt, "AI") {
		t.Fatalf("rendered prompt not sent: %q", prompt)
	}

	srv := httptest.NewServer((&httpapi.Server{Log: testdb.Log(), Voice: p, Calls: svc}).Handler())
	defer srv.Close()
	send := func(path, auth, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	call := `"call":{"id":"vapi-call-1","assistantOverrides":{"metadata":{"haalchaal_call_id":"` + callID + `"}}}`
	hook := "/v1/webhooks/voice/vapi"

	if code, _ := send(hook, "wrong-secret-wrong-secret-wrong-secret", `{"message":{"type":"status-update","status":"ringing",`+call+`}}`); code != http.StatusUnauthorized {
		t.Fatalf("bad secret: %d", code)
	}
	for _, st := range []string{"ringing", "in-progress", "in-progress"} {
		if code, body := send(hook, secret, `{"message":{"type":"status-update","status":"`+st+`",`+call+`}}`); code != 200 {
			t.Fatalf("%s: %d %s", st, code, body)
		}
	}
	if code, _ := send(hook, secret, `{"message":{"type":"speech-update","status":"started"}}`); code != 200 {
		t.Fatalf("ignored message type: %d", code)
	}

	// Mid-call red flag through the tool URL, then the same call again
	// (a platform retry), then a tool call for a call we never placed.
	tool := `{"message":{"type":"tool-calls",` + call + `,"toolCallList":[{"id":"tc-1","type":"function","function":{"name":"report_red_flag","arguments":{"category":"fall","severity":"emergency","quote":"main bathroom mein gir gayi"}}}]}}`
	for i := 0; i < 2; i++ {
		code, body := send("/v1/voice/tools/report_red_flag", secret, tool)
		if code != 200 || !strings.Contains(body, `"toolCallId":"tc-1","result":"Reported.`) {
			t.Fatalf("tool call %d: %d %s", i, code, body)
		}
	}
	code, body := send("/v1/voice/tools/report_red_flag", secret, `{"message":{"type":"tool-calls","call":{"id":"other"},"toolCallList":[{"id":"tc-x","function":{"name":"report_red_flag","arguments":{}}}]}}`)
	if code != 200 || !strings.Contains(body, `"error":"Not recorded."`) {
		t.Fatalf("unknown call: %d %s", code, body)
	}
	var alerts int
	var typ, cat, src string
	_ = pool.QueryRow(ctx, `SELECT count(*), min(type), min(category), min(source) FROM alerts WHERE parent_id = $1`, fam.ParentID).Scan(&alerts, &typ, &cat, &src)
	if alerts != 1 || typ != "emergency" || cat != "fall" || src != "tool" {
		t.Fatalf("alerts %d %s %s %s", alerts, typ, cat, src)
	}

	// Vapi also sends tool calls to the assistant's server URL when a tool has
	// none of its own; they must not be lost there.
	code, body = send(hook, secret, `{"message":{"type":"tool-calls",`+call+`,"toolCallList":[{"id":"tc-2","function":{"name":"report_stop_request","arguments":{}}}]}}`)
	if code != 200 || !strings.Contains(body, `"toolCallId":"tc-2","result"`) {
		t.Fatalf("tool call on webhook URL: %d %s", code, body)
	}
	var parentStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM parents WHERE id = $1`, fam.ParentID).Scan(&parentStatus)
	if parentStatus != "paused" {
		t.Fatalf("stop request: parent %s", parentStatus)
	}

	report := `{"message":{"type":"end-of-call-report","endedReason":"customer-ended-call","startedAt":"2026-10-06T04:31:00Z","endedAt":"2026-10-06T04:34:30Z","cost":0.2,` + call + `,
	 "artifact":{"messages":[
	  {"role":"system","message":"SYSTEM","secondsFromStart":0},
	  {"role":"bot","message":"Namaste, main HaalChaal ki AI assistant hoon.","secondsFromStart":1},
	  {"role":"user","message":"Beta main kal bathroom mein gir gayi thi.","secondsFromStart":6.5},
	  {"role":"bot","message":"Main abhi aapke parivaar ko bata rahi hoon.","secondsFromStart":12}]}}}`
	for i := 0; i < 2; i++ {
		if code, body := send(hook, secret, report); code != 200 {
			t.Fatalf("report %d: %d %s", i, code, body)
		}
	}
	var dur, cost int
	_ = pool.QueryRow(ctx, `SELECT status, coalesce(duration_sec,0), coalesce(cost_paise,0) FROM calls WHERE id = $1`, callID).Scan(&status, &dur, &cost)
	if status != calls.StatusCompleted || dur != 210 || cost != 1700 {
		t.Fatalf("after report: %s %ds %dp", status, dur, cost)
	}
	var transcripts int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM transcripts WHERE call_id = $1`, callID).Scan(&transcripts)
	if transcripts != 1 {
		t.Fatalf("transcripts %d", transcripts)
	}

	drain()
	var reports, redFlags int
	_ = pool.QueryRow(ctx, `SELECT count(*), coalesce(max(red_flag_count),0) FROM call_reports WHERE call_id = $1`, callID).Scan(&reports, &redFlags)
	if reports != 1 || redFlags < 1 {
		t.Fatalf("reports %d red flags %d", reports, redFlags)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE parent_id = $1 AND type = 'emergency' AND category = 'fall'`, fam.ParentID).Scan(&alerts)
	if alerts != 1 {
		t.Fatalf("the tool alert and the transcript finding must merge into one emergency, got %d", alerts)
	}
}
