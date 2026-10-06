package notify

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCleanParam(t *testing.T) {
	if got := CleanParam("  line1\n\tline2     x  "); got != "line1 line2 x" {
		t.Errorf("got %q", got)
	}
	if got := CleanParam(strings.Repeat("a", 2000)); len([]rune(got)) != MaxParam {
		t.Errorf("len %d", len([]rune(got)))
	}
}

func TestValidate(t *testing.T) {
	ok := TemplateMessage{To: "+919876541234", Template: TplDailySummary, Language: "en", Params: []string{"Kamla ji", "all well"}}
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]TemplateMessage{
		"unknown":     {Template: "promo_v1", Params: []string{"x"}},
		"param count": {Template: TplDailySummary, Params: []string{"x"}},
		"empty param": {Template: TplDailySummary, Params: []string{"x", "  "}},
	} {
		if Validate(m) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAck(t *testing.T) {
	if id, ok := ParseAck(AckPayload("abc")); !ok || id != "abc" {
		t.Fatal(id, ok)
	}
	for _, p := range []string{"ack:", "nack:abc", "", "abc"} {
		if _, ok := ParseAck(p); ok {
			t.Errorf("%q parsed", p)
		}
	}
}

func TestRender(t *testing.T) {
	if got := Render(Templates[TplAlertEmergency], []string{"Kamla ji", "gir gayi"}); !strings.Contains(got, `Kamla ji said "gir gayi"`) {
		t.Fatal(got)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	secret := []byte("app-secret")
	body := MetaInboundBody(
		InboundEvent{Kind: KindButton, From: "+919876541234", Payload: "ack:a1", MessageID: "wamid.1"},
		InboundEvent{Kind: KindText, From: "+919876541234", Payload: "please resume", MessageID: "wamid.2"},
		InboundEvent{Kind: KindStatus, From: "+919876541234", Payload: "delivered", MessageID: "wamid.out"},
	)
	req := httptest.NewRequest("POST", "/v1/webhooks/whatsapp", bytes.NewReader(body))
	req.Header.Set(MetaSignatureHeader, MetaSign(secret, body))
	got, err := ReadVerifiedMetaBody(req, secret)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := ParseMetaPayload(got)
	if err != nil || len(evs) != 3 {
		t.Fatalf("%v %v", evs, err)
	}
	if evs[0].Kind != KindButton || evs[0].Payload != "ack:a1" || evs[0].From != "+919876541234" || evs[0].EventID != "msg:wamid.1" {
		t.Errorf("button %+v", evs[0])
	}
	if evs[1].Kind != KindText || evs[1].Payload != "please resume" {
		t.Errorf("text %+v", evs[1])
	}
	if evs[2].Kind != KindStatus || evs[2].MessageID != "wamid.out" || evs[2].EventID != "status:wamid.out:delivered" {
		t.Errorf("status %+v", evs[2])
	}
}

func TestMetaSignatureRejected(t *testing.T) {
	body := MetaInboundBody(InboundEvent{Kind: KindText, From: "+919876541234", Payload: "x", MessageID: "m"})
	for name, sig := range map[string]string{
		"missing":    "",
		"no prefix":  strings.TrimPrefix(MetaSign([]byte("s"), body), "sha256="),
		"wrong key":  MetaSign([]byte("other"), body),
		"not hex":    "sha256=zz",
		"other body": MetaSign([]byte("s"), []byte("{}")),
	} {
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		if sig != "" {
			req.Header.Set(MetaSignatureHeader, sig)
		}
		if _, err := ReadVerifiedMetaBody(req, []byte("s")); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err %v", name, err)
		}
	}
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.Header.Set(MetaSignatureHeader, MetaSign(nil, body))
	if _, err := ReadVerifiedMetaBody(req, nil); !errors.Is(err, ErrBadSignature) {
		t.Error("empty secret accepted")
	}
}

func TestMetaPayloadRejectsBadSender(t *testing.T) {
	body := []byte(`{"entry":[{"changes":[{"value":{"messages":[{"from":"abc","id":"m","type":"text","text":{"body":"x"}}]}}]}]}`)
	if _, err := ParseMetaPayload(body); !errors.Is(err, ErrBadPayload) {
		t.Fatal(err)
	}
}
