package main

import (
	"testing"
	"time"
)

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 9, 29, 14, 30, 0, 0, time.Local)
	for in, want := range map[string]time.Time{
		"30m":              now.Add(30 * time.Minute),
		"1h30m":            now.Add(90 * time.Minute),
		"2d":               now.AddDate(0, 0, 2),
		"15:00":            time.Date(2026, 9, 29, 15, 0, 0, 0, time.Local),
		"09:15":            time.Date(2026, 9, 30, 9, 15, 0, 0, time.Local), // passed today: tomorrow
		"14:30":            time.Date(2026, 9, 30, 14, 30, 0, 0, time.Local),
		"2026-10-01 08:00": time.Date(2026, 10, 1, 8, 0, 0, 0, time.Local),
	} {
		got, err := parseWhen(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-5m", "0s", "0d", "25:00", "tomorrow", "2026-13-01 08:00", "999d"} {
		if _, err := parseWhen(bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
