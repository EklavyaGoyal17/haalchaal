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

type fakeMigrations struct {
	pending bool
	err     error
}

func (f fakeMigrations) HasPending(context.Context) (bool, error) { return f.pending, f.err }

func newServer(p Pinger, m MigrationChecker) *Server {
	return &Server{DB: p, Migrations: m, Log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
}

func TestRoutes(t *testing.T) {
	current := fakeMigrations{}
	tests := []struct {
		name   string
		db     Pinger
		mig    MigrationChecker
		method string
		path   string
		want   int
	}{
		{"healthz ok", nil, nil, http.MethodGet, "/healthz", http.StatusOK},
		{"healthz ignores db", fakePinger{errors.New("down")}, nil, http.MethodGet, "/healthz", http.StatusOK},
		{"readyz ready", fakePinger{}, current, http.MethodGet, "/readyz", http.StatusOK},
		{"readyz db down", fakePinger{errors.New("down")}, current, http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"readyz no db", nil, current, http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"readyz no migrator", fakePinger{}, nil, http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"readyz migrations pending", fakePinger{}, fakeMigrations{pending: true}, http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"readyz migration check fails", fakePinger{}, fakeMigrations{err: errors.New("boom")}, http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"wrong method", nil, nil, http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{"unknown path", nil, nil, http.MethodGet, "/nope", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newServer(tt.db, tt.mig).Handler().ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
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
	s := newServer(nil, nil)
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
