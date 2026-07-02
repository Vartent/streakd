package engine

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The DB-backed test packages (this one and the repo root) share one database
// and drop/recreate the streaks schema per test. `go test ./...` runs
// packages in parallel, so without cross-package serialization one package
// drops the schema mid-migration of the other. A session-scoped advisory
// lock held for the whole package run serializes them.
const testPackageAdvisoryLock = 0x5f7265616b7431

func TestMain(m *testing.M) {
	os.Exit(runSerializedOnTestDB(m, testPackageAdvisoryLock))
}

func runSerializedOnTestDB(m *testing.M, lockID int64) int {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultTestDB
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err == nil && pool.Ping(ctx) == nil {
		conn, err := pool.Acquire(ctx)
		if err == nil {
			if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err == nil {
				defer func() {
					_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID)
					conn.Release()
					pool.Close()
				}()
				return m.Run()
			}
			conn.Release()
		}
		pool.Close()
	}
	// No test DB reachable: individual tests skip themselves.
	return m.Run()
}
