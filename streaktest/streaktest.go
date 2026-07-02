// Package streaktest is a time-travel harness for testing streak behavior:
// drive a simulated clock through days, DST nights, and timezone changes and
// assert on derived state and emitted events.
package streaktest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	streakd "github.com/Vartent/streakd"
)

// Clock is a mutable time source safe for concurrent readers.
type Clock struct{ t atomic.Pointer[time.Time] }

func NewClock(at time.Time) *Clock {
	c := &Clock{}
	c.t.Store(&at)
	return c
}

func (c *Clock) Now() time.Time   { return *c.t.Load() }
func (c *Clock) Set(at time.Time) { c.t.Store(&at) }
func (c *Clock) Advance(d time.Duration) {
	at := c.Now().Add(d)
	c.t.Store(&at)
}

// Sim couples an engine to a simulated clock. Every Advance ticks the
// scheduler on an hourly grid so settlement and reminder events fire exactly
// as they would in production.
type Sim struct {
	T     *testing.T
	E     *streakd.Engine
	Clock *Clock
}

// New builds a Sim on a fresh streaks schema (drops any existing one).
func New(t *testing.T, pool *pgxpool.Pool, start time.Time, opts ...streakd.Option) *Sim {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS streaks CASCADE"); err != nil {
		t.Fatalf("streaktest: drop schema: %v", err)
	}
	clock := NewClock(start)
	e, err := streakd.New(pool, append([]streakd.Option{streakd.WithClock(clock.Now)}, opts...)...)
	if err != nil {
		t.Fatalf("streaktest: new engine: %v", err)
	}
	if err := e.Migrate(ctx); err != nil {
		t.Fatalf("streaktest: migrate: %v", err)
	}
	return &Sim{T: t, E: e, Clock: clock}
}

// Record reports activity for the subject now.
func (s *Sim) Record(subject, key string) streakd.StreakView {
	s.T.Helper()
	v, err := s.E.Record(context.Background(), streakd.RecordReq{Subject: subject, Key: key})
	if err != nil {
		s.T.Fatalf("streaktest: record %s/%s: %v", subject, key, err)
	}
	return v
}

// Advance moves the clock forward, ticking the scheduler every hour.
func (s *Sim) Advance(d time.Duration) {
	s.T.Helper()
	deadline := s.Clock.Now().Add(d)
	for s.Clock.Now().Before(deadline) {
		step := time.Hour
		if remaining := deadline.Sub(s.Clock.Now()); remaining < step {
			step = remaining
		}
		s.Clock.Advance(step)
		if err := s.E.Tick(context.Background()); err != nil {
			s.T.Fatalf("streaktest: tick: %v", err)
		}
	}
}

// AdvanceDays is Advance in 24h units.
func (s *Sim) AdvanceDays(n int) { s.T.Helper(); s.Advance(time.Duration(n) * 24 * time.Hour) }

// TravelTo relocates the subject to a new IANA zone.
func (s *Sim) TravelTo(subject, tz string) {
	s.T.Helper()
	if err := s.E.SetTimezone(context.Background(), subject, tz); err != nil {
		s.T.Fatalf("streaktest: travel %s -> %s: %v", subject, tz, err)
	}
}

// State returns the derived view.
func (s *Sim) State(subject, key string) streakd.StreakView {
	s.T.Helper()
	v, err := s.E.Get(context.Background(), subject, key)
	if err != nil {
		s.T.Fatalf("streaktest: get %s/%s: %v", subject, key, err)
	}
	return v
}

// ExpectAlive asserts liveness and count.
func (s *Sim) ExpectAlive(subject, key string, count int) {
	s.T.Helper()
	v := s.State(subject, key)
	if v.State == streakd.Broken || v.Count != count {
		s.T.Fatalf("streaktest: %s/%s = %+v, want alive count %d", subject, key, v, count)
	}
}

// ExpectBroken asserts the streak is broken (derived count 0).
func (s *Sim) ExpectBroken(subject, key string) {
	s.T.Helper()
	if v := s.State(subject, key); v.State != streakd.Broken || v.Count != 0 {
		s.T.Fatalf("streaktest: %s/%s = %+v, want broken", subject, key, v)
	}
}

// Events returns all outbox events so far.
func (s *Sim) Events() []streakd.Event {
	s.T.Helper()
	evs, err := s.E.PollEvents(context.Background(), 0, 10000)
	if err != nil {
		s.T.Fatalf("streaktest: poll events: %v", err)
	}
	return evs
}

// EventCount counts events of one type.
func (s *Sim) EventCount(typ streakd.EventType) int {
	n := 0
	for _, ev := range s.Events() {
		if ev.Type == typ {
			n++
		}
	}
	return n
}
