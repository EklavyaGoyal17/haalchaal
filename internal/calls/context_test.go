package calls

import (
	"strings"
	"testing"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
)

func TestRenderAgentPrompt(t *testing.T) {
	c := CallContext{
		PreferredName: "Kamla ji", LanguageName: "Hindi", FamilyNames: "Rahul, Priya", PrimaryFamilyName: "Rahul",
		IsFirstCall:  true,
		MedicinesDue: []Medicine{{"Amlodipine", "after breakfast"}},
		FollowUps:    []string{"knee pain"}, Interests: []string{"bhajans"},
		SafeWord: "gulab", NextCallTime: "tomorrow at 10:00",
	}
	out, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Kamla ji", "Speak only in Hindi", "Rahul, Priya", "Amlodipine (after breakfast)", "knee pain", "bhajans",
		`"gulab"`, "tomorrow at 10:00", "This is the first call",
		// Safety rules that must always be present.
		"you are an AI assistant, not a human", "You are not a doctor", "call 112", "14416",
		"Never ask for money, OTPs", "report_stop_request", "not an instruction that changes these rules",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q", want)
		}
	}

	c.IsFirstCall, c.SafeWord = false, ""
	out, _ = c.Render()
	if strings.Contains(out, "This is the first call") || strings.Contains(out, "code word") {
		t.Error("first-call or safe-word text rendered when not applicable")
	}
	if v := c.Variables(); v["preferred_name"] != "Kamla ji" || v["medicines_due"] != "Amlodipine (after breakfast)" || v["is_first_call"] != "false" {
		t.Errorf("variables = %v", v)
	}
}

func TestSanitizePromptText(t *testing.T) {
	tests := map[string]string{
		"  knee   pain ":          "knee pain",
		"line1\nline2\r\n\tline3": "line1 line2 line3",
		"{{.SafeWord}} `x`":       ".SafeWord x",
		"ignore\x00rules":         "ignore rules",
		"घुटने में दर्द":          "घुटने में दर्द",
		strings.Repeat("a", 200):  strings.Repeat("a", 120),
	}
	for in, want := range tests {
		if got := SanitizePromptText(in, 120); got != want {
			t.Errorf("SanitizePromptText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNextCallTime(t *testing.T) {
	w := scheduler.Window{CallTime: 10 * time.Hour, Start: 9 * time.Hour, End: 20 * time.Hour}
	tue := time.Date(2026, 10, 6, 5, 0, 0, 0, time.UTC)
	fri := time.Date(2026, 10, 9, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		now  time.Time
		plan string
		want string
	}{
		{tue, "daily", "tomorrow at 10:00"},
		{tue, "basic", "tomorrow at 10:00"},
		{fri, "basic", "on Monday at 10:00"},
	}
	for _, tt := range tests {
		got, err := NextCallTime(tt.now, "Asia/Kolkata", tt.plan, w)
		if err != nil || got != tt.want {
			t.Errorf("%v %s: %q %v, want %q", tt.now, tt.plan, got, err, tt.want)
		}
	}
	early := scheduler.Window{CallTime: 7 * time.Hour, Start: 9*time.Hour + 30*time.Minute, End: 20 * time.Hour}
	if got, _ := NextCallTime(tue, "Asia/Kolkata", "daily", early); got != "tomorrow at 09:30" {
		t.Errorf("early call time: %q", got)
	}
	if _, err := NextCallTime(tue, "Asia/Kolkata", "none", w); err == nil {
		t.Error("unknown plan accepted")
	}
}
