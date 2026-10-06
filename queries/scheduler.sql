-- name: ListSchedulableParents :many
-- Parents the scheduler may create slots for: active parent, callable account,
-- and the three consents required before any call (SPEC §13), none withdrawn.
SELECT p.id, p.phone_e164, p.timezone, p.call_time_local, p.window_start, p.window_end, a.plan
FROM parents p
JOIN accounts a ON a.id = p.account_id
WHERE p.status = 'active'
  AND a.status IN ('trial', 'active')
  AND (
    SELECT count(DISTINCT c.kind) FROM consents c
    WHERE c.parent_id = p.id
      AND c.withdrawn_at IS NULL
      AND c.given_at <= @now
      AND c.kind IN ('calls', 'data_processing', 'share_with_family')
  ) = 3
ORDER BY p.id;

-- name: InsertSlot :one
-- Returns no row when today's slot already exists.
INSERT INTO call_slots (parent_id, local_date)
VALUES (@parent_id, @local_date)
ON CONFLICT (parent_id, local_date) DO NOTHING
RETURNING id;

-- name: SlotExists :one
SELECT EXISTS (SELECT 1 FROM call_slots WHERE parent_id = @parent_id AND local_date = @local_date);

-- name: InsertCallAttempt :one
INSERT INTO calls (slot_id, parent_id, attempt_no, scheduled_for, provider)
VALUES (@slot_id, @parent_id, @attempt_no, @scheduled_for, @provider)
RETURNING *;

-- name: GetCallGuardState :one
-- Everything place_call needs to re-check its guards in one read.
SELECT c.id AS call_id, c.status AS call_status, c.attempt_no, c.slot_id,
       p.id AS parent_id, p.status AS parent_status, p.phone_e164, p.timezone,
       p.call_time_local, p.window_start, p.window_end,
       a.status AS account_status, a.plan,
       ARRAY(
         SELECT DISTINCT cs.kind FROM consents cs
         WHERE cs.parent_id = p.id AND cs.withdrawn_at IS NULL AND cs.given_at <= @now
         ORDER BY cs.kind
       )::text[] AS consent_kinds
FROM calls c
JOIN parents p ON p.id = c.parent_id
JOIN accounts a ON a.id = p.account_id
WHERE c.id = @call_id;
