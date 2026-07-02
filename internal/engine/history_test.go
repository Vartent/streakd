package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Vartent/streakd/internal/core"
)

func daysEndingAt(end core.Date, n int) []core.Date {
	out := make([]core.Date, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, end.AddDays(-i))
	}
	return out
}

func TestReplaceHistoryBuildsStateByReplay(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))

	// 20 straight days ending today: replay must grant the 2 earned freezes.
	v, err := e.ReplaceHistory(ctx, "u1", "practice", daysEndingAt(core.MustDate("2026-05-20"), 20))
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if v.Count != 20 || v.State != core.Alive || !v.EarnedThisPeriod || v.Freezes.Available != 2 {
		t.Fatalf("view = %+v, want alive 20 earned with 2 freezes", v)
	}
	assertOracle(t, e, "u1", "practice")

	// Replacing again with an ended-4-days-ago run: reads broken immediately.
	v, err = e.ReplaceHistory(ctx, "u1", "practice", daysEndingAt(core.MustDate("2026-05-16"), 5))
	if err != nil {
		t.Fatalf("replace 2: %v", err)
	}
	if v.Count != 0 || v.State != core.Broken || v.Longest != 5 {
		t.Fatalf("failed-streak view = %+v, want broken 0 longest 5", v)
	}

	// Empty list is a full reset.
	v, err = e.ReplaceHistory(ctx, "u1", "practice", nil)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if v.Count != 0 || v.Longest != 0 || v.State != core.Broken {
		t.Fatalf("reset view = %+v, want zeroed", v)
	}
}

func TestShiftHistorySimulatesRolloverFreezeAndBreak(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))

	if _, err := e.ReplaceHistory(ctx, "u1", "practice", daysEndingAt(core.MustDate("2026-05-20"), 20)); err != nil {
		t.Fatal(err)
	}

	// Day rollover: earned-today becomes earned-yesterday; count survives,
	// flame un-completes, deadline is tonight.
	v, err := e.ShiftHistory(ctx, "u1", "practice", 1)
	if err != nil {
		t.Fatalf("shift 1: %v", err)
	}
	if v.Count != 20 || v.EarnedThisPeriod || v.State != core.Alive || v.SecondsUntilLoss <= 0 {
		t.Fatalf("rollover view = %+v, want alive 20 unearned with deadline", v)
	}

	// One more day: yesterday is now a miss covered by a freeze.
	v, err = e.ShiftHistory(ctx, "u1", "practice", 1)
	if err != nil {
		t.Fatalf("shift 2: %v", err)
	}
	if v.State != core.Frozen || v.Count != 20 || v.Freezes.Available != 1 {
		t.Fatalf("frozen view = %+v, want frozen 20 with 1 freeze left after pending spend", v)
	}

	// Recording now settles the freeze spend (real event) and extends. Day 21
	// also completes the next 7-day earn cycle, so a fresh freeze is granted
	// in the same record: 1 left after the spend + 1 earned = 2.
	v, err = e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if v.Count != 21 || v.State != core.Alive || v.Freezes.Available != 2 || v.Freezes.Progress != 0 {
		t.Fatalf("post-freeze record = %+v, want alive 21 freezes 2 (spend then re-earn)", v)
	}
	events, _ := e.PollEvents(ctx, 0, 1000)
	consumed, earned := 0, 0
	for _, ev := range events {
		switch ev.Type {
		case core.EventFreezeConsumed:
			consumed++
		case core.EventFreezeEarned:
			earned++
		}
	}
	if consumed != 1 || earned != 1 {
		t.Fatalf("freeze events = %d consumed / %d earned, want 1/1", consumed, earned)
	}
	assertOracle(t, e, "u1", "practice")

	// Shift far past the remaining freeze: break, with real broken event on
	// the next settle (scheduler tick).
	if _, err := e.ShiftHistory(ctx, "u1", "practice", 4); err != nil {
		t.Fatal(err)
	}
	v, _ = e.Get(ctx, "u1", "practice")
	if v.State != core.Broken || v.Count != 0 || v.Longest != 21 {
		t.Fatalf("broken view = %+v, want broken 0 longest 21", v)
	}
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	events, _ = e.PollEvents(ctx, 0, 1000)
	broken := 0
	for _, ev := range events {
		if ev.Type == core.EventBroken {
			broken++
		}
	}
	if broken != 1 {
		t.Fatalf("broken events after tick = %d, want 1", broken)
	}
}

func TestSetFreezes(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC))
	e := freshEngine(t, clock, WithStreakType("practice", kanjiLikeConfig()))

	if _, err := e.Record(ctx, RecordReq{Subject: "u1", Key: "practice"}); err != nil {
		t.Fatal(err)
	}
	v, err := e.SetFreezes(ctx, "u1", "practice", 2)
	if err != nil {
		t.Fatalf("set freezes: %v", err)
	}
	if v.Freezes.Available != 2 {
		t.Fatalf("freezes = %d, want 2", v.Freezes.Available)
	}
	if _, err := e.SetFreezes(ctx, "u1", "practice", -1); err == nil {
		t.Fatal("negative freezes must be rejected")
	}
	// Granted freezes actually protect: shift 2 days, still alive-frozen.
	if _, err := e.ShiftHistory(ctx, "u1", "practice", 2); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.Get(ctx, "u1", "practice"); v.State != core.Frozen || v.Count != 1 {
		t.Fatalf("view = %+v, want frozen count 1", v)
	}
}
