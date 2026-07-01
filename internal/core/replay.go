package core

import "time"

// Replay reconstructs state from scratch out of the ledger of earned periods
// (ascending, deduplicated). It is the oracle: after any sequence of
// operations, Replay over the period ledger must reproduce the incrementally
// maintained state exactly. Divergence is a bug by definition.
func Replay(cfg Config, earned []Date, now time.Time, loc *time.Location) State {
	cfg = cfg.Normalized()
	s := NewState(cfg)
	for _, p := range earned {
		at := periodRepresentative(p, loc, cfg)
		s, _ = Settle(s, cfg, at, loc)
		s, _, _ = Apply(s, cfg, at, loc)
	}
	s, _ = Settle(s, cfg, now, loc)
	return s
}
