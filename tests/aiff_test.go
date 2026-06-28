package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

const (
	sampleAIFF = "../testdata/sample.aiff"
	notagsAIFF = "../testdata/notags.aiff"
	sampleAIFC = "../testdata/sample.aifc"
)

func TestAIFFParse(t *testing.T) {
	doc := mustParseFile(t, sampleAIFF)
	if doc.Format() != wl.FormatAIFF {
		t.Errorf("format = %v, want AIFF", doc.Format())
	}
	f := doc.Fields()
	if f.Title != "Sample Title" {
		t.Errorf("title = %q", f.Title)
	}
	if len(f.Artists) != 1 || f.Artists[0] != "Sample Artist" {
		t.Errorf("artists = %v", f.Artists)
	}
	if f.Album != "Sample Album" {
		t.Errorf("album = %q", f.Album)
	}
	if f.RecordingDate != "2021" {
		t.Errorf("date = %q, want 2021", f.RecordingDate)
	}
	if len(f.Genres) != 1 || f.Genres[0] != "Rock" {
		t.Errorf("genre = %v", f.Genres)
	}
	if f.TrackNumber != 3 {
		t.Errorf("track = %d, want 3", f.TrackNumber)
	}
	if f.Comment != "hello world" {
		t.Errorf("comment = %q", f.Comment)
	}
	tr := doc.Properties().First()
	if tr.SampleRate != 44100 || tr.Channels != 2 || tr.BitsPerSample != 16 {
		t.Errorf("track geometry = %+v", tr)
	}
	if tr.Duration <= 0 {
		t.Errorf("duration = %v, want > 0", tr.Duration)
	}
	if tr.Codec != "PCM" {
		t.Errorf("codec = %q, want PCM", tr.Codec)
	}
	// ffmpeg writes its "Lavf..." software stamp into the ID3 TSSE frame.
	if !hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Errorf("expected an inherited-encoder warning, got %v", doc.Warnings())
	}
	if len(doc.Families()) == 0 {
		t.Error("families should be non-empty")
	}
}

func TestAIFFParseNoTags(t *testing.T) {
	doc := mustParseFile(t, notagsAIFF)
	if doc.Format() != wl.FormatAIFF {
		t.Fatalf("format = %v, want AIFF", doc.Format())
	}
	if doc.Tags().Len() != 0 {
		t.Errorf("expected no tags, got %d", doc.Tags().Len())
	}
	if doc.Properties().First().SampleRate != 44100 {
		t.Errorf("sample rate = %d, want 44100", doc.Properties().First().SampleRate)
	}
	if doc.Properties().Duration() <= 0 {
		t.Error("duration should be > 0 even without tags")
	}
	if hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Error("a tagless file should have no encoder-noise warning")
	}
}

// TestAIFFParseAIFC covers the AIFF-C variant: the AIFC form type, the FVER
// chunk, native text tags, and an 80-bit COMM rate decoded from a 24-byte COMM
// with a "sowt" compression type.
func TestAIFFParseAIFC(t *testing.T) {
	doc := mustParseFile(t, sampleAIFC)
	if doc.Format() != wl.FormatAIFF {
		t.Fatalf("format = %v, want AIFF", doc.Format())
	}
	if doc.Properties().Container != "AIFC" {
		t.Errorf("container = %q, want AIFC", doc.Properties().Container)
	}
	if doc.Fields().Title != "AIFC Title" {
		t.Errorf("title = %q", doc.Fields().Title)
	}
	if doc.Fields().Comment != "aifc comment" {
		t.Errorf("comment = %q", doc.Fields().Comment)
	}
	tr := doc.Properties().First()
	if tr.SampleRate != 44100 {
		t.Errorf("AIFF-C 80-bit rate decoded to %d, want 44100", tr.SampleRate)
	}
	if tr.Codec != "PCM (little-endian)" {
		t.Errorf("codec = %q, want PCM (little-endian) for sowt", tr.Codec)
	}
	// An edit must preserve the AIFC form type and the FVER chunk.
	src := readFixture(t, sampleAIFC)
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Edited AIFC").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if string(out[8:12]) != "AIFC" {
		t.Errorf("form type after edit = %q, want AIFC", out[8:12])
	}
	if !bytes.Contains(out, []byte("FVER")) {
		t.Error("FVER chunk was not preserved across an edit")
	}
	if got := mustParseBytes(t, out); got.Fields().Title != "Edited AIFC" {
		t.Errorf("edited AIFC title = %q", got.Fields().Title)
	}
}

func TestAIFFRoundTripNativeAndID3(t *testing.T) {
	src := readFixture(t, sampleAIFF)
	plan, err := mustParseBytes(t, src).Edit().
		Set(tag.Title, "Edited Title").
		Set(tag.Composer, "Edited Composer"). // non-native key -> lands in ID3
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	got := mustParseBytes(t, out)
	if got.Fields().Title != "Edited Title" {
		t.Errorf("title = %q", got.Fields().Title)
	}
	if len(got.Fields().Composers) != 1 || got.Fields().Composers[0] != "Edited Composer" {
		t.Errorf("composer = %v", got.Fields().Composers)
	}
	// Untouched fields survive (artist/album came from the ID3 chunk).
	if got.Fields().Album != "Sample Album" || got.Fields().Artists[0] != "Sample Artist" {
		t.Errorf("untouched fields lost: album=%q artists=%v", got.Fields().Album, got.Fields().Artists)
	}
}

func TestAIFFEssenceStableAcrossTagEdit(t *testing.T) {
	for _, f := range []string{sampleAIFF, notagsAIFF, sampleAIFC} {
		src := readFixture(t, f)
		before := essenceOf(t, src)
		plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Edited").Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		out := applyToBytes(t, src, plan)
		if after := essenceOf(t, out); !before.Equal(after) {
			t.Errorf("%s: audio essence changed across a tag edit", f)
		}
		if before.ExtentVersion != "aiff-ssnd-v2" {
			t.Errorf("%s: extent version = %q", f, before.ExtentVersion)
		}
		if mustParseBytes(t, out).Fields().Title != "Edited" {
			t.Errorf("%s: title not applied", f)
		}
	}
}

func TestAIFFNoOpWritesNothing(t *testing.T) {
	path := copyToTemp(t, sampleAIFF)
	before, _ := os.ReadFile(path)
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, doc.Fields().Title).Prepare() // same value
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Fatal("re-setting the same title should be a no-op")
	}
	_, res, err := plan.Execute(context.Background(), wl.SaveBack())
	if err != nil {
		t.Fatal(err)
	}
	if res.Committed {
		t.Error("a no-op SaveBack must not write")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("no-op SaveBack changed the file")
	}
}

func TestAIFFCoverRoundTrip(t *testing.T) {
	src := readFixture(t, sampleAIFF)
	before := essenceOf(t, src)

	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if after := essenceOf(t, out); !before.Equal(after) {
		t.Error("essence changed when adding a cover")
	}
	got := mustParseBytes(t, out)
	if len(got.Pictures()) != 1 || got.Pictures()[0].Type != wl.PicFrontCover {
		t.Fatalf("pictures = %+v", got.Pictures())
	}
	if got.Pictures()[0].MIME != "image/png" {
		t.Errorf("MIME = %q, want image/png", got.Pictures()[0].MIME)
	}
	// The pre-existing tags must survive adding a cover.
	if got.Fields().Title != "Sample Title" {
		t.Errorf("title lost when adding cover: %q", got.Fields().Title)
	}
	// Remove the cover again.
	plan2, _ := got.Edit().ClearPictures().Prepare()
	if n := len(mustParseBytes(t, applyToBytes(t, out, plan2)).Pictures()); n != 0 {
		t.Errorf("ClearPictures left %d pictures", n)
	}
}

// Write-side differential: ffmpeg/ffprobe must read what we wrote and accept
// our audio. These skip cleanly when the tools are absent.

func TestAIFFDifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	path := copyToTemp(t, notagsAIFF)
	plan, err := mustParseFile(t, path).Edit().
		Set(tag.Title, "Differential Title").       // native NAME chunk
		Set(tag.Comment, "Differential Comment").   // native ANNO chunk
		Set(tag.Composer, "Differential Composer"). // non-native key -> ID3 chunk
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_entries", "format_tags", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var probe struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("parse ffprobe json: %v\n%s", err, out)
	}
	// title/comment come from the native chunks; composer is only in the ID3 chunk,
	// which the ffmpeg AIFF demuxer also reads.
	for k, want := range map[string]string{
		"title": "Differential Title", "comment": "Differential Comment", "composer": "Differential Composer",
	} {
		if got := lookupCI(probe.Format.Tags, k); got != want {
			t.Errorf("ffprobe tag %q = %q, want %q (all: %v)", k, got, want, probe.Format.Tags)
		}
	}
}

func TestAIFFDifferentialFFmpegDecodes(t *testing.T) {
	requireTool(t, "ffmpeg")
	path := copyToTemp(t, sampleAIFF)
	plan, err := mustParseFile(t, path).Edit().
		Set(tag.Title, "Valid AIFF").
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}).
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	// Decode only the audio stream: this fails loudly if our chunk framing or the
	// FORM size is broken.
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-map", "0:a", "-f", "null", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected our output: %v\n%s", err, out)
	}
	if got := mustParseFile(t, path).Fields().Title; got != "Valid AIFF" {
		t.Errorf("title after edit = %q", got)
	}
}

// TestAIFFVerifyEssenceOnWrite exercises SaveBack with WithVerifyEssence: the
// SSND chunk is copied from its source offset, so the engine's tap must hash the
// right sample-frame bytes against the parsed extent.
func TestAIFFVerifyEssenceOnWrite(t *testing.T) {
	path := copyToTemp(t, sampleAIFF)
	plan, err := mustParseFile(t, path).Edit().Set(tag.Title, "Verified").Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatalf("SaveBack with verify: %v", err)
	}
}

// TestAIFFSSNDOffsetExcludedFromEssence verifies that SSND "offset" alignment bytes
// precede the first sample frame and are not hashed as audio. Files with identical
// sample frames but different offsets should hash the same.
func TestAIFFSSNDOffsetExcludedFromEssence(t *testing.T) {
	samples := bytes.Repeat([]byte{0xA7}, 400)
	align := []byte{0xFF, 0xFF, 0xFF, 0xFF} // distinct so a leak would change the digest

	off0 := aiffFile("AIFF", stdCOMM(), aiffSSNDOffset(0, nil, samples))
	off4 := aiffFile("AIFF", stdCOMM(), aiffSSNDOffset(4, align, samples))

	if d0, d4 := essenceOf(t, off0), essenceOf(t, off4); !d0.Equal(d4) {
		t.Errorf("offset 0 vs 4 essence differ: alignment bytes leaked into the digest\n off0=%x\n off4=%x", d0.Sum, d4.Sum)
	}

	// A corrupt header whose declared offset overruns the body must collapse the
	// sample range to empty (the no-audio path), not leave AudioStart > AudioEnd.
	huge := aiffFile("AIFF", stdCOMM(), aiffSSNDOffset(1<<20, nil, samples))
	doc := mustParseBytes(t, huge) // must not panic
	_, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(huge)))
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("oversized SSND offset: hash err = %v, want ErrInvalidData (no-audio path)", err)
	}
}

// TestAIFFSSNDOffsetResultMatchesReparse checks that a metadata edit on an AIFF with
// a non-zero SSND offset returns a result document whose essence range matches a fresh
// parse of the written output.
func TestAIFFSSNDOffsetResultMatchesReparse(t *testing.T) {
	samples := bytes.Repeat([]byte{0xA7}, 400)
	align := []byte{0xFF, 0xFF, 0xFF, 0xFF} // distinct: a leak would change the digest
	data := aiffFile("AIFF", aiffText("NAME", "Old"), stdCOMM(), aiffSSNDOffset(4, align, samples))

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	resDoc, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	resDigest, err := resDoc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(w.b)))
	if err != nil {
		t.Fatalf("hash result essence: %v", err)
	}
	if reparse := essenceOf(t, w.b); !resDigest.Equal(reparse) {
		t.Errorf("result-view essence != reparse essence (SSND offset bytes leaked into the result view)\n result=%x\nreparse=%x", resDigest.Sum, reparse.Sum)
	}

	// A second edit on the returned document recomputes layout without reparsing, so
	// ssndAlign must be carried forward.
	plan2, err := resDoc.Edit().Set(tag.Artist, "Chained").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w2 writerTo
	res2, _, err := plan2.Execute(context.Background(), wl.WriteTo(&w2, wl.BytesSource(w.b)))
	if err != nil {
		t.Fatal(err)
	}
	res2Digest, err := res2.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(w2.b)))
	if err != nil {
		t.Fatalf("hash chained result essence: %v", err)
	}
	if reparse := essenceOf(t, w2.b); !res2Digest.Equal(reparse) {
		t.Errorf("chained result-view essence != reparse (result doc dropped ssndAlign)\n result=%x\nreparse=%x", res2Digest.Sum, reparse.Sum)
	}
}

func TestAIFFRejectsNonAIFF(t *testing.T) {
	// A FORM container that is not AIFF/AIFC carries no AIFF signature, so content-only
	// detection rejects it as unsupported rather than routing it by extension.
	data := append([]byte("FORM\x00\x00\x00\x04"), []byte("8SVX")...)
	path := writeTempFile(t, "x.aiff", data)
	_, err := wl.ParseFile(context.Background(), path)
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Fatalf("non-AIFF FORM error = %v, want ErrUnsupportedFormat", err)
	}
}
