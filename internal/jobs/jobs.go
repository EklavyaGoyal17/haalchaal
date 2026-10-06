// Package jobs is the Postgres-backed job queue (SPEC §5): enqueue with dedupe
// keys, claim with FOR UPDATE SKIP LOCKED, retry with exponential backoff, and
// a reaper for jobs whose worker died.
//
// Payloads hold IDs only, never personal data. Every handler must be
// idempotent: a job can run more than once (a reaped lock, a crash after the
// work but before Complete).
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/EklavyaGoyal17/haalchaal/internal/db"
)

// Job kinds (SPEC §5).
const (
	KindPlaceCall     = "place_call"
	KindProcessCall   = "process_call"
	KindSendSummary   = "send_summary"
	KindSendAlert     = "send_alert"
	KindEscalateAlert = "escalate_alert"
	KindRetention     = "retention"
)

const (
	// DefaultMaxAttempts matches the jobs.max_attempts column default.
	DefaultMaxAttempts = 5
	// LockTimeout is how long a running job may hold its lock before the
	// reaper requeues it. Handlers run with a shorter deadline.
	LockTimeout = 5 * time.Minute

	backoffBase = 30 * time.Second
	backoffCap  = 30 * time.Minute
	maxErrorLen = 500
)

// Backoff is the delay before retrying a job that has been attempted
// `attempts` times: 30s * 2^attempts, capped at 30 minutes.
func Backoff(attempts int32) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts >= 6 { // 30s * 2^6 = 32m, already past the cap
		return backoffCap
	}
	d := backoffBase << attempts
	if d > backoffCap {
		return backoffCap
	}
	return d
}

// Payload is the only shape a job payload may take: IDs, never personal data.
// Fields are optional; parent_id lets queued jobs be cancelled per parent.
type Payload struct {
	ParentID       *uuid.UUID `json:"parent_id,omitempty"`
	CallID         *uuid.UUID `json:"call_id,omitempty"`
	SlotID         *uuid.UUID `json:"slot_id,omitempty"`
	AlertID        *uuid.UUID `json:"alert_id,omitempty"`
	FamilyMemberID *uuid.UUID `json:"family_member_id,omitempty"`
	Step           *int       `json:"step,omitempty"`
	Recipient      string     `json:"recipient,omitempty"` // a recipient kind or ID, never a phone number
	Date           string     `json:"date,omitempty"`      // YYYY-MM-DD
}

// Spec describes a job to enqueue.
type Spec struct {
	Kind        string
	DedupeKey   string // empty means no dedupe
	Payload     Payload
	RunAt       time.Time
	MaxAttempts int32 // 0 means DefaultMaxAttempts
}

// Enqueue inserts a job using dbtx, which may be a transaction so the job is
// created atomically with the rows it refers to. It returns the job id and
// false when a job with the same dedupe key already exists.
func Enqueue(ctx context.Context, dbtx db.DBTX, s Spec) (int64, bool, error) {
	if s.Kind == "" {
		return 0, false, errors.New("enqueue: kind is required")
	}
	if s.RunAt.IsZero() {
		return 0, false, errors.New("enqueue: run_at is required")
	}
	payload, err := json.Marshal(s.Payload)
	if err != nil {
		return 0, false, fmt.Errorf("enqueue %s: marshal payload: %w", s.Kind, err)
	}
	maxAttempts := s.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	var key *string
	if s.DedupeKey != "" {
		key = &s.DedupeKey
	}
	id, err := db.New(dbtx).EnqueueJob(ctx, db.EnqueueJobParams{
		Kind:        s.Kind,
		DedupeKey:   key,
		Payload:     payload,
		RunAt:       s.RunAt.UTC(),
		MaxAttempts: maxAttempts,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("enqueue %s: %w", s.Kind, err)
	}
	return id, true, nil
}

// Job is a claimed job handed to a Handler.
type Job struct {
	ID          int64
	Kind        string
	Payload     Payload
	Attempts    int32 // including this one
	MaxAttempts int32
}

// LastAttempt reports whether a failure now would fail the job for good.
func (j Job) LastAttempt() bool { return j.Attempts >= j.MaxAttempts }

// permanentError marks a failure that retrying cannot fix.
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent wraps err so the job fails at once instead of retrying.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was wrapped with Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

func truncateError(err error) string {
	s := err.Error()
	if len(s) > maxErrorLen {
		s = s[:maxErrorLen]
	}
	return s
}

func toJob(r db.Job) (Job, error) {
	j := Job{ID: r.ID, Kind: r.Kind, Attempts: r.Attempts, MaxAttempts: r.MaxAttempts}
	if err := json.Unmarshal(r.Payload, &j.Payload); err != nil {
		return j, fmt.Errorf("job %d: decode payload: %w", r.ID, err)
	}
	return j, nil
}
