-- +goose Up
-- Reports that need review leave the queue once an admin has looked at them.
ALTER TABLE call_reports ADD COLUMN reviewed_by TEXT;
ALTER TABLE call_reports ADD COLUMN reviewed_at TIMESTAMPTZ;
CREATE INDEX call_reports_review ON call_reports (created_at) WHERE needs_review AND reviewed_at IS NULL;

-- +goose Down
DROP INDEX call_reports_review;
ALTER TABLE call_reports DROP COLUMN reviewed_at;
ALTER TABLE call_reports DROP COLUMN reviewed_by;
