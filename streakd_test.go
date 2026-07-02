package streakd_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	streakd "github.com/Vartent/streakd"
	"github.com/Vartent/streakd/streaktest"
)

// Public-API smoke test: the DESIGN.md walkthrough scenario, driven through
// the streaktest harness (which is itself under test here).
func TestPublicAPIWalkthrough(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://streakd:streakd@localhost:5599/streakd_test?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no test database reachable (%v)", err)
	}

	sim := streaktest.New(t, pool,
		time.Date(2026, 3, 7, 21, 0, 0, 0, time.UTC),
		streakd.WithStreakType("practice", streakd.Config{
			Period:  streakd.PeriodDay,
			Freezes: streakd.FreezePolicy{Initial: 1, Max: 2, AutoConsume: true},
		}),
	)

	sim.TravelTo("u1", "Europe/Berlin")
	sim.Record("u1", "practice")
	sim.AdvanceDays(1)
	sim.Record("u1", "practice")
	sim.ExpectAlive("u1", "practice", 2)

	// Fly to Los Angeles the same day: never breaks a streak.
	sim.TravelTo("u1", "America/Los_Angeles")
	sim.ExpectAlive("u1", "practice", 2)

	// Lapse one day: the initial freeze holds the chain.
	sim.AdvanceDays(2)
	sim.ExpectAlive("u1", "practice", 2)
	if got := sim.EventCount(streakd.EventFreezeConsumed); got != 1 {
		t.Fatalf("freeze_consumed = %d, want 1", got)
	}
	if got := sim.EventCount(streakd.EventBroken); got != 0 {
		t.Fatalf("broken = %d, want 0", got)
	}

	// Two more silent days: now it breaks.
	sim.AdvanceDays(2)
	sim.ExpectBroken("u1", "practice")

	// Repair restores the pre-break count.
	v, err := sim.E.Repair(context.Background(), "u1", "practice")
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if v.Count != 2 || v.State == streakd.Broken {
		t.Fatalf("repaired = %+v, want count 2", v)
	}
}
