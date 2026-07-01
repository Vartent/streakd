package core

import (
	"math/rand"
	"testing"
	"time"
)

// The oracle property: replaying the ledger of earned periods from scratch
// must reproduce the incrementally maintained state exactly, for arbitrary
// activity patterns, configs, and timezones. This is the invariant the whole
// engine leans on (engine.Recount), so it gets the heaviest testing.
func TestReplayOracleProperty(t *testing.T) {
	zones := []string{"UTC", "Europe/Berlin", "Pacific/Kiritimati", "Australia/Lord_Howe", "Asia/Kathmandu", "Pacific/Honolulu"}
	masks := []string{"", "1111100", "1010101", "0000011"}

	for seed := int64(0); seed < 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		cfg := Config{
			Period:            PeriodDay,
			WeekdayMask:       masks[rng.Intn(len(masks))],
			BoundaryOffsetMin: []int{0, 0, 180, -120}[rng.Intn(4)],
			Milestones:        []int{3, 7, 30},
			Freezes: FreezePolicy{
				Initial:           rng.Intn(2),
				EarnEveryNPeriods: []int{0, 3, 7}[rng.Intn(3)],
				Max:               2,
				AutoConsume:       rng.Intn(4) != 0,
			},
		}.Normalized()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("seed %d: generated invalid config: %v", seed, err)
		}
		loc := mustLoc(t, zones[rng.Intn(len(zones))])

		// Walk ~90 days; each day 60% chance of activity at a random hour.
		start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		s := NewState(cfg)
		var ledger []Date
		var now time.Time
		for day := 0; day < 90; day++ {
			if rng.Intn(10) >= 6 {
				continue
			}
			now = start.AddDate(0, 0, day).Add(time.Duration(rng.Intn(24)) * time.Hour)
			var earned bool
			s, _ = Settle(s, cfg, now, loc)
			var prev State
			prev = s
			s, _, earned = Apply(s, cfg, now, loc)
			if earned {
				ledger = append(ledger, s.LastEarned)
			} else if s != prev {
				t.Fatalf("seed %d day %d: unearned Apply mutated state: %+v -> %+v", seed, day, prev, s)
			}

			// Invariants on every step.
			if s.CurrentCount < 0 || s.Freezes < 0 || s.Freezes > cfg.Freezes.Max || s.Longest < s.CurrentCount {
				t.Fatalf("seed %d day %d: invariant violated: %+v", seed, day, s)
			}
		}
		if now.IsZero() {
			continue
		}
		final := start.AddDate(0, 0, 95)
		s, _ = Settle(s, cfg, final, loc)

		replayed := Replay(cfg, ledger, final, loc)
		if replayed != s {
			t.Fatalf("seed %d: oracle divergence\n cfg: %+v\n incremental: %+v\n replayed:    %+v\n ledger: %v",
				seed, cfg, s, replayed, ledger)
		}
	}
}

// Derive must be stable across Settle at any point of any random walk: the
// user-visible numbers never change just because the settler ran.
func TestDeriveStableAcrossSettleProperty(t *testing.T) {
	for seed := int64(100); seed < 120; seed++ {
		rng := rand.New(rand.NewSource(seed))
		cfg := Config{
			Period:  PeriodDay,
			Freezes: FreezePolicy{Initial: rng.Intn(3), Max: 3, AutoConsume: rng.Intn(2) == 0},
		}.Normalized()
		loc := mustLoc(t, "Europe/Berlin")
		start := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // spans the DST night
		s := NewState(cfg)
		for day := 0; day < 30; day++ {
			now := start.AddDate(0, 0, day).Add(time.Duration(rng.Intn(24)) * time.Hour)
			before := Derive(s, cfg, now, loc)
			settled, _ := Settle(s, cfg, now, loc)
			after := Derive(settled, cfg, now, loc)
			if before.Count != after.Count || (before.Liveness == Broken) != (after.Liveness == Broken) ||
				before.FreezesAfterSpend != after.FreezesAfterSpend || !before.LossAt.Equal(after.LossAt) {
				t.Fatalf("seed %d day %d: derive unstable across settle:\n before %+v\n after  %+v", seed, day, before, after)
			}
			s = settled
			if rng.Intn(2) == 0 {
				s, _, _ = Apply(s, cfg, now, loc)
			}
		}
	}
}
