// Package streakd is an embeddable streak engine: timezone-correct period
// math, freezes, repair, and at-risk reminder events on top of your existing
// Postgres.
//
// Quickstart:
//
//	pool, _ := pgxpool.New(ctx, dsn)
//	eng, _ := streakd.New(pool,
//	    streakd.WithDefaultTimezone("UTC"),
//	    streakd.WithStreakType("practice", streakd.Config{
//	        Period:     streakd.PeriodDay,
//	        Milestones: []int{3, 7, 14, 30, 50, 100, 365},
//	        Freezes:    streakd.FreezePolicy{EarnEveryNPeriods: 7, Max: 2, AutoConsume: true},
//	    }),
//	    streakd.WithEventHandler(func(ev streakd.Event) { /* push, telegram, ... */ }),
//	)
//	_ = eng.Migrate(ctx)               // owns the `streaks` schema, own version table
//	go eng.RunScheduler(ctx, 5*time.Minute)
//
//	view, _ := eng.Record(ctx, streakd.RecordReq{Subject: "user:1", Key: "practice"})
//	view, _ = eng.Get(ctx, "user:1", "practice") // always derived, never stale
//
// State is a pure function of an append-only ledger: reads are correct even
// if the scheduler never runs — a dead cron can delay a notification but can
// never corrupt a streak or show a stale count.
package streakd

import (
	"github.com/Vartent/streakd/internal/core"
	"github.com/Vartent/streakd/internal/engine"
)

// Engine and its option/request/response types.
type (
	Engine        = engine.Engine
	Option        = engine.Option
	RecordReq     = engine.RecordReq
	StreakView    = engine.StreakView
	FreezesView   = engine.FreezesView
	MilestoneView = engine.MilestoneView
	TargetView    = engine.TargetView
	CalendarDay   = engine.CalendarDay
	Event         = engine.Event
)

// Streak behavior configuration.
type (
	Config       = core.Config
	FreezePolicy = core.FreezePolicy
	Period       = core.Period
	Date         = core.Date
	EventType    = core.EventType
	Liveness     = core.Liveness
)

const (
	PeriodDay   = core.PeriodDay
	PeriodWeek  = core.PeriodWeek
	PeriodMonth = core.PeriodMonth

	Alive  = core.Alive
	Frozen = core.Frozen
	Broken = core.Broken

	EventExtended       = core.EventExtended
	EventFreezeEarned   = core.EventFreezeEarned
	EventFreezeConsumed = core.EventFreezeConsumed
	EventBroken         = core.EventBroken
	EventMilestone      = core.EventMilestone
	EventCompleted      = core.EventCompleted
	EventRepaired       = core.EventRepaired
	EventAtRisk         = core.EventAtRisk
)

// Constructor and options.
var (
	New                 = engine.New
	WithClock           = engine.WithClock
	WithDefaultTimezone = engine.WithDefaultTimezone
	WithStreakType      = engine.WithStreakType
	WithEventHandler    = engine.WithEventHandler
	WithLogger          = engine.WithLogger

	MigrateDB = engine.MigrateDB

	ParseDate = core.ParseDate
	MustDate  = core.MustDate
)

// Sentinel errors.
var (
	ErrNotFound          = engine.ErrNotFound
	ErrBadTimezone       = engine.ErrBadTimezone
	ErrOutsidePeriod     = engine.ErrOutsidePeriod
	ErrNothingToRepair   = engine.ErrNothingToRepair
	ErrUnknownStreakType = engine.ErrUnknownStreakType
)
