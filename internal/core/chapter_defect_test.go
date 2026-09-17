package core

import (
	"slices"
	"testing"
	"time"
)

func chs(starts ...time.Duration) []Chapter {
	out := make([]Chapter, len(starts))
	for i, s := range starts {
		out[i] = Chapter{Start: s, Title: "C"}
	}
	return out
}

// TestChaptersPastDuration: shared editor/linter rule; unknown duration (0) reports none.
func TestChaptersPastDuration(t *testing.T) {
	const sec = time.Second
	cases := []struct {
		name     string
		chapters []Chapter
		duration time.Duration
		want     []time.Duration
	}{
		{"none past", chs(0, sec), 10 * sec, nil},
		{"one past", chs(0, 20*sec), 10 * sec, []time.Duration{20 * sec}},
		{"exactly at the end is inside", chs(10 * sec), 10 * sec, nil},
		{"unknown duration reports none", chs(0, 20*sec), 0, nil},
		{"negative duration reports none", chs(20 * sec), -sec, nil},
		{"no chapters", nil, 10 * sec, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []time.Duration
			for _, ch := range ChaptersPastDuration(c.chapters, c.duration) {
				got = append(got, ch.Start)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("ChaptersPastDuration = %v, want %v", got, c.want)
			}
		})
	}
}

// TestDuplicateChapterStarts: one report per colliding start; input need not be sorted.
func TestDuplicateChapterStarts(t *testing.T) {
	const sec = time.Second
	cases := []struct {
		name     string
		chapters []Chapter
		want     []time.Duration
	}{
		{"no duplicates", chs(0, sec, 2*sec), nil},
		{"one pair", chs(0, sec, sec), []time.Duration{sec}},
		{"three sharing a start report once", chs(sec, sec, sec), []time.Duration{sec}},
		{"two separate collisions, first-seen order", chs(2*sec, sec, 2*sec, sec), []time.Duration{2 * sec, sec}},
		{"unsorted input", chs(3*sec, 0, 3*sec), []time.Duration{3 * sec}},
		{"no chapters", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DuplicateChapterStarts(c.chapters); !slices.Equal(got, c.want) {
				t.Errorf("DuplicateChapterStarts = %v, want %v", got, c.want)
			}
		})
	}
}
