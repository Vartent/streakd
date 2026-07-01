package engine

import (
	"context"

	"github.com/Vartent/streakd/internal/core"
	"github.com/Vartent/streakd/internal/store"
)

// Get returns the derived view of one streak. It never writes: correctness
// does not depend on any settler having run.
func (e *Engine) Get(ctx context.Context, subject, key string) (StreakView, error) {
	now := e.clock()
	subj, err := store.GetSubject(ctx, e.pool, e.appID, subject)
	if err != nil {
		return StreakView{}, translateNotFound(err)
	}
	loc, err := e.loadLocation(subj.Timezone)
	if err != nil {
		return StreakView{}, err
	}
	st, err := store.GetStreak(ctx, e.pool, subj.ID, key)
	if err != nil {
		return StreakView{}, translateNotFound(err)
	}
	amount, err := store.GetMarkAmount(ctx, e.pool, st.ID, core.PeriodKey(now, loc, st.Config))
	if err != nil {
		return StreakView{}, err
	}
	return buildView(st, subj.Timezone, amount, now, loc), nil
}

// List returns derived views of all streaks of a subject.
func (e *Engine) List(ctx context.Context, subject string) ([]StreakView, error) {
	now := e.clock()
	subj, err := store.GetSubject(ctx, e.pool, e.appID, subject)
	if err != nil {
		return nil, translateNotFound(err)
	}
	loc, err := e.loadLocation(subj.Timezone)
	if err != nil {
		return nil, err
	}
	streaks, err := store.ListStreaks(ctx, e.pool, subj.ID)
	if err != nil {
		return nil, err
	}
	views := make([]StreakView, 0, len(streaks))
	for _, st := range streaks {
		amount, err := store.GetMarkAmount(ctx, e.pool, st.ID, core.PeriodKey(now, loc, st.Config))
		if err != nil {
			return nil, err
		}
		views = append(views, buildView(st, subj.Timezone, amount, now, loc))
	}
	return views, nil
}

// CalendarDay is one rendered cell for history UIs.
type CalendarDay struct {
	Period string `json:"period"`
	Amount int    `json:"amount"`
	Earned bool   `json:"earned"`
}

// Calendar returns the mark history between two dates inclusive.
func (e *Engine) Calendar(ctx context.Context, subject, key string, from, to core.Date) ([]CalendarDay, error) {
	subj, err := store.GetSubject(ctx, e.pool, e.appID, subject)
	if err != nil {
		return nil, translateNotFound(err)
	}
	st, err := store.GetStreak(ctx, e.pool, subj.ID, key)
	if err != nil {
		return nil, translateNotFound(err)
	}
	marks, err := store.MarksBetween(ctx, e.pool, st.ID, from, to)
	if err != nil {
		return nil, err
	}
	var days []CalendarDay
	for d := from; !d.After(to); d = d.AddDays(1) {
		amount := marks[d.String()]
		if amount == 0 {
			continue
		}
		days = append(days, CalendarDay{
			Period: d.String(),
			Amount: amount,
			Earned: amount >= st.Config.MinAmountPerPeriod,
		})
	}
	return days, nil
}

// PollEvents returns outbox events with id > after, oldest first.
func (e *Engine) PollEvents(ctx context.Context, after int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := e.pool.Query(ctx, `
		SELECT id, subject_external_id, streak_key, type,
		       payload->>'period', COALESCE((payload->>'count')::int, 0), created_at
		FROM streaks.events WHERE app_id = $1 AND id > $2 ORDER BY id LIMIT $3
	`, e.appID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		var typ string
		if err := rows.Scan(&ev.ID, &ev.Subject, &ev.Key, &typ, &ev.Period, &ev.Count, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.Type = core.EventType(typ)
		out = append(out, ev)
	}
	return out, rows.Err()
}
