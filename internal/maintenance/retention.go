// Package maintenance holds the daily retention job and key rotation
// (SPEC §13).
package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// RecordingDeleter is implemented by voice providers that keep recordings.
type RecordingDeleter interface {
	DeleteRecording(ctx context.Context, providerCallID string) error
}

// Retention runs the daily retention job.
type Retention struct {
	Pool          *pgxpool.Pool
	Clock         clock.Clock
	Log           *slog.Logger
	Voice         voice.Provider
	TranscriptTTL time.Duration // RETENTION_TRANSCRIPT_DAYS; also for family messages
	AuditTTL      time.Duration // RETENTION_AUDIT_DAYS
	Location      *time.Location
}

// Housekeeping windows for rows that hold IDs only.
const (
	webhookEventTTL = 30 * 24 * time.Hour
	finishedJobTTL  = 30 * 24 * time.Hour
	runHour         = 3 // local time of the daily run
)

// RetentionSpec is the retention job for a local date.
func RetentionSpec(date string, runAt time.Time) jobs.Spec {
	return jobs.Spec{Kind: jobs.KindRetention, DedupeKey: "retention:" + date, Payload: jobs.Payload{Date: date}, RunAt: runAt}
}

// Schedule enqueues today's retention job (03:00 local, or now if later).
// Called every scheduler tick; the dedupe key makes it once a day.
func (r *Retention) Schedule(ctx context.Context) error {
	now := r.Clock.Now()
	local := now.In(r.Location)
	at := time.Date(local.Year(), local.Month(), local.Day(), runHour, 0, 0, 0, r.Location)
	if at.Before(now) {
		at = now
	}
	_, _, err := jobs.Enqueue(ctx, r.Pool, RetentionSpec(local.Format(time.DateOnly), at))
	return err
}

// Run is the retention job handler. Each step is idempotent.
func (r *Retention) Run(ctx context.Context, _ jobs.Job) error {
	now := r.Clock.Now()
	q := db.New(r.Pool)
	expired, err := q.ListExpiredTranscriptCalls(ctx, now)
	if err != nil {
		return fmt.Errorf("list expired transcripts: %w", err)
	}
	recordings := 0
	if del, ok := r.Voice.(RecordingDeleter); ok {
		for _, c := range expired {
			if c.ProviderCallID == nil || c.Provider != r.Voice.Name() {
				continue
			}
			// Fail the job (and retry) rather than lose track of a recording.
			if err := del.DeleteRecording(ctx, *c.ProviderCallID); err != nil {
				return fmt.Errorf("delete recording for call %s: %w", c.ID, err)
			}
			recordings++
		}
	}
	transcripts, err := q.DeleteExpiredTranscripts(ctx, now)
	if err != nil {
		return fmt.Errorf("delete transcripts: %w", err)
	}
	memories, err := q.DeleteExpiredMemories(ctx, &now)
	if err != nil {
		return fmt.Errorf("delete memories: %w", err)
	}
	inbound, err := q.DeleteOldInbound(ctx, now.Add(-r.TranscriptTTL))
	if err != nil {
		return fmt.Errorf("delete inbound: %w", err)
	}
	events, err := q.DeleteOldWebhookEvents(ctx, now.Add(-webhookEventTTL))
	if err != nil {
		return fmt.Errorf("delete webhook events: %w", err)
	}
	jobsN, err := q.DeleteFinishedJobsBefore(ctx, now.Add(-finishedJobTTL))
	if err != nil {
		return fmt.Errorf("delete jobs: %w", err)
	}
	auditN, err := q.DeleteOldAudit(ctx, now.Add(-r.AuditTTL))
	if err != nil {
		return fmt.Errorf("delete audit: %w", err)
	}
	d := audit.Details{
		"transcripts": fmt.Sprint(transcripts), "recordings": fmt.Sprint(recordings), "memories": fmt.Sprint(memories),
		"inbound": fmt.Sprint(inbound), "webhook_events": fmt.Sprint(events), "jobs": fmt.Sprint(jobsN), "audit": fmt.Sprint(auditN),
	}
	if err := audit.Write(ctx, r.Pool, audit.ActorSystem, "retention", "system", "retention", d); err != nil {
		return err
	}
	r.Log.Info("retention done", "transcripts", transcripts, "recordings", recordings, "memories", memories,
		"inbound", inbound, "webhook_events", events, "jobs", jobsN, "audit", auditN)
	return nil
}
