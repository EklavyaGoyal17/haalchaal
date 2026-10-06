-- name: ListActiveFollowUps :many
SELECT * FROM memories
WHERE parent_id = @parent_id AND kind = 'follow_up'
  AND (expires_at IS NULL OR expires_at > @now)
ORDER BY created_at DESC, id
LIMIT @max_items;

-- name: InsertMemory :one
INSERT INTO memories (parent_id, kind, content_enc, source_call_id, expires_at)
VALUES (@parent_id, @kind, @content_enc, @source_call_id, @expires_at)
RETURNING id;

-- name: GetParentWithAccount :one
SELECT p.*, a.plan, a.status AS account_status
FROM parents p JOIN accounts a ON a.id = p.account_id
WHERE p.id = $1;
