package waxlabel

import (
	"context"
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

// TestSplitNumberPairsConflictWarning checks the total-vs-slash conflict warning the split
// returns: it fires only when the same edit sets an explicit total that disagrees with the
// slash-derived one, carrying the affected total (and number) key. The split precedence is
// unchanged - the explicit total still wins - so this only asserts the advisory signal.
func TestSplitNumberPairsConflictWarning(t *testing.T) {
	// wantConflict asserts exactly one number-total-conflict warning keyed to totKey.
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
		// Precedence unchanged: the explicit total still wins.
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
		// "1/07" derives total "07"; an explicit TRACKTOTAL=7 denotes the same number, and the
		// derived "07" is discarded (explicit wins), so this must not warn.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "1/07")
		p.Set(tag.TrackTotal, "7")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
		// Symmetric: "3/12" derived "12" vs explicit "012".
		var p2 tag.TagPatch
		p2.Set(tag.TrackNumber, "3/12")
		p2.Set(tag.TrackTotal, "012")
		ts2 := p2.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts2, p2))
	})

	t.Run("non-numeric explicit total still conflicts", func(t *testing.T) {
		// A non-numeric explicit total never parses equal to the numeric derived one, so it is a
		// genuine disagreement and still warns.
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
		// "3" has no slash, so no derived total exists to disagree with an explicit one.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "3")
		p.Set(tag.TrackTotal, "99")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
	})

	t.Run("malformed slashed number with explicit total -> no warn", func(t *testing.T) {
		// "abc/1" fails numeric validation, so NumberTotalSplit reports split=false and no
		// derived total exists to conflict.
		var p tag.TagPatch
		p.Set(tag.TrackNumber, "abc/1")
		p.Set(tag.TrackTotal, "99")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
	})

	t.Run("multi-valued number with explicit total -> no warn", func(t *testing.T) {
		// A multi-valued number is out of scope for the split (never lose a value), so it
		// derives no total and cannot conflict.
		var p tag.TagPatch
		p.Add(tag.TrackNumber, "4/12")
		p.Add(tag.TrackNumber, "3")
		p.Set(tag.TrackTotal, "99")
		ts := p.Apply(tag.NewTagSet())
		wantNoConflict(t, splitNumberPairs(&ts, p))
	})
}

// TestNumberTotalConflictWarningSurfacing checks the whole path through Prepare: an authored
// edit whose explicit total disagrees with a slash-derived one surfaces the conflict warning
// in the plan report, while a faithful carry (Editor.carried) suppresses it - the split still
// runs, only the warning is gated, so a copy never flags the source's own values.
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
