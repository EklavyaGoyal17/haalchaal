package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
	fakenotify "github.com/EklavyaGoyal17/haalchaal/internal/notify/fake"
	"github.com/EklavyaGoyal17/haalchaal/internal/outbound"
)

func waServer() (*Server, *fakenotify.Messenger) {
	m := fakenotify.New("app-secret", "verify-me", nil)
	return &Server{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Messenger: m, Outbound: &outbound.Service{}}, m
}

func TestWhatsAppVerify(t *testing.T) {
	s, _ := waServer()
	h := s.Handler()
	tests := []struct {
		query string
		code  int
		body  string
	}{
		{"hub.mode=subscribe&hub.verify_token=verify-me&hub.challenge=12345", 200, "12345"},
		{"hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=12345", 403, ""},
		{"hub.mode=unsubscribe&hub.verify_token=verify-me&hub.challenge=1", 403, ""},
		{"hub.mode=subscribe&hub.challenge=1", 403, ""},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/webhooks/whatsapp?"+tt.query, nil))
		if rec.Code != tt.code || (tt.body != "" && rec.Body.String() != tt.body) {
			t.Errorf("%s: %d %q", tt.query, rec.Code, rec.Body.String())
		}
	}
}

func TestWhatsAppWebhookRejectsBadSignature(t *testing.T) {
	s, m := waServer()
	body := notify.MetaInboundBody(notify.InboundEvent{Kind: notify.KindButton, From: "+919876541234", Payload: "ack:x", MessageID: "w1"})
	for name, sig := range map[string]string{"none": "", "wrong": notify.MetaSign([]byte("nope"), body), "body swap": m.Sign([]byte("{}"))} {
		req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/whatsapp", bytes.NewReader(body))
		if sig != "" {
			req.Header.Set(notify.MetaSignatureHeader, sig)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	bad := []byte(`{"entry":[{"changes":[{"value":{"messages":[{"from":"nope","id":"x"}]}}]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/whatsapp", bytes.NewReader(bad))
	req.Header.Set(notify.MetaSignatureHeader, m.Sign(bad))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed: %d", rec.Code)
	}
}
