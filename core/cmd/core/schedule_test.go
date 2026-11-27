package main

import (
	"testing"
	"time"
)

func TestDurationUntilNextLocalHM(t *testing.T) {
	t.Parallel()
	cst := time.FixedZone("CST", 8*3600)
	cases := []struct {
		name   string
		now    time.Time
		hour   int
		minute int
		want   time.Duration
	}{
		{
			name:   "same_day_future",
			now:    time.Date(2026, 11, 27, 0, 15, 0, 0, cst),
			hour:   1,
			minute: 0,
			want:   45 * time.Minute,
		},
		{
			name:   "same_day_exact_moves_to_next_day",
			now:    time.Date(2026, 11, 27, 2, 30, 0, 0, cst),
			hour:   2,
			minute: 30,
			want:   24 * time.Hour,
		},
		{
			name:   "past_time_schedules_tomorrow",
			now:    time.Date(2026, 11, 27, 10, 0, 0, 0, cst),
			hour:   4,
			minute: 30,
			want:   18*time.Hour + 30*time.Minute,
		},
		{
			name:   "out_of_range_normalized",
			now:    time.Date(2026, 11, 27, 0, 0, 0, 0, cst),
			hour:   25,
			minute: 75,
			want:   1*time.Hour + 15*time.Minute,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := durationUntilNextLocalHM(tc.now, cst, tc.hour, tc.minute)
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
