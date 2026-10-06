-- name: ListExpiredTranscriptCalls :many
-- Calls whose transcripts are due for deletion, so recordings at the voice
-- platform can be deleted too.
SELECT c.id, c.provider, c.provider_call_id FROM transcripts t JOIN calls c ON c.id = t.call_id
WHERE t.delete_after <= @now;

-- name: DeleteExpiredTranscripts :execrows
DELETE FROM transcripts WHERE delete_after <= @now;

-- name: DeleteOldInbound :execrows
DELETE FROM inbound_messages WHERE received_at < @cutoff;

-- name: DeleteOldWebhookEvents :execrows
DELETE FROM webhook_events WHERE received_at < @cutoff;

-- name: DeleteOldAudit :execrows
DELETE FROM audit_log WHERE at < @cutoff;
