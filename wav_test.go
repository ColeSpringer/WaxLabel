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
	sampleWAV = "testdata/sample.wav"
	notagsWAV = "testdata/notags.wav"
)

func TestWAVParse(t *testing.T) {
	doc := mustParseFile(t, sampleWAV)
	if doc.Format() != wl.FormatWAV {
		t.Errorf("format = %v, want WAV", doc.Format())
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
	// ffmpeg's ISFT software stamp is inherited-encoder noise.
	if !hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Errorf("expected an inherited-encoder warning, got %v", doc.Warnings())
	}
	if len(doc.Families()) == 0 {
		t.Error("families should be non-empty")
	}
}

func TestWAVParseNoTags(t *testing.T) {
	doc := mustParseFile(t, notagsWAV)
	if doc.Format() != wl.FormatWAV {
		t.Fatalf("format = %v, want WAV", doc.Format())
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

func TestWAVRoundTripINFO(t *testing.T) {
	src := readFixture(t, sampleWAV)
	plan, err := mustParseBytes(t, src).Edit().
		Set(tag.Title, "Edited Title").
		Set(tag.Artist, "Edited Artist").
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	got := mustParseBytes(t, out)
	if got.Fields().Title != "Edited Title" || got.Fields().Artists[0] != "Edited Artist" {
		t.Errorf("round-trip: title=%q artists=%v", got.Fields().Title, got.Fields().Artists)
	}
	// Untouched INFO values survive.
	if got.Fields().Album != "Sample Album" || got.Fields().Comment != "hello world" {
		t.Errorf("untouched fields lost: album=%q comment=%q", got.Fields().Album, got.Fields().Comment)
	}
}

func TestWAVEssenceStableAcrossTagEdit(t *testing.T) {
	for _, f := range []string{sampleWAV, notagsWAV} {
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
		if before.ExtentVersion != "wav-data-v1" {
			t.Errorf("%s: extent version = %q", f, before.ExtentVersion)
		}
		if mustParseBytes(t, out).Fields().Title != "Edited" {
			t.Errorf("%s: title not applied", f)
		}
	}
}

func TestWAVNoOpWritesNothing(t *testing.T) {
	path := copyToTemp(t, sampleWAV)
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

func TestWAVCoverRoundTrip(t *testing.T) {
	src := readFixture(t, sampleWAV)
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
	// The pre-existing INFO tags must survive the id3-chunk addition.
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

func TestWAVDifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	path := copyToTemp(t, sampleWAV)
	plan, err := mustParseFile(t, path).Edit().
		Set(tag.Title, "Differential Title").
		Set(tag.Album, "Differential Album").
		Set(tag.Composer, "Differential Composer"). // non-INFO key -> also lands in id3
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
	// title/album come from INFO; composer is only in the id3 chunk, which the
	// ffmpeg WAV demuxer also reads.
	for k, want := range map[string]string{
		"title": "Differential Title", "album": "Differential Album", "composer": "Differential Composer",
	} {
		if got := lookupCI(probe.Format.Tags, k); got != want {
			t.Errorf("ffprobe tag %q = %q, want %q (all: %v)", k, got, want, probe.Format.Tags)
		}
	}
}

func TestWAVDifferentialFFmpegDecodes(t *testing.T) {
	requireTool(t, "ffmpeg")
	path := copyToTemp(t, sampleWAV)
	plan, err := mustParseFile(t, path).Edit().
		Set(tag.Title, "Valid WAV").
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}).
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	// Decode only the audio stream: this fails loudly if our chunk framing or the
	// RIFF size is broken. (The embedded cover becomes a separate video stream.)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-map", "0:a", "-f", "null", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected our output: %v\n%s", err, out)
	}
	if got := mustParseFile(t, path).Fields().Title; got != "Valid WAV" {
		t.Errorf("title after edit = %q", got)
	}
}

// TestWAVVerifyEssenceOnWrite exercises the SaveBack path with WithVerifyEssence:
// the data chunk is copied from its source offset, so the engine's tap must hash
// the right bytes against the parsed extent.
func TestWAVVerifyEssenceOnWrite(t *testing.T) {
	path := copyToTemp(t, sampleWAV)
	plan, err := mustParseFile(t, path).Edit().Set(tag.Title, "Verified").Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatalf("SaveBack with verify: %v", err)
	}
}

// TestWAVTrailingMetadataChangeDetected proves the fingerprint covers trailing
// metadata: a WAV whose id3 chunk sits after the data chunk, externally edited in
// place (same size and mtime), is caught on save-back. The old [0,dataOff)
// fingerprint missed anything after the audio.
func TestWAVTrailingMetadataChangeDetected(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavData(400), wavID3(id3v2(3, textFrame(3, "TIT2", "Original"))))
	path := writeTempFile(t, "trail.wav", data)

	doc := mustParseFile(t, path)
	if doc.Fields().Title != "Original" {
		t.Fatalf("setup: trailing id3 title = %q", doc.Fields().Title)
	}

	// Externally rewrite the trailing id3 chunk in place (equal length so size is
	// unchanged) and restore the mtime, so only the bytes differ.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	raw = bytes.Replace(raw, []byte("Original"), []byte("Changed!"), 1) // both 8 bytes
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}

	plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); !errors.Is(err, waxerr.ErrSourceChanged) {
		t.Errorf("expected ErrSourceChanged from an external trailing-metadata edit, got %v", err)
	}
}

// TestWAVPostWriteWarningsMatchReparse confirms the document returned from a
// write recomputes its warnings instead of echoing the parse warnings: a
// duplicate-tag-block the rewrite consolidated must no longer be reported.
func TestWAVPostWriteWarningsMatchReparse(t *testing.T) {
	data := wavFile(wavFmtPCM(),
		wavInfo([2]string{"INAM", "First"}),
		wavInfo([2]string{"INAM", "Second"}),
		wavData(400))
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnDuplicateTagBlock) {
		t.Fatal("setup: expected a duplicate-tag-block warning at parse")
	}
	plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	outDoc, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	if hasWarning(outDoc, wl.WarnDuplicateTagBlock) {
		t.Error("post-write document still reports a duplicate-tag-block the rewrite resolved")
	}
	if hasWarning(mustParseBytes(t, w.b), wl.WarnDuplicateTagBlock) {
		t.Error("a fresh parse of the output should not warn about duplicates")
	}
}

// TestWAVPostWriteRetainsFamilies confirms the document returned from a write
// surfaces the same family view as a fresh parse of the output - here the
// secondary INFO container after an id3 chunk is added.
func TestWAVPostWriteRetainsFamilies(t *testing.T) {
	src := readFixture(t, sampleWAV)
	plan, err := mustParseBytes(t, src).Edit().
		Set(tag.Composer, "Promoted"). // forces an id3 chunk; INFO becomes secondary
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	outDoc, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src)))
	if err != nil {
		t.Fatal(err)
	}
	hasRIFFFamily := func(d *wl.Document) bool {
		for _, f := range d.Families() {
			if f.Family == wl.FamilyRIFF {
				return true
			}
		}
		return false
	}
	if !hasRIFFFamily(outDoc) {
		t.Error("post-write document dropped the secondary RIFF family view")
	}
	if !hasRIFFFamily(mustParseBytes(t, w.b)) {
		t.Error("a fresh parse of the output should carry the RIFF family view")
	}
}
