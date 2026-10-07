package vapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

const testSecret = "0123456789abcdef0123456789abcdef-test"

func newTest(t *testing.T, api http.HandlerFunc) *Provider {
	t.Helper()
	base := ""
	if api != nil {
		srv := httptest.NewServer(api)
		t.Cleanup(srv.Close)
		base = srv.URL
	}
	p, err := New(Options{APIKey: "key-1", WebhookSecret: testSecret, AssistantID: "asst-1", PhoneNumberID: "pn-1", CostPaisePerUSD: 8500, BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewValidates(t *testing.T) {
	ok := Options{APIKey: "k", WebhookSecret: testSecret, AssistantID: "a", PhoneNumberID: "p"}
	cases := []struct {
		name string
		mod  func(*Options)
		want string
	}{
		{"missing key", func(o *Options) { o.APIKey = "" }, "VOICE_API_KEY"},
		{"missing everything", func(o *Options) { *o = Options{} }, "VOICE_PHONE_NUMBER_ID"},
		{"short secret", func(o *Options) { o.WebhookSecret = "short" }, "at least"},
		{"plain http", func(o *Options) { o.BaseURL = "http://api.vapi.ai" }, "https"},
		{"negative rate", func(o *Options) { o.CostPaisePerUSD = -1 }, "negative"},
	}
	for _, c := range cases {
		o := ok
		c.mod(&o)
		if _, err := New(o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
	if _, err := New(ok); err != nil {
		t.Fatalf("valid options: %v", err)
	}
}

func TestStartCall(t *testing.T) {
	var got map[string]any
	p := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/call" || r.Header.Get("Authorization") != "Bearer key-1" {
			t.Errorf("request %s %s auth %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"vapi-call-1","status":"queued"}`)
	})
	sc, err := p.StartCall(context.Background(), voice.CallRequest{
		CallID: "our-1", To: "+919876543210", Language: "hi", SystemPrompt: "PROMPT",
		Variables: map[string]string{"parent_name": "Amma", "system_prompt": "must not win"},
	})
	if err != nil || sc.ProviderCallID != "vapi-call-1" {
		t.Fatalf("StartCall = %+v, %v", sc, err)
	}
	if got["assistantId"] != "asst-1" || got["phoneNumberId"] != "pn-1" {
		t.Errorf("ids: %v", got)
	}
	if got["customer"].(map[string]any)["number"] != "+919876543210" {
		t.Errorf("customer: %v", got["customer"])
	}
	ov := got["assistantOverrides"].(map[string]any)
	vars := ov["variableValues"].(map[string]any)
	if vars["system_prompt"] != "PROMPT" || vars["parent_name"] != "Amma" || vars["call_id"] != "our-1" || vars["language"] != "hi" {
		t.Errorf("variables: %v", vars)
	}
	if ov["metadata"].(map[string]any)[MetadataCallID] != "our-1" {
		t.Errorf("metadata: %v", ov["metadata"])
	}
	plan := ov["artifactPlan"].(map[string]any)
	if plan["recordingEnabled"] != false || plan["pcapEnabled"] != false {
		t.Errorf("recording must be off: %v", plan)
	}
	if _, ok := ov["server"]; ok {
		t.Error("the server URL must come from the saved assistant, or Vapi drops the credential")
	}
}

func TestStartCallErrorsCarryNoContent(t *testing.T) {
	p := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"bad prompt: SECRET-PROMPT-TEXT +919876543210"}`)
	})
	_, err := p.StartCall(context.Background(), voice.CallRequest{CallID: "c", To: "+919876543210", SystemPrompt: "SECRET-PROMPT-TEXT"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{"SECRET", "9876543210", "key-1"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q leaks %q", err, leak)
		}
	}
	p = newTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	})
	if _, err := p.StartCall(context.Background(), voice.CallRequest{CallID: "c", To: "+919876543210"}); err == nil {
		t.Error("a response without an id must be an error")
	}
}

func TestDeleteRecording(t *testing.T) {
	for _, c := range []struct {
		status  int
		wantErr bool
	}{{200, false}, {404, false}, {503, true}, {401, true}} {
		var path, method string
		p := newTest(t, func(w http.ResponseWriter, r *http.Request) {
			path, method = r.URL.EscapedPath(), r.Method
			w.WriteHeader(c.status)
		})
		err := p.DeleteRecording(context.Background(), "abc/../x")
		if (err != nil) != c.wantErr {
			t.Errorf("status %d: err = %v", c.status, err)
		}
		if method != http.MethodDelete || path != "/call/abc%2F..%2Fx" {
			t.Errorf("request %s %s", method, path)
		}
	}
}

func post(t *testing.T, p *Provider, body string, hdr map[string]string) (voice.Delivery, error) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/webhooks/voice/vapi", strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return p.ParseDelivery(r)
}

var bearer = map[string]string{"Authorization": "Bearer " + testSecret}

func TestAuth(t *testing.T) {
	p := newTest(t, nil)
	body := `{"message":{"type":"status-update","status":"ringing","call":{"id":"v1"}}}`
	cases := []struct {
		name string
		hdr  map[string]string
		ok   bool
	}{
		{"bearer", bearer, true},
		{"lowercase bearer", map[string]string{"Authorization": "bearer " + testSecret}, true},
		{"x-vapi-secret", map[string]string{"X-Vapi-Secret": testSecret}, true},
		{"none", nil, false},
		{"wrong", map[string]string{"Authorization": "Bearer " + testSecret + "x"}, false},
		{"prefix only", map[string]string{"Authorization": "Bearer " + testSecret[:20]}, false},
		{"basic", map[string]string{"Authorization": "Basic " + testSecret}, false},
		{"wrong secret header wins over bearer", map[string]string{"X-Vapi-Secret": "nope", "Authorization": "Bearer " + testSecret}, false},
	}
	for _, c := range cases {
		_, err := post(t, p, body, c.hdr)
		if c.ok != (err == nil) || (!c.ok && !errors.Is(err, voice.ErrBadSignature)) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

func TestStatusUpdates(t *testing.T) {
	p := newTest(t, nil)
	for _, c := range []struct {
		status string
		want   voice.EventType
	}{{"ringing", voice.EventRinging}, {"in-progress", voice.EventAnswered}, {"ended", ""}, {"queued", ""}, {"forwarding", ""}} {
		body := `{"message":{"type":"status-update","status":"` + c.status + `","timestamp":1760000000000,"call":{"id":"v1","assistantOverrides":{"metadata":{"haalchaal_call_id":"our-1"}}}}}`
		d, err := post(t, p, body, bearer)
		if err != nil {
			t.Fatalf("%s: %v", c.status, err)
		}
		if c.want == "" {
			if len(d.Events) != 0 {
				t.Errorf("%s: events %+v", c.status, d.Events)
			}
			continue
		}
		if len(d.Events) != 1 {
			t.Fatalf("%s: events %+v", c.status, d.Events)
		}
		e := d.Events[0]
		if e.Type != c.want || e.ProviderCallID != "v1" || e.CallID != "our-1" || e.EventID != "v1:status:"+c.status || !e.At.Equal(time.UnixMilli(1760000000000)) {
			t.Errorf("%s: %+v", c.status, e)
		}
	}
}

func TestIgnoredAndBadMessages(t *testing.T) {
	p := newTest(t, nil)
	for _, body := range []string{
		`{"message":{"type":"transcript","transcript":"hello","call":{"id":"v1"}}}`,
		`{"message":{"type":"speech-update","status":"started"}}`,
		`{"message":{"type":"conversation-update","messages":[]}}`,
	} {
		d, err := post(t, p, body, bearer)
		if err != nil || len(d.Events)+len(d.ToolCalls) != 0 {
			t.Errorf("%s: %+v %v", body, d, err)
		}
	}
	for _, body := range []string{
		`not json`,
		`{}`,
		`{"message":{"type":"status-update","status":"ringing"}}`,
		`{"message":{"type":"end-of-call-report","call":{}}}`,
		`{"message":{"type":"tool-calls","call":{"id":"v1"},"toolCallList":[]}}`,
		`{"message":{"type":"tool-calls","call":{"id":"v1"},"toolCallList":[{"id":"","function":{"name":"report_red_flag"}}]}}`,
		`{"message":{"type":"tool-calls","call":{"id":"v1"},"toolCallList":[{"id":"t","function":{"name":"report_red_flag","arguments":[1]}}]}}`,
	} {
		if _, err := post(t, p, body, bearer); !errors.Is(err, voice.ErrBadPayload) {
			t.Errorf("%s: err = %v, want bad payload", body, err)
		}
	}
	big := `{"message":{"type":"transcript","transcript":"` + strings.Repeat("a", MaxBody) + `"}}`
	if _, err := post(t, p, big, bearer); !errors.Is(err, voice.ErrBadPayload) {
		t.Errorf("oversized: %v", err)
	}
}

func TestToolCalls(t *testing.T) {
	p := newTest(t, nil)
	body := `{"message":{"type":"tool-calls","call":{"id":"v1","assistantOverrides":{"metadata":{"haalchaal_call_id":"our-1"}}},
	"toolCallList":[
	 {"id":"tc1","type":"function","function":{"name":"report_red_flag","arguments":{"category":"fall","severity":"emergency","quote":"main gir gayi"}}},
	 {"id":"tc2","type":"function","function":{"name":"report_scam","arguments":"{\"pattern\":\"otp\",\"count\":2}"}},
	 {"id":"tc3","type":"function","function":{"name":"report_stop_request"}}]}}`
	d, err := post(t, p, body, bearer)
	if err != nil || len(d.ToolCalls) != 3 {
		t.Fatalf("%+v %v", d, err)
	}
	a, b, c := d.ToolCalls[0], d.ToolCalls[1], d.ToolCalls[2]
	if a.Tool != voice.ToolReportRedFlag || a.EventID != "tc1" || a.ProviderCallID != "v1" || a.CallID != "our-1" || a.Args["category"] != "fall" || a.Args["quote"] != "main gir gayi" {
		t.Errorf("red flag: %+v", a)
	}
	if b.Tool != voice.ToolReportScam || b.Args["pattern"] != "otp" || b.Args["count"] != "2" {
		t.Errorf("scam (string arguments): %+v", b)
	}
	if c.Tool != voice.ToolReportStopRequest || len(c.Args) != 0 {
		t.Errorf("stop: %+v", c)
	}

	raw, _ := json.Marshal(p.ToolReply([]voice.ToolResult{{Call: a}, {Call: b, Err: errors.New("db down: Amma said gir gayi")}}))
	want := `{"results":[{"toolCallId":"tc1","result":"Reported. The family is being informed."},{"toolCallId":"tc2","error":"Not recorded."}]}`
	if string(raw) != want {
		t.Errorf("reply %s", raw)
	}
}

func TestEndOfCallReport(t *testing.T) {
	p := newTest(t, nil)
	answered := `"artifact":{"messages":[
	  {"role":"system","message":"SYSTEM PROMPT","secondsFromStart":0},
	  {"role":"bot","message":"Namaste, main HaalChaal ki AI hoon.","secondsFromStart":0.5},
	  {"role":"user","message":"Haan beta, theek hoon.","secondsFromStart":4.25},
	  {"role":"tool_calls","message":"","secondsFromStart":5},
	  {"role":"user","message":"   ","secondsFromStart":6}]}`
	cases := []struct {
		name, reason, extra string
		want                []voice.EventType
	}{
		{"hung up after talking", "customer-ended-call", answered, []voice.EventType{voice.EventCompleted}},
		{"worker died mid-call", "call.in-progress.error-vapifault-worker-died", answered, []voice.EventType{voice.EventCompleted}},
		{"no answer", "customer-did-not-answer", "", []voice.EventType{voice.EventNoAnswer}},
		{"sip 480", "call.in-progress.error-providerfault-outbound-sip-480-temporarily-unavailable", "", []voice.EventType{voice.EventNoAnswer}},
		{"busy", "customer-busy", "", []voice.EventType{voice.EventBusy}},
		{"voicemail", "voicemail", answered, []voice.EventType{voice.EventNoAnswer, voice.EventFailed}},
		{"silence, nobody spoke", "silence-timed-out", "", []voice.EventType{voice.EventCompleted}},
		{"never connected", "call.start.error-get-phone-number", "", []voice.EventType{voice.EventFailed}},
		{"unknown reason, nobody spoke", "something-new", "", []voice.EventType{voice.EventFailed}},
	}
	for _, c := range cases {
		body := `{"message":{"type":"end-of-call-report","endedReason":"` + c.reason + `","timestamp":"2026-10-08T05:00:09Z",
		  "startedAt":"2026-10-08T04:57:00Z","endedAt":"2026-10-08T05:00:05Z","cost":0.12,
		  "call":{"id":"v1","assistantOverrides":{"metadata":{"haalchaal_call_id":"our-1"}}}` + comma(c.extra) + `}}`
		d, err := post(t, p, body, bearer)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(d.Events) != len(c.want) {
			t.Fatalf("%s: events %+v", c.name, d.Events)
		}
		for i, e := range d.Events {
			if e.Type != c.want[i] || e.CallID != "our-1" || e.ProviderCallID != "v1" {
				t.Errorf("%s[%d]: %+v", c.name, i, e)
			}
		}
		e := d.Events[0]
		if e.EventID != "v1:end" || !e.At.Equal(time.Date(2026, 10, 8, 5, 0, 5, 0, time.UTC)) {
			t.Errorf("%s: id %q at %v", c.name, e.EventID, e.At)
		}
		if e.Type != voice.EventCompleted {
			if len(e.Transcript) != 0 || e.CostPaise != 0 {
				t.Errorf("%s: unanswered event carries data: %+v", c.name, e)
			}
			continue
		}
		if e.DurationSec != 185 || e.CostPaise != 1020 {
			t.Errorf("%s: duration %d cost %d", c.name, e.DurationSec, e.CostPaise)
		}
		if c.extra != "" {
			if len(e.Transcript) != 2 || e.Transcript[0].Speaker != "agent" || e.Transcript[1].Speaker != "parent" ||
				e.Transcript[1].Text != "Haan beta, theek hoon." || e.Transcript[1].Offset != 4250*time.Millisecond {
				t.Errorf("%s: transcript %+v", c.name, e.Transcript)
			}
		}
	}
}

func comma(s string) string {
	if s == "" {
		return ""
	}
	return "," + s
}

func TestCostNeedsRate(t *testing.T) {
	p, err := New(Options{APIKey: "k", WebhookSecret: testSecret, AssistantID: "a", PhoneNumberID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	c := 0.5
	if got := p.paise(&c); got != 0 {
		t.Errorf("no rate configured: cost %d, want 0 (unrecorded)", got)
	}
	bad := -1.0
	if got := newTest(t, nil).paise(&bad, &c); got != 4250 {
		t.Errorf("negative cost skipped: %d", got)
	}
}

func TestSingleToolCallParse(t *testing.T) {
	p := newTest(t, nil)
	r := httptest.NewRequest(http.MethodPost, "/v1/voice/tools/report_red_flag", strings.NewReader(
		`{"message":{"type":"tool-calls","call":{"id":"v1"},"toolCallList":[{"id":"t","function":{"name":"report_red_flag","arguments":{}}}]}}`))
	r.Header.Set("Authorization", "Bearer "+testSecret)
	tc, err := p.ParseToolCall(r)
	if err != nil || tc.EventID != "t" || tc.CallID != "" {
		t.Fatalf("%+v %v", tc, err)
	}
	r = httptest.NewRequest(http.MethodPost, "/v1/webhooks/voice/vapi", strings.NewReader(
		`{"message":{"type":"tool-calls","call":{"id":"v1"},"toolCallList":[{"id":"t","function":{"name":"report_red_flag"}}]}}`))
	r.Header.Set("Authorization", "Bearer "+testSecret)
	if _, err := p.ParseWebhook(r); !errors.Is(err, voice.ErrBadPayload) {
		t.Errorf("ParseWebhook must refuse to drop tool calls: %v", err)
	}
}
