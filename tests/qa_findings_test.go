package waxlabel_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestPrepareRejectsInvalidUTF8 is the F1 regression: an edit introducing invalid UTF-8 in
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

// TestCopiedValueNotSpuriouslyRejected is the F1 copy-path guard: a value read back through
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

// TestLegacyStripDropsTagWithoutFabrication is the F5 regression: a --legacy strip on a
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

// TestClearAllDropsFrontID3 is the F5 companion: clearing every tag and picture on an MP3
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

// TestPlanReportIsDefensiveCopy is the F8 regression (made live by F3's keyed warning):
// mutating a warning's Keys (or the Operations slice) on the value Report() returns must not
// reach back into the plan's own report, so a later Report() is intact.
func TestPlanReportIsDefensiveCopy(t *testing.T) {
	// GENRE=17 on a tagless MP3 produces a KEYED numeric-genre warning (F10).
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

// TestMatroskaReturnedDocumentReEditable is the P2 regression: the album SimpleTags a
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

// TestNumericGenreWarningSurvivesNoOp is the P3 regression: setting a bare numeric GENRE on
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

// TestPrepareRejectsInvalidChapterLanguage is a QA-review regression: an edit authoring
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

// TestNumericGenreWarnsOnceForDuplicates is a QA-review regression: a repeated numeric GENRE
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
