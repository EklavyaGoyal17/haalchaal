// Package fake is the default VoiceProvider in dev and tests. It never
// contacts anyone. Webhooks use a simple signed JSON format so the real
// handlers, including signature checks, run unchanged in simcall and tests.
package fake

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// SignatureHeader carries hex(HMAC-SHA256(secret, body)).
const SignatureHeader = "X-Fake-Signature"

// MaxBody bounds webhook bodies.
const MaxBody = 1 << 20

// Provider records requested calls in memory.
type Provider struct {
	Secret []byte

	mu       sync.Mutex
	calls    []voice.CallRequest
	byCall   map[string]string // our call id -> provider call id
	failNext error
}

// New returns a fake provider that signs and verifies with secret.
func New(secret string) *Provider {
	return &Provider{Secret: []byte(secret), byCall: map[string]string{}}
}

func (p *Provider) Name() string { return "fake" }

// FailNextStart makes the next StartCall return err (for tests).
func (p *Provider) FailNextStart(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failNext = err
}

// StartCall records the request and returns a fresh provider call id.
func (p *Provider) StartCall(_ context.Context, req voice.CallRequest) (voice.StartedCall, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.failNext; err != nil {
		p.failNext = nil
		return voice.StartedCall{}, err
	}
	id := "fake-" + uuid.NewString()
	p.calls = append(p.calls, req)
	p.byCall[req.CallID] = id
	return voice.StartedCall{ProviderCallID: id}, nil
}

// Calls returns every request so far.
func (p *Provider) Calls() []voice.CallRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]voice.CallRequest(nil), p.calls...)
}

// ProviderCallID returns the provider id given to one of our calls.
func (p *Provider) ProviderCallID(callID string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.byCall[callID]
	return id, ok
}

// WireEvent is the fake webhook body format: {"events": [...]}.
type WireEvent struct {
	EventID        string     `json:"event_id"`
	ProviderCallID string     `json:"provider_call_id"`
	CallID         string     `json:"call_id,omitempty"`
	Type           string     `json:"type"`
	At             time.Time  `json:"at"`
	DurationSec    int        `json:"duration_sec,omitempty"`
	CostPaise      int64      `json:"cost_paise,omitempty"`
	Transcript     []WireTurn `json:"transcript,omitempty"`
}

// WireTurn is a transcript turn on the wire; offset in milliseconds.
type WireTurn struct {
	Speaker  string `json:"speaker"`
	Text     string `json:"text"`
	OffsetMS int64  `json:"offset_ms"`
}

// WireTool is the fake tool-call body.
type WireTool struct {
	EventID        string            `json:"event_id"`
	ProviderCallID string            `json:"provider_call_id"`
	CallID         string            `json:"call_id,omitempty"`
	Args           map[string]string `json:"args"`
}

// Sign returns the signature header value for body.
func (p *Provider) Sign(body []byte) string {
	m := hmac.New(sha256.New, p.Secret)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func (p *Provider) verifiedBody(r *http.Request) ([]byte, error) {
	if len(p.Secret) == 0 {
		return nil, voice.ErrBadSignature // fail closed without a secret
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read body", voice.ErrBadPayload)
	}
	if len(body) > MaxBody {
		return nil, fmt.Errorf("%w: body too large", voice.ErrBadPayload)
	}
	got, err := hex.DecodeString(r.Header.Get(SignatureHeader))
	if err != nil || !hmac.Equal(got, mustHex(p.Sign(body))) {
		return nil, voice.ErrBadSignature
	}
	return body, nil
}

func mustHex(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", voice.ErrBadPayload, err)
	}
	return nil
}

// ParseWebhook verifies the signature and decodes events.
func (p *Provider) ParseWebhook(r *http.Request) ([]voice.Event, error) {
	body, err := p.verifiedBody(r)
	if err != nil {
		return nil, err
	}
	var w struct {
		Events []WireEvent `json:"events"`
	}
	if err := decodeStrict(body, &w); err != nil {
		return nil, err
	}
	out := make([]voice.Event, 0, len(w.Events))
	for _, e := range w.Events {
		t := voice.EventType(e.Type)
		if e.EventID == "" || (e.ProviderCallID == "" && e.CallID == "") || !t.Valid() {
			return nil, fmt.Errorf("%w: event missing id, call or valid type", voice.ErrBadPayload)
		}
		ev := voice.Event{
			EventID: e.EventID, ProviderCallID: e.ProviderCallID, CallID: e.CallID, Type: t,
			At: e.At.UTC(), DurationSec: e.DurationSec, CostPaise: e.CostPaise,
		}
		for _, tr := range e.Transcript {
			ev.Transcript = append(ev.Transcript, voice.Turn{Speaker: tr.Speaker, Text: tr.Text, Offset: time.Duration(tr.OffsetMS) * time.Millisecond})
		}
		out = append(out, ev)
	}
	return out, nil
}

// ParseToolCall verifies the signature and decodes one tool call. The tool
// name comes from the URL path, set by the router.
func (p *Provider) ParseToolCall(r *http.Request) (voice.ToolCall, error) {
	body, err := p.verifiedBody(r)
	if err != nil {
		return voice.ToolCall{}, err
	}
	var w WireTool
	if err := decodeStrict(body, &w); err != nil {
		return voice.ToolCall{}, err
	}
	if w.EventID == "" || (w.ProviderCallID == "" && w.CallID == "") {
		return voice.ToolCall{}, errors.Join(voice.ErrBadPayload, errors.New("tool call missing id or call"))
	}
	return voice.ToolCall{EventID: w.EventID, ProviderCallID: w.ProviderCallID, CallID: w.CallID, Tool: r.PathValue("tool"), Args: w.Args}, nil
}
