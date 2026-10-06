package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ParseUsers reads ADMIN_USERS ("email:bcrypt_hash,..."). Every hash must be
// a bcrypt hash with cost 10 or more.
func ParseUsers(s string) (map[string][]byte, error) {
	users := map[string][]byte{}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		email, hash, ok := strings.Cut(entry, ":")
		email = strings.ToLower(strings.TrimSpace(email))
		if !ok || email == "" || !strings.Contains(email, "@") {
			return nil, errors.New("ADMIN_USERS entries must look like email:bcrypt_hash")
		}
		cost, err := bcrypt.Cost([]byte(hash))
		if err != nil {
			return nil, fmt.Errorf("ADMIN_USERS hash for %s is not a bcrypt hash", email)
		}
		if cost < 10 {
			return nil, fmt.Errorf("ADMIN_USERS hash for %s has cost %d; use 10 or more", email, cost)
		}
		if _, dup := users[email]; dup {
			return nil, fmt.Errorf("ADMIN_USERS lists %s twice", email)
		}
		users[email] = []byte(hash)
	}
	return users, nil
}

// HashPassword returns a bcrypt hash for ADMIN_USERS.
func HashPassword(pw string) (string, error) {
	if len(pw) < 12 {
		return "", errors.New("use a password of at least 12 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h), err
}

// dummyHash keeps the timing of unknown-user logins close to real ones.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), 10)

// limiter blocks an address after too many failed logins.
type limiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	max      int
	window   time.Duration
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{failures: map[string][]time.Time{}, max: max, window: window}
}

func (l *limiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key, now)
	return len(l.failures[key]) >= l.max
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key, now)
	l.failures[key] = append(l.failures[key], now)
	if len(l.failures) > 10000 { // bound memory under a spray from many addresses
		for k := range l.failures {
			l.prune(k, now)
		}
	}
}

func (l *limiter) prune(key string, now time.Time) {
	fs := l.failures[key]
	i := 0
	for ; i < len(fs) && now.Sub(fs[i]) > l.window; i++ {
	}
	if i == len(fs) {
		delete(l.failures, key)
		return
	}
	l.failures[key] = fs[i:]
}

type ctxKey struct{}

// Admin returns the authenticated admin's email from a request context.
func Admin(ctx context.Context) string {
	s, _ := ctx.Value(ctxKey{}).(string)
	return s
}

// clientIP is the connecting address, or, when that address is a trusted
// proxy, the right-most untrusted address in X-Forwarded-For. Without
// trusted proxies the header is ignored, since any client can set it.
func (h *Handler) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !h.trusted(host) {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := strings.TrimSpace(parts[i])
		if net.ParseIP(ip) == nil {
			break
		}
		if !h.trusted(ip) {
			return ip
		}
	}
	return host
}

func (h *Handler) trusted(ip string) bool {
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	for _, n := range h.TrustedProxies {
		if n.Contains(p) {
			return true
		}
	}
	return false
}

// secure reports whether the request arrived over TLS, directly or through a
// trusted proxy that says so.
func (h *Handler) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return h.trusted(host) && r.Header.Get("X-Forwarded-Proto") == "https"
}

// requireAdmin enforces HTTP Basic auth against ADMIN_USERS, with failures
// rate-limited per client address and per account.
func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.RequireTLS && !h.secure(r) {
			// Basic auth sends the password with every request: never over
			// plain HTTP in production.
			http.Error(w, "admin pages require HTTPS", http.StatusForbidden)
			return
		}
		now := h.Clock.Now()
		ip := h.clientIP(r)
		if h.limiter.blocked("ip:"+ip, now) {
			w.Header().Set("Retry-After", "900")
			http.Error(w, "too many failed logins; try again later", http.StatusTooManyRequests)
			return
		}
		email, pw, ok := r.BasicAuth()
		email = strings.ToLower(strings.TrimSpace(email))
		if ok && h.limiter.blocked("user:"+email, now) {
			w.Header().Set("Retry-After", "900")
			http.Error(w, "too many failed logins; try again later", http.StatusTooManyRequests)
			return
		}
		hash, known := h.Users[email]
		if !known {
			hash = dummyHash
		}
		if !ok || bcrypt.CompareHashAndPassword(hash, []byte(pw)) != nil || !known {
			if ok {
				h.limiter.fail("ip:"+ip, now)
				h.limiter.fail("user:"+email, now)
				h.Log.Warn("admin login failed", "ip", ip)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="HaalChaal admin", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, email)))
	})
}

// csrfTTL is how long a form token stays valid.
const csrfTTL = 12 * time.Hour

// csrfToken binds a token to the admin and the time it was issued.
func (h *Handler) csrfToken(email string, now time.Time) string {
	ts := make([]byte, 8)
	binary.BigEndian.PutUint64(ts, uint64(now.Unix()))
	m := hmac.New(sha256.New, h.CSRFKey)
	m.Write([]byte(email))
	m.Write(ts)
	return base64.RawURLEncoding.EncodeToString(append(ts, m.Sum(nil)...))
}

func (h *Handler) validCSRF(email, token string, now time.Time) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 8+sha256.Size {
		return false
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(raw[:8])), 0)
	if now.Sub(issued) > csrfTTL || issued.After(now.Add(time.Minute)) {
		return false
	}
	m := hmac.New(sha256.New, h.CSRFKey)
	m.Write([]byte(email))
	m.Write(raw[:8])
	return subtle.ConstantTimeCompare(m.Sum(nil), raw[8:]) == 1
}

// checkForm parses a POST form (bounded) and verifies the CSRF token and,
// when the browser sends one, that Origin matches the host.
func (h *Handler) checkForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			http.Error(w, "cross-origin form rejected", http.StatusForbidden)
			return false
		}
	}
	if !h.validCSRF(Admin(r.Context()), r.PostForm.Get("csrf"), h.Clock.Now()) {
		http.Error(w, "form expired or invalid; reload the page and try again", http.StatusForbidden)
		return false
	}
	return true
}
