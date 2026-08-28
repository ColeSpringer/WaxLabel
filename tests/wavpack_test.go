package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

const (
	sampleWV = "../testdata/sample.wv"
	notagsWV = "../testdata/notags.wv"
)

func TestWavPackParse(t *testing.T) {
	doc := mustParseFile(t, sampleWV)
	if doc.Format() != wl.FormatWavPack {
		t.Fatalf("format = %v, want WavPack", doc.Format())
	}
	tr := doc.Properties().First()
	if tr.Codec != "WavPack" || tr.SampleRate != 44100 || tr.Channels != 2 || tr.BitsPerSample != 16 {
		t.Errorf("track = %+v, want WavPack 44100 Hz stereo 16-bit", tr)
	}
	if tr.Duration <= 0 || tr.TotalSamples == 0 {
		t.Errorf("duration = %v, samples = %d, want both non-zero", tr.Duration, tr.TotalSamples)
	}
	if got := doc.Fields().Title; got != "Sample Title" {
		t.Errorf("title = %q", got)
	}
	// APE "DATE" must canonicalize like every other codec's, or one file would read
	// differently depending on which container it sits in.
	if got := doc.Fields().RecordingDate; got != "2021" {
		t.Errorf("RecordingDate = %q, want 2021", got)
	}
	if len(doc.Families()) == 0 {
		t.Error("Families() should be non-empty")
	}
}

// TestWavPackRoundTripPreservesEssence is the core invariant: editing tags must not
// disturb the WavPack blocks, and the values must read back.
func TestWavPackRoundTripPreservesEssence(t *testing.T) {
	src := readFixture(t, sampleWV)
	before := essenceOf(t, src)

	plan, err := mustParseBytes(t, src).Edit().
		Set(tag.Title, "Changed Title").
		Set(tag.Artist, "First", "Second").
		Set(tag.Key("CUSTOM_X"), "cval").
		Clear(tag.Genre).
		Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	out := applyToBytes(t, src, plan)

	if after := essenceOf(t, out); !before.Equal(after) {
		t.Error("audio essence changed across a tag edit")
	}
	got := mustParseBytes(t, out)
	if got.Fields().Title != "Changed Title" {
		t.Errorf("title = %q", got.Fields().Title)
	}
	// APE stores a multi-valued key as one NUL-separated item, so this also pins the
	// multi-value encode and decode.
	if a := got.Fields().Artists; !slices.Equal(a, []string{"First", "Second"}) {
		t.Errorf("artists = %v", a)
	}
	if v, ok := got.Get(tag.Key("CUSTOM_X")); !ok || v[0] != "cval" {
		t.Errorf("custom key not round-tripped: %v", v)
	}
	if _, ok := got.Get(tag.Genre); ok {
		t.Error("GENRE should have been cleared")
	}
}

func TestWavPackNoOpWritesNothing(t *testing.T) {
	path := copyToTemp(t, sampleWV)
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, doc.Fields().Title).Prepare()
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
}

// TestWavPackTagCreatedOnBareFile: a file whose APEv2 tag was dropped (or never
// written) gains one, and clearing every key drops it again rather than leaving an
// empty container behind.
func TestWavPackTagCreatedAndDropped(t *testing.T) {
	src := readFixture(t, notagsWV)
	doc := mustParseBytes(t, src)

	plan, err := doc.Edit().Set(tag.Album, "Fresh").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if got := mustParseBytes(t, out).Fields().Album; got != "Fresh" {
		t.Fatalf("album = %q, want Fresh", got)
	}

	re := mustParseBytes(t, out)
	ed := re.Edit()
	for _, k := range re.Tags().Keys() {
		ed = ed.Clear(k)
	}
	plan2, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, out, plan2)
	if bytes.Contains(out2, []byte("APETAGEX")) {
		t.Error("clearing every key must drop the APEv2 tag, not leave an empty one")
	}
	if n := mustParseBytes(t, out2).Tags().Len(); n != 0 {
		t.Errorf("tags after clearing everything = %d, want 0", n)
	}
}

// TestWavPackCoverRoundTrip exercises the APE Cover Art convention through the
// public picture API.
func TestWavPackCoverRoundTrip(t *testing.T) {
	src := readFixture(t, sampleWV)
	before := essenceOf(t, src)

	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if after := essenceOf(t, out); !before.Equal(after) {
		t.Error("essence changed adding a cover")
	}
	got := mustParseBytes(t, out)
	if pics := got.Pictures(); len(pics) != 1 || pics[0].Type != wl.PicFrontCover || !bytes.Equal(pics[0].Data, tinyPNG()) {
		t.Fatalf("pictures = %+v, want one front cover with the original bytes", got.Pictures())
	}

	plan2, _ := got.Edit().ClearPictures().Prepare()
	out2 := applyToBytes(t, out, plan2)
	if pics := mustParseBytes(t, out2).Pictures(); len(pics) != 0 {
		t.Errorf("ClearPictures left %d pictures", len(pics))
	}
	if after := essenceOf(t, out2); !before.Equal(after) {
		t.Error("essence changed removing the cover")
	}
}

// TestWavPackTrailingID3v1Preserved: a WavPack file can carry a legacy ID3v1 after
// its APEv2 tag. It stays legacy - surfaced, preserved, never authoritative - and a
// strip removes it.
func TestWavPackTrailingID3v1Preserved(t *testing.T) {
	v1 := make([]byte, 128)
	copy(v1[0:3], "TAG")
	copy(v1[3:33], "Legacy Only Title")
	src := append(slices.Clone(readFixture(t, sampleWV)), v1...)

	doc := mustParseBytes(t, src)
	if !hasWarning(doc, wl.WarnTrailingID3v1) {
		t.Error("expected the trailing-ID3v1 warning")
	}
	if got := doc.Fields().Title; got != "Sample Title" {
		t.Errorf("title = %q, want the APE value to stay authoritative", got)
	}
	plan, err := doc.Edit().Set(tag.Album, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.HasSuffix(out, v1) {
		t.Error("the trailing ID3v1 was not preserved through an edit")
	}

	plan2, err := mustParseBytes(t, out).Edit().Set(tag.Album, "Stripped").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, out, plan2)
	if bytes.HasSuffix(out2, v1) {
		t.Error("--legacy strip should have dropped the trailing ID3v1")
	}
	if got := mustParseBytes(t, out2).Fields().Album; got != "Stripped" {
		t.Errorf("album after strip = %q", got)
	}
}

// TestWavPackTruncatedHeaderRejected: a wvpk marker with no usable block header is
// corrupt input, not a format WaxLabel cannot read.
func TestWavPackTruncatedHeaderRejected(t *testing.T) {
	_, err := wl.Parse(context.Background(), wl.BytesSource([]byte("wvpk\x10\x00\x00\x00")))
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want ErrInvalidData", err)
	}
}

// TestWavPackUnsupportedVersionRefused: a stream version outside the documented
// range is refused by name rather than misread.
func TestWavPackUnsupportedVersionRefused(t *testing.T) {
	data := slices.Clone(readFixture(t, sampleWV))
	data[8], data[9] = 0xFF, 0x0F // version 0x0FFF, above the supported range
	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("err = %v, want ErrUnsupportedFormat", err)
	}
}

// TestWavPackDifferentialFFprobeReadsOurTags is the independent read-back proof:
// ffprobe is a different codebase from a different author, so unlike a self
// round-trip it cannot share a misreading of the APE layout with us.
func TestWavPackDifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	path := copyToTemp(t, sampleWV)
	plan, err := mustParseFile(t, path).Edit().
		Set(tag.Title, "Differential Title").
		Set(tag.Key("CUSTOM_TAG"), "custom-value").
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
	for k, want := range map[string]string{"title": "Differential Title", "CUSTOM_TAG": "custom-value"} {
		if got := lookupCI(probe.Format.Tags, k); got != want {
			t.Errorf("ffprobe tag %q = %q, want %q (all: %v)", k, got, want, probe.Format.Tags)
		}
	}
}

// TestWavPackDifferentialFFmpegDecodes: our rewritten tail must not disturb the
// blocks, which fails loudly if the audio extent moved.
func TestWavPackDifferentialFFmpegDecodes(t *testing.T) {
	requireTool(t, "ffmpeg")
	path := copyToTemp(t, sampleWV)
	plan, err := mustParseFile(t, path).Edit().
		Set(tag.Title, "Valid WavPack").
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: pattern(70000)}).
		Prepare(wl.WithUnrecognizedPictures())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path, "-map", "0:a", "-f", "null", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected our output: %v\n%s", err, out)
	}
	if got := mustParseFile(t, path).Fields().Title; got != "Valid WavPack" {
		t.Errorf("title after edit = %q", got)
	}
}
