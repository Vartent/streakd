package core

import (
	"testing"
	"time"
)

func at(day string) time.Time {
	return MustDate(day).Time().Add(12 * time.Hour) // noon UTC of that date
}

func TestApplyBasicChain(t *testing.T) {
	utc := time.UTC
	cfg := dayCfg()
	s := NewState(cfg)

	s, events, earned := Apply(s, cfg, at("2026-05-11"), utc)
	if !earned || s.CurrentCount != 1 || s.Longest != 1 || s.LastEarned.String() != "2026-05-11" {
		t.Fatalf("first apply = %+v earned=%v", s, earned)
	}
	if len(events) != 1 || events[0].Type != EventExtended || events[0].Count != 1 {
		t.Fatalf("first apply events = %+v", events)
	}
	// Same period again: strict no-op.
	before := s
	s, events, earned = Apply(s, cfg, at("2026-05-11").Add(3*time.Hour), utc)
	if earned || s != before || len(events) != 0 {
		t.Fatalf("same-day apply must not change state: %+v earned=%v events=%v", s, earned, events)
	}
	s, _, _ = Apply(s, cfg, at("2026-05-12"), utc)
	if s.CurrentCount != 2 || s.Longest != 2 {
		t.Fatalf("second day apply = %+v, want count 2", s)
	}
}

func TestApplyRestDayDoesNotEarn(t *testing.T) {
	cfg := dayCfg(func(c *Config) { c.WeekdayMask = "1111100" })
	s := State{CurrentCount: 3, LastEarned: MustDate("2026-05-15")} // Friday
	s2, events, earned := Apply(s, cfg, at("2026-05-16"), time.UTC) // Saturday
	if earned || s2 != s || len(events) != 0 {
		t.Fatalf("rest-day apply must be a no-op, got %+v earned=%v", s2, earned)
	}
}

func TestApplyAfterBreakRestartsAtOne(t *testing.T) {
	cfg := dayCfg()
	// Broken and settled: count zeroed, last earned long ago.
	s := State{CurrentCount: 0, Longest: 9, LastEarned: MustDate("2026-05-01"), SettledThrough: MustDate("2026-05-10")}
	s, events, earned := Apply(s, cfg, at("2026-05-11"), time.UTC)
	if !earned || s.CurrentCount != 1 || s.Longest != 9 {
		t.Fatalf("apply after break = %+v earned=%v, want count 1 longest 9", s, earned)
	}
	if events[0].Count != 1 {
		t.Fatalf("extended event count = %d, want 1", events[0].Count)
	}
}

func TestApplyFreezeEarning(t *testing.T) {
	utc := time.UTC
	cfg := Config{
		Period:  PeriodDay,
		Freezes: FreezePolicy{EarnEveryNPeriods: 3, Max: 2, AutoConsume: true},
	}.Normalized()
	s := NewState(cfg)
	days := []string{"2026-05-11", "2026-05-12", "2026-05-13"}
	var allEvents []Event
	for _, d := range days {
		var ev []Event
		s, ev, _ = Apply(s, cfg, at(d), utc)
		allEvents = append(allEvents, ev...)
	}
	if s.Freezes != 1 || s.FreezeProgress != 0 {
		t.Fatalf("after 3 earns: freezes=%d progress=%d, want 1/0", s.Freezes, s.FreezeProgress)
	}
	var earnedEvents int
	for _, e := range allEvents {
		if e.Type == EventFreezeEarned {
			earnedEvents++
		}
	}
	if earnedEvents != 1 {
		t.Fatalf("freeze_earned events = %d, want 1", earnedEvents)
	}
}

func TestApplyFreezeEarningAtCapHoldsProgress(t *testing.T) {
	utc := time.UTC
	cfg := Config{
		Period:  PeriodDay,
		Freezes: FreezePolicy{EarnEveryNPeriods: 3, Max: 1, AutoConsume: true},
	}.Normalized()
	// At cap with progress about to trigger.
	s := State{CurrentCount: 2, Longest: 2, LastEarned: MustDate("2026-05-12"), Freezes: 1, FreezeProgress: 2}
	s, events, _ := Apply(s, cfg, at("2026-05-13"), utc)
	if s.Freezes != 1 || s.FreezeProgress != 3 {
		t.Fatalf("at-cap apply: freezes=%d progress=%d, want 1/3 (held)", s.Freezes, s.FreezeProgress)
	}
	for _, e := range events {
		if e.Type == EventFreezeEarned {
			t.Fatal("must not emit freeze_earned at cap")
		}
	}
	// Spend the freeze via a missed day, then the very next earn grants one.
	s, sev := Settle(s, cfg, at("2026-05-15"), utc) // 05-14 missed
	if s.Freezes != 0 || len(sev) != 1 || sev[0].Type != EventFreezeConsumed {
		t.Fatalf("settle = %+v events=%v, want freeze consumed", s, sev)
	}
	s, events, _ = Apply(s, cfg, at("2026-05-15"), utc)
	if s.Freezes != 1 || s.FreezeProgress != 0 {
		t.Fatalf("post-spend apply: freezes=%d progress=%d, want immediate regrant 1/0", s.Freezes, s.FreezeProgress)
	}
}

func TestApplyMilestoneAndTarget(t *testing.T) {
	utc := time.UTC
	cfg := Config{Period: PeriodDay, Milestones: []int{2}, Target: 3}.Normalized()
	s := NewState(cfg)
	s, _, _ = Apply(s, cfg, at("2026-05-11"), utc)
	s, ev2, _ := Apply(s, cfg, at("2026-05-12"), utc)
	if len(ev2) != 2 || ev2[1].Type != EventMilestone || ev2[1].Count != 2 {
		t.Fatalf("milestone events = %+v", ev2)
	}
	_, ev3, _ := Apply(s, cfg, at("2026-05-13"), utc)
	if len(ev3) != 2 || ev3[1].Type != EventCompleted || ev3[1].Count != 3 {
		t.Fatalf("target events = %+v", ev3)
	}
}

func TestSettleConsumesFreezesThenBreaks(t *testing.T) {
	utc := time.UTC
	cfg := Config{Period: PeriodDay, Freezes: FreezePolicy{Max: 5, AutoConsume: true}}.Normalized()

	// Two misses, three freezes: both consumed, streak survives.
	s := State{CurrentCount: 5, Longest: 5, LastEarned: MustDate("2026-05-08"), Freezes: 3}
	s, events := Settle(s, cfg, at("2026-05-11"), utc)
	if s.CurrentCount != 5 || s.Freezes != 1 || s.SettledThrough.String() != "2026-05-10" {
		t.Fatalf("settle = %+v, want count kept, 1 freeze, settled through 05-10", s)
	}
	if len(events) != 2 || events[0].Type != EventFreezeConsumed || events[0].Period.String() != "2026-05-09" ||
		events[1].Period.String() != "2026-05-10" {
		t.Fatalf("settle events = %+v", events)
	}
	// Idempotent: same instant again does nothing.
	s2, events := Settle(s, cfg, at("2026-05-11"), utc)
	if s2 != s || len(events) != 0 {
		t.Fatalf("re-settle changed state: %+v events=%v", s2, events)
	}

	// One more missed day with the last freeze gone -> broken.
	s = State{CurrentCount: 5, LastEarned: MustDate("2026-05-08"), FreezeProgress: 2}
	s, events = Settle(s, cfg, at("2026-05-10"), utc)
	if s.CurrentCount != 0 || s.FreezeProgress != 0 {
		t.Fatalf("break settle = %+v, want count and progress zeroed", s)
	}
	if len(events) != 1 || events[0].Type != EventBroken || events[0].Count != 5 {
		t.Fatalf("break events = %+v, want broken carrying lost count 5", events)
	}
}

func TestSettleSkipsRestDays(t *testing.T) {
	cfg := dayCfg(func(c *Config) { c.WeekdayMask = "1111100" })
	// Friday earned, settle on Monday: weekend is not a miss.
	s := State{CurrentCount: 4, LastEarned: MustDate("2026-05-08")}
	s, events := Settle(s, cfg, at("2026-05-11"), time.UTC)
	if s.CurrentCount != 4 || len(events) != 0 {
		t.Fatalf("weekend settle = %+v events=%v, want untouched", s, events)
	}
}

func TestSettleFreshStreakOnlyAdvancesPointer(t *testing.T) {
	cfg := dayCfg()
	s, events := Settle(NewState(cfg), cfg, at("2026-05-11"), time.UTC)
	if s.CurrentCount != 0 || len(events) != 0 || s.SettledThrough.String() != "2026-05-10" {
		t.Fatalf("fresh settle = %+v events=%v", s, events)
	}
}

func TestSettleAgreesWithDerive(t *testing.T) {
	// For a spread of states, what Derive predicts must be exactly what
	// Settle persists.
	utc := time.UTC
	now := at("2026-05-20")
	cfg := Config{Period: PeriodDay, Freezes: FreezePolicy{Max: 5, AutoConsume: true}}.Normalized()
	states := []State{
		{CurrentCount: 3, LastEarned: MustDate("2026-05-19")},
		{CurrentCount: 3, LastEarned: MustDate("2026-05-18"), Freezes: 1},
		{CurrentCount: 3, LastEarned: MustDate("2026-05-15"), Freezes: 2},
		{CurrentCount: 3, LastEarned: MustDate("2026-05-15"), Freezes: 5},
		{CurrentCount: 3, LastEarned: MustDate("2026-05-01"), Freezes: 3},
		{CurrentCount: 0, LastEarned: MustDate("2026-05-01"), SettledThrough: MustDate("2026-05-10")},
	}
	for i, s := range states {
		d := Derive(s, cfg, now, utc)
		settled, _ := Settle(s, cfg, now, utc)
		if settled.CurrentCount != d.Count {
			t.Fatalf("state %d: derive count %d != settled count %d", i, d.Count, settled.CurrentCount)
		}
		if settled.Freezes != d.FreezesAfterSpend {
			t.Fatalf("state %d: derive freezes-after %d != settled freezes %d", i, d.FreezesAfterSpend, settled.Freezes)
		}
		dAfter := Derive(settled, cfg, now, utc)
		if dAfter.Count != d.Count || (dAfter.Liveness == Broken) != (d.Liveness == Broken) {
			t.Fatalf("state %d: derive changed across settle: before %+v after %+v", i, d, dAfter)
		}
	}
}
