package core

import (
	"testing"
	"time"
)

// TestEqualChaptersModuloEnds covers the duration-aware chapter equivalence diff uses so its
// verdict agrees with how copy grades a reconstructable end. Reconstructable end differences
// (a gapless interior end, or a trailing end that runs to EOF) compare equal; a genuine
// interior gap, an early-ending trailing chapter, or any non-end difference still differs.
func TestEqualChaptersModuloEnds(t *testing.T) {
	ms := time.Millisecond
	s := time.Second
	ch := func(start, end time.Duration, title string) Chapter {
		return Chapter{Start: start, End: end, Title: title}
	}
	dur := 10 * s

	cases := []struct {
		name       string
		a, b       []Chapter
		durA, durB time.Duration
		want       bool
	}{
		{
			name: "identical open lists",
			a:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: true,
		},
		{
			name: "interior gapless end vs open are equal",
			a:    []Chapter{ch(0, 5*s, "A"), ch(5*s, 0, "B")}, // A.End == B.Start
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: true,
		},
		{
			name: "interior gapped end still differs",
			a:    []Chapter{ch(0, 3*s, "A"), ch(5*s, 0, "B")}, // A ends early (gap before B)
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: false,
		},
		{
			name: "trailing end at EOF vs open are equal",
			a:    []Chapter{ch(0, 0, "A"), ch(5*s, dur, "B")}, // B runs to EOF
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: true,
		},
		{
			name: "trailing end before EOF still differs",
			a:    []Chapter{ch(0, 0, "A"), ch(5*s, 8*s, "B")}, // B ends at 8s, before the 10s EOF
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: false,
		},
		{
			name: "trailing end floored to ms vs a non-whole-ms duration are equal",
			// The Truncate guard: a written ID3 trailing end reads back as floor(dur) ms, while
			// Properties().Duration() is nanosecond-precise. Without truncating dur to ms, a naive
			// End >= dur would be 2037ms >= 2037.5ms -> false and wrongly report "differ".
			a:    []Chapter{ch(0, 0, "A"), ch(1*s, 2037*ms, "B")},
			b:    []Chapter{ch(0, 0, "A"), ch(1*s, 0, "B")},
			durA: 2037*ms + 500*time.Microsecond, durB: 2037*ms + 500*time.Microsecond, want: true,
		},
		{
			name: "different length differs",
			a:    []Chapter{ch(0, 0, "A")},
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: false,
		},
		{
			name: "different title differs",
			a:    []Chapter{ch(0, 0, "A")},
			b:    []Chapter{ch(0, 0, "Z")},
			durA: dur, durB: dur, want: false,
		},
		{
			name: "different start differs",
			a:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			b:    []Chapter{ch(0, 0, "A"), ch(6*s, 0, "B")},
			durA: dur, durB: dur, want: false,
		},
		{
			name: "unknown duration leaves a trailing end distinct",
			// dur == 0: the trailing rule cannot fire, so a bounded trailing end is not
			// normalized and stays distinct from an open one.
			a:    []Chapter{ch(0, 0, "A"), ch(5*s, 8*s, "B")},
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: 0, durB: 0, want: false,
		},
		{
			name: "byte-identical lists are equal despite differing durations",
			// The trailing rule normalizes per-file: a 50s end runs to EOF in a 50s file but
			// sits mid-file in a 100s file. Byte-identical chapter metadata must still compare
			// equal, so the fast path returns early before the per-file normalization diverges.
			a:    []Chapter{ch(0, 50*s, "A")},
			b:    []Chapter{ch(0, 50*s, "A")},
			durA: 50 * s, durB: 100 * s, want: true,
		},
		{
			name: "byte-identical lists are equal when one duration is unknown",
			a:    []Chapter{ch(0, 50*s, "A")},
			b:    []Chapter{ch(0, 50*s, "A")},
			durA: 50 * s, durB: 0, want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EqualChaptersModuloEnds(tc.a, tc.b, tc.durA, tc.durB); got != tc.want {
				t.Errorf("EqualChaptersModuloEnds = %v, want %v", got, tc.want)
			}
			// Symmetric: swapping the operands (and their durations) must not change the verdict.
			if got := EqualChaptersModuloEnds(tc.b, tc.a, tc.durB, tc.durA); got != tc.want {
				t.Errorf("EqualChaptersModuloEnds (swapped) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEqualChaptersModuloEndsDoesNotMutate checks the helper leaves its inputs untouched
// (it normalizes clones), so a caller's chapter slices are safe to reuse afterward.
func TestEqualChaptersModuloEndsDoesNotMutate(t *testing.T) {
	s := time.Second
	a := []Chapter{{Start: 0, End: 5 * s, Title: "A"}, {Start: 5 * s, End: 10 * s, Title: "B"}}
	b := []Chapter{{Start: 0, End: 0, Title: "A"}, {Start: 5 * s, End: 0, Title: "B"}}
	EqualChaptersModuloEnds(a, b, 10*s, 10*s)
	if a[0].End != 5*s || a[1].End != 10*s {
		t.Errorf("input a was mutated: %+v", a)
	}
}
