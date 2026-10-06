-- name: InsertAuditLog :exec
-- details must never hold health data or full phone numbers.
INSERT INTO audit_log (actor, action, entity, entity_id, details)
VALUES ($1, $2, $3, $4, $5);
