// Package vapi is the VoiceProvider for Vapi (https://docs.vapi.ai).
//
// Calls use a saved assistant (VOICE_AGENT_ID) whose system prompt is the
// single template variable {{system_prompt}}; each call fills it through
// assistantOverrides.variableValues. The assistant, its three function tools
// and the phone number keep their server URLs and credential in Vapi, because
// Vapi only attaches the credential to server URLs from saved configuration
// (docs: server-url/server-authentication). Setup is in
// docs/runbooks/vapi-setup.md.
//
// Webhooks are authenticated with a shared secret that Vapi sends as
// "Authorization: Bearer <secret>" or "X-Vapi-Secret: <secret>" (a Bearer
// Token custom credential). There is no request signature, so the secret must
// be long and random, and TLS is required in front of the server.
package vapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// DefaultBaseURL is Vapi's API.
const DefaultBaseURL = "https://api.vapi.ai"

// MaxBody bounds webhook bodies. End-of-call reports carry the whole
// transcript plus the assistant configuration, still far below this.
const MaxBody = 1 << 20

// MinSecretLen is the shortest webhook secret accepted: there is no request
// signature, so the secret is the only proof a request came from Vapi.
const MinSecretLen = 32

// MaxDurationSeconds ends any call that runs past 10 minutes.
const MaxDurationSeconds = 600

// MaxToolCalls bounds tool calls in one request.
const MaxToolCalls = 10

// MetadataCallID is the assistantOverrides.metadata key carrying our call id.
const MetadataCallID = "haalchaal_call_id"

// Options configure the provider. Every field except BaseURL, HTTPClient and
// CostPaisePerUSD is required.
type Options struct {
	APIKey          string // private API key
	WebhookSecret   string // the Bearer Token credential's token
	AssistantID     string
	PhoneNumberID   string
	CostPaisePerUSD int64 // 0 leaves costs unrecorded
	BaseURL         string
	HTTPClient      *http.Client
}

// Provider talks to Vapi.
type Provider struct {
	apiKey        string
	secret        [32]byte // sha256 of the webhook secret, compared in constant time
	assistantID   string
	phoneNumberID string
	paisePerUSD   int64
	base          string
	http          *http.Client
}

// New validates opts and returns a provider.
func New(o Options) (*Provider, error) {
	var missing []string
	for _, kv := range [][2]string{{"VOICE_API_KEY", o.APIKey}, {"VOICE_WEBHOOK_SECRET", o.WebhookSecret}, {"VOICE_AGENT_ID", o.AssistantID}, {"VOICE_PHONE_NUMBER_ID", o.PhoneNumberID}} {
		if strings.TrimSpace(kv[1]) == "" {
			missing = append(missing, kv[0])
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("vapi: %s required", strings.Join(missing, ", "))
	}
	if len(o.WebhookSecret) < MinSecretLen {
		return nil, fmt.Errorf("vapi: VOICE_WEBHOOK_SECRET must be at least %d characters", MinSecretLen)
	}
	base := o.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return nil, errors.New("vapi: base URL must be https")
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	if o.CostPaisePerUSD < 0 {
		return nil, errors.New("vapi: cost rate must not be negative")
	}
	return &Provider{
		apiKey: o.APIKey, secret: sha256.Sum256([]byte(o.WebhookSecret)),
		assistantID: o.AssistantID, phoneNumberID: o.PhoneNumberID,
		paisePerUSD: o.CostPaisePerUSD, base: strings.TrimRight(base, "/"), http: hc,
	}, nil
}

func isLoopback(host string) bool { return host == "127.0.0.1" || host == "localhost" || host == "::1" }

func (p *Provider) Name() string { return "vapi" }

type createCall struct {
	AssistantID        string    `json:"assistantId"`
	PhoneNumberID      string    `json:"phoneNumberId"`
	Customer           customer  `json:"customer"`
	AssistantOverrides overrides `json:"assistantOverrides"`
}

type customer struct {
	Number string `json:"number"`
}

type overrides struct {
	VariableValues     map[string]string `json:"variableValues"`
	Metadata           map[string]string `json:"metadata"`
	ArtifactPlan       artifactPlan      `json:"artifactPlan"`
	MaxDurationSeconds int               `json:"maxDurationSeconds"`
}

// artifactPlan turns off audio we never use: no recording, no SIP packet
// capture (which can hold audio). Transcripts still arrive in the report.
type artifactPlan struct {
	RecordingEnabled bool `json:"recordingEnabled"`
	PcapEnabled      bool `json:"pcapEnabled"`
}

// StartCall places one outbound call. Errors carry the status code only:
// Vapi error bodies can echo the request, which holds the prompt.
func (p *Provider) StartCall(ctx context.Context, req voice.CallRequest) (voice.StartedCall, error) {
	vars := make(map[string]string, len(req.Variables)+3)
	for k, v := range req.Variables {
		vars[k] = v
	}
	vars["system_prompt"] = req.SystemPrompt
	vars["language"] = req.Language
	vars["call_id"] = req.CallID
	body, err := json.Marshal(createCall{
		AssistantID: p.assistantID, PhoneNumberID: p.phoneNumberID,
		Customer: customer{Number: req.To},
		AssistantOverrides: overrides{
			VariableValues: vars, Metadata: map[string]string{MetadataCallID: req.CallID},
			MaxDurationSeconds: MaxDurationSeconds,
		},
	})
	if err != nil {
		return voice.StartedCall{}, fmt.Errorf("vapi: encode call: %w", err)
	}
	resp, err := p.do(ctx, http.MethodPost, "/call", body)
	if err != nil {
		return voice.StartedCall{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return voice.StartedCall{}, fmt.Errorf("vapi: create call: status %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxBody)).Decode(&out); err != nil || out.ID == "" {
		return voice.StartedCall{}, errors.New("vapi: create call: response without a call id")
	}
	return voice.StartedCall{ProviderCallID: out.ID}, nil
}

// DeleteRecording deletes the call at Vapi, which erases its recordings and
// artifacts. A call Vapi no longer has counts as deleted.
func (p *Provider) DeleteRecording(ctx context.Context, providerCallID string) error {
	if providerCallID == "" {
		return nil
	}
	resp, err := p.do(ctx, http.MethodDelete, "/call/"+url.PathEscape(providerCallID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxBody))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return fmt.Errorf("vapi: delete call: status %d", resp.StatusCode)
}

func (p *Provider) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rd)
	if err != nil {
		return nil, fmt.Errorf("vapi: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("vapi: %s %s: %w", method, strings.SplitN(path, "/", 3)[1], ctx.Err())
		}
		return nil, fmt.Errorf("vapi: %s %s: request failed", method, strings.SplitN(path, "/", 3)[1])
	}
	return resp, nil
}

// verifiedBody checks the shared secret, then reads the body.
func (p *Provider) verifiedBody(r *http.Request) ([]byte, error) {
	got := r.Header.Get("X-Vapi-Secret")
	if got == "" {
		if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
			got = a[7:]
		}
	}
	if got == "" {
		return nil, voice.ErrBadSignature
	}
	sum := sha256.Sum256([]byte(got))
	if subtle.ConstantTimeCompare(sum[:], p.secret[:]) != 1 {
		return nil, voice.ErrBadSignature
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read body", voice.ErrBadPayload)
	}
	if len(body) > MaxBody {
		return nil, fmt.Errorf("%w: body too large", voice.ErrBadPayload)
	}
	return body, nil
}

type envelope struct {
	Message *message `json:"message"`
}

type message struct {
	Type            string          `json:"type"`
	Timestamp       json.RawMessage `json:"timestamp"`
	Status          string          `json:"status"`
	EndedReason     string          `json:"endedReason"`
	Call            *wireCall       `json:"call"`
	Artifact        *artifact       `json:"artifact"`
	ToolCallList    []wireToolCall  `json:"toolCallList"`
	StartedAt       *time.Time      `json:"startedAt"`
	EndedAt         *time.Time      `json:"endedAt"`
	Cost            *float64        `json:"cost"`
	DurationSeconds *float64        `json:"durationSeconds"`
}

type wireCall struct {
	ID                 string     `json:"id"`
	EndedReason        string     `json:"endedReason"`
	StartedAt          *time.Time `json:"startedAt"`
	EndedAt            *time.Time `json:"endedAt"`
	Cost               *float64   `json:"cost"`
	AssistantOverrides *struct {
		Metadata map[string]any `json:"metadata"`
	} `json:"assistantOverrides"`
}

type artifact struct {
	Messages []wireMessage `json:"messages"`
}

type wireMessage struct {
	Role             string  `json:"role"`
	Message          string  `json:"message"`
	SecondsFromStart float64 `json:"secondsFromStart"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// ParseDelivery verifies the secret and maps one server message. Message
// types we do not use (transcripts, speech updates, conversation updates)
// give an empty delivery, which handlers acknowledge.
func (p *Provider) ParseDelivery(r *http.Request) (voice.Delivery, error) {
	body, err := p.verifiedBody(r)
	if err != nil {
		return voice.Delivery{}, err
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || env.Message == nil {
		return voice.Delivery{}, fmt.Errorf("%w: not a server message", voice.ErrBadPayload)
	}
	m := env.Message
	switch m.Type {
	case "tool-calls", "status-update", "end-of-call-report":
	default:
		return voice.Delivery{}, nil
	}
	if m.Call == nil || m.Call.ID == "" {
		return voice.Delivery{}, fmt.Errorf("%w: message without a call id", voice.ErrBadPayload)
	}
	callID := ourCallID(m.Call)
	at := parseTimestamp(m.Timestamp)
	switch m.Type {
	case "tool-calls":
		calls, err := toolCalls(m, callID)
		return voice.Delivery{ToolCalls: calls}, err
	case "status-update":
		var t voice.EventType
		switch m.Status {
		case "ringing":
			t = voice.EventRinging
		case "in-progress":
			t = voice.EventAnswered
		default:
			// "ended" is handled by the end-of-call report, which carries the
			// reason and transcript; queued, scheduled and forwarding carry no
			// state we track.
			return voice.Delivery{}, nil
		}
		return voice.Delivery{Events: []voice.Event{{
			EventID: m.Call.ID + ":status:" + m.Status, ProviderCallID: m.Call.ID, CallID: callID, Type: t, At: at,
		}}}, nil
	default:
		return voice.Delivery{Events: p.endEvents(m, callID, at)}, nil
	}
}

func toolCalls(m *message, callID string) ([]voice.ToolCall, error) {
	if len(m.ToolCallList) == 0 || len(m.ToolCallList) > MaxToolCalls {
		return nil, fmt.Errorf("%w: tool call count", voice.ErrBadPayload)
	}
	out := make([]voice.ToolCall, 0, len(m.ToolCallList))
	for _, tc := range m.ToolCallList {
		if tc.ID == "" || tc.Function.Name == "" {
			return nil, fmt.Errorf("%w: tool call without id or name", voice.ErrBadPayload)
		}
		args, err := toolArgs(tc.Function.Arguments)
		if err != nil {
			return nil, err
		}
		out = append(out, voice.ToolCall{EventID: tc.ID, ProviderCallID: m.Call.ID, CallID: callID, Tool: tc.Function.Name, Args: args})
	}
	return out, nil
}

// toolArgs accepts arguments as an object or as a JSON string holding one
// (OpenAI style). Non-string values are kept in their JSON form.
func toolArgs(raw json.RawMessage) (map[string]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]string{}, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("%w: tool arguments", voice.ErrBadPayload)
		}
		raw = []byte(s)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("%w: tool arguments", voice.ErrBadPayload)
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
		} else {
			out[k] = string(v)
		}
	}
	return out, nil
}

// endEvents maps an end-of-call report to the attempt's final event.
func (p *Provider) endEvents(m *message, callID string, at time.Time) []voice.Event {
	c := m.Call
	reason := m.EndedReason
	if reason == "" {
		reason = c.EndedReason
	}
	started, ended := firstTime(m.StartedAt, c.StartedAt), firstTime(m.EndedAt, c.EndedAt)
	if !ended.IsZero() {
		at = ended
	}
	turns := transcript(m.Artifact)
	ev := voice.Event{EventID: c.ID + ":end", ProviderCallID: c.ID, CallID: callID, At: at}

	switch kind := endKind(reason, parentSpoke(turns)); kind {
	case voice.EventNoAnswer, voice.EventBusy, voice.EventFailed:
		ev.Type = kind
		if reason == "voicemail" {
			// Voicemail may follow an in-progress status, from which no_answer
			// is not a valid move; the failed event then ends the attempt.
			fb := ev
			fb.EventID, fb.Type = c.ID+":end:fallback", voice.EventFailed
			return []voice.Event{ev, fb}
		}
		return []voice.Event{ev}
	default:
		ev.Type = voice.EventCompleted
		ev.Transcript = turns
		ev.DurationSec = duration(m.DurationSeconds, started, ended)
		ev.CostPaise = p.paise(m.Cost, c.Cost)
		return []voice.Event{ev}
	}
}

// endKind classifies an ended reason (docs: calls/call-ended-reason). A call
// in which the parent said anything is completed whatever the reason, so the
// transcript is processed and the report decides if it was usable.
func endKind(reason string, parentSpoke bool) voice.EventType {
	switch {
	case reason == "customer-busy":
		return voice.EventBusy
	case reason == "customer-did-not-answer" || reason == "voicemail",
		strings.Contains(reason, "sip-408"), strings.Contains(reason, "sip-480"):
		return voice.EventNoAnswer
	case parentSpoke:
		return voice.EventCompleted
	}
	switch reason {
	case "customer-ended-call", "assistant-ended-call", "assistant-ended-call-after-message-spoken",
		"assistant-said-end-call-phrase", "silence-timed-out", "exceeded-max-duration":
		return voice.EventCompleted
	}
	return voice.EventFailed
}

func transcript(a *artifact) []voice.Turn {
	if a == nil {
		return nil
	}
	var out []voice.Turn
	for _, m := range a.Messages {
		var speaker string
		switch m.Role {
		case "user":
			speaker = "parent"
		case "bot", "assistant":
			speaker = "agent"
		default:
			continue // system prompt, tool calls and results are not speech
		}
		if strings.TrimSpace(m.Message) == "" {
			continue
		}
		off := time.Duration(0)
		if m.SecondsFromStart > 0 && m.SecondsFromStart < 86400 {
			off = time.Duration(m.SecondsFromStart * float64(time.Second))
		}
		out = append(out, voice.Turn{Speaker: speaker, Text: m.Message, Offset: off})
	}
	return out
}

func parentSpoke(turns []voice.Turn) bool {
	for _, t := range turns {
		if t.Speaker == "parent" {
			return true
		}
	}
	return false
}

func duration(secs *float64, started, ended time.Time) int {
	if secs != nil && *secs > 0 && *secs < 86400 {
		return int(math.Round(*secs))
	}
	if !started.IsZero() && ended.After(started) && ended.Sub(started) < 24*time.Hour {
		return int(ended.Sub(started).Round(time.Second) / time.Second)
	}
	return 0
}

// paise converts a USD cost with the configured rate; unknown or nonsense
// values give 0 (unrecorded).
func (p *Provider) paise(costs ...*float64) int64 {
	if p.paisePerUSD == 0 {
		return 0
	}
	for _, c := range costs {
		if c != nil && *c > 0 && *c < 1000 {
			return int64(math.Round(*c * float64(p.paisePerUSD)))
		}
	}
	return 0
}

func firstTime(ts ...*time.Time) time.Time {
	for _, t := range ts {
		if t != nil && !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Time{}
}

// parseTimestamp reads Vapi's message timestamp: epoch milliseconds or an
// ISO 8601 string. Zero means unknown; handlers then use their clock.
func parseTimestamp(raw json.RawMessage) time.Time {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return time.Time{}
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t.UTC()
			}
		}
		return time.Time{}
	}
	if ms, err := strconv.ParseFloat(string(raw), 64); err == nil && ms > 0 && ms < 1e14 {
		return time.UnixMilli(int64(ms)).UTC()
	}
	return time.Time{}
}

func ourCallID(c *wireCall) string {
	if c.AssistantOverrides == nil {
		return ""
	}
	if s, ok := c.AssistantOverrides.Metadata[MetadataCallID].(string); ok {
		return s
	}
	return ""
}

// ToolReply is Vapi's tool result format. Vapi expects HTTP 200 even for a
// failed call, with "error" instead of "result".
func (p *Provider) ToolReply(results []voice.ToolResult) any {
	type item struct {
		ToolCallID string `json:"toolCallId"`
		Result     string `json:"result,omitempty"`
		Error      string `json:"error,omitempty"`
	}
	out := struct {
		Results []item `json:"results"`
	}{Results: make([]item, 0, len(results))}
	for _, r := range results {
		it := item{ToolCallID: r.Call.EventID}
		if r.Err == nil {
			it.Result = "Reported. The family is being informed."
		} else {
			it.Error = "Not recorded."
		}
		out.Results = append(out.Results, it)
	}
	return out
}

// ParseWebhook satisfies voice.Provider; handlers use ParseDelivery so that
// tool calls sent to the webhook URL are not lost.
func (p *Provider) ParseWebhook(r *http.Request) ([]voice.Event, error) {
	d, err := p.ParseDelivery(r)
	if err == nil && len(d.ToolCalls) > 0 {
		return nil, fmt.Errorf("%w: tool calls need ParseDelivery", voice.ErrBadPayload)
	}
	return d.Events, err
}

// ParseToolCall satisfies voice.Provider for a request with one tool call.
func (p *Provider) ParseToolCall(r *http.Request) (voice.ToolCall, error) {
	d, err := p.ParseDelivery(r)
	if err != nil {
		return voice.ToolCall{}, err
	}
	if len(d.ToolCalls) != 1 {
		return voice.ToolCall{}, fmt.Errorf("%w: expected one tool call", voice.ErrBadPayload)
	}
	return d.ToolCalls[0], nil
}
