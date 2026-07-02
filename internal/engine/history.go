package engine

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Vartent/streakd/internal/core"
	"github.com/Vartent/streakd/internal/store"
)

// Admin / test-harness operations. None of them fake time: they rewrite the
// ledger and let the engine derive real state from the real clock, so day
// rollovers, freeze spends and breaks exercise the exact production paths.

// ReplaceHistory atomically replaces a streak's entire ledger with the given
// earned periods (ascending or not — they are deduplicated by the earn-once
// primary key) and recomputes state by replay. An empty list resets the
// streak to a blank slate. Uses: importing history from a legacy system,
// support corrections, and test setup.
func (e *Engine) ReplaceHistory(ctx context.Context, subject, key string, periods []core.Date) (StreakView, error) {
	now := e.clock()
	var view StreakView
	err := e.inTx(ctx, func(tx pgx.Tx, emit func(Event)) error {
		subj, err := store.UpsertSubject(ctx, tx, e.appID, subject, e.defaultTZ)
		if err != nil {
			return err
		}
		loc, err := e.loadLocation(subj.Timezone)
		if err != nil {
			return err
		}
		cfg, err := e.configFor(key, nil)
		if err != nil {
			return err
		}
		st, err := store.UpsertStreakLocked(ctx, tx, subj.ID, key, cfg)
		if err != nil {
			return err
		}
		if err := store.DeleteAllMarks(ctx, tx, st.ID); err != nil {
			return err
		}
		if err := store.DeleteReminderClaims(ctx, tx, st.ID); err != nil {
			return err
		}
		for _, p := range periods {
			if _, err := store.AddMark(ctx, tx, st.ID, p, st.Config.MinAmountPerPeriod, subj.Timezone, now); err != nil {
				return err
			}
		}
		state, err := e.recountLocked(ctx, tx, st, now, loc)
		if err != nil {
			return err
		}
		st.State = state
		amount, err := store.GetMarkAmount(ctx, tx, st.ID, core.PeriodKey(now, loc, st.Config))
		if err != nil {
			return err
		}
		view = buildView(st, subj.Timezone, amount, now, loc)
		return nil
	})
	return view, err
}

// ShiftHistory moves the whole ledger `days` days into the past (positive =
// older), preserving the freeze inventory and the settle pointer's relative
// position. State is NOT recounted: the next read derives the aged state and
// the next Record/scheduler tick settles it with real events — exactly what
// a test wants to observe. Day-period streaks only.
func (e *Engine) ShiftHistory(ctx context.Context, subject, key string, days int) (StreakView, error) {
	if days <= 0 {
		return StreakView{}, fmt.Errorf("streakd: shift days must be positive, got %d", days)
	}
	now := e.clock()
	var view StreakView
	err := e.inTx(ctx, func(tx pgx.Tx, emit func(Event)) error {
		subj, err := store.GetSubject(ctx, tx, e.appID, subject)
		if err != nil {
			return translateNotFound(err)
		}
		loc, err := e.loadLocation(subj.Timezone)
		if err != nil {
			return err
		}
		st, err := store.GetStreakLocked(ctx, tx, subj.ID, key)
		if err != nil {
			return translateNotFound(err)
		}
		if st.Config.Period != core.PeriodDay {
			return fmt.Errorf("streakd: ShiftHistory supports day periods only, streak has %q", st.Config.Period)
		}
		if err := store.ShiftMarks(ctx, tx, st.ID, days); err != nil {
			return err
		}
		if err := store.DeleteReminderClaims(ctx, tx, st.ID); err != nil {
			return err
		}
		state := st.State
		if !state.LastEarned.IsZero() {
			state.LastEarned = state.LastEarned.AddDays(-days)
		}
		if !state.SettledThrough.IsZero() {
			state.SettledThrough = state.SettledThrough.AddDays(-days)
		}
		if err := store.SaveStreakState(ctx, tx, st.ID, state); err != nil {
			return err
		}
		st.State = state
		amount, err := store.GetMarkAmount(ctx, tx, st.ID, core.PeriodKey(now, loc, st.Config))
		if err != nil {
			return err
		}
		view = buildView(st, subj.Timezone, amount, now, loc)
		return nil
	})
	return view, err
}

// SetFreezes sets the freeze inventory directly (support grants, test setup).
// The value may exceed the config cap; the cap only limits earning.
func (e *Engine) SetFreezes(ctx context.Context, subject, key string, n int) (StreakView, error) {
	if n < 0 || n > core.MaxFreezes {
		return StreakView{}, fmt.Errorf("streakd: freezes must be in [0, %d], got %d", core.MaxFreezes, n)
	}
	now := e.clock()
	var view StreakView
	err := e.inTx(ctx, func(tx pgx.Tx, emit func(Event)) error {
		subj, err := store.GetSubject(ctx, tx, e.appID, subject)
		if err != nil {
			return translateNotFound(err)
		}
		loc, err := e.loadLocation(subj.Timezone)
		if err != nil {
			return err
		}
		st, err := store.GetStreakLocked(ctx, tx, subj.ID, key)
		if err != nil {
			return translateNotFound(err)
		}
		state := st.State
		state.Freezes = n
		if err := store.SaveStreakState(ctx, tx, st.ID, state); err != nil {
			return err
		}
		st.State = state
		amount, err := store.GetMarkAmount(ctx, tx, st.ID, core.PeriodKey(now, loc, st.Config))
		if err != nil {
			return err
		}
		view = buildView(st, subj.Timezone, amount, now, loc)
		return nil
	})
	return view, err
}
