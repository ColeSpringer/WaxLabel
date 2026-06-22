package waxlabel

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestSplitNumberPairs pins A2's "n/total" normalization and its precedence rules
// directly on the helper Prepare runs, covering cases a cross-format CLI table cannot
// reach: a present-empty value, a base-carried total, leading-zero preservation, the
// no-churn gate, and the multi-valued out-of-scope case. The (tags, patch) pair fed in
// is exactly what TagPatch.Apply produces, so the test exercises the same inputs
// Prepare passes.
func TestSplitNumberPairs(t *testing.T) {
	// wantVals asserts a key is present with exactly vals; wantAbsent asserts it is
	// absent. They are kept distinct (rather than one helper keyed on nil) so the
	// present-empty vs absent distinction A2 turns on can never be conflated in a test.
	wantVals := func(t *testing.T, ts tag.TagSet, key tag.Key, vals ...string) {
		t.Helper()
		got, ok := ts.Get(key)
		if !ok || !slices.Equal(got, vals) {
			t.Errorf("%s = %v (present=%v), want %v", key, got, ok, vals)
		}
	}
	wantAbsent := func(t *testing.T, ts tag.TagSet, key tag.Key) {
		t.Helper()
		if got, ok := ts.Get(key); ok {
			t.Errorf("%s = %v, want absent", key, got)
		}
	}

	t.Run("basic split", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/12")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3")
		wantVals(t, ts, tag.TrackTotal, "12")
	})

	t.Run("explicit total wins over slash", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/12")
		p.Set(tag.TrackTotal, "20")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3")
		wantVals(t, ts, tag.TrackTotal, "20")
	})

	t.Run("clear total wins over slash", func(t *testing.T) {
		base := tag.NewTagSet()
		base.Set(tag.TrackTotal, "10")
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/12")
		p.Clear(tag.TrackTotal)
		ts := p.Apply(base)
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3")
		wantAbsent(t, ts, tag.TrackTotal) // the same-edit clear beats the slash
	})

	t.Run("slash updates base-carried total", func(t *testing.T) {
		base := tag.NewTagSet()
		base.Set(tag.TrackTotal, "10")
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/12")
		ts := p.Apply(base)
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3")
		wantVals(t, ts, tag.TrackTotal, "12") // not the "absent in editedTags" test - it updates
	})

	t.Run("no churn: untouched literal is left alone", func(t *testing.T) {
		base := tag.NewTagSet()
		base.Set(tag.TrackNumber, "3/12") // a foreign literal carried from the base file
		var p tag.TagPatch
		p.Set(tag.Title, "X") // edits an unrelated field
		ts := p.Apply(base)
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3/12") // unchanged - not split
		wantAbsent(t, ts, tag.TrackTotal)        // never invented
	})

	t.Run("present-empty does not split", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "") // set TRACKNUMBER=
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "") // still a present empty value (A1xA2 compose)
		wantAbsent(t, ts, tag.TrackTotal)
	})

	t.Run("multi-valued number left untouched (no data loss)", func(t *testing.T) {
		var p tag.TagPatch
		p.Add(tag.TrackNumber, "4/12")
		p.Add(tag.TrackNumber, "3")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "4/12", "3") // not split, nothing discarded
		wantAbsent(t, ts, tag.TrackTotal)
	})

	t.Run("trailing slash yields only the number", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3")
		wantAbsent(t, ts, tag.TrackTotal)
	})

	t.Run("leading slash yields only the total", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "/12")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantAbsent(t, ts, tag.TrackNumber) // no number survives
		wantVals(t, ts, tag.TrackTotal, "12")
	})

	t.Run("leading zeros preserved (not renumbered)", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "03/09")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "03") // ParseNumPair would collapse to "3"; we keep substrings
		wantVals(t, ts, tag.TrackTotal, "09")
	})

	t.Run("disc number splits to disc total", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.DiscNumber, "1/2")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.DiscNumber, "1")
		wantVals(t, ts, tag.DiscTotal, "2")
	})
}
