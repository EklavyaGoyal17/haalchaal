package alerts

import (
	"slices"
	"testing"
)

func TestEmbeddedKeywordsParse(t *testing.T) {
	d, err := ParseKeywords(keywordsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"fall", "chest_pain", "breathing", "fainting", "stroke_signs", "bleeding", "confusion", "self_harm", "scam"}
	if !slices.Equal(d.Categories(), want) {
		t.Fatalf("categories = %v", d.Categories())
	}
	// Every red-flag category except "other" has phrases.
	for _, c := range RedFlagCategories {
		if c != "other" && !slices.Contains(d.Categories(), c) {
			t.Errorf("no keywords for %s", c)
		}
	}
}

func TestDetect(t *testing.T) {
	d := DefaultDetector
	tests := []struct {
		text string
		want []string
	}{
		{"Kal bathroom mein gir gayi thi", []string{"fall"}},
		{"मैं कल गिर गई थी", []string{"fall"}},
		{"I FELL   in the garden", []string{"fall"}},
		{"My fellow walkers are fine", nil},
		{"Seene  mein dard ho raha hai", []string{"chest_pain"}},
		{"நெஞ்சு வலி இருக்கு", []string{"chest_pain"}},
		{"I can’t breathe properly", []string{"breathing"}},
		{"main jeena nahi chahti", []string{"self_harm"}},
		{"CBI officer ne kaha video call pe raho", []string{"scam"}},
		{"Mera beta police mein hai", []string{"scam"}},
		{"We ate hotpot", nil},
		{"OTP maanga tha", []string{"scam"}},
		{"Sab theek hai, khana kha liya", nil},
		{"gir gayi aur seene mein dard", []string{"fall", "chest_pain"}},
	}
	for _, tt := range tests {
		var got []string
		for _, h := range d.Detect([]string{tt.text}) {
			got = append(got, h.Category)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("Detect(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestDetectOneHitPerCategory(t *testing.T) {
	hits := DefaultDetector.Detect([]string{"gir gaya", "fisal gaya", "fell"})
	if len(hits) != 1 || hits[0].Quote != "gir gaya" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestParseKeywordsErrors(t *testing.T) {
	for name, src := range map[string]string{
		"phrase first":   "- x\n",
		"empty category": "fall:\nchest:\n- y\n",
		"duplicate":      "a:\n- x\na:\n- y\n",
		"garbage":        "a:\n- x\nwhat is this\n",
		"empty phrase":   "a:\n- \"\"\n",
	} {
		if _, err := ParseKeywords([]byte(src)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestRedFlagSeverity(t *testing.T) {
	if RedFlagSeverity("confusion") != TypeUrgent || RedFlagSeverity("fall") != TypeEmergency || RedFlagSeverity("self_harm") != TypeEmergency {
		t.Fatal("severity")
	}
}
