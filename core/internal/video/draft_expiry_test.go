package video

import (
	"testing"
	"time"
)

func TestDraftTaskUsableAtOfficialCutover(t *testing.T) {
	cutover := draftFourteenDayCutover
	cases := []struct {
		name    string
		created time.Time
		now     time.Time
		want    bool
	}{
		{"old task before seven days", cutover.Add(-time.Second), cutover.Add(7*24*time.Hour - 2*time.Second), true},
		{"old task at seven days", cutover.Add(-time.Second), cutover.Add(7*24*time.Hour - time.Second), false},
		{"new task before fourteen days", cutover, cutover.Add(14*24*time.Hour - time.Nanosecond), true},
		{"new task at fourteen days", cutover, cutover.Add(14 * 24 * time.Hour), false},
		{"missing creation time", time.Time{}, cutover, false},
		{"future creation time", cutover.Add(time.Second), cutover, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := draftTaskUsable(tc.created, tc.now); got != tc.want {
				t.Fatalf("usable = %t, want %t", got, tc.want)
			}
		})
	}
}
