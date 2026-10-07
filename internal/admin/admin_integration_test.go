//go:build integration

package admin_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/EklavyaGoyal17/haalchaal/internal/admin"
	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/httpapi"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice/fake"
)

const (
	email = "founder@haalchaal.test"
	pw    = "correct horse battery staple"
)

type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	srv   *httptest.Server
	clk   *clock.Fake
	voice *fake.Provider
	svc   *calls.Service
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := testdb.New(t)
	k, _ := crypto.GenerateKey()
	kr, _ := crypto.ParseKeyring("1:"+k, "1")
	hash, _ := bcrypt.GenerateFromPassword([]byte(pw), 10)
	clk := clock.NewFake(time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)) // 09:30 IST
	v := fake.New("s")
	ist, _ := time.LoadLocation("Asia/Kolkata")
	svc := &calls.Service{Pool: pool, Clock: clk, Log: testdb.Log(), Keyring: kr, Voice: v,
		Gate:         safety.Gate{Env: config.EnvDev, CallsEnabled: true, DevAllowlist: []string{"+919876500001"}},
		RetryOffsets: []time.Duration{15 * time.Minute}}
	h, err := admin.New(admin.Handler{Pool: pool, Clock: clk, Log: testdb.Log(), Keyring: kr,
		Users: map[string][]byte{email: hash}, CSRFKey: kr.DeriveKey("csrf"), Calls: svc, Location: ist})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&httpapi.Server{Log: testdb.Log(), Admin: h}).Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, pool: pool, srv: srv, clk: clk, voice: v, svc: svc}
}

func (e *env) do(method, path string, form url.Values, auth bool) (*http.Response, string) {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if auth {
		req.SetBasicAuth(email, pw)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (e *env) csrf(path string) string {
	e.t.Helper()
	resp, body := e.do("GET", path, nil, true)
	if resp.StatusCode != 200 {
		e.t.Fatalf("GET %s: %d", path, resp.StatusCode)
	}
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		e.t.Fatalf("no csrf token on %s", path)
	}
	return m[1]
}

func onboardValues(token string) url.Values {
	return url.Values{
		"csrf": {token}, "plan": {"daily"}, "parent_name": {"Kamla ji"}, "parent_phone": {"98765 00001"}, "language": {"hi"},
		"call_time": {"10:00"}, "window_start": {"09:00"}, "window_end": {"20:00"}, "timezone": {"Asia/Kolkata"},
		"interests": {"bhajans, cricket"}, "safe_word": {"gulab"},
		"member_name": {"Rahul", "", ""}, "member_phone": {"+91 98765 00002", "", ""}, "member_relation": {"son", "", ""},
		"member_language": {"en", "en", "en"}, "member_optin_0": {"yes"},
		"med_name": {"Amlodipine 5mg", "", "", "", ""}, "med_timing": {"after breakfast", "", "", "", ""},
		"consent_calls": {"yes"}, "consent_data": {"yes"}, "consent_share": {"yes"}, "given_by": {"parent"},
		"text_version": {"consent-hi-v1"}, "evidence_ref": {"form 17"}, "activate": {"yes"},
	}
}

func TestAuthRequired(t *testing.T) {
	e := setup(t)
	for _, p := range []string{"/admin", "/admin/review", "/admin/parents", "/admin/accounts/new", "/admin/static/admin.css"} {
		if resp, _ := e.do("GET", p, nil, false); resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s without auth: %d", p, resp.StatusCode)
		}
	}
	// Wrong passwords are rate limited.
	for range 5 {
		req, _ := http.NewRequest("GET", e.srv.URL+"/admin", nil)
		req.SetBasicAuth(email, "wrong")
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
	}
	if resp, _ := e.do("GET", "/admin", nil, true); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after 5 failures: %d, want 429", resp.StatusCode)
	}
}

func TestCSRFRequired(t *testing.T) {
	e := setup(t)
	form := onboardValues("bogus")
	if resp, _ := e.do("POST", "/admin/accounts/new", form, true); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad token: %d", resp.StatusCode)
	}
	form.Del("csrf")
	if resp, _ := e.do("POST", "/admin/accounts/new", form, true); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM parents`).Scan(&n)
	if n != 0 {
		t.Fatal("family created without a valid CSRF token")
	}
}

func TestOnboardValidation(t *testing.T) {
	e := setup(t)
	form := onboardValues(e.csrf("/admin/accounts/new"))
	form.Set("parent_phone", "12345")
	form.Set("consent_share", "")
	resp, body := e.do("POST", "/admin/accounts/new", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "phone number is not valid") || !strings.Contains(body, "Calls can only start") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `value="Kamla ji"`) {
		t.Fatal("form values not kept")
	}
}

// TestOnboardEndToEnd: onboarding through the admin page alone yields a
// parent the scheduler calls, with encrypted fields and audit rows.
func TestOnboardEndToEnd(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	resp, _ := e.do("POST", "/admin/accounts/new", onboardValues(e.csrf("/admin/accounts/new")), true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("onboard: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/admin/parents/") {
		t.Fatalf("redirect %q", loc)
	}
	parentPath := strings.SplitN(loc, "?", 2)[0]

	var phone, status string
	var safeWord []byte
	if err := e.pool.QueryRow(ctx, `SELECT phone_e164, status, safe_word_enc FROM parents`).Scan(&phone, &status, &safeWord); err != nil {
		t.Fatal(err)
	}
	if phone != "+919876500001" || status != "active" || strings.Contains(string(safeWord), "gulab") {
		t.Fatalf("%s %s", phone, status)
	}
	var audits int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE actor = 'admin:'||$1`, email).Scan(&audits)
	if audits < 4 { // 3 consents + onboarding
		t.Fatalf("audit rows %d", audits)
	}

	// Duplicate phone is refused politely.
	resp, body := e.do("POST", "/admin/accounts/new", onboardValues(e.csrf("/admin/accounts/new")), true)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "already registered") {
		t.Fatalf("duplicate: %d", resp.StatusCode)
	}

	// The scheduler picks the parent up at call time and the call goes out.
	e.clk.Set(time.Date(2026, 10, 6, 4, 30, 0, 0, time.UTC)) // 10:00 IST
	if _, err := (&scheduler.Scheduler{Pool: e.pool, Clock: e.clk, Log: testdb.Log(), Provider: "fake"}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	w := &jobs.Worker{DB: e.pool, Clock: e.clk, Log: testdb.Log()}
	w.Handle(jobs.KindPlaceCall, e.svc.PlaceCall)
	if _, err := w.RunOnce(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	reqs := e.voice.Calls()
	if len(reqs) != 1 || !strings.Contains(reqs[0].SystemPrompt, "Kamla ji") || !strings.Contains(reqs[0].SystemPrompt, "gulab") ||
		!strings.Contains(reqs[0].SystemPrompt, "Amlodipine 5mg (after breakfast)") || !strings.Contains(reqs[0].SystemPrompt, "bhajans") {
		t.Fatalf("call requests %+v", reqs)
	}

	// Parent page renders decrypted medicines; call detail writes an audit row.
	if resp, body := e.do("GET", parentPath, nil, true); resp.StatusCode != 200 || !strings.Contains(body, "Amlodipine 5mg") || strings.Contains(body, "+919876500001") {
		t.Fatalf("parent page: %d (phone must be masked)", resp.StatusCode)
	}
	var callID string
	_ = e.pool.QueryRow(ctx, `SELECT id FROM calls`).Scan(&callID)
	if resp, _ := e.do("GET", "/admin/calls/"+callID, nil, true); resp.StatusCode != 200 {
		t.Fatalf("call page %d", resp.StatusCode)
	}
	var views int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'view_call' AND entity_id = $1`, callID).Scan(&views)
	if views != 1 {
		t.Fatalf("view_call audit rows %d", views)
	}

	// Withdrawing the calls consent stops the parent and cancels what is scheduled.
	resp, _ = e.do("POST", parentPath+"/consents", url.Values{"csrf": {e.csrf(parentPath)}, "action": {"withdraw"}, "kind": {"calls"}}, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("withdraw %d", resp.StatusCode)
	}
	_ = e.pool.QueryRow(ctx, `SELECT status FROM parents`).Scan(&status)
	if status != "stopped" {
		t.Fatalf("status after withdrawal %s", status)
	}
	// Resume is refused without the consent.
	resp, _ = e.do("POST", parentPath+"/status", url.Values{"csrf": {e.csrf(parentPath)}, "status": {"active"}, "reason": {"family asked"}}, true)
	if !strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Fatal("resumed without consent")
	}

	// Export contains decrypted data and is audited.
	resp, body = e.do("GET", parentPath+"/export", nil, true)
	var doc map[string]any
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &doc) != nil || !strings.Contains(body, "gulab") {
		t.Fatalf("export %d", resp.StatusCode)
	}

	// Erase needs the exact confirmation, then removes content.
	id := strings.TrimPrefix(parentPath, "/admin/parents/")
	resp, _ = e.do("POST", parentPath+"/erase", url.Values{"csrf": {e.csrf(parentPath)}, "confirm": {"erase"}}, true)
	if !strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Fatal("erased without confirmation")
	}
	resp, _ = e.do("POST", parentPath+"/erase", url.Values{"csrf": {e.csrf(parentPath)}, "confirm": {"ERASE " + id[:8]}}, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("erase %d", resp.StatusCode)
	}
	var name string
	var meds int
	_ = e.pool.QueryRow(ctx, `SELECT preferred_name, phone_e164, (SELECT count(*) FROM medicines) FROM parents`).Scan(&name, &phone, &meds)
	if name != "Erased" || !strings.HasPrefix(phone, "+999") || meds != 0 {
		t.Fatalf("after erase: %s %s %d", name, phone, meds)
	}
}

func TestTestCallRespectsGuards(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	form := onboardValues(e.csrf("/admin/accounts/new"))
	form.Set("parent_phone", "+919876500009") // not on the allowlist
	resp, _ := e.do("POST", "/admin/accounts/new", form, true)
	parentPath := strings.SplitN(resp.Header.Get("Location"), "?", 2)[0]
	resp, _ = e.do("POST", parentPath+"/test-call", url.Values{"csrf": {e.csrf(parentPath)}}, true)
	if resp.StatusCode != http.StatusSeeOther || strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Fatalf("test call: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	w := &jobs.Worker{DB: e.pool, Clock: e.clk, Log: testdb.Log()}
	w.Handle(jobs.KindPlaceCall, e.svc.PlaceCall)
	if _, err := w.RunOnce(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	_ = e.pool.QueryRow(ctx, `SELECT status, end_reason FROM calls`).Scan(&status, &reason)
	if status != "cancelled" || reason != "not_in_dev_allowlist" || len(e.voice.Calls()) != 0 {
		t.Fatalf("test call to unlisted number: %s %s", status, reason)
	}
}

func TestSeedDemo(t *testing.T) {
	pool := testdb.New(t)
	k, _ := crypto.GenerateKey()
	kr, _ := crypto.ParseKeyring("1:"+k, "1")
	ist, _ := time.LoadLocation("Asia/Kolkata")
	h, err := admin.New(admin.Handler{Pool: pool, Clock: clock.NewFake(time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)), Log: testdb.Log(),
		Keyring: kr, Users: map[string][]byte{email: []byte("$2a$10$x")}, CSRFKey: kr.DeriveKey("csrf"), Location: ist})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, err := h.SeedDemo(ctx); err != nil || n != 4 {
		t.Fatalf("first seed: %d, %v", n, err)
	}
	if n, err := h.SeedDemo(ctx); err != nil || n != 0 {
		t.Fatalf("second seed must add nothing: %d, %v", n, err)
	}
	var active, paused int
	_ = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'active'), count(*) FILTER (WHERE status <> 'active') FROM parents`).Scan(&active, &paused)
	if active != 3 || paused != 1 {
		t.Fatalf("active %d, not active %d", active, paused)
	}
	var audits int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'onboard_family'`).Scan(&audits)
	if audits != 4 {
		t.Fatalf("onboarding audit rows %d", audits)
	}
}
