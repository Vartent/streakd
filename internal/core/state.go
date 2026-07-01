package core

import "time"

// State is the persisted streak state as of SettledThrough. It never contains
// derived values; reads must go through Derive.
type State struct {
	CurrentCount   int
	Longest        int
	LastEarned     Date // zero = never earned
	SettledThrough Date // last elapsed period the settler has resolved
	Freezes        int
	FreezeProgress int // earned periods since the last freeze grant
}

// Liveness is the derived condition of a streak.
type Liveness string

const (
	// Alive: the chain is intact with no pending misses.
	Alive Liveness = "alive"
	// Frozen: there are elapsed misses that the freeze inventory covers; the
	// chain survives once settled.
	Frozen Liveness = "frozen"
	// Broken: the chain is lost (or was never started); effective count is 0.
	Broken Liveness = "broken"
)

// Derived is the effective, display-ready view of a streak at an instant.
// It is what Get and record responses return; it is never persisted.
type Derived struct {
	Count             int
	Longest           int
	Liveness          Liveness
	EarnedThisPeriod  bool
	CurrentPeriod     Date
	PendingFreezeSpend int
	// FreezesAfterSpend is the inventory once pending misses are settled.
	FreezesAfterSpend int
	// LossAt is the instant the streak breaks if no further activity happens
	// (accounting for remaining freezes and rest days). Zero when Broken.
	LossAt time.Time
}

// Derive computes the effective streak view from persisted state, pure and
// side-effect free. It must agree exactly with what Settle would persist.
func Derive(s State, cfg Config, now time.Time, loc *time.Location) Derived {
	cfg = cfg.Normalized()
	cur := PeriodKey(now, loc, cfg)
	d := Derived{
		Longest:          s.Longest,
		CurrentPeriod:    cur,
		EarnedThisPeriod: !s.LastEarned.IsZero() && s.LastEarned == cur,
	}

	if s.LastEarned.IsZero() || s.CurrentCount == 0 {
		d.Liveness = Broken
		d.FreezesAfterSpend = s.Freezes
		return d
	}

	from := later(s.LastEarned, s.SettledThrough)
	missed := MissedBetween(from, cur, cfg, s.Freezes+1)
	switch {
	case missed == 0:
		d.Liveness = Alive
		d.Count = s.CurrentCount
		d.FreezesAfterSpend = s.Freezes
	case missed <= s.Freezes && cfg.Freezes.AutoConsume:
		d.Liveness = Frozen
		d.Count = s.CurrentCount
		d.PendingFreezeSpend = missed
		d.FreezesAfterSpend = s.Freezes - missed
	default:
		// The gap exceeds the inventory: auto-consume still burns every freeze
		// covering the first misses before the chain snaps (matching Settle).
		d.Liveness = Broken
		if cfg.Freezes.AutoConsume {
			d.PendingFreezeSpend = s.Freezes
			d.FreezesAfterSpend = 0
		} else {
			d.FreezesAfterSpend = s.Freezes
		}
		return d
	}

	d.LossAt = lossAt(d, cfg, loc)
	return d
}

// lossAt finds the boundary after which the streak cannot survive without
// activity: the first unearned active period, advanced by the freezes that
// will remain to cover further misses.
func lossAt(d Derived, cfg Config, loc *time.Location) time.Time {
	p := d.CurrentPeriod
	if d.EarnedThisPeriod || !cfg.scheduledOn(p) {
		p = NextScheduled(p, cfg)
	}
	spare := 0
	if cfg.Freezes.AutoConsume {
		spare = d.FreezesAfterSpend
	}
	for i := 0; i < spare; i++ {
		p = NextScheduled(p, cfg)
	}
	return PeriodEnd(p, loc, cfg)
}
