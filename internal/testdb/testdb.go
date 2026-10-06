// Package testdb gives integration tests a freshly migrated database and
// small fixtures. Tests using it skip when TEST_DATABASE_URL is unset, and
// must not run in parallel with other packages (make test-integration uses -p 1).
package testdb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/migrate"
)

// New resets and migrates TEST_DATABASE_URL and returns a pool.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m, err := migrate.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Log discards output; tests that care about logs build their own.
func Log() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// Family describes a fixture parent. Zero values get sensible defaults.
type Family struct {
	Plan          string // daily
	AccountStatus string // active
	ParentStatus  string // active
	Phone         string // a unique +9198... number
	Timezone      string // Asia/Kolkata
	CallTime      string // 10:00
	WindowStart   string // 09:00
	WindowEnd     string // 20:00
	Consents      []string
	NoConsents    bool
	MemberPhone   string // a unique +9199... number
}

// Created holds fixture ids.
type Created struct {
	AccountID uuid.UUID
	ParentID  uuid.UUID
	MemberID  uuid.UUID
}

var seq int

func pgTime(t testing.TB, s string) pgtype.Time {
	t.Helper()
	tt, err := time.Parse("15:04", s)
	if err != nil {
		t.Fatal(err)
	}
	d := time.Duration(tt.Hour())*time.Hour + time.Duration(tt.Minute())*time.Minute
	return pgtype.Time{Microseconds: d.Microseconds(), Valid: true}
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// CreateFamily inserts an account, one family member and one parent, with
// the three required consents unless NoConsents or Consents says otherwise.
func CreateFamily(t testing.TB, pool *pgxpool.Pool, f Family) Created {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)
	seq++
	acc, err := q.CreateAccount(ctx, db.CreateAccountParams{Plan: or(f.Plan, "daily")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET status = $2 WHERE id = $1`, acc.ID, or(f.AccountStatus, "active")); err != nil {
		t.Fatal(err)
	}
	optIn := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rel := "son"
	m, err := q.CreateFamilyMember(ctx, db.CreateFamilyMemberParams{
		AccountID: acc.ID, Name: "Test Member", PhoneE164: or(f.MemberPhone, phoneN("+9199", seq)),
		Relation: &rel, Language: "en", Priority: 1, WhatsappOptInAt: &optIn,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := q.CreateParent(ctx, db.CreateParentParams{
		AccountID: acc.ID, PreferredName: "Test ji", PhoneE164: or(f.Phone, phoneN("+9198", seq)),
		Language: "hi", Timezone: or(f.Timezone, "Asia/Kolkata"),
		CallTimeLocal: pgTime(t, or(f.CallTime, "10:00")),
		WindowStart:   pgTime(t, or(f.WindowStart, "09:00")),
		WindowEnd:     pgTime(t, or(f.WindowEnd, "20:00")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetParentStatus(ctx, db.SetParentStatusParams{ID: p.ID, Status: or(f.ParentStatus, "active")}); err != nil {
		t.Fatal(err)
	}
	kinds := f.Consents
	if kinds == nil && !f.NoConsents {
		kinds = []string{"calls", "data_processing", "share_with_family"}
	}
	for _, k := range kinds {
		if _, err := q.RecordConsent(ctx, db.RecordConsentParams{
			ParentID: p.ID, Kind: k, GivenBy: "parent", Method: "in_person",
			TextVersion: "v1", GivenAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return Created{AccountID: acc.ID, ParentID: p.ID, MemberID: m.ID}
}

func phoneN(prefix string, n int) string { return fmt.Sprintf("%s%08d", prefix, n) }
