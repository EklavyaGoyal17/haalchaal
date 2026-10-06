//go:build integration

package migrate_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/migrate"
)

// setup connects to TEST_DATABASE_URL and returns a freshly migrated database.
// Tests in this file share one database, so they must not run in parallel.
func setup(t *testing.T) (*pgxpool.Pool, *migrate.Migrator) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m, err := migrate.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	return pool, m
}

func userTables(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrationsUpDownUp(t *testing.T) {
	pool, m := setup(t)
	ctx := context.Background()

	pending, err := m.HasPending(ctx)
	if err != nil || pending {
		t.Fatalf("after up: pending=%v err=%v", pending, err)
	}
	if n := userTables(t, pool); n != 16 {
		t.Fatalf("after up: %d tables, want 16", n)
	}

	if err := m.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if n := userTables(t, pool); n != 0 {
		t.Fatalf("after down: %d tables remain", n)
	}
	if pending, _ := m.HasPending(ctx); !pending {
		t.Fatal("after down: expected pending migrations")
	}

	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if n := userTables(t, pool); n != 16 {
		t.Fatalf("after second up: %d tables, want 16", n)
	}
}

func pgTime(h, m int) pgtype.Time {
	return pgtype.Time{Microseconds: int64(h*3600+m*60) * 1e6, Valid: true}
}

func wantViolation(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("err = %v, want SQLSTATE %s", err, code)
	}
}

const (
	checkViolation  = "23514"
	uniqueViolation = "23505"
)

func TestSchemaAndQueries(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	q := db.New(pool)

	kr, err := crypto.ParseKeyring("1:QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE=", "1")
	if err != nil {
		t.Fatal(err)
	}

	acct, err := q.CreateAccount(ctx, db.CreateAccountParams{Plan: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	if acct.Status != "trial" {
		t.Errorf("account status = %q, want trial", acct.Status)
	}

	now := time.Now().UTC()
	if _, err := q.CreateFamilyMember(ctx, db.CreateFamilyMemberParams{
		AccountID: acct.ID, Name: "Ravi", PhoneE164: "+919800000001", Language: "en", Priority: 1, WhatsappOptInAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("family E.164 check", func(t *testing.T) {
		_, err := q.CreateFamilyMember(ctx, db.CreateFamilyMemberParams{
			AccountID: acct.ID, Name: "Bad", PhoneE164: "09800000002", Language: "en", Priority: 2,
		})
		wantViolation(t, err, checkViolation)
	})
	t.Run("family priority range", func(t *testing.T) {
		_, err := q.CreateFamilyMember(ctx, db.CreateFamilyMemberParams{
			AccountID: acct.ID, Name: "Six", PhoneE164: "+919800000006", Language: "en", Priority: 6,
		})
		wantViolation(t, err, checkViolation)
	})

	safeWord, err := kr.EncryptString("gulab jamun", "parents.safe_word_enc")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := q.CreateParent(ctx, db.CreateParentParams{
		AccountID: acct.ID, PreferredName: "Kamla ji", PhoneE164: "+919800000010", Language: "hi",
		Timezone: "Asia/Kolkata", CallTimeLocal: pgTime(10, 0), WindowStart: pgTime(9, 0), WindowEnd: pgTime(20, 0),
		SafeWordEnc: safeWord,
	})
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != "onboarding" {
		t.Errorf("parent status = %q, want onboarding", parent.Status)
	}

	t.Run("encrypted column round trip", func(t *testing.T) {
		got, err := q.GetParent(ctx, parent.ID)
		if err != nil {
			t.Fatal(err)
		}
		pt, err := kr.DecryptString(got.SafeWordEnc, "parents.safe_word_enc")
		if err != nil || pt != "gulab jamun" {
			t.Fatalf("decrypted %q, %v", pt, err)
		}
		if _, err := kr.DecryptString(got.SafeWordEnc, "parents.interests_enc"); err == nil {
			t.Fatal("value decrypted under another column's aad")
		}
	})

	t.Run("parent checks", func(t *testing.T) {
		base := db.CreateParentParams{
			AccountID: acct.ID, PreferredName: "X", Language: "hi", Timezone: "Asia/Kolkata",
			CallTimeLocal: pgTime(10, 0), WindowStart: pgTime(9, 0), WindowEnd: pgTime(20, 0),
		}
		dup := base
		dup.PhoneE164 = "+919800000010"
		_, err := q.CreateParent(ctx, dup)
		wantViolation(t, err, uniqueViolation)

		badWindow := base
		badWindow.PhoneE164 = "+919800000011"
		badWindow.WindowStart, badWindow.WindowEnd = pgTime(20, 0), pgTime(9, 0)
		_, err = q.CreateParent(ctx, badWindow)
		wantViolation(t, err, checkViolation)

		badLang := base
		badLang.PhoneE164 = "+919800000012"
		badLang.Language = "fr"
		_, err = q.CreateParent(ctx, badLang)
		wantViolation(t, err, checkViolation)

		badContact := base
		badContact.PhoneE164 = "+919800000013"
		bad := "12345"
		badContact.LocalContactPhoneE164 = &bad
		_, err = q.CreateParent(ctx, badContact)
		wantViolation(t, err, checkViolation)
	})

	t.Run("consents", func(t *testing.T) {
		for _, kind := range []string{"calls", "data_processing", "share_with_family"} {
			if _, err := q.RecordConsent(ctx, db.RecordConsentParams{
				ParentID: parent.ID, Kind: kind, GivenBy: "parent", Method: "in_person", TextVersion: "v1", GivenAt: now,
			}); err != nil {
				t.Fatal(err)
			}
		}
		kinds, err := q.ListValidConsentKinds(ctx, parent.ID)
		if err != nil || len(kinds) != 3 {
			t.Fatalf("kinds = %v, %v", kinds, err)
		}
		n, err := q.WithdrawConsent(ctx, db.WithdrawConsentParams{ParentID: parent.ID, Kind: "calls", WithdrawnAt: &now})
		if err != nil || n != 1 {
			t.Fatalf("withdraw rows = %d, %v", n, err)
		}
		kinds, _ = q.ListValidConsentKinds(ctx, parent.ID)
		if len(kinds) != 2 || kinds[0] == "calls" || kinds[1] == "calls" {
			t.Fatalf("after withdraw kinds = %v", kinds)
		}
	})

	t.Run("one slot per parent per day", func(t *testing.T) {
		insert := `INSERT INTO call_slots (parent_id, local_date) VALUES ($1, '2026-10-07')`
		if _, err := pool.Exec(ctx, insert, parent.ID); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(ctx, insert, parent.ID)
		wantViolation(t, err, uniqueViolation)
	})

	t.Run("one alert per call and category", func(t *testing.T) {
		var slotID, callID uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM call_slots WHERE parent_id = $1`, parent.ID).Scan(&slotID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `
			INSERT INTO calls (slot_id, parent_id, attempt_no, scheduled_for, provider)
			VALUES ($1, $2, 1, now(), 'fake') RETURNING id`, slotID, parent.ID).Scan(&callID); err != nil {
			t.Fatal(err)
		}
		insert := `INSERT INTO alerts (parent_id, call_id, type, category, source) VALUES ($1, $2, 'emergency', 'fall', $3)`
		if _, err := pool.Exec(ctx, insert, parent.ID, callID, "tool"); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(ctx, insert, parent.ID, callID, "model")
		wantViolation(t, err, uniqueViolation)
	})

	t.Run("audit log", func(t *testing.T) {
		if err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{
			Actor: "system", Action: "record_consent", Entity: "parent", EntityID: parent.ID.String(),
		}); err != nil {
			t.Fatal(err)
		}
	})
}
