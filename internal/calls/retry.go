package calls

import (
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
)

// MaxAttempts is the hard limit per slot, matching the calls table check.
const MaxAttempts = 3

// NextAttemptAt decides whether another attempt follows an unanswered one,
// and when. offsets are RETRY_OFFSETS (gap after attempt 1, after attempt 2).
// The next attempt must start on the slot's local date, before window_end
// and not before window_start. ok is false when the slot is out of attempts.
func NextAttemptAt(attemptNo int, endedAt time.Time, offsets []time.Duration, w scheduler.Window, tz string, slotDate time.Time) (time.Time, bool) {
	if attemptNo < 1 || attemptNo >= MaxAttempts || attemptNo > len(offsets) {
		return time.Time{}, false
	}
	at := endedAt.Add(offsets[attemptNo-1])
	local, err := scheduler.LocalNow(at, tz)
	if err != nil || !local.Date.Equal(slotDate) {
		return time.Time{}, false
	}
	if local.Wall >= w.End {
		return time.Time{}, false
	}
	if local.Wall < w.Start { // cannot happen after a same-day attempt, but stay inside the window
		start, err := scheduler.InstantAt(slotDate, w.Start, tz)
		if err != nil {
			return time.Time{}, false
		}
		at = start
	}
	return at, true
}
