// Package store owns the streaks.* Postgres schema: migrations and typed
// queries. All state-mutating queries are designed to run inside a caller-held
// transaction with the streak row locked.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate brings the streaks schema up to date. Versioning lives in
// streaks.goose_version so host-app migration tooling is never touched.
func Migrate(ctx context.Context, db *sql.DB) error {
	// The version table lives inside the schema it versions, so the schema
	// must exist before goose initializes.
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS streaks"); err != nil {
		return fmt.Errorf("store: create schema: %w", err)
	}
	st, err := database.NewStore(database.DialectPostgres, "streaks.goose_version")
	if err != nil {
		return fmt.Errorf("store: init version store: %w", err)
	}
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: migrations fs: %w", err)
	}
	provider, err := goose.NewProvider("", db, sub, goose.WithStore(st))
	if err != nil {
		return fmt.Errorf("store: init migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}
