package streakd_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Serialize against the internal/engine package: both drop/recreate the
// shared streaks schema, and `go test ./...` runs packages in parallel. The
// lock ID must match internal/engine's.
const testPackageAdvisoryLock = 0x5f7265616b7431

func TestMain(m *testing.M) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://streakd:streakd@localhost:5599/streakd_test?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err == nil && pool.Ping(ctx) == nil {
		conn, err := pool.Acquire(ctx)
		if err == nil {
			if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", testPackageAdvisoryLock); err == nil {
				code := m.Run()
				_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", testPackageAdvisoryLock)
				conn.Release()
				pool.Close()
				os.Exit(code)
			}
			conn.Release()
		}
		pool.Close()
	}
	os.Exit(m.Run())
}
