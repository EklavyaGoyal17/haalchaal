package admin

import (
	"crypto/tls"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

func cidr(s string) *net.IPNet { _, n, _ := net.ParseCIDR(s); return n }

func TestClientIP(t *testing.T) {
	h := &Handler{TrustedProxies: []*net.IPNet{cidr("10.0.0.0/8")}}
	tests := []struct {
		remote, xff, want string
	}{
		{"203.0.113.9:5000", "", "203.0.113.9"},
		{"203.0.113.9:5000", "1.2.3.4", "203.0.113.9"},              // untrusted peer: header ignored
		{"10.0.0.5:5000", "198.51.100.7", "198.51.100.7"},           // via the load balancer
		{"10.0.0.5:5000", "6.6.6.6, 198.51.100.7", "198.51.100.7"},  // spoofed left-most entry ignored
		{"10.0.0.5:5000", "198.51.100.7, 10.0.0.9", "198.51.100.7"}, // chain of trusted proxies
		{"10.0.0.5:5000", "garbage", "10.0.0.5"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/admin", nil)
		r.RemoteAddr = tt.remote
		if tt.xff != "" {
			r.Header.Set("X-Forwarded-For", tt.xff)
		}
		if got := h.clientIP(r); got != tt.want {
			t.Errorf("%s %q -> %s, want %s", tt.remote, tt.xff, got, tt.want)
		}
	}
}

func TestSecure(t *testing.T) {
	h := &Handler{TrustedProxies: []*net.IPNet{cidr("10.0.0.0/8")}}
	r := httptest.NewRequest("GET", "/admin", nil)
	r.RemoteAddr = "203.0.113.9:1"
	r.Header.Set("X-Forwarded-Proto", "https")
	if h.secure(r) {
		t.Error("spoofed proto from untrusted peer accepted")
	}
	r.RemoteAddr = "10.0.0.5:1"
	if !h.secure(r) {
		t.Error("trusted proxy https rejected")
	}
	r2 := httptest.NewRequest("GET", "/admin", nil)
	r2.TLS = &tls.ConnectionState{}
	if !h.secure(r2) {
		t.Error("direct TLS rejected")
	}
}

func TestCSRFToken(t *testing.T) {
	h := &Handler{CSRFKey: make([]byte, 32)}
	now := time.Date(2026, 10, 6, 5, 0, 0, 0, time.UTC)
	tok := h.csrfToken("a@x", now)
	if !h.validCSRF("a@x", tok, now.Add(time.Hour)) {
		t.Fatal("valid token rejected")
	}
	if h.validCSRF("b@x", tok, now) {
		t.Error("token valid for another admin")
	}
	if h.validCSRF("a@x", tok, now.Add(13*time.Hour)) {
		t.Error("expired token accepted")
	}
	if h.validCSRF("a@x", tok[:len(tok)-2]+"AA", now) || h.validCSRF("a@x", "", now) || h.validCSRF("a@x", "!!!", now) {
		t.Error("tampered token accepted")
	}
	other := &Handler{CSRFKey: []byte("another key of thirty-two bytes!")}
	if other.validCSRF("a@x", tok, now) {
		t.Error("token valid under another key")
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(3, time.Minute)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 3 {
		if l.blocked("k", t0) {
			t.Fatalf("blocked after %d", i)
		}
		l.fail("k", t0)
	}
	if !l.blocked("k", t0) {
		t.Fatal("not blocked after 3")
	}
	if l.blocked("k", t0.Add(2*time.Minute)) {
		t.Fatal("still blocked after the window")
	}
}

func TestParseUsers(t *testing.T) {
	h, _ := HashPassword("a long enough password")
	u, err := ParseUsers("Founder@Example.com:" + h + ", ")
	if err != nil || len(u) != 1 || u["founder@example.com"] == nil {
		t.Fatalf("%v %v", u, err)
	}
	for _, bad := range []string{"nobody", "x@y:notahash", "noat:" + h, "a@b:" + h + ",a@b:" + h} {
		if _, err := ParseUsers(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := HashPassword("short"); err == nil {
		t.Error("short password accepted")
	}
}
