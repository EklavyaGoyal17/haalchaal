// Package audit writes audit_log rows (SPEC §13). Details must never hold
// health data, transcript text or full phone numbers; the writer accepts only
// a small typed set of detail values to make that hard to get wrong.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EklavyaGoyal17/haalchaal/internal/db"
)

// ActorSystem is the actor for automated actions.
const ActorSystem = "system"

// Admin returns the actor string for an admin.
func Admin(email string) string { return "admin:" + email }

// Details are short, non-sensitive key/value facts: ids, statuses, reasons.
type Details map[string]string

// Write inserts one audit row using dbtx (a pool or transaction).
func Write(ctx context.Context, dbtx db.DBTX, actor, action, entity, entityID string, d Details) error {
	var raw []byte
	if len(d) > 0 {
		var err error
		if raw, err = json.Marshal(d); err != nil {
			return fmt.Errorf("audit details: %w", err)
		}
	}
	if err := db.New(dbtx).InsertAuditLog(ctx, db.InsertAuditLogParams{
		Actor: actor, Action: action, Entity: entity, EntityID: entityID, Details: raw,
	}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}
