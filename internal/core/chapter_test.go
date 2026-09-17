package core

import (
	"testing"
	"time"
)

// TestOpenPastDurationEnds: past-duration zero-length end folds open (ID3 vs start-only stores).
func TestOpenPastDurationEnds(t *testing.T) {
	const duration = 30 * time.Second
	cases := []struct {
		name     string
		chs      []Chapter
		duration time.Duration
		wantEnd  time.Duration
	}{
		{"past duration folds open", []Chapter{{Start: 0, End: 10 * time.Second}, {Start: 35 * time.Second, End: 35 * time.Second}}, duration, 0},
		{"at duration folds open", []Chapter{{Start: duration, End: duration}}, duration, 0},
		{"in range is left alone", []Chapter{{Start: 0, End: 10 * time.Second}, {Start: 20 * time.Second, End: 20 * time.Second}}, duration, 20 * time.Second},
		{"a real end is left alone", []Chapter{{Start: 35 * time.Second, End: 40 * time.Second}}, duration, 40 * time.Second},
		{"zero duration is a no-op", []Chapter{{Start: 35 * time.Second, End: 35 * time.Second}}, 0, 35 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			OpenPastDurationEnds(c.chs, c.duration)
			if got := c.chs[len(c.chs)-1].End; got != c.wantEnd {
				t.Errorf("last End = %v, want %v", got, c.wantEnd)
			}
		})
	}
	OpenPastDurationEnds(nil, duration)
}

// TestFormatChapterTimeRounds: nearest millisecond; sub-ms values carry correctly.
func TestFormatChapterTimeRounds(t *testing.T) {
	cases := map[time.Duration]string{
		362811791 * time.Nanosecond:   "0:00:00.363",
		999_600_000 * time.Nanosecond: "0:00:01.000",
	}
	for in, want := range cases {
		if got := FormatChapterTime(in); got != want {
			t.Errorf("FormatChapterTime(%v) = %q, want %q", in, got, want)
		}
	}
}
