// Package engine orchestrates core transitions over the store within
// transactions: recording, reads, timezone changes, repair, and the outbox.
package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Vartent/streakd/internal/core"
	"github.com/Vartent/streakd/internal/store"
)

const (
	defaultAppID = 1
	// boundaryGrace lets an activity recorded moments after a period boundary
	// still land in the period it happened in, as long as that period has not
	// been settled yet.
	boundaryGrace = 10 * time.Minute
	// repairWindow bounds how long after a break a streak can be repaired.
	repairWindow = 30 * 24 * time.Hour
)

var (
	ErrUnknownStreakType = errors.New("streakd: unregistered streak key and no config provided")
	ErrNotFound          = errors.New("streakd: not found")
	ErrBadTimezone       = errors.New("streakd: invalid IANA timezone")
	ErrOutsidePeriod     = errors.New("streakd: activity outside the current period")
	ErrNothingToRepair   = errors.New("streakd: no recent break to repair")
)

// Event is the engine-level event delivered to handlers and pollers.
type Event struct {
	ID        int64          `json:"id"`
	Subject   string         `json:"subject"`
	Key       string         `json:"key"`
	Type      core.EventType `json:"type"`
	Period    string         `json:"period"`
	Count     int            `json:"count"`
	CreatedAt time.Time      `json:"created_at"`
}

// Engine is the embedded streakd instance. Safe for concurrent use.
type Engine struct {
	pool      *pgxpool.Pool
	appID     int64
	clock     func() time.Time
	defaultTZ string
	types     map[string]core.Config
	onEvent   func(Event)
	logger    *slog.Logger
}

type Option func(*Engine)

// WithClock injects a time source (tests, simulations).
func WithClock(now func() time.Time) Option { return func(e *Engine) { e.clock = now } }

// WithDefaultTimezone sets the timezone for subjects that never called
// SetTimezone. Defaults to UTC.
func WithDefaultTimezone(tz string) Option { return func(e *Engine) { e.defaultTZ = tz } }

// WithStreakType registers a config template; Record auto-creates streaks for
// registered keys on first activity.
func WithStreakType(key string, cfg core.Config) Option {
	return func(e *Engine) { e.types[key] = cfg.Normalized() }
}

// WithEventHandler registers a synchronous post-commit callback. Delivery is
// at-least-once from this process's successful transactions; cross-process
// consumers should poll the outbox instead.
func WithEventHandler(h func(Event)) Option { return func(e *Engine) { e.onEvent = h } }

// WithLogger overrides the scheduler's logger (defaults to slog.Default()).
func WithLogger(l *slog.Logger) Option { return func(e *Engine) { e.logger = l } }

func New(pool *pgxpool.Pool, opts ...Option) (*Engine, error) {
	e := &Engine{
		pool:      pool,
		appID:     defaultAppID,
		clock:     time.Now,
		defaultTZ: "UTC",
		types:     map[string]core.Config{},
		logger:    slog.Default(),
	}
	for _, o := range opts {
		o(e)
	}
	if _, err := time.LoadLocation(e.defaultTZ); err != nil {
		return nil, fmt.Errorf("%w: %q", ErrBadTimezone, e.defaultTZ)
	}
	for key, cfg := range e.types {
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("streakd: streak type %q: %w", key, err)
		}
	}
	return e, nil
}

// Migrate applies the streaks schema migrations through a temporary
// database/sql handle on the same connection string.
func (e *Engine) Migrate(ctx context.Context) error {
	db := stdlib.OpenDBFromPool(e.pool)
	defer db.Close()
	return store.Migrate(ctx, db)
}

// MigrateDB applies migrations on a caller-provided handle (for hosts that
// already manage a database/sql connection).
func MigrateDB(ctx context.Context, db *sql.DB) error {
	return store.Migrate(ctx, db)
}

func (e *Engine) loadLocation(tz string) (*time.Location, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrBadTimezone, tz)
	}
	return loc, nil
}

func (e *Engine) configFor(key string, override *core.Config) (core.Config, error) {
	if override != nil {
		cfg := override.Normalized()
		return cfg, cfg.Validate()
	}
	cfg, ok := e.types[key]
	if !ok {
		return core.Config{}, fmt.Errorf("%w: %q", ErrUnknownStreakType, key)
	}
	return cfg, nil
}

// inTx runs fn in a transaction and, on commit, fires the event handler for
// every event fn queued.
func (e *Engine) inTx(ctx context.Context, fn func(tx pgx.Tx, emit func(Event)) error) error {
	var queued []Event
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		queued = queued[:0]
		return fn(tx, func(ev Event) { queued = append(queued, ev) })
	})
	if err != nil {
		return err
	}
	if e.onEvent != nil {
		for _, ev := range queued {
			e.onEvent(ev)
		}
	}
	return nil
}

// persistEvents writes core events to the outbox and queues them for the
// post-commit handler.
func persistEvents(ctx context.Context, tx pgx.Tx, appID int64, st store.Streak, subjectExternalID string,
	events []core.Event, at time.Time, emit func(Event)) error {
	for _, ce := range events {
		id, err := store.InsertEvent(ctx, tx, appID, st.ID, subjectExternalID, st.Key, ce)
		if err != nil {
			return err
		}
		emit(Event{
			ID: id, Subject: subjectExternalID, Key: st.Key,
			Type: ce.Type, Period: ce.Period.String(), Count: ce.Count, CreatedAt: at,
		})
	}
	return nil
}
