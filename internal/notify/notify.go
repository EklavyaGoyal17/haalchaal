// Package notify defines the messaging interface (SPEC §6) and the WhatsApp
// templates (SPEC §10). Vendor adapters live in subpackages.
package notify

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

// QuickReply is a template button.
type QuickReply struct {
	Title   string // "I'm on it"
	Payload string // "ack:<alert_id>"
}

// TemplateMessage is one approved-template message.
type TemplateMessage struct {
	To       string // E.164
	Template string // approved template name
	Language string // template language code
	Params   []string
	Buttons  []QuickReply
}

// InboundEvent is a verified inbound message or delivery status.
type InboundEvent struct {
	EventID   string
	From      string // E.164
	Kind      string // "button", "text" or "status"
	Payload   string // button payload, text body, or delivery status
	MessageID string // for statuses: the outbound message id
}

// Inbound kinds.
const (
	KindButton = "button"
	KindText   = "text"
	KindStatus = "status"
)

// Messenger sends template messages and parses inbound webhooks.
type Messenger interface {
	Name() string
	SendTemplate(ctx context.Context, m TemplateMessage) (providerMessageID string, err error)
	ParseWebhook(r *http.Request) ([]InboundEvent, error) // verifies the signature first
	VerifyToken() string                                  // for the GET subscription check
}

// ErrBadSignature means an inbound webhook failed verification.
var ErrBadSignature = errors.New("notify: bad webhook signature")

// ErrBadPayload means a verified webhook could not be parsed.
var ErrBadPayload = errors.New("notify: bad webhook payload")

// Template names (SPEC §10), plus the admin alert.
const (
	TplDailySummary   = "daily_summary_v1"
	TplMissedCalls    = "missed_calls_v1"
	TplAlertEmergency = "alert_emergency_v1"
	TplAlertUrgent    = "alert_urgent_v1"
	TplAlertScam      = "alert_scam_v1"
	TplCallsPaused    = "calls_paused_v1"
	TplCallPending    = "call_pending_v1"
	TplAdminAlert     = "admin_alert_v1"
)

// Template describes one approved template.
type Template struct {
	Name      string
	Params    int
	AckButton bool
	English   string // body with {{n}} placeholders, for the fake and docs
}

// Templates is the catalog. Bodies must match what Meta approved.
var Templates = map[string]Template{
	TplDailySummary:   {TplDailySummary, 2, false, "{{1}}'s check-in today: {{2}}"},
	TplMissedCalls:    {TplMissedCalls, 2, false, "{{1}} did not answer today's check-in calls ({{2}}). Please give them a call."},
	TplAlertEmergency: {TplAlertEmergency, 2, true, `URGENT: in today's call, {{1}} said "{{2}}". We asked them to call 112. Please contact them now.`},
	TplAlertUrgent:    {TplAlertUrgent, 2, true, "Please check on {{1}} today: {{2}}"},
	TplAlertScam:      {TplAlertScam, 2, true, "{{1}} mentioned a possible scam call ({{2}}). Please call them and remind them never to share an OTP or send money."},
	TplCallsPaused:    {TplCallsPaused, 1, false, "{{1}} asked us to pause the daily calls, so we have paused them. Reply here if you would like us to resume."},
	TplCallPending:    {TplCallPending, 1, false, "Today's call with {{1}} is done. We'll share the details shortly."},
	TplAdminAlert:     {TplAdminAlert, 3, false, "HaalChaal admin: {{1}}. Parent ref {{2}}. Review: {{3}}"},
}

// AckTitle is the quick-reply button text.
const AckTitle = "I'm on it"

// AckPayload is the button payload acknowledging an alert.
func AckPayload(alertID string) string { return "ack:" + alertID }

// ParseAck returns the alert id from an "ack:<id>" payload.
func ParseAck(payload string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(payload), "ack:")
	return id, ok && id != ""
}

// MaxParam is WhatsApp's limit for one template parameter.
const MaxParam = 1024

// CleanParam makes text safe as a template parameter: no newlines or tabs,
// no runs of spaces, bounded length.
func CleanParam(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > MaxParam {
		out = string(r[:MaxParam-1]) + "…"
	}
	return out
}

// Render fills a template body, for the fake messenger and tests.
func Render(t Template, params []string) string {
	s := t.English
	for i, p := range params {
		s = strings.ReplaceAll(s, "{{"+strconv.Itoa(i+1)+"}}", p)
	}
	return s
}

// Validate checks a message against the catalog.
func Validate(m TemplateMessage) error {
	t, ok := Templates[m.Template]
	if !ok {
		return errors.New("notify: unknown template " + m.Template)
	}
	if len(m.Params) != t.Params {
		return errors.New("notify: wrong parameter count for " + m.Template)
	}
	for _, p := range m.Params {
		if strings.TrimSpace(p) == "" {
			return errors.New("notify: empty parameter for " + m.Template)
		}
	}
	return nil
}
