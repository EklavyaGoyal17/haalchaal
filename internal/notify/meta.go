package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/EklavyaGoyal17/haalchaal/internal/domain"
)

// MetaSignatureHeader carries "sha256=<hex HMAC-SHA256 of the raw body>".
const MetaSignatureHeader = "X-Hub-Signature-256"

// MaxWebhookBody bounds inbound webhook bodies.
const MaxWebhookBody = 1 << 20

// MetaSign returns the header value Meta would send for body.
func MetaSign(appSecret, body []byte) string {
	m := hmac.New(sha256.New, appSecret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// ReadVerifiedMetaBody reads the body and checks X-Hub-Signature-256 with
// the app secret in constant time. An empty secret rejects everything.
func ReadVerifiedMetaBody(r *http.Request, appSecret []byte) ([]byte, error) {
	if len(appSecret) == 0 {
		return nil, ErrBadSignature
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxWebhookBody+1))
	if err != nil || len(body) > MaxWebhookBody {
		return nil, fmt.Errorf("%w: body unreadable or too large", ErrBadPayload)
	}
	sig, ok := strings.CutPrefix(r.Header.Get(MetaSignatureHeader), "sha256=")
	got, decErr := hex.DecodeString(sig)
	want, _ := hex.DecodeString(strings.TrimPrefix(MetaSign(appSecret, body), "sha256="))
	if !ok || decErr != nil || !hmac.Equal(got, want) {
		return nil, ErrBadSignature
	}
	return body, nil
}

// metaPayload is the subset of Meta's webhook format we read.
type metaPayload struct {
	Object string `json:"object"`
	Entry  []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Messages []struct {
					From   string                 `json:"from"`
					ID     string                 `json:"id"`
					Type   string                 `json:"type"`
					Text   *struct{ Body string } `json:"text"`
					Button *struct {
						Payload string `json:"payload"`
						Text    string `json:"text"`
					} `json:"button"`
					Interactive *struct {
						ButtonReply *struct {
							ID    string `json:"id"`
							Title string `json:"title"`
						} `json:"button_reply"`
					} `json:"interactive"`
				} `json:"messages"`
				Statuses []struct {
					ID          string `json:"id"`
					Status      string `json:"status"`
					RecipientID string `json:"recipient_id"`
				} `json:"statuses"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// ParseMetaPayload decodes a verified Meta webhook body. Unknown message
// types are kept as text events with an empty payload so they still reach
// the admin review queue. Numbers become E.164.
func ParseMetaPayload(body []byte) ([]InboundEvent, error) {
	var p metaPayload
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadPayload, err)
	}
	var out []InboundEvent
	for _, e := range p.Entry {
		for _, c := range e.Changes {
			for _, m := range c.Value.Messages {
				from := toE164(m.From)
				if m.ID == "" || !domain.ValidE164(from) {
					return nil, fmt.Errorf("%w: message without id or valid sender", ErrBadPayload)
				}
				ev := InboundEvent{EventID: "msg:" + m.ID, From: from, Kind: KindText, MessageID: m.ID}
				switch {
				case m.Button != nil:
					ev.Kind, ev.Payload = KindButton, m.Button.Payload
				case m.Interactive != nil && m.Interactive.ButtonReply != nil:
					ev.Kind, ev.Payload = KindButton, m.Interactive.ButtonReply.ID
				case m.Text != nil:
					ev.Payload = m.Text.Body
				}
				out = append(out, ev)
			}
			for _, s := range c.Value.Statuses {
				if s.ID == "" || s.Status == "" {
					return nil, fmt.Errorf("%w: status without id", ErrBadPayload)
				}
				out = append(out, InboundEvent{
					EventID: "status:" + s.ID + ":" + s.Status, From: toE164(s.RecipientID),
					Kind: KindStatus, Payload: s.Status, MessageID: s.ID,
				})
			}
		}
	}
	return out, nil
}

// toE164 adds the leading plus Meta leaves off.
func toE164(n string) string {
	n = strings.TrimSpace(n)
	if n == "" || strings.HasPrefix(n, "+") {
		return n
	}
	return "+" + n
}

// MetaInboundBody builds a Meta-format webhook body, for the fake and tests.
func MetaInboundBody(events ...InboundEvent) []byte {
	type msg struct {
		From   string            `json:"from"`
		ID     string            `json:"id"`
		Type   string            `json:"type"`
		Text   map[string]string `json:"text,omitempty"`
		Button map[string]string `json:"button,omitempty"`
	}
	type status struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		RecipientID string `json:"recipient_id"`
	}
	var msgs []msg
	var sts []status
	for _, e := range events {
		from := strings.TrimPrefix(e.From, "+")
		switch e.Kind {
		case KindButton:
			msgs = append(msgs, msg{From: from, ID: e.MessageID, Type: "button", Button: map[string]string{"payload": e.Payload, "text": AckTitle}})
		case KindText:
			msgs = append(msgs, msg{From: from, ID: e.MessageID, Type: "text", Text: map[string]string{"body": e.Payload}})
		case KindStatus:
			sts = append(sts, status{ID: e.MessageID, Status: e.Payload, RecipientID: from})
		}
	}
	body, _ := json.Marshal(map[string]any{
		"object": "whatsapp_business_account",
		"entry": []any{map[string]any{"changes": []any{map[string]any{
			"field": "messages",
			"value": map[string]any{"messaging_product": "whatsapp", "messages": msgs, "statuses": sts},
		}}}},
	})
	return body
}
