-- name: InsertCallReport :execrows
INSERT INTO call_reports (call_id, schema_version, report_enc, red_flag_count, confidence, needs_review, model, created_at)
VALUES (@call_id, @schema_version, @report_enc, @red_flag_count, @confidence, @needs_review, @model, @created_at)
ON CONFLICT (call_id) DO NOTHING;

-- name: GetCallReport :one
SELECT * FROM call_reports WHERE call_id = $1;

-- name: CallReportExists :one
SELECT EXISTS (SELECT 1 FROM call_reports WHERE call_id = $1);

-- name: ListRecentReports :many
-- Earlier valid reports for a parent, newest first, for multi-call rules.
SELECT r.* FROM call_reports r
JOIN calls c ON c.id = r.call_id
WHERE c.parent_id = @parent_id AND r.call_id <> @exclude_call_id AND r.schema_version = '1'
  AND r.created_at <= @before
ORDER BY r.created_at DESC
LIMIT @max_items;

-- name: DeleteTranscript :execrows
DELETE FROM transcripts WHERE call_id = $1;

-- name: ListOptedInMembers :many
SELECT * FROM family_members
WHERE account_id = @account_id AND whatsapp_opt_in_at IS NOT NULL
ORDER BY priority;
