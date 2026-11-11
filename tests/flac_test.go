package waxlabel_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"

	"errors"
)

// flacWithLowercaseComments splices a VORBIS_COMMENT block carrying lowercase field
// names into the notags.flac fixture (whose empty comment block sits at a known offset),
// so a test can drive a real lowercase-keyed FLAC through parse / edit / write.
func flacWithLowercaseComments(t *testing.T) []byte {
	t.Helper()
	data := readFixture(t, "../testdata/notags.flac")
	// The fixture's VORBIS_COMMENT is type 4 at offset 42 ([4-byte hdr][body]); replace its
	// body with one carrying lowercase names. The block is not last (PADDING follows it).
	const off = 42
	bodyLen := int(data[off+1])<<16 | int(data[off+2])<<8 | int(data[off+3])
	body := vorbis.RenderCommentList("test", []vorbis.Comment{
		{Name: "artist", Value: "A"},
		{Name: "title", Value: "Old"},
		{Name: "year", Value: "2019"}, // alias of RecordingDate
	})
	out := append([]byte{}, data[:off]...)
	out = append(out, 0x04, byte(len(body)>>16), byte(len(body)>>8), byte(len(body))) // type 4, not last
	out = append(out, body...)
	return append(out, data[off+4+bodyLen:]...)
}

// Editing one field in a lowercase-keyed file keeps the file's spelling for the edited and
// untouched keys, while an edited date alias rewrites to the preferred DATE.
func TestFLACPreservesLowercaseKeyCasingOnEdit(t *testing.T) {
	src := flacWithLowercaseComments(t)
	doc := mustParseBytes(t, src)
	if v, _ := doc.Get(tag.Title); len(v) == 0 || v[0] != "Old" {
		t.Fatalf("setup: TITLE = %v, want [Old]", v)
	}
	plan, err := doc.Edit().Set(tag.Title, "New").Set(tag.RecordingDate, "2020").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	out := applyToBytes(t, src, plan)

	for _, want := range []string{"title=New", "artist=A", "DATE=2020"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output missing %q (key casing / alias not preserved as expected)", want)
		}
	}
	if bytes.Contains(out, []byte("TITLE=New")) {
		t.Error("edited TITLE was upper-cased; the file's lowercase spelling was not preserved")
	}
}

const sampleFLAC = "../testdata/sample.flac"

func mustParseFile(t *testing.T, path string) *wl.Document {
	t.Helper()
	doc, err := wl.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", path, err)
	}
	return doc
}

// writeTempFile writes content to a fresh temp file and returns its path.
func writeTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// readFixture reads a checked-in test file.
func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// copyToTemp copies a fixture into a writable temp file so save tests do not
// mutate the checked-in fixtures.
func copyToTemp(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	return dst
}

func TestParseReadsTagsAndProperties(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)

	if doc.Format() != wl.FormatFLAC {
		t.Errorf("Format = %v, want FLAC", doc.Format())
	}
	f := doc.Fields()
	if f.Title != "Original Title" {
		t.Errorf("Title = %q, want %q", f.Title, "Original Title")
	}
	if len(f.Artists) != 1 || f.Artists[0] != "Original Artist" {
		t.Errorf("Artists = %v, want [Original Artist]", f.Artists)
	}
	if f.Album != "Test Album" {
		t.Errorf("Album = %q, want %q", f.Album, "Test Album")
	}

	props := doc.Properties()
	tr := props.First()
	if tr.SampleRate != 44100 {
		t.Errorf("SampleRate = %d, want 44100", tr.SampleRate)
	}
	if tr.Channels != 2 {
		t.Errorf("Channels = %d, want 2", tr.Channels)
	}
}

// ffmpeg stamps "encoder=Lavf..." in FLAC fixtures, and the parser must surface it.
func TestParseSurfacesInheritedEncoder(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	found := false
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnInheritedEncoder {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an inherited-encoder warning; got %v", doc.Warnings())
	}
}

func TestNoOpSaveBackWritesNothing(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	before, _ := os.ReadFile(path)
	stat0, _ := os.Stat(path)

	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !plan.IsNoOp() {
		t.Fatalf("expected a no-op plan for an unedited document")
	}
	_, res, err := plan.Execute(context.Background(), wl.SaveBack())
	if err != nil {
		t.Fatalf("Execute SaveBack: %v", err)
	}
	if res.Committed {
		t.Errorf("no-op SaveBack reported Committed=true; should write nothing")
	}

	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Errorf("no-op SaveBack changed the file bytes")
	}
	stat1, _ := os.Stat(path)
	if !stat0.ModTime().Equal(stat1.ModTime()) {
		t.Errorf("no-op SaveBack changed mtime")
	}
}

func TestNoOpWriteToProducesIdenticalBytes(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	src, _ := os.ReadFile(sampleFLAC)

	plan, err := doc.Edit().Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	var out bytes.Buffer
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&out, wl.BytesSource(src))); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if !bytes.Equal(src, out.Bytes()) {
		t.Errorf("no-op WriteTo produced %d bytes, want identical %d", out.Len(), len(src))
	}
}

func TestEditPreservesUneditedFields(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)

	plan, err := doc.Edit().Set(tag.Title, "New Title").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.IsNoOp() {
		t.Fatal("editing Title should not be a no-op")
	}
	_, res, err := plan.Execute(context.Background(), wl.SaveBack())
	if err != nil {
		t.Fatalf("SaveBack: %v", err)
	}
	if !res.Committed {
		t.Fatal("expected Committed=true")
	}

	got := mustParseFile(t, path).Fields()
	if got.Title != "New Title" {
		t.Errorf("Title = %q, want New Title", got.Title)
	}
	// Preservation-first: untouched fields survive verbatim.
	if got.Album != "Test Album" {
		t.Errorf("Album = %q, want preserved Test Album", got.Album)
	}
	if len(got.Artists) != 1 || got.Artists[0] != "Original Artist" {
		t.Errorf("Artists = %v, want preserved [Original Artist]", got.Artists)
	}
}

func TestDeterministicGolden(t *testing.T) {
	src, _ := os.ReadFile(sampleFLAC)
	render := func() []byte {
		doc, err := wl.Parse(context.Background(), wl.BytesSource(src))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		plan, err := doc.Edit().Set(tag.Title, "Deterministic").Set(tag.Genre, "Ambient").Prepare()
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		var out bytes.Buffer
		if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&out, wl.BytesSource(src))); err != nil {
			t.Fatalf("WriteTo: %v", err)
		}
		return out.Bytes()
	}
	a, b := render(), render()
	if !bytes.Equal(a, b) {
		t.Errorf("same input + same edit produced different bytes (%d vs %d)", len(a), len(b))
	}
}

// Accessor results must not alias the Document's internal state.
func TestAccessorsAreDetached(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)

	ts := doc.Tags()
	ts.Set(tag.Title, "mutated copy")
	if got, _ := doc.Get(tag.Title); got[0] == "mutated copy" {
		t.Error("mutating Tags() copy affected the Document")
	}

	props := doc.Properties()
	props.Tracks[0].SampleRate = 999
	if doc.Properties().First().SampleRate == 999 {
		t.Error("mutating Properties() copy affected the Document")
	}

	f := doc.Fields()
	if len(f.Artists) > 0 {
		f.Artists[0] = "x"
		if doc.Fields().Artists[0] == "x" {
			t.Error("mutating Fields() slice affected the Document")
		}
	}
}

func TestAddAndReadPicture(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)

	png := testfile.PNG()
	pic := wl.Picture{Type: wl.PicFrontCover, Data: png}
	plan, err := doc.Edit().AddPicture(pic).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatalf("SaveBack: %v", err)
	}

	pics := mustParseFile(t, path).Pictures()
	if len(pics) != 1 {
		t.Fatalf("got %d pictures, want 1", len(pics))
	}
	if pics[0].Type != wl.PicFrontCover {
		t.Errorf("picture type = %v, want FrontCover", pics[0].Type)
	}
	if pics[0].MIME != "image/png" {
		t.Errorf("sniffed MIME = %q, want image/png", pics[0].MIME)
	}
	if pics[0].Width != 1 || pics[0].Height != 1 {
		t.Errorf("sniffed dims = %dx%d, want 1x1", pics[0].Width, pics[0].Height)
	}
	if !bytes.Equal(pics[0].Data, png) {
		t.Error("picture data not preserved")
	}
}

// Indexed GIF (8-color GCT): sniffed palette size must fill PICTURE Colors and survive reparse.
func TestIndexedPictureColorsRoundTrip(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)

	// GIF89a, 4x4, packed 0x82: GCT present (0x80), size field 2 -> 8 colors.
	idxGIF := append([]byte("GIF89a"), 0x04, 0x00, 0x04, 0x00, 0x82, 0x00, 0x00)
	plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: idxGIF}).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatalf("SaveBack: %v", err)
	}

	pics := mustParseFile(t, path).Pictures()
	if len(pics) != 1 {
		t.Fatalf("got %d pictures, want 1", len(pics))
	}
	if pics[0].MIME != "image/gif" {
		t.Errorf("sniffed MIME = %q, want image/gif", pics[0].MIME)
	}
	if pics[0].Colors != 8 {
		t.Errorf("palette size = %d, want 8 (the sniffed GCT entry count must reach the PICTURE block)", pics[0].Colors)
	}
}

func TestSourceChangedDetected(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Mutate the file out from under the parsed document.
	extra, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(extra, 0), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	_, _, err = plan.Execute(context.Background(), wl.SaveBack())
	if !errors.Is(err, waxerr.ErrSourceChanged) {
		t.Errorf("err = %v, want ErrSourceChanged", err)
	}
}

func TestAudioEssenceStableAcrossTagEdit(t *testing.T) {
	ctx := context.Background()
	before, err := mustParseFile(t, sampleFLAC).HashAudioEssence(ctx)
	if err != nil {
		t.Fatalf("HashAudioEssence: %v", err)
	}

	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)
	plan, _ := doc.Edit().Set(tag.Title, "Different").Prepare()
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatalf("SaveBack: %v", err)
	}
	after, err := mustParseFile(t, path).HashAudioEssence(ctx)
	if err != nil {
		t.Fatalf("HashAudioEssence after: %v", err)
	}
	if !before.Equal(after) {
		t.Errorf("essence changed across a tag-only edit:\n  before %s\n  after  %s", before, after)
	}
	if before.ExtentVersion != "flac-frames-v2" {
		t.Errorf("ExtentVersion = %q, want flac-frames-v2", before.ExtentVersion)
	}
}

func TestVerifyEssenceOnWrite(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, "Verified").Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatalf("SaveBack with verify: %v", err)
	}
}

func TestParseNoTags(t *testing.T) {
	doc := mustParseFile(t, "../testdata/notags.flac")
	if doc.Fields().Title != "" {
		t.Errorf("expected empty title, got %q", doc.Fields().Title)
	}
	// Must still expose valid audio properties.
	if doc.Properties().First().SampleRate != 44100 {
		t.Errorf("SampleRate = %d, want 44100", doc.Properties().First().SampleRate)
	}
}

// A valid FLAC, including a minimal near-zero-bitrate one whose STREAMINFO declares no total,
// must not be flagged truncated.
func TestFLACValidFilesNotFlaggedTruncated(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"synthetic minimal (bitrate ~0)", synthFLAC()},
		{"real fixture", readFixture(t, sampleFLAC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if doc := mustParseBytes(t, tc.data); hasWarning(doc, wl.WarnTruncatedAudio) {
				t.Errorf("valid FLAC flagged truncated; got %v", doc.Warnings())
			}
		})
	}
}

// Requested padding above FLAC's ~16 MiB per-block limit is clamped to it, and the write must warn
// padding-clamped. A sane padding does not warn.
func TestFLACPaddingClampWarns(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	hasClamp := func(p *wl.Plan) bool {
		for _, w := range p.Report().Warnings {
			if w.Code == wl.WarnPaddingClamped {
				return true
			}
		}
		return false
	}

	big, err := doc.Edit().Set(tag.Title, "Padded").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: (1 << 24) + (1 << 20)})) // ~17 MiB > FLAC's block cap
	if err != nil {
		t.Fatal(err)
	}
	if !hasClamp(big) {
		t.Errorf("padding above FLAC's block cap must warn padding-clamped; got %v", big.Report().Warnings)
	}

	small, err := doc.Edit().Set(tag.Title, "Padded").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 4096}))
	if err != nil {
		t.Fatal(err)
	}
	if hasClamp(small) {
		t.Errorf("a 4 KiB padding must not warn padding-clamped; got %v", small.Report().Warnings)
	}
}

// numberedValues is n distinct values.
func numberedValues(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(i)
	}
	return out
}

// A write holds no more Vorbis comments than a reader with default limits takes, and a file read
// under a raised limit keeps the comments it holds.
func TestFLACCommentCountLimit(t *testing.T) {
	const limit = 100000
	src := flacWithComments()

	plan, err := mustParseBytes(t, src).Edit().Set(tag.Comment, numberedValues(limit)...).Prepare()
	if err != nil {
		t.Fatalf("%d comments: %v", limit, err)
	}
	if got, _ := mustParseBytes(t, applyToBytes(t, src, plan)).Get(tag.Comment); len(got) != limit {
		t.Errorf("read back %d comments, want %d", len(got), limit)
	}

	_, err = mustParseBytes(t, src).Edit().Set(tag.Comment, numberedValues(limit+1)...).Prepare()
	if !errors.Is(err, waxerr.ErrContentTooLarge) {
		t.Errorf("%d comments: err = %v, want ErrContentTooLarge", limit+1, err)
	}

	over := flacWithComments(append([]string{"TITLE=old"}, slices.Repeat([]string{"C=v"}, limit)...)...)
	raised := wl.WithLimits(wl.Limits{MaxElements: 2 * limit})
	doc, err := wl.Parse(context.Background(), wl.BytesSource(over), raised)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = doc.Edit().Set(tag.Title, "new").Prepare()
	if err != nil {
		t.Fatalf("an edit that adds no comment to a file holding %d: %v", limit+1, err)
	}
	var out writerTo
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&out, wl.BytesSource(over))); err != nil {
		t.Fatalf("writing it: %v", err)
	}
	if _, err := doc.Edit().Set(tag.Artist, "one more").Prepare(); !errors.Is(err, waxerr.ErrContentTooLarge) {
		t.Errorf("one more comment than the file held: err = %v, want ErrContentTooLarge", err)
	}
}

// A comment block past the 24-bit block length is refused as content too large for the format, not
// as a damaged file.
func TestFLACCommentBlockSizeLimit(t *testing.T) {
	_, err := mustParseBytes(t, flacWithComments("TITLE=x")).Edit().Set(tag.Lyrics, strings.Repeat("la ", 6<<20)).Prepare()
	if !errors.Is(err, waxerr.ErrContentTooLarge) {
		t.Errorf("an 18 MiB comment block: err = %v, want ErrContentTooLarge", err)
	}
}

// A write holds no more metadata blocks than a reader with default limits takes.
func TestFLACBlockCountLimit(t *testing.T) {
	const limit = 100000
	// STREAMINFO, the comment block, APPLICATION blocks and one PADDING block: limit blocks in all.
	blocks := [][]byte{testfile.CommentBlock("test", "TITLE=x")}
	for len(blocks) < limit-2 {
		blocks = append(blocks, testfile.FLACBlock(2, []byte("test")))
	}
	src := testfile.FLAC(append(blocks, testfile.FLACBlock(1, make([]byte, 64)))...)
	doc := mustParseBytes(t, src)

	plan, err := doc.Edit().Set(tag.Title, "y").Prepare()
	if err != nil {
		t.Fatalf("an edit that adds no block: %v", err)
	}
	if got := mustParseBytes(t, applyToBytes(t, src, plan)).Fields().Title; got != "y" {
		t.Errorf("title = %q, want y", got)
	}
	_, err = doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
	if !errors.Is(err, waxerr.ErrContentTooLarge) {
		t.Errorf("a block past the limit: err = %v, want ErrContentTooLarge", err)
	}
}

// A field name no other reader takes for the key (a space at its edge, a letter outside ASCII) is
// reported and left alone: the key reads as unset, and setting it writes the real field beside it.
func TestFLACNonConformingFieldNameStaysOpaque(t *testing.T) {
	for _, name := range []string{" ALBUM", "ALBUM ", "ALBUMſ", "albıum"} {
		src := flacWithComments(name+"=theirs", "TITLE=x")
		doc := mustParseBytes(t, src)
		if v, ok := doc.Get(tag.Album); ok {
			t.Errorf("%q read as ALBUM %q", name, v)
		}
		if !hasWarning(doc, wl.WarnInvalidTagKey) {
			t.Errorf("%q: no invalid-tag-key warning in %v", name, doc.Warnings())
		}
		plan, err := doc.Edit().Set(tag.Album, "ours").Prepare()
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		out := applyToBytes(t, src, plan)
		for _, want := range []string{name + "=theirs", "ALBUM=ours"} {
			if !bytes.Contains(out, []byte(want)) {
				t.Errorf("%q: output lacks %q", name, want)
			}
		}
		if got := mustParseBytes(t, out).Fields().Album; got != "ours" {
			t.Errorf("%q: ALBUM reads back %q, want ours", name, got)
		}
	}
}

// A comment that is not valid UTF-8 reads with U+FFFD in place of the invalid bytes, is reported,
// and keeps its bytes through an edit of another key and an edit of its own key that keeps it.
func TestVorbisInvalidUTF8ValueIsReportedAndKept(t *testing.T) {
	const raw = "TITLE=caf\xe9"
	for name, src := range map[string][]byte{
		"FLAC":     flacWithComments(raw, "ALBUM=A"),
		"Ogg FLAC": testfile.OggFLAC(testfile.CommentBlock("test", raw, "ALBUM=A")),
	} {
		doc := mustParseBytes(t, src)
		if got := doc.Fields().Title; got != "caf\uFFFD" {
			t.Errorf("%s: TITLE = %q, want the substitution", name, got)
		}
		keyed := false
		for _, w := range doc.Warnings() {
			keyed = keyed || w.Code == wl.WarnInvalidText && slices.Equal(w.Keys, []tag.Key{tag.Title})
		}
		if !keyed {
			t.Errorf("%s: no invalid-text warning keyed TITLE in %v", name, doc.Warnings())
		}
		if !lintHasCode(doc, "invalid-text") {
			t.Errorf("%s: lint does not report invalid-text", name)
		}
		for edit, ed := range map[string]*wl.Editor{
			"another key":      doc.Edit().Add(tag.Artist, "Band"),
			"a value added":    doc.Edit().Add(tag.Title, "second"),
			"the same value":   doc.Edit().Set(tag.Title, "caf\uFFFD", "second"),
			"a comment beside": doc.Edit().Set(tag.Comment, "note"),
		} {
			plan, err := ed.Prepare()
			if err != nil {
				t.Fatalf("%s, %s: %v", name, edit, err)
			}
			if out := applyToBytes(t, src, plan); !bytes.Contains(out, []byte(raw)) {
				t.Errorf("%s, %s: the write lost the bytes of %q", name, edit, raw)
			}
		}
	}
}

// An owned comment that no projector reads (a chapter time that does not parse and its title, a
// repeated chapter, an untimed or second SYNCEDLYRICS) is reported, survives an unrelated edit,
// and goes with a clear of its kind or of its name. The write happens though no tag, chapter or
// synced lyric changes and the padding takes up the freed bytes.
func TestVorbisUnreadOwnedCommentsAreReportedAndCleared(t *testing.T) {
	entries := []string{
		"TITLE=x",
		"CHAPTER001=bogus", "CHAPTER001NAME=Intro",
		"CHAPTER002=00:00:01.000", "CHAPTER002=00:00:05.000", "CHAPTER002NAME=Verse",
		"SYNCEDLYRICS=[00:01.00]sung", "SYNCEDLYRICS=just words",
		"METADATA_BLOCK_PICTURE=not~base64",
	}
	const junkChapters, junkLyrics, junkPicture = "CHAPTER001=bogus", "SYNCEDLYRICS=just words", "METADATA_BLOCK_PICTURE=not~base64"
	for name, src := range map[string][]byte{
		"FLAC":     testfile.FLAC(testfile.CommentBlock("test", entries...), testfile.FLACBlock(1, make([]byte, 1024))),
		"Ogg FLAC": testfile.OggFLAC(testfile.CommentBlock("test", entries...)),
	} {
		doc := mustParseBytes(t, src)
		if n := countWarnings(doc, wl.WarnMalformedTagEntry); n != 2 {
			t.Errorf("%s: %d malformed-tag-entry warnings in %v, want one for the chapters and one for the lyrics", name, n, doc.Warnings())
		}
		if chs := doc.Chapters(); len(chs) != 1 || chs[0].Start != 5*time.Second || chs[0].Title != "Verse" {
			t.Errorf("%s: chapters = %+v, want the one readable chapter", name, chs)
		}

		for edit, tc := range map[string]struct {
			ed            *wl.Editor
			gone          []string // what the write removes
			warnsDropped  bool
			chapters, lrc int // what a re-read holds
		}{
			"an unrelated edit":         {ed: doc.Edit().Set(tag.Artist, "Band"), chapters: 1, lrc: 1},
			"the chapters cleared":      {ed: doc.Edit().ClearChapters(), gone: []string{junkChapters, "CHAPTER001NAME", "CHAPTER002"}, lrc: 1},
			"the synced lyrics cleared": {ed: doc.Edit().ClearSyncedLyrics(), gone: []string{junkLyrics, "SYNCEDLYRICS"}, chapters: 1},
			"a chapter's name cleared":  {ed: doc.Edit().Clear("CHAPTER001"), gone: []string{junkChapters}, chapters: 1, lrc: 1},
			"a title's name cleared":    {ed: doc.Edit().Clear("CHAPTER001NAME"), gone: []string{"CHAPTER001NAME"}, chapters: 1, lrc: 1},
			"a repeat's name cleared":   {ed: doc.Edit().Clear("CHAPTER002"), gone: []string{"CHAPTER002=00:00:01.000"}, chapters: 1, lrc: 1},
			"the lyrics name cleared":   {ed: doc.Edit().Clear("SYNCEDLYRICS"), gone: []string{junkLyrics}, chapters: 1, lrc: 1},
			"the pictures cleared":      {ed: doc.Edit().ClearPictures(), gone: []string{junkPicture}, chapters: 1, lrc: 1},
			"the picture name cleared":  {ed: doc.Edit().Clear("METADATA_BLOCK_PICTURE"), gone: []string{junkPicture}, chapters: 1, lrc: 1},
			"a picture added":           {ed: doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}), chapters: 1, lrc: 1},
			"the chapters replaced": {ed: doc.Edit().SetChapters(wl.Chapter{Start: 2 * time.Second, Title: "New"}),
				gone: []string{junkChapters, "CHAPTER001NAME", "CHAPTER002=00:00:01.000"}, warnsDropped: true, chapters: 1, lrc: 1},
		} {
			plan, err := tc.ed.Prepare()
			if err != nil {
				t.Fatalf("%s, %s: %v", name, edit, err)
			}
			if plan.IsNoOp() {
				t.Errorf("%s, %s: the plan is a no-op", name, edit)
				continue
			}
			report := plan.Report()
			if hasOp := slices.ContainsFunc(report.Operations, func(op string) bool { return strings.Contains(op, "unread") }); hasOp != (len(tc.gone) > 0 && !tc.warnsDropped) {
				t.Errorf("%s, %s: operations = %q", name, edit, report.Operations)
			}
			if got := hasWarn(report.Warnings, wl.WarnMalformedTagEntryDropped); got != tc.warnsDropped {
				t.Errorf("%s, %s: malformed-tag-entry-dropped warned = %v, want %v (%v)", name, edit, got, tc.warnsDropped, report.Warnings)
			}
			out := applyToBytes(t, src, plan)
			for _, junk := range []string{junkChapters, "CHAPTER001NAME=Intro", "CHAPTER002=00:00:01.000", junkLyrics, junkPicture} {
				wantGone := slices.ContainsFunc(tc.gone, func(g string) bool { return strings.HasPrefix(junk, g) })
				if gone := !bytes.Contains(out, []byte(junk)); gone != wantGone {
					t.Errorf("%s, %s: %q gone = %v, want %v", name, edit, junk, gone, wantGone)
				}
			}
			re := mustParseBytes(t, out)
			if len(re.Chapters()) != tc.chapters || len(re.SyncedLyrics()) != tc.lrc {
				t.Errorf("%s, %s: re-read %d chapter(s) and %d synced lyrics, want %d and %d", name, edit, len(re.Chapters()), len(re.SyncedLyrics()), tc.chapters, tc.lrc)
			}
			if name == "FLAC" && edit != "a picture added" && len(out) != len(src) {
				t.Errorf("%s, %s: the write changed the file size (%d -> %d); the padding should take up the difference", name, edit, len(src), len(out))
			}
		}
	}
}

// A key with a space at its edge, which only a hand-built Key can be, is not written to a Vorbis
// comment under a name nothing reads back: the plan says so, with or without padding to reuse.
func TestVorbisKeyWithEdgeWhitespaceIsDroppedWithAWarning(t *testing.T) {
	for name, src := range map[string][]byte{
		"FLAC":            flacWithComments("TITLE=x"),
		"FLAC no padding": testfile.FLAC(testfile.CommentBlock("test", "TITLE=x")),
		"Ogg FLAC":        testfile.OggFLAC(testfile.CommentBlock("test", "TITLE=x")),
	} {
		plan, err := mustParseBytes(t, src).Edit().Set(tag.Key(" EDGE"), "v").Prepare()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !hasWarn(plan.Report().Warnings, wl.WarnValueDropped) {
			t.Errorf("%s: no value-dropped warning in %v", name, plan.Report().Warnings)
		}
		if out := applyToBytes(t, src, plan); bytes.Contains(out, []byte("EDGE")) {
			t.Errorf("%s: the write holds the key under a name nothing reads", name)
		}
	}
}

// A chapter added beside one whose title holds invalid bytes keeps those bytes.
func TestVorbisChapterEditKeepsInvalidUTF8Title(t *testing.T) {
	src := flacWithComments("CHAPTER001=00:00:00.000", "CHAPTER001NAME=Intr\xf6")
	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().SetChapters(append(doc.Chapters(), wl.Chapter{Start: time.Second, Title: "Two"})...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, []byte("CHAPTER001NAME=Intr\xf6")) || !bytes.Contains(out, []byte("CHAPTER002NAME=Two")) {
		t.Error("the chapter edit did not keep the stored title bytes beside the new chapter")
	}
}

// Clearing the pictures also removes a PICTURE block that does not decode, which a picture added
// beside it keeps.
func TestClearPicturesRemovesAnUnreadablePictureBlock(t *testing.T) {
	bad := testfile.FLACBlock(6, []byte{0, 0, 0, 3, 1, 2})
	for name, src := range map[string][]byte{
		"FLAC":     testfile.FLAC(testfile.CommentBlock("test", "TITLE=x"), bad, testfile.FLACBlock(1, make([]byte, 64))),
		"Ogg FLAC": testfile.OggFLAC(testfile.CommentBlock("test", "TITLE=x"), bad),
	} {
		doc := mustParseBytes(t, src)
		if !hasWarning(doc, wl.WarnInvalidPicture) {
			t.Fatalf("%s: setup: no invalid-picture warning in %v", name, doc.Warnings())
		}
		for edit, tc := range map[string]struct {
			ed       *wl.Editor
			gone     bool
			pictures int
		}{
			"cleared":           {doc.Edit().ClearPictures(), true, 0},
			"cleared and added": {doc.Edit().ClearPictures().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}), true, 1},
			"added":             {doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}), false, 1},
			"an unrelated edit": {doc.Edit().Set(tag.Artist, "Band"), false, 0},
		} {
			plan, err := tc.ed.Prepare()
			if err != nil {
				t.Fatalf("%s, %s: %v", name, edit, err)
			}
			if plan.IsNoOp() {
				t.Errorf("%s, %s: the plan is a no-op", name, edit)
				continue
			}
			if hasOp := slices.ContainsFunc(plan.Report().Operations, func(op string) bool { return strings.Contains(op, "unreadable PICTURE") }); hasOp != tc.gone {
				t.Errorf("%s, %s: operations = %q", name, edit, plan.Report().Operations)
			}
			re := mustParseBytes(t, applyToBytes(t, src, plan))
			if gone := !hasWarning(re, wl.WarnInvalidPicture); gone != tc.gone || len(re.Pictures()) != tc.pictures {
				t.Errorf("%s, %s: unreadable block gone = %v with %d picture(s), want %v with %d", name, edit, gone, len(re.Pictures()), tc.gone, tc.pictures)
			}
		}
	}
}

// A copy puts the source's pictures in place of the destination's without having been asked to
// clear anything, so a cover comment and a PICTURE block the destination holds unread stay.
func TestTransferKeepsUnreadablePictures(t *testing.T) {
	src := mustParseBytes(t, coverBearingFLAC(t, "Source"))
	for name, dst := range map[string][]byte{
		"FLAC": testfile.FLAC(testfile.CommentBlock("test", "TITLE=x", "METADATA_BLOCK_PICTURE=not~base64"),
			testfile.FLACBlock(6, []byte{0, 0, 0, 3, 1, 2}), testfile.FLACBlock(1, make([]byte, 64))),
		"Ogg FLAC": testfile.OggFLAC(testfile.CommentBlock("test", "TITLE=x", "METADATA_BLOCK_PICTURE=not~base64"),
			testfile.FLACBlock(6, []byte{0, 0, 0, 3, 1, 2})),
	} {
		plan, _, err := src.PrepareTransfer(mustParseBytes(t, dst))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out := applyToBytes(t, dst, plan)
		re := mustParseBytes(t, out)
		if n := countWarnings(re, wl.WarnInvalidPicture); n != 2 || !bytes.Contains(out, []byte("METADATA_BLOCK_PICTURE=not~base64")) {
			t.Errorf("%s: %d invalid-picture warning(s) after the copy, want the comment and the block kept: %v", name, n, re.Warnings())
		}
		if len(re.Pictures()) != 1 {
			t.Errorf("%s: %d picture(s) after the copy, want the source's cover", name, len(re.Pictures()))
		}
	}
}

// An edit that is discarded writes nothing, however the padding policy would resize the file's
// padding on a write: more padding than the policy's cap is cut only by a write that has
// something else to do, or that was asked for the padding, and the plan then names the cut.
func TestFLACDiscardedEditDoesNotRewriteToResizePadding(t *testing.T) {
	for name, pad := range map[string][]byte{
		"more padding than the cap": testfile.FLACBlock(1, make([]byte, 4<<20)),
		"no padding":                nil,
	} {
		blocks := [][]byte{testfile.CommentBlock("test", "TITLE=x")}
		if pad != nil {
			blocks = append(blocks, pad)
		}
		src := testfile.FLAC(blocks...)
		doc := mustParseBytes(t, src)

		plan, err := doc.Edit().Set("CHAPTER005", "x").Prepare()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !plan.IsNoOp() || !hasWarn(plan.Report().Warnings, wl.WarnValueDropped) {
			t.Errorf("%s: a discarded edit plans a write: no-op %v, operations %q, warnings %v", name,
				plan.IsNoOp(), plan.Report().Operations, plan.Report().Warnings)
		}

		// The padding asked for is a change of its own.
		plan, err = doc.Edit().Set("CHAPTER005", "x").Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 2048, Min: 2048, Max: 2048}))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if plan.IsNoOp() || plan.Report().PaddingAfter != 2048 {
			t.Errorf("%s: padding asked for with a discarded edit: no-op %v, padding %d", name, plan.IsNoOp(), plan.Report().PaddingAfter)
		}
	}

	// A real edit of the padded file cuts the padding to the cap and says so.
	src := testfile.FLAC(testfile.CommentBlock("test", "TITLE=x"), testfile.FLACBlock(1, make([]byte, 4<<20)))
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "y").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if ops := plan.Report().Operations; !slices.Contains(ops, "padding 4194304 -> 1048576") {
		t.Errorf("operations = %q, want the padding cut named", ops)
	}
}
