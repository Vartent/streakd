package core

import (
	"testing"
	"time"
)

func freezeCfg(available int) Config {
	return Config{
		Period:  PeriodDay,
		Freezes: FreezePolicy{Max: 5, AutoConsume: true},
	}.Normalized()
}

func TestDerive(t *testing.T) {
	utc := time.UTC
	now := time.Date(2026, 5, 11, 15, 0, 0, 0, time.UTC) // Monday
	cases := []struct {
		name         string
		state        State
		cfg          Config
		wantCount    int
		wantLiveness Liveness
		wantEarned   bool
		wantPending  int
		wantLeft     int
	}{
		{
			name:         "fresh streak is broken with count 0",
			state:        NewState(dayCfg()),
			cfg:          dayCfg(),
			wantLiveness: Broken,
		},
		{
			name:         "earned today is alive",
			state:        State{CurrentCount: 4, Longest: 6, LastEarned: MustDate("2026-05-11")},
			cfg:          dayCfg(),
			wantCount:    4, wantLiveness: Alive, wantEarned: true,
		},
		{
			name:         "earned yesterday is alive but unearned",
			state:        State{CurrentCount: 4, LastEarned: MustDate("2026-05-10")},
			cfg:          dayCfg(),
			wantCount:    4, wantLiveness: Alive,
		},
		{
			name:         "one miss with freeze is frozen keeping count",
			state:        State{CurrentCount: 4, LastEarned: MustDate("2026-05-09"), Freezes: 2},
			cfg:          freezeCfg(2),
			wantCount:    4, wantLiveness: Frozen, wantPending: 1, wantLeft: 1,
		},
		{
			name:         "two misses covered by exactly two freezes",
			state:        State{CurrentCount: 4, LastEarned: MustDate("2026-05-08"), Freezes: 2},
			cfg:          freezeCfg(2),
			wantCount:    4, wantLiveness: Frozen, wantPending: 2, wantLeft: 0,
		},
		{
			name:         "misses beyond freezes break the streak and burn the inventory",
			state:        State{CurrentCount: 4, LastEarned: MustDate("2026-05-08"), Freezes: 1},
			cfg:          freezeCfg(1),
			wantLiveness: Broken, wantPending: 1, wantLeft: 0,
		},
		{
			// The user-visible kanji bug: a dead streak must read as 0, not
			// keep showing the stale stored count.
			name:         "lapsed streak without freezes reads zero",
			state:        State{CurrentCount: 9, LastEarned: MustDate("2026-05-04")},
			cfg:          dayCfg(),
			wantLiveness: Broken,
		},
		{
			name:         "freezes without auto-consume do not save the streak",
			state:        State{CurrentCount: 4, LastEarned: MustDate("2026-05-09"), Freezes: 2},
			cfg:          Config{Period: PeriodDay, Freezes: FreezePolicy{Max: 5}}.Normalized(),
			wantLiveness: Broken, wantLeft: 2,
		},
		{
			name: "settled pointer prevents double counting misses",
			// Settle already consumed a freeze for 05-09; derive must not
			// charge it again.
			state:        State{CurrentCount: 4, LastEarned: MustDate("2026-05-09"), SettledThrough: MustDate("2026-05-10"), Freezes: 1},
			cfg:          freezeCfg(1),
			wantCount:    4, wantLiveness: Alive, wantLeft: 1,
		},
		{
			name: "weekend gap is not a miss under weekday mask",
			// Friday 05-08 -> Monday 05-11, mask Mon-Fri.
			state:        State{CurrentCount: 4, LastEarned: MustDate("2026-05-08")},
			cfg:          dayCfg(func(c *Config) { c.WeekdayMask = "1111100" }),
			wantCount:    4, wantLiveness: Alive,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Derive(tc.state, tc.cfg, now, utc)
			if d.Count != tc.wantCount || d.Liveness != tc.wantLiveness || d.EarnedThisPeriod != tc.wantEarned ||
				d.PendingFreezeSpend != tc.wantPending || d.FreezesAfterSpend != tc.wantLeft {
				t.Fatalf("Derive = %+v, want count=%d liveness=%s earned=%v pending=%d left=%d",
					d, tc.wantCount, tc.wantLiveness, tc.wantEarned, tc.wantPending, tc.wantLeft)
			}
			if tc.wantLiveness == Broken && !d.LossAt.IsZero() {
				t.Fatalf("broken streak must have zero LossAt, got %v", d.LossAt)
			}
			if tc.wantLiveness != Broken && d.LossAt.IsZero() {
				t.Fatal("live streak must have a LossAt deadline")
			}
		})
	}
}

func TestDeriveLossAt(t *testing.T) {
	utc := time.UTC
	now := time.Date(2026, 5, 11, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		state State
		cfg   Config
		want  time.Time
	}{
		{
			"unearned today, no freezes: dies at midnight",
			State{CurrentCount: 3, LastEarned: MustDate("2026-05-10")},
			dayCfg(),
			time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		},
		{
			"earned today, no freezes: dies end of tomorrow",
			State{CurrentCount: 3, LastEarned: MustDate("2026-05-11")},
			dayCfg(),
			time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		},
		{
			"earned today, two freezes buy two more days",
			State{CurrentCount: 3, LastEarned: MustDate("2026-05-11"), Freezes: 2},
			freezeCfg(2),
			time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			"frozen: remaining freeze extends the deadline",
			State{CurrentCount: 3, LastEarned: MustDate("2026-05-09"), Freezes: 2},
			freezeCfg(2),
			// 05-10 missed (1 freeze pending), today unearned, 1 freeze left
			// covers today -> dies end of 05-12.
			time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		},
		{
			"weekday mask: Friday earned survives weekend until Monday midnight",
			State{CurrentCount: 3, LastEarned: MustDate("2026-05-08")},
			dayCfg(func(c *Config) { c.WeekdayMask = "1111100" }),
			time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Derive(tc.state, tc.cfg, now, utc)
			if !d.LossAt.Equal(tc.want) {
				t.Fatalf("LossAt = %v, want %v", d.LossAt, tc.want)
			}
		})
	}
}

// Ported from kanji_cards streak_timezone_test.go: travel east across the date
// line makes the next session the first activity of a NEW local day.
func TestDeriveTravelEastEarnsNewLocalDay(t *testing.T) {
	loc := mustLoc(t, "Pacific/Kiritimati")
	now := time.Date(2026, 5, 11, 13, 0, 0, 0, time.UTC) // local 2026-05-12 03:00
	s := State{CurrentCount: 4, Longest: 4, LastEarned: MustDate("2026-05-11")}

	d := Derive(s, dayCfg(), now, loc)
	if d.Liveness != Alive || d.Count != 4 || d.EarnedThisPeriod {
		t.Fatalf("pre-practice derive = %+v, want alive count 4 unearned", d)
	}
	s2, events, earned := Apply(s, dayCfg(), now, loc)
	if !earned || s2.CurrentCount != 5 || s2.LastEarned.String() != "2026-05-12" {
		t.Fatalf("apply = %+v earned=%v, want count 5 on 2026-05-12", s2, earned)
	}
	if len(events) != 1 || events[0].Type != EventExtended {
		t.Fatalf("events = %+v, want single extended", events)
	}
}

// Ported from kanji_cards: travel west makes the same UTC instant the PREVIOUS
// (already earned) local day — recording again must not double count.
func TestDeriveTravelWestDoesNotDoubleCount(t *testing.T) {
	loc := mustLoc(t, "Pacific/Honolulu")
	now := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC) // local 2026-05-10 23:00
	s := State{CurrentCount: 4, Longest: 4, LastEarned: MustDate("2026-05-10")}

	d := Derive(s, dayCfg(), now, loc)
	if d.Liveness != Alive || d.Count != 4 || !d.EarnedThisPeriod {
		t.Fatalf("derive = %+v, want alive count 4 earned", d)
	}
	s2, events, earned := Apply(s, dayCfg(), now, loc)
	if earned || s2.CurrentCount != 4 || len(events) != 0 {
		t.Fatalf("apply = %+v earned=%v events=%v, want unchanged no-op", s2, earned, events)
	}
}

func TestDeriveWeekAndMonthPeriods(t *testing.T) {
	utc := time.UTC
	week := Config{Period: PeriodWeek}.Normalized()
	// Earned the week of Dec 29; now Jan 7 (week of Jan 5) -> alive, unearned.
	s := State{CurrentCount: 3, LastEarned: MustDate("2025-12-29")}
	d := Derive(s, week, time.Date(2026, 1, 7, 12, 0, 0, 0, time.UTC), utc)
	if d.Liveness != Alive || d.Count != 3 || d.EarnedThisPeriod {
		t.Fatalf("week derive = %+v, want alive 3 unearned", d)
	}
	// Skipping a whole week kills it.
	d = Derive(s, week, time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC), utc)
	if d.Liveness != Broken {
		t.Fatalf("week derive after gap = %+v, want broken", d)
	}
	month := Config{Period: PeriodMonth}.Normalized()
	s = State{CurrentCount: 2, LastEarned: MustDate("2025-12-01")}
	d = Derive(s, month, time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), utc)
	if d.Liveness != Alive || d.Count != 2 {
		t.Fatalf("month derive = %+v, want alive 2", d)
	}
}

// DST nights: a streak earned the day before a transition survives through it,
// and the wall-clock day key never double-earns on the 25-hour fall-back day.
func TestDeriveAcrossDSTNights(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	cfg := dayCfg()
	// Spring forward: earned 03-28; 03-29 12:00 local (23-hour day) -> alive.
	s := State{CurrentCount: 7, LastEarned: MustDate("2026-03-28")}
	d := Derive(s, cfg, time.Date(2026, 3, 29, 10, 0, 0, 0, time.UTC), berlin)
	if d.Liveness != Alive || d.Count != 7 {
		t.Fatalf("spring forward derive = %+v, want alive 7", d)
	}
	// Fall back: earn during the first 02:30, try again during the second 02:30.
	s = State{CurrentCount: 1, LastEarned: Date{}}
	first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC) // 02:30 CEST
	s2, _, earned := Apply(NewState(cfg), cfg, first, berlin)
	if !earned || s2.LastEarned.String() != "2026-10-25" {
		t.Fatalf("first ambiguous-hour apply = %+v earned=%v", s2, earned)
	}
	second := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC) // 02:30 CET, same wall time again
	s3, _, earned := Apply(s2, cfg, second, berlin)
	if earned || s3.CurrentCount != 1 {
		t.Fatalf("second ambiguous-hour apply must be a no-op, got %+v earned=%v", s3, earned)
	}
}
