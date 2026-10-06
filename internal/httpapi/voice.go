package httpapi

import (
	"errors"
	"net/http"

	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/logging"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// maxWebhookBody bounds every webhook body; transcripts of a 5-minute call
// are far smaller.
const maxWebhookBody = 1 << 20

// voiceWebhook handles POST /v1/webhooks/voice/{provider}: verify, dedupe,
// persist and enqueue, then answer quickly. Bodies are never logged.
func (s *Server) voiceWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rid := logging.RequestID(ctx)
	if s.Voice == nil || s.Calls == nil || r.PathValue("provider") != s.Voice.Name() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown provider"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
	events, err := s.Voice.ParseWebhook(r)
	if err != nil {
		s.rejectWebhook(w, r, err)
		return
	}
	applied, dupes := 0, 0
	for _, ev := range events {
		res, err := s.Calls.ApplyEvent(ctx, s.Voice.Name(), ev)
		switch {
		case errors.Is(err, calls.ErrUnknownCall):
			s.Log.WarnContext(ctx, "voice webhook for unknown call", "request_id", rid, "event_type", ev.Type)
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown call"})
			return
		case err != nil:
			s.Log.ErrorContext(ctx, "voice webhook", "request_id", rid, "event_type", ev.Type, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		case res == calls.EventDuplicate:
			dupes++
		default:
			applied++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "received": len(events), "duplicates": dupes})
}

// voiceTool handles POST /v1/voice/tools/{tool}: a mid-call report that must
// create or merge an alert at once and answer within a second.
func (s *Server) voiceTool(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rid := logging.RequestID(ctx)
	if s.Voice == nil || s.Calls == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown provider"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
	tc, err := s.Voice.ParseToolCall(r)
	if err != nil {
		s.rejectWebhook(w, r, err)
		return
	}
	res, err := s.Calls.HandleTool(ctx, s.Voice.Name(), tc)
	switch {
	case errors.Is(err, calls.ErrUnknownTool):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown tool"})
	case errors.Is(err, calls.ErrUnknownCall):
		s.Log.ErrorContext(ctx, "tool call for unknown call", "request_id", rid, "tool", tc.Tool)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown call"})
	case err != nil:
		s.Log.ErrorContext(ctx, "tool call", "request_id", rid, "tool", tc.Tool, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	default:
		// The agent reads this back; it never carries data from the call.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": res == calls.EventDuplicate, "message": "Reported. The family is being informed."})
	}
}

func (s *Server) rejectWebhook(w http.ResponseWriter, r *http.Request, err error) {
	rid := logging.RequestID(r.Context())
	var tooBig *http.MaxBytesError
	switch {
	case errors.Is(err, voice.ErrBadSignature):
		s.Log.WarnContext(r.Context(), "webhook signature rejected", "request_id", rid, "path", r.URL.Path)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad signature"})
	case errors.As(err, &tooBig):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body too large"})
	default:
		s.Log.WarnContext(r.Context(), "webhook payload rejected", "request_id", rid, "path", r.URL.Path)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad payload"})
	}
}
