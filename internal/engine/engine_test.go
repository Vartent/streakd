package engine

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vartent/streakd/internal/core"
)

// Integration tests against a real Postgres (TEST_DATABASE_URL, defaulting to
// the local ephemeral container used in CI and dev).

const defaultTestDB = "postgres://streakd:streakd@localhost:5599/streakd_test?sslmode=disable"

type testClock struct{ t atomic.Pointer[time.Time] }

func newTestClock(at time.Time) *testClock {
	c := &testClock{}
	c.t.Store(&at)
	return c
}
func (c *testClock) Now() time.Time     { return *c.t.Load() }
func (c *testClock) Set(at time.Time)   { c.t.Store(&at) }
func (c *testClock) Advance(d time.Duration) {
	at := c.Now().Add(d)
	c.t.Store(&at)
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultTestDB
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no test database reachable (%v); set TEST_DATABASE_URL", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func freshEngine(t *testing.T, clock *testClock, opts ...Option) *Engine {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS streaks CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	opts = append([]Option{WithClock(clock.Now)}, opts...)
	e, err := New(pool, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return e
}

func kanjiLikeConfig() core.Config {
	return core.Config{
		Period:     core.PeriodDay,
		Milestones: []int{3, 7, 14, 30, 50, 100, 365},
		Freezes:    core.FreezePolicy{Initial: 0, EarnEveryNPeriods: 7, Max: 2, AutoConsume: true},
	}
}

// assertOracle verifies the operational invariant: replaying the ledger
// reproduces the stored state.
func assertOracle(t *testing.T, e *Engine, subject, key string) {
	t.Helper()
	before, err := e.Get(context.Background(), subject, key)
	if err != nil {
		t.Fatalf("oracle get: %v", err)
	}
	after, err := e.Recount(context.Background(), subject, key)
	if err != nil {
		t.Fatalf("oracle recount: %v", err)
	}
	if before.Count != after.Count || before.Longest != after.Longest ||
		before.State != after.State || before.Freezes != after.Freezes {
		t.Fatalf("oracle divergence:\n stored view:   %+v\n replayed view: %+v", before, after)
	}
}

func TestRecordLifecycle(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))

	v, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if v.Count != 1 || v.State != core.Alive || !v.EarnedThisPeriod {
		t.Fatalf("first record view = %+v", v)
	}
	// Second same-day record: amount bumps, count does not.
	v, err = e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"})
	if err != nil {
		t.Fatalf("second record: %v", err)
	}
	if v.Count != 1 || v.AmountThisPeriod != 2 {
		t.Fatalf("same-day record view = %+v", v)
	}

	// Next day, before any activity: still alive, unearned — the "midnight
	// un-completes the flame but keeps the count" behavior kanji was missing.
	clock.Advance(24 * time.Hour)
	v, err = e.Get(ctx, "u1", "practice")
	if err != nil {
		t.Fatalf("get next day: %v", err)
	}
	if v.Count != 1 || v.EarnedThisPeriod || v.State != core.Alive || v.SecondsUntilLoss <= 0 {
		t.Fatalf("next-day view = %+v, want alive unearned count 1 with deadline", v)
	}

	v, _ = e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"})
	if v.Count != 2 {
		t.Fatalf("day-2 record = %+v", v)
	}

	// Skip two days with no freezes: broken, count reads 0 immediately.
	clock.Advance(72 * time.Hour)
	v, err = e.Get(ctx, "u1", "practice")
	if err != nil {
		t.Fatalf("get after lapse: %v", err)
	}
	if v.Count != 0 || v.State != core.Broken || v.Longest != 2 {
		t.Fatalf("lapsed view = %+v, want broken count 0 longest 2", v)
	}
	assertOracle(t, e, "u1", "practice")
}

func TestRecordConcurrencyRace(t *testing.T) {
	// The kanji_cards production race: 50 concurrent activity reports on one
	// user must produce exactly one earned period and 50 accumulated amount.
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent record: %v", err)
		}
	}
	v, err := e.Get(ctx, "u1", "practice")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Count != 1 || v.AmountThisPeriod != n {
		t.Fatalf("after %d concurrent records: count=%d amount=%d, want 1/%d", n, v.Count, v.AmountThisPeriod, n)
	}
	events, err := e.PollEvents(ctx, 0, 100)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	extended := 0
	for _, ev := range events {
		if ev.Type == core.EventExtended {
			extended++
		}
	}
	if extended != 1 {
		t.Fatalf("extended events = %d, want exactly 1", extended)
	}
	assertOracle(t, e, "u1", "practice")
}

func TestRecordIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))

	v1, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice", IdempotencyKey: "evt-1"})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	v2, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice", IdempotencyKey: "evt-1"})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	j1, _ := json.Marshal(v1)
	j2, _ := json.Marshal(v2)
	if string(j1) != string(j2) {
		t.Fatalf("replayed view differs:\n first  %s\n replay %s", j1, j2)
	}
	if v, _ := e.Get(ctx, "u1", "practice"); v.AmountThisPeriod != 1 {
		t.Fatalf("amount after replay = %d, want 1 (no double record)", v.AmountThisPeriod)
	}
}

func TestFreezeConsumptionAndInventory(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))

	// Earn 7 days -> 1 freeze earned.
	for i := 0; i < 7; i++ {
		if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"}); err != nil {
			t.Fatalf("day %d: %v", i, err)
		}
		clock.Advance(24 * time.Hour)
	}
	v, _ := e.Get(ctx, "u1", "practice")
	if v.Count != 7 || v.Freezes.Available != 1 || v.Freezes.Progress != 0 {
		t.Fatalf("after 7 days: %+v, want count 7 freezes 1", v)
	}
	// Miss one day (clock already 1 day past the last earn; skip one more).
	clock.Advance(24 * time.Hour)
	v, _ = e.Get(ctx, "u1", "practice")
	if v.State != core.Frozen || v.Count != 7 || v.Freezes.Available != 0 {
		t.Fatalf("frozen view = %+v, want frozen count 7, freeze pending spend", v)
	}
	// Recording settles the freeze spend and extends.
	v, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"})
	if err != nil {
		t.Fatalf("record after freeze: %v", err)
	}
	if v.Count != 8 || v.Freezes.Available != 0 || v.State != core.Alive {
		t.Fatalf("post-freeze record = %+v, want count 8", v)
	}
	events, _ := e.PollEvents(ctx, 0, 200)
	var consumed bool
	for _, ev := range events {
		if ev.Type == core.EventFreezeConsumed {
			consumed = true
		}
	}
	if !consumed {
		t.Fatal("expected a freeze_consumed event in the outbox")
	}
	assertOracle(t, e, "u1", "practice")
}

func TestSetTimezoneGenerosity(t *testing.T) {
	ctx := context.Background()
	// 2026-05-11 20:00 UTC.
	clock := newTestClock(time.Date(2026, 5, 11, 20, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))

	if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Fly east: in Kiritimati it is already 2026-05-12 10:00. The earned day
	// must survive and the streak stays alive.
	if err := e.SetTimezone(ctx, "u1", "Pacific/Kiritimati"); err != nil {
		t.Fatalf("set tz: %v", err)
	}
	v, err := e.Get(ctx, "u1", "practice")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Count != 1 || v.State != core.Alive || v.EarnedThisPeriod {
		t.Fatalf("post-eastward view = %+v, want alive count 1 unearned (new local day)", v)
	}
	// And the new local day is earnable immediately.
	v, err = e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"})
	if err != nil {
		t.Fatalf("record in new tz: %v", err)
	}
	if v.Count != 2 {
		t.Fatalf("new-tz record = %+v, want count 2", v)
	}

	// Fly west: same instant is still 2026-05-12 in Honolulu... it is 05-12
	// 00:00 UTC + clock; keep it simple — the streak must never break from
	// the change alone.
	if err := e.SetTimezone(ctx, "u1", "Pacific/Honolulu"); err != nil {
		t.Fatalf("set tz west: %v", err)
	}
	v, err = e.Get(ctx, "u1", "practice")
	if err != nil {
		t.Fatalf("get after westward: %v", err)
	}
	if v.State == core.Broken || v.Count != 2 {
		t.Fatalf("post-westward view = %+v, want count 2 not broken", v)
	}
}

func TestUnrecordToggle(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("habit", kanjiLikeConfig()))

	if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "habit"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	clock.Advance(24 * time.Hour)
	if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "habit"}); err != nil {
		t.Fatalf("record 2: %v", err)
	}
	v, err := e.Unrecord(ctx, "u1", "habit")
	if err != nil {
		t.Fatalf("unrecord: %v", err)
	}
	if v.Count != 1 || v.EarnedThisPeriod {
		t.Fatalf("after unrecord = %+v, want count 1 unearned", v)
	}
	// Toggle back on.
	v, err = e.Record(ctx, RecordReq{Subject: "u1", Key: "habit"})
	if err != nil {
		t.Fatalf("re-record: %v", err)
	}
	if v.Count != 2 {
		t.Fatalf("after re-record = %+v, want count 2", v)
	}
	assertOracle(t, e, "u1", "habit")
}

func TestRepairRestoresPreBreakCount(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))

	for i := 0; i < 5; i++ {
		if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"}); err != nil {
			t.Fatalf("day %d: %v", i, err)
		}
		clock.Advance(24 * time.Hour)
	}
	// Lapse three days: broken (no freezes yet at 5 earned days).
	clock.Advance(3 * 24 * time.Hour)
	if v, _ := e.Get(ctx, "u1", "practice"); v.State != core.Broken {
		t.Fatalf("expected broken before repair, got %+v", v)
	}
	// Trigger settle (and the broken event) via a record, then repair.
	if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"}); err != nil {
		t.Fatalf("record after lapse: %v", err)
	}
	v, err := e.Repair(ctx, "u1", "practice")
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	// 5 lost + 1 rebuilt after the break.
	if v.Count != 6 || v.State != core.Alive {
		t.Fatalf("repaired view = %+v, want alive count 6", v)
	}
	// Second repair with no new break must refuse.
	if _, err := e.Repair(ctx, "u1", "practice"); err != ErrNothingToRepair {
		t.Fatalf("second repair err = %v, want ErrNothingToRepair", err)
	}
}

func TestGetUnknownSubject(t *testing.T) {
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))
	if _, err := e.Get(context.Background(), "ghost", "practice"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRecordRejectsFuture(t *testing.T) {
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))
	_, err := e.Record(context.Background(), RecordReq{
		Subject: "u1", Key: "practice", At: clock.Now().Add(2 * time.Hour),
	})
	if err == nil {
		t.Fatal("future-dated record must be rejected")
	}
}

func TestCalendar(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))
	for i := 0; i < 3; i++ {
		if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"}); err != nil {
			t.Fatal(err)
		}
		clock.Advance(48 * time.Hour) // every other day
	}
	days, err := e.Calendar(ctx, "u1", "practice", core.MustDate("2026-05-01"), core.MustDate("2026-05-31"))
	if err != nil {
		t.Fatalf("calendar: %v", err)
	}
	if len(days) != 3 || days[0].Period != "2026-05-11" || !days[0].Earned {
		t.Fatalf("calendar = %+v, want 3 earned days from 05-11", days)
	}
}
