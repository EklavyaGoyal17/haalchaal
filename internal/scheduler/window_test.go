package scheduler

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func hm(h, m int) time.Duration { return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute }

func TestDue(t *testing.T) {
	tests := []struct {
		plan string
		day  time.Weekday
		want bool
	}{
		{PlanDaily, time.Sunday, true},
		{PlanDaily, time.Tuesday, true},
		{PlanBasic, time.Monday, true},
		{PlanBasic, time.Tuesday, false},
		{PlanBasic, time.Wednesday, true},
		{PlanBasic, time.Thursday, false},
		{PlanBasic, time.Friday, true},
		{PlanBasic, time.Saturday, false},
		{PlanBasic, time.Sunday, false},
		{"premium", time.Monday, false},
		{"", time.Monday, false},
	}
	for _, tt := range tests {
		if got := Due(tt.plan, tt.day); got != tt.want {
			t.Errorf("Due(%q, %v) = %v, want %v", tt.plan, tt.day, got, tt.want)
		}
	}
}

func TestWindow(t *testing.T) {
	def := Window{CallTime: hm(10, 30), Start: hm(9, 0), End: hm(20, 0)}
	early := Window{CallTime: hm(7, 0), Start: hm(9, 0), End: hm(20, 0)}
	late := Window{CallTime: hm(21, 0), Start: hm(9, 0), End: hm(20, 0)}
	inverted := Window{CallTime: hm(10, 0), Start: hm(20, 0), End: hm(9, 0)}
	tests := []struct {
		name          string
		w             Window
		wall          time.Duration
		contains, rdy bool
	}{
		{"before window", def, hm(8, 59), false, false},
		{"window open, before call time", def, hm(9, 0), true, false},
		{"at call time", def, hm(10, 30), true, true},
		{"server down all morning", def, hm(15, 0), true, true},
		{"one minute before end", def, hm(19, 59), true, true},
		{"at window end", def, hm(20, 0), false, false},
		{"after window end", def, hm(23, 0), false, false},
		{"call time before window waits for start", early, hm(7, 30), false, false},
		{"call time before window starts at window start", early, hm(9, 0), true, true},
		{"call time after window end never ready", late, hm(19, 0), true, false},
		{"inverted window never contains", inverted, hm(21, 0), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.w.Contains(tt.wall); got != tt.contains {
				t.Errorf("Contains = %v, want %v", got, tt.contains)
			}
			if got := tt.w.ReadyForFirstAttempt(tt.wall); got != tt.rdy {
				t.Errorf("ReadyForFirstAttempt = %v, want %v", got, tt.rdy)
			}
		})
	}
}

func TestLocalNow(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		tz   string
		date string
		wall time.Duration
		day  time.Weekday
	}{
		{"kolkata morning", time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC), "Asia/Kolkata", "2026-10-06", hm(9, 30), time.Tuesday},
		{"kolkata crosses midnight", time.Date(2026, 10, 6, 19, 0, 0, 0, time.UTC), "Asia/Kolkata", "2026-10-07", hm(0, 30), time.Wednesday},
		{"utc", time.Date(2026, 10, 6, 23, 59, 0, 0, time.UTC), "UTC", "2026-10-06", hm(23, 59), time.Tuesday},
		// London springs forward at 01:00 UTC on 29 March 2026.
		{"london before dst", time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC), "Europe/London", "2026-03-29", hm(0, 30), time.Sunday},
		{"london after dst", time.Date(2026, 3, 29, 9, 0, 0, 0, time.UTC), "Europe/London", "2026-03-29", hm(10, 0), time.Sunday},
		{"new york behind utc", time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC), "America/New_York", "2026-10-05", hm(22, 0), time.Monday},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := LocalNow(tt.now, tt.tz)
			if err != nil {
				t.Fatal(err)
			}
			if l.DateString() != tt.date || l.Wall != tt.wall || l.Day != tt.day {
				t.Errorf("got %s %v %v, want %s %v %v", l.DateString(), l.Wall, l.Day, tt.date, tt.wall, tt.day)
			}
		})
	}
	if _, err := LocalNow(time.Now(), "Mars/Olympus"); err == nil {
		t.Error("unknown timezone accepted")
	}
}

func TestInstantAt(t *testing.T) {
	d := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	got, err := InstantAt(d, hm(20, 0), "Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 6, 14, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("InstantAt = %v, want %v", got, want)
	}
}

func TestWindowFromPG(t *testing.T) {
	pt := func(d time.Duration) pgtype.Time { return pgtype.Time{Microseconds: d.Microseconds(), Valid: true} }
	w, err := WindowFromPG(pt(hm(10, 0)), pt(hm(9, 0)), pt(hm(20, 0)))
	if err != nil || w != (Window{hm(10, 0), hm(9, 0), hm(20, 0)}) {
		t.Fatalf("WindowFromPG = %+v, %v", w, err)
	}
	if _, err := WindowFromPG(pgtype.Time{}, pt(0), pt(1)); err == nil {
		t.Error("null time accepted")
	}
}
