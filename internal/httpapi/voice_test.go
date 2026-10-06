package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice/fake"
)

// These requests must all be rejected before any database work, so the
// service has no pool: reaching it would panic and fail the test.
func TestVoiceWebhookRejections(t *testing.T) {
	var logs bytes.Buffer
	p := fake.New("secret")
	s := &Server{Log: slog.New(slog.NewJSONHandler(&logs, nil)), Voice: p, Calls: &calls.Service{}}
	h := s.Handler()

	good := []byte(`{"events":[{"event_id":"e1","provider_call_id":"fake-1","type":"ringing","at":"2026-10-06T05:00:00Z"}]}`)
	secretText := []byte(`{"events":[{"event_id":"e1","provider_call_id":"fake-1","type":"transcript_ready","at":"2026-10-06T05:00:00Z","transcript":[{"speaker":"parent","text":"PRIVATE-HEALTH-DETAIL","offset_ms":1}]}]}`)
	tests := []struct {
		name string
		path string
		body []byte
		sig  string
		want int
	}{
		{"no signature", "/v1/webhooks/voice/fake", good, "", http.StatusUnauthorized},
		{"wrong signature", "/v1/webhooks/voice/fake", good, fake.New("other").Sign(good), http.StatusUnauthorized},
		{"garbage signature", "/v1/webhooks/voice/fake", good, "zz", http.StatusUnauthorized},
		{"signature for different body", "/v1/webhooks/voice/fake", good, p.Sign([]byte("{}")), http.StatusUnauthorized},
		{"tampered transcript", "/v1/webhooks/voice/fake", secretText, p.Sign(good), http.StatusUnauthorized},
		{"unknown provider", "/v1/webhooks/voice/vapi", good, p.Sign(good), http.StatusNotFound},
		{"signed but malformed", "/v1/webhooks/voice/fake", []byte(`{"events":[{"type":"nope"}]}`), p.Sign([]byte(`{"events":[{"type":"nope"}]}`)), http.StatusBadRequest},
		{"signed unknown field", "/v1/webhooks/voice/fake", []byte(`{"events":[],"x":1}`), p.Sign([]byte(`{"events":[],"x":1}`)), http.StatusBadRequest},
		{"tool without signature", "/v1/voice/tools/report_red_flag", []byte(`{"event_id":"t","provider_call_id":"p","args":{}}`), "", http.StatusUnauthorized},
		{"unknown tool, signed", "/v1/voice/tools/transfer_money", []byte(`{"event_id":"t","provider_call_id":"p","args":{}}`), p.Sign([]byte(`{"event_id":"t","provider_call_id":"p","args":{}}`)), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(tt.body))
			if tt.sig != "" {
				req.Header.Set(fake.SignatureHeader, tt.sig)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, tt.want, rec.Body)
			}
		})
	}
	if strings.Contains(logs.String(), "PRIVATE-HEALTH-DETAIL") {
		t.Fatal("request body leaked into logs")
	}
}

func TestVoiceWebhookBodyLimit(t *testing.T) {
	p := fake.New("secret")
	s := &Server{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Voice: p, Calls: &calls.Service{}}
	big := bytes.Repeat([]byte("a"), maxWebhookBody+10)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/voice/fake", bytes.NewReader(big))
	req.Header.Set(fake.SignatureHeader, p.Sign(big))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestWebhookMethodNotAllowed(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Voice: fake.New("s"), Calls: &calls.Service{}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/webhooks/voice/fake", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestFakeWithoutSecretRejectsEverything(t *testing.T) {
	p := fake.New("")
	s := &Server{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Voice: p, Calls: &calls.Service{}}
	body := []byte(`{"events":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/voice/fake", bytes.NewReader(body))
	req.Header.Set(fake.SignatureHeader, p.Sign(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}
