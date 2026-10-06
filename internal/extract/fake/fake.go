// Package fake is a deterministic, rule-based Extractor for dev, tests and
// the golden transcripts. It understands a small vocabulary of Hindi
// (romanised and Devanagari), English and Tamil phrases. It is not a model:
// real extraction quality is judged in Milestone 8.
package fake

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/extract"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// Extractor is the fake.
type Extractor struct{}

// Name is stored in call_reports.model.
func (Extractor) Name() string { return "fake-rules-v1" }

var (
	someoneElse = []string{"main unki", "main inki", "main uski", "this is her", "this is his", "this is their",
		"ghar pe nahi", "ghar par nahi", "not at home", "unki bahu", "main padosi", "main naukar", "she is not here", "he is not here",
		"நான் அவங்க"}
	moodDistressed = []string{"bahut pareshan", "bahut dar lag", "dar lag raha", "ro rahi", "ro raha", "scared", "very upset", "crying", "बहुत परेशान"}
	moodLow        = []string{"udaas", "akela", "akeli", "mann nahi lagta", "lonely", "sad", "feeling low", "उदास", "अकेल", "தனியா"}
	moodGood       = []string{"theek hoon", "theek hai", "accha hoon", "achha hoon", "badhiya", "mast", "i'm fine", "i am fine", "i'm good", "doing well",
		"ठीक हूँ", "ठीक हूं", "अच्छा हूँ", "நல்லா இருக்கேன்", "நல்லா இருக்கு"}
	sleepPoor = []string{"neend nahi", "neend nahin", "so nahi", "so nahin paya", "so nahi payi", "couldn't sleep", "could not sleep", "didn't sleep",
		"slept badly", "नींद नहीं", "தூக்கம் வரல"}
	sleepGood = []string{"achhi neend", "acchi neend", "neend aayi", "neend aa gayi", "achhe se soyi", "achhe se soya", "slept well", "good sleep",
		"अच्छी नींद", "நல்லா தூங்கினேன்"}
	appetitePoor = []string{"bhookh nahi", "bhook nahi", "khana nahi khaya", "kuch nahi khaya", "not hungry", "didn't eat", "भूख नहीं", "சாப்பிடல"}
	appetiteOK   = []string{"khana kha liya", "khana khaya", "nashta kiya", "nashta kar liya", "had breakfast", "ate well", "had lunch",
		"खाना खा लिया", "சாப்பிட்டேன்"}
	medNo     = []string{"nahi li", "nahin li", "nahi khayi", "bhool gayi", "bhool gaya", "bhul gayi", "bhul gaya", "forgot", "didn't take", "did not take", "not taken", "नहीं ली", "भूल", "மறந்துட்டேன்"}
	medYes    = []string{"le li", "li hai", "li thi", "kha li", "khayi", "kha liya", "haan li", "took it", "taken", "i took", "ले ली", "खा ली", "சாப்பிட்டேன்", "போட்டுட்டேன்"}
	medUnsure = []string{"yaad nahi", "pata nahi", "not sure", "don't remember", "याद नहीं"}
	stopWords = []string{"band kar do", "band karo", "mat karo call", "call mat karo", "call nahi chahiye", "calls nahi chahiye", "yeh calls nahi chahiye",
		"stop calling", "stop these calls", "don't call", "do not call", "बंद कर दो", "கூப்பிடாதீங்க"}
	privateMarkers = []string{"family ko mat batana", "ghar walon ko mat batana", "bachchon ko mat batana", "kisi ko mat batana", "beta ko mat batana",
		"rahul ko mat batana", "mat batana", "don't tell", "do not tell", "keep this between us", "मत बताना", "சொல்லாதீங்க"}
	messageMarkers = []string{"ko bolna", "ko batana ki", "ko kehna", "tell my son", "tell my daughter", "tell rahul", "tell priya", "please tell", "ko bata dena"}
	painWords      = []string{"dard", "pain", "ache", "दर्द", "வலி"}
	painLocations  = map[string][]string{
		"knee": {"ghutne", "ghutna", "knee", "घुटन", "முட்டி"},
		"back": {"kamar", "peeth", "back", "कमर", "முதுகு"},
		"head": {"sir", "sar mein", "head", "सिर", "தலை"},
		"leg":  {"pair", "taang", "leg", "पैर", "கால்"},
	}
	worse     = []string{"zyada", "badh gaya", "worse", "aur bura", "ज़्यादा"}
	better    = []string{"kam hai", "kam ho gaya", "better", "behtar", "कम"}
	agency    = []string{"cbi", "police", "customs", "income tax", "ed officer", "पुलिस"}
	pressure  = []string{"arrest", "giraftar", "paise", "money", "otp", "video call", "jail", "case", "warrant", "bhejo", "transfer"}
	otpBank   = []string{"otp", "bank account", "account number", "kyc", "aadhaar", "aadhar", "card number", "pin"}
	money     = []string{"paise bhejo", "paisa bhejo", "money transfer", "send money", "paise maange", "paise mang"}
	videoCall = []string{"video call pe raho", "video call par raho", "video call pe rehna", "stay on video call"}
	timeRe    = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*(baje|am|pm|o'clock)`)
)

func has(t string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// Extract applies the rules to the parent's turns.
func (Extractor) Extract(_ context.Context, in extract.Input) (extract.Report, error) {
	if len(in.Transcript) == 0 {
		return extract.Report{}, extract.ErrNoTranscript
	}
	r := extract.Report{
		SchemaVersion: extract.SchemaVersion, AnsweredBy: "parent",
		Mood: "unknown", Sleep: "unknown", Appetite: "unknown",
	}
	var parent []string
	words := 0
	for _, t := range in.Transcript {
		if t.Speaker != "agent" {
			parent = append(parent, t.Text)
			words += len(strings.Fields(t.Text))
		}
	}
	all := norm(strings.Join(parent, " \n "))

	switch {
	case words < 4:
		r.CallQuality = "unusable"
	case words < 12:
		r.CallQuality = "partial"
	default:
		r.CallQuality = "good"
	}
	if has(all, someoneElse) {
		r.AnsweredBy = "someone_else"
	}

	switch {
	case has(all, moodDistressed):
		r.Mood = "distressed"
	case has(all, moodLow):
		r.Mood = "low"
	case has(all, moodGood):
		r.Mood = "good"
	}
	switch {
	case has(all, sleepPoor):
		r.Sleep = "poor"
	case has(all, sleepGood):
		r.Sleep = "good"
	}
	switch {
	case has(all, appetitePoor):
		r.Appetite = "poor"
	case has(all, appetiteOK):
		r.Appetite = "normal"
	}

	r.Medicines = medicines(in.Transcript, in.MedicinesDue)

	for _, h := range alerts.DefaultDetector.Detect(parent) {
		if h.Category == "scam" {
			continue
		}
		sev := "emergency"
		if h.Category == "confusion" {
			sev = "urgent"
		}
		r.RedFlags = append(r.RedFlags, extract.RedFlag{Category: h.Category, Severity: sev, Quote: h.Quote})
	}
	r.ScamSignals = scams(parent)
	if len(r.RedFlags) > 0 && r.Mood == "good" {
		// "Theek hai" after a fall is not good spirits.
		r.Mood = "unknown"
	}

	var private []string
	for _, p := range parent {
		n := norm(p)
		switch {
		case has(n, privateMarkers):
			private = append(private, strings.TrimSpace(p))
		case has(n, messageMarkers):
			r.MessagesForFamily = append(r.MessagesForFamily, strings.TrimSpace(p))
		}
		if has(n, painWords) {
			for loc, ws := range painLocations {
				if has(n, ws) && !has(n, []string{"seene", "chhati", "chest", "सीने", "நெஞ்சு"}) {
					trend := "unknown"
					if has(n, worse) {
						trend = "worse"
					} else if has(n, better) {
						trend = "better"
					}
					r.Pain = append(r.Pain, extract.Pain{Location: loc, Trend: trend, Quote: p})
				}
			}
		}
	}
	r.PrivateNotes = private

	r.CallPreferences.StopRequested = has(all, stopWords)
	if m := timeRe.FindStringSubmatch(all); m != nil && has(all, []string{"call", "phone", "baje", "time"}) {
		h := atoi(m[1])
		if strings.EqualFold(m[3], "pm") && h < 12 {
			h += 12
		}
		mm := 0
		if m[2] != "" {
			mm = atoi(m[2])
		}
		if h >= 0 && h < 24 && mm < 60 {
			s := fmt.Sprintf("%02d:%02d", h, mm)
			r.CallPreferences.NewTimeRequested = &s
		}
	}

	for _, p := range r.Pain {
		if len(r.FollowUps) < extract.MaxFollowUps {
			r.FollowUps = append(r.FollowUps, "Ask how the "+p.Location+" pain is")
		}
	}
	if has(all, []string{"doctor ke paas", "doctor se milne", "doctor appointment", "डॉक्टर"}) && len(r.FollowUps) < extract.MaxFollowUps {
		r.FollowUps = append(r.FollowUps, "Ask how the doctor visit went")
	}

	r.Confidence = 0.9
	if r.CallQuality != "good" || r.AnsweredBy != "parent" {
		r.Confidence = 0.5
	}
	if r.AnsweredBy == "someone_else" {
		// Someone else answered: keep no health data about the parent.
		r.Mood, r.Sleep, r.Appetite = "unknown", "unknown", "unknown"
		r.Medicines, r.Pain, r.FollowUps, r.PrivateNotes, r.MessagesForFamily = nil, nil, nil, nil, nil
	}
	if err := r.Validate(); err != nil {
		return extract.Report{}, err
	}
	name := in.PreferredName
	if name == "" {
		name = "your parent"
	}
	var err error
	if r.FamilySummary, err = extract.FallbackSummary(r, name, in.FamilyLanguage); err != nil {
		return extract.Report{}, err
	}
	return r, nil
}

// medicines matches each due medicine to the parent's reply after the agent
// asks about it. MedicinesDue entries look like "Name (timing)".
func medicines(turns []voice.Turn, due []string) []extract.MedicineTaken {
	var out []extract.MedicineTaken
	for _, d := range due {
		name := strings.TrimSpace(strings.SplitN(d, " (", 2)[0])
		key := norm(strings.Fields(name + " x")[0])
		taken := "not_asked"
		for i, t := range turns {
			if t.Speaker != "agent" || !strings.Contains(norm(t.Text), key) {
				continue
			}
			for _, reply := range turns[i+1:] {
				if reply.Speaker == "agent" {
					break
				}
				n := norm(reply.Text)
				switch {
				case has(n, medNo):
					taken = "no"
				case has(n, medUnsure):
					taken = "unsure"
				case has(n, medYes):
					taken = "yes"
				default:
					taken = "unsure"
				}
				break
			}
			break
		}
		out = append(out, extract.MedicineTaken{Name: name, Taken: taken})
	}
	return out
}

// scams confirms a scam only with a pressure element: an agency plus a
// threat or demand, or a direct request for OTPs, money or a video call.
// A plain mention of "police" is not a scam.
func scams(parent []string) []extract.ScamSignal {
	var out []extract.ScamSignal
	seen := map[string]bool{}
	add := func(p, q string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, extract.ScamSignal{Pattern: p, Quote: alerts.Truncate(strings.TrimSpace(q), extract.MaxQuote)})
		}
	}
	for _, p := range parent {
		n := norm(p)
		if has(n, agency) && has(n, pressure) {
			add("agency_threat", p)
		}
		if has(n, otpBank) && has(n, []string{"maang", "mang", "pooch", "asked", "bata do", "share", "batao", "bhejo", "chahiye"}) {
			add("otp_or_bank_request", p)
		}
		if has(n, money) {
			add("money_request", p)
		}
		if has(n, videoCall) {
			add("video_call_pressure", p)
		}
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
