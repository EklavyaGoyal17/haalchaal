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

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"+91 98765 43210", "+919876543210", true},
		{"98765-43210", "+919876543210", true},
		{"098765 43210", "+919876543210", true},
		{"0091 9876543210", "+919876543210", true},
		{"+44 (20) 7946 0958", "+442079460958", true},
		{"12345", "12345", false},
		{"5876543210", "5876543210", false}, // not an Indian mobile prefix, no country code
		{"98765x43210", "", false},
		{"9876+543210", "", false},
	}
	for _, tt := range tests {
		got, ok := NormalizePhone(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("NormalizePhone(%q) = %q %v, want %q %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
