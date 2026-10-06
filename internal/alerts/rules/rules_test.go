package rules

import (
	"testing"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/extract"
)

func base() extract.Report {
	return extract.Report{SchemaVersion: "1", CallQuality: "good", AnsweredBy: "parent", Mood: "good", Sleep: "good", Appetite: "normal", Confidence: 0.9}
}

type want struct{ typ, cat, src string }

func check(t *testing.T, got []Finding, ws ...want) {
	t.Helper()
	if len(got) != len(ws) {
		t.Fatalf("got %+v, want %+v", got, ws)
	}
	for i, w := range ws {
		if got[i].Type != w.typ || got[i].Category != w.cat || got[i].Source != w.src {
			t.Errorf("finding %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestEvaluate(t *testing.T) {
	t.Run("clean call", func(t *testing.T) { check(t, Evaluate(base(), nil, nil)) })

	t.Run("red flags by severity", func(t *testing.T) {
		r := base()
		r.RedFlags = []extract.RedFlag{{Category: "fall", Severity: "emergency", Quote: "gir gayi"}, {Category: "confusion", Severity: "urgent", Quote: "x"}, {Category: "self_harm", Severity: "urgent", Quote: "y"}}
		check(t, Evaluate(r, nil, nil),
			want{"emergency", "fall", "model"}, want{"urgent", "confusion", "model"}, want{"emergency", "self_harm", "model"})
	})

	t.Run("keyword-only red flag still alerts", func(t *testing.T) {
		check(t, Evaluate(base(), []alerts.Hit{{Category: "chest_pain", Quote: "seene mein dard"}}, nil),
			want{"emergency", "chest_pain", "keyword"})
	})

	t.Run("keyword merges into model flag and upgrades", func(t *testing.T) {
		r := base()
		r.RedFlags = []extract.RedFlag{{Category: "fall", Severity: "urgent", Quote: "fisal gayi"}}
		got := Evaluate(r, []alerts.Hit{{Category: "fall", Quote: "gir gayi"}}, nil)
		check(t, got, want{"emergency", "fall", "model"})
		if got[0].Detail != "fisal gayi" {
			t.Errorf("detail %q", got[0].Detail)
		}
	})

	t.Run("scam signals collapse to the most serious", func(t *testing.T) {
		r := base()
		r.ScamSignals = []extract.ScamSignal{{Pattern: "video_call_pressure", Quote: "v"}, {Pattern: "agency_threat", Quote: "a"}, {Pattern: "money_request", Quote: "m"}}
		got := Evaluate(r, nil, nil)
		check(t, got, want{"scam", "agency_threat", "model"})
		if got[0].Detail != "a" {
			t.Errorf("detail %q", got[0].Detail)
		}
		r.ScamSignals = []extract.ScamSignal{{Pattern: "other", Quote: "o"}}
		check(t, Evaluate(r, nil, nil), want{"scam", "scam_other", "model"})
	})

	t.Run("model scam", func(t *testing.T) {
		r := base()
		r.ScamSignals = []extract.ScamSignal{{Pattern: "agency_threat", Quote: "CBI"}}
		check(t, Evaluate(r, []alerts.Hit{{Category: "scam"}}, nil), want{"scam", "agency_threat", "model"})
	})

	t.Run("scam keyword not confirmed goes to admins only", func(t *testing.T) {
		got := Evaluate(base(), []alerts.Hit{{Category: "scam", Quote: "beta police mein hai"}}, nil)
		check(t, got, want{"scam", "scam_keyword", "keyword"})
		if !got[0].AdminOnly {
			t.Error("not admin-only")
		}
	})

	t.Run("stop and distress", func(t *testing.T) {
		r := base()
		r.CallPreferences.StopRequested = true
		r.Mood = "distressed"
		check(t, Evaluate(r, nil, nil), want{"urgent", "stop_requested", "model"}, want{"urgent", "distressed", "rule"})
	})

	t.Run("same medicine missed twice", func(t *testing.T) {
		r, prev := base(), base()
		r.Medicines = []extract.MedicineTaken{{Name: "Metformin", Taken: "no"}, {Name: "Amlodipine", Taken: "no"}}
		prev.Medicines = []extract.MedicineTaken{{Name: "metformin ", Taken: "no"}, {Name: "Amlodipine", Taken: "yes"}}
		got := Evaluate(r, nil, []extract.Report{prev})
		check(t, got, want{"urgent", "missed_medicine", "rule"})
		if got[0].Detail != "Metformin" {
			t.Errorf("detail %q", got[0].Detail)
		}
		check(t, Evaluate(r, nil, nil)) // first day: no alert
	})

	t.Run("low mood three in a row", func(t *testing.T) {
		low := base()
		low.Mood = "low"
		check(t, Evaluate(low, nil, []extract.Report{low, low}), want{"watch", "low_mood", "rule"})
		check(t, Evaluate(low, nil, []extract.Report{low, base()}))
		check(t, Evaluate(low, nil, []extract.Report{low}))
	})

	t.Run("poor sleep three in a row", func(t *testing.T) {
		p := base()
		p.Sleep = "poor"
		check(t, Evaluate(p, nil, []extract.Report{p, p}), want{"watch", "poor_sleep", "rule"})
	})
}
