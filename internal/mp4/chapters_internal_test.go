package mp4

import (
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
)

func TestSentinelToZero64(t *testing.T) {
	cases := []struct {
		v, sentinel, want uint64
	}{
		{0xFFFFFFFF, 0xFFFFFFFF, 0},                  // v0 "unknown duration"
		{123, 0xFFFFFFFF, 123},                       // a real v0 duration
		{0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF, 0},  // v1 "unknown duration"
		{0xFFFFFFFF, 0xFFFFFFFFFFFFFFFF, 0xFFFFFFFF}, // not the v1 sentinel
	}
	for _, c := range cases {
		if got := sentinelToZero64(c.v, c.sentinel); got != c.want {
			t.Errorf("sentinelToZero64(%#x, %#x) = %d, want %d", c.v, c.sentinel, got, c.want)
		}
	}
}

func TestChapterDeltasLastChapterBounded(t *testing.T) {
	chs := []core.Chapter{{Start: 0}, {Start: 5 * time.Second}}
	// An unknown movie duration (the sentinel maps to 0) must give the final
	// chapter a one-second tail, not a multi-week span - the regression a raw
	// 0xFFFFFFFF movieDuration would cause.
	if d := chapterDeltas(chs, 1000, 0); d[1] != 1000 {
		t.Errorf("last delta with unknown duration = %d, want 1000 (1s tail)", d[1])
	}
	// A real movie duration bounds the last chapter to the remaining span.
	if d := chapterDeltas(chs, 1000, 9000); d[1] != 4000 {
		t.Errorf("last delta with duration 9000 = %d, want 4000", d[1])
	}
	// An out-of-order start cannot encode a negative span (defense-in-depth behind
	// the editor's sort).
	if d := chapterDeltas([]core.Chapter{{Start: 5 * time.Second}, {Start: time.Second}}, 1000, 0); d[0] != 0 {
		t.Errorf("backwards gap delta = %d, want 0 (clamped)", d[0])
	}
}
