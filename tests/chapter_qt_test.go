package waxlabel_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// ffmpeg writes a QuickTime chapter text track alongside the chpl when it adds chapters;
// testfile.MP4QTFile builds one, so the reader is exercised without ffmpeg.

func TestMP4QTChapterRead(t *testing.T) {
	startsMS := []int{0, 5000, 12000}
	titles := []string{"Cold Open", "Act One", "Act Two"}
	data := testfile.MP4QTFile(startsMS, titles)
	doc := mustParseBytes(t, data)
	chs := doc.Chapters()
	if len(chs) != 3 {
		t.Fatalf("got %d QuickTime chapters, want 3", len(chs))
	}
	for i, want := range titles {
		if chs[i].Title != want {
			t.Errorf("chapter %d title = %q, want %q", i, chs[i].Title, want)
		}
		if chs[i].Start != time.Duration(startsMS[i])*time.Millisecond {
			t.Errorf("chapter %d start = %v, want %dms", i, chs[i].Start, startsMS[i])
		}
	}
	// The QuickTime track carries End from the next sample's start.
	if chs[0].End != 5*time.Second {
		t.Errorf("chapter 0 End = %v, want 5s", chs[0].End)
	}
	// The native view notes the chapter track.
	hasQT := false
	for _, e := range doc.Native().Describe() {
		if e.Kind == "moov chapter track" {
			hasQT = true
		}
	}
	if !hasQT {
		t.Error("native view should note the QuickTime chapter track")
	}
}

func TestMP4ChapterWriteCreatesQTTrack(t *testing.T) {
	// Setting chapters on a file with an audio track but no chapters builds a QuickTime chapter text
	// track (the form iTunes/Apple Books read) alongside the chpl.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "Book"))
	re := execChapters(t, data, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "Intro"},
			wl.Chapter{Start: 5 * time.Second, Title: "Body"},
			wl.Chapter{Start: 9 * time.Second, Title: "Outro"},
		)
	})
	chs := re.Chapters()
	if len(chs) != 3 || chs[1].Title != "Body" || chs[1].Start != 5*time.Second {
		t.Fatalf("chapters read back wrong: %+v", chs)
	}
	if chs[0].End != 5*time.Second {
		t.Errorf("End not carried from the QuickTime track: %v", chs[0].End)
	}
	hasQT := false
	for _, e := range re.Native().Describe() {
		if e.Kind == "moov chapter track" {
			hasQT = true
		}
	}
	if !hasQT {
		t.Error("native view should show the written QuickTime chapter track")
	}
	if re.Fields().Title != "Book" {
		t.Errorf("tag lost on a chapter create: %q", re.Fields().Title)
	}
}

// mp4AudioTrakTkhd builds an audio trak with a tkhd carrying the given track id and any extra
// children, such as a tref.
func mp4AudioTrakTkhd(trackID int, audioStco uint32, extra ...[]byte) []byte {
	tkhd := testfile.MP4Atom("tkhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), testfile.BE32(trackID), make([]byte, 4)))
	stbl := testfile.MP4Atom("stbl", slices.Concat(testfile.MP4StsdAudio(), testfile.MP4Stco(audioStco)))
	minf := testfile.MP4Atom("minf", stbl)
	mdia := testfile.MP4Atom("mdia", slices.Concat(testfile.MP4HdlrSoun(), testfile.MP4Mdhd(44100), minf))
	return testfile.MP4Atom("trak", slices.Concat(tkhd, slices.Concat(extra...), mdia))
}

func TestMP4ChapterEditKeepsMoovFingerprinted(t *testing.T) {
	// A chapter edit appends a chapter mdat at end-of-file. On a non-faststart file (moov after the
	// audio mdat) that would sandwich the moov between two mdats, in the fingerprint's un-hashed gap.
	build := func(stcoOff uint32) []byte {
		moov := testfile.MP4Atom("moov", mp4AudioTrakTkhd(1, stcoOff))
		mdat := testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 200))
		return slices.Concat(testfile.MP4Ftyp(), mdat, moov) // moov AFTER mdat (non-faststart)
	}
	tmp := build(0)
	data := build(uint32(bytes.Index(tmp, []byte("mdat")) + 4))

	edit := func(title string) []byte {
		plan, err := mustParseBytes(t, data).Edit().
			Set(tag.Title, title).
			SetChapters(wl.Chapter{Start: 0, Title: "Chap"}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		return applyToBytes(t, data, plan)
	}
	outA, outB := edit("AAAA"), edit("BBBB") // differ only in the moov title, same length
	if len(outA) != len(outB) {
		t.Fatalf("outputs differ in size (%d vs %d); test cannot isolate the fingerprint", len(outA), len(outB))
	}
	idA, idB := mustParseBytes(t, outA).Identity(), mustParseBytes(t, outB).Identity()
	if !idA.HasFinger || !idB.HasFinger {
		t.Fatal("expected structural fingerprints")
	}
	if idA.Fingerprint == idB.Fingerprint {
		t.Error("the moov is in the un-hashed fingerprint gap after a chapter edit (chapter mdat not excluded from essence)")
	}
}

// chapter start past the QuickTime stts 32-bit field (~49.7 days at the movie timescale) surfaces a
// warning and still writes a reparsable file. The uint64 Nero chpl keeps the exact start.
func TestMP4ChapterStartOverflowWarns(t *testing.T) {
	build := func(stcoOff uint32) []byte {
		moov := testfile.MP4Atom("moov", mp4AudioTrakTkhd(1, stcoOff))
		return slices.Concat(testfile.MP4Ftyp(), moov, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 64)))
	}
	tmp := build(0)
	data := build(uint32(bytes.Index(tmp, []byte("mdat")) + 4))

	plan, err := mustParseBytes(t, data).Edit().
		SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 50 * 24 * time.Hour, Title: "B"}, // ~50 days > MaxUint32 ms units
		).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	overflow := false
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnChapterStartOverflow {
			overflow = true
		}
	}
	if !overflow {
		t.Errorf("expected a chapter-start-overflow warning; got %v", plan.Report().Warnings)
	}
	// The clamp must not corrupt the output: it still reparses with both chapters.
	if chs := mustParseBytes(t, applyToBytes(t, data, plan)).Chapters(); len(chs) != 2 {
		t.Errorf("reparsed chapters = %d, want 2", len(chs))
	}
}

// checks the direct-edit warning for MP4's start+title-only chapter storage.
func TestMP4SetChaptersMetadataDroppedWarns(t *testing.T) {
	build := func(stcoOff uint32) []byte {
		moov := testfile.MP4Atom("moov", mp4AudioTrakTkhd(1, stcoOff))
		return slices.Concat(testfile.MP4Ftyp(), moov, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 64)))
	}
	tmp := build(0)
	data := build(uint32(bytes.Index(tmp, []byte("mdat")) + 4))

	// A gapped end (chapter A ends at 1s, chapter B starts at 3s) is metadata MP4 drops.
	plan, err := mustParseBytes(t, data).Edit().
		SetChapters(
			wl.Chapter{Start: 0, End: time.Second, Title: "A"},
			wl.Chapter{Start: 3 * time.Second, Title: "B"},
		).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnChapterMetadataDropped) {
		t.Errorf("MP4 SetChapters with a gapped end should warn chapter-metadata-dropped; got %v", plan.Report().Warnings)
	}
	if planWarns(t, plan, wl.WarnChapterEndsDropped) {
		t.Error("MP4 must not emit chapter-ends-dropped (that is the Matroska CLI limitation)")
	}

	plain, err := mustParseBytes(t, data).Edit().
		SetChapters(wl.Chapter{Start: 0, Title: "A"}, wl.Chapter{Start: 3 * time.Second, Title: "B"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if planWarns(t, plain, wl.WarnChapterMetadataDropped) {
		t.Errorf("plain MP4 chapters must not warn chapter-metadata-dropped; got %v", plain.Report().Warnings)
	}
}

func TestMP4ChapterMaxTrackIDNoCreate(t *testing.T) {
	// A track already holding the max track id (0xFFFFFFFF) leaves no free id, so a chapter create must
	// not build a track with the wrapped invalid id 0; it falls back to the chpl, and the chapters
	// still read.
	build := func(stcoOff uint32) []byte {
		// -1 encodes the max track id 0xFFFFFFFF through BE32's uint32 conversion on every int width;
		// int(uint32(0xFFFFFFFF)) overflows a 32-bit int at compile time.
		moov := testfile.MP4Atom("moov", mp4AudioTrakTkhd(-1, stcoOff))
		return slices.Concat(testfile.MP4Ftyp(), moov, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 64)))
	}
	tmp := build(0)
	data := build(uint32(bytes.Index(tmp, []byte("mdat")) + 4))

	plan, err := mustParseBytes(t, data).Edit().
		SetChapters(wl.Chapter{Start: 0, Title: "A"}, wl.Chapter{Start: 2 * time.Second, Title: "B"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if chs := re.Chapters(); len(chs) != 2 || chs[0].Title != "A" {
		t.Errorf("chpl-fallback chapters wrong: %+v", chs)
	}
	for _, e := range re.Native().Describe() {
		if e.Kind == "moov chapter track" {
			t.Error("a chapter track was created despite no free track id")
		}
	}
}

func TestMP4ChapterTrefPreservesOtherRefs(t *testing.T) {
	// An audio tref holding a non-"chap" reference must keep it when a chapter create adds the "chap".
	otherRef := testfile.MP4Atom("hint", testfile.BE32(7)) // a non-chap reference
	tref := testfile.MP4Atom("tref", otherRef)
	build := func(stcoOff uint32) []byte {
		moov := testfile.MP4Atom("moov", mp4AudioTrakTkhd(1, stcoOff, tref))
		return slices.Concat(testfile.MP4Ftyp(), moov, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 64)))
	}
	tmp := build(0)
	data := build(uint32(bytes.Index(tmp, []byte("mdat")) + 4))

	plan, err := mustParseBytes(t, data).Edit().
		SetChapters(wl.Chapter{Start: 0, Title: "One"}, wl.Chapter{Start: 2 * time.Second, Title: "Two"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("hint")) {
		t.Error("a non-chap tref reference was dropped when adding chapters")
	}
	if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 || chs[1].Title != "Two" {
		t.Errorf("chapters wrong: %+v", chs)
	}
}

func TestMP4ChapterClearRemovesDanglingChap(t *testing.T) {
	// A file with a chpl and an audio tref "chap" that does not resolve to a text
	// track (dangling). ClearChapters drops the chpl and must also strip the
	// dangling "chap" reference, per the chapter-clear contract.
	chpl := mp4Chpl(1, []time.Duration{0, 5 * time.Second}, []string{"X", "Y"})
	dangling := testfile.MP4Atom("tref", testfile.MP4Atom("chap", testfile.BE32(99))) // track 99 does not exist
	build := func(stcoOff uint32) []byte {
		trak := mp4AudioTrakTkhd(1, stcoOff, dangling)
		moov := testfile.MP4Atom("moov", slices.Concat(trak, testfile.MP4Atom("udta", chpl)))
		return slices.Concat(testfile.MP4Ftyp(), moov, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 64)))
	}
	tmp := build(0)
	data := build(uint32(bytes.Index(tmp, []byte("mdat")) + 4))

	doc := mustParseBytes(t, data)
	if len(doc.Chapters()) != 2 {
		t.Fatalf("setup: chapters = %d, want 2 (from chpl)", len(doc.Chapters()))
	}
	plan, err := doc.Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	re := mustParseBytes(t, out)
	if len(re.Chapters()) != 0 {
		t.Errorf("chapters remain after clear: %+v", re.Chapters())
	}
	if bytes.Contains(out, []byte("chap")) {
		t.Error("the dangling tref \"chap\" survived ClearChapters")
	}
}

func TestMP4ChapterClearThenSetOnReturnedDocument(t *testing.T) {
	// ClearChapters deletes the audio tref; SetChapters on the Document that write returned must
	// re-insert one, not splice a tref over the audio track's mdia.
	data := testfile.MP4QTFile([]int{0, 5000}, []string{"A", "B"})
	plan1, err := mustParseBytes(t, data).Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w1 writerTo
	res1, _, err := plan1.Execute(context.Background(), wl.WriteTo(&w1, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(res1.Chapters()) != 0 {
		t.Fatalf("chapters not cleared: %d", len(res1.Chapters()))
	}

	plan2, err := res1.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "New One"},
		wl.Chapter{Start: 2 * time.Second, Title: "New Two"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, w1.b, plan2))
	if chs := re.Chapters(); len(chs) != 2 || chs[0].Title != "New One" {
		t.Errorf("chapters wrong after clear-then-set: %+v", chs)
	}
	if got := re.Properties().First().Codec; got != "AAC" {
		t.Errorf("audio track corrupted by clear-then-set (mdia spliced over?): codec = %q", got)
	}
}

func TestMP4ChapterCreateThenClearOnReturnedDocument(t *testing.T) {
	// SetChapters on a chapterless file inserts an audio tref; ClearChapters on the Document that
	// write returned must remove it, not leave it dangling.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	plan1, err := mustParseBytes(t, data).Edit().
		SetChapters(wl.Chapter{Start: 0, Title: "A"}, wl.Chapter{Start: 3 * time.Second, Title: "B"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w1 writerTo
	res1, _, err := plan1.Execute(context.Background(), wl.WriteTo(&w1, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := res1.Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, w1.b, plan2)
	re := mustParseBytes(t, out2)
	if len(re.Chapters()) != 0 {
		t.Errorf("chapters remain after create-then-clear: %+v", re.Chapters())
	}
	if bytes.Contains(out2, []byte("chap")) {
		t.Error("a dangling tref \"chap\" survived create-then-clear")
	}
}

func TestMP4ChaptersSortedByStart(t *testing.T) {
	// Chapters set out of order are stored sorted by start time, so the chpl and the QuickTime track
	// encode forward spans. A reparse reads them in order with no source conflict.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	re := execChapters(t, data, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 6 * time.Second, Title: "Third"},
			wl.Chapter{Start: 0, Title: "First"},
			wl.Chapter{Start: 3 * time.Second, Title: "Second"},
		)
	})
	chs := re.Chapters()
	if len(chs) != 3 || chs[0].Title != "First" || chs[1].Title != "Second" || chs[2].Title != "Third" {
		t.Fatalf("chapters not sorted by start: %+v", chs)
	}
	if chs[0].Start != 0 || chs[1].Start != 3*time.Second || chs[2].Start != 6*time.Second {
		t.Errorf("chapter starts wrong after sort: %+v", chs)
	}
	if chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Error("sorted chapters should make the chpl and QuickTime track agree (no conflict)")
	}
}

func TestMP4ChapterClearRemovesQTTrack(t *testing.T) {
	// Clearing chapters on a file with a QuickTime chapter track removes that track and the audio
	// track's tref "chap", not just the chpl.
	data := testfile.MP4QTFile([]int{0, 5000}, []string{"One", "Two"})
	re := execChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.ClearChapters() })
	if chs := re.Chapters(); len(chs) != 0 {
		t.Fatalf("chapters remain after clear: %+v", chs)
	}
	for _, e := range re.Native().Describe() {
		if e.Kind == "moov chapter track" {
			t.Error("QuickTime chapter track survived ClearChapters")
		}
	}
	plan, err := mustParseBytes(t, data).Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, data, plan); bytes.Contains(out, []byte("chap")) {
		t.Error("a tref \"chap\" reference survived ClearChapters")
	}
}

func TestMP4ChapterReEditQTResult(t *testing.T) {
	// A chapter edit on a QuickTime-track file, then a *tag* edit that grows the metadata on the
	// Document the chapter write returned.
	data := testfile.MP4QTFile([]int{0, 5000}, []string{"V1 A", "V1 B"})
	plan1, err := mustParseBytes(t, data).Edit().
		SetChapters(wl.Chapter{Start: 0, Title: "Edited A"}, wl.Chapter{Start: 4 * time.Second, Title: "Edited B"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w1 writerTo
	res1, _, err := plan1.Execute(context.Background(), wl.WriteTo(&w1, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}

	plan2, err := res1.Edit().Set(tag.Title, "A Long Title That Forces The Metadata Region To Grow And Shift Offsets").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, w1.b, plan2))
	if chs := re.Chapters(); len(chs) != 2 || chs[0].Title != "Edited A" || chs[1].Title != "Edited B" {
		t.Errorf("chapters corrupted by a follow-up tag edit: %+v", chs)
	}
	if re.Fields().Title != "A Long Title That Forces The Metadata Region To Grow And Shift Offsets" {
		t.Errorf("tag edit after a chapter edit did not apply: %q", re.Fields().Title)
	}
}

// A tag edit grows ilst inside meta, and a follow-up SetChapters on the returned document must
// splice chpl into bytes whose meta size stays self-consistent.
func TestMP4TagGrowThenChapterEditReparses(t *testing.T) {
	src := readFixture(t, sampleM4B)
	const grownTitle = "A Much Longer Title That Forces The ilst And meta Region To Grow Substantially"

	// 1. Grow ilst, and therefore the enclosing meta region.
	plan1, err := mustParseBytes(t, src).Edit().Set(tag.Title, grownTitle).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w1 writerTo
	res1, _, err := plan1.Execute(context.Background(), wl.WriteTo(&w1, wl.BytesSource(src)))
	if err != nil {
		t.Fatal(err)
	}

	// 2. SetChapters on the returned document splices chpl into its cached udta bytes.
	plan2, err := res1.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "Chained A"},
		wl.Chapter{Start: 3 * time.Second, Title: "Chained B"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	// 3. The twice-edited output reparses with both edits.
	re := mustParseBytes(t, applyToBytes(t, w1.b, plan2))
	if chs := re.Chapters(); len(chs) != 2 || chs[0].Title != "Chained A" || chs[1].Title != "Chained B" {
		t.Errorf("chained chapters corrupted: %+v", chs)
	}
	if re.Fields().Title != grownTitle {
		t.Errorf("grown title lost across the chained edit: %q", re.Fields().Title)
	}
}

func TestMP4ChapterEditRealFixtureQTTrack(t *testing.T) {
	// Editing chapters on the ffmpeg M4B fixture (chpl + a QuickTime track) replaces
	// the QuickTime track in place: a fresh read returns the new chapters.
	src := readFixture(t, sampleM4B)
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "Fresh Start"},
			wl.Chapter{Start: 4 * time.Second, Title: "Fresh Middle"},
		)
	})
	chs := re.Chapters()
	if len(chs) != 2 || chs[0].Title != "Fresh Start" || chs[1].Title != "Fresh Middle" {
		t.Fatalf("fixture chapters not rewritten: %+v", chs)
	}
	if chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Error("the rewritten QuickTime track should agree with the chpl (no conflict)")
	}
	if re.Fields().Title != "Sample Audiobook" {
		t.Errorf("fixture tag lost on chapter edit: %q", re.Fields().Title)
	}
}

func TestMP4ChapterNonZeroStartRoundTrip(t *testing.T) {
	// A multi-chapter list whose first start is not zero round-trips with every start preserved: the
	// QuickTime track's leading empty edit carries the offset.
	src := readFixture(t, sampleM4B)
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 4 * time.Second, Title: "Four"},
			wl.Chapter{Start: 9 * time.Second, Title: "Nine"},
		)
	})
	chs := re.Chapters()
	if len(chs) != 2 || chs[0].Start != 4*time.Second || chs[0].Title != "Four" {
		t.Fatalf("first chapter not preserved at 4s: %+v", chs)
	}
	if chs[1].Start != 9*time.Second {
		t.Errorf("second chapter start = %v, want 9s", chs[1].Start)
	}
	if chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Error("a non-zero first start must not self-report a chapter-source-conflict")
	}
}

func TestMP4ChapterSingleNonZeroStart(t *testing.T) {
	// One chapter at a non-zero start keeps its start and materializes its End at the movie duration:
	// the text track has no "open" on the wire, so the span to the movie end is what gets reported.
	src := readFixture(t, sampleM4B)
	set := func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(wl.Chapter{Start: 4 * time.Second, Title: "Only"})
	}
	re := execChapters(t, src, set)
	chs := re.Chapters()
	if len(chs) != 1 || chs[0].Start != 4*time.Second || chs[0].Title != "Only" {
		t.Fatalf("single chapter not preserved at 4s: %+v", chs)
	}
	if chs[0].End != 9*time.Second {
		t.Errorf("a lone chapter's End = %v, want 9s (the movie duration it spans to)", chs[0].End)
	}
	if chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Error("a single non-zero-start chapter must not report a source conflict")
	}
	// Idempotency: re-applying the same edit (still open-ended) to the written file changes nothing; an
	// open last chapter and one ending at the movie duration are the same bytes.
	plan2, err := set(re.Edit()).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.IsNoOp() {
		t.Errorf("a second identical chapter set should be a no-op; operations: %v", plan2.Report().Operations)
	}
}

func TestMP4ChapterSourceConflict(t *testing.T) {
	// A chpl and a QuickTime track that disagree (different titles) must warn, and
	// the richer QuickTime representation wins the projection.
	startsMS := []int{0, 5000}
	qtTitles := []string{"QT Intro", "QT Body"}
	chpl := mp4Chpl(1, []time.Duration{0, 5 * time.Second}, []string{"Nero Intro", "Nero Body"})
	data := testfile.MP4QTFile(startsMS, qtTitles, chpl)

	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnChapterSourceConflict) {
		t.Errorf("expected a chapter-source-conflict warning; got %v", doc.Warnings())
	}
	if chs := doc.Chapters(); len(chs) != 2 || chs[0].Title != "QT Intro" {
		t.Errorf("conflict resolution should prefer the QuickTime track; got %+v", chs)
	}
}

func TestMP4ChapterAgreementNoConflict(t *testing.T) {
	// A chpl and a QuickTime track that agree must NOT warn.
	startsMS := []int{0, 8000}
	titles := []string{"Same A", "Same B"}
	chpl := mp4Chpl(1, []time.Duration{0, 8 * time.Second}, titles)
	data := testfile.MP4QTFile(startsMS, titles, chpl)
	doc := mustParseBytes(t, data)
	if hasWarning(doc, wl.WarnChapterSourceConflict) {
		t.Errorf("agreeing chapter sources should not warn; got %v", doc.Warnings())
	}
}

func TestMP4ChapterEditRewritesQTTrack(t *testing.T) {
	// Editing chapters on a file with a QuickTime chapter track rebuilds that track too: no
	// chapters-stale warning, and a fresh read returns the edit from the QuickTime track (the
	// representation iTunes/Apple Books use).
	data := testfile.MP4QTFile([]int{0, 5000}, []string{"Original One", "Original Two"})
	plan, err := mustParseBytes(t, data).Edit().
		SetChapters(
			wl.Chapter{Start: 0, Title: "Edited One"},
			wl.Chapter{Start: 3 * time.Second, Title: "Edited Two"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnChaptersStale {
			t.Errorf("a rebuilt QuickTime track must not be stale; warnings = %v", plan.Report().Warnings)
		}
	}
	out := applyToBytes(t, data, plan)
	chs := mustParseBytes(t, out).Chapters()
	if len(chs) != 2 || chs[0].Title != "Edited One" || chs[1].Title != "Edited Two" {
		t.Errorf("edited chapters not read back from the rewritten QuickTime track: %+v", chs)
	}
	if chs[1].Start != 3*time.Second {
		t.Errorf("second chapter start = %v, want 3s", chs[1].Start)
	}
}

func TestMP4QTChapterReEditAfterTagEdit(t *testing.T) {
	// Tag-edit a QuickTime-chapter file, then chapter-edit the Document that write returned.
	data := testfile.MP4QTFile([]int{0, 5000}, []string{"A", "B"})
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Tagged").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	doc, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := doc.Edit().SetChapters(wl.Chapter{Start: 0, Title: "Added After Tag"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, warn := range plan2.Report().Warnings {
		if warn.Code == wl.WarnChaptersStale {
			t.Errorf("a chapter edit after a tag edit should rebuild the QuickTime track, not leave it stale: %v", plan2.Report().Warnings)
		}
	}
	out2 := applyToBytes(t, w.b, plan2)
	re := mustParseBytes(t, out2)
	if chs := re.Chapters(); len(chs) != 1 || chs[0].Title != "Added After Tag" {
		t.Errorf("chapter edit after a tag edit not read back from the rebuilt QuickTime track: %+v", chs)
	}
	if re.Fields().Title != "Tagged" {
		t.Error("the tag edit was lost after a follow-up chapter edit")
	}
}
