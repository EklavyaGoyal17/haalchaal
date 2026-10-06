package extract

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const validJSON = `{
  "schema_version": "1", "call_quality": "good", "answered_by": "parent",
  "mood": "good", "sleep": "good", "appetite": "normal",
  "medicines": [{"name": "Amlodipine", "taken": "yes"}],
  "pain": [], "red_flags": [], "scam_signals": [],
  "call_preferences": {"stop_requested": false, "new_time_requested": null},
  "messages_for_family": [], "follow_ups": [], "private_notes": [],
  "family_summary": "All well.", "confidence": 0.9
}`

func mutate(t *testing.T, f func(m map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validJSON), &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	b, _ := json.Marshal(m)
	return string(b)
}

func TestParseValid(t *testing.T) {
	r, err := Parse(validJSON)
	if err != nil {
		t.Fatal(err)
	}
	if r.NeedsReview() {
		t.Error("clean report needs review")
	}
	if _, err := Parse("```json\n" + validJSON + "\n```"); err != nil {
		t.Errorf("fenced: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	tests := map[string]string{
		"unknown field": mutate(t, func(m map[string]any) { m["diagnosis"] = "flu" }),
		"bad enum":      mutate(t, func(m map[string]any) { m["mood"] = "ecstatic" }),
		"bad severity": mutate(t, func(m map[string]any) {
			m["red_flags"] = []any{map[string]any{"category": "fall", "severity": "mild", "quote": "x"}}
		}),
		"bad category": mutate(t, func(m map[string]any) {
			m["red_flags"] = []any{map[string]any{"category": "flu", "severity": "urgent", "quote": "x"}}
		}),
		"bad taken":      mutate(t, func(m map[string]any) { m["medicines"] = []any{map[string]any{"name": "x", "taken": "maybe"}} }),
		"confidence > 1": mutate(t, func(m map[string]any) { m["confidence"] = 1.5 }),
		"confidence < 0": mutate(t, func(m map[string]any) { m["confidence"] = -0.1 }),
		"bad time": mutate(t, func(m map[string]any) {
			m["call_preferences"] = map[string]any{"stop_requested": false, "new_time_requested": "25:00"}
		}),
		"wrong version": mutate(t, func(m map[string]any) { m["schema_version"] = "2" }),
		"missing enum":  mutate(t, func(m map[string]any) { delete(m, "answered_by") }),
		"nested unknown": mutate(t, func(m map[string]any) {
			m["medicines"] = []any{map[string]any{"name": "x", "taken": "yes", "dose": "5mg"}}
		}),
		"two objects": validJSON + validJSON,
		"prose":       "Here is the report: " + validJSON,
		"empty":       "",
		"array":       "[]",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(raw)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
		})
	}
}

func TestParseCaps(t *testing.T) {
	long := strings.Repeat("क", 500)
	raw := mutate(t, func(m map[string]any) {
		m["follow_ups"] = []any{"a", "b", "c", "d", "e"}
		m["red_flags"] = []any{map[string]any{"category": "fall", "severity": "emergency", "quote": long}}
		m["family_summary"] = strings.Repeat("x", 2000)
	})
	r, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.FollowUps) != 3 || len([]rune(r.RedFlags[0].Quote)) != MaxQuote || len(r.FamilySummary) != MaxSummary {
		t.Fatalf("caps not applied: %d %d %d", len(r.FollowUps), len([]rune(r.RedFlags[0].Quote)), len(r.FamilySummary))
	}
}

func TestNeedsReview(t *testing.T) {
	base, _ := Parse(validJSON)
	tests := map[string]func(*Report){
		"low confidence": func(r *Report) { r.Confidence = 0.69 },
		"red flag":       func(r *Report) { r.RedFlags = []RedFlag{{"fall", "emergency", "x"}} },
		"scam":           func(r *Report) { r.ScamSignals = []ScamSignal{{"money_request", "x"}} },
		"someone else":   func(r *Report) { r.AnsweredBy = "someone_else" },
		"unknown answer": func(r *Report) { r.AnsweredBy = "unknown" },
		"partial":        func(r *Report) { r.CallQuality = "partial" },
		"stop":           func(r *Report) { r.CallPreferences.StopRequested = true },
	}
	for name, f := range tests {
		r := base
		f(&r)
		if !r.NeedsReview() {
			t.Errorf("%s: needs_review false", name)
		}
	}
	if !(Report{CallQuality: "unusable"}).Unusable() {
		t.Error("unusable")
	}
}

func TestNilListsNormalised(t *testing.T) {
	raw := mutate(t, func(m map[string]any) { delete(m, "pain"); delete(m, "private_notes") })
	r, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "null,") {
		t.Fatalf("null list in %s", b)
	}
}
