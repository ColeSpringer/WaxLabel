package waxlabel_test

import (
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// TestSyncedLyricsReadCapTruncationWarns synthesizes a synced-lyrics set larger than the
// modeled per-set line cap (65,536), writes it, and re-parses it. Both the ID3 SYLT store
// (MP3) and the VorbisComment LRC store (FLAC) must cap the surfaced set at the limit AND
// surface a WarnSyncedLyricsTruncated, so the drop is not silent. The write path does not
// cap, so the stored container genuinely carries the over-cap set the read then truncates.
func TestSyncedLyricsReadCapTruncationWarns(t *testing.T) {
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
			re := mustParseBytes(t, applyToBytes(t, src, plan))

			got := re.SyncedLyrics()
			if len(got) != 1 {
				t.Fatalf("re-parsed synced-lyrics sets = %d, want 1", len(got))
			}
			if n := len(got[0].Lines); n != cap {
				t.Errorf("re-parsed lines = %d, want the cap %d", n, cap)
			}
			if !hasWarning(re, wl.WarnSyncedLyricsTruncated) {
				t.Errorf("reading a >cap synced-lyrics set must warn WarnSyncedLyricsTruncated, but none was surfaced")
			}
		})
	}
}
