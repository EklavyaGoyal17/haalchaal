-- +goose Up
-- Safety work (alerts, call processing) is claimed before routine work
-- (summaries) when both are due, so a backlog never delays an emergency.
ALTER TABLE jobs ADD COLUMN priority SMALLINT NOT NULL DEFAULT 100;
DROP INDEX jobs_ready;
CREATE INDEX jobs_ready ON jobs (priority, run_at, id) WHERE status = 'queued';

-- +goose Down
DROP INDEX jobs_ready;
CREATE INDEX jobs_ready ON jobs (run_at) WHERE status = 'queued';
ALTER TABLE jobs DROP COLUMN priority;
