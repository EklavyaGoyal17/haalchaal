package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestMaskPhone(t *testing.T) {
	tests := []struct{ in, want string }{
		{"+919876541234", "+91******1234"},
		{"+14155550123", "+14*****0123"},
		{"+12345678", "+12**5678"},
		{"1234567", "***"},
		{"+91", "***"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := MaskPhone(tt.in); got != tt.want {
			t.Errorf("MaskPhone(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMaskPhoneNeverLeaksMiddleDigits(t *testing.T) {
	in := "+919876541234"
	got := MaskPhone(in)
	if bytes.Contains([]byte(got), []byte("987654")) {
		t.Fatalf("MaskPhone(%q) = %q leaks middle digits", in, got)
	}
}

func TestNewWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo).Info("hello", "k", "v")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("log line is not JSON: %v: %s", err, buf.String())
	}
	if m["msg"] != "hello" || m["k"] != "v" {
		t.Fatalf("unexpected log line: %v", m)
	}
}

func TestRequestID(t *testing.T) {
	ctx := context.Background()
	if RequestID(ctx) != "" {
		t.Fatal("empty context should have no request id")
	}
	ctx = WithRequestID(ctx, "abc")
	if got := RequestID(ctx); got != "abc" {
		t.Fatalf("RequestID = %q, want abc", got)
	}
	if a, b := NewRequestID(), NewRequestID(); a == b || len(a) != 16 {
		t.Fatalf("NewRequestID gave %q and %q", a, b)
	}
}
