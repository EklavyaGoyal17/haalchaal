package calls

import "github.com/EklavyaGoyal17/haalchaal/internal/voice"

// Attempt states (calls.status).
const (
	StatusScheduled  = "scheduled"
	StatusDialing    = "dialing"
	StatusRinging    = "ringing"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusNoAnswer   = "no_answer"
	StatusBusy       = "busy"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

// Terminal reports whether no further transition is possible.
func Terminal(s string) bool {
	switch s {
	case StatusCompleted, StatusNoAnswer, StatusBusy, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// Unanswered reports whether a terminal status counts as an unanswered
// attempt for retries. A completed but unusable call is decided later, by
// the report.
func Unanswered(s string) bool {
	return s == StatusNoAnswer || s == StatusBusy || s == StatusFailed
}

// rank orders the live states; transitions only move forward.
var rank = map[string]int{
	StatusScheduled:  0,
	StatusDialing:    1,
	StatusRinging:    2,
	StatusInProgress: 3,
}

// CanTransition reports whether an attempt may move from one status to
// another (SPEC §5):
//
//	scheduled -> dialing -> ringing -> in_progress -> completed
//	dialing | ringing -> no_answer | busy | failed
//	any non-terminal state -> cancelled
//
// Also allowed, because platforms skip or reorder events: dialing ->
// in_progress (no ringing event), dialing | ringing -> completed (the
// answered event was lost), and in_progress -> failed (dropped mid-call).
func CanTransition(from, to string) bool {
	if Terminal(from) || from == to {
		return false
	}
	fr, ok := rank[from]
	if !ok {
		return false
	}
	switch to {
	case StatusCancelled:
		return true
	case StatusDialing:
		return from == StatusScheduled
	case StatusRinging:
		return from == StatusDialing
	case StatusInProgress:
		return from == StatusDialing || from == StatusRinging
	case StatusCompleted:
		return fr >= rank[StatusDialing]
	case StatusNoAnswer, StatusBusy:
		return from == StatusDialing || from == StatusRinging
	case StatusFailed:
		return fr >= rank[StatusDialing]
	}
	return false
}

// StatusForEvent maps a voice event to the attempt status it moves to. ok is
// false for events that carry no status, such as transcript_ready.
func StatusForEvent(t voice.EventType) (status string, ok bool) {
	switch t {
	case voice.EventRinging:
		return StatusRinging, true
	case voice.EventAnswered:
		return StatusInProgress, true
	case voice.EventCompleted:
		return StatusCompleted, true
	case voice.EventNoAnswer:
		return StatusNoAnswer, true
	case voice.EventBusy:
		return StatusBusy, true
	case voice.EventFailed:
		return StatusFailed, true
	}
	return "", false
}
