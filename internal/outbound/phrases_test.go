package outbound

import (
	"strings"
	"testing"
)

func TestPhraseTablesComplete(t *testing.T) {
	for lang, table := range phrases {
		for key := range phrases["en"] {
			if strings.TrimSpace(table[key]) == "" {
				t.Errorf("%s: missing %q", lang, key)
			}
		}
		for key := range table {
			if _, ok := phrases["en"][key]; !ok {
				t.Errorf("%s: extra key %q", lang, key)
			}
		}
	}
}

func TestPhraseSelection(t *testing.T) {
	tests := []struct{ got, want string }{
		{categoryPhrase("fall", "hi"), "वे गिर गए थे"},
		{categoryPhrase("fall", "en"), "they had a fall"},
		{categoryPhrase("fall", "ta"), "they had a fall"}, // no Tamil templates yet: English
		{categoryPhrase("scam:agency_threat", "en"), "something worrying"},
		{categoryPhrase("unknown", "hi"), "कुछ चिंता की बात"},
		{scamPhrase("money_request", "hi"), "कोई पैसे माँग रहा था"},
		{scamPhrase("weird", "en"), "a suspicious caller"},
		{urgentReason("missed_medicine", "Metformin", true, "hi"), "उन्होंने लगातार दो कॉल में Metformin नहीं ली"},
		{urgentReason("missed_medicine", "Metformin", false, "en"), "they missed a medicine on two calls in a row"},
		{urgentReason("confusion", "main kahan hoon.", true, "en"), `in today's call they said "main kahan hoon"`},
		{urgentReason("confusion", "x", false, "hi"), "वे उलझन में लग रहे थे"},
		{phrasef("hi", "attempts_n", 3), "3 कोशिशें"},
		{alertNote("en"), "We have also sent you a separate alert about today's call."},
	}
	for i, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%d: got %q, want %q", i, tt.got, tt.want)
		}
	}
	// A quote containing format verbs is inserted literally.
	if got := phrasef("en", "said_fmt", "100%s %d"); got != `in today's call they said "100%s %d"` {
		t.Errorf("format injection: %q", got)
	}
}
