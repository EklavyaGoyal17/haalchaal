package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
)

// Handler runs one job. Returning nil completes it; an error retries it with
// backoff (or fails it at once when wrapped with Permanent).
type Handler func(ctx context.Context, j Job) error

// DeadLetterFunc is called when a job fails for good.
type DeadLetterFunc func(ctx context.Context, kind string, id int64, payload Payload)

// Worker claims and runs jobs. Several Workers, in one process or many, can
// share a database: SKIP LOCKED gives each job to exactly one of them.
type Worker struct {
	DB           db.DBTX
	Clock        clock.Clock
	Log          *slog.Logger
	Name         string        // identifies this process in locked_by; defaults to host-pid
	Concurrency  int           // claim loops; default 4
	PollInterval time.Duration // idle wait between empty claims; default 1s
	JobTimeout   time.Duration // per-job deadline; default 2m, must stay under LockTimeout
	OnDeadLetter DeadLetterFunc

	mu       sync.RWMutex
	handlers map[string]Handler
}

// Handle registers the handler for a job kind. Jobs of kinds with no handler
// stay queued, so a worker never claims work it cannot do.
func (w *Worker) Handle(kind string, h Handler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.handlers == nil {
		w.handlers = map[string]Handler{}
	}
	w.handlers[kind] = h
}

func (w *Worker) kinds() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	ks := make([]string, 0, len(w.handlers))
	for k := range w.handlers {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (w *Worker) handler(kind string) (Handler, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	h, ok := w.handlers[kind]
	return h, ok
}

func (w *Worker) name() string {
	if w.Name != "" {
		return w.Name
	}
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

func (w *Worker) jobTimeout() time.Duration {
	if w.JobTimeout > 0 && w.JobTimeout < LockTimeout {
		return w.JobTimeout
	}
	return 2 * time.Minute
}

// Run starts the claim loops and the reaper and blocks until ctx is done.
// In-flight jobs finish (within their deadline) before Run returns.
func (w *Worker) Run(ctx context.Context) error {
	n := w.Concurrency
	if n <= 0 {
		n = 4
	}
	poll := w.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	base := w.name()
	w.Log.Info("worker started", "name", base, "concurrency", n, "kinds", w.kinds())

	var wg sync.WaitGroup
	for i := range n {
		id := fmt.Sprintf("%s-%d", base, i)
		wg.Go(func() { w.loop(ctx, id, poll) })
	}
	wg.Go(func() { w.reapLoop(ctx) })
	wg.Wait()
	w.Log.Info("worker stopped", "name", base)
	return nil
}

func (w *Worker) loop(ctx context.Context, id string, poll time.Duration) {
	for ctx.Err() == nil {
		ran, err := w.RunOnce(ctx, id)
		if err != nil && ctx.Err() == nil {
			w.Log.Error("job loop", "worker", id, "error", err)
		}
		if ran && err == nil {
			continue // drain the queue without waiting
		}
		// Jitter spreads idle polls from many workers.
		wait := poll + time.Duration(rand.Int64N(int64(poll/2)+1))
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

func (w *Worker) reapLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if _, err := w.Reap(ctx); err != nil && ctx.Err() == nil {
			w.Log.Error("reaper", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce claims and runs at most one job as workerID. It reports whether a
// job was claimed.
func (w *Worker) RunOnce(ctx context.Context, workerID string) (bool, error) {
	kinds := w.kinds()
	if len(kinds) == 0 {
		return false, nil
	}
	q := db.New(w.DB)
	row, err := q.ClaimJob(ctx, db.ClaimJobParams{WorkerID: workerID, Now: w.Clock.Now(), Kinds: kinds})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim: %w", err)
	}

	// Finishing uses a context that survives shutdown, so a job that ran is
	// always marked.
	finishCtx := context.WithoutCancel(ctx)
	log := w.Log.With("job_id", row.ID, "kind", row.Kind, "attempt", row.Attempts)

	j, decodeErr := toJob(row)
	runErr := decodeErr
	if decodeErr != nil {
		runErr = Permanent(decodeErr)
	} else if h, ok := w.handler(row.Kind); !ok {
		runErr = Permanent(fmt.Errorf("no handler for kind %q", row.Kind))
	} else {
		runErr = w.call(ctx, h, j)
	}

	if runErr == nil {
		n, err := q.CompleteJob(finishCtx, db.CompleteJobParams{ID: row.ID, WorkerID: workerID, Attempts: row.Attempts})
		if err != nil {
			return true, fmt.Errorf("complete job %d: %w", row.ID, err)
		}
		if n == 0 {
			log.Warn("job lock lost before completion")
		}
		return true, nil
	}

	msg := truncateError(runErr)
	if IsPermanent(runErr) || row.Attempts >= row.MaxAttempts {
		n, err := q.FailJob(finishCtx, db.FailJobParams{ID: row.ID, WorkerID: workerID, Attempts: row.Attempts, LastError: &msg})
		if err != nil {
			return true, fmt.Errorf("fail job %d: %w", row.ID, err)
		}
		if n > 0 {
			log.Error("job failed", "error", msg)
			w.deadLetter(finishCtx, row.Kind, row.ID, j.Payload)
		}
		return true, nil
	}

	delay := Backoff(row.Attempts)
	_, err = q.RetryJob(finishCtx, db.RetryJobParams{
		ID: row.ID, WorkerID: workerID, Attempts: row.Attempts,
		RunAt: w.Clock.Now().Add(delay), LastError: &msg,
	})
	if err != nil {
		return true, fmt.Errorf("retry job %d: %w", row.ID, err)
	}
	log.Warn("job will retry", "error", msg, "delay", delay.String())
	return true, nil
}

// call runs h with a deadline and turns a panic into an error, so one bad job
// cannot take the worker down.
func (w *Worker) call(ctx context.Context, h Handler, j Job) (err error) {
	ctx, cancel := context.WithTimeout(ctx, w.jobTimeout())
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			w.Log.Error("job panicked", "job_id", j.ID, "kind", j.Kind, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return h(ctx, j)
}

// Reap requeues jobs whose lock is older than LockTimeout, and fails those
// with no attempts left. It returns how many rows it touched.
func (w *Worker) Reap(ctx context.Context) (int, error) {
	rows, err := db.New(w.DB).ReapStaleJobs(ctx, w.Clock.Now().Add(-LockTimeout))
	if err != nil {
		return 0, fmt.Errorf("reap: %w", err)
	}
	for _, r := range rows {
		if r.Status == "failed" {
			w.Log.Error("reaped job has no attempts left", "job_id", r.ID, "kind", r.Kind)
			j, _ := toJob(r)
			w.deadLetter(ctx, r.Kind, r.ID, j.Payload)
		} else {
			w.Log.Warn("reaped job requeued", "job_id", r.ID, "kind", r.Kind)
		}
	}
	return len(rows), nil
}

func (w *Worker) deadLetter(ctx context.Context, kind string, id int64, p Payload) {
	if w.OnDeadLetter != nil {
		w.OnDeadLetter(ctx, kind, id, p)
	}
}
