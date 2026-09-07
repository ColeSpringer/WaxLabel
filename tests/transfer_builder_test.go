package waxlabel_test

import (
	"errors"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// changeFor returns the plan change for key, and whether it is present.
func changeFor(changes []tag.Change, key tag.Key) (tag.Change, bool) {
	for _, c := range changes {
		if c.Key == key {
			return c, true
		}
	}
	return tag.Change{}, false
}

// mp3WithSyncedLyrics returns notags.mp3 carrying one synced-lyrics set, a destination for
// the lyrics-clearing tests.
func mp3WithSyncedLyrics(t *testing.T) []byte {
	t.Helper()
	return writeBack(t, notagsMP3, func(ed *wl.Editor) {
		ed.SetSyncedLyrics(wl.SyncedLyrics{
			Lines: []wl.SyncedLine{{Time: 0, Text: "one"}, {Time: time.Second, Text: "two"}},
		})
	})
}

// TestTransferSetChaptersReplacesCarriedList: a replacement list is both what the report
// grades and what the write lands, so the source's own chapters never reach the destination.
func TestTransferSetChaptersReplacesCarriedList(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	if len(src.Chapters()) < 3 {
		t.Fatalf("setup: %s should carry several chapters, got %d", sampleM4B, len(src.Chapters()))
	}
	dstBytes := readFixture(t, notagsMP3)
	repl := []wl.Chapter{
		{Start: 0, End: time.Second, Title: "Cut A"},
		{Start: time.Second, Title: "Cut B"},
	}

	plan, report, err := src.Transfer().SetChapters(repl...).Prepare(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if it := chapterItem(t, report); it.Count != 2 {
		t.Errorf("report chapter count = %d, want 2 (the replacement list)", it.Count)
	}
	got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters()
	if len(got) != 2 {
		t.Fatalf("written chapters = %d, want 2: %+v", len(got), got)
	}
	if got[0].Title != "Cut A" || got[1].Title != "Cut B" {
		t.Errorf("written titles = %q/%q, want Cut A/Cut B", got[0].Title, got[1].Title)
	}
	if got[0].Start != 0 || got[1].Start != time.Second {
		t.Errorf("written starts = %v/%v, want 0/1s", got[0].Start, got[1].Start)
	}
}

// TestTransferReplacementEndAtSourceEOFStaysLiteral: the run-to-end-of-file reopen applies
// only to the source's own list. A replacement is authored against the destination, so an
// end at the source's duration is written literally rather than refilled to the
// destination's longer end.
func TestTransferReplacementEndAtSourceEOFStaysLiteral(t *testing.T) {
	src := mustParseFile(t, chaptersMKA)
	dstBytes := readFixture(t, notagsMP3)
	srcEnd := src.Properties().Duration().Truncate(time.Millisecond)
	dstDur := mustParseBytes(t, dstBytes).Properties().Duration()
	if srcEnd <= 0 || dstDur <= srcEnd {
		t.Fatalf("setup: need a destination longer than the source, got %v vs %v", dstDur, srcEnd)
	}

	repl := []wl.Chapter{{Start: 0, Title: "A"}, {Start: srcEnd / 2, End: srcEnd, Title: "B"}}
	plan, _, err := src.Transfer().SetChapters(repl...).Prepare(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters()
	if len(got) != 2 || got[1].End != srcEnd {
		t.Fatalf("replacement final end = %v, want the literal %v: %+v", got[len(got)-1].End, srcEnd, got)
	}

	// The default carry opens the same end, so the destination refills it to its own.
	planCarry, _, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	carried := mustParseBytes(t, applyToBytes(t, dstBytes, planCarry)).Chapters()
	if last := carried[len(carried)-1]; last.End <= srcEnd {
		t.Errorf("carried final end = %v, want it refilled past the source end %v", last.End, srcEnd)
	}
}

// TestTransferSetChaptersNoneClearsDestination: an explicit empty replacement means "no
// chapters" and removes the destination's own, even though a chapterless source has
// nothing to report.
func TestTransferSetChaptersNoneClearsDestination(t *testing.T) {
	src := mustParseFile(t, notagsFLAC)
	if len(src.Chapters()) != 0 {
		t.Fatalf("setup: %s should carry no chapters", notagsFLAC)
	}
	dstBytes := readFixture(t, sampleM4B)
	dst := mustParseBytes(t, dstBytes)
	if len(dst.Chapters()) == 0 {
		t.Fatalf("setup: %s should carry chapters", sampleM4B)
	}

	plan, report, err := src.Transfer().SetChapters().Prepare(dst)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	for _, it := range report.Items {
		if it.Kind == wl.TransferChapter {
			t.Errorf("report should carry no chapter item for a chapterless source, got %+v", it)
		}
	}
	if _, ok := changeFor(plan.Changes(), "chapters"); !ok {
		t.Errorf("plan changes should show the chapters removed, got %v", plan.Changes())
	}
	if got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters(); len(got) != 0 {
		t.Errorf("written chapters = %d, want none: %+v", len(got), got)
	}
}

// TestTransferSourceWithoutChaptersKeepsDestination: a source that simply has no chapters
// is not an explicit clear, so the destination keeps its own.
func TestTransferSourceWithoutChaptersKeepsDestination(t *testing.T) {
	src := mustParseFile(t, notagsFLAC)
	dstBytes := readFixture(t, sampleM4B)
	dst := mustParseBytes(t, dstBytes)
	want := len(dst.Chapters())

	plan, _, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters(); len(got) != want {
		t.Errorf("written chapters = %d, want the destination's %d", len(got), want)
	}
}

// TestTransferSetSyncedLyricsReplaces: a replacement set is graded and written in place of
// the source's own.
func TestTransferSetSyncedLyricsReplaces(t *testing.T) {
	srcBytes := mp3WithSyncedLyrics(t)
	src := mustParseBytes(t, srcBytes)
	if len(src.SyncedLyrics()) != 1 {
		t.Fatalf("setup: source should carry one synced-lyrics set, got %d", len(src.SyncedLyrics()))
	}
	dstBytes := readFixture(t, notagsFLAC)

	repl := wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: 2 * time.Second, Text: "replaced"}}}
	plan, _, err := src.Transfer().SetSyncedLyrics(repl).Prepare(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).SyncedLyrics()
	if len(got) != 1 || len(got[0].Lines) != 1 || got[0].Lines[0].Text != "replaced" {
		t.Errorf("written synced lyrics = %+v, want the single replacement line", got)
	}
}

// TestTransferSetSyncedLyricsNoneClearsDestination: an explicit empty replacement removes
// the destination's own sets.
func TestTransferSetSyncedLyricsNoneClearsDestination(t *testing.T) {
	src := mustParseFile(t, notagsFLAC)
	dstBytes := mp3WithSyncedLyrics(t)
	dst := mustParseBytes(t, dstBytes)
	if len(dst.SyncedLyrics()) != 1 {
		t.Fatalf("setup: destination should carry one synced-lyrics set, got %d", len(dst.SyncedLyrics()))
	}

	plan, _, err := src.Transfer().SetSyncedLyrics().Prepare(dst)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).SyncedLyrics(); len(got) != 0 {
		t.Errorf("written synced lyrics = %+v, want none", got)
	}
}

// TestTransferReplacementChaptersSorted: an out-of-order replacement is sorted by start,
// matching [wl.Editor.SetChapters].
func TestTransferReplacementChaptersSorted(t *testing.T) {
	src := mustParseFile(t, notagsFLAC)
	dstBytes := readFixture(t, notagsMP3)
	repl := []wl.Chapter{{Start: time.Second, Title: "B"}, {Start: 0, Title: "A"}}

	plan, _, err := src.Transfer().SetChapters(repl...).Prepare(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters()
	if len(got) != 2 || got[0].Title != "A" || got[1].Title != "B" {
		t.Errorf("written chapters = %+v, want A then B", got)
	}
}

// TestTransferPlanMatchesPrepareWithReplacement: the format-level simulation grades the
// same replacement list the executable plan writes.
func TestTransferPlanMatchesPrepareWithReplacement(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	dst := mustParseBytes(t, readFixture(t, notagsMP3))
	repl := []wl.Chapter{{Start: 0, Title: "Cut A"}, {Start: time.Second, Title: "Cut B"}}

	simulated, err := src.Transfer().SetChapters(repl...).Plan(dst.Format())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	_, prepared, err := src.Transfer().SetChapters(repl...).Prepare(dst)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(simulated.Items) != len(prepared.Items) {
		t.Fatalf("item counts differ: %d vs %d", len(simulated.Items), len(prepared.Items))
	}
	for i := range simulated.Items {
		if simulated.Items[i] != prepared.Items[i] {
			t.Errorf("item %d: Plan %+v, Prepare %+v", i, simulated.Items[i], prepared.Items[i])
		}
	}
}

// TestTransferReplacementPastDurationWarns: a replacement start past the destination's
// playable length is a destination-fit problem the copy still surfaces, while the
// source-authoring sanity warnings stay suppressed.
func TestTransferReplacementPastDurationWarns(t *testing.T) {
	src := mustParseFile(t, notagsFLAC)
	dstBytes := readFixture(t, notagsMP3)
	dst := mustParseBytes(t, dstBytes)
	past := dst.Properties().Duration() + time.Minute

	// Two chapters share a start, so the duplicate warning would fire if the carried
	// suppression were not in force.
	plan, _, err := src.Transfer().SetChapters(
		wl.Chapter{Start: 0, Title: "A"},
		wl.Chapter{Start: 0, Title: "A again"},
		wl.Chapter{Start: past, Title: "B"},
	).Prepare(dst)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	ws := plan.Report().Warnings
	if !reportHasWarning(ws, wl.WarnChapterPastDuration) {
		t.Errorf("expected a past-duration warning; got %v", ws)
	}
	if reportHasWarning(ws, wl.WarnDuplicateChapter) {
		t.Errorf("a carried list must not raise the source-authoring duplicate warning; got %v", ws)
	}
}

// TestTransferWrappersMatchBuilder: PrepareTransfer is the no-replacement builder.
func TestTransferWrappersMatchBuilder(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	dstBytes := readFixture(t, notagsMP3)

	_, viaWrapper, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	_, viaBuilder, err := src.Transfer().Prepare(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("Transfer().Prepare: %v", err)
	}
	if len(viaWrapper.Items) != len(viaBuilder.Items) {
		t.Fatalf("item counts differ: %d vs %d", len(viaWrapper.Items), len(viaBuilder.Items))
	}
	for i := range viaWrapper.Items {
		if viaWrapper.Items[i] != viaBuilder.Items[i] {
			t.Errorf("item %d: wrapper %+v, builder %+v", i, viaWrapper.Items[i], viaBuilder.Items[i])
		}
	}
}

// TestTransferZeroDocumentRefused: a zero-value document has no media to copy.
func TestTransferZeroDocumentRefused(t *testing.T) {
	var zero wl.Document
	if _, err := zero.Transfer().Plan(wl.FormatFLAC); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("Plan on a zero document = %v, want ErrInvalidData", err)
	}
	dst := mustParseBytes(t, readFixture(t, notagsMP3))
	if _, _, err := zero.Transfer().Prepare(dst); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("Prepare on a zero document = %v, want ErrInvalidData", err)
	}
}
