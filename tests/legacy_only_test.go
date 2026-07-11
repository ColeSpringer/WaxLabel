package waxlabel_test

import (
	"bytes"
	"context"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// These tests pin the "preserve and surface" contract for legacy metadata: lint --fix
// auto-strips a legacy container (MP3 ID3v1/APEv2, FLAC leading ID3v2 / trailing ID3v1)
// only when it is provably, fully redundant with the canonical set. A container holding a
// value or non-tag content that lives nowhere else is kept, surfaced as legacy-only rather
// than a bare "(none)", and left in place by the safe fix.

// apeTagBinaryItem builds a footer-only APEv2 tag carrying a single binary (NonText) item -
// content Pairs() skips and a legacy strip cannot prove redundant.
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

// apicFrontFrame builds a valid front-cover APIC frame (v2.3 header).
func apicFrontFrame(mime string, data []byte) []byte {
	body := []byte{0} // Latin-1 text encoding
	body = append(body, mime...)
	body = append(body, 0) // MIME terminator
	body = append(body, 3) // picture type: front cover
	body = append(body, 0) // empty description terminator
	body = append(body, data...)
	return apicFrameRaw(body)
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

// executeToResult applies a plan, returning the post-write Document (as Execute builds it, without
// re-parsing) and the bytes written, so a test can compare the two.
func executeToResult(t *testing.T, src []byte, plan *wl.Plan) (*wl.Document, []byte) {
	t.Helper()
	var w writerTo
	resDoc, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src)))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return resDoc, w.b
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

func TestLintFixPreservesID3v1OnlyMetadata(t *testing.T) {
	// ID3v1 carries the only copy of the title/artist (no front ID3v2). A legacy strip would
	// destroy them, so the safe fix must decline it and the values must survive.
	data := slices.Clone(mp3Audio(t))
	data = append(data, id3v1("V1 Only Title", "V1 Only Artist", 255)...)

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
	data := id3v2(3, textFrame(3, "TIT2", "Same Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, id3v1("Same Title", "", 255)...)

	doc := mustParseBytes(t, data)
	if got := doc.LegacyOnlyKeys(); len(got) != 0 {
		t.Fatalf("a redundant ID3v1 should report no legacy-only keys, got %v", got)
	}

	out := applyLintFix(t, data)
	if bytes.HasSuffix(out, id3v1("Same Title", "", 255)) {
		t.Error("lint --fix should strip a fully redundant ID3v1")
	}
	if mustParseBytes(t, out).Fields().Title != "Same Title" {
		t.Error("the authoritative ID3v2 title must survive the strip")
	}
}

func TestLintFixPreservesFLACLeadingID3v2Only(t *testing.T) {
	// A leading ID3v2 holds the only title (the Vorbis comment is empty). It must surface as a
	// legacy family and legacy-only tag, and the safe fix must keep it.
	data := slices.Concat(id3v2(3, textFrame(3, "TIT2", "Leading Only")), flacWithVendor("test"))

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
	data := slices.Concat(flacWithVendor("test"), id3v1("Trailing Only", "", 255))

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
	lead := id3v2(3, apicFrontFrame("image/png", tinyPNG()))
	data := slices.Concat(lead, flacWithVendor("test", "TITLE=Vorbis Title"))

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

func TestFLACPostWriteMatchesReparseLegacySignals(t *testing.T) {
	// A FLAC with a preserved leading ID3v2 (unique title + cover) edited under the default
	// preserve policy: the Document returned by Execute must report the same legacy signals as a
	// fresh parse of the written bytes, not under-report them.
	lead := id3v2(3, textFrame(3, "TIT2", "Lead Title"), apicFrontFrame("image/png", tinyPNG()))
	data := slices.Concat(lead, flacWithVendor("test"))

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "New Artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	resDoc, out := executeToResult(t, data, plan)
	reparse := mustParseBytes(t, out)

	if got, want := len(resDoc.LegacyOnlyKeys()), len(reparse.LegacyOnlyKeys()); got != want || want == 0 {
		t.Errorf("LegacyOnlyKeys: result=%d reparse=%d (want equal and non-zero)", got, want)
	}
	if resDoc.HasOpaqueLegacyContent() != reparse.HasOpaqueLegacyContent() || !reparse.HasOpaqueLegacyContent() {
		t.Errorf("HasOpaqueLegacyContent: result=%v reparse=%v (want equal and true)",
			resDoc.HasOpaqueLegacyContent(), reparse.HasOpaqueLegacyContent())
	}
	if !hasFamily(resDoc, wl.FamilyID3v2, tag.Title) {
		t.Error("post-write Document dropped the leading ID3v2 family entry")
	}
	for _, code := range []string{"legacy-only-tags", "legacy-opaque-content"} {
		if lintHasCode(resDoc, code) != lintHasCode(reparse, code) {
			t.Errorf("lint %s differs: result=%v reparse=%v", code, lintHasCode(resDoc, code), lintHasCode(reparse, code))
		}
	}
}

func TestMP3PostWriteMatchesReparseOpaqueLegacy(t *testing.T) {
	// An MP3 with an APEv2 binary item edited under the default preserve policy: the returned
	// Document must report opaque legacy content, matching a fresh parse of the written bytes.
	data := id3v2(3, textFrame(3, "TIT2", "V2 Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, apeTagBinaryItem("Cover Art (front)", []byte("\x89PNG binary"))...)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "New Artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	resDoc, out := executeToResult(t, data, plan)
	reparse := mustParseBytes(t, out)

	if !resDoc.HasOpaqueLegacyContent() {
		t.Error("post-write Document under-reported opaque APEv2 content")
	}
	if resDoc.HasOpaqueLegacyContent() != reparse.HasOpaqueLegacyContent() {
		t.Errorf("opaque mismatch: result=%v reparse=%v", resDoc.HasOpaqueLegacyContent(), reparse.HasOpaqueLegacyContent())
	}
	if lintHasCode(resDoc, "legacy-opaque-content") != lintHasCode(reparse, "legacy-opaque-content") {
		t.Error("lint legacy-opaque-content differs between result and reparse")
	}
}

func TestLegacyTagAndOpaqueContentBothSurface(t *testing.T) {
	// A leading ID3v2 carrying both a unique title and a cover: the title is legacy-only and the
	// cover is opaque non-tag content. These are independent signals, so both lint findings must
	// fire, neither shadowing the other.
	lead := id3v2(3, textFrame(3, "TIT2", "Lead Title"), apicFrontFrame("image/png", tinyPNG()))
	data := slices.Concat(lead, flacWithVendor("test"))

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
	data := slices.Concat(id3v2(3, textFrame(3, "TIT2", "Same")), flacWithVendor("test", "TITLE=Same"))

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
