// Package voice defines the voice platform interface (SPEC §6). Vendor
// adapters live in subpackages and keep vendor types to themselves.
package voice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// CallRequest asks the platform to place one outbound call.
type CallRequest struct {
	CallID       string            // our attempt id; sent as metadata so webhooks echo it
	To           string            // E.164
	Language     string            // "hi", "ta" or "en"
	SystemPrompt string            // rendered agent prompt (§7)
	Variables    map[string]string // same values, for platforms that template prompts themselves
}

// StartedCall is the platform's handle for a placed call.
type StartedCall struct{ ProviderCallID string }

// EventType is a call status or transcript event.
type EventType string

const (
	EventRinging         EventType = "ringing"
	EventAnswered        EventType = "answered"
	EventCompleted       EventType = "completed"
	EventNoAnswer        EventType = "no_answer"
	EventBusy            EventType = "busy"
	EventFailed          EventType = "failed"
	EventTranscriptReady EventType = "transcript_ready"
)

// Valid reports whether t is a known event type.
func (t EventType) Valid() bool {
	switch t {
	case EventRinging, EventAnswered, EventCompleted, EventNoAnswer, EventBusy, EventFailed, EventTranscriptReady:
		return true
	}
	return false
}

// Turn is one utterance in a transcript.
type Turn struct {
	Speaker string // "agent" or "parent"
	Text    string
	Offset  time.Duration // from call start
}

type storedTurn struct {
	Speaker  string `json:"speaker"`
	Text     string `json:"text"`
	OffsetMS int64  `json:"offset_ms"`
}

// MarshalTurns encodes a transcript as the JSON array stored (encrypted) in
// transcripts.turns_enc: [{speaker, text, offset_ms}].
func MarshalTurns(turns []Turn) ([]byte, error) {
	out := make([]storedTurn, len(turns))
	for i, t := range turns {
		out[i] = storedTurn{Speaker: t.Speaker, Text: t.Text, OffsetMS: t.Offset.Milliseconds()}
	}
	return json.Marshal(out)
}

// UnmarshalTurns decodes MarshalTurns output.
func UnmarshalTurns(b []byte) ([]Turn, error) {
	var in []storedTurn
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	out := make([]Turn, len(in))
	for i, t := range in {
		out[i] = Turn{Speaker: t.Speaker, Text: t.Text, Offset: time.Duration(t.OffsetMS) * time.Millisecond}
	}
	return out, nil
}

// Event is one webhook event, already verified and mapped to our types.
type Event struct {
	EventID        string // provider's unique id, used for dedupe
	ProviderCallID string
	CallID         string // our id echoed from metadata, if available
	Type           EventType
	At             time.Time
	DurationSec    int
	CostPaise      int64
	Transcript     []Turn // set for EventTranscriptReady, or on EventCompleted if bundled
}

// Mid-call tools (SPEC §7).
const (
	ToolReportRedFlag     = "report_red_flag"
	ToolReportScam        = "report_scam"
	ToolReportStopRequest = "report_stop_request"
)

// ToolCall is a verified mid-call function call.
type ToolCall struct {
	EventID        string
	ProviderCallID string
	CallID         string // our id echoed from metadata, if available
	Tool           string
	Args           map[string]string
}

// Provider is a voice platform.
type Provider interface {
	Name() string
	StartCall(ctx context.Context, req CallRequest) (StartedCall, error)
	ParseWebhook(r *http.Request) ([]Event, error)   // verifies the signature first
	ParseToolCall(r *http.Request) (ToolCall, error) // verifies the signature first
}

// ErrBadSignature means a webhook failed verification. Handlers answer 401
// and never process the body.
var ErrBadSignature = errors.New("voice: bad webhook signature")

// ErrBadPayload means a verified webhook could not be parsed.
var ErrBadPayload = errors.New("voice: bad webhook payload")
