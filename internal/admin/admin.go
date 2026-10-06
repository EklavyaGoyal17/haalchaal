// Package admin serves the founders' admin pages (SPEC §11): onboarding,
// consents, the review queue, call detail with audit, status changes, test
// calls, export and erase, and the pilot dashboard. Server-rendered
// html/template pages, HTTP Basic auth, CSRF tokens on every form.
package admin

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/logging"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Handler serves /admin.
type Handler struct {
	Pool     *pgxpool.Pool
	Clock    clock.Clock
	Log      *slog.Logger
	Keyring  *crypto.Keyring
	Users    map[string][]byte // email -> bcrypt hash
	CSRFKey  []byte
	Calls    *calls.Service
	Location *time.Location // DEFAULT_TIMEZONE, for display

	// RequireTLS refuses admin requests that did not arrive over HTTPS
	// (directly or via a trusted proxy). Set in staging and prod.
	RequireTLS     bool
	TrustedProxies []*net.IPNet

	limiter *limiter
	pages   map[string]*template.Template
}

// New builds the handler and parses every page template.
func New(h Handler) (*Handler, error) {
	if len(h.Users) == 0 {
		return nil, fmt.Errorf("admin: no ADMIN_USERS configured")
	}
	if len(h.CSRFKey) < 32 {
		return nil, fmt.Errorf("admin: CSRF key too short")
	}
	if h.Location == nil {
		h.Location = time.UTC
	}
	h.limiter = newLimiter(5, 15*time.Minute)
	h.pages = map[string]*template.Template{}
	funcs := template.FuncMap{
		"mask": logging.MaskPhone,
		"when": func(t time.Time) string { return t.In(h.Location).Format("02 Jan 15:04") },
		"whenp": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.In(h.Location).Format("02 Jan 15:04")
		},
		"date":   func(d pgtype.Date) string { return d.Time.Format("02 Jan 2006") },
		"clock":  pgClock,
		"rupees": func(p int64) string { return fmt.Sprintf("₹%d.%02d", p/100, p%100) },
		"pct": func(a, b int32) string {
			if b == 0 {
				return "-"
			}
			return fmt.Sprintf("%.0f%%", 100*float64(a)/float64(b))
		},
		"minutes": func(sec float64) string { return fmt.Sprintf("%.1f", sec/60) },
		"short":   func(s fmt.Stringer) string { return s.String()[:8] },
		"deref": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
		"derefi": func(n *int32) int32 {
			if n == nil {
				return 0
			}
			return *n
		},
		"join": strings.Join,
		"inc":  func(i int) int { return i + 1 },
	}
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
		if name == "layout" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", p)
		if err != nil {
			return nil, fmt.Errorf("admin template %s: %w", name, err)
		}
		h.pages[name] = t
	}
	return &h, nil
}

func pgClock(t pgtype.Time) string {
	if !t.Valid {
		return ""
	}
	d := time.Duration(t.Microseconds) * time.Microsecond
	return fmt.Sprintf("%02d:%02d", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// Register mounts every admin route on mux, all behind authentication.
func (h *Handler) Register(mux *http.ServeMux) {
	auth := func(f http.HandlerFunc) http.Handler { return h.requireAdmin(f) }
	mux.Handle("GET /admin", auth(h.dashboard))
	mux.Handle("GET /admin/{$}", auth(h.dashboard))
	mux.Handle("GET /admin/review", auth(h.review))
	mux.Handle("GET /admin/calls/{id}", auth(h.callDetail))
	mux.Handle("POST /admin/calls/{id}/reviewed", auth(h.markReviewed))
	mux.Handle("POST /admin/alerts/{id}/resolve", auth(h.resolveAlert))
	mux.Handle("POST /admin/inbound/{id}/handled", auth(h.markInboundHandled))
	mux.Handle("GET /admin/accounts/new", auth(h.onboardForm))
	mux.Handle("POST /admin/accounts/new", auth(h.onboardSubmit))
	mux.Handle("GET /admin/parents", auth(h.parents))
	mux.Handle("GET /admin/parents/{id}", auth(h.parentDetail))
	mux.Handle("POST /admin/parents/{id}/consents", auth(h.consent))
	mux.Handle("POST /admin/parents/{id}/status", auth(h.setStatus))
	mux.Handle("POST /admin/parents/{id}/schedule", auth(h.setSchedule))
	mux.Handle("POST /admin/parents/{id}/medicines", auth(h.medicines))
	mux.Handle("POST /admin/parents/{id}/test-call", auth(h.testCall))
	mux.Handle("GET /admin/parents/{id}/export", auth(h.export))
	mux.Handle("POST /admin/parents/{id}/erase", auth(h.erase))
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /admin/static/", auth(http.StripPrefix("/admin/static/", http.FileServerFS(static)).ServeHTTP))
}

// page is the data every template gets.
type page struct {
	Title string
	Admin string
	CSRF  string
	Flash string
	Error string
	Data  any
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name, title string, data any) {
	t, ok := h.pages[name]
	if !ok {
		http.Error(w, "page not found", http.StatusInternalServerError)
		return
	}
	email := Admin(r.Context())
	p := page{Title: title, Admin: email, CSRF: h.csrfToken(email, h.Clock.Now()), Data: data,
		Flash: r.URL.Query().Get("ok"), Error: r.URL.Query().Get("err")}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, p); err != nil {
		h.Log.Error("admin render", "page", name, "error", err)
	}
}

// redirect implements post/redirect/get with a short message. Messages are
// fixed strings chosen by the handler, never user input.
func redirect(w http.ResponseWriter, r *http.Request, path, okMsg, errMsg string) {
	q := ""
	switch {
	case errMsg != "":
		q = "?err=" + urlEscape(errMsg)
	case okMsg != "":
		q = "?ok=" + urlEscape(okMsg)
	}
	http.Redirect(w, r, path+q, http.StatusSeeOther)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	h.Log.Error("admin", "path", r.URL.Path, "request_id", logging.RequestID(r.Context()), "error", err)
	http.Error(w, msg, http.StatusInternalServerError)
}

func urlEscape(s string) string { return url.QueryEscape(s) }
