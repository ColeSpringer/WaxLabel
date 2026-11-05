package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// These tests pin the "preserve and surface" contract for legacy metadata: lint --fix auto-strips a
// legacy container (MP3 ID3v1/APEv2, FLAC leading ID3v2 / trailing ID3v1) only when it is provably,
// fully redundant with the canonical set.

// apeTagBinaryItem builds a footer-only APEv2 tag carrying a single binary (NonText) item; content
// Pairs() skips and a legacy strip cannot prove redundant.
func apeTagBinaryItem(key string, value []byte) []byte {
	var hdr [8]byte
	put32le(hdr[0:4], len(value))
	put32le(hdr[4:8], 2) // item flags: binary value (NonText)
	body := append(hdr[:], []byte(key)...)
	body = append(body, 0)
	body = append(body, value...)
	foot := make([]byte, 32)
	copy(foot[0:8], "APETAGEX")
	put32le(foot[8:12], 2000)
	put32le(foot[12:16], len(body)+32)
	put32le(foot[16:20], 1)
	return append(body, foot...)
}

// applyLintFix runs a document's safe remediation and returns the saved bytes.
func applyLintFix(t *testing.T, data []byte) []byte {
	t.Helper()
	doc := mustParseBytes(t, data)
	fix := doc.PlanLintFix()
	plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
	if err != nil {
		t.Fatalf("lint --fix Prepare: %v", err)
	}
	return applyToBytes(t, data, plan)
}

// lintHasCode reports whether a document's lint findings include the given code.
func lintHasCode(doc *wl.Document, code string) bool {
	for _, f := range doc.Lint() {
		if f.Code == code {
			return true
		}
	}
	return false
}

// hasFamily reports whether a document carries a family entry of the given family for key.
func hasFamily(doc *wl.Document, fam wl.Family, key tag.Key) bool {
	for _, f := range doc.Families() {
		if f.Family == fam && f.Key == key {
			return true
		}
	}
	return false
}

// mp3WithLegacyOnlyID3v1 builds an MP3 whose only metadata lives in a trailing ID3v1, so a
// legacy strip would destroy it.
func mp3WithLegacyOnlyID3v1(t *testing.T) []byte {
	t.Helper()
	data := slices.Clone(mp3Audio(t))
	return append(data, testfile.ID3v1("V1 Only Title", "V1 Only Artist", "", "", "", 255)...)
}

func TestLintFixPreservesID3v1OnlyMetadata(t *testing.T) {
	// ID3v1 carries the only copy of the title/artist (no front ID3v2). A legacy strip would
	// destroy them, so the safe fix must decline it and the values must survive.
	data := mp3WithLegacyOnlyID3v1(t)

	doc := mustParseBytes(t, data)
	if len(doc.LegacyOnlyKeys()) == 0 {
		t.Fatal("expected ID3v1-only values to be reported as legacy-only")
	}
	if !lintHasCode(doc, "legacy-only-tags") {
		t.Error("lint should surface a legacy-only-tags finding")
	}

	out := applyLintFix(t, data)
	if !bytes.Contains(out, []byte("V1 Only Title")) {
		t.Error("lint --fix destroyed the ID3v1-only title")
	}
	if len(mustParseBytes(t, out).LegacyOnlyKeys()) == 0 {
		t.Error("legacy-only values missing after lint --fix")
	}
}

func TestLintFixPreservesAPEv2OnlyMetadata(t *testing.T) {
	// The APEv2 holds the only copy of the title. lint --fix must keep it.
	data := slices.Clone(mp3Audio(t))
	data = append(data, apeTag(map[string]string{"Title": "APE Only Title"})...)

	doc := mustParseBytes(t, data)
	if len(doc.LegacyOnlyKeys()) == 0 {
		t.Fatal("expected APEv2-only values to be reported as legacy-only")
	}

	out := applyLintFix(t, data)
	if !bytes.Contains(out, []byte("APE Only Title")) {
		t.Error("lint --fix destroyed the APEv2-only title")
	}
}

func TestLintFixPreservesAPEv2BinaryItem(t *testing.T) {
	// An APEv2 carrying a binary (cover) item holds content the projection does not fold in, so a
	// legacy strip cannot prove it redundant even though it contributes no legacy-only tag.
	data := slices.Clone(mp3Audio(t))
	data = append(data, apeTagBinaryItem("Cover Art (front)", []byte("\x89PNG binary payload"))...)

	doc := mustParseBytes(t, data)
	if !doc.HasOpaqueLegacyContent() {
		t.Fatal("expected the APEv2 binary item to mark opaque legacy content")
	}
	if !lintHasCode(doc, "legacy-opaque-content") {
		t.Error("lint should surface a legacy-opaque-content finding")
	}

	out := applyLintFix(t, data)
	if !bytes.Contains(out, []byte("Cover Art (front)")) {
		t.Error("lint --fix destroyed the APEv2 binary item")
	}
}

func TestLintFixStripsRedundantID3v1(t *testing.T) {
	// When the ID3v1 only duplicates the authoritative ID3v2, it holds nothing unique, so the
	// safe fix still strips it.
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Same Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, testfile.ID3v1("Same Title", "", "", "", "", 255)...)

	doc := mustParseBytes(t, data)
	if got := doc.LegacyOnlyKeys(); len(got) != 0 {
		t.Fatalf("a redundant ID3v1 should report no legacy-only keys, got %v", got)
	}

	out := applyLintFix(t, data)
	if bytes.HasSuffix(out, testfile.ID3v1("Same Title", "", "", "", "", 255)) {
		t.Error("lint --fix should strip a fully redundant ID3v1")
	}
	if mustParseBytes(t, out).Fields().Title != "Same Title" {
		t.Error("the authoritative ID3v2 title must survive the strip")
	}
}

func TestLintFixPreservesFLACLeadingID3v2Only(t *testing.T) {
	// A leading ID3v2 holds the only title (the Vorbis comment is empty). It must surface as a
	// legacy family and legacy-only tag, and the safe fix must keep it.
	data := slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Leading Only")), testfile.TaggedFLAC("test"))

	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatFLAC {
		t.Fatalf("format = %v, want FLAC", doc.Format())
	}
	if len(doc.LegacyOnlyKeys()) == 0 {
		t.Fatal("expected the leading ID3v2 title to be reported as legacy-only")
	}
	if !hasFamily(doc, wl.FamilyID3v2, tag.Title) {
		t.Error("expected a leading-ID3v2 family entry for the title")
	}
	if !lintHasCode(doc, "legacy-only-tags") {
		t.Error("lint should surface a legacy-only-tags finding for FLAC")
	}

	out := applyLintFix(t, data)
	if !bytes.Contains(out, []byte("Leading Only")) {
		t.Error("lint --fix destroyed the FLAC leading ID3v2 title")
	}
	if !hasFamily(mustParseBytes(t, out), wl.FamilyID3v2, tag.Title) {
		t.Error("the leading ID3v2 family entry is missing after lint --fix")
	}
}

func TestLintFixPreservesFLACTrailingID3v1Only(t *testing.T) {
	// A trailing ID3v1 holds the only title. Same parity as MP3: keep it.
	data := slices.Concat(testfile.TaggedFLAC("test"), testfile.ID3v1("Trailing Only", "", "", "", "", 255))

	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatFLAC {
		t.Fatalf("format = %v, want FLAC", doc.Format())
	}
	if len(doc.LegacyOnlyKeys()) == 0 {
		t.Fatal("expected the trailing ID3v1 title to be reported as legacy-only")
	}
	if !hasFamily(doc, wl.FamilyID3v1, tag.Title) {
		t.Error("expected a trailing-ID3v1 family entry for the title")
	}

	out := applyLintFix(t, data)
	if !bytes.Contains(out, []byte("Trailing Only")) {
		t.Error("lint --fix destroyed the FLAC trailing ID3v1 title")
	}
}

func TestLintFixPreservesFLACLeadingID3v2Picture(t *testing.T) {
	// A leading ID3v2 holding only a cover carries no legacy-only tag, but the picture is content
	// the FLAC canonical does not fold in, so the strip must be declined on the opaque flag.
	lead := testfile.ID3v2(3, testfile.FrontCover("image/png", testfile.PNG()))
	data := slices.Concat(lead, testfile.TaggedFLAC("test", "TITLE=Vorbis Title"))

	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatFLAC {
		t.Fatalf("format = %v, want FLAC", doc.Format())
	}
	if !doc.HasOpaqueLegacyContent() {
		t.Fatal("expected the leading ID3v2 picture to mark opaque legacy content")
	}
	if got := doc.LegacyOnlyKeys(); len(got) != 0 {
		t.Fatalf("a picture-only leading ID3v2 should report no legacy-only keys, got %v", got)
	}
	if !lintHasCode(doc, "legacy-opaque-content") {
		t.Error("lint should surface a legacy-opaque-content finding")
	}

	out := applyLintFix(t, data)
	if !bytes.HasPrefix(out, []byte("ID3")) {
		t.Error("lint --fix stripped the leading ID3v2 that held the only cover")
	}
	if mustParseBytes(t, out).Fields().Title != "Vorbis Title" {
		t.Error("the Vorbis title must be unaffected")
	}
}

func TestFLACEditKeepsLeadingID3LegacySignals(t *testing.T) {
	// A FLAC with a preserved leading ID3v2 (unique title + cover) edited under the default
	// preserve policy: the written file still reports the title as legacy-only and the cover as
	// opaque legacy content.
	lead := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Lead Title"), testfile.FrontCover("image/png", testfile.PNG()))
	data := slices.Concat(lead, testfile.TaggedFLAC("test"))

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "New Artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if len(re.LegacyOnlyKeys()) == 0 {
		t.Error("the leading ID3v2 title is no longer legacy-only")
	}
	if !re.HasOpaqueLegacyContent() {
		t.Error("the leading ID3v2 cover is no longer opaque legacy content")
	}
	if !hasFamily(re, wl.FamilyID3v2, tag.Title) {
		t.Error("the leading ID3v2 family entry is missing")
	}
	for _, code := range []string{"legacy-only-tags", "legacy-opaque-content"} {
		if !lintHasCode(re, code) {
			t.Errorf("lint does not report %s", code)
		}
	}
}

func TestMP3EditKeepsOpaqueAPELegacy(t *testing.T) {
	// An MP3 with an APEv2 binary item edited under the default preserve policy: the written file
	// still reports opaque legacy content.
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "V2 Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, apeTagBinaryItem("Cover Art (front)", []byte("\x89PNG binary"))...)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "New Artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if !re.HasOpaqueLegacyContent() {
		t.Error("the APEv2 binary item is no longer opaque legacy content")
	}
	if !lintHasCode(re, "legacy-opaque-content") {
		t.Error("lint does not report legacy-opaque-content")
	}
}

func TestLegacyTagAndOpaqueContentBothSurface(t *testing.T) {
	// A leading ID3v2 carrying both a unique title and a cover: the title is legacy-only and the cover
	// is opaque non-tag content.
	lead := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Lead Title"), testfile.FrontCover("image/png", testfile.PNG()))
	data := slices.Concat(lead, testfile.TaggedFLAC("test"))

	doc := mustParseBytes(t, data)
	if len(doc.LegacyOnlyKeys()) == 0 {
		t.Fatal("expected the leading ID3v2 title to be legacy-only")
	}
	if !doc.HasOpaqueLegacyContent() {
		t.Fatal("expected the leading ID3v2 cover to mark opaque legacy content")
	}
	if !lintHasCode(doc, "legacy-only-tags") {
		t.Error("lint missing legacy-only-tags")
	}
	if !lintHasCode(doc, "legacy-opaque-content") {
		t.Error("lint missing legacy-opaque-content")
	}
}

func TestLintFixStripsRedundantFLACLeadingID3v2(t *testing.T) {
	// A leading ID3v2 whose title duplicates the Vorbis comment holds nothing unique, so the
	// safe fix still strips it.
	data := slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Same")), testfile.TaggedFLAC("test", "TITLE=Same"))

	doc := mustParseBytes(t, data)
	if got := doc.LegacyOnlyKeys(); len(got) != 0 {
		t.Fatalf("a redundant leading ID3v2 should report no legacy-only keys, got %v", got)
	}

	out := applyLintFix(t, data)
	if bytes.HasPrefix(out, []byte("ID3")) {
		t.Error("lint --fix should strip a fully redundant leading ID3v2")
	}
	if mustParseBytes(t, out).Fields().Title != "Same" {
		t.Error("the Vorbis title must survive the strip")
	}
}

// The legacy-conflict check keys on a family entry being legacy, not on the container's name, so
// FLAC's stray leading ID3v2 warns as MP3's ID3v1 does when an edit leaves it holding a stale
// value. The APEv2-native formats, whose APE tag is their own store, must not.
func TestFLACLeadingID3ConflictWarns(t *testing.T) {
	// The legacy value must agree before the edit: the warning is for a divergence the
	// edit introduces, not one the file already had.
	src := append(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Current")), flacWithComments("TITLE=Current")...)
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planHasWarning(plan, wl.WarnLegacyConflict) {
		t.Error("editing TITLE past a stale leading ID3v2 should warn about the legacy conflict")
	}

	wv := readFixture(t, sampleWV)
	plan2, err := mustParseBytes(t, wv).Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if planHasWarning(plan2, wl.WarnLegacyConflict) {
		t.Error("an APEv2-native file must not warn about a conflict with its own tag")
	}
}
