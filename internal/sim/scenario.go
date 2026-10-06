// Package sim replays scenario files from testdata/transcripts through the
// whole pipeline: scheduler, job handlers and the real HTTP webhook and tool
// handlers (with signatures), using the fake vendors and a fake clock. It
// backs `haalchaal simcall` and the golden tests.
package sim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Scenario is one scenario file.
type Scenario struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Parent      ParentSpec   `json:"parent"`
	Family      []MemberSpec `json:"family"`
	Medicines   []MedSpec    `json:"medicines"`
	FollowUps   []string     `json:"follow_ups"`
	History     []HistoryDay `json:"history,omitempty"` // earlier days, replayed first
	Attempts    []Attempt    `json:"attempts"`
	// Acks are "I'm on it" presses after the scenario day's calls end.
	Acks []AckSpec `json:"acks,omitempty"`
	// RunMinutes is how long to keep running queued jobs (escalation steps)
	// after the calls end; default 180.
	RunMinutes int      `json:"run_minutes,omitempty"`
	Expected   Expected `json:"expected"`
}

// AckSpec presses the acknowledgement button on an alert.
type AckSpec struct {
	AfterMin int    `json:"after_min"`
	Member   int    `json:"member"`          // 1-based priority of the family member pressing it
	Category string `json:"category"`        // alert to acknowledge
	Phone    string `json:"phone,omitempty"` // "stranger": press from a number outside the family
}

// ParentSpec describes the simulated parent.
type ParentSpec struct {
	PreferredName string   `json:"preferred_name"`
	Language      string   `json:"language"`
	Plan          string   `json:"plan"`
	Timezone      string   `json:"timezone"`
	CallTime      string   `json:"call_time"`
	WindowStart   string   `json:"window_start"`
	WindowEnd     string   `json:"window_end"`
	Interests     []string `json:"interests"`
	SafeWord      string   `json:"safe_word"`
	FirstCallDone bool     `json:"first_call_done"`
	LocalContact  string   `json:"local_contact"`
}

// MemberSpec describes a family member, in priority order.
type MemberSpec struct {
	Name     string `json:"name"`
	Relation string `json:"relation"`
	Language string `json:"language"`
	NoOptIn  bool   `json:"no_whatsapp_opt_in"`
}

// MedSpec is one medicine.
type MedSpec struct {
	Name   string `json:"name"`
	Timing string `json:"timing"`
}

// HistoryDay is an earlier day's call, used for multi-day rules.
type HistoryDay struct {
	Attempts []Attempt `json:"attempts"`
}

// Attempt scripts what happens on one call attempt.
type Attempt struct {
	Events     []EventSpec `json:"events"`
	ToolCalls  []ToolSpec  `json:"tool_calls,omitempty"`
	Transcript []TurnSpec  `json:"transcript,omitempty"`
	// TranscriptOnCompleted bundles the transcript into the completed event
	// instead of a separate transcript_ready event.
	TranscriptOnCompleted bool `json:"transcript_on_completed,omitempty"`
}

// EventSpec is one provider event, AfterSec seconds after dialing.
type EventSpec struct {
	Type        string `json:"type"`
	AfterSec    int    `json:"after_sec"`
	DurationSec int    `json:"duration_sec,omitempty"`
	CostPaise   int64  `json:"cost_paise,omitempty"`
}

// ToolSpec is a mid-call tool call.
type ToolSpec struct {
	Tool     string            `json:"tool"`
	AfterSec int               `json:"after_sec"`
	Args     map[string]string `json:"args"`
}

// TurnSpec is one transcript turn.
type TurnSpec struct {
	Speaker  string `json:"speaker"`
	Text     string `json:"text"`
	OffsetMS int64  `json:"offset_ms"`
}

// Expected is what a scenario must produce. Empty fields are not checked.
type Expected struct {
	Attempts     []string         `json:"attempts,omitempty"`    // final status per attempt, in order
	SlotStatus   string           `json:"slot_status,omitempty"` // final status of the scenario day's slot
	Alerts       []ExpectedAlert  `json:"alerts"`                // exact set (type/category), order-independent
	ParentStatus string           `json:"parent_status,omitempty"`
	Report       *json.RawMessage `json:"report,omitempty"` // subset of the stored report for the day's last processed call
	NeedsReview  *bool            `json:"needs_review,omitempty"`
	// TranscriptDeleted expects no transcript to remain for the day's calls.
	TranscriptDeleted bool              `json:"transcript_deleted,omitempty"`
	Messages          []ExpectedMessage `json:"messages,omitempty"` // checked by golden tests (M5)
	NoMessages        bool              `json:"no_messages,omitempty"`
	Acknowledged      []string          `json:"acknowledged,omitempty"`
	MustNotSend       []string          `json:"must_not_send,omitempty"` // substrings no outbound message may contain
}

// ExpectedAlert identifies an alert.
type ExpectedAlert struct {
	Type     string `json:"type"`
	Category string `json:"category"`
	Source   string `json:"source,omitempty"`
}

// ExpectedMessage is an outbound message.
type ExpectedMessage struct {
	To       string   `json:"to"` // "member:1", "member:2", "admin", "local_contact"
	Template string   `json:"template"`
	Contains []string `json:"contains,omitempty"`
}

// Load reads and strictly decodes a scenario file.
func Load(path string) (Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	var s Scenario
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Scenario{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if s.Name == "" {
		s.Name = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	return s, s.validate()
}

// LoadByName loads dir/<name>.json; name must be a bare file name.
func LoadByName(dir, name string) (Scenario, error) {
	if name == "" || strings.ContainsAny(name, `/\.`) {
		return Scenario{}, fmt.Errorf("invalid scenario name %q", name)
	}
	return Load(filepath.Join(dir, name+".json"))
}

func (s Scenario) validate() error {
	if len(s.Attempts) == 0 {
		return fmt.Errorf("%s: no attempts", s.Name)
	}
	if len(s.Family) == 0 {
		return fmt.Errorf("%s: no family members", s.Name)
	}
	return nil
}
