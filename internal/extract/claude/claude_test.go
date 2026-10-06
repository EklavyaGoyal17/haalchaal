package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReducedSchemaKeepsFields(t *testing.T) {
	raw, err := reducedSchema()
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "maxLength") || strings.Contains(s, `"minimum"`) || strings.Contains(s, "$schema") {
		t.Errorf("keywords left: %s", s)
	}
	var v struct {
		Properties map[string]struct {
			Items struct {
				Properties map[string]any `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(raw, &v)
	if _, ok := v.Properties["scam_signals"].Items.Properties["pattern"]; !ok {
		t.Fatal("the scam_signals.pattern field was stripped")
	}
}

func server(t *testing.T, status int, body string, seen *map[string]any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if seen != nil {
			_ = json.Unmarshal(b, seen)
			(*seen)["_beta"] = r.Header.Get("anthropic-beta")
			(*seen)["_key"] = r.Header.Get("x-api-key")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func msg(stop, text string) string {
	return `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"` + stop +
		`","content":[{"type":"text","text":` + strconvQuote(text) + `}],"usage":{"input_tokens":1,"output_tokens":1}}`
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestComplete(t *testing.T) {
	var seen map[string]any
	srv := server(t, 200, msg("end_turn", `{"ok":true}`), &seen)
	c, err := New("sk-test", "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Complete(context.Background(), "SECRET TRANSCRIPT")
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("%q %v", out, err)
	}
	if seen["model"] != DefaultModel || seen["_key"] != "sk-test" || !strings.Contains(seen["_beta"].(string), "server-side-fallback-2026-07-01") ||
		seen["fallbacks"] != "default" {
		t.Errorf("request: %v", seen)
	}
	oc, _ := seen["output_config"].(map[string]any)
	if f, _ := oc["format"].(map[string]any); f["type"] != "json_schema" {
		t.Errorf("output_config %v", oc)
	}
	if c.Name() != "anthropic:claude-opus-5-5" {
		t.Error(c.Name())
	}
}

func TestCompleteErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"refusal":   {200, msg("refusal", "")},
		"truncated": {200, msg("max_tokens", `{"a":`)},
		"400":       {400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad SECRET TRANSCRIPT"}}`},
	} {
		srv := server(t, tc.status, tc.body, nil)
		c, _ := New("sk", "claude-sonnet-5-5", srv.URL)
		_, err := c.Complete(context.Background(), "SECRET TRANSCRIPT")
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: err %v", name, err)
		}
	}
	if _, err := New("", "", ""); err == nil {
		t.Error("missing key accepted")
	}
}
