// Package extract turns a call transcript into a validated report (SPEC §8).
// Validation lives here in Go, never only in the prompt.
package extract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// SchemaVersion is the report schema version this code writes and accepts.
const SchemaVersion = "1"

// Limits (SPEC §8).
const (
	MaxFollowUps = 3
	MaxQuote     = 200
	MaxSummary   = 900
	MaxListItems = 10
	MaxItemLen   = 300
	ReviewBelow  = 0.7 // confidence under this needs review
)

// Report is schema version 1.
type Report struct {
	SchemaVersion     string          `json:"schema_version"`
	CallQuality       string          `json:"call_quality"`
	AnsweredBy        string          `json:"answered_by"`
	Mood              string          `json:"mood"`
	Sleep             string          `json:"sleep"`
	Appetite          string          `json:"appetite"`
	Medicines         []MedicineTaken `json:"medicines"`
	Pain              []Pain          `json:"pain"`
	RedFlags          []RedFlag       `json:"red_flags"`
	ScamSignals       []ScamSignal    `json:"scam_signals"`
	CallPreferences   CallPreferences `json:"call_preferences"`
	MessagesForFamily []string        `json:"messages_for_family"`
	FollowUps         []string        `json:"follow_ups"`
	PrivateNotes      []string        `json:"private_notes"`
	FamilySummary     string          `json:"family_summary"`
	Confidence        float64         `json:"confidence"`
}

// MedicineTaken is whether one due medicine was taken.
type MedicineTaken struct {
	Name  string `json:"name"`
	Taken string `json:"taken"`
}

// Pain is one pain the parent mentioned.
type Pain struct {
	Location string `json:"location"`
	Trend    string `json:"trend"`
	Quote    string `json:"quote"`
}

// RedFlag is a possible emergency.
type RedFlag struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	Quote    string `json:"quote"`
}

// ScamSignal is a possible scam.
type ScamSignal struct {
	Pattern string `json:"pattern"`
	Quote   string `json:"quote"`
}

// CallPreferences are requests about the calls themselves.
type CallPreferences struct {
	StopRequested    bool    `json:"stop_requested"`
	NewTimeRequested *string `json:"new_time_requested"`
}

// Enumerations.
var (
	CallQualities = []string{"good", "partial", "unusable"}
	AnsweredBys   = []string{"parent", "someone_else", "unknown"}
	Moods         = []string{"good", "ok", "low", "distressed", "unknown"}
	Sleeps        = []string{"good", "poor", "unknown"}
	Appetites     = []string{"normal", "poor", "unknown"}
	TakenValues   = []string{"yes", "no", "unsure", "not_asked"}
	PainTrends    = []string{"new", "better", "same", "worse", "unknown"}
	RedFlagCats   = []string{"fall", "chest_pain", "breathing", "fainting", "stroke_signs", "bleeding", "confusion", "self_harm", "other"}
	Severities    = []string{"emergency", "urgent"}
	ScamPatterns  = []string{"agency_threat", "otp_or_bank_request", "money_request", "video_call_pressure", "other"}
)

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidationError lists everything wrong with a report, phrased so it can be
// appended to a retry prompt.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid report: " + strings.Join(e.Problems, "; ")
}

// Parse strictly decodes model output into a Report and validates it.
// Unknown fields, trailing data and unknown enum values are rejected; lists
// and strings over their limits are capped. Surrounding whitespace or a
// ```json fence is tolerated.
func Parse(raw string) (Report, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.DisallowUnknownFields()
	var r Report
	if err := dec.Decode(&r); err != nil {
		return Report{}, &ValidationError{Problems: []string{"not a single valid JSON object matching the schema: " + err.Error()}}
	}
	if dec.More() {
		return Report{}, &ValidationError{Problems: []string{"extra data after the JSON object"}}
	}
	if err := r.Validate(); err != nil {
		return Report{}, err
	}
	return r, nil
}

// Validate checks enums and ranges and caps lists and lengths in place.
func (r *Report) Validate() error {
	var p []string
	enum := func(field, v string, allowed []string) {
		if !slices.Contains(allowed, v) {
			// Echo at most a short prefix: a model may put call content in
			// an enum field, and this text reaches job errors.
			p = append(p, fmt.Sprintf("%s %q is not one of %s", field, capStr(v, 20), strings.Join(allowed, ", ")))
		}
	}
	if r.SchemaVersion != SchemaVersion {
		p = append(p, fmt.Sprintf("schema_version must be %q", SchemaVersion))
	}
	enum("call_quality", r.CallQuality, CallQualities)
	enum("answered_by", r.AnsweredBy, AnsweredBys)
	enum("mood", r.Mood, Moods)
	enum("sleep", r.Sleep, Sleeps)
	enum("appetite", r.Appetite, Appetites)
	if r.Confidence < 0 || r.Confidence > 1 || r.Confidence != r.Confidence {
		p = append(p, "confidence must be between 0 and 1")
	}
	if t := r.CallPreferences.NewTimeRequested; t != nil && !hhmm.MatchString(*t) {
		p = append(p, "call_preferences.new_time_requested must be HH:MM or null")
	}

	r.Medicines = capList(r.Medicines, MaxListItems)
	for i := range r.Medicines {
		m := &r.Medicines[i]
		enum("medicines.taken", m.Taken, TakenValues)
		m.Name = capStr(m.Name, MaxItemLen)
	}
	r.Pain = capList(r.Pain, MaxListItems)
	for i := range r.Pain {
		x := &r.Pain[i]
		enum("pain.trend", x.Trend, PainTrends)
		x.Location, x.Quote = capStr(x.Location, MaxItemLen), capStr(x.Quote, MaxQuote)
	}
	r.RedFlags = capList(r.RedFlags, MaxListItems)
	for i := range r.RedFlags {
		x := &r.RedFlags[i]
		enum("red_flags.category", x.Category, RedFlagCats)
		enum("red_flags.severity", x.Severity, Severities)
		x.Quote = capStr(x.Quote, MaxQuote)
	}
	r.ScamSignals = capList(r.ScamSignals, MaxListItems)
	for i := range r.ScamSignals {
		x := &r.ScamSignals[i]
		enum("scam_signals.pattern", x.Pattern, ScamPatterns)
		x.Quote = capStr(x.Quote, MaxQuote)
	}
	r.MessagesForFamily = capStrings(r.MessagesForFamily, MaxListItems, MaxItemLen)
	r.FollowUps = capStrings(r.FollowUps, MaxFollowUps, MaxItemLen)
	r.PrivateNotes = capStrings(r.PrivateNotes, MaxListItems, MaxItemLen)
	r.FamilySummary = capStr(strings.TrimSpace(r.FamilySummary), MaxSummary)
	if !utf8.ValidString(r.FamilySummary) {
		p = append(p, "family_summary is not valid UTF-8")
	}
	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	r.normalize()
	return nil
}

// normalize turns nil lists into empty ones so stored JSON always has arrays.
func (r *Report) normalize() {
	if r.Medicines == nil {
		r.Medicines = []MedicineTaken{}
	}
	if r.Pain == nil {
		r.Pain = []Pain{}
	}
	if r.RedFlags == nil {
		r.RedFlags = []RedFlag{}
	}
	if r.ScamSignals == nil {
		r.ScamSignals = []ScamSignal{}
	}
	if r.MessagesForFamily == nil {
		r.MessagesForFamily = []string{}
	}
	if r.FollowUps == nil {
		r.FollowUps = []string{}
	}
	if r.PrivateNotes == nil {
		r.PrivateNotes = []string{}
	}
}

func capList[T any](xs []T, n int) []T {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func capStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func capStrings(xs []string, n, l int) []string {
	var out []string
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, capStr(x, l))
		}
	}
	return capList(out, n)
}

// NeedsReview reports whether a human must look at this report (SPEC §8).
func (r Report) NeedsReview() bool {
	return r.Confidence < ReviewBelow || len(r.RedFlags) > 0 || len(r.ScamSignals) > 0 ||
		r.AnsweredBy != "parent" || r.CallQuality != "good" || r.CallPreferences.StopRequested
}

// Unusable reports whether the call counts as unanswered for retries.
func (r Report) Unusable() bool { return r.CallQuality == "unusable" }

// ErrNoTranscript means there is nothing to extract from.
var ErrNoTranscript = errors.New("empty transcript")
