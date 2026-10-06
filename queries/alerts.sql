-- name: UpsertCallAlert :one
-- One alert per (call_id, category): mid-call tool alerts and post-call
-- findings merge into the same row. An emergency is never downgraded.
INSERT INTO alerts (parent_id, call_id, type, category, source, detail_enc)
VALUES (@parent_id, @call_id, @type, @category, @source, @detail_enc)
ON CONFLICT (call_id, category) WHERE call_id IS NOT NULL DO UPDATE
SET type = CASE
      WHEN alerts.type = 'emergency' OR EXCLUDED.type = 'emergency' THEN 'emergency'
      WHEN alerts.type = 'urgent' OR EXCLUDED.type = 'urgent' THEN 'urgent'
      ELSE EXCLUDED.type END,
    detail_enc = COALESCE(alerts.detail_enc, EXCLUDED.detail_enc)
RETURNING id, type, (xmax = 0)::boolean AS inserted;

-- name: InsertSlotAlert :one
-- missed_calls alerts, one per slot. Returns no row for a duplicate.
INSERT INTO alerts (parent_id, slot_id, type, category, source)
VALUES (@parent_id, @slot_id, @type, @category, 'rule')
ON CONFLICT (slot_id, category) WHERE slot_id IS NOT NULL DO NOTHING
RETURNING id;

-- name: InsertParentAlert :one
-- Alerts tied to neither a call nor a slot (for example, watch trends).
INSERT INTO alerts (parent_id, type, category, source, detail_enc)
VALUES (@parent_id, @type, @category, @source, @detail_enc)
RETURNING id;

-- name: GetAlert :one
SELECT * FROM alerts WHERE id = $1;

-- name: ListAlertsForCall :many
SELECT * FROM alerts WHERE call_id = $1 ORDER BY created_at, id;

-- name: ListAlertsForParent :many
SELECT * FROM alerts WHERE parent_id = $1 ORDER BY created_at, id;
