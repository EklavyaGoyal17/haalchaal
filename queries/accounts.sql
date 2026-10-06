-- name: CreateAccount :one
INSERT INTO accounts (plan, billing_note)
VALUES ($1, $2)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- name: CreateFamilyMember :one
INSERT INTO family_members (account_id, name, phone_e164, relation, language, priority, whatsapp_opt_in_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListFamilyMembers :many
SELECT * FROM family_members WHERE account_id = $1 ORDER BY priority;
