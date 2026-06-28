package waxlabel

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestTrimNumericValues checks the same order Prepare uses: trim numeric values first,
// then split number pairs. That stores the value WaxLabel already validates and lets
// padded slash pairs keep their inner component trimming.
func TestTrimNumericValues(t *testing.T) {
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

	t.Run("plain padded number trimmed", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, " 3 ")
		ts := p.Apply(tag.NewTagSet())
		trimNumericValues(&ts, p)
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3")
		wantAbsent(t, ts, tag.TrackTotal)
	})

	t.Run("padded slash pair trimmed and split", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, " 3 / 12 ")
		ts := p.Apply(tag.NewTagSet())
		trimNumericValues(&ts, p)
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3")
		wantVals(t, ts, tag.TrackTotal, "12")
	})

	t.Run("padded standalone total trimmed", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackTotal, " 12 ")
		ts := p.Apply(tag.NewTagSet())
		trimNumericValues(&ts, p)
		wantVals(t, ts, tag.TrackTotal, "12")
	})

	t.Run("carried base value not rewritten (scoped to patched keys)", func(t *testing.T) {
		base := tag.NewTagSet()
		base.Set(tag.TrackNumber, " 5 ") // a foreign padded literal carried from the base file
		var p tag.TagPatch
		p.Set(tag.Title, "X") // edits an unrelated field
		ts := p.Apply(base)
		trimNumericValues(&ts, p)
		wantVals(t, ts, tag.TrackNumber, " 5 ") // untouched - the edit never patched it
	})

	t.Run("leading zeros preserved (only whitespace removed)", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, " 03 ")
		ts := p.Apply(tag.NewTagSet())
		trimNumericValues(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "03")
	})

	t.Run("non-numeric key keeps its whitespace", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.Title, "  spaced title  ")
		ts := p.Apply(tag.NewTagSet())
		trimNumericValues(&ts, p)
		wantVals(t, ts, tag.Title, "  spaced title  ")
	})

	t.Run("date key trimmed for storage", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.RecordingDate, " 2021 ")
		ts := p.Apply(tag.NewTagSet())
		trimNumericValues(&ts, p)
		wantVals(t, ts, tag.RecordingDate, "2021") // stored clean, so no malformed-date note
	})
}
