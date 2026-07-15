package main

import (
	"testing"
	"time"
)

func TestParseWhen(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  time.Time
		valid bool
	}{
		{"empty", "", time.Time{}, true},
		{"date only", "2026-07-15", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), true},
		{"space minute", "2026-07-15 07:30", time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC), true},
		{"t minute", "2026-07-15T07:30", time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC), true},
		{"space second", "2026-07-15 07:30:05", time.Date(2026, 7, 15, 7, 30, 5, 0, time.UTC), true},
		{"t second", "2026-07-15T07:30:05", time.Date(2026, 7, 15, 7, 30, 5, 0, time.UTC), true},
		// A DBA can paste an ERRORLOG timestamp verbatim, fractional seconds and all.
		{"errorlog fractional", "2026-07-15 07:13:10.54", time.Date(2026, 7, 15, 7, 13, 10, 540000000, time.UTC), true},
		{"garbage", "yesterday", time.Time{}, false},
		{"time only", "07:30", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseWhen(c.in)
			if c.valid && err != nil {
				t.Fatalf("parseWhen(%q) unexpected error: %v", c.in, err)
			}
			if !c.valid {
				if err == nil {
					t.Fatalf("parseWhen(%q) = %v, want error", c.in, got)
				}
				return
			}
			if !got.Equal(c.want) {
				t.Fatalf("parseWhen(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
