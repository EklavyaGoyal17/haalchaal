// Package scheduler selects due calls: plan days, call windows in the parent's
// timezone, and the per-minute tick that creates each day's slot (SPEC §5).
package scheduler

import (
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Plans (accounts.plan).
const (
	PlanDaily = "daily"
	PlanBasic = "basic"
)

// Due reports whether a plan calls on the given local weekday. daily is every
// day; basic is Monday, Wednesday and Friday. Unknown plans are never due.
func Due(plan string, wd time.Weekday) bool {
	switch plan {
	case PlanDaily:
		return true
	case PlanBasic:
		return wd == time.Monday || wd == time.Wednesday || wd == time.Friday
	default:
		return false
	}
}

// Window is a parent's daily call window as offsets from local midnight.
type Window struct {
	CallTime time.Duration // preferred call time
	Start    time.Duration // window_start, default 09:00
	End      time.Duration // window_end, default 20:00
}

// Earliest is when the first attempt may start: the preferred call time, but
// never before the window opens.
func (w Window) Earliest() time.Duration { return max(w.CallTime, w.Start) }

// boundsOK reports whether start and end form a same-day window.
func (w Window) boundsOK() bool {
	return w.Start >= 0 && w.End <= 24*time.Hour && w.Start < w.End
}

// Valid reports whether the window can hold a first attempt at all.
func (w Window) Valid() bool { return w.boundsOK() && w.Earliest() < w.End }

// Contains reports whether a local wall-clock offset is inside the window
// (start inclusive, end exclusive).
func (w Window) Contains(wall time.Duration) bool {
	return w.boundsOK() && wall >= w.Start && wall < w.End
}

// ReadyForFirstAttempt reports whether today's slot should be created now.
func (w Window) ReadyForFirstAttempt(wall time.Duration) bool {
	return w.Valid() && wall >= w.Earliest() && wall < w.End
}

// WindowFromPG converts TIME columns into a Window.
func WindowFromPG(callTime, start, end pgtype.Time) (Window, error) {
	if !callTime.Valid || !start.Valid || !end.Valid {
		return Window{}, errors.New("call window has a null time")
	}
	us := func(t pgtype.Time) time.Duration { return time.Duration(t.Microseconds) * time.Microsecond }
	return Window{CallTime: us(callTime), Start: us(start), End: us(end)}, nil
}

// Local is an instant seen in a parent's timezone.
type Local struct {
	Date time.Time     // local calendar date at 00:00 UTC, for DATE columns
	Wall time.Duration // wall-clock time since local midnight
	Day  time.Weekday
}

// PGDate returns Date as a DATE parameter.
func (l Local) PGDate() pgtype.Date { return pgtype.Date{Time: l.Date, Valid: true} }

// DateString returns the local date as YYYY-MM-DD.
func (l Local) DateString() string { return l.Date.Format(time.DateOnly) }

// LocalNow converts now into the named timezone. Wall is the clock on the
// wall, not elapsed time, so on a daylight-saving change day 09:00 is still
// 09:00.
func LocalNow(now time.Time, tz string) (Local, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Local{}, fmt.Errorf("timezone %q: %w", tz, err)
	}
	t := now.In(loc)
	y, m, d := t.Date()
	wall := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute +
		time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond())
	return Local{Date: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Wall: wall, Day: t.Weekday()}, nil
}

// InstantAt returns the UTC instant of a wall-clock offset on a local date in
// tz. Used to compute retry times that must not cross window_end.
func InstantAt(date time.Time, wall time.Duration, tz string) (time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}, fmt.Errorf("timezone %q: %w", tz, err)
	}
	h := int(wall / time.Hour)
	m := int(wall % time.Hour / time.Minute)
	s := int(wall % time.Minute / time.Second)
	return time.Date(date.Year(), date.Month(), date.Day(), h, m, s, 0, loc).UTC(), nil
}
