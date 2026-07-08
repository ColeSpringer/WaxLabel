package waxlabel_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestPrepareRejectsInvalidUTF8 is a regression guard: an edit introducing invalid UTF-8 in
// a tag value or chapter title is rejected at Prepare (so "result == fresh parse" holds by
// construction), while a perfectly valid value is NOT spuriously rejected and round-trips.
func TestPrepareRejectsInvalidUTF8(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	bad := "bad\xff\xfevalue" // 0xff 0xfe is not valid UTF-8

	if _, err := mustParseBytes(t, src).Edit().Set(tag.Artist, bad).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("invalid-UTF-8 tag value: err = %v, want ErrInvalidData", err)
	}
	// A chapter title (Matroska supports chapters).
	if _, err := mustParseBytes(t, readFixture(t, sampleMKA)).Edit().
		SetChapters(wl.Chapter{Start: time.Second, Title: bad}).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("invalid-UTF-8 chapter title: err = %v, want ErrInvalidData", err)
	}

	// A valid (multibyte) value is not rejected and round-trips faithfully.
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Artist, "Vàlid ☃ name").Prepare()
	if err != nil {
		t.Fatalf("valid UTF-8 value spuriously rejected: %v", err)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, src, plan)).Tags().First(tag.Artist); v != "Vàlid ☃ name" {
		t.Errorf("valid value round-trip = %q, want \"Vàlid ☃ name\"", v)
	}
}

// TestCopiedValueNotSpuriouslyRejected is the copy-path guard: a value read back through
// the (now sanitizing) parse path is always valid UTF-8, so copying it onto another file
// must never trip the new Prepare reject - it fires only on freshly authored input.
func TestCopiedValueNotSpuriouslyRejected(t *testing.T) {
	// sampleMKA's chapters/tags read back valid; copy them onto a fresh MP3.
	src := mustParseBytes(t, readFixture(t, sampleMKA))
	dst := mustParseBytes(t, readFixture(t, notagsMP3))
	if _, _, err := src.PrepareTransfer(dst); err != nil {
		t.Errorf("copying parsed (valid) metadata must not be rejected: %v", err)
	}
}

// TestLegacyStripDropsTagWithoutFabrication is a regression guard: a --legacy strip on a
// tagless MP3 with a trailing APEv2 tag must drop the APE and NOT fabricate an empty front
// ID3v2 tag. The write stays a real write (the file shrinks), buildResult does not panic on
// the nil tag, and the output re-parses tagless.
func TestLegacyStripDropsTagWithoutFabrication(t *testing.T) {
	data := append(slices.Clone(mp3Audio(t)), apeTag(map[string]string{"Title": "APE Title"})...)
	doc := mustParseBytes(t, data)

	plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if plan.IsNoOp() {
		t.Error("a legacy strip that removes an APE tag must be a real write, not a no-op")
	}
	out := applyToBytes(t, data, plan)
	if len(out) >= len(data) {
		t.Errorf("legacy-strip output %d >= input %d: a tag was fabricated or the APE not stripped", len(out), len(data))
	}
	if bytes.HasPrefix(out, []byte("ID3")) {
		t.Error("legacy-strip fabricated a front ID3v2 tag (the 8 KiB-tag bug)")
	}
	if bytes.Contains(out, []byte("APE Title")) {
		t.Error("the APEv2 tag was not stripped")
	}
	if re := mustParseBytes(t, out); re.Format() != wl.FormatMP3 { // re-parse: no panic, valid file
		t.Errorf("reparsed format = %v, want MP3", re.Format())
	}
}

// TestClearAllDropsFrontID3 is the companion: clearing every tag and picture on an MP3
// that HAD a front ID3v2 drops the container entirely (matching WAV/AIFF), with no panic in
// the result builder and a tagless re-parse.
func TestClearAllDropsFrontID3(t *testing.T) {
	src := readFixture(t, sampleMP3)
	doc := mustParseBytes(t, src)
	ed := doc.Edit()
	for _, k := range doc.Tags().Keys() {
		ed.Clear(k)
	}
	ed.ClearPictures()

	plan, err := ed.Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	out := applyToBytes(t, src, plan)
	if bytes.HasPrefix(out, []byte("ID3")) {
		t.Error("clear-all left a front ID3v2 tag; the container should be dropped")
	}
	re := mustParseBytes(t, out) // re-parse must not panic on the now-tagless file
	if n := len(re.Tags().Keys()); n != 0 {
		t.Errorf("reparsed cleared MP3 has %d keys, want 0", n)
	}
}

// TestPlanReportIsDefensiveCopy is a regression guard (made live by the keyed warning):
// mutating a warning's Keys (or the Operations slice) on the value Report() returns must not
// reach back into the plan's own report, so a later Report() is intact.
func TestPlanReportIsDefensiveCopy(t *testing.T) {
	// GENRE=17 on a tagless MP3 produces a keyed numeric-genre warning.
	plan, err := mustParseBytes(t, readFixture(t, notagsMP3)).Edit().Set(tag.Genre, "17").Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	r1 := plan.Report()
	idx := slices.IndexFunc(r1.Warnings, func(w wl.Warning) bool { return len(w.Keys) > 0 })
	if idx < 0 {
		t.Fatalf("setup: expected a keyed warning, got %v", r1.Warnings)
	}
	r1.Warnings[idx].Keys[0] = tag.Key("MUTATED")
	if got := plan.Report().Warnings[idx].Keys[0]; got == tag.Key("MUTATED") {
		t.Error("mutating Report().Warnings[i].Keys leaked into a later Report() (shallow clone)")
	}
	if len(r1.Operations) > 0 {
		r1.Operations[0] = "MUTATED-OP"
		if got := plan.Report().Operations[0]; got == "MUTATED-OP" {
			t.Error("mutating Report().Operations leaked into a later Report()")
		}
	}
}

// TestMatroskaReturnedDocumentReEditable is a regression guard: the album SimpleTags a
// Matroska write synthesizes (re-emitted canonical values) must carry their rendered bytes,
// so a second unrelated edit on the RETURNED in-memory Document (not a re-parse) does not
// mistake a freshly generated tag for one whose source bytes were too big to capture.
func TestMatroskaReturnedDocumentReEditable(t *testing.T) {
	src := readFixture(t, sampleMKA)
	// Edit 1 changes ARTIST, synthesizing the ARTIST SimpleTag in the returned document.
	out1, doc1 := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Artist, "Edit One"))
	// Edit 2 on the returned document changes a DIFFERENT album tag, leaving the synthesized
	// ARTIST unchanged - this must not fail the preservation check.
	plan, err := doc1.Edit().Set(tag.Album, "Edit Two").Prepare()
	if err != nil {
		t.Fatalf("second edit on the returned Document failed: %v", err)
	}
	if plan.IsNoOp() {
		t.Fatal("second edit should be a real write")
	}
	out2, _ := saveMatroska(t, out1, doc1.Edit().Set(tag.Album, "Edit Two"))
	re := mustParseBytes(t, out2)
	if v, _ := re.Get(tag.Artist); len(v) != 1 || v[0] != "Edit One" {
		t.Errorf("ARTIST lost across the second edit: %v", v)
	}
	if re.Fields().Album != "Edit Two" {
		t.Errorf("second-edit ALBUM not applied: %q", re.Fields().Album)
	}
}

// TestNumericGenreWarningSurvivesNoOp is a regression guard: setting a bare numeric GENRE on
// an ID3 file whose genre already projects to that name is a byte no-op, but the user's
// input was still reinterpreted - so the numeric-genre caveat must survive the no-op
// downgrade rather than vanishing behind a clean "no changes" report.
func TestNumericGenreWarningSurvivesNoOp(t *testing.T) {
	src := readFixture(t, sampleMP3) // already GENRE=Rock
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Genre, "17").Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !plan.IsNoOp() {
		t.Fatal("setup: GENRE=17 on a Rock file should re-project to a byte no-op")
	}
	warned := false
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnNumericGenre {
			warned = true
		}
	}
	if !warned {
		t.Errorf("a no-op numeric-genre set must still surface the caveat; got %v", plan.Report().Warnings)
	}
}

// TestPrepareRejectsInvalidChapterLanguage is a regression guard: an edit authoring
// invalid UTF-8 in a chapter language (not just the title) is rejected at Prepare, so it
// cannot be written verbatim into the EBML and round-trip as raw invalid bytes.
func TestPrepareRejectsInvalidChapterLanguage(t *testing.T) {
	src := readFixture(t, sampleMKA)
	bad := "e\xffg"
	if _, err := mustParseBytes(t, src).Edit().
		SetChapters(wl.Chapter{Start: time.Second, Title: "X", Language: bad}).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("invalid chapter language: err = %v, want ErrInvalidData", err)
	}
	if _, err := mustParseBytes(t, src).Edit().
		SetChapters(wl.Chapter{Start: time.Second, Title: "X", LanguageIETF: bad}).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("invalid chapter IETF language: err = %v, want ErrInvalidData", err)
	}
}

// TestNumericGenreWarnsOnceForDuplicates is a regression guard: a repeated numeric GENRE
// reference must warn once, not once per occurrence (and that single warning is what
// DowngradeNoOp carries onto a no-op report).
func TestNumericGenreWarnsOnceForDuplicates(t *testing.T) {
	plan, err := mustParseBytes(t, readFixture(t, notagsMP3)).Edit().Set(tag.Genre, "17", "17").Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	n := 0
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnNumericGenre {
			n++
		}
	}
	if n != 1 {
		t.Errorf("GENRE=17,17 produced %d numeric-genre warnings, want 1", n)
	}
}

// chapterSetTwo applies a fixed two-chapter list (with End==0, as --add-chapter builds).
func chapterSetTwo(e *wl.Editor) *wl.Editor {
	return e.SetChapters(
		wl.Chapter{Start: 0, Title: "One"},
		wl.Chapter{Start: 5 * time.Second, Title: "Two"},
	)
}

// TestMP4ChapterReapplyMultiNoOpQT is a regression guard on the QuickTime path:
// re-applying an identical multi-chapter list to an already-written file collapses to a
// true no-op. --add-chapter builds chapters with End==0 while a parse derives End from the
// next start, so core.EqualChapters always reports a change and the chapter plan path never
// self-reports NoOp; the round-tripped result (which equals a reparse) is the honest basis
// that collapses it, so a save/copy no longer churns the file.
func TestMP4ChapterReapplyMultiNoOpQT(t *testing.T) {
	data := mp4QTFile([]int{0, 5000}, []string{"Seed A", "Seed B"})
	_, re := execChapters(t, data, chapterSetTwo)
	plan2, err := chapterSetTwo(re.Edit()).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.IsNoOp() {
		t.Errorf("re-applying an identical multi-chapter list (QuickTime path) should be a no-op; ops: %v", plan2.Report().Operations)
	}
}

// TestMP4ChapterReapplyMultiNoOpChpl is a regression guard on the chpl-only fallback (no
// QuickTime track). A track holding the max id leaves no free id, so SetChapters writes
// only the chpl; chplRoundTrip shares decodeChpl's end-fill convention, so a re-apply
// collapses to a no-op just like the QuickTime path - covering the second sub-path, which
// builds its result view differently.
func TestMP4ChapterReapplyMultiNoOpChpl(t *testing.T) {
	build := func(stcoOff uint32) []byte {
		moov := mp4Atom("moov", mp4AudioTrakTkhd(int(uint32(0xFFFFFFFF)), stcoOff))
		return slices.Concat(mp4Ftyp(), moov, mp4Atom("mdat", bytes.Repeat([]byte{0xA7}, 64)))
	}
	tmp := build(0)
	data := build(uint32(bytes.Index(tmp, []byte("mdat")) + 4))

	_, re := execChapters(t, data, chapterSetTwo)
	for _, e := range re.Native().Describe() {
		if e.Kind == "moov chapter track" {
			t.Fatal("expected the chpl-only fallback, but a QuickTime chapter track was created")
		}
	}
	plan2, err := chapterSetTwo(re.Edit()).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.IsNoOp() {
		t.Errorf("re-applying an identical multi-chapter list (chpl-only path) should be a no-op; ops: %v", plan2.Report().Operations)
	}
}

// TestMP4ChapterConflictedReapplyResolvesThenCollapses is the conflict guard: on a
// file whose chpl and QuickTime track disagree, re-applying the (QuickTime-preferred)
// projection must NOT collapse - the write resolves the conflict by rewriting the stale
// chpl, and DowngradeNoOp does not carry the conflict warning. After that write the file is
// consistent, so a further identical re-apply collapses normally.
func TestMP4ChapterConflictedReapplyResolvesThenCollapses(t *testing.T) {
	chpl := mp4Chpl(1, []time.Duration{0, 5 * time.Second}, []string{"Nero One", "Nero Two"})
	data := mp4QTFile([]int{0, 5000}, []string{"QT One", "QT Two"}, chpl)

	doc := mustParseBytes(t, data)
	if !chapterWarn(doc, wl.WarnChapterSourceConflict) {
		t.Fatal("setup: expected a chapter-source-conflict")
	}
	// Re-apply exactly what the conflicted file projects (the QuickTime view wins).
	set := func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "QT One"},
			wl.Chapter{Start: 5 * time.Second, Title: "QT Two"},
		)
	}
	plan1, err := set(doc.Edit()).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan1.IsNoOp() {
		t.Error("a conflicted source must not collapse to a no-op: the write resolves the conflict")
	}

	re := mustParseBytes(t, applyToBytes(t, data, plan1))
	if chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Fatal("the conflict-resolving write should have made the chpl and QuickTime track agree")
	}
	plan2, err := set(re.Edit()).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.IsNoOp() {
		t.Errorf("re-applying on the now-consistent file should be a no-op; ops: %v", plan2.Report().Operations)
	}
}

// TestVorbisDiscReadSideFold is the read-side regression: a Vorbis file carrying both
// native DISC and DISCNUMBER folds both onto canonical DISCNUMBER (two values, one key).
// The native bytes are preserved, so this is a canonical-view-only change and no-op
// detection stays sane - a no-op edit on such a file does not spuriously churn.
func TestVorbisDiscReadSideFold(t *testing.T) {
	data := flacWithComments("TITLE=Folded", "DISC=1", "DISCNUMBER=2")
	doc := mustParseBytes(t, data)
	if v, ok := doc.Get(tag.DiscNumber); !ok || !slices.Equal(v, []string{"1", "2"}) {
		t.Errorf("DISC + DISCNUMBER should fold to DISCNUMBER=[1 2]; got %v (ok=%v)", v, ok)
	}
	plan, err := doc.Edit().Set(tag.Title, "Folded").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("a no-op edit on a folded-disc file should stay a no-op; ops: %v", plan.Report().Operations)
	}
}

// TestMP4ChapterReapplyTruncatedTitleCarriesWarning pins that the no-op collapse still
// surfaces input-loss: re-applying an over-long chapter title to a file already holding its
// truncation is byte-identical (a no-op), but the chapter-title-truncated warning - the
// user's input being trimmed to a container limit - is carried onto the no-op report, exactly
// as value-reduced is for an over-precise date. (--strict does not escalate either; this is
// the informational-surface consistency.)
func TestMP4ChapterReapplyTruncatedTitleCarriesWarning(t *testing.T) {
	longTitle := strings.Repeat("a", 300) // exceeds the 255-byte chapter-title limit
	data := mp4QTFile([]int{0, 5000}, []string{"Seed A", "Seed B"})
	set := func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: longTitle},
			wl.Chapter{Start: 5 * time.Second, Title: "Two"},
		)
	}
	_, re := execChapters(t, data, set)
	plan2, err := set(re.Edit()).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.IsNoOp() {
		t.Fatalf("re-applying the identical (already-truncated) title should be a no-op; ops: %v", plan2.Report().Operations)
	}
	if !planWarns(t, plan2, wl.WarnChapterTitleTruncated) {
		t.Errorf("the chapter-title-truncated input-loss warning must survive the no-op collapse; warnings: %v", plan2.Report().Warnings)
	}
}

// TestMP4ChapterLastEndEditNotCollapsed guards the chapter no-op collapse against dropping a
// real edit: setting an explicit End on the LAST chapter of a QuickTime-track file encodes that
// value into the final sample's stts duration (chapterDeltas). The QuickTime reader now recovers
// that last end, so the round-trip Result carries it and differs from base's own recovered
// last end - the collapse sees a genuine change and lets the write through, with no special-case
// gate (the earlier dropped-last-end behavior needed one).
func TestMP4ChapterLastEndEditNotCollapsed(t *testing.T) {
	data := mp4QTFile([]int{0, 5000}, []string{"A", "B"})
	// Same starts/titles the file already projects, but the last chapter gains an explicit End
	// (8 s). base's recovered last end differs (the synthetic track's final delta yields 6 s), so
	// the round-trip Result reflects a genuine 8 s end and the edit must not collapse.
	plan, err := mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "A"},
		wl.Chapter{Start: 5 * time.Second, End: 8 * time.Second, Title: "B"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("an explicit final-chapter End sets the last sample duration; it must not collapse to a no-op")
	}
	if out := applyToBytes(t, data, plan); bytes.Equal(out, data) {
		t.Error("the final-End edit produced byte-identical output; the requested end time was dropped")
	}
}
