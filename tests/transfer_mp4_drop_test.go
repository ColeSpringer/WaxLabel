package waxlabel_test

import (
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestTransferDropsUnstorableMP4ValuesPreservesDest checks that MP4 values the writer cannot
// store are reported as dropped and do not overwrite valid destination values.
func TestTransferDropsUnstorableMP4ValuesPreservesDest(t *testing.T) {
	keys := []tag.Key{tag.TrackNumber, tag.TrackTotal, tag.DiscNumber, tag.DiscTotal, tag.MediaType, tag.Compilation}

	// Destination MP4 with good, storable values for every key the source will try to
	// overwrite with an unstorable one.
	dstBytes := writeBack(t, "../testdata/notags.m4a", func(e *wl.Editor) {
		e.Set(tag.TrackNumber, "5")
		e.Set(tag.TrackTotal, "10")
		e.Set(tag.DiscNumber, "1")
		e.Set(tag.DiscTotal, "2")
		e.Set(tag.MediaType, "1")
		e.Set(tag.Compilation, "1")
	})
	dst := mustParseBytes(t, dstBytes)
	// Snapshot what the destination round-trips so the assertions compare against the
	// real stored form (e.g. cpil may read back canonicalized), not a guessed spelling.
	want := map[tag.Key][]string{}
	for _, k := range keys {
		v, ok := dst.Get(k)
		if !ok {
			t.Fatalf("setup: destination missing %s", k)
		}
		want[k] = v
	}

	// Source carries values the iTunes atoms cannot represent: out-of-uint16 track/disc
	// numbers and totals, a non-numeric stik, and a non-boolean cpil. Literal Vorbis comments
	// keep them verbatim; writeBack would split or coerce them at set time.
	src := mustParseBytes(t, flacWithComments(
		"TITLE=Carried",
		"TRACKNUMBER=70000",
		"TRACKTOTAL=70000",
		"DISCNUMBER=70000",
		"DISCTOTAL=70000",
		"MEDIATYPE=bogus",
		"COMPILATION=maybe",
	))

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}

	disp := map[tag.Key]wl.Disposition{}
	for _, it := range report.Items {
		if it.Kind == wl.TransferField {
			disp[it.Key] = it.Disposition
		}
	}
	for _, k := range keys {
		if disp[k] != wl.Dropped {
			t.Errorf("%s disposition = %s, want dropped (the MP4 atom cannot store the source value)", k, disp[k])
		}
	}
	if disp[tag.Title] != wl.Carried {
		t.Errorf("TITLE disposition = %s, want carried", disp[tag.Title])
	}

	// The destination's good values survive: dropped items are skipped, never clobbered.
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	for _, k := range keys {
		if got, _ := result.Get(k); !slices.Equal(got, want[k]) {
			t.Errorf("destination %s = %v, want preserved %v (a dropped source value must not clobber)", k, got, want[k])
		}
	}
	if got, _ := result.Get(tag.Title); !slices.Equal(got, []string{"Carried"}) {
		t.Errorf("TITLE = %v, want [Carried] (carried despite the dropped numerics)", got)
	}

	// TRACKNUMBER="0" with no total is not covered here. That pair-level collapse depends on
	// the sibling total slot, so a per-value predicate cannot detect it.
}

// TestTransferSlashTrackTotalToMP4 checks how transfer to MP4 handles the TRACKTOTAL carried by
// a slash-combined source TRACKNUMBER. The FLAC read path splits "3/12" into TRACKNUMBER=3 +
// TRACKTOTAL=12 at parse (tag.NormalizeNumberPairs), so transfer does not derive the total
// itself; it sees two independent canonical keys and grades each against the destination.
func TestTransferSlashTrackTotalToMP4(t *testing.T) {
	dstBytes := readFixture(t, "../testdata/notags.m4a")

	transfer := func(t *testing.T, trackNumber string) (wl.TransferReport, *wl.Document) {
		t.Helper()
		// The FLAC read path splits a slashed TRACKNUMBER into number + total at parse, so
		// transfer sees TRACKNUMBER and TRACKTOTAL as separate keys.
		src := mustParseBytes(t, flacWithComments("TRACKNUMBER="+trackNumber))
		plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
		if err != nil {
			t.Fatalf("PrepareTransfer(%q): %v", trackNumber, err)
		}
		return report, mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	}
	totalItem := func(r wl.TransferReport) (wl.TransferItem, bool) {
		for _, it := range r.Items {
			if it.Kind == wl.TransferField && it.Key == tag.TrackTotal {
				return it, true
			}
		}
		return wl.TransferItem{}, false
	}
	dispositionOf := func(r wl.TransferReport, k tag.Key) (wl.Disposition, bool) {
		for _, it := range r.Items {
			if it.Kind == wl.TransferField && it.Key == k {
				return it.Disposition, true
			}
		}
		return 0, false
	}

	// 3/12: both split keys are representable, so the report lists a carried TRACKTOTAL and
	// the write stores it.
	report, result := transfer(t, "3/12")
	if it, ok := totalItem(report); !ok || it.Disposition != wl.Carried {
		t.Errorf("3/12: TRACKTOTAL item = %+v (present=%v), want one carried", it, ok)
	}
	if got, _ := result.Get(tag.TrackTotal); !slices.Equal(got, []string{"12"}) {
		t.Errorf("3/12: result TRACKTOTAL = %v, want [12]", got)
	}

	// 3/70000: the number is representable but the split-off total is not (past uint16), so
	// the TRACKTOTAL item is Dropped - matching the writer, which stores 3 and drops 70000.
	report, result = transfer(t, "3/70000")
	if it, ok := totalItem(report); !ok || it.Disposition != wl.Dropped {
		t.Errorf("3/70000: TRACKTOTAL item = %+v (present=%v), want one dropped", it, ok)
	}
	if got, _ := result.Get(tag.TrackTotal); slices.Contains(got, "70000") {
		t.Errorf("3/70000: result TRACKTOTAL = %v, must not store the dropped 70000", got)
	}

	// 70000/3: the split makes TRACKNUMBER=70000 (dropped, past uint16) and TRACKTOTAL=3 two
	// independent keys. The number drops, but the representable total still carries on its own
	// - the read-path split promoted it to a first-class value, so it no longer rides on the
	// number the way a transfer-time-derived total once did.
	report, result = transfer(t, "70000/3")
	if it, ok := totalItem(report); !ok || it.Disposition != wl.Carried {
		t.Errorf("70000/3: TRACKTOTAL item = %+v (present=%v), want one carried", it, ok)
	}
	if got, _ := result.Get(tag.TrackTotal); !slices.Equal(got, []string{"3"}) {
		t.Errorf("70000/3: result TRACKTOTAL = %v, want [3] (representable total carries despite the dropped number)", got)
	}
	if d, ok := dispositionOf(report, tag.TrackNumber); !ok || d != wl.Dropped {
		t.Errorf("70000/3: TRACKNUMBER disposition = %s (present=%v), want dropped", d, ok)
	}
}

// TestTransferSlashTotalDoesNotClobberDest checks that when a source's total is unstorable at
// the destination, the destination's own total survives. The FLAC read path splits
// "3/70000" into TRACKNUMBER=3 + TRACKTOTAL=70000; transfer grades that total Dropped (past
// uint16 / negative) and skips it, so it never overwrites the destination's TRACKTOTAL, while
// the representable number still carries.
func TestTransferSlashTotalDoesNotClobberDest(t *testing.T) {
	for _, num := range []string{"3/70000", "3/-5"} {
		dstBytes := writeBack(t, "../testdata/notags.m4a", func(e *wl.Editor) {
			e.Set(tag.TrackNumber, "1")
			e.Set(tag.TrackTotal, "10")
		})
		dst := mustParseBytes(t, dstBytes)
		wantTotal, _ := dst.Get(tag.TrackTotal) // the destination's own total, to be preserved

		src := mustParseBytes(t, flacWithComments("TRACKNUMBER="+num))
		plan, report, err := src.PrepareTransfer(dst)
		if err != nil {
			t.Fatalf("%s: PrepareTransfer: %v", num, err)
		}

		// The report grades the split-off total dropped...
		var totDisp wl.Disposition
		var sawTot bool
		for _, it := range report.Items {
			if it.Kind == wl.TransferField && it.Key == tag.TrackTotal {
				totDisp, sawTot = it.Disposition, true
			}
		}
		if !sawTot || totDisp != wl.Dropped {
			t.Errorf("%s: TRACKTOTAL item = %s (present=%v), want dropped", num, totDisp, sawTot)
		}
		// ...so the destination's own total must survive, not be clobbered to 0.
		result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
		if got, _ := result.Get(tag.TrackTotal); !slices.Equal(got, wantTotal) {
			t.Errorf("%s: result TRACKTOTAL = %v, want preserved %v (a dropped total must not clobber the dest)", num, got, wantTotal)
		}
		// The representable number still carries.
		if got, _ := result.Get(tag.TrackNumber); len(got) != 1 || got[0] != "3" {
			t.Errorf("%s: result TRACKNUMBER = %v, want [3] (the number still carries)", num, got)
		}
	}
}
