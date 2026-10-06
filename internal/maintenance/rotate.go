package maintenance

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
)

// EncryptedColumns lists every _enc column with the key used to page
// through its table. Each value's associated data is "table.column".
var EncryptedColumns = []struct{ Table, Column, Key string }{
	{"parents", "interests_enc", "id"},
	{"parents", "safe_word_enc", "id"},
	{"medicines", "name_enc", "id"},
	{"transcripts", "turns_enc", "call_id"},
	{"call_reports", "report_enc", "call_id"},
	{"memories", "content_enc", "id"},
	{"alerts", "detail_enc", "id"},
	{"alerts", "review_note_enc", "id"},
	{"inbound_messages", "body_enc", "id"},
}

// RotateKeys re-encrypts every value not written with the active key. Run
// it after adding a new key and making it active; when it reports zero
// remaining, the old key can be removed from ENCRYPTION_KEYS. Safe to stop
// and re-run.
func RotateKeys(ctx context.Context, pool *pgxpool.Pool, kr *crypto.Keyring, log *slog.Logger) (int, error) {
	total := 0
	for _, c := range EncryptedColumns {
		aad := c.Table + "." + c.Column
		// Identifiers come from the fixed list above, never from input.
		sel := fmt.Sprintf(`SELECT %[3]s, %[2]s FROM %[1]s WHERE %[2]s IS NOT NULL AND %[3]s > $1 ORDER BY %[3]s LIMIT 500`, c.Table, c.Column, c.Key)
		upd := fmt.Sprintf(`UPDATE %[1]s SET %[2]s = $2 WHERE %[3]s = $1 AND %[2]s = $3`, c.Table, c.Column, c.Key)
		after := "00000000-0000-0000-0000-000000000000"
		n := 0
		for {
			type row struct {
				id  string
				val []byte
			}
			var page []row
			rows, err := pool.Query(ctx, sel, after)
			if err != nil {
				return total, fmt.Errorf("%s: %w", aad, err)
			}
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.id, &r.val); err != nil {
					rows.Close()
					return total, err
				}
				page = append(page, r)
			}
			rows.Close()
			if len(page) == 0 {
				break
			}
			err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				for _, r := range page {
					if !kr.NeedsRotation(r.val) {
						continue
					}
					nv, err := kr.Rotate(r.val, aad)
					if err != nil {
						return fmt.Errorf("%s %s: %w", aad, r.id, err)
					}
					// Compare-and-set: a concurrent write wins and is already current.
					if _, err := tx.Exec(ctx, upd, r.id, nv, r.val); err != nil {
						return err
					}
					n++
				}
				return nil
			})
			if err != nil {
				return total, err
			}
			after = page[len(page)-1].id
		}
		if n > 0 {
			log.Info("rotated", "column", aad, "values", n)
		}
		total += n
	}
	err := audit.Write(ctx, pool, audit.ActorSystem, "rotate_keys", "system", "keys", audit.Details{
		"active_kid": fmt.Sprint(kr.ActiveKID()), "values": fmt.Sprint(total),
	})
	return total, err
}
