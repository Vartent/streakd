package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Vartent/streakd/internal/core"
	"github.com/Vartent/streakd/internal/store"
)

// SetTimezone moves a subject to a new IANA zone, applying the generosity
// rule to every streak: first all elapsed periods are settled under the OLD
// zone (real misses cost what they cost), then any gap created purely by the
// zone shift itself is forgiven. A timezone change can therefore never break
// a streak; at most it delays the next earnable period.
func (e *Engine) SetTimezone(ctx context.Context, subject, tz string) error {
	newLoc, err := e.loadLocation(tz)
	if err != nil {
		return err
	}
	now := e.clock()
	return e.inTx(ctx, func(tx pgx.Tx, emit func(Event)) error {
		subj, err := store.UpsertSubject(ctx, tx, e.appID, subject, e.defaultTZ)
		if err != nil {
			return err
		}
		if subj.Timezone == tz {
			return nil
		}
		oldLoc, err := e.loadLocation(subj.Timezone)
		if err != nil {
			// A zone the tzdata no longer knows: treat everything as settled
			// under the new zone rather than failing the user forever.
			oldLoc = newLoc
		}
		streaks, err := store.ListStreaks(ctx, tx, subj.ID)
		if err != nil {
			return err
		}
		for _, st := range streaks {
			st, err := store.GetStreakLocked(ctx, tx, subj.ID, st.Key)
			if err != nil {
				return err
			}
			state, events := core.Settle(st.State, st.Config, now, oldLoc)

			// Forgive the shift-created remainder: pull the settle pointer to
			// just before the new-zone current period without spending
			// freezes or breaking.
			curNew := core.PeriodKey(now, newLoc, st.Config)
			if prev := prevCalendarPeriod(curNew, st.Config); state.SettledThrough.Before(prev) {
				state.SettledThrough = prev
			}
			if err := store.SaveStreakState(ctx, tx, st.ID, state); err != nil {
				return err
			}
			if err := persistEvents(ctx, tx, e.appID, st, subj.ExternalID, events, now, emit); err != nil {
				return err
			}
		}
		return store.SetSubjectTimezone(ctx, tx, subj.ID, tz, now)
	})
}

// prevCalendarPeriod mirrors core's settle pointer semantics (mask-agnostic).
func prevCalendarPeriod(p core.Date, cfg core.Config) core.Date {
	switch cfg.Period {
	case core.PeriodWeek:
		return p.AddDays(-7)
	case core.PeriodMonth:
		t := p.Time().AddDate(0, -1, 0)
		return core.Date{Y: t.Year(), M: t.Month(), D: t.Day()}
	default:
		return p.AddDays(-1)
	}
}

// Timezone returns the subject's current zone (default if never set).
func (e *Engine) Timezone(ctx context.Context, subject string) (string, error) {
	subj, err := store.GetSubject(ctx, e.pool, e.appID, subject)
	if err != nil {
		if translateNotFound(err) == ErrNotFound {
			return e.defaultTZ, nil
		}
		return "", err
	}
	return subj.Timezone, nil
}

// Repair restores a streak to its pre-break length. Allowed while the most
// recent break is younger than the repair window; any periods earned since
// the break are kept on top of the restored count.
func (e *Engine) Repair(ctx context.Context, subject, key string) (StreakView, error) {
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

		state, settleEvents := core.Settle(st.State, st.Config, now, loc)
		if err := persistEvents(ctx, tx, e.appID, st, subj.ExternalID, settleEvents, now, emit); err != nil {
			return err
		}

		lostCount, brokenAt, err := latestBreak(ctx, tx, st.ID)
		if err != nil {
			return err
		}
		if lostCount == 0 || now.Sub(brokenAt) > repairWindow {
			return ErrNothingToRepair
		}

		state.CurrentCount += lostCount
		if state.CurrentCount > state.Longest {
			state.Longest = state.CurrentCount
		}
		// Forgive the post-break gap so the restored chain is immediately alive.
		cur := core.PeriodKey(now, loc, st.Config)
		prev := prevCalendarPeriod(cur, st.Config)
		if state.SettledThrough.Before(prev) {
			state.SettledThrough = prev
		}
		if err := store.SaveStreakState(ctx, tx, st.ID, state); err != nil {
			return err
		}
		st.State = state

		repaired := core.Event{Type: core.EventRepaired, Period: cur, Count: state.CurrentCount}
		if err := persistEvents(ctx, tx, e.appID, st, subj.ExternalID, []core.Event{repaired}, now, emit); err != nil {
			return err
		}
		amount, err := store.GetMarkAmount(ctx, tx, st.ID, cur)
		if err != nil {
			return err
		}
		view = buildView(st, subj.Timezone, amount, now, loc)
		return nil
	})
	return view, err
}

// latestBreak finds the most recent break that has not been repaired yet.
func latestBreak(ctx context.Context, q store.Querier, streakID int64) (int, time.Time, error) {
	var payload []byte
	var at time.Time
	var typ string
	err := q.QueryRow(ctx, `
		SELECT type, payload, created_at FROM streaks.events
		WHERE streak_id = $1 AND type IN ('broken', 'repaired')
		ORDER BY id DESC LIMIT 1
	`, streakID).Scan(&typ, &payload, &at)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, time.Time{}, nil
		}
		return 0, time.Time{}, fmt.Errorf("streakd: latest break: %w", err)
	}
	if typ != "broken" {
		return 0, time.Time{}, nil // most recent marker is a repair; nothing to repair again
	}
	var p struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return 0, time.Time{}, err
	}
	return p.Count, at, nil
}

// SetReminder sets (or clears, with empty string) the local-time at-risk
// reminder for one streak, e.g. "20:30".
func (e *Engine) SetReminder(ctx context.Context, subject, key, localTime string) error {
	if localTime != "" {
		if _, err := time.Parse("15:04", localTime); err != nil {
			return fmt.Errorf("streakd: reminder time must be HH:MM, got %q", localTime)
		}
	}
	return e.inTx(ctx, func(tx pgx.Tx, emit func(Event)) error {
		subj, err := store.GetSubject(ctx, tx, e.appID, subject)
		if err != nil {
			return translateNotFound(err)
		}
		st, err := store.GetStreakLocked(ctx, tx, subj.ID, key)
		if err != nil {
			return translateNotFound(err)
		}
		cfg := st.Config
		cfg.ReminderLocalTime = localTime
		return store.UpdateStreakConfig(ctx, tx, st.ID, cfg)
	})
}

// Recount rebuilds state from the ledger (the oracle made operational).
func (e *Engine) Recount(ctx context.Context, subject, key string) (StreakView, error) {
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
