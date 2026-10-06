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

func TestSecurityHeaders(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Referrer-Policy", "Cache-Control", "X-Request-Id"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS on plain HTTP")
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("no HSTS behind TLS proxy")
	}
}

func TestLoadShedding(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 4)
	s := &Server{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), MaxInFlight: 2}
	_ = s.Handler() // initialises the semaphore
	slow := s.limitInFlight(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-block
	}))
	for range 2 {
		go slow.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}
	<-started
	<-started
	rec := httptest.NewRecorder()
	slow.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("third request: %d", rec.Code)
	}
	close(block)
}
