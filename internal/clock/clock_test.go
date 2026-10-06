package clock

import (
	"testing"
	"time"
)

func TestFake(t *testing.T) {
	start := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	c := NewFake(start)
	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}
	c.Advance(15 * time.Minute)
	if got, want := c.Now(), start.Add(15*time.Minute); !got.Equal(want) {
		t.Fatalf("after Advance, Now() = %v, want %v", got, want)
	}
	ist := time.FixedZone("IST", 5*3600+1800)
	c.Set(time.Date(2026, 10, 8, 10, 0, 0, 0, ist))
	if loc := c.Now().Location(); loc != time.UTC {
		t.Fatalf("Set should normalise to UTC, got %v", loc)
	}
}

func TestRealIsUTC(t *testing.T) {
	if loc := (Real{}).Now().Location(); loc != time.UTC {
		t.Fatalf("Real.Now() location = %v, want UTC", loc)
	}
}
