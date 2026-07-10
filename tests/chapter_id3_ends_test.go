package waxlabel_test

import (
	"os"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// id3ChapterFixtures are the four ID3-CHAP-backed formats and a clean, chapterless fixture
// for each. They share one physical chapter store (internal/id3 CHAP/CTOC), so the
// open-ended-chapter serialization rule must hold identically across all four.
var id3ChapterFixtures = []struct {
	format wl.Format
	path   string
}{
	{wl.FormatMP3, "../testdata/notags.mp3"},
	{wl.FormatAAC, "../testdata/notags.aac"},
	{wl.FormatAIFF, "../testdata/notags.aiff"},
	{wl.FormatWAV, "../testdata/notags.wav"},
}

// TestID3ChapterOpenEndsMaterialized checks that authoring open-ended chapters (End == 0) on
// the ID3-backed formats serializes concrete ends a spec-conforming reader can use, rather
// than the 0xFFFFFFFF "unused" sentinel (~49.7 days) that ffprobe/players take literally:
//   - an interior open chapter reads back with End == the next chapter's start (a gapless
//     interval); and
//   - the trailing open chapter reads back with End == the media duration (ms-floored),
//     never open.
//
// The trailing assertion is on the concrete End value, which the sentinel cannot spoof:
// WaxLabel's own decoder reads 0xFFFFFFFF back as End == 0, so a regression that dropped the
// trailing end to the sentinel would surface here as End == 0 (open), failing the != 0 check.
func TestID3ChapterOpenEndsMaterialized(t *testing.T) {
	for _, fx := range id3ChapterFixtures {
		t.Run(fx.format.String(), func(t *testing.T) {
			src, err := os.ReadFile(fx.path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			doc := mustParseBytes(t, src)
			dur := doc.Properties().Duration()
			if dur <= 500*time.Millisecond {
				t.Fatalf("fixture duration %v is too short for this test", dur)
			}
			plan, err := doc.Edit().SetChapters(
				wl.Chapter{Start: 0, Title: "A"},
				wl.Chapter{Start: 500 * time.Millisecond, Title: "B"},
			).Prepare()
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			chs := mustParseBytes(t, applyToBytes(t, src, plan)).Chapters()
			if len(chs) != 2 {
				t.Fatalf("got %d chapters, want 2", len(chs))
			}
			// Interior open chapter -> the next chapter's start.
			if chs[0].End != 500*time.Millisecond {
				t.Errorf("interior chapter End = %v, want 500ms (next start)", chs[0].End)
			}
			// Trailing open chapter -> a concrete, non-sentinel end derived from the duration.
			if chs[1].End == 0 {
				t.Error("trailing chapter End reads back open: the trailing end regressed to the 0xFFFFFFFF sentinel")
			}
			if want := dur.Truncate(time.Millisecond); chs[1].End != want {
				t.Errorf("trailing chapter End = %v, want %v (ms-floored media duration)", chs[1].End, want)
			}
		})
	}
}

// TestID3ChapterTrailingEndBoundedPastDuration checks the past/at-duration trailing chapter across
// the ID3-backed formats: authoring a chapter that starts past the media duration serializes a
// bounded zero-length end (End == Start) rather than the 0xFFFFFFFF sentinel that ffprobe/players
// render as ~49.7 days. WaxLabel's own decoder reads a bounded start==end back as End == Start and
// the sentinel back as End == 0, so asserting End == Start (not merely != 0) distinguishes the two.
func TestID3ChapterTrailingEndBoundedPastDuration(t *testing.T) {
	for _, fx := range id3ChapterFixtures {
		t.Run(fx.format.String(), func(t *testing.T) {
			src, err := os.ReadFile(fx.path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			doc := mustParseBytes(t, src)
			// A whole-ms start well past the media duration, so the trailing fill bounds it to a
			// zero-length end (max(duration, start) == start) instead of leaving it open.
			past := doc.Properties().Duration().Truncate(time.Millisecond) + 5*time.Second
			plan, err := doc.Edit().SetChapters(
				wl.Chapter{Start: 0, Title: "A"},
				wl.Chapter{Start: past, Title: "Past"},
			).Prepare()
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			chs := mustParseBytes(t, applyToBytes(t, src, plan)).Chapters()
			if len(chs) != 2 {
				t.Fatalf("got %d chapters, want 2", len(chs))
			}
			last := chs[len(chs)-1]
			if last.End == 0 {
				t.Error("past-duration trailing chapter reads back open: the end regressed to the 0xFFFFFFFF sentinel")
			}
			if last.End != last.Start {
				t.Errorf("past-duration trailing chapter End = %v, want == Start %v (bounded zero-length)", last.End, last.Start)
			}
		})
	}
}

// TestID3ChapterPastDurationCopyDiffIdentical pins the cross-package interaction the bounded
// past-duration end depends on: a past-duration chapter now reads back bounded (End == Start)
// instead of open, and core.normalizeReconstructableEnds must still fold that bounded end (always
// >= the media duration) back to open just as it did the old sentinel - so copying the chapters
// into a different-duration destination still diffs as chapters-identical. A regression in that
// fold surfaces here as a spurious chapter difference.
func TestID3ChapterPastDurationCopyDiffIdentical(t *testing.T) {
	srcBytes, err := os.ReadFile("../testdata/notags.mp3")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	srcDoc := mustParseBytes(t, srcBytes)
	past := srcDoc.Properties().Duration().Truncate(time.Millisecond) + 5*time.Second
	plan, err := srcDoc.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "A"},
		wl.Chapter{Start: past, Title: "Past"},
	).Prepare()
	if err != nil {
		t.Fatalf("author chapters: %v", err)
	}
	authored := mustParseBytes(t, applyToBytes(t, srcBytes, plan))

	// Copy the chapters into a destination of a different duration (AAC), then diff.
	dstBytes, err := os.ReadFile("../testdata/notags.aac")
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	tplan, _, err := authored.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	copied := mustParseBytes(t, applyToBytes(t, dstBytes, tplan))

	if !wl.EqualChaptersModuloEnds(authored.Chapters(), copied.Chapters(),
		authored.Properties().Duration(), copied.Properties().Duration()) {
		t.Errorf("past-duration chapters diff as different after copy across durations:\n src=%+v (dur %v)\n dst=%+v (dur %v)",
			authored.Chapters(), authored.Properties().Duration(), copied.Chapters(), copied.Properties().Duration())
	}
}

// TestID3ChapterReapplyOpenEndsNoOp checks that authoring open-ended chapters and then
// re-authoring the identical open-ended chapters collapses to a no-op. After the first write
// the file carries filled ends (interior -> next start, trailing -> duration), so the second
// edit's open-ended list differs from the stored chapters by literal end comparison and does
// NOT satisfy the fast-path no-op gate; it re-renders to byte-identical CHAP frames and
// DowngradeNoOp collapses it via re-projection. This is the one behavior shift Finding 1
// introduces (the no-op now arrives by byte-identity, as it already does for MP4), so it is
// pinned here on MP3 (front-tag path) and WAV (embedded id3-chunk path).
func TestID3ChapterReapplyOpenEndsNoOp(t *testing.T) {
	chapters := []wl.Chapter{
		{Start: 0, Title: "A"},
		{Start: 300 * time.Millisecond, Title: "B"},
		{Start: 600 * time.Millisecond, Title: "C"},
	}
	for _, fx := range []struct {
		format wl.Format
		path   string
	}{
		{wl.FormatMP3, "../testdata/notags.mp3"},
		{wl.FormatWAV, "../testdata/notags.wav"},
	} {
		t.Run(fx.format.String(), func(t *testing.T) {
			src, err := os.ReadFile(fx.path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			plan, err := mustParseBytes(t, src).Edit().SetChapters(chapters...).Prepare()
			if err != nil {
				t.Fatalf("first Prepare: %v", err)
			}
			written := applyToBytes(t, src, plan)
			rePlan, err := mustParseBytes(t, written).Edit().SetChapters(chapters...).Prepare()
			if err != nil {
				t.Fatalf("reapply Prepare: %v", err)
			}
			if !rePlan.IsNoOp() {
				t.Errorf("re-applying identical open-ended chapters was not a no-op; changes = %v", rePlan.Changes())
			}
		})
	}
}
