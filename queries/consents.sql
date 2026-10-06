-- name: RecordConsent :one
INSERT INTO consents (parent_id, kind, given_by, method, text_version, evidence_ref, given_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: WithdrawConsent :execrows
UPDATE consents SET withdrawn_at = $3
WHERE parent_id = $1 AND kind = $2 AND withdrawn_at IS NULL;

-- name: ListValidConsentKinds :many
SELECT DISTINCT kind FROM consents
WHERE parent_id = $1 AND withdrawn_at IS NULL
ORDER BY kind;
