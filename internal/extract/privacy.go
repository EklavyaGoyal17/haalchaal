package extract

import (
	"strings"
	"unicode"
)

// Normalize lowercases and collapses whitespace and punctuation runs, for
// comparing text regardless of formatting.
func Normalize(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// minLeakWords is the shortest private-note fragment treated as a leak. A
// note of one or two words ("BP", "knee") would match too much ordinary text,
// so short notes are checked whole instead of by fragments.
const minLeakWords = 3

// LeaksPrivate reports whether text contains any private note, after
// normalising case and whitespace. Besides the whole note, any run of
// minLeakWords consecutive words from a note counts, so a paraphrase that
// keeps a distinctive phrase is caught too.
func LeaksPrivate(text string, notes []string) bool {
	t := " " + Normalize(text) + " "
	for _, n := range notes {
		nn := Normalize(n)
		if nn == "" {
			continue
		}
		if strings.Contains(t, " "+nn+" ") {
			return true
		}
		words := strings.Fields(nn)
		for i := 0; i+minLeakWords <= len(words); i++ {
			if strings.Contains(t, " "+strings.Join(words[i:i+minLeakWords], " ")+" ") {
				return true
			}
		}
	}
	return false
}
