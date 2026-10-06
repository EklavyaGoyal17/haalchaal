-- +goose Up
-- The reaper scans running jobs by lock age; cancellation looks jobs up by
-- parent. Both stay small partial indexes.
CREATE INDEX jobs_running_locked ON jobs (locked_at) WHERE status = 'running';
CREATE INDEX jobs_queued_parent ON jobs ((payload->>'parent_id')) WHERE status = 'queued';

-- +goose Down
DROP INDEX jobs_queued_parent;
DROP INDEX jobs_running_locked;
