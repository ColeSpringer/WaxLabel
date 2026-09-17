package waxlabel

import (
	"context"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestSplitNumberPairs covers Prepare's "n/total" helper: empty values, carried
// totals, leading zeros, no-churn gate, multi-valued out-of-scope.
func TestSplitNumberPairs(t *testing.T) {
	// wantVals: present with exact vals. wantAbsent: absent. Kept distinct so
	// present-empty vs absent cannot be conflated.
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
		// Lone "/" left verbatim; TRACKTOTAL untouched.
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
		// "1/2/3" fails numeric validation; left verbatim.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "1/2/3")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "1/2/3")
		wantAbsent(t, ts, tag.TrackTotal)
	})

	t.Run("non-numeric number kept verbatim, no total manufactured", func(t *testing.T) {
		// Non-numeric number side: left verbatim.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "abc/1")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "abc/1")
		wantAbsent(t, ts, tag.TrackTotal)
	})

	t.Run("non-numeric total kept verbatim", func(t *testing.T) {
		// Non-numeric total side: left verbatim.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/abc")
		ts := p.Apply(tag.NewTagSet())
		splitNumberPairs(&ts, p)
		wantVals(t, ts, tag.TrackNumber, "3/abc")
		wantAbsent(t, ts, tag.TrackTotal)
	})
}

// TestSplitNumberPairsConflictWarning: warn when explicit total disagrees with
// slash-derived; explicit still wins.
func TestSplitNumberPairsConflictWarning(t *testing.T) {
	// wantConflict: exactly one number-total-conflict warning for totKey.
	wantConflict := func(t *testing.T, ws []Warning, totKey, numKey tag.Key) {
		t.Helper()
		if len(ws) != 1 {
			t.Fatalf("got %d warnings, want exactly 1: %v", len(ws), ws)
		}
		if ws[0].Code != WarnNumberTotalConflict {
			t.Errorf("warning code = %v, want WarnNumberTotalConflict", ws[0].Code)
		}
		if !slices.Contains(ws[0].Keys, totKey) || !slices.Contains(ws[0].Keys, numKey) {
			t.Errorf("warning keys = %v, want to contain %s and %s", ws[0].Keys, totKey, numKey)
		}
	}
	wantNoConflict := func(t *testing.T, ws []Warning) {
		t.Helper()
		if len(ws) != 0 {
			t.Errorf("got %d warnings, want none: %v", len(ws), ws)
		}
	}

	t.Run("explicit total disagrees with slash -> warns", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/12")
		p.Set(tag.TrackTotal, "99")
		ts := p.Apply(tag.NewTagSet())
		ws := splitNumberPairs(&ts, p)
		wantConflict(t, ws, tag.TrackTotal, tag.TrackNumber)
		// Explicit total still wins.
		if v, _ := ts.First(tag.TrackTotal); v != "99" {
			t.Errorf("TRACKTOTAL = %q, want 99 (explicit wins)", v)
		}
	})

	t.Run("disc variant warns and keys DISCTOTAL", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.DiscNumber, "1/2")
		p.Set(tag.DiscTotal, "9")
		ts := p.Apply(tag.NewTagSet())
		wantConflict(t, splitNumberPairs(&ts, p), tag.DiscTotal, tag.DiscNumber)
	})

	t.Run("explicit total agrees with slash -> no warn", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/12")
		p.Set(tag.TrackTotal, "12")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
	})

	t.Run("leading-zero-only difference is not a conflict", func(t *testing.T) {
		// "1/07" vs TRACKTOTAL=7: same number, no warn.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "1/07")
		p.Set(tag.TrackTotal, "7")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
		// "3/12" vs TRACKTOTAL=012: same number, no warn.
		var p2 tag.TagPatch
		p2.Set(tag.TrackNumber, "3/12")
		p2.Set(tag.TrackTotal, "012")
		ts2 := p2.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts2, p2))
	})

	t.Run("non-numeric explicit total still conflicts", func(t *testing.T) {
		// Non-numeric explicit total still warns.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/12")
		p.Set(tag.TrackTotal, "many")
		ts := p.Apply(tag.NewTagSet())
		wantConflict(t, splitNumberPairs(&ts, p), tag.TrackTotal, tag.TrackNumber)
	})

	t.Run("only slashed number set -> no warn", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/12")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
	})

	t.Run("only total set -> no warn", func(t *testing.T) {
		var p tag.TagPatch
		p.Set(tag.TrackTotal, "99")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p)) // number not touched
	})

	t.Run("explicit clear of total -> no warn", func(t *testing.T) {
		base := tag.NewTagSet()
		base.Set(tag.TrackTotal, "10")
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3/12")
		p.Clear(tag.TrackTotal)
		ts := p.Apply(base)
		wantNoConflict(t, splitNumberPairs(&ts, p)) // no explicit value to conflict with
	})

	t.Run("plain number with explicit total -> no warn", func(t *testing.T) {
		// No slash: no derived total to conflict.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3")
		p.Set(tag.TrackTotal, "99")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
	})

	t.Run("malformed slashed number with explicit total -> no warn", func(t *testing.T) {
		// Invalid pair: no derived total.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "abc/1")
		p.Set(tag.TrackTotal, "99")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
	})

	t.Run("multi-valued number with explicit total -> no warn", func(t *testing.T) {
		// Multi-valued number: out of scope, no conflict.
		var p tag.TagPatch
		p.Add(tag.TrackNumber, "4/12")
		p.Add(tag.TrackNumber, "3")
		p.Set(tag.TrackTotal, "99")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
	})
}

// TestNumberTotalConflictWarningSurfacing: Prepare surfaces conflict; carried suppresses.
func TestNumberTotalConflictWarningSurfacing(t *testing.T) {
	hasConflict := func(p *Plan) bool {
		for _, w := range p.Report().Warnings {
			if w.Code == WarnNumberTotalConflict {
				return true
			}
		}
		return false
	}

	doc, err := ParseFile(context.Background(), "testdata/notags.flac")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	t.Run("authored edit surfaces the warning", func(t *testing.T) {
		plan, err := doc.Edit().Set(tag.TrackNumber, "3/12").Set(tag.TrackTotal, "99").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if !hasConflict(plan) {
			t.Errorf("authored edit with a disagreeing total should warn; warnings=%v", plan.Report().Warnings)
		}
	})

	t.Run("faithful carry suppresses the warning", func(t *testing.T) {
		ed := doc.Edit()
		ed.carried = true
		ed.Set(tag.TrackNumber, "3/12")
		ed.Set(tag.TrackTotal, "99")
		plan, err := ed.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if hasConflict(plan) {
			t.Errorf("a faithful carry must not surface the total-vs-slash conflict; warnings=%v", plan.Report().Warnings)
		}
	})
}
