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
			name: "differing durations make a byte-identical trailing end distinct",
			// The trailing rule normalizes per file. A 50s end runs to EOF in a 50s file, so its
			// End canonicalizes to 0, but it sits mid-file in a 100s file, where its End stays 50s.
			// The two lists therefore canonicalize differently and are not equal, even though their
			// metadata is byte-identical. This is the case the duration-blind fast path got wrong,
			// so the fast path now gates on equal durations. It is also the more accurate answer: a
			// [0,50s] chapter covers a 50s file entirely but only the first half of a 100s file.
			a:    []Chapter{ch(0, 50*s, "A")},
			b:    []Chapter{ch(0, 50*s, "A")},
			durA: 50 * s, durB: 100 * s, want: false,
		},
		{
			name: "unknown duration makes a byte-identical trailing end distinct",
			// With durB == 0 the trailing end cannot be shown to run to EOF, so it is not
			// normalized and stays 50s, distinct from the 50s file's run-to-EOF end (normalized to
			// 0). Reporting it equal instead would bring back the non-transitive mka/mp3/flac shape.
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
			// Symmetric: swapping the operands (and their durations) must not change the verdict.
			if got := EqualChaptersModuloEnds(tc.b, tc.a, tc.durB, tc.durA); got != tc.want {
				t.Errorf("EqualChaptersModuloEnds (swapped) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEqualChaptersModuloEndsTransitive pins the property this comparison must hold: because
// equality is the same canonical normalized form, it is transitive. Three files carrying the same
// [0,50s] chapter but with different durations (unknown, 50s, 100s) must never produce A==B and
// B==C yet A!=C.
func TestEqualChaptersModuloEndsTransitive(t *testing.T) {
	s := time.Second
	chs := []Chapter{{Start: 0, End: 50 * s, Title: "A"}}
	files := []struct {
		name string
		dur  time.Duration
	}{
		{"mka", 0},        // unknown duration: the 50s end cannot be shown to run to EOF
		{"mp3", 50 * s},   // end runs to EOF, so it normalizes to open
		{"flac", 100 * s}, // end sits mid-file, so it stays bounded
	}
	eq := func(i, j int) bool { return EqualChaptersModuloEnds(chs, chs, files[i].dur, files[j].dur) }

	// Check every ordered triple for a transitivity violation: no A==B and B==C with A!=C.
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
	// The two verdicts the shape hinges on. A same-duration pair is equal, while the 50s and 100s
	// files differ because their ends canonicalize to open and bounded respectively. The old
	// byte-identical fast path forced both to equal, which is what broke transitivity.
	if !eq(1, 1) {
		t.Error("same-duration identical chapters must be equal (mp3 == mp3)")
	}
	if eq(1, 2) {
		t.Error("a 50s file and a 100s file with the same [0,50s] chapter must differ (mp3 != flac)")
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
