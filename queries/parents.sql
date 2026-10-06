-- name: CreateParent :one
INSERT INTO parents (
  account_id, preferred_name, phone_e164, language, timezone, call_time_local,
  window_start, window_end, interests_enc, safe_word_enc,
  local_contact_name, local_contact_phone_e164
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetParent :one
SELECT * FROM parents WHERE id = $1;

-- name: SetParentStatus :exec
UPDATE parents SET status = $2 WHERE id = $1;

-- name: CreateMedicine :one
INSERT INTO medicines (parent_id, name_enc, timing)
VALUES ($1, $2, $3)
RETURNING *;

-- name: ListActiveMedicines :many
SELECT * FROM medicines WHERE parent_id = $1 AND active ORDER BY timing, id;
