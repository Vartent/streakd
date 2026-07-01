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

// RecordReq describes one activity report.
type RecordReq struct {
	Subject string
	Key     string
	// At defaults to the engine clock. When set, it must land in the current
	// period (a short grace window covers boundary races for the previous,
	// not-yet-settled period).
	At time.Time
	// Amount defaults to 1; periods earn when the accumulated amount reaches
	// the config threshold.
	Amount int
	// IdempotencyKey memoizes the response: replays return the original
	// result without re-recording.
	IdempotencyKey string
	// Config creates the streak with this config on first activity instead of
	// a registered streak type.
	Config *core.Config
}

// Record reports activity and returns the derived post-activity view.
func (e *Engine) Record(ctx context.Context, req RecordReq) (StreakView, error) {
	now := e.clock()
	at := req.At
	if at.IsZero() {
		at = now
	}
	if at.After(now.Add(time.Minute)) {
		return StreakView{}, fmt.Errorf("%w: activity timestamp in the future", ErrOutsidePeriod)
	}
	amount := req.Amount
	if amount <= 0 {
		amount = 1
	}

	var view StreakView
	err := e.inTx(ctx, func(tx pgx.Tx, emit func(Event)) error {
		subject, err := store.UpsertSubject(ctx, tx, e.appID, req.Subject, e.defaultTZ)
		if err != nil {
			return err
		}
		loc, err := e.loadLocation(subject.Timezone)
		if err != nil {
			return err
		}
		cfg, err := e.configFor(req.Key, req.Config)
		if err != nil {
			return err
		}
		st, err := store.UpsertStreakLocked(ctx, tx, subject.ID, req.Key, cfg)
		if err != nil {
			return err
		}

		if req.IdempotencyKey != "" {
			if memo, found, err := store.LookupIdempotency(ctx, tx, e.appID, req.IdempotencyKey); err != nil {
				return err
			} else if found {
				return json.Unmarshal(memo, &view)
			}
		}

		p := core.PeriodKey(at, loc, st.Config)
		cur := core.PeriodKey(now, loc, st.Config)
		state := st.State
		var events []core.Event

		switch {
		case p == cur:
			// Normal path: resolve elapsed periods, then earn the current one.
			var settleEvents []core.Event
			state, settleEvents = core.Settle(state, st.Config, now, loc)
			events = append(events, settleEvents...)
		case p.Before(cur) && now.Sub(at) <= boundaryGrace && state.SettledThrough.Before(p) && state.LastEarned.Before(p):
			// Boundary race: the activity happened just before a boundary that
			// has since passed. Earn its period first, then settle forward —
			// the same order a ledger replay would use.
		default:
			return fmt.Errorf("%w: period %s, current %s", ErrOutsidePeriod, p, cur)
		}

		total, err := store.AddMark(ctx, tx, st.ID, p, amount, subject.Timezone, at)
		if err != nil {
			return err
		}
		if total >= st.Config.MinAmountPerPeriod {
			var applyEvents []core.Event
			var earned bool
			state, applyEvents, earned = core.Apply(state, st.Config, at, loc)
			if earned {
				events = append(events, applyEvents...)
			}
		}
		if p.Before(cur) {
			// Grace path: settle the boundary we crossed after earning.
			var settleEvents []core.Event
			state, settleEvents = core.Settle(state, st.Config, now, loc)
			events = append(events, settleEvents...)
		}

		if err := store.SaveStreakState(ctx, tx, st.ID, state); err != nil {
			return err
		}
		st.State = state
		if err := persistEvents(ctx, tx, e.appID, st, subject.ExternalID, events, now, emit); err != nil {
			return err
		}

		curAmount := total
		if p != cur {
			if curAmount, err = store.GetMarkAmount(ctx, tx, st.ID, cur); err != nil {
				return err
			}
		}
		view = buildView(st, subject.Timezone, curAmount, now, loc)

		if req.IdempotencyKey != "" {
			memo, err := json.Marshal(view)
			if err != nil {
				return err
			}
			return store.SaveIdempotency(ctx, tx, e.appID, req.IdempotencyKey, st.ID, memo, now)
		}
		return nil
	})
	return view, err
}

// Unrecord removes the current period's activity (toggle semantics) and
// rebuilds state from the ledger so longest/freeze accounting stays exact.
func (e *Engine) Unrecord(ctx context.Context, subject, key string) (StreakView, error) {
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
		cur := core.PeriodKey(now, loc, st.Config)
		if _, err := store.DeleteMark(ctx, tx, st.ID, cur); err != nil {
			return err
		}
		state, err := e.recountLocked(ctx, tx, st, now, loc)
		if err != nil {
			return err
		}
		st.State = state
		view = buildView(st, subj.Timezone, 0, now, loc)
		return nil
	})
	return view, err
}

// recountLocked replays the ledger and persists the result (oracle path).
func (e *Engine) recountLocked(ctx context.Context, tx pgx.Tx, st store.Streak, now time.Time, loc *time.Location) (core.State, error) {
	earned, err := store.EarnedPeriods(ctx, tx, st.ID, st.Config.MinAmountPerPeriod)
	if err != nil {
		return core.State{}, err
	}
	state := core.Replay(st.Config, earned, now, loc)
	if err := store.SaveStreakState(ctx, tx, st.ID, state); err != nil {
		return core.State{}, err
	}
	return state, nil
}

func translateNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
