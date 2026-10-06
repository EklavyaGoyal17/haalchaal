package extract

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/EklavyaGoyal17/haalchaal/prompts"
)

var fallbackTemplate = template.Must(template.New("summary_fallback.tmpl").
	Funcs(template.FuncMap{"join": strings.Join}).
	ParseFS(prompts.FS, "summary_fallback.tmpl"))

type fallbackData struct {
	Name, Language, Mood, Sleep, Appetite string
	MedsTaken, MedsMissed, MedsUnsure     []string
	Pains, Messages                       []string
	SomeoneElse                           bool
}

// FallbackSummary builds a family summary from structured fields only, with
// prompts/summary_fallback.tmpl. It never reads private_notes, and drops any
// message for the family that repeats one.
func FallbackSummary(r Report, name, language string) (string, error) {
	d := fallbackData{Name: name, Language: language, Mood: r.Mood, Sleep: r.Sleep, Appetite: r.Appetite}
	if r.AnsweredBy == "someone_else" {
		// Nothing about the parent's health goes in this summary.
		d.SomeoneElse = true
		var b bytes.Buffer
		if err := fallbackTemplate.Execute(&b, d); err != nil {
			return "", fmt.Errorf("fallback summary: %w", err)
		}
		return strings.Join(strings.Fields(b.String()), " "), nil
	}
	for _, m := range r.Medicines {
		switch m.Taken {
		case "yes":
			d.MedsTaken = append(d.MedsTaken, m.Name)
		case "no":
			d.MedsMissed = append(d.MedsMissed, m.Name)
		case "unsure":
			d.MedsUnsure = append(d.MedsUnsure, m.Name)
		}
	}
	for _, p := range r.Pain {
		if p.Location != "" {
			d.Pains = append(d.Pains, p.Location)
		}
	}
	for _, m := range r.MessagesForFamily {
		if !LeaksPrivate(m, r.PrivateNotes) {
			d.Messages = append(d.Messages, strings.TrimRight(strings.TrimSpace(m), ".।!"))
		}
	}
	var b bytes.Buffer
	if err := fallbackTemplate.Execute(&b, d); err != nil {
		return "", fmt.Errorf("fallback summary: %w", err)
	}
	return capStr(strings.Join(strings.Fields(b.String()), " "), MaxSummary), nil
}

// SafeSummary returns the summary that may go to the family: the model's
// summary unless it repeats a private note, in which case the fallback.
// It reports whether the fallback was used.
func SafeSummary(r Report, name, language string) (string, bool, error) {
	if r.AnsweredBy == "someone_else" {
		s, err := FallbackSummary(r, name, language)
		return s, true, err
	}
	s := strings.Join(strings.Fields(r.FamilySummary), " ")
	if s != "" && !LeaksPrivate(s, r.PrivateNotes) {
		return s, false, nil
	}
	fb, err := FallbackSummary(r, name, language)
	return fb, true, err
}
