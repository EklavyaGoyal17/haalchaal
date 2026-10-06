package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func newServer(p Pinger) *Server {
	return &Server{DB: p, Log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
}

func TestRoutes(t *testing.T) {
	tests := []struct {
		name   string
		db     Pinger
		method string
		path   string
		want   int
	}{
		{"healthz ok", nil, http.MethodGet, "/healthz", http.StatusOK},
		{"healthz ignores db", fakePinger{errors.New("down")}, http.MethodGet, "/healthz", http.StatusOK},
		{"readyz db up", fakePinger{}, http.MethodGet, "/readyz", http.StatusOK},
		{"readyz db down", fakePinger{errors.New("down")}, http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"readyz no db", nil, http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"wrong method", nil, http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{"unknown path", nil, http.MethodGet, "/nope", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newServer(tt.db).Handler().ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.want, rec.Body.String())
			}
			if rec.Header().Get("X-Request-Id") == "" {
				t.Error("missing X-Request-Id header")
			}
		})
	}
}

func TestRecovererHidesPanic(t *testing.T) {
	s := newServer(nil)
	h := s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret detail") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); body != "internal error\n" {
		t.Fatalf("body = %q", body)
	}
}
