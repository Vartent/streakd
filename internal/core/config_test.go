package core

import "testing"

func validConfig() Config {
	return Config{
		Period:  PeriodDay,
		Freezes: FreezePolicy{Initial: 0, EarnEveryNPeriods: 7, Max: 3, AutoConsume: true},
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		ok     bool
	}{
		{"default day config", func(c *Config) {}, true},
		{"empty period defaults to day", func(c *Config) { c.Period = "" }, true},
		{"week period", func(c *Config) { c.Period = PeriodWeek }, true},
		{"month period", func(c *Config) { c.Period = PeriodMonth }, true},
		{"unknown period", func(c *Config) { c.Period = "fortnight" }, false},
		{"weekday mask ok", func(c *Config) { c.WeekdayMask = "1111100" }, true},
		{"mask on week period", func(c *Config) { c.Period = PeriodWeek; c.WeekdayMask = "1111100" }, false},
		{"mask wrong length", func(c *Config) { c.WeekdayMask = "11111" }, false},
		{"mask bad chars", func(c *Config) { c.WeekdayMask = "11111x0" }, false},
		{"mask all rest days", func(c *Config) { c.WeekdayMask = "0000000" }, false},
		{"offset upper bound", func(c *Config) { c.BoundaryOffsetMin = 720 }, true},
		{"offset lower bound", func(c *Config) { c.BoundaryOffsetMin = -720 }, true},
		{"offset too large", func(c *Config) { c.BoundaryOffsetMin = 721 }, false},
		{"offset too small", func(c *Config) { c.BoundaryOffsetMin = -721 }, false},
		{"negative threshold", func(c *Config) { c.MinAmountPerPeriod = -1 }, false},
		{"threshold ok", func(c *Config) { c.MinAmountPerPeriod = 25 }, true},
		{"negative target", func(c *Config) { c.Target = -1 }, false},
		{"finite target", func(c *Config) { c.Target = 100 }, true},
		{"negative freezes", func(c *Config) { c.Freezes.Initial = -1 }, false},
		{"freeze max over cap", func(c *Config) { c.Freezes.Max = 101 }, false},
		{"initial over max", func(c *Config) { c.Freezes.Initial = 4 }, false},
		{"earning with zero max", func(c *Config) { c.Freezes.Max = 0 }, false},
		{"freezes fully disabled", func(c *Config) { c.Freezes = FreezePolicy{} }, true},
		{"milestones ascending", func(c *Config) { c.Milestones = []int{7, 30, 100} }, true},
		{"milestones unsorted", func(c *Config) { c.Milestones = []int{30, 7} }, false},
		{"milestones duplicate", func(c *Config) { c.Milestones = []int{7, 7} }, false},
		{"milestone zero", func(c *Config) { c.Milestones = []int{0, 7} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(&c)
			err := c.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want ok", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("Validate() passed, want error (config %+v)", c)
			}
		})
	}
}

func TestNormalizedDefaults(t *testing.T) {
	c := Config{}.Normalized()
	if c.Period != PeriodDay || c.MinAmountPerPeriod != 1 {
		t.Fatalf("Normalized() = %+v, want day period and threshold 1", c)
	}
}
