package jobs

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	tests := []struct {
		attempts int32
		want     time.Duration
	}{
		{-1, 30 * time.Second},
		{0, 30 * time.Second},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 30 * time.Minute},
		{20, 30 * time.Minute},
		{1 << 30, 30 * time.Minute},
	}
	for _, tt := range tests {
		if got := Backoff(tt.attempts); got != tt.want {
			t.Errorf("Backoff(%d) = %v, want %v", tt.attempts, got, tt.want)
		}
	}
}

func TestPermanent(t *testing.T) {
	base := errors.New("bad payload")
	if Permanent(nil) != nil {
		t.Error("Permanent(nil) != nil")
	}
	p := Permanent(base)
	if !IsPermanent(p) || !errors.Is(p, base) {
		t.Error("Permanent lost its identity")
	}
	if !IsPermanent(fmt.Errorf("wrapped: %w", p)) {
		t.Error("wrapped permanent not detected")
	}
	if IsPermanent(base) {
		t.Error("plain error is permanent")
	}
}

func TestTruncateError(t *testing.T) {
	long := errors.New(strings.Repeat("x", 2000))
	if got := truncateError(long); len(got) != maxErrorLen {
		t.Errorf("len = %d", len(got))
	}
	if got := truncateError(errors.New("short")); got != "short" {
		t.Errorf("got %q", got)
	}
}

func TestLastAttempt(t *testing.T) {
	if (Job{Attempts: 4, MaxAttempts: 5}).LastAttempt() {
		t.Error("4/5 is not last")
	}
	if !(Job{Attempts: 5, MaxAttempts: 5}).LastAttempt() {
		t.Error("5/5 is last")
	}
}
