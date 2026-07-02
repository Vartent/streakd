package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Vartent/streakd/internal/core"
	"github.com/Vartent/streakd/internal/store"
)

// schedulerAdvisoryLock serializes concurrent scheduler instances.
const schedulerAdvisoryLock = 0x5f7265616b64

// RunScheduler ticks until ctx is cancelled. The scheduler exists only for
// side effects — near-real-time settlement events and at-risk reminders.
// Correctness of every read never depends on it running.
func (e *Engine) RunScheduler(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := e.Tick(ctx); err != nil && ctx.Err() == nil {
			e.logger.Error("streakd: scheduler tick failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Tick runs one scheduler pass: settle everything due, then emit due at-risk
// reminders. Safe to run from multiple processes (advisory lock) and
// idempotent within a period (settle pointers, reminder claims).
func (e *Engine) Tick(ctx context.Context) error {
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", schedulerAdvisoryLock).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil // another instance is ticking
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", schedulerAdvisoryLock)
	}()

	if err := e.settleDue(ctx); err != nil {
		return err
	}
	return e.remindDue(ctx)
}

// dueStreak is one streak candidate a tick may need to touch.
type dueStreak struct {
	subjectID      int64
	externalID     string
	timezone       string
	key            string
	config         core.Config
	lastEarned     core.Date
	settledThrough core.Date
	currentCount   int
}

func (e *Engine) loadCandidates(ctx context.Context, where string) ([]dueStreak, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT s.subject_id, sub.external_id, sub.timezone, s.key, s.config,
		       s.last_earned, s.settled_through, s.current_count
		FROM streaks.streaks s
		JOIN streaks.subjects sub ON sub.id = s.subject_id
		WHERE s.status = 'active' AND `+where)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dueStreak
	for rows.Next() {
		var (
			d              dueStreak
			cfgJSON        []byte
			lastEarned     *time.Time
			settledThrough *time.Time
		)
		if err := rows.Scan(&d.subjectID, &d.externalID, &d.timezone, &d.key, &cfgJSON,
			&lastEarned, &settledThrough, &d.currentCount); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cfgJSON, &d.config); err != nil {
			return nil, err
		}
		d.config = d.config.Normalized()
		if lastEarned != nil {
			d.lastEarned = core.Date{Y: lastEarned.Year(), M: lastEarned.Month(), D: lastEarned.Day()}
		}
		if settledThrough != nil {
			d.settledThrough = core.Date{Y: settledThrough.Year(), M: settledThrough.Month(), D: settledThrough.Day()}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// settleDue settles every streak whose settle pointer lags the previous
// period in its subject's zone. The Go-side prefilter keeps ticks cheap:
// row locks are only taken for streaks with an actual boundary crossing.
func (e *Engine) settleDue(ctx context.Context) error {
	now := e.clock()
	candidates, err := e.loadCandidates(ctx, "s.last_earned IS NOT NULL")
	if err != nil {
		return err
	}
	for _, d := range candidates {
		loc, err := e.loadLocation(d.timezone)
		if err != nil {
			continue // unresolvable zone: reads still derive correctly
		}
		cur := core.PeriodKey(now, loc, d.config)
		from := laterDate(d.lastEarned, d.settledThrough)
		if !from.Before(prevCalendarPeriod(cur, d.config)) && !d.settledThrough.IsZero() {
			continue // nothing elapsed since the last settle
		}
		if core.MissedBetween(from, cur, d.config, core.MaxFreezes+1) == 0 && !d.settledThrough.IsZero() {
			// Pointer lags but only across rest days; advance it lazily on
			// the next real transition instead of locking now.
			continue
		}
		if err := e.settleOne(ctx, d, now, loc); err != nil {
			return fmt.Errorf("settle %s/%s: %w", d.externalID, d.key, err)
		}
	}
	return nil
}

func laterDate(a, b core.Date) core.Date {
	if a.Before(b) {
		return b
	}
	return a
}

func (e *Engine) settleOne(ctx context.Context, d dueStreak, now time.Time, loc *time.Location) error {
	return e.inTx(ctx, func(tx pgx.Tx, emit func(Event)) error {
		st, err := store.GetStreakLocked(ctx, tx, d.subjectID, d.key)
		if err != nil {
			return translateNotFound(err)
		}
		state, events := core.Settle(st.State, st.Config, now, loc)
		if len(events) == 0 && state == st.State {
			return nil
		}
		if err := store.SaveStreakState(ctx, tx, st.ID, state); err != nil {
			return err
		}
		return persistEvents(ctx, tx, e.appID, st, d.externalID, events, now, emit)
	})
}

// remindDue emits at_risk events for day-period streaks that are alive,
// unearned today, past their configured local reminder time, and unclaimed.
func (e *Engine) remindDue(ctx context.Context) error {
	now := e.clock()
	candidates, err := e.loadCandidates(ctx,
		"s.config->>'reminder_local_time' <> '' AND s.current_count > 0")
	if err != nil {
		return err
	}
	for _, d := range candidates {
		if d.config.Period != core.PeriodDay || d.config.ReminderLocalTime == "" {
			continue
		}
		loc, err := e.loadLocation(d.timezone)
		if err != nil {
			continue
		}
		cur := core.PeriodKey(now, loc, d.config)
		reminderAt, ok := reminderInstant(cur, d.config.ReminderLocalTime, loc)
		if !ok || now.Before(reminderAt) {
			continue
		}
		if err := e.remindOne(ctx, d, now, loc); err != nil {
			return fmt.Errorf("remind %s/%s: %w", d.externalID, d.key, err)
		}
	}
	return nil
}

func (e *Engine) remindOne(ctx context.Context, d dueStreak, now time.Time, loc *time.Location) error {
	return e.inTx(ctx, func(tx pgx.Tx, emit func(Event)) error {
		st, err := store.GetStreakLocked(ctx, tx, d.subjectID, d.key)
		if err != nil {
			return translateNotFound(err)
		}
		derived := core.Derive(st.State, st.Config, now, loc)
		// Dead-streak awareness: never nag about a broken streak, an already
		// earned day, or a rest day.
		if derived.Liveness == core.Broken || derived.EarnedThisPeriod || !st.Config.ScheduledOn(derived.CurrentPeriod) {
			return nil
		}
		claimed, err := store.ClaimReminder(ctx, tx, st.ID, derived.CurrentPeriod)
		if err != nil || !claimed {
			return err
		}
		ev := core.Event{Type: core.EventAtRisk, Period: derived.CurrentPeriod, Count: derived.Count}
		return persistEvents(ctx, tx, e.appID, st, d.externalID, []core.Event{ev}, now, emit)
	})
}

// reminderInstant places HH:MM on the wall clock of civil day `period` in loc.
func reminderInstant(period core.Date, hhmm string, loc *time.Location) (time.Time, bool) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(period.Y, period.M, period.D, t.Hour(), t.Minute(), 0, 0, loc), true
}
