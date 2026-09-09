package core

import (
	"testing"
	"time"
)

// TestOpenPastDurationEnds pins the fold that makes an ID3 read agree with the start-only
// stores: the bounded zero-length end CHAP must carry for a chapter past the media duration
// reads back open, while an in-range zero-length chapter and a zero duration are untouched.
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
	OpenPastDurationEnds(nil, duration) // an empty list must not panic
}

// TestFormatChapterTimeRounds: the human timestamp reports the nearest millisecond, so a
// sub-millisecond start does not read one millisecond low, and a value just under a second
// carries into the seconds field rather than printing ".1000".
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
