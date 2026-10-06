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
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
	"github.com/EklavyaGoyal17/haalchaal/internal/outbound"
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

	Messenger notify.Messenger
	Outbound  *outbound.Service

	// Admin serves /admin when configured (nil disables the admin pages).
	Admin interface{ Register(*http.ServeMux) }

	// MaxInFlight bounds concurrent requests per instance (default 256);
	// excess requests get 503 so a flood cannot exhaust the database pool.
	MaxInFlight int

	sem chan struct{}
}

// Handler returns the root handler with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("POST /v1/webhooks/voice/{provider}", s.voiceWebhook)
	mux.HandleFunc("POST /v1/voice/tools/{tool}", s.voiceTool)
	mux.HandleFunc("GET /v1/webhooks/whatsapp", s.whatsappVerify)
	mux.HandleFunc("POST /v1/webhooks/whatsapp", s.whatsappWebhook)
	if s.Admin != nil {
		s.Admin.Register(mux)
	}
	n := s.MaxInFlight
	if n <= 0 {
		n = 256
	}
	s.sem = make(chan struct{}, n)
	return s.securityHeaders(s.requestID(s.limitInFlight(s.recoverer(mux))))
}

// securityHeaders sets conservative headers on every response. Nothing here
// is meant to be framed, sniffed, cached or embedded elsewhere.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" { // HSTS is harmless if the header lies
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// limitInFlight sheds load beyond MaxInFlight concurrent requests. Health
// checks are exempt so a busy instance is not marked dead.
func (s *Server) limitInFlight(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "busy"})
		}
	})
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
