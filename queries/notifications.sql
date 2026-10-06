-- name: UpsertNotification :one
-- Creates the row for a logical send, or returns the existing one.
INSERT INTO notifications (alert_id, call_id, family_member_id, recipient_kind, channel, template, dedupe_key, created_at)
VALUES (@alert_id, @call_id, @family_member_id, @recipient_kind, 'whatsapp', @template, @dedupe_key, @created_at)
ON CONFLICT (dedupe_key) WHERE dedupe_key IS NOT NULL DO UPDATE SET dedupe_key = EXCLUDED.dedupe_key
RETURNING *;

-- name: MarkNotificationSent :exec
UPDATE notifications SET status = 'sent', provider_message_id = @provider_message_id, error = NULL, sent_at = @sent_at
WHERE id = @id;

-- name: MarkNotificationFailed :exec
UPDATE notifications SET status = 'failed', error = @error WHERE id = @id AND status IN ('queued', 'failed');

-- name: UpdateNotificationDelivery :execrows
-- Delivery statuses only move forward; failed can arrive at any point.
UPDATE notifications SET status = @status
WHERE provider_message_id = @provider_message_id
  AND (
    (@status = 'delivered' AND status IN ('queued', 'sent'))
    OR (@status = 'read' AND status IN ('queued', 'sent', 'delivered'))
    OR (@status = 'failed' AND status IN ('queued', 'sent'))
  );

-- name: ListNotificationsForParent :many
SELECT n.* FROM notifications n
LEFT JOIN alerts a ON a.id = n.alert_id
LEFT JOIN calls c ON c.id = n.call_id
WHERE a.parent_id = @parent_id OR c.parent_id = @parent_id
ORDER BY n.created_at, n.id;

-- name: MarkAlertNotified :exec
UPDATE alerts
SET status = CASE WHEN status = 'open' THEN 'notified' ELSE status END,
    escalation_step = GREATEST(escalation_step, @step)
WHERE id = @id;

-- name: AcknowledgeAlert :one
-- Only someone in the alert's family can acknowledge it.
UPDATE alerts a SET status = 'acknowledged', acknowledged_by = @member_id, acknowledged_at = @at
FROM parents p, family_members m
WHERE a.id = @alert_id AND p.id = a.parent_id AND m.id = @member_id AND m.account_id = p.account_id
  AND a.status IN ('open', 'notified')
RETURNING a.id, a.parent_id;

-- name: ListMembersByPhone :many
SELECT * FROM family_members WHERE phone_e164 = $1 ORDER BY created_at;

-- name: InsertInboundMessage :one
INSERT INTO inbound_messages (family_member_id, from_e164, kind, body_enc, handled_at, received_at)
VALUES (@family_member_id, @from_e164, @kind, @body_enc, @handled_at, @received_at)
RETURNING id;

-- name: ListWatchAlertsForCall :many
SELECT * FROM alerts WHERE call_id = @call_id AND type = 'watch' ORDER BY created_at, id;

-- name: GetMember :one
SELECT * FROM family_members WHERE id = $1;
