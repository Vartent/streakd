package core

import (
	"testing"
	"time"
)

func dayCfg(mutate ...func(*Config)) Config {
	c := Config{Period: PeriodDay}
	for _, m := range mutate {
		m(&c)
	}
	return c.Normalized()
}

func TestPeriodKeyDay(t *testing.T) {
	cases := []struct {
		name   string
		utc    time.Time
		zone   string
		offset int
		want   string
	}{
		{"plain utc", time.Date(2026, 5, 11, 23, 59, 0, 0, time.UTC), "UTC", 0, "2026-05-11"},
		{"kiritimati next day", time.Date(2026, 5, 11, 13, 0, 0, 0, time.UTC), "Pacific/Kiritimati", 0, "2026-05-12"},
		{"honolulu previous day", time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC), "Pacific/Honolulu", 0, "2026-05-10"},
		// Berlin DST spring-forward night (2026-03-29 02:00 -> 03:00 CEST).
		// 01:30 UTC = 03:30 CEST, wall clock past the 03:00 boundary, so with a
		// 3h boundary offset it belongs to the NEW day despite only 2.5 real
		// hours since midnight.
		{"berlin gap with 3h boundary", time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC), "Europe/Berlin", 180, "2026-03-29"},
		// 00:30 UTC = 01:30 CET, before the 03:00 wall boundary -> previous day.
		{"berlin before 3h boundary", time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC), "Europe/Berlin", 180, "2026-03-28"},
		// Fall-back night (2026-10-25 03:00 CEST -> 02:00 CET): both instants
		// showing 02:30 on the wall stay before a 3h boundary -> previous day.
		{"berlin ambiguous first pass", time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), "Europe/Berlin", 180, "2026-10-24"},
		{"berlin ambiguous second pass", time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC), "Europe/Berlin", 180, "2026-10-24"},
		// Night-owl semantics: 02:30 local with a 3h offset counts as yesterday.
		{"night owl before boundary", time.Date(2026, 5, 12, 2, 30, 0, 0, time.UTC), "UTC", 180, "2026-05-11"},
		{"night owl after boundary", time.Date(2026, 5, 12, 3, 0, 0, 0, time.UTC), "UTC", 180, "2026-05-12"},
		// Negative offset: the day starts at 22:00 the previous evening.
		{"negative offset evening rollover", time.Date(2026, 5, 11, 23, 0, 0, 0, time.UTC), "UTC", -120, "2026-05-12"},
		{"lord howe half-hour DST", time.Date(2026, 1, 15, 13, 45, 0, 0, time.UTC), "Australia/Lord_Howe", 0, "2026-01-16"},
		{"kathmandu 0545", time.Date(2026, 5, 11, 18, 20, 0, 0, time.UTC), "Asia/Kathmandu", 0, "2026-05-12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dayCfg(func(c *Config) { c.BoundaryOffsetMin = tc.offset })
			got := PeriodKey(tc.utc, mustLoc(t, tc.zone), cfg)
			if got.String() != tc.want {
				t.Fatalf("PeriodKey = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPeriodKeyWeekAndMonth(t *testing.T) {
	utc := time.UTC
	week := Config{Period: PeriodWeek}.Normalized()
	// 2026-01-01 is a Thursday; its ISO week starts Monday 2025-12-29.
	if got := PeriodKey(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), utc, week); got.String() != "2025-12-29" {
		t.Fatalf("week key across new year = %s, want 2025-12-29", got)
	}
	if got := PeriodKey(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), utc, week); got.String() != "2026-01-05" {
		t.Fatalf("week key on Monday = %s, want 2026-01-05", got)
	}
	month := Config{Period: PeriodMonth}.Normalized()
	if got := PeriodKey(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), utc, month); got.String() != "2026-01-01" {
		t.Fatalf("month key = %s, want 2026-01-01", got)
	}
	// With a 3h boundary offset, Jan 1st 01:00 still belongs to December.
	monthOff := Config{Period: PeriodMonth, BoundaryOffsetMin: 180}.Normalized()
	if got := PeriodKey(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC), utc, monthOff); got.String() != "2025-12-01" {
		t.Fatalf("month key with offset = %s, want 2025-12-01", got)
	}
}

func TestPeriodEnd(t *testing.T) {
	utc := time.UTC
	if got := PeriodEnd(MustDate("2026-05-11"), utc, dayCfg()); !got.Equal(time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("day end = %v", got)
	}
	if got := PeriodEnd(MustDate("2026-05-11"), utc, dayCfg(func(c *Config) { c.BoundaryOffsetMin = 180 })); !got.Equal(time.Date(2026, 5, 12, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("offset day end = %v", got)
	}
	// The period boundary sits on the wall clock: Berlin day 2026-03-28 ends at
	// wall midnight even though that night is one real hour short.
	berlin := mustLoc(t, "Europe/Berlin")
	if got := PeriodEnd(MustDate("2026-03-28"), berlin, dayCfg()); !got.Equal(time.Date(2026, 3, 29, 0, 0, 0, 0, berlin)) {
		t.Fatalf("berlin DST-night day end = %v", got)
	}
	week := Config{Period: PeriodWeek}.Normalized()
	if got := PeriodEnd(MustDate("2025-12-29"), utc, week); !got.Equal(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("week end = %v", got)
	}
	month := Config{Period: PeriodMonth}.Normalized()
	if got := PeriodEnd(MustDate("2026-01-01"), utc, month); !got.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("month end = %v", got)
	}
}

func TestPeriodKeyRoundTripsPeriodEnd(t *testing.T) {
	// The instant a period ends is the first instant of the next period.
	zones := []string{"UTC", "Europe/Berlin", "Pacific/Kiritimati", "Australia/Lord_Howe", "Asia/Kathmandu"}
	dates := []string{"2026-03-28", "2026-03-29", "2026-10-24", "2026-10-25", "2028-02-28", "2026-12-31"}
	for _, z := range zones {
		loc := mustLoc(t, z)
		for _, offset := range []int{0, 180, -120} {
			cfg := dayCfg(func(c *Config) { c.BoundaryOffsetMin = offset })
			for _, ds := range dates {
				p := MustDate(ds)
				end := PeriodEnd(p, loc, cfg)
				if got := PeriodKey(end, loc, cfg); got != p.AddDays(1) {
					t.Fatalf("%s offset %d: PeriodKey(end of %s) = %s, want %s", z, offset, p, got, p.AddDays(1))
				}
				if got := PeriodKey(end.Add(-time.Second), loc, cfg); got != p {
					t.Fatalf("%s offset %d: PeriodKey(just before end of %s) = %s, want %s", z, offset, p, got, p)
				}
			}
		}
	}
}

func TestScheduleWithWeekdayMask(t *testing.T) {
	// Mon-Fri mask. 2026-05-11 is a Monday.
	cfg := dayCfg(func(c *Config) { c.WeekdayMask = "1111100" })
	fri := MustDate("2026-05-15")
	if got := NextScheduled(fri, cfg); got.String() != "2026-05-18" {
		t.Fatalf("NextScheduled(Fri) = %s, want Monday 2026-05-18", got)
	}
	if got := MissedBetween(fri, MustDate("2026-05-18"), cfg, 10); got != 0 {
		t.Fatalf("weekend gap missed = %d, want 0", got)
	}
	// Thu -> Tue skips Fri and Mon (2 active periods missed).
	if got := MissedBetween(MustDate("2026-05-14"), MustDate("2026-05-19"), cfg, 10); got != 2 {
		t.Fatalf("Thu->Tue missed = %d, want 2", got)
	}
	if got := MissedBetween(MustDate("2026-05-11"), MustDate("2026-05-12"), dayCfg(), 10); got != 0 {
		t.Fatalf("consecutive days missed = %d, want 0", got)
	}
	if got := MissedBetween(MustDate("2026-05-11"), MustDate("2026-06-11"), dayCfg(), 5); got != 5 {
		t.Fatalf("capped missed = %d, want cap 5", got)
	}
	if got := MissedBetween(Date{}, MustDate("2026-05-12"), dayCfg(), 10); got != 0 {
		t.Fatalf("missed from zero date = %d, want 0", got)
	}
}
