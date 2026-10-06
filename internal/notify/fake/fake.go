// Package fake is the default Messenger in dev and tests. It stores sent
// messages in memory and logs a masked line; it never contacts anyone.
// Inbound webhooks use Meta's real format and signature, so the real handler
// code runs unchanged.
package fake

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/EklavyaGoyal17/haalchaal/internal/logging"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
)

// Sent is one recorded message.
type Sent struct {
	ID string
	notify.TemplateMessage
	Body string // rendered English body
}

// Messenger records messages.
type Messenger struct {
	AppSecret []byte
	Verify    string
	Log       *slog.Logger

	mu       sync.Mutex
	sent     []Sent
	seq      int
	failNext error
}

// New returns a fake messenger.
func New(appSecret, verifyToken string, log *slog.Logger) *Messenger {
	return &Messenger{AppSecret: []byte(appSecret), Verify: verifyToken, Log: log}
}

func (m *Messenger) Name() string        { return "whatsapp" }
func (m *Messenger) VerifyToken() string { return m.Verify }

// FailNextSend makes the next SendTemplate fail (for tests).
func (m *Messenger) FailNextSend(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext = err
}

// SendTemplate validates and records the message.
func (m *Messenger) SendTemplate(_ context.Context, msg notify.TemplateMessage) (string, error) {
	if err := notify.Validate(msg); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failNext; err != nil {
		m.failNext = nil
		return "", err
	}
	m.seq++
	id := fmt.Sprintf("wamid.fake.%d", m.seq)
	m.sent = append(m.sent, Sent{ID: id, TemplateMessage: msg, Body: notify.Render(notify.Templates[msg.Template], msg.Params)})
	if m.Log != nil {
		m.Log.Info("fake whatsapp sent", "to", logging.MaskPhone(msg.To), "template", msg.Template, "message_id", id)
	}
	return id, nil
}

// Sent returns every recorded message.
func (m *Messenger) Sent() []Sent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Sent(nil), m.sent...)
}

// ParseWebhook verifies X-Hub-Signature-256 and parses Meta's format.
func (m *Messenger) ParseWebhook(r *http.Request) ([]notify.InboundEvent, error) {
	body, err := notify.ReadVerifiedMetaBody(r, m.AppSecret)
	if err != nil {
		return nil, err
	}
	return notify.ParseMetaPayload(body)
}

// Sign returns the signature header for body.
func (m *Messenger) Sign(body []byte) string { return notify.MetaSign(m.AppSecret, body) }

var _ notify.Messenger = (*Messenger)(nil)
