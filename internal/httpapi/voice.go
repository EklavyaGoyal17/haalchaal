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
	if s.Voice == nil || s.Calls == nil || r.PathValue("provider") != s.Voice.Name() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown provider"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
	if dp, ok := s.Voice.(voice.DeliveryParser); ok {
		s.voiceDelivery(w, r, dp)
		return
	}
	events, err := s.Voice.ParseWebhook(r)
	if err != nil {
		s.rejectWebhook(w, r, err)
		return
	}
	dupes, status, ok := s.applyEvents(r, events)
	if !ok {
		writeJSON(w, status, map[string]string{"error": http.StatusText(status)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "received": len(events), "duplicates": dupes})
}

// applyEvents applies verified events in order. On failure it returns the
// status to answer with; the platform then retries the whole request, and
// events already applied are deduplicated.
func (s *Server) applyEvents(r *http.Request, events []voice.Event) (dupes, status int, ok bool) {
	ctx := r.Context()
	rid := logging.RequestID(ctx)
	for _, ev := range events {
		res, err := s.Calls.ApplyEvent(ctx, s.Voice.Name(), ev)
		switch {
		case errors.Is(err, calls.ErrUnknownCall):
			s.Log.WarnContext(ctx, "voice webhook for unknown call", "request_id", rid, "event_type", ev.Type)
			return dupes, http.StatusNotFound, false
		case err != nil:
			s.Log.ErrorContext(ctx, "voice webhook", "request_id", rid, "event_type", ev.Type, "error", err)
			return dupes, http.StatusInternalServerError, false
		case res == calls.EventDuplicate:
			dupes++
		}
	}
	return dupes, http.StatusOK, true
}

// voiceDelivery serves providers that may send tool calls and status events
// to either URL (Vapi). Tool calls are recorded first: they are the urgent
// part. A request with tool calls is always answered 200 with per-call
// results, because the platform ignores any other status.
func (s *Server) voiceDelivery(w http.ResponseWriter, r *http.Request, dp voice.DeliveryParser) {
	d, err := dp.ParseDelivery(r)
	if err != nil {
		s.rejectWebhook(w, r, err)
		return
	}
	if len(d.ToolCalls) > 0 {
		results := make([]voice.ToolResult, 0, len(d.ToolCalls))
		for _, tc := range d.ToolCalls {
			results = append(results, voice.ToolResult{Call: tc, Err: s.handleTool(r, tc)})
		}
		if len(d.Events) > 0 {
			_, _, _ = s.applyEvents(r, d.Events)
		}
		writeJSON(w, http.StatusOK, dp.ToolReply(results))
		return
	}
	dupes, status, ok := s.applyEvents(r, d.Events)
	if !ok {
		writeJSON(w, status, map[string]string{"error": http.StatusText(status)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "received": len(d.Events), "duplicates": dupes})
}

// handleTool records one tool call and logs failures without call content.
func (s *Server) handleTool(r *http.Request, tc voice.ToolCall) error {
	ctx := r.Context()
	rid := logging.RequestID(ctx)
	_, err := s.Calls.HandleTool(ctx, s.Voice.Name(), tc)
	switch {
	case errors.Is(err, calls.ErrUnknownTool):
		s.Log.WarnContext(ctx, "unknown tool", "request_id", rid)
	case errors.Is(err, calls.ErrUnknownCall):
		s.Log.ErrorContext(ctx, "tool call for unknown call", "request_id", rid, "tool", tc.Tool)
	case err != nil:
		s.Log.ErrorContext(ctx, "tool call", "request_id", rid, "tool", tc.Tool, "error", err)
	}
	return err
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
	if dp, ok := s.Voice.(voice.DeliveryParser); ok {
		s.voiceDelivery(w, r, dp)
		return
	}
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
