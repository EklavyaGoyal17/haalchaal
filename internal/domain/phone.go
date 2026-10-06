// Package domain holds core types and enums shared across the backend.
package domain

import "regexp"

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// ValidE164 reports whether s is a phone number in E.164 form, such as +919876541234.
func ValidE164(s string) bool { return e164.MatchString(s) }
