//go:build integration

package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
)

// Tuesday 6 Oct 2026. Kolkata is UTC+05:30.
func ist(h, m int) time.Time {
	return time.Date(2026, 10, 6, h, m, 0, 0, time.FixedZone("IST", 5*3600+1800)).UTC()
}

func newScheduler(pool *pgxpool.Pool, clk clock.Clock) *scheduler.Scheduler {
	return &scheduler.Scheduler{Pool: pool, Clock: clk, Log: testdb.Log(), Provider: "fake"}
}

type counts struct{ slots, calls, jobs int }

func count(t *testing.T, pool *pgxpool.Pool, parent uuid.UUID) counts {
	t.Helper()
	var c counts
	err := pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM call_slots WHERE parent_id = $1),
		       (SELECT count(*) FROM calls WHERE parent_id = $1),
		       (SELECT count(*) FROM jobs WHERE kind = 'place_call' AND payload->>'parent_id' = $1::text)`,
		parent).Scan(&c.slots, &c.calls, &c.jobs)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExactlyOneSlotPerDueDay(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	f := testdb.CreateFamily(t, pool, testdb.Family{})
	clk := clock.NewFake(ist(10, 0))

	// Many concurrent ticks across "processes", then more ticks later in the day.
	var wg sync.WaitGroup
	for range 10 {
		s := newScheduler(pool, clk)
		wg.Go(func() {
			for range 5 {
				if _, err := s.Tick(ctx); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	clk.Set(ist(15, 0))
	if _, err := newScheduler(pool, clk).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if c := count(t, pool, f.ParentID); c != (counts{1, 1, 1}) {
		t.Fatalf("got %+v, want one slot, one attempt, one job", c)
	}

	var attempt int
	var status, provider string
	var scheduled time.Time
	if err := pool.QueryRow(ctx, `SELECT attempt_no, status, provider, scheduled_for FROM calls WHERE parent_id = $1`, f.ParentID).
		Scan(&attempt, &status, &provider, &scheduled); err != nil {
		t.Fatal(err)
	}
	if attempt != 1 || status != "scheduled" || provider != "fake" || !scheduled.Equal(ist(10, 0)) {
		t.Fatalf("attempt %d %s %s %v", attempt, status, provider, scheduled)
	}
	var key string
	_ = pool.QueryRow(ctx, `SELECT dedupe_key FROM jobs WHERE kind='place_call'`).Scan(&key)
	var callID uuid.UUID
	_ = pool.QueryRow(ctx, `SELECT id FROM calls`).Scan(&callID)
	if key != "place_call:"+callID.String() {
		t.Fatalf("dedupe key %q", key)
	}

	// Next day gets its own slot.
	clk.Set(ist(10, 0).Add(24 * time.Hour))
	if _, err := newScheduler(pool, clk).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if c := count(t, pool, f.ParentID); c.slots != 2 {
		t.Fatalf("day two: %+v", c)
	}
}

func TestSchedulingRules(t *testing.T) {
	tests := []struct {
		name  string
		fam   testdb.Family
		at    time.Time
		slots int
	}{
		{"before call time", testdb.Family{}, ist(9, 59), 0},
		{"at call time", testdb.Family{}, ist(10, 0), 1},
		{"server down all morning", testdb.Family{}, ist(19, 59), 1},
		{"at window end", testdb.Family{}, ist(20, 0), 0},
		{"after window end", testdb.Family{}, ist(22, 0), 0},
		{"call time before window waits", testdb.Family{CallTime: "08:00"}, ist(8, 30), 0},
		{"call time before window starts at window", testdb.Family{CallTime: "08:00"}, ist(9, 0), 1},
		{"basic plan on tuesday", testdb.Family{Plan: "basic"}, ist(11, 0), 0},
		{"paused parent", testdb.Family{ParentStatus: "paused"}, ist(11, 0), 0},
		{"stopped parent", testdb.Family{ParentStatus: "stopped"}, ist(11, 0), 0},
		{"onboarding parent", testdb.Family{ParentStatus: "onboarding"}, ist(11, 0), 0},
		{"trial account", testdb.Family{AccountStatus: "trial"}, ist(11, 0), 1},
		{"paused account", testdb.Family{AccountStatus: "paused"}, ist(11, 0), 0},
		{"cancelled account", testdb.Family{AccountStatus: "cancelled"}, ist(11, 0), 0},
		{"no consents", testdb.Family{NoConsents: true}, ist(11, 0), 0},
		{"missing share consent", testdb.Family{Consents: []string{"calls", "data_processing"}}, ist(11, 0), 0},
		{"recording not required", testdb.Family{Consents: []string{"calls", "data_processing", "share_with_family"}}, ist(11, 0), 1},
		// 11:00 IST is 06:30 BST in London: before that parent's window.
		{"other timezone not yet", testdb.Family{Timezone: "Europe/London"}, ist(11, 0), 0},
		{"other timezone open", testdb.Family{Timezone: "Europe/London"}, ist(15, 0), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := testdb.New(t)
			f := testdb.CreateFamily(t, pool, tt.fam)
			if _, err := newScheduler(pool, clock.NewFake(tt.at)).Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if c := count(t, pool, f.ParentID); c.slots != tt.slots || c.calls != tt.slots || c.jobs != tt.slots {
				t.Fatalf("got %+v, want %d of each", c, tt.slots)
			}
		})
	}
}

func TestWithdrawnConsentStopsScheduling(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	f := testdb.CreateFamily(t, pool, testdb.Family{})
	if _, err := pool.Exec(ctx, `UPDATE consents SET withdrawn_at = $2 WHERE parent_id = $1 AND kind = 'calls'`, f.ParentID, ist(8, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := newScheduler(pool, clock.NewFake(ist(11, 0))).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if c := count(t, pool, f.ParentID); c.slots != 0 {
		t.Fatalf("slot created after withdrawal: %+v", c)
	}
}

func TestSkippedDayLoggedOnce(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	testdb.CreateFamily(t, pool, testdb.Family{})
	s := newScheduler(pool, clock.NewFake(ist(21, 0)))
	r1, _ := s.Tick(ctx)
	r2, _ := s.Tick(ctx)
	if r1.Skipped != 1 || r2.Skipped != 0 || r1.Created != 0 {
		t.Fatalf("first %+v, second %+v", r1, r2)
	}
}

func TestBasicPlanMonWedFri(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	f := testdb.CreateFamily(t, pool, testdb.Family{Plan: "basic"})
	clk := clock.NewFake(ist(11, 0).AddDate(0, 0, -1)) // Monday 5 Oct
	s := newScheduler(pool, clk)
	for range 7 {
		if _, err := s.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		clk.Advance(24 * time.Hour)
	}
	rows, err := pool.Query(ctx, `SELECT extract(isodow FROM local_date)::int FROM call_slots WHERE parent_id = $1 ORDER BY local_date`, f.ParentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var days []int
	for rows.Next() {
		var d int
		_ = rows.Scan(&d)
		days = append(days, d)
	}
	if len(days) != 3 || days[0] != 1 || days[1] != 3 || days[2] != 5 {
		t.Fatalf("slot weekdays %v, want [1 3 5]", days)
	}
}

func TestManyParents(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	for range 200 {
		testdb.CreateFamily(t, pool, testdb.Family{})
	}
	clk := clock.NewFake(ist(10, 0))
	var wg sync.WaitGroup
	for range 4 {
		s := newScheduler(pool, clk)
		wg.Go(func() {
			if _, err := s.Tick(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var slots, calls, jobs int
	_ = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM call_slots), (SELECT count(*) FROM calls), (SELECT count(*) FROM jobs)`).Scan(&slots, &calls, &jobs)
	if slots != 200 || calls != 200 || jobs != 200 {
		t.Fatalf("slots %d calls %d jobs %d, want 200 each", slots, calls, jobs)
	}
}
