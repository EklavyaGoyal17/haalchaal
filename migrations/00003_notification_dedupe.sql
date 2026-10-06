-- +goose Up
-- One notification row per logical send, so a retried job never sends the
-- same message twice once it has gone out.
ALTER TABLE notifications ADD COLUMN dedupe_key TEXT;
ALTER TABLE notifications ADD COLUMN sent_at TIMESTAMPTZ;
CREATE UNIQUE INDEX notifications_dedupe ON notifications (dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX notifications_provider_message ON notifications (provider_message_id) WHERE provider_message_id IS NOT NULL;
CREATE INDEX notifications_call ON notifications (call_id) WHERE call_id IS NOT NULL;
CREATE INDEX inbound_messages_unhandled ON inbound_messages (received_at) WHERE handled_at IS NULL;

-- +goose Down
DROP INDEX inbound_messages_unhandled;
DROP INDEX notifications_call;
DROP INDEX notifications_provider_message;
DROP INDEX notifications_dedupe;
ALTER TABLE notifications DROP COLUMN sent_at;
ALTER TABLE notifications DROP COLUMN dedupe_key;
