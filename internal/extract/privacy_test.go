package extract

import "testing"

func TestLeaksPrivate(t *testing.T) {
	notes := []string{"Mujhe pichhle hafte chakkar aaya tha", "money worries"}
	tests := []struct {
		text string
		want bool
	}{
		{"Kamla ji sounded cheerful today.", false},
		{"She said mujhe pichhle hafte chakkar aaya tha.", true},
		{"MUJHE   PICHHLE\nhafte CHAKKAR aaya tha!", true},
		{"She mentioned pichhle hafte chakkar briefly", true}, // 3-word fragment
		{"She has money worries", true},                       // short note, whole match
		{"She has money", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := LeaksPrivate(tt.text, notes); got != tt.want {
			t.Errorf("LeaksPrivate(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
	if LeaksPrivate("anything", nil) || LeaksPrivate("anything", []string{"  "}) {
		t.Error("empty notes leak")
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize("  Hello,   WORLD!!  सीने में  दर्द "); got != "hello world सीने में दर्द" {
		t.Errorf("got %q", got)
	}
}
