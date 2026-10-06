-- name: EnqueueJob :one
-- A duplicate dedupe_key inserts nothing and returns no row.
INSERT INTO jobs (kind, dedupe_key, payload, run_at, max_attempts)
VALUES (@kind, @dedupe_key, @payload, @run_at, @max_attempts)
ON CONFLICT (dedupe_key) DO NOTHING
RETURNING id;

-- name: ClaimJob :one
-- SPEC §5 claim, limited to the kinds this worker handles. now comes from the
-- injectable clock rather than SQL now().
UPDATE jobs
SET status = 'running', locked_by = @worker_id::text, locked_at = @now::timestamptz, attempts = attempts + 1
WHERE id = (
  SELECT j.id FROM jobs j
  WHERE j.status = 'queued' AND j.run_at <= @now::timestamptz AND j.kind = ANY(@kinds::text[])
  ORDER BY j.run_at, j.id
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
RETURNING *;

-- name: CompleteJob :execrows
-- locked_by and attempts fence out a worker whose lock was reaped.
UPDATE jobs
SET status = 'done', locked_by = NULL, locked_at = NULL, last_error = NULL
WHERE id = @id AND status = 'running' AND locked_by = @worker_id::text AND attempts = @attempts;

-- name: RetryJob :execrows
UPDATE jobs
SET status = 'queued', locked_by = NULL, locked_at = NULL, run_at = @run_at, last_error = @last_error
WHERE id = @id AND status = 'running' AND locked_by = @worker_id::text AND attempts = @attempts;

-- name: FailJob :execrows
UPDATE jobs
SET status = 'failed', locked_by = NULL, locked_at = NULL, last_error = @last_error
WHERE id = @id AND status = 'running' AND locked_by = @worker_id::text AND attempts = @attempts;

-- name: ReapStaleJobs :many
-- Requeue running jobs whose lock expired. A job that has used every attempt
-- fails instead, so a job that crashes its worker cannot loop forever.
UPDATE jobs
SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
    locked_by = NULL, locked_at = NULL, last_error = 'lock expired'
WHERE status = 'running' AND locked_at < @cutoff::timestamptz
RETURNING *;

-- name: CancelQueuedCallJobsForParent :execrows
-- Used when consent is withdrawn or a stop is requested. Only place_call is
-- cancelled: processing of finished calls and safety alerts must still run.
UPDATE jobs
SET status = 'failed', last_error = 'cancelled: ' || @reason::text
WHERE status = 'queued' AND kind = 'place_call' AND payload->>'parent_id' = @parent_id::text;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = $1;

-- name: CountJobsByStatus :many
SELECT kind, status, count(*)::bigint AS n FROM jobs GROUP BY kind, status ORDER BY kind, status;

-- name: DeleteFinishedJobsBefore :execrows
-- Retention for the queue itself; done and failed rows hold IDs only.
DELETE FROM jobs WHERE status IN ('done', 'failed') AND run_at < @cutoff;
