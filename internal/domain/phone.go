// Package domain holds core types and enums shared across the backend.
package domain

import "regexp"

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// ValidE164 reports whether s is a phone number in E.164 form, such as +919876541234.
func ValidE164(s string) bool { return e164.MatchString(s) }

// NormalizePhone turns common ways of writing a number into E.164: spaces,
// dashes, dots and brackets are dropped, a leading 00 becomes +, and a
// 10-digit Indian mobile number (optionally with a leading 0) gets +91. It
// returns false when the result is still not E.164.
func NormalizePhone(s string) (string, bool) {
	var b []byte
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b = append(b, byte(r))
		case r == '+' && len(b) == 0 && i == indexFirstNonSpace(s):
			b = append(b, '+')
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", false
		}
	}
	n := string(b)
	switch {
	case len(n) > 2 && n[:2] == "00":
		n = "+" + n[2:]
	case len(n) == 11 && n[0] == '0' && n[1] >= '6':
		n = "+91" + n[1:]
	case len(n) == 10 && n[0] >= '6':
		n = "+91" + n
	}
	return n, ValidE164(n)
}

func indexFirstNonSpace(s string) int {
	for i, r := range s {
		if r != ' ' {
			return i
		}
	}
	return 0
}
