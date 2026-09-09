package waxlabel_test

import (
	"context"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

func findingCodes(fs []wl.Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Code] = true
	}
	return m
}

func TestLintEncoderNoise(t *testing.T) {
	// sample.flac carries ffmpeg's "encoder=Lavf..." stamp. lint reuses the canonical
	// parse-warning code (inherited-encoder), so dump and lint name it identically.
	codes := findingCodes(mustParseFile(t, sampleFLAC).Lint())
	if !codes["inherited-encoder"] {
		t.Errorf("expected inherited-encoder finding; got %v", codes)
	}
}

func TestLintMalformedDate(t *testing.T) {
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.RecordingDate, "not-a-date")
	}))
	codes := findingCodes(doc.Lint())
	if !codes["malformed-date"] {
		t.Errorf("expected malformed-date finding; got %v", codes)
	}
}

func TestLintAcceptsValidDates(t *testing.T) {
	for _, d := range []string{"2021", "2021-06", "2021-06-15"} {
		doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
			e.Set(tag.RecordingDate, d)
		}))
		for _, f := range doc.Lint() {
			if f.Code == "malformed-date" {
				t.Errorf("valid date %q flagged as malformed", d)
			}
		}
	}
}

func TestLintCalendarDates(t *testing.T) {
	lintDate := func(d string) map[string]bool {
		doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
			e.Set(tag.RecordingDate, d)
		}))
		return findingCodes(doc.Lint())
	}

	// Calendar-invalid dates are flagged, including non-leap Feb 29 and
	// non-zero-padded forms.
	for _, d := range []string{"2021-02-31", "2021-13-01", "2021-00-10", "2021-06-00", "2021-2-3", "99999"} {
		if !lintDate(d)["malformed-date"] {
			t.Errorf("date %q should be flagged as malformed", d)
		}
	}
	// Real calendar dates, including a leap-day, are accepted.
	for _, d := range []string{"2020-02-29", "2021-12-31", "2021-06", "2021"} {
		if lintDate(d)["malformed-date"] {
			t.Errorf("valid date %q wrongly flagged", d)
		}
	}
	// Feb 29 in a non-leap year is invalid.
	if !lintDate("2021-02-29")["malformed-date"] {
		t.Error("2021-02-29 (non-leap) should be flagged")
	}
}

func TestLintDuplicatePicture(t *testing.T) {
	png := tinyPNG()
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: png})
		e.AddPicture(wl.Picture{Type: wl.PicBackCover, Data: png}) // same bytes, different role
	}))
	codes := findingCodes(doc.Lint())
	if !codes["duplicate-picture"] {
		t.Errorf("expected duplicate-picture finding; got %v", codes)
	}
}

// TestDuplicatePictureMessageAgrees is a regression guard: the editor's edit-scope warning and the
// linter's whole-set finding must produce the SAME duplicate-picture message even when the
// identical bytes appear under different roles (front + back) - naming a single occurrence's role
// made the two disagree by iteration order. The message names both roles in a stable order.
func TestDuplicatePictureMessageAgrees(t *testing.T) {
	png := tinyPNG()
	plan, err := mustParseBytes(t, readFixture(t, "../testdata/notags.flac")).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: png}).
		AddPicture(wl.Picture{Type: wl.PicBackCover, Data: png}). // same bytes, different role
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var editorMsg string
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnDuplicatePicture {
			editorMsg = w.Message
		}
	}
	if editorMsg == "" {
		t.Fatal("editor did not warn duplicate-picture on identical front+back covers")
	}
	var lintMsg string
	for _, f := range mustParseBytes(t, applyToBytes(t, readFixture(t, "../testdata/notags.flac"), plan)).Lint() {
		if f.Code == "duplicate-picture" {
			lintMsg = f.Message
		}
	}
	if lintMsg == "" {
		t.Fatal("linter did not flag duplicate-picture on the written file")
	}
	if editorMsg != lintMsg {
		t.Errorf("duplicate-picture messages diverge:\n  editor: %q\n  linter: %q", editorMsg, lintMsg)
	}
	for _, role := range []string{"Front cover", "Back cover"} {
		if !strings.Contains(lintMsg, role) {
			t.Errorf("message %q should name the %q role (all roles the bytes appear under)", lintMsg, role)
		}
	}
}

func TestLintSingleValuedMulti(t *testing.T) {
	// A single-valued key (ENCODER) carrying two values - the read-side symptom of
	// a transcoded or multi-scope file - is flagged as a warning. A multivalued key
	// (ARTIST) given two values is not.
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Encoder, "Lavf", "Lavc")
		e.Set(tag.Artist, "A", "B")
	}))
	var found *wl.Finding
	findings := doc.Lint()
	for i := range findings {
		if findings[i].Code == "single-valued-multi" {
			found = &findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected single-valued-multi finding; got %v", findingCodes(findings))
	}
	if found.Key != tag.Encoder {
		t.Errorf("single-valued-multi Key = %q, want ENCODER (ARTIST is multivalued and exempt)", found.Key)
	}
}

func TestLintCustomKeyMultiValueNotFlagged(t *testing.T) {
	// A custom key with several values is legitimate - it has no typed accessor that
	// would lose data, so it gets only the info-level custom-key finding, never the
	// single-valued-multi warning (which exists for the typed projection's
	// first-only read of known keys).
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Key("MY_CUSTOM_FIELD"), "a", "b")
	}))
	codes := findingCodes(doc.Lint())
	if codes["single-valued-multi"] {
		t.Error("a custom multi-valued key was wrongly flagged single-valued-multi")
	}
	if !codes["custom-key"] {
		t.Errorf("a custom key should still get the custom-key info finding; got %v", codes)
	}
}

func TestLintCustomKeyIsInfo(t *testing.T) {
	// A custom (non-vocabulary) key is reported at info severity, so it never flips
	// a clean file to a non-zero exit - it is purely advisory.
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Key("MY_CUSTOM_FIELD"), "x")
	}))
	var found *wl.Finding
	findings := doc.Lint()
	for i := range findings {
		if findings[i].Code == "custom-key" {
			found = &findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected custom-key finding; got %v", findingCodes(findings))
	}
	if found.Severity != wl.LintInfo {
		t.Errorf("custom-key severity = %v, want info", found.Severity)
	}
	if found.Key != tag.Key("MY_CUSTOM_FIELD") {
		t.Errorf("custom-key Key = %q, want MY_CUSTOM_FIELD", found.Key)
	}
}

// TestLintNegativeNumeric checks that negative numbering values show up in lint as
// info-level findings, matching the edit-time advisory without changing the clean-file
// exit behavior.
func TestLintNegativeNumeric(t *testing.T) {
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.TrackNumber, "-1")
	}))
	findings := doc.Lint()
	var found *wl.Finding
	for i := range findings {
		if findings[i].Code == "negative-numeric" {
			found = &findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected negative-numeric finding for TRACKNUMBER=-1; got %v", findingCodes(findings))
	}
	if found.Severity != wl.LintInfo {
		t.Errorf("negative-numeric severity = %v, want info", found.Severity)
	}
	if found.Key != tag.TrackNumber {
		t.Errorf("negative-numeric Key = %q, want TRACKNUMBER", found.Key)
	}
}

func TestLintClean(t *testing.T) {
	// A freshly written file with one good date and no legacy noise should be
	// clean.
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Title, "Clean").Set(tag.RecordingDate, "2022-01-01")
	}))
	if fs := doc.Lint(); len(fs) != 0 {
		t.Errorf("expected no findings, got %v", fs)
	}
}

// writeBack applies edits to a fixture in memory and returns the written bytes.
func writeBack(t *testing.T, fixture string, edit func(*wl.Editor)) []byte {
	t.Helper()
	src := readFixture(t, fixture)
	doc, err := wl.Parse(context.Background(), wl.BytesSource(src))
	if err != nil {
		t.Fatal(err)
	}
	ed := doc.Edit()
	edit(ed)
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var buf writerTo
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(src))); err != nil {
		t.Fatal(err)
	}
	return buf.b
}

type writerTo struct{ b []byte }

func (w *writerTo) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

// TestLintNumericGenre verifies that a numeric genre reference resolved on read surfaces as
// an info-level lint finding, reconciling lint with README's promise that dump and lint both
// report numeric-genre. The MP4 gnre atom (written via --numeric-genre) is the parse-time
// source of the warning; info severity keeps it advisory, never flipping the clean exit.
func TestLintNumericGenre(t *testing.T) {
	base := mp4Tagged(mp4Text("\xa9nam", "T"))
	plan, err := mustParseBytes(t, base).Edit().Set(tag.Genre, "Rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, base, plan)

	findings := mustParseBytes(t, out).Lint()
	var found *wl.Finding
	for i := range findings {
		if findings[i].Code == "numeric-genre" {
			found = &findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected numeric-genre finding; got %v", findingCodes(findings))
	}
	if found.Severity != wl.LintInfo {
		t.Errorf("numeric-genre severity = %v, want info", found.Severity)
	}
}

func TestLintTruncatedAudio(t *testing.T) {
	// A WAV whose data chunk declares far more than the file holds surfaces the
	// parse-time truncated-audio warning as a lint finding.
	dataHdr := append([]byte("data"), wavLE32(100000)...)
	data := wavFile(wavFmtPCM(), append(dataHdr, make([]byte, 200)...))
	codes := findingCodes(mustParseBytes(t, data).Lint())
	if !codes["truncated-audio"] {
		t.Errorf("expected truncated-audio finding; got %v", codes)
	}
}

// TestLintFindingFixable: a finding says whether PlanLintFix acts on it, decided by the same
// gates the fix uses, so a consumer need not hard-code the fixable codes.
func TestLintFindingFixable(t *testing.T) {
	stamped := mustParseBytes(t, flacWithVendor("Lavf61.7.100"))
	var sawEncoder bool
	for _, f := range stamped.Lint() {
		if f.Code == "inherited-encoder" {
			sawEncoder = true
			if !f.Fixable {
				t.Error("inherited-encoder should be fixable")
			}
		}
	}
	if !sawEncoder {
		t.Fatal("fixture should lint inherited-encoder")
	}
	// A legacy container holding the only copy of a value is preserved, not fixable.
	legacyOnly := mustParseBytes(t, mp3WithLegacyOnlyID3v1(t))
	for _, f := range legacyOnly.Lint() {
		if f.Code == "trailing-id3v1" && f.Fixable {
			t.Error("a legacy-only trailing-id3v1 must not be fixable")
		}
	}
	fix := legacyOnly.PlanLintFix()
	if len(fix.Options) != 0 {
		t.Errorf("PlanLintFix must agree with the marker: %+v", fix)
	}
}

// TestLintEncoderFixableOnlyWhereTheFixReaches: the fix clears a stamp from ENCODER and
// neutralizes a container vendor string, but nothing reaches one stored under ENCODEDBY
// (ID3's TENC frame), so such a finding must not advertise a repair lint --fix then
// declines.
func TestLintEncoderFixableOnlyWhereTheFixReaches(t *testing.T) {
	tencOnly := mustParseBytes(t, mp3WithFrames(t, id3Frame(4, "TENC", append([]byte{0}, "Lavf61.7.100"...))))
	var saw bool
	for _, f := range tencOnly.Lint() {
		if f.Code != "inherited-encoder" {
			continue
		}
		saw = true
		if f.Fixable {
			t.Error("a stamp only under ENCODEDBY is out of the fix's reach")
		}
	}
	if !saw {
		t.Fatal("a stamped TENC should lint inherited-encoder")
	}
	if fix := tencOnly.PlanLintFix(); len(fix.Patch.Keys()) != 0 {
		t.Errorf("the fix has nothing to patch: %+v", fix.Patch)
	}
	// A stamp the fix does reach still reads fixable.
	stamped := mustParseBytes(t, flacWithVendor("Lavf61.7.100"))
	for _, f := range stamped.Lint() {
		if f.Code == "inherited-encoder" && !f.Fixable {
			t.Error("a vendor-string stamp is reachable and should read fixable")
		}
	}
}
