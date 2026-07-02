package core

import "time"

// EventType enumerates state-transition events. Engine-level events (at_risk,
// repaired) share this type but are not produced by core transitions.
type EventType string

const (
	EventExtended       EventType = "extended"
	EventFreezeEarned   EventType = "freeze_earned"
	EventFreezeConsumed EventType = "freeze_consumed"
	EventBroken         EventType = "broken"
	EventMilestone      EventType = "milestone"
	EventCompleted      EventType = "completed"
	EventRepaired       EventType = "repaired"
	EventAtRisk         EventType = "at_risk"
)

// Event describes one transition. Count carries the streak count after the
// event, except for EventBroken where it carries the count that was lost.
type Event struct {
	Type   EventType
	Period Date
	Count  int
}

// Settle resolves every fully elapsed active period after
// max(LastEarned, SettledThrough) and before the current period: each miss
// consumes a freeze or breaks the streak. Settle is idempotent — calling it
// twice at the same instant is a no-op the second time.
func Settle(s State, cfg Config, now time.Time, loc *time.Location) (State, []Event) {
	cfg = cfg.Normalized()
	cur := PeriodKey(now, loc, cfg)
	from := later(s.LastEarned, s.SettledThrough)
	var events []Event

	if s.LastEarned.IsZero() {
		// Nothing ever earned; just advance the settle pointer.
		s.SettledThrough = prevPeriod(cur, cfg)
		return s, events
	}

	for p := NextScheduled(from, cfg); p.Before(cur); p = NextScheduled(p, cfg) {
		if s.CurrentCount == 0 {
			break // already broken; nothing left to spend or lose
		}
		if cfg.Freezes.AutoConsume && s.Freezes > 0 {
			s.Freezes--
			events = append(events, Event{Type: EventFreezeConsumed, Period: p, Count: s.CurrentCount})
			continue
		}
		events = append(events, Event{Type: EventBroken, Period: p, Count: s.CurrentCount})
		s.CurrentCount = 0
		s.FreezeProgress = 0
	}
	s.SettledThrough = later(s.SettledThrough, prevPeriod(cur, cfg))
	return s, events
}

// prevPeriod returns the calendar period immediately before p (mask-agnostic:
// the settle pointer tracks elapsed calendar periods, not active ones).
func prevPeriod(p Date, cfg Config) Date {
	switch cfg.Period {
	case PeriodWeek:
		return p.AddDays(-7)
	case PeriodMonth:
		t := p.Time().AddDate(0, -1, 0)
		return Date{t.Year(), t.Month(), t.Day()}
	default:
		return p.AddDays(-1)
	}
}

// Apply earns the period containing `at`. The caller must Settle first (in
// the same transaction); Apply assumes all prior periods are resolved.
// It returns earned=false — with state unchanged — when the period was
// already earned or falls on a rest day.
func Apply(s State, cfg Config, at time.Time, loc *time.Location) (State, []Event, bool) {
	cfg = cfg.Normalized()
	p := PeriodKey(at, loc, cfg)
	if !cfg.ScheduledOn(p) {
		return s, nil, false
	}
	if !s.LastEarned.IsZero() && !s.LastEarned.Before(p) {
		return s, nil, false
	}

	var events []Event
	s.CurrentCount++
	s.LastEarned = p
	if s.CurrentCount > s.Longest {
		s.Longest = s.CurrentCount
	}
	events = append(events, Event{Type: EventExtended, Period: p, Count: s.CurrentCount})

	if n := cfg.Freezes.EarnEveryNPeriods; n > 0 {
		s.FreezeProgress++
		if s.FreezeProgress >= n {
			if s.Freezes < cfg.Freezes.Max {
				s.Freezes++
				s.FreezeProgress = 0
				events = append(events, Event{Type: EventFreezeEarned, Period: p, Count: s.CurrentCount})
			} else {
				// At cap: hold progress so the next earn after a spend grants
				// immediately.
				s.FreezeProgress = n
			}
		}
	}

	for _, m := range cfg.Milestones {
		if s.CurrentCount == m {
			events = append(events, Event{Type: EventMilestone, Period: p, Count: s.CurrentCount})
		}
	}
	if cfg.Target > 0 && s.CurrentCount == cfg.Target {
		events = append(events, Event{Type: EventCompleted, Period: p, Count: s.CurrentCount})
	}
	return s, events, true
}

// NewState returns the starting state for a fresh streak under cfg.
func NewState(cfg Config) State {
	cfg = cfg.Normalized()
	return State{Freezes: cfg.Freezes.Initial}
}
