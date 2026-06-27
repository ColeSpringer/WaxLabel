package waxlabel

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestSplitNumberPairs checks "n/total" normalization and its precedence rules directly
// on the helper Prepare runs. It covers cases a cross-format CLI table cannot reach:
// present-empty values, base-carried totals, leading-zero preservation, the no-churn
// gate, and the multi-valued out-of-scope case. The (tags, patch) pair fed in is exactly
// what TagPatch.Apply produces, so the test exercises the same inputs Prepare passes.
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

	t.Run("bare slash kept verbatim, key not deleted", func(t *testing.T) {
		// "--set TRACKNUMBER=/" must not delete the key. A lone slash carries no number,
		// so it fails validation; splitNumberPairs leaves it verbatim instead of splitting
		// to empty/empty, and the set-time note flags it. TRACKTOTAL is untouched.
		base := tag.NewTagSet()
		base.Set(tag.TrackNumber, "5")
		base.Set(tag.TrackTotal, "10")
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "/")
		ts := p.Apply(base)
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "/") // kept verbatim, not deleted
		wantVals(t, ts, tag.TrackTotal, "10") // unchanged
		if tag.ValidNumericValue(tag.TrackNumber, "/") {
			t.Error(`ValidNumericValue(TRACKNUMBER, "/") = true; a bare slash must fail so a malformed-number note is emitted`)
		}
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

	t.Run("triple slash kept verbatim, no malformed total derived", func(t *testing.T) {
		// "1/2/3" splits to num="1", total="2/3"; "2/3" fails numeric validation, so the
		// split is skipped entirely. The value stays verbatim on the number key and no
		// malformed TRACKTOTAL is synthesized; the set-time note already flags the input.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "1/2/3")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "1/2/3")
		wantAbsent(t, ts, tag.TrackTotal)
	})

	t.Run("non-numeric number kept verbatim, no total manufactured", func(t *testing.T) {
		// "abc/1" has a valid total ("1") but a non-numeric number ("abc"); validating the
		// whole value (not just the total) keeps it verbatim on the number key rather than
		// splitting into a malformed TRACKNUMBER="abc" plus a manufactured TRACKTOTAL="1".
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "abc/1")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "abc/1")
		wantAbsent(t, ts, tag.TrackTotal)
	})

	t.Run("non-numeric total kept verbatim", func(t *testing.T) {
		// Symmetric to the above: a non-numeric total ("3/abc") is left verbatim, not split
		// into TRACKNUMBER="3" + a malformed TRACKTOTAL="abc".
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/abc")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3/abc")
		wantAbsent(t, ts, tag.TrackTotal)
	})
}
