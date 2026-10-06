//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
)

var t0 = time.Date(2026, 10, 6, 5, 0, 0, 0, time.UTC)

func newWorker(pool db.DBTX, clk clock.Clock) *jobs.Worker {
	return &jobs.Worker{DB: pool, Clock: clk, Log: testdb.Log(), Name: "test"}
}

func TestTwoWorkersNeverClaimTheSameJob(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	clk := clock.NewFake(t0)
	const total = 300
	for i := range total {
		if _, ok, err := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "noop", DedupeKey: fmt.Sprintf("noop:%d", i), RunAt: t0}); err != nil || !ok {
			t.Fatalf("enqueue %d: ok=%v err=%v", i, ok, err)
		}
	}

	var mu sync.Mutex
	seen := map[int64]string{}
	var dupes atomic.Int32
	record := func(name string) jobs.Handler {
		return func(ctx context.Context, j jobs.Job) error {
			mu.Lock()
			defer mu.Unlock()
			if prev, ok := seen[j.ID]; ok {
				dupes.Add(1)
				t.Errorf("job %d claimed by %s and %s", j.ID, prev, name)
			}
			seen[j.ID] = name
			return nil
		}
	}

	// Two independent workers (as if two processes), 8 claim loops each.
	var wg sync.WaitGroup
	for w := range 2 {
		worker := newWorker(pool, clk)
		worker.Handle("noop", record(fmt.Sprintf("worker%d", w)))
		for g := range 8 {
			id := fmt.Sprintf("w%d-%d", w, g)
			wg.Go(func() {
				for {
					ran, err := worker.RunOnce(ctx, id)
					if err != nil {
						t.Error(err)
						return
					}
					if !ran {
						return
					}
				}
			})
		}
	}
	wg.Wait()

	if len(seen) != total || dupes.Load() != 0 {
		t.Fatalf("ran %d distinct jobs with %d duplicates, want %d and 0", len(seen), dupes.Load(), total)
	}
	var done int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status = 'done' AND attempts = 1`).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if done != total {
		t.Fatalf("%d jobs done with one attempt, want %d", done, total)
	}
}

func TestDedupeKey(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	id1, ok1, err := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "noop", DedupeKey: "k", RunAt: t0})
	if err != nil || !ok1 || id1 == 0 {
		t.Fatalf("first enqueue: %d %v %v", id1, ok1, err)
	}
	_, ok2, err := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "noop", DedupeKey: "k", RunAt: t0})
	if err != nil || ok2 {
		t.Fatalf("duplicate enqueue: ok=%v err=%v", ok2, err)
	}
	// No dedupe key: always inserted.
	for range 2 {
		if _, ok, err := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "noop", RunAt: t0}); err != nil || !ok {
			t.Fatalf("keyless enqueue: ok=%v err=%v", ok, err)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&n)
	if n != 3 {
		t.Fatalf("%d jobs, want 3", n)
	}
}

func TestRunAtAndUnhandledKinds(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	clk := clock.NewFake(t0)
	w := newWorker(pool, clk)
	var ran atomic.Int32
	w.Handle("noop", func(context.Context, jobs.Job) error { ran.Add(1); return nil })

	_, _, _ = jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "noop", RunAt: t0.Add(time.Minute)})
	_, _, _ = jobs.Enqueue(ctx, pool, jobs.Spec{Kind: jobs.KindPlaceCall, RunAt: t0})

	if got, _ := w.RunOnce(ctx, "a"); got {
		t.Fatal("claimed a future job or an unhandled kind")
	}
	clk.Advance(time.Minute)
	if got, _ := w.RunOnce(ctx, "a"); !got || ran.Load() != 1 {
		t.Fatal("due job not run")
	}
	var status string
	_ = pool.QueryRow(ctx, `SELECT status FROM jobs WHERE kind = 'place_call'`).Scan(&status)
	if status != "queued" {
		t.Fatalf("unhandled kind status %q, want queued", status)
	}
}

func TestRetryBackoffThenFail(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	clk := clock.NewFake(t0)
	w := newWorker(pool, clk)
	var dead []int64
	w.OnDeadLetter = func(_ context.Context, kind string, id int64, _ jobs.Payload) { dead = append(dead, id) }
	w.Handle("flaky", func(context.Context, jobs.Job) error { return errors.New("vendor down") })

	id, _, _ := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "flaky", RunAt: t0, MaxAttempts: 3})
	q := db.New(pool)

	for attempt := int32(1); attempt <= 3; attempt++ {
		ran, err := w.RunOnce(ctx, "a")
		if err != nil || !ran {
			t.Fatalf("attempt %d: ran=%v err=%v", attempt, ran, err)
		}
		j, _ := q.GetJob(ctx, id)
		if j.Attempts != attempt {
			t.Fatalf("attempts = %d, want %d", j.Attempts, attempt)
		}
		if attempt < 3 {
			if j.Status != "queued" || !j.RunAt.Equal(clk.Now().Add(jobs.Backoff(attempt))) {
				t.Fatalf("attempt %d: status %s run_at %v", attempt, j.Status, j.RunAt)
			}
			if j.LastError == nil || *j.LastError != "vendor down" {
				t.Fatalf("last_error = %v", j.LastError)
			}
			// Not claimable before the backoff elapses.
			if ran, _ := w.RunOnce(ctx, "a"); ran {
				t.Fatal("claimed before backoff")
			}
			clk.Advance(jobs.Backoff(attempt))
		} else if j.Status != "failed" {
			t.Fatalf("final status %s, want failed", j.Status)
		}
	}
	if len(dead) != 1 || dead[0] != id {
		t.Fatalf("dead letters = %v", dead)
	}
}

func TestPermanentAndPanicAndUnknownKind(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	clk := clock.NewFake(t0)
	w := newWorker(pool, clk)
	w.Handle("perm", func(context.Context, jobs.Job) error { return jobs.Permanent(errors.New("bad")) })
	w.Handle("boom", func(context.Context, jobs.Job) error { panic("kaboom") })

	permID, _, _ := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "perm", RunAt: t0})
	boomID, _, _ := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "boom", RunAt: t0})
	for range 2 {
		if _, err := w.RunOnce(ctx, "a"); err != nil {
			t.Fatal(err)
		}
	}
	q := db.New(pool)
	if j, _ := q.GetJob(ctx, permID); j.Status != "failed" || j.Attempts != 1 {
		t.Fatalf("permanent: %s after %d", j.Status, j.Attempts)
	}
	if j, _ := q.GetJob(ctx, boomID); j.Status != "queued" || j.LastError == nil {
		t.Fatalf("panic: status %s", j.Status)
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	clk := clock.NewFake(t0)
	w := newWorker(pool, clk)
	parent, call := uuid.New(), uuid.New()
	var got jobs.Payload
	w.Handle("p", func(_ context.Context, j jobs.Job) error { got = j.Payload; return nil })
	_, _, _ = jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "p", RunAt: t0, Payload: jobs.Payload{ParentID: &parent, CallID: &call}})
	if _, err := w.RunOnce(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got.ParentID == nil || *got.ParentID != parent || *got.CallID != call {
		t.Fatalf("payload = %+v", got)
	}
}

func TestReaper(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	clk := clock.NewFake(t0)
	w := newWorker(pool, clk)
	var dead int
	w.OnDeadLetter = func(context.Context, string, int64, jobs.Payload) { dead++ }

	stale, _, _ := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "x", RunAt: t0})
	fresh, _, _ := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "x", RunAt: t0})
	exhausted, _, _ := jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "x", RunAt: t0, MaxAttempts: 1})
	set := func(id int64, age time.Duration, attempts int) {
		if _, err := pool.Exec(ctx, `UPDATE jobs SET status='running', locked_by='dead', locked_at=$2, attempts=$3 WHERE id=$1`,
			id, t0.Add(-age), attempts); err != nil {
			t.Fatal(err)
		}
	}
	set(stale, 6*time.Minute, 1)
	set(fresh, 4*time.Minute, 1)
	set(exhausted, 10*time.Minute, 1)

	n, err := w.Reap(ctx)
	if err != nil || n != 2 {
		t.Fatalf("reaped %d, err %v; want 2", n, err)
	}
	q := db.New(pool)
	for id, want := range map[int64]string{stale: "queued", fresh: "running", exhausted: "failed"} {
		if j, _ := q.GetJob(ctx, id); j.Status != want {
			t.Errorf("job %d status %s, want %s", id, j.Status, want)
		}
	}
	if dead != 1 {
		t.Errorf("dead letters = %d, want 1", dead)
	}

	// The worker whose lock was reaped cannot complete the job afterwards.
	c, err := q.CompleteJob(ctx, db.CompleteJobParams{ID: stale, WorkerID: "dead", Attempts: 1})
	if err != nil || c != 0 {
		t.Fatalf("stale worker completed reaped job: rows=%d err=%v", c, err)
	}
}

func TestCancelQueuedCallJobsForParent(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	for _, s := range []jobs.Spec{
		{Kind: jobs.KindPlaceCall, Payload: jobs.Payload{ParentID: &a}},
		{Kind: jobs.KindPlaceCall, Payload: jobs.Payload{ParentID: &a}},
		{Kind: jobs.KindEscalateAlert, Payload: jobs.Payload{ParentID: &a}}, // safety alerts survive a stop
		{Kind: jobs.KindProcessCall, Payload: jobs.Payload{ParentID: &a}},
		{Kind: jobs.KindPlaceCall, Payload: jobs.Payload{ParentID: &b}},
	} {
		s.RunAt = t0
		if _, _, err := jobs.Enqueue(ctx, pool, s); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.New(pool).CancelQueuedCallJobsForParent(ctx, db.CancelQueuedCallJobsForParentParams{Reason: "consent_withdrawn", ParentID: a.String()})
	if err != nil || n != 2 {
		t.Fatalf("cancelled %d, err %v", n, err)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	pool := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	w := newWorker(pool, clock.Real{})
	w.PollInterval = 10 * time.Millisecond
	var ran atomic.Int32
	w.Handle("noop", func(context.Context, jobs.Job) error { ran.Add(1); return nil })
	for range 20 {
		_, _, _ = jobs.Enqueue(ctx, pool, jobs.Spec{Kind: "noop", RunAt: time.Now().Add(-time.Second)})
	}
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()
	deadline := time.After(10 * time.Second)
	for ran.Load() < 20 {
		select {
		case <-deadline:
			t.Fatalf("only %d jobs ran", ran.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
