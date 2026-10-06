package extract

import (
	"strings"
	"testing"
)

func sample() Report {
	return Report{
		SchemaVersion: "1", CallQuality: "good", AnsweredBy: "parent", Mood: "good", Sleep: "poor", Appetite: "normal",
		Medicines:         []MedicineTaken{{"Amlodipine", "yes"}, {"Metformin", "no"}},
		Pain:              []Pain{{Location: "knee", Trend: "same"}},
		MessagesForFamily: []string{"come home for Diwali", "my secret loan worries are bad"},
		PrivateNotes:      []string{"secret loan worries"},
		FamilySummary:     "She is well. She has secret loan worries.",
		Confidence:        0.9,
	}
}

func TestSafeSummaryUsesFallbackOnLeak(t *testing.T) {
	r := sample()
	s, fb, err := SafeSummary(r, "Kamla ji", "en")
	if err != nil || !fb {
		t.Fatalf("fallback not used: %v %v", fb, err)
	}
	if LeaksPrivate(s, r.PrivateNotes) || strings.Contains(strings.ToLower(s), "loan") {
		t.Fatalf("fallback leaks: %q", s)
	}
	for _, want := range []string{"Kamla ji", "did not sleep well", "Medicines taken: Amlodipine", "Not taken: Metformin", "knee", "come home for Diwali"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q missing %q", s, want)
		}
	}
	if strings.Contains(s, "\n") {
		t.Error("summary has a newline")
	}
}

func TestSafeSummaryKeepsCleanModelSummary(t *testing.T) {
	r := sample()
	r.FamilySummary = "  She is well   and cheerful. "
	s, fb, _ := SafeSummary(r, "Kamla ji", "en")
	if fb || s != "She is well and cheerful." {
		t.Fatalf("got %q fallback=%v", s, fb)
	}
	r.FamilySummary = ""
	if _, fb, _ := SafeSummary(r, "Kamla ji", "en"); !fb {
		t.Fatal("empty summary should fall back")
	}
}

func TestFallbackHindi(t *testing.T) {
	s, err := FallbackSummary(sample(), "कमला जी", "hi")
	if err != nil || !strings.Contains(s, "कमला जी") || !strings.Contains(s, "Amlodipine") {
		t.Fatalf("%q %v", s, err)
	}
}
