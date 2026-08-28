package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
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
	sampleAPE = "../testdata/sample.ape" // Monkey's Audio 3.99, real APEv2 tag
	notagsAPE = "../testdata/notags.ape"
)

func TestMonkeysAudioParse(t *testing.T) {
	doc := mustParseFile(t, sampleAPE)
	if doc.Format() != wl.FormatMonkeysAudio {
		t.Fatalf("format = %v, want Monkey's Audio", doc.Format())
	}
	tr := doc.Properties().First()
	if tr.Codec != "Monkey's Audio" || tr.SampleRate != 44100 || tr.Channels != 2 || tr.BitsPerSample != 16 {
		t.Errorf("track = %+v, want Monkey's Audio 44100 Hz stereo 16-bit", tr)
	}
	if tr.Duration <= 0 || tr.TotalSamples == 0 {
		t.Errorf("duration = %v, samples = %d, want both non-zero", tr.Duration, tr.TotalSamples)
	}
	if got := doc.Fields().Title; got != "Tagged" {
		t.Errorf("title = %q", got)
	}
	if got := doc.Fields().RecordingDate; got != "2026" {
		t.Errorf("RecordingDate = %q, want 2026 (the APE \"Year\" item)", got)
	}
}

// TestMonkeysAudioRoundTripPreservesEssence: editing tags must not disturb the
// compressed frames, and the values must read back.
func TestMonkeysAudioRoundTripPreservesEssence(t *testing.T) {
	src := readFixture(t, sampleAPE)
	before := essenceOf(t, src)

	plan, err := mustParseBytes(t, src).Edit().
		Set(tag.Title, "Changed Title").
		Set(tag.Artist, "First", "Second").
		Clear(tag.Album).
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
	if a := got.Fields().Artists; !slices.Equal(a, []string{"First", "Second"}) {
		t.Errorf("artists = %v", a)
	}
	if _, ok := got.Get(tag.Album); ok {
		t.Error("ALBUM should have been cleared")
	}
}

// TestMonkeysAudioTagCreated: a file with no APEv2 tag gains one on the first edit.
func TestMonkeysAudioTagCreated(t *testing.T) {
	src := readFixture(t, notagsAPE)
	doc := mustParseBytes(t, src)
	if doc.Tags().Len() != 0 {
		t.Fatalf("setup: expected an untagged fixture, got %d keys", doc.Tags().Len())
	}
	plan, err := doc.Edit().Set(tag.Album, "Fresh").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	re := mustParseBytes(t, out)
	if re.Fields().Album != "Fresh" {
		t.Errorf("album = %q, want Fresh", re.Fields().Album)
	}
	if before := essenceOf(t, src); !before.Equal(essenceOf(t, out)) {
		t.Error("creating a tag changed the audio essence")
	}
}

func TestMonkeysAudioNoOpWritesNothing(t *testing.T) {
	path := copyToTemp(t, sampleAPE)
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

func TestMonkeysAudioCoverRoundTrip(t *testing.T) {
	src := readFixture(t, sampleAPE)
	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicBackCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	pics := mustParseBytes(t, out).Pictures()
	// The APE convention names front and back covers, so a back cover keeps its role.
	if len(pics) != 1 || pics[0].Type != wl.PicBackCover || !bytes.Equal(pics[0].Data, tinyPNG()) {
		t.Fatalf("pictures = %+v, want one back cover with the original bytes", pics)
	}
}

// legacyAPEHeader builds the pre-3.98 layout, which inlines the geometry and leaves
// the frame size to be derived from the version. ffmpeg cannot encode Monkey's Audio
// at all, so the older header shape has to be synthesized to be covered.
func legacyAPEHeader(version uint16, compressionLevel, formatFlags, channels uint16, rate, totalFrames, finalBlocks uint32) []byte {
	b := make([]byte, 32)
	copy(b[0:4], "MAC ")
	binary.LittleEndian.PutUint16(b[4:6], version)
	binary.LittleEndian.PutUint16(b[6:8], compressionLevel)
	binary.LittleEndian.PutUint16(b[8:10], formatFlags)
	binary.LittleEndian.PutUint16(b[10:12], channels)
	binary.LittleEndian.PutUint32(b[12:16], rate)
	binary.LittleEndian.PutUint32(b[24:28], totalFrames)
	binary.LittleEndian.PutUint32(b[28:32], finalBlocks)
	return b
}

// TestMonkeysAudioLegacyHeader covers the pre-3.98 layout: the geometry comes from
// the inline fields, and the frame size from the version, so the sample count is
// (frames-1)*blocksPerFrame + finalFrameBlocks.
func TestMonkeysAudioLegacyHeader(t *testing.T) {
	for _, c := range []struct {
		name    string
		version uint16
		level   uint16
		frames  uint32
		final   uint32
		samples uint64
	}{
		{"v3.97 largest frame", 3970, 2000, 3, 1000, 2*73728*4 + 1000},
		{"v3.95 largest frame", 3950, 2000, 2, 500, 73728*4 + 500},
		{"v3.90 larger frame", 3900, 2000, 2, 500, 73728 + 500},
		{"v3.89 smallest frame", 3890, 2000, 2, 500, 9216 + 500},
		{"v3.89 extra high uses the larger frame", 3890, 4000, 2, 500, 73728 + 500},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := append(legacyAPEHeader(c.version, c.level, 0, 2, 44100, c.frames, c.final), make([]byte, 512)...)
			doc := mustParseBytes(t, data)
			tr := doc.Properties().First()
			if tr.TotalSamples != c.samples {
				t.Errorf("TotalSamples = %d, want %d", tr.TotalSamples, c.samples)
			}
			if tr.SampleRate != 44100 || tr.Channels != 2 || tr.BitsPerSample != 16 {
				t.Errorf("track = %+v, want 44100 Hz stereo 16-bit from the inline fields", tr)
			}
		})
	}
}

// TestMonkeysAudioLegacyBitDepthFlags: before 3.98 the bit depth is encoded in the
// format flags rather than stored.
func TestMonkeysAudioLegacyBitDepthFlags(t *testing.T) {
	for _, c := range []struct {
		flags uint16
		depth int
	}{{0, 16}, {1, 8}, {8, 24}} {
		data := append(legacyAPEHeader(3970, 2000, c.flags, 2, 44100, 1, 100), make([]byte, 256)...)
		if got := mustParseBytes(t, data).Properties().First().BitsPerSample; got != c.depth {
			t.Errorf("format flags %#x -> %d-bit, want %d", c.flags, got, c.depth)
		}
	}
}

// TestMonkeysAudioAncientVersionRefused: below 3.8 the header is a different layout
// and is refused by name rather than misread.
func TestMonkeysAudioAncientVersionRefused(t *testing.T) {
	data := append(legacyAPEHeader(3320, 2000, 0, 2, 44100, 1, 100), make([]byte, 64)...)
	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("err = %v, want ErrUnsupportedFormat", err)
	}
}

func TestMonkeysAudioTruncatedHeaderRejected(t *testing.T) {
	_, err := wl.Parse(context.Background(), wl.BytesSource([]byte("MAC \x96\x0f\x00\x00")))
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want ErrInvalidData", err)
	}
}

// TestMonkeysAudioDifferentialFFprobeReadsOurTags is the independent read-back
// proof. ffmpeg cannot encode Monkey's Audio, but its decoder reads the container
// and its APEv2 trailer, so ffprobe still serves as the oracle.
func TestMonkeysAudioDifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	path := copyToTemp(t, sampleAPE)
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

func TestMonkeysAudioDifferentialFFmpegDecodes(t *testing.T) {
	requireTool(t, "ffmpeg")
	path := copyToTemp(t, sampleAPE)
	plan, err := mustParseFile(t, path).Edit().
		Set(tag.Title, "Valid APE").
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
	if got := mustParseFile(t, path).Fields().Title; got != "Valid APE" {
		t.Errorf("title after edit = %q", got)
	}
}
