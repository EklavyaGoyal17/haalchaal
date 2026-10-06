package calls

import (
	"testing"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

func TestCanTransition(t *testing.T) {
	all := []string{StatusScheduled, StatusDialing, StatusRinging, StatusInProgress, StatusCompleted, StatusNoAnswer, StatusBusy, StatusFailed, StatusCancelled}
	allowed := map[[2]string]bool{
		{StatusScheduled, StatusDialing}:    true,
		{StatusScheduled, StatusCancelled}:  true,
		{StatusDialing, StatusRinging}:      true,
		{StatusDialing, StatusInProgress}:   true,
		{StatusDialing, StatusCompleted}:    true,
		{StatusDialing, StatusNoAnswer}:     true,
		{StatusDialing, StatusBusy}:         true,
		{StatusDialing, StatusFailed}:       true,
		{StatusDialing, StatusCancelled}:    true,
		{StatusRinging, StatusInProgress}:   true,
		{StatusRinging, StatusCompleted}:    true,
		{StatusRinging, StatusNoAnswer}:     true,
		{StatusRinging, StatusBusy}:         true,
		{StatusRinging, StatusFailed}:       true,
		{StatusRinging, StatusCancelled}:    true,
		{StatusInProgress, StatusCompleted}: true,
		{StatusInProgress, StatusFailed}:    true,
		{StatusInProgress, StatusCancelled}: true,
	}
	for _, from := range append(all, "bogus") {
		for _, to := range append(all, "bogus") {
			want := allowed[[2]string{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestStatusForEvent(t *testing.T) {
	for ev, want := range map[voice.EventType]string{
		voice.EventRinging: StatusRinging, voice.EventAnswered: StatusInProgress, voice.EventCompleted: StatusCompleted,
		voice.EventNoAnswer: StatusNoAnswer, voice.EventBusy: StatusBusy, voice.EventFailed: StatusFailed,
	} {
		if got, ok := StatusForEvent(ev); !ok || got != want {
			t.Errorf("%s -> %s %v", ev, got, ok)
		}
	}
	if _, ok := StatusForEvent(voice.EventTranscriptReady); ok {
		t.Error("transcript_ready has a status")
	}
}

func TestUnansweredAndTerminal(t *testing.T) {
	for _, s := range []string{StatusNoAnswer, StatusBusy, StatusFailed} {
		if !Unanswered(s) || !Terminal(s) {
			t.Errorf("%s", s)
		}
	}
	if Unanswered(StatusCompleted) || Unanswered(StatusCancelled) || Terminal(StatusRinging) {
		t.Error("wrong classification")
	}
}

func TestNextAttemptAt(t *testing.T) {
	hm := func(h, m int) time.Duration { return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute }
	w := scheduler.Window{CallTime: hm(10, 0), Start: hm(9, 0), End: hm(20, 0)}
	ist := func(h, m int) time.Time {
		return time.Date(2026, 10, 6, h, m, 0, 0, time.FixedZone("IST", 19800)).UTC()
	}
	date := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	offsets := []time.Duration{15 * time.Minute, 45 * time.Minute}
	tests := []struct {
		name    string
		attempt int
		ended   time.Time
		offsets []time.Duration
		want    time.Time
		ok      bool
	}{
		{"after attempt 1", 1, ist(10, 1), offsets, ist(10, 16), true},
		{"after attempt 2", 2, ist(10, 20), offsets, ist(11, 5), true},
		{"no attempt 4", 3, ist(11, 6), offsets, time.Time{}, false},
		{"would cross window end", 1, ist(19, 50), offsets, time.Time{}, false},
		{"exactly at window end", 2, ist(19, 15), offsets, time.Time{}, false},
		{"just inside window end", 2, ist(19, 14), offsets, ist(19, 59), true},
		{"one offset configured", 2, ist(10, 20), offsets[:1], time.Time{}, false},
		{"no offsets configured", 1, ist(10, 1), nil, time.Time{}, false},
		{"invalid attempt", 0, ist(10, 1), offsets, time.Time{}, false},
		{"crosses midnight", 1, ist(23, 50), offsets, time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NextAttemptAt(tt.attempt, tt.ended, tt.offsets, w, "Asia/Kolkata", date)
			if ok != tt.ok || !got.Equal(tt.want) {
				t.Errorf("got %v %v, want %v %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
