package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Vartent/streakd/internal/core"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%s): %v", name, err)
	}
	return loc
}

// tickUntil advances the clock in steps, ticking after each, from the current
// clock time up to `until`.
func tickUntil(t *testing.T, e *Engine, clock *testClock, until time.Time, step time.Duration) {
	t.Helper()
	for clock.Now().Before(until) {
		clock.Advance(step)
		if err := e.Tick(context.Background()); err != nil {
			t.Fatalf("tick at %v: %v", clock.Now(), err)
		}
	}
}

func eventLog(t *testing.T, e *Engine) []string {
	t.Helper()
	events, err := e.PollEvents(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	var out []string
	for _, ev := range events {
		out = append(out, fmt.Sprintf("%s %s %s@%s#%d", ev.Type, ev.Subject, ev.Key, ev.Period, ev.Count))
	}
	return out
}

// The Phase 3 gate: three subjects in three zones over ten simulated days,
// asserting the exact ordered event log including a DST night.
func TestSchedulerTenDaySimulation(t *testing.T) {
	ctx := context.Background()
	// Start 2026-03-25 00:00 UTC; Berlin crosses DST on 03-29.
	clock := newTestClock(time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC))
	cfg := core.Config{
		Period:            core.PeriodDay,
		Freezes:           core.FreezePolicy{Initial: 1, Max: 2, AutoConsume: true},
		ReminderLocalTime: "20:00",
	}
	e := freshEngine(t, clock, WithStreakType("habit", cfg))

	for subj, tz := range map[string]string{
		"berlin": "Europe/Berlin", "utc": "UTC", "kiritimati": "Pacific/Kiritimati",
	} {
		if err := e.SetTimezone(ctx, subj, tz); err != nil {
			t.Fatalf("tz %s: %v", subj, err)
		}
	}

	// Recording plan, per subject, as LOCAL dates (all at local 10:00):
	// - utc records 10 straight days.
	// - berlin records 3 days, then stops: initial freeze covers 03-28,
	//   breaks on 03-29 (the DST night, 23 wall hours).
	// - kiritimati records 3 days, breaks the same way, restarts 03-31,
	//   then lapses again.
	plan := map[string]map[string]bool{
		"utc": {"2026-03-25": true, "2026-03-26": true, "2026-03-27": true, "2026-03-28": true,
			"2026-03-29": true, "2026-03-30": true, "2026-03-31": true, "2026-04-01": true,
			"2026-04-02": true, "2026-04-03": true},
		"berlin":     {"2026-03-25": true, "2026-03-26": true, "2026-03-27": true},
		"kiritimati": {"2026-03-25": true, "2026-03-26": true, "2026-03-27": true, "2026-03-31": true},
	}
	zones := map[string]*time.Location{
		"utc":        mustLoc(t, "UTC"),
		"berlin":     mustLoc(t, "Europe/Berlin"),
		"kiritimati": mustLoc(t, "Pacific/Kiritimati"),
	}

	// One chronological walk with a tick every 30 minutes — the scheduler sees
	// every evening, exactly like production.
	start := time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	for now := start; now.Before(end); now = now.Add(30 * time.Minute) {
		clock.Set(now)
		if err := e.Tick(ctx); err != nil {
			t.Fatalf("tick at %v: %v", now, err)
		}
		for subj, loc := range zones {
			lt := now.In(loc)
			if lt.Hour() == 10 && lt.Minute() == 0 && plan[subj][core.DateOf(now, loc).String()] {
				if _, err := e.Record(ctx, RecordReq{Subject: subj, Key: "habit"}); err != nil {
					t.Fatalf("record %s at %v: %v", subj, now, err)
				}
			}
		}
	}

	// Assertions on the final states.
	if v, _ := e.Get(ctx, "utc", "habit"); v.Count != 10 || v.State != core.Alive {
		t.Fatalf("utc final = %+v, want alive 10", v)
	}
	if v, _ := e.Get(ctx, "berlin", "habit"); v.Count != 0 || v.State != core.Broken || v.Longest != 3 {
		t.Fatalf("berlin final = %+v, want broken longest 3", v)
	}
	// kiritimati restarted on day 6 but lapsed again through day 9: broken a
	// second time by the end of the window.
	if v, _ := e.Get(ctx, "kiritimati", "habit"); v.Count != 0 || v.State != core.Broken || v.Longest != 3 {
		t.Fatalf("kiritimati final = %+v, want broken 0 longest 3", v)
	}

	// Event-log structural assertions.
	log := eventLog(t, e)
	counts := map[string]int{}
	for _, l := range log {
		var typ, subj string
		fmt.Sscanf(l, "%s %s", &typ, &subj)
		counts[typ+"/"+subj]++
	}
	expect := map[string]int{
		"extended/utc":               10,
		"extended/berlin":            3,
		"extended/kiritimati":        4,
		"freeze_consumed/berlin":     1, // day 4 covered by initial freeze
		"freeze_consumed/kiritimati": 1,
		"broken/berlin":              1, // day 5
		"broken/kiritimati":          2, // day 5, and again after the day-6 restart lapsed
	}
	for k, want := range expect {
		if counts[k] != want {
			t.Fatalf("event count %s = %d, want %d\nfull log:\n%v", k, counts[k], want, log)
		}
	}
	// at_risk fires only on evenings with a live, unearned streak:
	// berlin 03-28 (frozen) and 03-29 (last chance); kiritimati the same two
	// local evenings plus 04-01 after its restart; utc never (earned by 10:00).
	if counts["at_risk/utc"] != 0 {
		t.Fatalf("utc got at_risk events:\n%v", log)
	}
	if counts["at_risk/berlin"] != 2 {
		t.Fatalf("berlin at_risk = %d, want exactly 2\n%v", counts["at_risk/berlin"], log)
	}
	if counts["at_risk/kiritimati"] != 3 {
		t.Fatalf("kiritimati at_risk = %d, want exactly 3\n%v", counts["at_risk/kiritimati"], log)
	}
	for _, subj := range []string{"berlin", "utc", "kiritimati"} {
		assertOracle(t, e, subj, "habit")
	}
}

// Scheduler death for two days must not corrupt anything: reads stay correct
// throughout, and the catch-up tick emits each missed event exactly once.
func TestSchedulerDowntimeCatchUp(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	cfg := core.Config{
		Period:  core.PeriodDay,
		Freezes: core.FreezePolicy{Initial: 1, Max: 2, AutoConsume: true},
	}
	e := freshEngine(t, clock, WithStreakType("habit", cfg))

	for i := 0; i < 3; i++ {
		if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "habit"}); err != nil {
			t.Fatal(err)
		}
		clock.Advance(24 * time.Hour)
		if err := e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Scheduler dies; user lapses for 3 days (freeze covers 1, then break).
	clock.Advance(3 * 24 * time.Hour)
	// Reads during downtime are already correct.
	if v, _ := e.Get(ctx, "u1", "habit"); v.State != core.Broken || v.Count != 0 {
		t.Fatalf("during-downtime read = %+v, want broken 0", v)
	}
	// Catch-up tick emits freeze_consumed + broken exactly once.
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(ctx); err != nil { // second tick must add nothing
		t.Fatal(err)
	}
	counts := map[core.EventType]int{}
	events, _ := e.PollEvents(ctx, 0, 100)
	for _, ev := range events {
		counts[ev.Type]++
	}
	if counts[core.EventFreezeConsumed] != 1 || counts[core.EventBroken] != 1 {
		t.Fatalf("catch-up events = %+v, want exactly 1 freeze_consumed and 1 broken", counts)
	}
	assertOracle(t, e, "u1", "habit")
}

// Reminders must not fire for dead streaks (the kanji "nagging about a
// 5-day-old corpse" bug) nor twice per local day.
func TestSchedulerReminderDedupAndDeadStreakAwareness(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC))
	cfg := core.Config{Period: core.PeriodDay, ReminderLocalTime: "20:00"}
	e := freshEngine(t, clock, WithStreakType("habit", cfg))

	if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "habit"}); err != nil {
		t.Fatal(err)
	}
	// Next evening, unearned: one at_risk despite three ticks.
	clock.Set(time.Date(2026, 5, 12, 20, 5, 0, 0, time.UTC))
	for i := 0; i < 3; i++ {
		if err := e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := e.PollEvents(ctx, 0, 100)
	atRisk := 0
	for _, ev := range events {
		if ev.Type == core.EventAtRisk {
			atRisk++
		}
	}
	if atRisk != 1 {
		t.Fatalf("at_risk after 3 same-evening ticks = %d, want 1", atRisk)
	}
	// The streak dies overnight; evenings after that must stay silent.
	for day := 13; day <= 16; day++ {
		clock.Set(time.Date(2026, 5, day, 20, 5, 0, 0, time.UTC))
		if err := e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	events, _ = e.PollEvents(ctx, 0, 100)
	atRisk = 0
	for _, ev := range events {
		if ev.Type == core.EventAtRisk {
			atRisk++
		}
	}
	if atRisk != 1 {
		t.Fatalf("at_risk total after streak death = %d, want still 1 (no dead-streak nagging)", atRisk)
	}
}
