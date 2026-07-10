package waxlabel_test

import (
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// TestSyncedLyricsWriteCapTruncates authors a synced-lyrics set larger than the modeled
// per-set line cap (65,536) and prepares a write. The plan truncates the set to the cap
// before writing, surfaces a WarnSyncedLyricsTruncated so the drop is not silent, and the
// re-parsed file carries exactly the cap. Both the ID3 SYLT store (MP3) and the VorbisComment
// LRC store (FLAC) truncate at plan time, so the written container never carries the over-cap
// set (unlike the old behavior, where an over-cap set was written whole and read back short
// unwarned).
func TestSyncedLyricsWriteCapTruncates(t *testing.T) {
	const cap = 1 << 16
	const over = cap + 3
	lines := make([]wl.SyncedLine, over)
	for i := range lines {
		// 10 ms spacing keeps every timestamp distinct and well within both stores' ceilings.
		lines[i] = wl.SyncedLine{Time: time.Duration(i) * 10 * time.Millisecond, Text: "x"}
	}
	set := wl.SyncedLyrics{Language: "eng", Lines: lines}

	for _, tc := range []struct{ name, fixture string }{
		{"SYLT/MP3", "../testdata/notags.mp3"},
		{"LRC/FLAC", "../testdata/notags.flac"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := readFixture(t, tc.fixture)
			plan, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(set).Prepare()
			if err != nil {
				t.Fatalf("prepare over-cap synced lyrics: %v", err)
			}
			// The plan must warn the author that lines were dropped on write.
			if !planHasWarning(plan, wl.WarnSyncedLyricsTruncated) {
				t.Errorf("authoring a >cap synced-lyrics set must warn WarnSyncedLyricsTruncated, but none was surfaced")
			}
			re := mustParseBytes(t, applyToBytes(t, src, plan))

			got := re.SyncedLyrics()
			if len(got) != 1 {
				t.Fatalf("re-parsed synced-lyrics sets = %d, want 1", len(got))
			}
			if n := len(got[0].Lines); n != cap {
				t.Errorf("re-parsed lines = %d, want the cap %d", n, cap)
			}
			// The written file is exactly at the cap, so a fresh read does not re-truncate.
			if hasWarning(re, wl.WarnSyncedLyricsTruncated) {
				t.Errorf("a file written at the cap must not surface a read-path truncation warning")
			}
		})
	}
}

// planHasWarning reports whether a prepared plan's report carries the given warning code.
func planHasWarning(plan *wl.Plan, code wl.WarningCode) bool {
	for _, w := range plan.Report().Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}
