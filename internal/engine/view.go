package engine

import (
	"time"

	"github.com/Vartent/streakd/internal/core"
	"github.com/Vartent/streakd/internal/store"
)

// StreakView is the display-ready, always-derived state of one streak. It is
// JSON-stable: clients and idempotency memoization both serialize it.
type StreakView struct {
	Key              string         `json:"key"`
	Count            int            `json:"count"`
	Longest          int            `json:"longest"`
	State            core.Liveness  `json:"state"`
	EarnedThisPeriod bool           `json:"earned_this_period"`
	CurrentPeriod    string         `json:"current_period"`
	AmountThisPeriod int            `json:"amount_this_period"`
	AmountNeeded     int            `json:"amount_needed"`
	LossAt           *time.Time     `json:"loss_at,omitempty"`
	SecondsUntilLoss int64          `json:"seconds_until_loss"`
	Freezes          FreezesView    `json:"freezes"`
	Milestone        *MilestoneView `json:"milestone,omitempty"`
	Target           *TargetView    `json:"target,omitempty"`
	Timezone         string         `json:"timezone"`
}

type FreezesView struct {
	Available int `json:"available"`
	Max       int `json:"max"`
	// Progress / Needed track earning the next freeze (0/0 when disabled).
	Progress int `json:"progress"`
	Needed   int `json:"needed"`
}

type MilestoneView struct {
	Reached int `json:"reached"`
	Next    int `json:"next,omitempty"`
}

type TargetView struct {
	Goal int `json:"goal"`
	Done int `json:"done"`
}

func buildView(st store.Streak, tz string, amountThisPeriod int, now time.Time, loc *time.Location) StreakView {
	cfg := st.Config
	d := core.Derive(st.State, cfg, now, loc)
	v := StreakView{
		Key:              st.Key,
		Count:            d.Count,
		Longest:          d.Longest,
		State:            d.Liveness,
		EarnedThisPeriod: d.EarnedThisPeriod,
		CurrentPeriod:    d.CurrentPeriod.String(),
		AmountThisPeriod: amountThisPeriod,
		AmountNeeded:     cfg.MinAmountPerPeriod,
		Timezone:         tz,
	}
	if !d.LossAt.IsZero() {
		loss := d.LossAt
		v.LossAt = &loss
		if s := int64(loss.Sub(now).Seconds()); s > 0 {
			v.SecondsUntilLoss = s
		}
	}
	v.Freezes = FreezesView{
		Available: d.FreezesAfterSpend,
		Max:       cfg.Freezes.Max,
	}
	if cfg.Freezes.EarnEveryNPeriods > 0 {
		v.Freezes.Progress = d.FreezeProgress
		v.Freezes.Needed = cfg.Freezes.EarnEveryNPeriods
	}
	if len(cfg.Milestones) > 0 {
		m := &MilestoneView{}
		for _, ms := range cfg.Milestones {
			if d.Count >= ms {
				m.Reached = ms
			} else {
				m.Next = ms
				break
			}
		}
		v.Milestone = m
	}
	if cfg.Target > 0 {
		v.Target = &TargetView{Goal: cfg.Target, Done: d.Count}
	}
	return v
}
