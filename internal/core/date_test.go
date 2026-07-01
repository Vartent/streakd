package core

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%s): %v", name, err)
	}
	return loc
}

func TestDateOf(t *testing.T) {
	cases := []struct {
		name string
		utc  time.Time
		zone string
		want string
	}{
		{"kiritimati east of dateline", time.Date(2026, 5, 11, 13, 0, 0, 0, time.UTC), "Pacific/Kiritimati", "2026-05-12"},
		{"honolulu west", time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC), "Pacific/Honolulu", "2026-05-10"},
		{"kathmandu quarter offset", time.Date(2026, 5, 11, 18, 20, 0, 0, time.UTC), "Asia/Kathmandu", "2026-05-12"},
		{"lord howe DST half-hour", time.Date(2026, 1, 15, 13, 45, 0, 0, time.UTC), "Australia/Lord_Howe", "2026-01-16"},
		{"utc noon", time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC), "UTC", "2026-05-11"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DateOf(tc.utc, mustLoc(t, tc.zone))
			if got.String() != tc.want {
				t.Fatalf("DateOf = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDateArithmetic(t *testing.T) {
	d := MustDate("2026-02-27")
	if got := d.AddDays(2).String(); got != "2026-03-01" {
		t.Fatalf("leap-adjacent AddDays = %s, want 2026-03-01 (2026 is not a leap year)", got)
	}
	if got := MustDate("2028-02-28").AddDays(1).String(); got != "2028-02-29" {
		t.Fatalf("leap day AddDays = %s, want 2028-02-29", got)
	}
	if got := MustDate("2026-01-01").DaysUntil(MustDate("2026-01-31")); got != 30 {
		t.Fatalf("DaysUntil = %d, want 30", got)
	}
	if got := MustDate("2026-01-31").DaysUntil(MustDate("2026-01-01")); got != -30 {
		t.Fatalf("negative DaysUntil = %d, want -30", got)
	}
	if !(Date{}).IsZero() {
		t.Fatal("zero Date must report IsZero")
	}
	if MustDate("2026-05-11").IsZero() {
		t.Fatal("real date must not report IsZero")
	}
	// 2026-05-11 is a Monday.
	if got := MustDate("2026-05-11").ISOWeekday(); got != 1 {
		t.Fatalf("ISOWeekday(Mon) = %d, want 1", got)
	}
	if got := MustDate("2026-05-17").ISOWeekday(); got != 7 {
		t.Fatalf("ISOWeekday(Sun) = %d, want 7", got)
	}
}

func TestParseDateRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "2026-13-01", "2026-02-30", "11.05.2026", "2026-5-1"} {
		if _, err := ParseDate(s); err == nil {
			t.Fatalf("ParseDate(%q) unexpectedly succeeded", s)
		}
	}
}
