// Package core implements the pure streak semantics: period math, derivation,
// and state transitions. It has no dependencies on storage, transport, or the
// system clock; every function takes time and location explicitly.
package core

import (
	"fmt"
	"time"
)

// Date is a civil calendar date (no time, no zone). The zero value means
// "no date" and is used for "never earned".
type Date struct {
	Y int
	M time.Month
	D int
}

// DateOf returns the civil date of instant t observed in loc.
func DateOf(t time.Time, loc *time.Location) Date {
	y, m, d := t.In(loc).Date()
	return Date{y, m, d}
}

// ParseDate parses YYYY-MM-DD.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return Date{}, fmt.Errorf("core: bad date %q: %w", s, err)
	}
	return DateOf(t, time.UTC), nil
}

// MustDate is a test/config helper; panics on malformed input.
func MustDate(s string) Date {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func (d Date) IsZero() bool { return d == Date{} }

// Time returns the date at midnight UTC (canonical representation for
// arithmetic and DB DATE round-tripping).
func (d Date) Time() time.Time {
	return time.Date(d.Y, d.M, d.D, 0, 0, 0, 0, time.UTC)
}

func (d Date) AddDays(n int) Date {
	t := d.Time().AddDate(0, 0, n)
	return Date{t.Year(), t.Month(), t.Day()}
}

func (d Date) Before(o Date) bool { return d.Time().Before(o.Time()) }
func (d Date) After(o Date) bool  { return o.Before(d) }

// DaysUntil returns o - d in whole days (negative if o is before d).
func (d Date) DaysUntil(o Date) int {
	return int(o.Time().Sub(d.Time()) / (24 * time.Hour))
}

func (d Date) Weekday() time.Weekday { return d.Time().Weekday() }

// ISOWeekday returns 1 for Monday through 7 for Sunday.
func (d Date) ISOWeekday() int {
	wd := int(d.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Y, int(d.M), d.D)
}

// later returns the later of two dates; zero values lose.
func later(a, b Date) Date {
	if a.Before(b) {
		return b
	}
	return a
}
