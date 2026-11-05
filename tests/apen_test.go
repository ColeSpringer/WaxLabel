package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
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
		AddPicture(wl.Picture{Type: wl.PicBackCover, Data: testfile.PNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	pics := mustParseBytes(t, out).Pictures()
	// The APE convention names front and back covers, so a back cover keeps its role.
	if len(pics) != 1 || pics[0].Type != wl.PicBackCover || !bytes.Equal(pics[0].Data, testfile.PNG()) {
		t.Fatalf("pictures = %+v, want one back cover with the original bytes", pics)
	}
}

// legacyAPEHeader builds the pre-3.98 layout, which inlines the geometry and derives
// the frame size from the version. ffmpeg cannot encode Monkey's Audio, so the older
// header is synthesized.
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

// pre-3.98 layout: the geometry comes from the inline fields, and the frame size from the version,
// so the sample count is (frames-1)*blocksPerFrame + finalFrameBlocks.
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

// before 3.98 the bit depth is encoded in the format flags rather than stored.
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

// descriptorAPEFile is a 3.98+ Monkey's Audio file whose APE_DESCRIPTOR declares descLen bytes
// (at least 52), zero past the documented ones, then the header (one 1000-sample frame, 44.1 kHz
// stereo 16-bit) and audio zero bytes standing in for the frames.
func descriptorAPEFile(descLen uint32, audio int) []byte {
	b := make([]byte, int(descLen)+24+audio)
	copy(b, "MAC ")
	binary.LittleEndian.PutUint16(b[4:6], 3990)
	binary.LittleEndian.PutUint32(b[8:12], descLen)
	binary.LittleEndian.PutUint32(b[12:16], 24)
	h := b[descLen : descLen+24]
	binary.LittleEndian.PutUint16(h[0:2], 2000)    // compression level
	binary.LittleEndian.PutUint32(h[4:8], 73728)   // blocks per frame
	binary.LittleEndian.PutUint32(h[8:12], 1000)   // final frame blocks
	binary.LittleEndian.PutUint32(h[12:16], 1)     // total frames
	binary.LittleEndian.PutUint16(h[16:18], 16)    // bits per sample
	binary.LittleEndian.PutUint16(h[18:20], 2)     // channels
	binary.LittleEndian.PutUint32(h[20:24], 44100) // sample rate
	return b
}

// A 3.98+ header follows the descriptor's declared length, so a 64-byte descriptor puts it at 64,
// where it is read and where a tag edit leaves it; so does one past the parser's leading read.
func TestMonkeysAudioHeaderAtTheDeclaredDescriptorLength(t *testing.T) {
	for _, descLen := range []uint32{64, 5000} {
		t.Run(fmt.Sprint(descLen), func(t *testing.T) {
			src := append(descriptorAPEFile(descLen, 512), apeTagText(map[string]string{"Title": "Before"})...)
			doc := mustParseBytes(t, src)
			want := doc.Properties().First()
			if want.TotalSamples != 1000 || want.SampleRate != 44100 || want.Channels != 2 || want.BitsPerSample != 16 {
				t.Fatalf("track = %+v, want the header at %d: 1000 samples, 44.1 kHz stereo 16-bit", want, descLen)
			}
			plan, err := doc.Edit().Set(tag.Title, "After").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			re := mustParseBytes(t, applyToBytes(t, src, plan))
			if got := re.Properties().First(); got != want {
				t.Errorf("after the tag edit the track reads %+v, want %+v", got, want)
			}
			if got := re.Fields().Title; got != "After" {
				t.Errorf("title after the edit = %q, want After", got)
			}
		})
	}
}

// A declared header that does not end before the trailing tags is refused, naming why, instead of
// being read from elsewhere: a tag edit rewrites the bytes it would reach into.
func TestMonkeysAudioHeaderPastTheAudioRefused(t *testing.T) {
	at64 := descriptorAPEFile(64, 0) // header at 64..88
	for _, c := range []struct {
		name, cause string
		data        []byte
	}{
		{"into the tag", "into the trailing tag", slices.Concat(at64[:80], apeTagText(nil))}, // footer at 80
		{"past the end", "past the end of the file", descriptorAPEFile(200, 0)[:200]},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := wl.Parse(context.Background(), wl.BytesSource(c.data))
			if !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), c.cause) {
				t.Errorf("err = %v, want ErrInvalidData saying the header runs %s", err, c.cause)
			}
		})
	}
}

// readExtent records how far into its source a parse reads.
type readExtent struct {
	wl.ReaderAtSized
	end int64
}

func (r *readExtent) ReadAt(p []byte, off int64) (int, error) {
	r.end = max(r.end, off+int64(len(p)))
	return r.ReaderAtSized.ReadAt(p, off)
}

// The marker and version are checked before the trailing tags are peeled, so a file this reader
// refuses costs no read of its tag, however large.
func TestMonkeysAudioVersionCheckedBeforeTheTag(t *testing.T) {
	audio := append(legacyAPEHeader(3320, 2000, 0, 2, 44100, 1, 100), make([]byte, 8<<10)...)
	tagged := append(slices.Clip(audio), apeTagText(map[string]string{"Comment": strings.Repeat("x", 1<<20)})...)
	src := &readExtent{ReaderAtSized: wl.BytesSource(tagged)}
	if _, err := wl.Parse(context.Background(), src); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
	if src.end > int64(len(audio)) {
		t.Errorf("the refused parse read up to offset %d, into the tag at %d", src.end, len(audio))
	}
}

func TestMonkeysAudioTruncatedHeaderRejected(t *testing.T) {
	_, err := wl.Parse(context.Background(), wl.BytesSource([]byte("MAC \x96\x0f\x00\x00")))
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want ErrInvalidData", err)
	}
}

// independent read-back proof. ffmpeg cannot encode Monkey's Audio, but its decoder reads the
// container and its APEv2 trailer, so ffprobe still serves as the oracle.
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
