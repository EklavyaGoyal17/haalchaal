package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
	"github.com/EklavyaGoyal17/haalchaal/prompts"
)

// Input is what extraction needs (SPEC §6).
type Input struct {
	Transcript     []voice.Turn
	PreferredName  string
	MedicinesDue   []string
	FollowUps      []string
	FamilyLanguage string // language of family_summary
}

// Extractor returns a validated Report.
type Extractor interface {
	Name() string
	Extract(ctx context.Context, in Input) (Report, error)
}

// InvalidOutputError means the model's output failed validation twice. Raw
// is the last output, to be stored encrypted for admin review.
type InvalidOutputError struct {
	Raw string
	Err error
}

func (e *InvalidOutputError) Error() string {
	return "extraction output invalid after retry: " + e.Err.Error()
}
func (e *InvalidOutputError) Unwrap() error { return e.Err }

// LLM is a text completion model.
type LLM interface {
	Name() string
	Complete(ctx context.Context, prompt string) (string, error)
}

// ModelExtractor extracts with an LLM, validating in Go and retrying once
// with the validation problems appended.
type ModelExtractor struct{ LLM LLM }

// Name identifies the model in call_reports.model.
func (m ModelExtractor) Name() string { return m.LLM.Name() }

// Extract renders the prompt, calls the model, and validates its output.
func (m ModelExtractor) Extract(ctx context.Context, in Input) (Report, error) {
	prompt, err := RenderPrompt(in)
	if err != nil {
		return Report{}, err
	}
	raw, err := m.LLM.Complete(ctx, prompt)
	if err != nil {
		return Report{}, fmt.Errorf("extract: %w", err)
	}
	r, verr := Parse(raw)
	if verr == nil {
		return r, nil
	}
	retry := prompt + "\n\nYour previous answer was rejected: " + verr.Error() +
		"\nReturn only the corrected JSON object."
	raw, err = m.LLM.Complete(ctx, retry)
	if err != nil {
		return Report{}, fmt.Errorf("extract retry: %w", err)
	}
	if r, verr = Parse(raw); verr == nil {
		return r, nil
	}
	return Report{}, &InvalidOutputError{Raw: raw, Err: verr}
}

var extractTemplate = template.Must(template.New("extract.tmpl").Option("missingkey=error").ParseFS(prompts.FS, "extract.tmpl"))

// LanguageName names a family language for the prompt.
func LanguageName(code string) string {
	switch code {
	case "hi":
		return "Hindi"
	case "ta":
		return "Tamil"
	default:
		return "English"
	}
}

// RenderPrompt executes prompts/extract.tmpl.
func RenderPrompt(in Input) (string, error) {
	if len(in.Transcript) == 0 {
		return "", ErrNoTranscript
	}
	schema, err := prompts.FS.ReadFile("report.schema.json")
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = extractTemplate.Execute(&b, map[string]any{
		"MedicinesDue":       in.MedicinesDue,
		"FollowUps":          in.FollowUps,
		"FamilyLanguageName": LanguageName(in.FamilyLanguage),
		"Schema":             strings.TrimSpace(string(schema)),
		"TranscriptText":     TranscriptText(in.Transcript),
	})
	if err != nil {
		return "", fmt.Errorf("render extract prompt: %w", err)
	}
	return b.String(), nil
}

// TranscriptText formats turns one per line as "speaker: text". Speakers are
// limited to agent and parent, and text cannot forge the closing marker or a
// new speaker line.
func TranscriptText(turns []voice.Turn) string {
	var b strings.Builder
	for _, t := range turns {
		sp := "parent"
		if t.Speaker == "agent" {
			sp = "agent"
		}
		text := strings.Join(strings.Fields(t.Text), " ")
		text = strings.NewReplacer("<<<", "", ">>>", "", "TRANSCRIPT", "transcript").Replace(text)
		fmt.Fprintf(&b, "%s: %s\n", sp, text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ParentTexts returns what the parent said, one entry per turn.
func ParentTexts(turns []voice.Turn) []string {
	var out []string
	for _, t := range turns {
		if t.Speaker != "agent" {
			out = append(out, t.Text)
		}
	}
	return out
}

// IsInvalidOutput reports whether err is an InvalidOutputError.
func IsInvalidOutput(err error) (*InvalidOutputError, bool) {
	var e *InvalidOutputError
	ok := errors.As(err, &e)
	return e, ok
}
