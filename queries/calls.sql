-- name: GetCall :one
SELECT * FROM calls WHERE id = $1;

-- name: GetCallByProviderID :one
SELECT * FROM calls WHERE provider = @provider AND provider_call_id = @provider_call_id;

-- name: GetCallForUpdate :one
SELECT * FROM calls WHERE id = $1 FOR UPDATE;

-- name: MarkCallDialing :execrows
-- Committed before StartCall, so a re-run of place_call never dials twice.
UPDATE calls SET status = 'dialing' WHERE id = @id AND status = 'scheduled';

-- name: SetProviderCallID :execrows
UPDATE calls SET provider_call_id = @provider_call_id
WHERE id = @id AND provider_call_id IS NULL;

-- name: TransitionCall :execrows
-- Compare-and-set on the current status; optional fields keep their value
-- when the argument is null.
UPDATE calls
SET status       = @to_status,
    started_at   = COALESCE(started_at, sqlc.narg(started_at)),
    ended_at     = COALESCE(sqlc.narg(ended_at), ended_at),
    duration_sec = COALESCE(sqlc.narg(duration_sec), duration_sec),
    cost_paise   = COALESCE(sqlc.narg(cost_paise), cost_paise),
    end_reason   = COALESCE(sqlc.narg(end_reason), end_reason)
WHERE id = @id AND status = @from_status;

-- name: SetCallUsage :exec
-- Duration and cost can arrive on a later event than the terminal one.
UPDATE calls
SET duration_sec = COALESCE(sqlc.narg(duration_sec), duration_sec),
    cost_paise   = COALESCE(sqlc.narg(cost_paise), cost_paise)
WHERE id = @id;

-- name: CancelOpenCallsForParent :execrows
-- Stop request or consent withdrawal: nothing scheduled may still dial.
UPDATE calls SET status = 'cancelled', end_reason = @end_reason, ended_at = @now
WHERE parent_id = @parent_id AND status = 'scheduled';

-- name: ListStaleCalls :many
-- Attempts stuck in a live state with no provider event for too long.
SELECT * FROM calls
WHERE status IN ('dialing', 'ringing', 'in_progress')
  AND COALESCE(started_at, scheduled_for) < @cutoff::timestamptz
ORDER BY scheduled_for
LIMIT 100;

-- name: ListCallsForSlot :many
SELECT * FROM calls WHERE slot_id = $1 ORDER BY attempt_no;

-- name: GetSlot :one
SELECT * FROM call_slots WHERE id = $1;

-- name: SetSlotStatus :execrows
UPDATE call_slots SET status = @status WHERE id = @id AND status = 'pending';

-- name: CancelPendingSlotsForParent :execrows
UPDATE call_slots SET status = 'cancelled' WHERE parent_id = @parent_id AND status = 'pending';

-- name: InsertWebhookEvent :one
-- Returns no row for a duplicate (provider, event_id).
INSERT INTO webhook_events (provider, event_id, received_at)
VALUES (@provider, @event_id, @received_at)
ON CONFLICT (provider, event_id) DO NOTHING
RETURNING event_id;

-- name: InsertTranscript :execrows
INSERT INTO transcripts (call_id, turns_enc, language, delete_after)
VALUES (@call_id, @turns_enc, @language, @delete_after)
ON CONFLICT (call_id) DO NOTHING;

-- name: GetTranscript :one
SELECT * FROM transcripts WHERE call_id = $1;

-- name: MarkFirstCallDone :exec
UPDATE parents SET first_call_done_at = @at WHERE id = @id AND first_call_done_at IS NULL;
