package core

import (
	"testing"
	"time"
)

// TestEqualChaptersModuloEnds: duration-aware end equivalence. Gapless interior and EOF trailing ends match; real gaps differ.
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
			a:    []Chapter{ch(0, 5*s, "A"), ch(5*s, 0, "B")},
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: true,
		},
		{
			name: "interior gapped end still differs",
			a:    []Chapter{ch(0, 3*s, "A"), ch(5*s, 0, "B")},
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: false,
		},
		{
			name: "trailing end at EOF vs open are equal",
			a:    []Chapter{ch(0, 0, "A"), ch(5*s, dur, "B")},
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: true,
		},
		{
			name: "trailing end before EOF still differs",
			a:    []Chapter{ch(0, 0, "A"), ch(5*s, 8*s, "B")},
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: dur, durB: dur, want: false,
		},
		{
			name: "trailing end floored to ms vs a non-whole-ms duration are equal",
			// Truncate dur to ms: ID3 end is floor(dur); Properties duration is ns-precise.
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
			// dur == 0: trailing rule cannot fire.
			a:    []Chapter{ch(0, 0, "A"), ch(5*s, 8*s, "B")},
			b:    []Chapter{ch(0, 0, "A"), ch(5*s, 0, "B")},
			durA: 0, durB: 0, want: false,
		},
		{
			name: "differing durations make a byte-identical trailing end distinct",
			// Trailing end normalizes per file duration; fast path gates on equal durations.
			a:    []Chapter{ch(0, 50*s, "A")},
			b:    []Chapter{ch(0, 50*s, "A")},
			durA: 50 * s, durB: 100 * s, want: false,
		},
		{
			name: "unknown duration makes a byte-identical trailing end distinct",
			// durB == 0: cannot prove EOF; avoids non-transitive mka/mp3/flac shape.
			a:    []Chapter{ch(0, 50*s, "A")},
			b:    []Chapter{ch(0, 50*s, "A")},
			durA: 50 * s, durB: 0, want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EqualChaptersModuloEnds(tc.a, tc.b, tc.durA, tc.durB); got != tc.want {
				t.Errorf("EqualChaptersModuloEnds = %v, want %v", got, tc.want)
			}
			if got := EqualChaptersModuloEnds(tc.b, tc.a, tc.durB, tc.durA); got != tc.want {
				t.Errorf("EqualChaptersModuloEnds (swapped) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEqualChaptersModuloEndsTransitive: no A==B, B==C, A!=C with mixed durations.
func TestEqualChaptersModuloEndsTransitive(t *testing.T) {
	s := time.Second
	chs := []Chapter{{Start: 0, End: 50 * s, Title: "A"}}
	files := []struct {
		name string
		dur  time.Duration
	}{
		{"mka", 0},
		{"mp3", 50 * s},
		{"flac", 100 * s},
	}
	eq := func(i, j int) bool { return EqualChaptersModuloEnds(chs, chs, files[i].dur, files[j].dur) }

	for i := range files {
		for j := range files {
			for k := range files {
				if eq(i, j) && eq(j, k) && !eq(i, k) {
					t.Errorf("non-transitive: %s==%s and %s==%s but %s!=%s",
						files[i].name, files[j].name, files[j].name, files[k].name, files[i].name, files[k].name)
				}
			}
		}
	}
	if !eq(1, 1) {
		t.Error("same-duration identical chapters must be equal (mp3 == mp3)")
	}
	if eq(1, 2) {
		t.Error("a 50s file and a 100s file with the same [0,50s] chapter must differ (mp3 != flac)")
	}
}

// TestEqualChaptersModuloEndsDoesNotMutate: normalizes clones; inputs unchanged.
func TestEqualChaptersModuloEndsDoesNotMutate(t *testing.T) {
	s := time.Second
	a := []Chapter{{Start: 0, End: 5 * s, Title: "A"}, {Start: 5 * s, End: 10 * s, Title: "B"}}
	b := []Chapter{{Start: 0, End: 0, Title: "A"}, {Start: 5 * s, End: 0, Title: "B"}}
	EqualChaptersModuloEnds(a, b, 10*s, 10*s)
	if a[0].End != 5*s || a[1].End != 10*s {
		t.Errorf("input a was mutated: %+v", a)
	}
}
