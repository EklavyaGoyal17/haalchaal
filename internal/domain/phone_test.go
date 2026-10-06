package domain

import "testing"

func TestValidE164(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"+919876541234", true},
		{"+14155550123", true},
		{"+12345678", true},
		{"919876541234", false},   // missing +
		{"+0919876541234", false}, // country code cannot start with 0
		{"+91 98765 41234", false},
		{"+91-9876541234", false},
		{"+1234567", false},          // too short
		{"+1234567890123456", false}, // too long
		{"", false},
		{"+91987654123a", false},
	}
	for _, tt := range tests {
		if got := ValidE164(tt.in); got != tt.want {
			t.Errorf("ValidE164(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
