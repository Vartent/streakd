package core

import "time"

// PeriodKey maps an instant to the period it belongs to, observed in loc.
// Day periods key by civil date (shifted by the boundary offset), week periods
// by the Monday of the ISO week, month periods by the first of the month.
//
// The boundary offset is applied in wall-clock terms, not real duration: a
// boundary of "03:00 local" must stay 03:00 on the wall even across a DST
// night, so the offset is subtracted from the local clock fields in a
// zone-less frame.
func PeriodKey(t time.Time, loc *time.Location, cfg Config) Date {
	lt := t.In(loc)
	naive := time.Date(lt.Year(), lt.Month(), lt.Day(), lt.Hour(), lt.Minute(), lt.Second(), 0, time.UTC)
	naive = naive.Add(-time.Duration(cfg.BoundaryOffsetMin) * time.Minute)
	d := Date{naive.Year(), naive.Month(), naive.Day()}
	switch cfg.Period {
	case PeriodWeek:
		return d.AddDays(-(d.ISOWeekday() - 1))
	case PeriodMonth:
		return Date{d.Y, d.M, 1}
	default:
		return d
	}
}

// PeriodEnd returns the instant at which period p closes: the shifted local
// midnight that starts the next calendar period. DST gaps and overlaps are
// resolved by the Go time package's wall-clock normalization.
func PeriodEnd(p Date, loc *time.Location, cfg Config) time.Time {
	var next Date
	switch cfg.Period {
	case PeriodWeek:
		next = p.AddDays(7)
	case PeriodMonth:
		t := p.Time().AddDate(0, 1, 0)
		next = Date{t.Year(), t.Month(), t.Day()}
	default:
		next = p.AddDays(1)
	}
	return time.Date(next.Y, next.M, next.D, 0, cfg.BoundaryOffsetMin, 0, 0, loc)
}

// periodRepresentative returns an instant safely inside period p, used when
// replaying ledgers where only the period key is known.
func periodRepresentative(p Date, loc *time.Location, cfg Config) time.Time {
	return time.Date(p.Y, p.M, p.D, 12, cfg.BoundaryOffsetMin, 0, 0, loc)
}

// NextScheduled returns the first active period strictly after p.
func NextScheduled(p Date, cfg Config) Date {
	switch cfg.Period {
	case PeriodWeek:
		return p.AddDays(7)
	case PeriodMonth:
		t := p.Time().AddDate(0, 1, 0)
		return Date{t.Year(), t.Month(), t.Day()}
	default:
		for d := p.AddDays(1); ; d = d.AddDays(1) {
			if cfg.ScheduledOn(d) {
				return d
			}
		}
	}
}

// MissedBetween counts active periods strictly between from and to. Counting
// stops at cap (the caller only ever needs to know whether the count exceeds
// the freeze inventory, so unbounded gaps stay O(cap) for week/month and
// O(days) capped for day periods).
func MissedBetween(from, to Date, cfg Config, cap int) int {
	if from.IsZero() || !from.Before(to) {
		return 0
	}
	n := 0
	for p := NextScheduled(from, cfg); p.Before(to); p = NextScheduled(p, cfg) {
		n++
		if n >= cap {
			return n
		}
	}
	return n
}
