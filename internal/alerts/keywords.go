package alerts

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

//go:embed keywords.yaml
var keywordsFile []byte

// CategoryScamKeyword marks a scam keyword hit the model did not confirm.
const CategoryScamKeyword = "scam_keyword"

// Hit is one keyword match.
type Hit struct {
	Category string
	Phrase   string
	Quote    string // the text the phrase was found in, capped
}

// Detector matches the keyword list against transcript text.
type Detector struct {
	phrases map[string][]string // category -> normalised phrases
	order   []string
}

// DefaultDetector is built from the embedded keywords.yaml.
var DefaultDetector = mustDetector(keywordsFile)

func mustDetector(b []byte) *Detector {
	d, err := ParseKeywords(b)
	if err != nil {
		panic(err) // the embedded file is checked by tests
	}
	return d
}

// ParseKeywords reads the restricted YAML subset used by keywords.yaml:
// "category:" lines followed by "- phrase" lines; # comments and blank lines.
func ParseKeywords(b []byte) (*Detector, error) {
	d := &Detector{phrases: map[string][]string{}}
	cur := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "- "):
			if cur == "" {
				return nil, fmt.Errorf("keywords line %d: phrase before any category", n)
			}
			p := normalizeText(strings.Trim(strings.TrimSpace(line[2:]), `"'`))
			if p == "" {
				return nil, fmt.Errorf("keywords line %d: empty phrase", n)
			}
			d.phrases[cur] = append(d.phrases[cur], p)
		case strings.HasSuffix(line, ":") && !strings.ContainsAny(line, " -"):
			cur = strings.TrimSuffix(line, ":")
			if _, dup := d.phrases[cur]; dup {
				return nil, fmt.Errorf("keywords line %d: duplicate category %q", n, cur)
			}
			d.phrases[cur] = nil
			d.order = append(d.order, cur)
		default:
			return nil, fmt.Errorf("keywords line %d: unrecognised line", n)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for _, c := range d.order {
		if len(d.phrases[c]) == 0 {
			return nil, fmt.Errorf("keywords: category %q has no phrases", c)
		}
	}
	return d, nil
}

// Categories returns the categories in file order.
func (d *Detector) Categories() []string { return append([]string(nil), d.order...) }

// normalizeText lowercases and collapses whitespace; curly apostrophes become
// straight ones so "can’t" matches "can't".
func normalizeText(s string) string {
	s = strings.NewReplacer("’", "'", "‘", "'").Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// Detect returns at most one hit per category for the given texts (each text
// is one utterance), in category order.
func (d *Detector) Detect(texts []string) []Hit {
	var hits []Hit
	for _, cat := range d.order {
	search:
		for _, raw := range texts {
			t := normalizeText(raw)
			for _, p := range d.phrases[cat] {
				if containsPhrase(t, p) {
					hits = append(hits, Hit{Category: cat, Phrase: p, Quote: Truncate(strings.TrimSpace(raw), MaxQuote)})
					break search
				}
			}
		}
	}
	return hits
}

// containsPhrase finds p in t. A phrase that starts or ends with a Latin
// letter must not touch another letter there, so "fell" does not match
// "fellow" and "otp" does not match "hotpot". Other scripts match as
// substrings, because word boundaries there are not reliable.
func containsPhrase(t, p string) bool {
	for i := 0; ; {
		j := strings.Index(t[i:], p)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(p)
		if boundaryOK(t, start, end, p) {
			return true
		}
		_, size := utf8.DecodeRuneInString(t[start:])
		i = start + size
	}
}

func boundaryOK(t string, start, end int, p string) bool {
	first, _ := utf8.DecodeRuneInString(p)
	last, _ := utf8.DecodeLastRuneInString(p)
	if isLatin(first) && start > 0 {
		r, _ := utf8.DecodeLastRuneInString(t[:start])
		if unicode.IsLetter(r) {
			return false
		}
	}
	if isLatin(last) && end < len(t) {
		r, _ := utf8.DecodeRuneInString(t[end:])
		if unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func isLatin(r rune) bool { return r < 0x250 && unicode.IsLetter(r) }

// RedFlagSeverity is the alert type for a keyword-only red-flag hit. Safety
// wins over noise: everything is an emergency except confusion, which is
// urgent.
func RedFlagSeverity(category string) string {
	if category == "confusion" {
		return TypeUrgent
	}
	return TypeEmergency
}
