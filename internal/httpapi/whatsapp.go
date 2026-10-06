package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/EklavyaGoyal17/haalchaal/internal/logging"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
)

// whatsappVerify answers Meta's GET subscription check.
func (s *Server) whatsappVerify(w http.ResponseWriter, r *http.Request) {
	if s.Messenger == nil {
		http.NotFound(w, r)
		return
	}
	qv := r.URL.Query()
	want := s.Messenger.VerifyToken()
	got := qv.Get("hub.verify_token")
	if qv.Get("hub.mode") != "subscribe" || want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "verification failed"})
		return
	}
	challenge := qv.Get("hub.challenge")
	if len(challenge) > 256 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad challenge"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(challenge))
}

// whatsappWebhook handles POST /v1/webhooks/whatsapp: verify the signature,
// dedupe, persist, answer quickly. Bodies are never logged.
func (s *Server) whatsappWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rid := logging.RequestID(ctx)
	if s.Messenger == nil || s.Outbound == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, notify.MaxWebhookBody)
	events, err := s.Messenger.ParseWebhook(r)
	switch {
	case errors.Is(err, notify.ErrBadSignature):
		s.Log.WarnContext(ctx, "whatsapp signature rejected", "request_id", rid)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad signature"})
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad payload"})
		return
	}
	for _, ev := range events {
		if _, err := s.Outbound.HandleInbound(ctx, ev); err != nil {
			s.Log.ErrorContext(ctx, "whatsapp inbound", "request_id", rid, "kind", ev.Kind, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "received": len(events)})
}
