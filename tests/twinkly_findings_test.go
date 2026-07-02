package waxlabel_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// This file collects regression tests for the pre-v1.0 correctness pass: the read model must
// not accept (or the writer emit) something the round-trip cannot faithfully preserve while
// the success/warning report claims otherwise.

func countWarning(ws []wl.Warning, code wl.WarningCode) int {
	n := 0
	for _, w := range ws {
		if w.Code == code {
			n++
		}
	}
	return n
}

func hasWarn(ws []wl.Warning, code wl.WarningCode) bool { return countWarning(ws, code) > 0 }

// --- F3: non-conformant Vorbis key validated at read ---

// TestInvalidVorbisKeyWarnsPreservesAndCopiesClean covers the invalid-tag-key handling: an
// empty-name Vorbis comment is dropped from the canonical model (the writer's Key.Valid gate
// would reject it) and surfaced as a warning, so a copy no longer "carries" it and then aborts.
// An unrelated set preserves the raw comment verbatim with post-write warnings equal to a fresh
// re-parse (the round-trip invariant that catches a double-count).
func TestInvalidVorbisKeyWarnsPreservesAndCopiesClean(t *testing.T) {
	src := flacWithComments("TITLE=x", "=orphan") // a comment with an empty name
	doc := mustParseBytes(t, src)

	if !hasWarn(doc.Warnings(), wl.WarnInvalidTagKey) {
		t.Errorf("expected invalid-tag-key warning on parse, got %v", doc.Warnings())
	}
	if doc.Tags().Has(tag.Key("")) {
		t.Error("an empty key must not enter the canonical model")
	}

	// copy onto Ogg must not abort: the invalid key is not in the source model, so it is not
	// re-set on the destination and the write-time Key.Valid gate is never hit.
	dstBytes, err := os.ReadFile(notagsOgg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := doc.PrepareTransfer(mustParseBytes(t, dstBytes)); err != nil {
		t.Errorf("copy onto Ogg must not abort on an invalid source key, got %v", err)
	}

	// An unrelated edit preserves the raw =orphan comment: a fresh re-parse of the output still
	// warns invalid-tag-key exactly once (preserved, and not double-counted).
	plan, err := doc.Edit().Set(tag.Title, "y").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, src, plan))
	if got := countWarning(re.Warnings(), wl.WarnInvalidTagKey); got != 1 {
		t.Errorf("re-parse invalid-tag-key count = %d, want exactly 1 (preserved, no double-count)", got)
	}
}

// TestDuplicateWAVTextValuePreservedOnSave is the end-to-end guard for the finding that a
// blanket first-wins silently dropped a preservable text value. A WAV with two INAM (Title)
// items must carry both through an unrelated edit: the write forces an ID3 chunk whose v2.4
// TIT2 preserves both, so a re-parse still sees both. First-wins stays scoped to number keys.
func TestDuplicateWAVTextValuePreservedOnSave(t *testing.T) {
	src := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Title A"}, [2]string{"INAM", "Title B"}), wavData(400))
	doc := mustParseBytes(t, src)
	if got, _ := doc.Tags().Get(tag.Title); len(got) != 2 {
		t.Fatalf("parse should keep both duplicate INAM titles, got %v", got)
	}
	plan, err := doc.Edit().Set(tag.Artist, "New Artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, src, plan))
	got, _ := re.Tags().Get(tag.Title)
	if len(got) != 2 || got[0] != "Title A" || got[1] != "Title B" {
		t.Errorf("both duplicate titles must survive the save, got %v", got)
	}
}

// TestHostileVorbisKeyDroppedAndWarned covers the reader->model side of the hostile-key
// defense (the direct-injection test in review_test.go covers Change.String's sanitizer). A
// Vorbis comment whose name carries a control byte has no valid canonical key, so the reader
// drops it from the model - it can never leak an un-sanitized key into a diff or preview - and
// warns invalid-tag-key.
func TestHostileVorbisKeyDroppedAndWarned(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("TITLE=x", "BAD\x1bKEY=v"))
	if !hasWarn(doc.Warnings(), wl.WarnInvalidTagKey) {
		t.Errorf("a control-byte Vorbis key should warn invalid-tag-key, got %v", doc.Warnings())
	}
	for k := range doc.Tags().All() {
		if strings.HasPrefix(string(k), "BAD") {
			t.Errorf("a control-byte key must not enter the canonical model, found %q", k)
		}
	}
}

// --- F5: FLAC/Ogg over-range chapter/lyric clamp + warn ---

// maxChapterDuration and maxLRCTime mirror the codec ceilings the writer clamps to (see
// internal/vorbis/chapters.go maxChapterSec and internal/core/synced_lyrics.go maxLRCField).
const maxChapterDuration = 1_000_000 * time.Hour
const maxLRCTime = (1 << 21) * time.Minute

func TestVorbisChapterOverflowClampsAndWarns(t *testing.T) {
	for _, fx := range []string{notagsFLAC, notagsOgg} {
		src, err := os.ReadFile(fx)
		if err != nil {
			t.Fatal(err)
		}
		// The maximum accepted value stores unchanged with no warning.
		maxPlan, err := mustParseBytes(t, src).Edit().SetChapters(wl.Chapter{Start: maxChapterDuration, Title: "Edge"}).Prepare()
		if err != nil {
			t.Fatalf("%s: prepare max chapter: %v", fx, err)
		}
		if hasWarn(maxPlan.Report().Warnings, wl.WarnChapterStartOverflow) {
			t.Errorf("%s: the max-accepted chapter must not warn overflow", fx)
		}
		if re := mustParseBytes(t, applyToBytes(t, src, maxPlan)); len(re.Chapters()) != 1 || re.Chapters()[0].Start != maxChapterDuration {
			t.Errorf("%s: max chapter did not round-trip: %v", fx, re.Chapters())
		}

		// One unit over clamps to the ceiling, warns, and re-parses to the clamped value.
		overPlan, err := mustParseBytes(t, src).Edit().SetChapters(wl.Chapter{Start: maxChapterDuration + time.Second, Title: "Ghost"}).Prepare()
		if err != nil {
			t.Fatalf("%s: prepare over-range chapter: %v", fx, err)
		}
		if overPlan.IsNoOp() {
			t.Errorf("%s: an over-range chapter edit must not collapse to a no-op", fx)
		}
		if !hasWarn(overPlan.Report().Warnings, wl.WarnChapterStartOverflow) {
			t.Errorf("%s: expected chapter-start-overflow, got %v", fx, overPlan.Report().Warnings)
		}
		re := mustParseBytes(t, applyToBytes(t, src, overPlan))
		if len(re.Chapters()) != 1 || re.Chapters()[0].Start != maxChapterDuration {
			t.Errorf("%s: over-range chapter did not clamp+store, got %v", fx, re.Chapters())
		}
	}
}

func TestVorbisSyncedLyricOverflowClampsAndWarns(t *testing.T) {
	for _, fx := range []string{notagsFLAC, notagsOgg} {
		src, err := os.ReadFile(fx)
		if err != nil {
			t.Fatal(err)
		}
		over := wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: maxLRCTime + time.Minute, Text: "x"}}}
		plan, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(over).Prepare()
		if err != nil {
			t.Fatalf("%s: prepare over-range synced lyric: %v", fx, err)
		}
		if plan.IsNoOp() {
			t.Errorf("%s: an over-range synced-lyric edit must not collapse to a no-op", fx)
		}
		if !hasWarn(plan.Report().Warnings, wl.WarnSyncedLyricsTimestampClamped) {
			t.Errorf("%s: expected synced-lyrics-timestamp-clamped, got %v", fx, plan.Report().Warnings)
		}
		re := mustParseBytes(t, applyToBytes(t, src, plan))
		sls := re.SyncedLyrics()
		if len(sls) != 1 || len(sls[0].Lines) != 1 || sls[0].Lines[0].Time != maxLRCTime {
			t.Errorf("%s: over-range synced lyric did not clamp+store, got %v", fx, sls)
		}
	}
}

// --- F6: FLAC preserves an undecodable native PICTURE block across a picture edit ---

// flacWithMalformedPicture builds a FLAC carrying one valid VORBIS_COMMENT block and one
// PICTURE block whose body is too short to decode (warned + skipped at parse).
func flacWithMalformedPicture() []byte {
	out := []byte("fLaC")
	out = append(out, flacBlock(0, false, validStreamInfo())...)      // STREAMINFO
	out = append(out, flacBlock(4, false, renderVC("TITLE=x"))...)    // VORBIS_COMMENT
	out = append(out, flacBlock(6, true, []byte{0, 0, 0, 3, 1, 2})...) // PICTURE, truncated body, last
	return append(out, 0xFF, 0xF8)                                    // audio
}

func TestFLACMalformedPicturePreservedAcrossPictureEdit(t *testing.T) {
	src := flacWithMalformedPicture()
	doc := mustParseBytes(t, src)
	// Parse warns invalid-picture and skips the malformed block from the decoded set.
	if !hasWarn(doc.Warnings(), wl.WarnInvalidPicture) {
		t.Fatalf("parse should warn invalid-picture, got %v", doc.Warnings())
	}
	if len(doc.Pictures()) != 0 {
		t.Fatalf("the malformed picture must not decode, got %d pictures", len(doc.Pictures()))
	}

	// A picture edit re-emits the decoded set; the undecodable block must be preserved, not
	// dropped, so a fresh re-parse still warns invalid-picture.
	plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, src, plan))
	if !hasWarn(re.Warnings(), wl.WarnInvalidPicture) {
		t.Errorf("a picture edit dropped the malformed block; re-parse no longer warns: %v", re.Warnings())
	}
	// The freshly added valid cover is present.
	if len(re.Pictures()) != 1 || re.Pictures()[0].Type != wl.PicFrontCover {
		t.Errorf("the added front cover should read back, got %v", re.Pictures())
	}
}

// TestFLACMalformedPictureSurvivesChainedEdit covers the finding that buildResult omitted the
// malformed-block field from the in-memory result doc: a SECOND picture edit on the returned
// Document (chained, with no re-parse between edits) must still preserve the undecodable block,
// so a fresh parse of the twice-edited output still warns invalid-picture.
func TestFLACMalformedPictureSurvivesChainedEdit(t *testing.T) {
	src := flacWithMalformedPicture()
	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	result, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src)))
	if err != nil {
		t.Fatalf("execute first edit: %v", err)
	}
	// Second picture edit on the returned Document, which is reused without re-parsing.
	plan2, err := result.Edit().AddPicture(wl.Picture{Type: wl.PicBackCover, MIME: "image/png", Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatalf("prepare chained edit: %v", err)
	}
	re := mustParseBytes(t, applyToBytes(t, w.b, plan2))
	if !hasWarn(re.Warnings(), wl.WarnInvalidPicture) {
		t.Errorf("chained picture edit dropped the malformed block; re-parse no longer warns: %v", re.Warnings())
	}
}

// mp3WithMalformedAPIC prepends an ID3v2.4 tag with an undecodable APIC (its MIME has no NUL
// terminator, so validAPIC/decodeAPIC both fail) to real MPEG frames.
func mp3WithMalformedAPIC(t *testing.T) []byte {
	frames, err := os.ReadFile(notagsMP3)
	if err != nil {
		t.Fatal(err)
	}
	ss := func(n int) []byte {
		return []byte{byte(n>>21) & 0x7f, byte(n>>14) & 0x7f, byte(n>>7) & 0x7f, byte(n) & 0x7f}
	}
	apicBody := []byte("\x00image/png")
	frame := append([]byte("APIC"), ss(len(apicBody))...)
	frame = append(frame, 0x00, 0x00)
	frame = append(frame, apicBody...)
	tagBytes := append([]byte("ID3\x04\x00\x00"), ss(len(frame))...)
	return append(append(tagBytes, frame...), frames...)
}

// TestMP3MalformedAPICWarningNotStaleAfterPictureEdit covers the finding that a picture edit
// on an MP3 with a malformed APIC left a stale invalid-picture warning on the returned
// Document. The edit drops the malformed APIC (HasDroppedMalformedPicture), so the returned
// doc's warnings must equal a fresh re-parse of the output - which has no malformed cover and
// so emits no invalid-picture. A tag-only edit preserves the APIC, so the warning stays there.
func TestMP3MalformedAPICWarningNotStaleAfterPictureEdit(t *testing.T) {
	src := mp3WithMalformedAPIC(t)
	doc := mustParseBytes(t, src)
	if !hasWarn(doc.Warnings(), wl.WarnInvalidPicture) {
		t.Fatalf("parse should warn invalid-picture, got %v", doc.Warnings())
	}
	plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	result, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src)))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	reparse := mustParseBytes(t, w.b)
	if a, b := countWarning(result.Warnings(), wl.WarnInvalidPicture), countWarning(reparse.Warnings(), wl.WarnInvalidPicture); a != b {
		t.Errorf("invalid-picture stale: result doc has %d, fresh re-parse has %d", a, b)
	}
	if hasWarn(reparse.Warnings(), wl.WarnInvalidPicture) {
		t.Errorf("a picture edit dropped the malformed APIC, so the output must not warn invalid-picture: %v", reparse.Warnings())
	}
}

// --- F7: stricter trailing-ID3v1 detection ---

// flacWithTrailer builds a FLAC whose final 128 bytes are the given trailer, so the
// trailing-ID3v1 detector inspects exactly that block.
func flacWithTrailer(trailer []byte) []byte {
	out := []byte("fLaC")
	out = append(out, flacBlock(0, true, validStreamInfo())...) // STREAMINFO, last
	out = append(out, 0xFF, 0xF8, 0x00, 0x00)                   // a little audio
	return append(out, trailer...)
}

func TestTrailingID3v1StrictDetection(t *testing.T) {
	// A false positive: audio ending in "TAG" but with a control byte in a text field must not
	// be mistaken for an ID3v1 tag, or the audio-essence boundary is pulled back 128 bytes.
	fake := make([]byte, 128)
	copy(fake, "TAG")
	fake[10] = 0x01 // a control byte in the Title field
	fakeDoc := mustParseBytes(t, flacWithTrailer(fake))
	if hasWarn(fakeDoc.Warnings(), wl.WarnTrailingID3v1) {
		t.Errorf("a fake TAG tail must not be detected as trailing ID3v1: %v", fakeDoc.Warnings())
	}

	// A genuine ID3v1 tag is still detected.
	real := make([]byte, 128)
	copy(real, "TAG")
	copy(real[3:33], "A Title")
	copy(real[93:97], "2020")
	realDoc := mustParseBytes(t, flacWithTrailer(real))
	if !hasWarn(realDoc.Warnings(), wl.WarnTrailingID3v1) {
		t.Errorf("a genuine trailing ID3v1 tag should still be detected: %v", realDoc.Warnings())
	}
}

// --- F2: Matroska non-image cover copy must not destroy the destination cover ---

func TestMatroskaNonImageCoverPreservesDestPNG(t *testing.T) {
	// Source: a FLAC with a genuinely non-image (octet-stream) cover.
	nonImage := bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 40) // 160 bytes, not any image signature
	srcBytes := flacWithComments("TITLE=x")
	addPlan, err := mustParseBytes(t, srcBytes).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "application/octet-stream", Data: nonImage}).
		Prepare(wl.WithUnrecognizedPictures()) // deliberately embed a non-image cover
	if err != nil {
		t.Fatalf("add octet-stream cover: %v", err)
	}
	src := mustParseBytes(t, applyToBytes(t, srcBytes, addPlan))
	if pics := src.Pictures(); len(pics) != 1 || strings.HasPrefix(pics[0].MIME, "image/") {
		t.Fatalf("source cover should be a non-image octet-stream, got %v", pics)
	}

	// Destination: sample.mka, which carries a real PNG cover.
	dstBytes, err := os.ReadFile(sampleMKA)
	if err != nil {
		t.Fatal(err)
	}
	dst := mustParseBytes(t, dstBytes)
	tplan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	// The non-image cover is graded Dropped (Matroska's picture cap is image/*).
	droppedPic := false
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture && it.Disposition == wl.Dropped {
			droppedPic = true
		}
	}
	if !droppedPic {
		t.Errorf("the non-image source cover should be reported dropped: %+v", report.Items)
	}
	// Applying the transfer preserves the destination's PNG cover (the empty representable
	// subset must not trigger ClearPictures).
	re := mustParseBytes(t, applyToBytes(t, dstBytes, tplan))
	if pics := re.Pictures(); len(pics) != 1 || pics[0].MIME != "image/png" {
		t.Errorf("the destination PNG cover must survive an unrepresentable source cover, got %v", pics)
	}
}
