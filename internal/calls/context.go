package calls

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
	"github.com/EklavyaGoyal17/haalchaal/prompts"
)

// AAD names for the encrypted columns read here.
const (
	AADMedicineName = "medicines.name_enc"
	AADInterests    = "parents.interests_enc"
	AADSafeWord     = "parents.safe_word_enc"
	AADMemory       = "memories.content_enc"
	AADTranscript   = "transcripts.turns_enc"
)

// Limits on what goes into the agent prompt.
const (
	maxFollowUps  = 3
	maxInterests  = 3
	maxPromptItem = 120
	maxPromptName = 60
)

// Medicine is one medicine due today.
type Medicine struct{ Name, Timing string }

// CallContext is everything the agent prompt needs (SPEC §7).
type CallContext struct {
	PreferredName     string
	LanguageName      string
	FamilyNames       string
	PrimaryFamilyName string
	IsFirstCall       bool
	MedicinesDue      []Medicine
	FollowUps         []string
	Interests         []string
	SafeWord          string
	NextCallTime      string
}

// LanguageNames maps parents.language to the name used in the prompt.
var LanguageNames = map[string]string{"hi": "Hindi", "ta": "Tamil", "en": "English"}

var agentTemplate = template.Must(template.New("agent_system.tmpl").Option("missingkey=error").ParseFS(prompts.FS, "agent_system.tmpl"))

// Render executes prompts/agent_system.tmpl.
func (c CallContext) Render() (string, error) {
	var b bytes.Buffer
	if err := agentTemplate.Execute(&b, c); err != nil {
		return "", fmt.Errorf("render agent prompt: %w", err)
	}
	return b.String(), nil
}

// Variables returns the same values as a flat map, for platforms that
// template prompts themselves.
func (c CallContext) Variables() map[string]string {
	meds := make([]string, len(c.MedicinesDue))
	for i, m := range c.MedicinesDue {
		meds[i] = m.Name + " (" + m.Timing + ")"
	}
	first := "false"
	if c.IsFirstCall {
		first = "true"
	}
	return map[string]string{
		"preferred_name":      c.PreferredName,
		"language_name":       c.LanguageName,
		"family_names":        c.FamilyNames,
		"primary_family_name": c.PrimaryFamilyName,
		"is_first_call":       first,
		"medicines_due":       strings.Join(meds, "; "),
		"follow_ups":          strings.Join(c.FollowUps, "; "),
		"interests":           strings.Join(c.Interests, "; "),
		"safe_word":           c.SafeWord,
		"next_call_time":      c.NextCallTime,
	}
}

// SanitizePromptText flattens text that will be placed inside the agent
// prompt: no control characters or newlines, no template braces, bounded
// length. Follow-ups come from what a parent said in an earlier call, so they
// are data and must not be able to restructure the prompt (CLAUDE.md rule 13).
func SanitizePromptText(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == '{' || r == '}' || r == '`':
			continue
		case unicode.IsSpace(r) || unicode.IsControl(r):
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > max {
		out = strings.TrimSpace(string(r[:max]))
	}
	return out
}

// LoadContext reads and decrypts the call context for a parent.
func LoadContext(ctx context.Context, q *db.Queries, kr *crypto.Keyring, parentID uuid.UUID, now time.Time) (CallContext, db.GetParentWithAccountRow, error) {
	p, err := q.GetParentWithAccount(ctx, parentID)
	if err != nil {
		return CallContext{}, p, fmt.Errorf("load parent: %w", err)
	}
	c := CallContext{
		PreferredName: SanitizePromptText(p.PreferredName, maxPromptName),
		LanguageName:  LanguageNames[p.Language],
		IsFirstCall:   p.FirstCallDoneAt == nil,
	}
	if c.LanguageName == "" {
		return c, p, fmt.Errorf("parent language %q has no name", p.Language)
	}

	members, err := q.ListFamilyMembers(ctx, p.AccountID)
	if err != nil {
		return c, p, fmt.Errorf("load family: %w", err)
	}
	names := make([]string, 0, len(members))
	for _, m := range members { // ordered by priority, primary first
		names = append(names, SanitizePromptText(m.Name, maxPromptName))
	}
	if len(names) == 0 {
		return c, p, fmt.Errorf("parent has no family members")
	}
	c.FamilyNames = strings.Join(names, ", ")
	c.PrimaryFamilyName = names[0]

	meds, err := q.ListActiveMedicines(ctx, parentID)
	if err != nil {
		return c, p, fmt.Errorf("load medicines: %w", err)
	}
	for _, m := range meds {
		name, err := decrypt(kr, m.NameEnc, AADMedicineName)
		if err != nil {
			return c, p, err
		}
		c.MedicinesDue = append(c.MedicinesDue, Medicine{
			Name:   SanitizePromptText(name, maxPromptItem),
			Timing: SanitizePromptText(m.Timing, maxPromptItem),
		})
	}

	fus, err := q.ListActiveFollowUps(ctx, db.ListActiveFollowUpsParams{ParentID: parentID, Now: &now, MaxItems: maxFollowUps})
	if err != nil {
		return c, p, fmt.Errorf("load follow-ups: %w", err)
	}
	for _, m := range fus {
		s, err := decrypt(kr, m.ContentEnc, AADMemory)
		if err != nil {
			return c, p, err
		}
		if s = SanitizePromptText(s, maxPromptItem); s != "" {
			c.FollowUps = append(c.FollowUps, s)
		}
	}

	if len(p.InterestsEnc) > 0 {
		raw, err := decrypt(kr, p.InterestsEnc, AADInterests)
		if err != nil {
			return c, p, err
		}
		var interests []string
		if err := json.Unmarshal([]byte(raw), &interests); err != nil {
			return c, p, fmt.Errorf("decode interests: %w", err)
		}
		for _, s := range interests {
			if s = SanitizePromptText(s, maxPromptItem); s != "" && len(c.Interests) < maxInterests {
				c.Interests = append(c.Interests, s)
			}
		}
	}
	if len(p.SafeWordEnc) > 0 {
		sw, err := decrypt(kr, p.SafeWordEnc, AADSafeWord)
		if err != nil {
			return c, p, err
		}
		c.SafeWord = SanitizePromptText(sw, maxPromptName)
	}

	w, err := scheduler.WindowFromPG(p.CallTimeLocal, p.WindowStart, p.WindowEnd)
	if err != nil {
		return c, p, err
	}
	c.NextCallTime, err = NextCallTime(now, p.Timezone, p.Plan, w)
	return c, p, err
}

func decrypt(kr *crypto.Keyring, ct []byte, aad string) (string, error) {
	if kr == nil {
		return "", fmt.Errorf("decrypt %s: no encryption keys configured", aad)
	}
	s, err := kr.DecryptString(ct, aad)
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", aad, err)
	}
	return s, nil
}

// NextCallTime describes the next due day after today, such as "tomorrow at
// 10:00" or "on Wednesday at 10:00".
func NextCallTime(now time.Time, tz, plan string, w scheduler.Window) (string, error) {
	local, err := scheduler.LocalNow(now, tz)
	if err != nil {
		return "", err
	}
	e := w.Earliest()
	at := fmt.Sprintf("%02d:%02d", int(e/time.Hour), int(e%time.Hour/time.Minute))
	for i := 1; i <= 7; i++ {
		d := local.Date.AddDate(0, 0, i)
		if !scheduler.Due(plan, d.Weekday()) {
			continue
		}
		if i == 1 {
			return "tomorrow at " + at, nil
		}
		return "on " + d.Weekday().String() + " at " + at, nil
	}
	return "", fmt.Errorf("plan %q has no due day", plan)
}
