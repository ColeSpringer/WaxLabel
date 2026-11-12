package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
	"unicode/utf16"

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

// A chapter text sample's title is read alone, its 2-byte length then that many bytes: a final
// sample the file's end cuts off, before or inside its title, reads untitled as it does under the
// default limit, and style boxes after a short title are never read, so that title reads under a
// limit smaller than the sample.
func TestMP4QTChapterSampleReadWindow(t *testing.T) {
	starts, titles := []int{0, 5000, 12000}, []string{"Intro", "Verse", "Outro"}
	// lastSize is where the final sample's stsz entry sits.
	lastSize := func(data []byte) int {
		at := bytes.Index(data, []byte("stsz")) - 4
		return at + int(binary.BigEndian.Uint32(data[at:])) - 4
	}
	grow := func(b []byte, at int, n int) {
		binary.BigEndian.PutUint32(b[at:], binary.BigEndian.Uint32(b[at:])+uint32(n))
	}

	// The file ends where the final sample would start, and its stsz entry declares 1 MiB.
	cut := testfile.MP4QTFile(starts, titles)
	binary.BigEndian.PutUint32(cut[lastSize(cut):], 1<<20)
	cut = cut[:len(cut)-2-len("Outro")]

	// The file ends inside the final sample's title.
	cutTitle := testfile.MP4QTFile(starts, titles)
	cutTitle = cutTitle[:len(cutTitle)-len("tro")]

	// The final sample carries 128 KiB of style boxes after its title.
	styled := testfile.MP4QTFile(starts, titles)
	style := slices.Concat(testfile.BE32(128<<10), []byte("styl"), make([]byte, 128<<10-8))
	grow(styled, lastSize(styled), len(style))
	grow(styled, bytes.LastIndex(styled, []byte("mdat"))-4, len(style))
	styled = append(styled, style...)

	for _, c := range []struct {
		name  string
		data  []byte
		limit int64
		last  string
	}{
		{"cut-off sample", cut, 64 << 10, ""},
		{"title cut by EOF", cutTitle, 64 << 10, ""},
		{"styled sample", styled, 96 << 10, "Outro"},
	} {
		for _, limit := range []int64{c.limit, 0} {
			doc, err := wl.Parse(context.Background(), wl.BytesSource(c.data), wl.WithLimits(wl.Limits{MaxAllocBytes: limit}))
			if err != nil {
				t.Errorf("%s at limit %d: %v", c.name, limit, err)
				continue
			}
			chs := doc.Chapters()
			if len(chs) != 3 || chs[0].Title != "Intro" || chs[2].Title != c.last {
				t.Errorf("%s at limit %d: chapters %+v, want Intro, Verse and %q", c.name, limit, chs, c.last)
			}
		}
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

func TestMP4ChapterEditKeepsMoovFingerprinted(t *testing.T) {
	// A chapter edit appends a chapter mdat at end-of-file. On a non-faststart file (moov after the
	// audio mdat) that would sandwich the moov between two mdats, in the fingerprint's un-hashed gap.
	build := func(stcoOff uint32) []byte {
		moov := testfile.MP4Atom("moov", testfile.MP4SoundTrak(1, stcoOff))
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
		moov := testfile.MP4Atom("moov", testfile.MP4SoundTrak(1, stcoOff))
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
		moov := testfile.MP4Atom("moov", testfile.MP4SoundTrak(1, stcoOff))
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
		moov := testfile.MP4Atom("moov", testfile.MP4SoundTrak(-1, stcoOff))
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
		moov := testfile.MP4Atom("moov", testfile.MP4SoundTrak(1, stcoOff, tref))
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
		trak := testfile.MP4SoundTrak(1, stcoOff, dangling)
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
	// There is no chapter track to remove, and the plan does not say there is.
	if ops := plan.Report().Operations; !slices.Contains(ops, "chapter removal (chpl + track references)") {
		t.Errorf("operations %q, want the chpl and the track references named", ops)
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

// A late first chapter keeps every start and the last chapter ends with the movie, read from a
// track whose media runs on to the movie's end, as it is written, and from one whose media ends
// with the chapters, as earlier versions wrote it.
func TestMP4ChapterLateFirstStartEndsWithTheMovie(t *testing.T) {
	const mts = 90000 // the media timescale chapter tracks are written at
	written := rewriteChapters(t, readFixture(t, sampleM4B), func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(wl.Chapter{Start: 2 * time.Second, Title: "A"}, wl.Chapter{Start: 5 * time.Second, Title: "B"})
	})
	// The chapter trak's media duration and its last stts delta.
	hdlr := bytes.Index(written, []byte("SubtitleHandler"))
	mdhd := bytes.LastIndex(written[:hdlr], []byte("mdhd")) + 4 + 16
	stts := hdlr + bytes.Index(written[hdlr:], []byte("stts")) + 4
	last := stts + 8 + 8*(int(binary.BigEndian.Uint32(written[stts+4:]))-1) + 4
	if got := binary.BigEndian.Uint32(written[mdhd:]); got != 9*mts {
		t.Errorf("the chapter media lasts %d units, want the movie's 9 s (%d)", got, 9*mts)
	}
	earlier := slices.Clone(written)
	for _, at := range []int{mdhd, last} {
		binary.BigEndian.PutUint32(earlier[at:], binary.BigEndian.Uint32(earlier[at:])-2*mts)
	}
	for name, data := range map[string][]byte{"media to the movie's end": written, "media to the chapters' end": earlier} {
		doc := mustParseBytes(t, data)
		chs := doc.Chapters()
		if len(chs) != 2 || chs[0].Start != 2*time.Second || chs[1].Start != 5*time.Second || chs[1].End != 9*time.Second ||
			chapterWarn(doc, wl.WarnChapterSourceConflict) {
			t.Errorf("%s: chapters %+v, warnings %v; want 2 s and 5 s, the last ending at 9 s", name, chs, warningCodes(doc))
		}
	}
}

// Behind a late first chapter the last chapter's end reads back as it was set, to the chapter
// track's 90 kHz grid, and setting the same list again writes nothing.
func TestMP4ChapterLateFirstStartKeepsTheLastEnd(t *testing.T) {
	src := readFixture(t, sampleM4B)
	for name, chs := range map[string][]wl.Chapter{
		"an end short of the movie's":         {{Start: 2 * time.Second, Title: "A"}, {Start: 5 * time.Second, End: 8 * time.Second, Title: "B"}},
		"an end off the movie timescale":      {{Start: 2 * time.Second, Title: "A"}, {Start: 5 * time.Second, End: 8*time.Second + 400*time.Microsecond, Title: "B"}},
		"a chapter shorter than a movie unit": {{Start: 4 * time.Second, End: 4*time.Second + 100*time.Microsecond, Title: "Blip"}},
		"a first start of one movie unit":     {{Start: time.Millisecond, Title: "A"}, {Start: 5 * time.Second, End: 8 * time.Second, Title: "B"}},
		"one unit, an end between units":      {{Start: time.Millisecond, Title: "A"}, {Start: 5 * time.Second, End: 8*time.Second + 500*time.Microsecond, Title: "B"}},
		// Past the movie's end a span of one second marks an open chapter; this one must not grow into it.
		"an end past the movie's": {{Start: 250 * time.Millisecond, Title: "A"}, {Start: 9 * time.Second, End: 9*time.Second + 750*time.Millisecond, Title: "B"}},
	} {
		doc := mustParseBytes(t, rewriteChapters(t, src, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chs...) }))
		got, want := doc.Chapters(), chs[len(chs)-1].End
		if len(got) != len(chs) || got[len(got)-1].End != want || chapterWarn(doc, wl.WarnChapterSourceConflict) {
			t.Errorf("%s: chapters %+v, warnings %v; want the last to end at %v", name, got, warningCodes(doc), want)
		}
		if plan, err := doc.Edit().SetChapters(chs...).Prepare(); err != nil || !plan.IsNoOp() {
			t.Errorf("%s: the same list set again: no-op %v, err %v", name, err == nil && plan.IsNoOp(), err)
		}
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

// mp4QTChaptersCo64 is a file with a two-chapter QuickTime text track whose co64 entry is textAt
// of where its samples start.
func mp4QTChaptersCo64(textAt func(samples int) uint64) []byte {
	titles, filler := []string{"One", "Two"}, bytes.Repeat([]byte{0xA7}, 120)
	build := func(audio uint32, text uint64) []byte {
		kids := slices.Concat(testfile.MP4AudioTrakChap(2, audio), testfile.MP4TextTrakCo64(2, 1000, []int{0, 1000}, titles, text))
		return slices.Concat(testfile.MP4Ftyp(), testfile.MP4Atom("moov", kids),
			testfile.MP4Atom("mdat", slices.Concat(filler, testfile.MP4SamplesBlob(titles))))
	}
	payload := bytes.Index(build(0, 0), []byte("mdat")) + 4
	return build(uint32(payload), textAt(payload+len(filler)))
}

// A chapter sample whose co64 offset lies outside the file, as a negative int64 or past what an
// int64 can address, reads from a file as from bytes in memory: both chapters, the samples
// unread. The read is never made, so a file's refusal of the offset is not taken for a device
// error.
func TestMP4ChapterSampleOutsideTheFile(t *testing.T) {
	for _, text := range []uint64{1 << 63, 0xFFFFFFFFFFFFFFF0, 0x7FFFFFFFFFFFFFFF} {
		data := mp4QTChaptersCo64(func(int) uint64 { return text })
		path := filepath.Join(t.TempDir(), "chapters.m4b")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		fromFile, err := wl.ParseFile(context.Background(), path)
		if err != nil {
			t.Errorf("co64 %#x from a file: %v; want the chapters read", text, err)
			continue
		}
		fromBytes := mustParseBytes(t, data)
		if got := fromFile.Chapters(); len(got) != 2 || !reflect.DeepEqual(got, fromBytes.Chapters()) {
			t.Errorf("co64 %#x: chapters %+v from a file, %+v from bytes; want the same two", text, got, fromBytes.Chapters())
		}
	}
}

// mp4TwoChapAtoms is a file whose audio tref holds two "chap" atoms, the first naming no track and
// the second the chapter text track of chapters A and B.
func mp4TwoChapAtoms() []byte {
	titles := []string{"A", "B"}
	tref := testfile.MP4Atom("tref", slices.Concat(
		testfile.MP4Atom("chap", testfile.BE32(99)), testfile.MP4Atom("chap", testfile.BE32(2))))
	audio := bytes.Repeat([]byte{0xA7}, 120)
	build := func(audioStco, textStco uint32) []byte {
		moov := testfile.MP4Atom("moov", slices.Concat(testfile.MP4SoundTrak(1, audioStco, tref),
			testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, textStco)))
		return slices.Concat(testfile.MP4Ftyp(), moov, testfile.MP4Atom("mdat", slices.Concat(audio, testfile.MP4SamplesBlob(titles))))
	}
	at := bytes.Index(build(0, 0), []byte("mdat")) + 4
	return build(uint32(at), uint32(at+len(audio)))
}

// Every "chap" atom of the audio track's tref is read, so a chapter track behind a reference that
// names no track is found, and a clear removes them all.
func TestMP4ChapterTrackNamedByASecondChapAtom(t *testing.T) {
	data := mp4TwoChapAtoms()
	doc := mustParseBytes(t, data)
	if chs := doc.Chapters(); len(chs) != 2 || chs[1].Title != "B" {
		t.Fatalf("chapters = %+v, want A and B from the text track", chs)
	}
	plan, err := doc.Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if re := mustParseBytes(t, out); len(re.Chapters()) != 0 || bytes.Contains(out, []byte("chap")) || bytes.Contains(out, []byte("text")) {
		t.Errorf("after the clear: %d chapter(s), chap reference %v, text track %v",
			len(re.Chapters()), bytes.Contains(out, []byte("chap")), bytes.Contains(out, []byte("text")))
	}
}

// A "chap" reference naming the audio track itself names no chapter track: the audio stays the
// audio, in the digest and through a chapter write.
func TestMP4ChapReferenceToTheAudioTrackItself(t *testing.T) {
	synth := func(trak func(stco uint32) []byte) []byte {
		file := func(stco uint32) []byte {
			return slices.Concat(testfile.MP4Ftyp(), testfile.MP4Atom("moov", trak(stco)), testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)))
		}
		return file(uint32(bytes.Index(file(0), []byte("mdat")) + 4))
	}
	// The fixture's audio track, with real sample tables, names track 2; make it name itself.
	real := readFixture(t, sampleM4B)
	_, id, ok := bytes.Cut(real, []byte("chap"))
	if !ok || binary.BigEndian.Uint32(id) != 2 {
		t.Fatal("setup: the fixture's chap reference is not to track 2")
	}
	binary.BigEndian.PutUint32(id, 1)
	for name, data := range map[string][]byte{
		"one chunk":    synth(func(stco uint32) []byte { return testfile.MP4AudioTrakChap(1, stco) }),
		"real fixture": real,
	} {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, data)
			if hasWarning(doc, wl.WarnNoAudioFrames) {
				t.Fatalf("warnings %v: the audio reads as no audio", warningCodes(doc))
			}
			essence := essenceOf(t, data)
			for step, edit := range map[string]func(*wl.Editor) *wl.Editor{
				"tag edit":      func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "T") },
				"chapter write": func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("A", "B")...) },
			} {
				plan, err := edit(doc.Edit()).Prepare(wl.WithVerifyEssence())
				if err != nil {
					t.Fatalf("%s: %v", step, err)
				}
				out := applyToBytes(t, data, plan)
				if re := mustParseBytes(t, out); re.Properties().Tracks[0].SampleRate != 44100 || !essenceOf(t, out).Equal(essence) {
					t.Errorf("%s: audio track %+v, digest equal %v", step, re.Properties().Tracks[0], essenceOf(t, out).Equal(essence))
				}
			}
		})
	}
	plain := synth(func(stco uint32) []byte { return testfile.MP4SoundTrak(1, stco) })
	if one := synth(func(stco uint32) []byte { return testfile.MP4AudioTrakChap(1, stco) }); !essenceOf(t, one).Equal(essenceOf(t, plain)) {
		t.Error("the digest differs from that of the file with no reference")
	}
}

// Only a text track's samples are chapter storage: where the "chap" reference names a track of
// another kind, a chapter write leaves the mdat its samples fill.
func TestMP4ChapterWriteKeepsSamplesOfATrackThatIsNoTextTrack(t *testing.T) {
	rewrite := func(src []byte, titles ...string) []byte {
		plan, err := mustParseBytes(t, src).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		return applyToBytes(t, src, plan)
	}
	data := rewrite(testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T")), "Alpha", "Beta")
	// The written track's handler type sits 16 bytes ahead of its handler name.
	at := bytes.Index(data, []byte("SubtitleHandler")) - 16
	if at < 0 || string(data[at:at+4]) != "text" {
		t.Fatal("setup: no text handler in the written chapter track")
	}
	copy(data[at:], "soun")
	if out := rewrite(data, "Gamma"); !bytes.Contains(out, []byte("Alpha")) || !bytes.Contains(out, []byte("Beta")) {
		t.Error("the write removed the samples of a track that is no text track")
	}
}

// mp4Trak is what a test reads of one trak: its handler type, whether it holds a tref, and the
// track ids the tref's "chap" entries name.
type mp4Trak struct {
	handler string
	tref    bool
	chap    []uint32
}

// mp4Traks reads the traks of the file's moov, in order.
func mp4Traks(t *testing.T, data []byte) []mp4Trak {
	t.Helper()
	moov, moovLen := mp4FindBox(t, data, 0, len(data), "moov")
	offs, names := mp4Boxes(data, moov+8, moov+moovLen)
	var out []mp4Trak
	for i, name := range names {
		if name != "trak" {
			continue
		}
		var tr mp4Trak
		lo, hi := offs[i]+8, offs[i]+int(binary.BigEndian.Uint32(data[offs[i]:]))
		kids, kidNames := mp4Boxes(data, lo, hi)
		for k, kid := range kidNames {
			end := kids[k] + int(binary.BigEndian.Uint32(data[kids[k]:]))
			switch kid {
			case "tref":
				tr.tref = true
				refs, refNames := mp4Boxes(data, kids[k]+8, end)
				for r, ref := range refNames {
					if ref != "chap" {
						continue
					}
					for at := refs[r] + 8; at+4 <= refs[r]+int(binary.BigEndian.Uint32(data[refs[r]:])); at += 4 {
						tr.chap = append(tr.chap, binary.BigEndian.Uint32(data[at:]))
					}
				}
			case "mdia":
				hdlr, _ := mp4FindBox(t, data, kids[k]+8, end, "hdlr")
				tr.handler = string(data[hdlr+16 : hdlr+20])
			}
		}
		out = append(out, tr)
	}
	return out
}

// rewriteChapters plans edit on data with the essence verified and returns the written bytes.
func rewriteChapters(t *testing.T, data []byte, edit func(*wl.Editor) *wl.Editor) []byte {
	t.Helper()
	plan, err := edit(mustParseBytes(t, data).Edit()).Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	return applyToBytes(t, data, plan)
}

// A video track a "chap" entry names holds chapter thumbnails and is never the chapter track,
// wherever it sits in the file: the titles come from the text track, a clear removes that track
// alone and leaves the thumbnail reference, and a rewrite replaces it in place.
func TestMP4ChapterTrefVideoThumbnailNotChapterTrack(t *testing.T) {
	titles := []string{"One", "Two"}
	data := testfile.MP4TracksFile(titles, func(audio, text, thumbs uint32) []byte {
		return slices.Concat(testfile.MP4VideoTrak(3, thumbs), testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(2, 3)),
			testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, text))
	})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 2 || chs[0].Title != "One" || chs[1].Title != "Two" || chs[1].Start != 3*time.Second {
		t.Fatalf("chapters = %+v, want One and Two from the text track", chs)
	}

	cleared := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.ClearChapters() })
	want := []mp4Trak{{handler: "vide"}, {handler: "soun", tref: true, chap: []uint32{3}}}
	if got := mp4Traks(t, cleared); !reflect.DeepEqual(got, want) {
		t.Errorf("after the clear: traks %+v, want %+v", got, want)
	}
	if chs := mustParseBytes(t, cleared).Chapters(); len(chs) != 0 {
		t.Errorf("after the clear: chapters %+v", chs)
	}

	replaced := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(chaptersFromTitles("Uno", "Dos", "Tres")...)
	})
	want = []mp4Trak{{handler: "vide"}, {handler: "soun", tref: true, chap: []uint32{2, 3}}, {handler: "text"}}
	if got := mp4Traks(t, replaced); !reflect.DeepEqual(got, want) {
		t.Errorf("after the rewrite: traks %+v, want %+v", got, want)
	}
	if chs := mustParseBytes(t, replaced).Chapters(); len(chs) != 3 || chs[2].Title != "Tres" {
		t.Errorf("after the rewrite: chapters %+v, want Uno, Dos and Tres", chs)
	}
}

// A clear takes the chapter track's id out of every track's "chap" list, not the audio track's
// alone, and a list or tref left empty goes with it. Other references in a tref stay.
func TestMP4ChapterClearRewritesEveryTref(t *testing.T) {
	titles := []string{"One", "Two"}
	hint := testfile.MP4Atom("hint", testfile.BE32(1))
	data := testfile.MP4TracksFile(titles, func(audio, text, thumbs uint32) []byte {
		videoTref := testfile.MP4Atom("tref", slices.Concat(hint, testfile.MP4Atom("chap", testfile.BE32(2))))
		return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(2)), testfile.MP4VideoTrak(3, thumbs, videoTref),
			testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, text))
	})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 2 {
		t.Fatalf("setup: chapters = %+v", chs)
	}
	cleared := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.ClearChapters() })
	want := []mp4Trak{{handler: "soun"}, {handler: "vide", tref: true}}
	if got := mp4Traks(t, cleared); !reflect.DeepEqual(got, want) {
		t.Errorf("after the clear: traks %+v, want %+v", got, want)
	}
	if bytes.Contains(cleared, []byte("chap")) || !bytes.Contains(cleared, hint) {
		t.Errorf("after the clear: chap reference left %v, hint reference kept %v",
			bytes.Contains(cleared, []byte("chap")), bytes.Contains(cleared, hint))
	}

	// A rewrite reuses the track id, so both lists stay as they are.
	replaced := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("Uno")...) })
	want = []mp4Trak{{handler: "soun", tref: true, chap: []uint32{2}}, {handler: "vide", tref: true, chap: []uint32{2}}, {handler: "text"}}
	if got := mp4Traks(t, replaced); !reflect.DeepEqual(got, want) {
		t.Errorf("after the rewrite: traks %+v, want %+v", got, want)
	}
}

// A chapter track only the video track references is read, rewritten in place and cleared.
func TestMP4ChapterTrackReferencedFromVideoOnly(t *testing.T) {
	titles := []string{"One", "Two"}
	data := testfile.MP4TracksFile(titles, func(audio, text, thumbs uint32) []byte {
		return slices.Concat(testfile.MP4SoundTrak(1, audio), testfile.MP4VideoTrak(3, thumbs, testfile.MP4TrefChap(2)),
			testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, text))
	})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 2 || chs[1].Title != "Two" {
		t.Fatalf("chapters = %+v, want One and Two", chs)
	}
	replaced := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("Uno")...) })
	want := []mp4Trak{{handler: "soun"}, {handler: "vide", tref: true, chap: []uint32{2}}, {handler: "text"}}
	if got := mp4Traks(t, replaced); !reflect.DeepEqual(got, want) {
		t.Errorf("after the rewrite: traks %+v, want %+v", got, want)
	}
	if chs := mustParseBytes(t, replaced).Chapters(); len(chs) != 1 || chs[0].Title != "Uno" {
		t.Errorf("after the rewrite: chapters %+v, want Uno", chs)
	}
	cleared := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.ClearChapters() })
	want = []mp4Trak{{handler: "soun"}, {handler: "vide"}}
	if got := mp4Traks(t, cleared); !reflect.DeepEqual(got, want) {
		t.Errorf("after the clear: traks %+v, want %+v", got, want)
	}
}

// ffmpeg takes the last "chap" list in the file for the whole movie, so a new chapter track gets
// one list everywhere: itself, then every thumbnail track any list names. Each sound track and
// each video track that holds no thumbnails gains it, a trak that keeps a list has it, and a
// thumbnail track gains none.
func TestMP4ChapterTrackCreatedIsReferencedFromSoundAndVideo(t *testing.T) {
	data := testfile.MP4TracksFile(nil, func(audio, _, thumbs uint32) []byte {
		hint := func(id int, tref []byte) []byte {
			trak := testfile.MP4VideoTrak(id, thumbs, tref)
			copy(trak[bytes.Index(trak, []byte("vide")):], "hint")
			return trak
		}
		return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(7, 3)), testfile.MP4VideoTrak(3, thumbs),
			testfile.MP4VideoTrak(4, thumbs), testfile.MP4VideoTrak(5, thumbs),
			hint(7, testfile.MP4TrefChap(3, 4)), hint(9, testfile.MP4TrefChap(99)))
	})
	out := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("A", "B")...) })
	// Tracks 3 and 4 hold thumbnails, each named by one list. A track of another kind gains no
	// list, and loses one that names no thumbnails.
	list := []uint32{10, 3, 4}
	want := []mp4Trak{{handler: "soun", tref: true, chap: list}, {handler: "vide"}, {handler: "vide"},
		{handler: "vide", tref: true, chap: list}, {handler: "hint", tref: true, chap: list}, {handler: "hint"}, {handler: "text"}}
	if got := mp4Traks(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("traks %+v, want %+v", got, want)
	}
	if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 || chs[1].Title != "B" {
		t.Errorf("chapters %+v, want A and B", chs)
	}
}

// A rewrite of the chapter track reuses its id and leaves every "chap" list as it is. A clear
// leaves a list the video tracks it named: the id of the chapter track, of a track that is gone,
// of a second text track and of the sound track itself go, so no reader finds chapters after it,
// and the second text track stays in the file.
func TestMP4ChapterClearLeavesOnlyVideoTracksNamed(t *testing.T) {
	titles := []string{"One", "Two"}
	data := testfile.MP4TracksFile(titles, func(audio, text, thumbs uint32) []byte {
		return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(99, 2, 1, 5, 3)), testfile.MP4VideoTrak(3, thumbs),
			testfile.MP4TextTrak(5, 1000, []int{0, 4000}, titles, text),
			testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, text))
	})
	// The list names track 2 ahead of track 5, which comes first in the file.
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 2 || chs[1].Start != 3*time.Second {
		t.Fatalf("chapters = %+v, want those of the text track named first", chs)
	}
	replaced := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("Uno")...) })
	want := []mp4Trak{{handler: "soun", tref: true, chap: []uint32{99, 2, 1, 5, 3}}, {handler: "vide"}, {handler: "text"}, {handler: "text"}}
	if got := mp4Traks(t, replaced); !reflect.DeepEqual(got, want) {
		t.Errorf("after the rewrite: traks %+v, want %+v", got, want)
	}
	if chs := mustParseBytes(t, replaced).Chapters(); len(chs) != 1 || chs[0].Title != "Uno" {
		t.Errorf("after the rewrite: chapters %+v, want Uno", chs)
	}
	cleared := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.ClearChapters() })
	want = []mp4Trak{{handler: "soun", tref: true, chap: []uint32{3}}, {handler: "vide"}, {handler: "text"}}
	if got := mp4Traks(t, cleared); !reflect.DeepEqual(got, want) {
		t.Errorf("after the clear: traks %+v, want %+v", got, want)
	}
	// The text track left is the one that was not the chapter track, its 4 s sample and all.
	if chs := mustParseBytes(t, cleared).Chapters(); len(chs) != 0 || !bytes.Contains(cleared, slices.Concat(testfile.BE32(1), testfile.BE32(4000))) {
		t.Errorf("after the clear: chapters %+v, the second text track kept %v", chs, bytes.Contains(cleared, slices.Concat(testfile.BE32(1), testfile.BE32(4000))))
	}
}

// A list is read whole, however long: a thumbnail track named far down it is still named after a
// clear.
func TestMP4ChapterClearKeepsAThumbnailFarDownTheList(t *testing.T) {
	titles := []string{"One", "Two"}
	ids := append(slices.Repeat([]int{99}, 1500), 2, 3)
	data := testfile.MP4TracksFile(titles, func(audio, text, thumbs uint32) []byte {
		return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(ids...)), testfile.MP4VideoTrak(3, thumbs),
			testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, text))
	})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 2 {
		t.Fatalf("chapters %+v, want the two of the text track", chs)
	}
	cleared := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.ClearChapters() })
	want := []mp4Trak{{handler: "soun", tref: true, chap: []uint32{3}}, {handler: "vide"}}
	if got := mp4Traks(t, cleared); !reflect.DeepEqual(got, want) {
		t.Errorf("after the clear: traks %+v, want %+v", got, want)
	}
}

// A chapter track whose track id is 0 keeps its references through a rewrite, so the next read
// finds it and the next rewrite replaces it again.
func TestMP4ChapterTrackWithTrackIDZero(t *testing.T) {
	titles := []string{"One", "Two"}
	data := testfile.MP4TracksFile(titles, func(audio, text, _ uint32) []byte {
		return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(0)), testfile.MP4TextTrak(0, 1000, []int{0, 3000}, titles, text))
	})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 2 || chs[1].Title != "Two" {
		t.Fatalf("chapters = %+v, want One and Two", chs)
	}
	want := []mp4Trak{{handler: "soun", tref: true, chap: []uint32{0}}, {handler: "text"}}
	out := data
	for _, title := range []string{"Uno", "Eins"} {
		out = rewriteChapters(t, out, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles(title)...) })
		if got := mp4Traks(t, out); !reflect.DeepEqual(got, want) {
			t.Errorf("after the rewrite to %s: traks %+v, want %+v", title, got, want)
		}
		if chs := mustParseBytes(t, out).Chapters(); len(chs) != 1 || chs[0].Title != title {
			t.Errorf("after the rewrite: chapters %+v, want %s", chs, title)
		}
	}
	cleared := rewriteChapters(t, out, func(e *wl.Editor) *wl.Editor { return e.ClearChapters() })
	if got, want := mp4Traks(t, cleared), []mp4Trak{{handler: "soun"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("after the clear: traks %+v, want %+v", got, want)
	}
}

// A text track that only its own "chap" list names is no chapter track: a rewrite would replace
// it, list and all, and leave the new one named by nothing. Chapters written to the file get a
// track of their own, and the list goes so that no reader takes the old track for them.
func TestMP4ChapterTrackNamedOnlyByItself(t *testing.T) {
	titles := []string{"One", "Two"}
	data := testfile.MP4TracksFile(titles, func(audio, text, _ uint32) []byte {
		return slices.Concat(testfile.MP4SoundTrak(1, audio),
			mp4AfterTkhd(testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, text), testfile.MP4TrefChap(2)))
	})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 0 {
		t.Fatalf("chapters %+v read from a track only it names", chs)
	}
	out := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("A", "B")...) })
	want := []mp4Trak{{handler: "soun", tref: true, chap: []uint32{3}}, {handler: "text"}, {handler: "text"}}
	if got := mp4Traks(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("traks %+v, want %+v", got, want)
	}
	if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 || chs[1].Title != "B" {
		t.Errorf("chapters %+v, want A and B", chs)
	}
}

// mp4AfterTkhd puts atoms behind the tkhd of a trak, where a tref and an edts go.
func mp4AfterTkhd(trak []byte, atoms ...[]byte) []byte {
	at := 8 + int(binary.BigEndian.Uint32(trak[8:]))
	out := slices.Concat(trak[:at], slices.Concat(atoms...), trak[at:])
	binary.BigEndian.PutUint32(out, uint32(len(out)))
	return out
}

// A subtitle track a "chap" entry names is a chapter track like a text track, and one that holds
// a "chap" list of its own is replaced and removed whole, list and all.
func TestMP4ChapterTrackWithASubtitleHandlerAndItsOwnReferences(t *testing.T) {
	titles := []string{"One", "Two"}
	data := testfile.MP4TracksFile(titles, func(audio, text, thumbs uint32) []byte {
		chapters := mp4AfterTkhd(testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, text), testfile.MP4TrefChap(2, 3))
		copy(chapters[bytes.Index(chapters, []byte("text")):], "sbtl")
		return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(2)), testfile.MP4VideoTrak(3, thumbs), chapters)
	})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 2 || chs[1].Title != "Two" {
		t.Fatalf("chapters = %+v, want One and Two from the subtitle track", chs)
	}
	replaced := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("Uno")...) })
	want := []mp4Trak{{handler: "soun", tref: true, chap: []uint32{2}}, {handler: "vide"}, {handler: "text"}}
	if got := mp4Traks(t, replaced); !reflect.DeepEqual(got, want) {
		t.Errorf("after the rewrite: traks %+v, want %+v", got, want)
	}
	cleared := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.ClearChapters() })
	want = []mp4Trak{{handler: "soun"}, {handler: "vide"}}
	if got := mp4Traks(t, cleared); !reflect.DeepEqual(got, want) {
		t.Errorf("after the clear: traks %+v, want %+v", got, want)
	}
}

// utf16Title is a title as QuickTime stores one that is not ASCII: UTF-16 behind a byte-order mark.
func utf16Title(order binary.AppendByteOrder, title string) string {
	b := order.AppendUint16(nil, 0xFEFF)
	for _, u := range utf16.Encode([]rune(title)) {
		b = order.AppendUint16(b, u)
	}
	return string(b)
}

// A chapter track's UTF-16 titles read as the text the chpl holds in UTF-8, so the two stores
// agree, and a chapter edit carries every title over into both.
func TestMP4QTChapterUTF16Titles(t *testing.T) {
	titles := []string{"Caf\u00e9", "\u65e5\u672c\u8a9e", "Plain"}
	starts := []time.Duration{0, 3 * time.Second, 6 * time.Second}
	for name, order := range map[string]binary.AppendByteOrder{"big-endian": binary.BigEndian, "little-endian": binary.LittleEndian} {
		wide := make([]string, len(titles))
		for i, title := range titles {
			wide[i] = utf16Title(order, title)
		}
		for store, data := range map[string][]byte{
			"beside a chpl": testfile.MP4QTFile([]int{0, 3000, 6000}, wide, mp4Chpl(1, starts, titles)),
			"alone":         testfile.MP4QTFile([]int{0, 3000, 6000}, wide),
		} {
			t.Run(name+" "+store, func(t *testing.T) {
				doc := mustParseBytes(t, data)
				read := func(doc *wl.Document) (got []string) {
					for _, ch := range doc.Chapters() {
						got = append(got, ch.Title)
					}
					return got
				}
				if got := read(doc); !slices.Equal(got, titles) || hasWarning(doc, wl.WarnChapterSourceConflict) || hasWarning(doc, wl.WarnInvalidText) {
					t.Fatalf("titles %q, warnings %v; want %q and no warning", got, warningCodes(doc), titles)
				}
				plan, err := doc.Edit().SetChapters(append(doc.Chapters(), wl.Chapter{Start: 8 * time.Second, Title: "Added"})...).Prepare()
				if err != nil {
					t.Fatal(err)
				}
				out := applyToBytes(t, data, plan)
				re := mustParseBytes(t, out)
				if got := read(re); !slices.Equal(got, append(slices.Clone(titles), "Added")) || hasWarning(re, wl.WarnChapterSourceConflict) {
					t.Errorf("after the edit: titles %q, warnings %v", got, warningCodes(re))
				}
				// The chpl and the rewritten track each hold the title in UTF-8.
				if n := bytes.Count(out, []byte(titles[1])); n != 2 {
					t.Errorf("after the edit the file holds %q %d time(s), want one in each store", titles[1], n)
				}
			})
		}
	}
}

// A chapter track title that reads empty takes the chpl's, so the stores agree and an edit keeps
// the title. A title the track does hold still overrules a different one.
func TestMP4ChplTitleFillsAnEmptyTrackTitle(t *testing.T) {
	starts := []time.Duration{0, 3 * time.Second}
	data := testfile.MP4QTFile([]int{0, 3000}, []string{"", "Two"}, mp4Chpl(1, starts, []string{"One", "Two"}))
	doc := mustParseBytes(t, data)
	if chs := doc.Chapters(); len(chs) != 2 || chs[0].Title != "One" || chs[1].Title != "Two" || hasWarning(doc, wl.WarnChapterSourceConflict) {
		t.Fatalf("chapters %+v, warnings %v; want One and Two with no conflict", chs, warningCodes(doc))
	}
	re := execChapters(t, data, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(append(doc.Chapters(), wl.Chapter{Start: 6 * time.Second, Title: "Three"})...)
	})
	if chs := re.Chapters(); len(chs) != 3 || chs[0].Title != "One" {
		t.Errorf("after an edit: chapters %+v, want One first", chs)
	}

	other := testfile.MP4QTFile([]int{0, 3000}, []string{"", "Deux"}, mp4Chpl(1, starts, []string{"One", "Two"}))
	if doc := mustParseBytes(t, other); !hasWarning(doc, wl.WarnChapterSourceConflict) || doc.Chapters()[1].Title != "Deux" {
		t.Errorf("a differing track title: chapters %+v, warnings %v; want the track's with a conflict", doc.Chapters(), warningCodes(doc))
	}
}

// mp4WidenChapterTitles re-encodes the chapter titles of a file whose chapter track WaxLabel
// wrote, its samples filling the last mdat, as UTF-16 behind a byte-order mark.
func mp4WidenChapterTitles(t *testing.T, data []byte, order binary.AppendByteOrder) []byte {
	t.Helper()
	offs, names := mp4Boxes(data, 0, len(data))
	mdat := offs[len(offs)-1]
	if names[len(names)-1] != "mdat" {
		t.Fatalf("the file ends in %q, not the chapter mdat", names[len(names)-1])
	}
	// The chapter track's sample sizes: its trak is the one a text handler names.
	at := bytes.Index(data, []byte("SubtitleHandler"))
	stsz := at + bytes.Index(data[at:], []byte("stsz"))
	if at < 0 || stsz < at {
		t.Fatal("no chapter track sample sizes")
	}
	out := slices.Clone(data[:mdat])
	var samples []byte
	old := data[mdat+8:]
	for i := range int(binary.BigEndian.Uint32(out[stsz+12:])) {
		size := out[stsz+16+4*i:]
		sample := old[:binary.BigEndian.Uint32(size)]
		old = old[len(sample):]
		n := int(binary.BigEndian.Uint16(sample))
		wide := utf16Title(order, string(sample[2:2+n]))
		// What follows the title, the encoding atom, is kept.
		sample = slices.Concat(testfile.BE16(len(wide)), []byte(wide), sample[2+n:])
		binary.BigEndian.PutUint32(size, uint32(len(sample)))
		samples = append(samples, sample...)
	}
	return append(out, testfile.MP4Atom("mdat", samples)...)
}

// Only moov's own traks are named in a chapter write or taken for the chapter track: one nested
// in another trak is left as it is, since nothing would size the trak around it.
func TestMP4ChapterWriteLeavesNestedTraksAlone(t *testing.T) {
	titles := []string{"One", "Two"}
	for name, outerTref := range map[string][]byte{"named from a nested trak": nil, "named from the trak around it": testfile.MP4TrefChap(2)} {
		t.Run(name, func(t *testing.T) {
			data := testfile.MP4TracksFile(titles, func(audio, text, thumbs uint32) []byte {
				return testfile.MP4SoundTrak(1, audio, outerTref, testfile.MP4VideoTrak(3, thumbs, testfile.MP4TrefChap(2)),
					testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, text))
			})
			if chs := mustParseBytes(t, data).Chapters(); len(chs) != 0 {
				t.Fatalf("chapters %+v read from a nested trak", chs)
			}
			out := rewriteChapters(t, data, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("A", "B")...) })
			// The nested video trak keeps its reference, and the nested text trak its samples' sizes.
			if !bytes.Contains(out, testfile.MP4TrefChap(2)) || bytes.Count(out, []byte("stsz")) != 2 {
				t.Error("the nested traks changed")
			}
			want := []mp4Trak{{handler: "soun", tref: true, chap: []uint32{4}}, {handler: "text"}}
			if got := mp4Traks(t, out); !reflect.DeepEqual(got, want) {
				t.Errorf("traks %+v, want %+v", got, want)
			}
			if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 || chs[1].Title != "B" {
				t.Errorf("chapters %+v, want A and B", chs)
			}
		})
	}
}

// A file with no sound or video track gets no chapter track, which nothing would name: the
// chapters go in the chpl alone.
func TestMP4ChapterWriteWithNoTrackToNameAChapterTrack(t *testing.T) {
	data := testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
		hint := testfile.MP4VideoTrak(1, audio)
		copy(hint[bytes.Index(hint, []byte("vide")):], "hint")
		return hint
	})
	plan, err := mustParseBytes(t, data).Edit().SetChapters(chaptersFromTitles("A", "B")...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if got, want := mp4Traks(t, out), []mp4Trak{{handler: "hint"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("traks %+v, want %+v", got, want)
	}
	if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 || chs[1].Title != "B" {
		t.Errorf("chapters %+v, want A and B from the chpl", chs)
	}
}

// An edit list that ends before the last chapter starts, or plays from inside the media as a
// QuickTime trim does, places no end for that chapter: it ends with the media, not before its
// own start.
func TestMP4ChapterEndNeverBeforeItsStart(t *testing.T) {
	titles := []string{"One", "Two", "Three"}
	for name, edts := range map[string][]byte{
		"an edit shorter than the last start": testfile.MP4Edts([2]int64{5000, 0}),
		"an edit from inside the media":       testfile.MP4Edts([2]int64{7000, 3000}),
		"an edit that plays the media whole":  testfile.MP4Edts([2]int64{10000, 0}),
		"no edit list":                        nil,
	} {
		data := testfile.MP4TracksFile(titles, func(audio, text, _ uint32) []byte {
			return slices.Concat(testfile.MP4Mvhd(1000, 10000), testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(2)),
				mp4AfterTkhd(testfile.MP4TextTrak(2, 1000, []int{0, 3000, 8000, 10000}, titles, text), edts))
		})
		if chs := mustParseBytes(t, data).Chapters(); len(chs) != 3 || chs[2].Start != 8*time.Second || chs[2].End != 10*time.Second {
			t.Errorf("%s: chapters %+v, want the last from 8 s to 10 s", name, chs)
		}
	}
	// An edit that cuts the media short after the last chapter starts ends that chapter.
	data := testfile.MP4TracksFile(titles, func(audio, text, _ uint32) []byte {
		return slices.Concat(testfile.MP4Mvhd(1000, 10000), testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(2)),
			mp4AfterTkhd(testfile.MP4TextTrak(2, 1000, []int{0, 3000, 8000, 20000}, titles, text), testfile.MP4Edts([2]int64{9000, 0})))
	})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 3 || chs[2].End != 9*time.Second {
		t.Errorf("media that runs past its edit: chapters %+v, want the last to end at 9 s", chs)
	}
}

// A movie timescale finer than the chapter track's makes a long file's edit segment clamp at 32
// bits. The last chapter then ends with the media, as it did, not at the clamp, whether the
// first chapter starts at zero or late.
func TestMP4ChapterEndPastAClampedEdit(t *testing.T) {
	const movieTS = 1_000_000 // 2^32 units pass after 71.6 minutes
	data := testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
		return slices.Concat(testfile.MP4Mvhd(movieTS, 2*3600*movieTS), testfile.MP4SoundTrak(1, audio))
	})
	for name, first := range map[string]time.Duration{"first chapter at zero": 0, "late first chapter": time.Minute} {
		plan, err := mustParseBytes(t, data).Edit().SetChapters(wl.Chapter{Start: first, Title: "A"}, wl.Chapter{Start: time.Hour, Title: "B"}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		doc := mustParseBytes(t, applyToBytes(t, data, plan))
		if chs := doc.Chapters(); len(chs) != 2 || chs[0].Start != first || chs[1].End != 2*time.Hour || chapterWarn(doc, wl.WarnChapterSourceConflict) {
			t.Errorf("%s: chapters %+v, warnings %v; want the last to end at 2 h", name, chs, warningCodes(doc))
		}
	}
}

// A track id names the first text track that holds it, and a track with no header to give it an
// id is named by none, not by a list that names track 0.
func TestMP4ChapterTrackByItsTrackID(t *testing.T) {
	titles := []string{"One", "Two"}
	shared := testfile.MP4TracksFile(titles, func(audio, text, _ uint32) []byte {
		return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(2)),
			testfile.MP4TextTrak(2, 1000, []int{0, 3000}, titles, text), testfile.MP4TextTrak(2, 1000, []int{0, 4000}, titles, text))
	})
	if chs := mustParseBytes(t, shared).Chapters(); len(chs) != 2 || chs[1].Start != 3*time.Second {
		t.Errorf("two text tracks of one id: chapters %+v, want those of the first", chs)
	}
	headless := testfile.MP4TracksFile(titles, func(audio, text, _ uint32) []byte {
		trak := testfile.MP4TextTrak(0, 1000, []int{0, 3000}, titles, text)
		tkhd := 8 + int(binary.BigEndian.Uint32(trak[8:]))
		trak = slices.Concat(testfile.BE32(len(trak)-tkhd+8), []byte("trak"), trak[tkhd:])
		return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(0)), trak)
	})
	if chs := mustParseBytes(t, headless).Chapters(); len(chs) != 0 {
		t.Errorf("a text track with no tkhd: chapters %+v, want none", chs)
	}
	// Nor is a video track with no tkhd the thumbnail track a list names as track 0: it names a
	// new chapter track like any other.
	created := rewriteChapters(t, testfile.MP4TracksFile(nil, func(audio, _, thumbs uint32) []byte {
		trak := testfile.MP4VideoTrak(0, thumbs)
		tkhd := 8 + int(binary.BigEndian.Uint32(trak[8:]))
		trak = slices.Concat(testfile.BE32(len(trak)-tkhd+8), []byte("trak"), trak[tkhd:])
		return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(0)), testfile.MP4VideoTrak(0, thumbs), trak)
	}), func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("A")...) })
	list := []uint32{2, 0}
	want := []mp4Trak{{handler: "soun", tref: true, chap: list}, {handler: "vide"}, {handler: "vide", tref: true, chap: list}, {handler: "text"}}
	if got := mp4Traks(t, created); !reflect.DeepEqual(got, want) {
		t.Errorf("a video track with no tkhd: traks %+v, want %+v", got, want)
	}
}
