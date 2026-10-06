// Package migrate applies the embedded goose migrations in migrations/.
package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/EklavyaGoyal17/haalchaal/migrations"
)

// Migrator runs migrations against one database.
type Migrator struct {
	db       *sql.DB
	provider *goose.Provider
}

// New returns a Migrator that shares the given pool's connections.
func New(pool *pgxpool.Pool) (*Migrator, error) {
	db := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("goose provider: %w", err)
	}
	return &Migrator{db: db, provider: p}, nil
}

// Close releases the database handle (the pool itself stays open).
func (m *Migrator) Close() error { return m.db.Close() }

// Up applies every pending migration and returns the resulting version.
func (m *Migrator) Up(ctx context.Context) (int64, error) {
	if _, err := m.provider.Up(ctx); err != nil {
		return 0, fmt.Errorf("migrate up: %w", err)
	}
	return m.provider.GetDBVersion(ctx)
}

// Down rolls back the most recent migration and returns the resulting version.
func (m *Migrator) Down(ctx context.Context) (int64, error) {
	if _, err := m.provider.Down(ctx); err != nil {
		return 0, fmt.Errorf("migrate down: %w", err)
	}
	return m.provider.GetDBVersion(ctx)
}

// Reset rolls back every migration. Used by integration tests.
func (m *Migrator) Reset(ctx context.Context) error {
	if _, err := m.provider.DownTo(ctx, 0); err != nil {
		return fmt.Errorf("migrate reset: %w", err)
	}
	return nil
}

// Status describes each migration and whether it is applied.
func (m *Migrator) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	return m.provider.Status(ctx)
}

// HasPending reports whether any embedded migration is not yet applied.
func (m *Migrator) HasPending(ctx context.Context) (bool, error) {
	return m.provider.HasPending(ctx)
}
