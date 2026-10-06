package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
)

func TestSendTemplate(t *testing.T) {
	var got map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.123"}]}`))
	}))
	defer srv.Close()
	c, err := New("v21.0", "555", "tok", "sec", "ver")
	if err != nil {
		t.Fatal(err)
	}
	c.BaseURL = srv.URL
	id, err := c.SendTemplate(context.Background(), notify.TemplateMessage{
		To: "+919876541234", Template: notify.TplAlertEmergency, Language: "hi",
		Params:  []string{"Kamla ji", "gir\ngayi"},
		Buttons: []notify.QuickReply{{Title: notify.AckTitle, Payload: "ack:x"}},
	})
	if err != nil || id != "wamid.123" {
		t.Fatalf("%q %v", id, err)
	}
	if auth != "Bearer tok" || path != "/v21.0/555/messages" {
		t.Errorf("auth %q path %q", auth, path)
	}
	b, _ := json.Marshal(got)
	s := string(b)
	for _, want := range []string{`"to":"919876541234"`, `"name":"alert_emergency_v1"`, `"code":"hi"`, `"text":"gir gayi"`, `"payload":"ack:x"`, `"sub_type":"quick_reply"`} {
		if !strings.Contains(s, want) {
			t.Errorf("request missing %s: %s", want, s)
		}
	}
}

func TestSendTemplateErrorHidesParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"Param SECRET-QUOTE invalid","type":"OAuthException","code":132000}}`))
	}))
	defer srv.Close()
	c, _ := New("v21.0", "555", "tok", "sec", "ver")
	c.BaseURL = srv.URL
	_, err := c.SendTemplate(context.Background(), notify.TemplateMessage{To: "+919876541234", Template: notify.TplCallPending, Language: "en", Params: []string{"SECRET-QUOTE"}})
	if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "132000") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRequiresConfig(t *testing.T) {
	if _, err := New("", "1", "t", "s", "v"); err == nil {
		t.Fatal("missing version accepted")
	}
}
