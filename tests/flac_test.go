package waxlabel_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"

	"errors"
)

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

// The N-getter + 1-mutator deep-copy isolation test: accessor results must not
// alias the Document's internal state.
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

	png := tinyPNG()
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
	if before.ExtentVersion != "flac-frames-v1" {
		t.Errorf("ExtentVersion = %q, want flac-frames-v1", before.ExtentVersion)
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

// tinyPNG returns a 1x1 RGBA PNG header (no IDAT; enough for header sniffing
// and round-trip byte preservation).
func tinyPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
	}
}

// tinyJPEG returns a minimal complete JPEG header: SOI followed by a 3x5 SOF0. The
// image sniffer requires a readable Start-Of-Frame, so a bare FF D8 FF magic does not
// count as a recognized image.
func tinyJPEG() []byte {
	return []byte{
		0xFF, 0xD8, // SOI
		0xFF, 0xC0, 0x00, 0x11, 0x08, // SOF0, length 17, precision 8
		0x00, 0x05, // height 5
		0x00, 0x03, // width 3
		0x03, // components
		0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}
}

// TestFLACTruncationNotFlagged pins the deliberate non-detection: FLAC carries no
// declared encoded-essence size, so a mid-stream cut is undetectable without
// decoding and must never be flagged truncated. A valid FLAC - including a minimal,
// effectively zero-bitrate one - must stay clean; a per-byte bitrate floor would
// false-flag silent or low-bitrate lossless audio, which internal/flac/parse.go
// explicitly declines to flag.
func TestFLACTruncationNotFlagged(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"synthetic minimal (bitrate ~0)", synthFLAC()},
		{"real fixture", readFixture(t, sampleFLAC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if doc := mustParseBytes(t, tc.data); hasWarning(doc, wl.WarnTruncatedAudio) {
				t.Errorf("FLAC must never be flagged truncated; got %v", doc.Warnings())
			}
		})
	}
}

// TestFLACPaddingClampWarns verifies that requested padding above FLAC's ~16 MiB per-block
// limit is clamped to it, and the write must surface a padding-clamped warning so the
// smaller-than-asked padding is not silent. A sane padding does not warn.
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
