// Package httpapi holds HTTP routes, handlers and middleware.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/logging"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// Pinger reports whether the database is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// MigrationChecker reports whether any migration is not yet applied.
type MigrationChecker interface {
	HasPending(ctx context.Context) (bool, error)
}

// Server wires routes to their dependencies.
type Server struct {
	DB         Pinger
	Migrations MigrationChecker
	Log        *slog.Logger

	Voice voice.Provider
	Calls *calls.Service
}

// Handler returns the root handler with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("POST /v1/webhooks/voice/{provider}", s.voiceWebhook)
	mux.HandleFunc("POST /v1/voice/tools/{tool}", s.voiceTool)
	return s.requestID(s.recoverer(mux))
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	rid := logging.RequestID(ctx)
	if s.DB == nil || s.Migrations == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "not configured"})
		return
	}
	if err := s.DB.Ping(ctx); err != nil {
		s.Log.WarnContext(ctx, "readyz: database ping failed", "request_id", rid, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "unreachable"})
		return
	}
	pending, err := s.Migrations.HasPending(ctx)
	if err != nil {
		s.Log.WarnContext(ctx, "readyz: migration check failed", "request_id", rid, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "migrations": "unknown"})
		return
	}
	if pending {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "migrations": "pending"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// requestID tags each request with an id, echoed in X-Request-Id.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := logging.NewRequestID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

// recoverer turns a panic into a 500 without echoing the request.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.Log.ErrorContext(r.Context(), "panic in handler",
					"request_id", logging.RequestID(r.Context()), "method", r.Method, "path", r.URL.Path)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
