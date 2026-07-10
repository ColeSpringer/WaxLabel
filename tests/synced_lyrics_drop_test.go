package waxlabel_test

import (
	"bytes"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// TestSyncedLyricsDropDeterministicSingleWarning checks the library-side drop of a whole
// unstorable structural edit under WithAllowUnsupportedDrop: authoring synced lyrics on an MP4
// (which has no synced-lyrics store) drops the set with exactly one warning and no error, the
// drop yields a byte-identical no-op, and a repeated Prepare produces the identical result and
// the same single warning (so the drop does not mutate the editor's backing state). Exactly one
// synced-lyrics warning must appear: the whole-item drop code, not also the per-set
// metadata-loss code.
func TestSyncedLyricsDropDeterministicSingleWarning(t *testing.T) {
	src := readFixture(t, "../testdata/notags.m4a")
	set := wl.SyncedLyrics{Language: "eng", Lines: []wl.SyncedLine{{Time: 5 * time.Second, Text: "hi"}}}

	countSyncedWarnings := func(plan *wl.Plan) (unsupported, other int) {
		for _, w := range plan.Report().Warnings {
			switch w.Code {
			case wl.WarnSyncedLyricsUnsupported:
				unsupported++
			case wl.WarnSyncedLyricsMetadataDropped, wl.WarnSyncedLyricsTruncated:
				other++
			}
		}
		return unsupported, other
	}

	doc := mustParseBytes(t, src)
	plan1, err := doc.Edit().SetSyncedLyrics(set).Prepare(wl.WithAllowUnsupportedDrop())
	if err != nil {
		t.Fatalf("prepare with drop: %v", err)
	}
	if u, o := countSyncedWarnings(plan1); u != 1 || o != 0 {
		t.Errorf("synced-lyrics warnings = %d unsupported / %d other, want exactly 1 unsupported and no double", u, o)
	}
	// The only edit is unstorable, so the write is a byte-identical no-op that still warned.
	out1 := applyToBytes(t, src, plan1)
	if !bytes.Equal(out1, src) {
		t.Errorf("dropping the only edit must be a byte-identical no-op")
	}

	// A repeated Prepare on a fresh editor produces the identical result and the same single
	// warning, proving the drop built fresh slices rather than mutating the editor's state.
	plan2, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(set).Prepare(wl.WithAllowUnsupportedDrop())
	if err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if u, o := countSyncedWarnings(plan2); u != 1 || o != 0 {
		t.Errorf("second prepare warnings = %d unsupported / %d other, want exactly 1 unsupported", u, o)
	}
	if out2 := applyToBytes(t, src, plan2); !bytes.Equal(out2, out1) {
		t.Errorf("repeated Prepare produced different bytes (nondeterministic drop)")
	}
}

// TestSyncedLyricsDropDefaultsToError checks that without WithAllowUnsupportedDrop the whole-item
// capability gate is still a hard error, so a direct library caller who does not opt into
// dropping keeps the strict refusal.
func TestSyncedLyricsDropDefaultsToError(t *testing.T) {
	src := readFixture(t, "../testdata/notags.m4a")
	set := wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: 5 * time.Second, Text: "hi"}}}
	if _, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(set).Prepare(); err == nil {
		t.Error("authoring synced lyrics on MP4 without the drop option must fail, not silently drop")
	}
}
