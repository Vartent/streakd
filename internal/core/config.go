package core

import (
	"errors"
	"fmt"
	"strings"
)

// Period is the cadence of a streak.
type Period string

const (
	PeriodDay   Period = "day"
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
)

// MaxFreezes is the hard cap on a freeze inventory.
const MaxFreezes = 100

// maxBoundaryOffsetMin bounds how far a "day" boundary may be shifted from
// local midnight (12 hours either way).
const maxBoundaryOffsetMin = 12 * 60

// FreezePolicy configures automatic streak freezes.
type FreezePolicy struct {
	// Initial freezes granted when the streak is created.
	Initial int `json:"initial"`
	// EarnEveryNPeriods grants one freeze per N earned periods. 0 disables earning.
	EarnEveryNPeriods int `json:"earn_every_n_periods"`
	// Max caps the inventory. 0 disables freezes entirely.
	Max int `json:"max"`
	// AutoConsume spends freezes automatically on missed periods.
	AutoConsume bool `json:"auto_consume"`
}

// Config is the immutable behavior of one streak. It is snapshotted onto the
// streak instance at creation time.
type Config struct {
	Period Period `json:"period"`
	// WeekdayMask is seven '0'/'1' characters, Monday first ("1111100" =
	// weekdays only). Empty means every day. Day-period streaks only.
	WeekdayMask string `json:"weekday_mask,omitempty"`
	// BoundaryOffsetMin shifts the period boundary from local midnight,
	// e.g. 180 means the day rolls over at 03:00 local time.
	BoundaryOffsetMin int `json:"boundary_offset_min,omitempty"`
	// MinAmountPerPeriod is the activity amount needed to earn a period (>= 1;
	// 0 is normalized to 1).
	MinAmountPerPeriod int `json:"min_amount_per_period,omitempty"`
	// Target closes the streak as completed at N earned periods. 0 = endless.
	Target     int          `json:"target,omitempty"`
	Freezes    FreezePolicy `json:"freezes"`
	Milestones []int        `json:"milestones,omitempty"`
}

// Normalized returns cfg with defaults filled in.
func (c Config) Normalized() Config {
	if c.Period == "" {
		c.Period = PeriodDay
	}
	if c.MinAmountPerPeriod <= 0 {
		c.MinAmountPerPeriod = 1
	}
	return c
}

// Validate rejects configurations the engine cannot execute coherently.
func (c Config) Validate() error {
	if c.MinAmountPerPeriod < 0 {
		return errors.New("core: min_amount_per_period must be >= 0")
	}
	c = c.Normalized()
	switch c.Period {
	case PeriodDay, PeriodWeek, PeriodMonth:
	default:
		return fmt.Errorf("core: unknown period %q", c.Period)
	}
	if c.WeekdayMask != "" {
		if c.Period != PeriodDay {
			return errors.New("core: weekday_mask is only valid for day periods")
		}
		if len(c.WeekdayMask) != 7 || strings.Trim(c.WeekdayMask, "01") != "" {
			return fmt.Errorf("core: weekday_mask must be seven 0/1 characters, got %q", c.WeekdayMask)
		}
		if !strings.Contains(c.WeekdayMask, "1") {
			return errors.New("core: weekday_mask must include at least one active day")
		}
	}
	if c.BoundaryOffsetMin < -maxBoundaryOffsetMin || c.BoundaryOffsetMin > maxBoundaryOffsetMin {
		return fmt.Errorf("core: boundary_offset_min %d out of range [-720, 720]", c.BoundaryOffsetMin)
	}
	if c.Target < 0 {
		return errors.New("core: target must be >= 0")
	}
	f := c.Freezes
	if f.Initial < 0 || f.EarnEveryNPeriods < 0 || f.Max < 0 {
		return errors.New("core: freeze policy values must be >= 0")
	}
	if f.Max > MaxFreezes {
		return fmt.Errorf("core: freezes.max %d exceeds cap %d", f.Max, MaxFreezes)
	}
	if f.Initial > f.Max {
		return errors.New("core: freezes.initial exceeds freezes.max")
	}
	if f.EarnEveryNPeriods > 0 && f.Max == 0 {
		return errors.New("core: freeze earning enabled with freezes.max = 0")
	}
	prev := 0
	for _, m := range c.Milestones {
		if m <= prev {
			return fmt.Errorf("core: milestones must be strictly ascending and positive, got %v", c.Milestones)
		}
		prev = m
	}
	return nil
}

// scheduledOn reports whether date d is an active (non-rest) day. Week and
// month periods have no rest days.
func (c Config) scheduledOn(d Date) bool {
	if c.Period != PeriodDay || c.WeekdayMask == "" {
		return true
	}
	return c.WeekdayMask[d.ISOWeekday()-1] == '1'
}
