package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
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

// core invariant: editing tags must not disturb the WavPack blocks, and the values must read back.
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

func TestWavPackCoverRoundTrip(t *testing.T) {
	src := readFixture(t, sampleWV)
	before := essenceOf(t, src)

	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if after := essenceOf(t, out); !before.Equal(after) {
		t.Error("essence changed adding a cover")
	}
	got := mustParseBytes(t, out)
	if pics := got.Pictures(); len(pics) != 1 || pics[0].Type != wl.PicFrontCover || !bytes.Equal(pics[0].Data, testfile.PNG()) {
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

// WavPack file can carry a legacy ID3v1 after its APEv2 tag. It stays legacy; surfaced, preserved,
// never authoritative, and a strip removes it.
func TestWavPackTrailingID3v1Preserved(t *testing.T) {
	v1 := testfile.ID3v1("Legacy Only Title", "", "", "", "", 0)
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

// The WavPack audio ends where the stream says: after the sample group that reaches the total a
// first block at index 0 states or, otherwise, before a block whose index restarts or goes backward.
// Blocks past that are junk, even a valid stream of their own. A cut starts past index 0, where a
// stated total places no end: ffmpeg's -ss back-patches the cut's own count, a byte carve keeps the
// original's. The blocks of a multichannel group share an index, and a block with no samples carries
// the stream's metadata.
func TestWavPackStreamEndsWhereItSays(t *testing.T) {
	const noTotal = 0xFFFFFFFF
	blk := func(flags, total, index, samples uint32) []byte {
		return wvGroupBlock(1|wvRate44k|flags, total, index, samples, make([]byte, 8))
	}
	group := uint32(wvInitial | wvFinal)
	sample, notags := readFixture(t, sampleWV), readFixture(t, notagsWV)
	for _, c := range []struct {
		name   string
		stream []byte
		junk   int // bytes past the stream's end
	}{
		{"total reached, another stream after", slices.Concat(sample[:apeTagStart(sample)], notags[:apeTagStart(notags)]), apeTagStart(notags)},
		{"total reached, the index going on", slices.Concat(blk(group, 4410, 0, 4410), blk(group, 4410, 4410, 4410)), 40},
		{"group block with another index", slices.Concat(blk(wvInitial, noTotal, 0, 4410), blk(wvMono|wvFinal, noTotal, 4410, 4410)), 40},
		{"no total, index restarts", slices.Concat(blk(group, noTotal, 0, 4410), blk(group, noTotal, 4410, 4410), blk(group, noTotal, 0, 4410)), 40},
		{"no total, index goes backward", slices.Concat(blk(group, noTotal, 0, 4410), blk(group, noTotal, 8820, 4410), blk(group, noTotal, 4410, 4410)), 40},
		{"multichannel group sharing its index", slices.Concat(blk(wvInitial, 8820, 0, 4410), blk(wvMono, 8820, 0, 4410),
			blk(wvMono|wvFinal, 8820, 0, 4410), blk(group, 8820, 4410, 4410)), 0},
		{"metadata block after the last group", slices.Concat(blk(group, 4410, 0, 4410), blk(0, 4410, 0, 0)), 0},
		{"cut stating its own count", slices.Concat(blk(group, 13230, 441000, 4410), blk(group, 13230, 445410, 4410),
			blk(group, 13230, 449820, 4410)), 0},
		{"multichannel cut stating its own count", slices.Concat(blk(wvInitial, 8820, 441000, 4410), blk(wvFinal, 8820, 441000, 4410),
			blk(wvInitial, 8820, 445410, 4410), blk(wvFinal, 8820, 445410, 4410)), 0},
		{"total of 0, as ffmpeg leaves it on a pipe", slices.Concat(blk(group, 0, 0, 4410), blk(group, 0, 4410, 4410)), 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := slices.Concat(c.stream, headedAPETag(t, "Title", "T"))
			doc := mustParseBytes(t, src)
			w, ok := docWarning(doc, wl.WarnTrailingBytes)
			if want := fmt.Sprintf("%d byte(s) after the WavPack stream", c.junk); c.junk > 0 && (!ok || !strings.HasPrefix(w.Message, want)) {
				t.Errorf("junk warning = %q (present %v), want it to start %q", w.Message, ok, want)
			}
			if c.junk == 0 && ok {
				t.Errorf("the whole stream is audio, but %q", w.Message)
			}
			if !essenceOf(t, src).Equal(essenceOf(t, c.stream[:len(c.stream)-c.junk])) {
				t.Error("the digest does not cover the stream up to its end")
			}
		})
	}
}

// A WavPack stream whose first block states no total (the reference's unknown-length escape, or the 0
// ffmpeg leaves when it writes to a pipe) takes its length from its blocks: it reads its duration and
// takes a tag edit. When the walk to its end does not finish, here past the element limit, the length
// is unknown but the stream is still audio.
func TestWavPackWithoutAStatedTotal(t *testing.T) {
	for _, total := range []uint32{0, 0xFFFFFFFF} {
		t.Run(fmt.Sprintf("%#x", total), func(t *testing.T) {
			blk := func(index uint32) []byte {
				return wvGroupBlock(1|wvRate44k|wvInitial|wvFinal, total, index, 4410, make([]byte, 8))
			}
			src := slices.Concat(blk(0), blk(4410), blk(8820))
			doc := mustParseBytes(t, src)
			if tr := doc.Properties().First(); tr.TotalSamples != 13230 || tr.Duration != 300*time.Millisecond {
				t.Errorf("track = %+v, want the 13230 samples, 300 ms, the blocks hold", tr)
			}
			plan, err := doc.Edit().Set(tag.Title, "Tagged").Prepare()
			if err != nil {
				t.Fatalf("a tag edit was refused: %v", err)
			}
			re := mustParseBytes(t, applyToBytes(t, src, plan))
			if re.Fields().Title != "Tagged" || re.Properties().First().TotalSamples != 13230 {
				t.Errorf("after the edit: title %q, %d samples", re.Fields().Title, re.Properties().First().TotalSamples)
			}
			capped := mustParseLimited(t, src, wl.Limits{MaxElements: 2})
			if n := capped.Properties().First().TotalSamples; n != 0 || hasWarning(capped, wl.WarnNoAudioFrames) {
				t.Errorf("past the element limit: %d samples, warnings %v; want an unknown length, still audio", n, capped.Warnings())
			}
		})
	}
}

// A stream that starts past index 0 is a cut, whose stated total is no length: ffmpeg's -ss back-patches
// the cut's own count, while a byte carve keeps the whole original's in every block. Its length comes
// from its blocks, or is unknown when the walk to its end does not finish.
func TestWavPackCutLengthFromItsBlocks(t *testing.T) {
	for _, c := range []struct {
		name  string
		total uint32
	}{{"byte carve", 882000}, {"ffmpeg -ss cut", 13230}} {
		t.Run(c.name, func(t *testing.T) {
			blk := func(index uint32) []byte {
				return wvGroupBlock(1|wvRate44k|wvInitial|wvFinal, c.total, index, 4410, make([]byte, 8))
			}
			src := slices.Concat(blk(441000), blk(445410), blk(449820))
			if tr := mustParseBytes(t, src).Properties().First(); tr.TotalSamples != 13230 || tr.Duration != 300*time.Millisecond {
				t.Errorf("track = %+v, want the 13230 samples, 300 ms, the blocks hold", tr)
			}
			capped := mustParseLimited(t, src, wl.Limits{MaxElements: 2})
			if n := capped.Properties().First().TotalSamples; n != 0 || hasWarning(capped, wl.WarnNoAudioFrames) {
				t.Errorf("past the element limit: %d samples, warnings %v; want an unknown length, still audio", n, capped.Warnings())
			}
		})
	}
}

// A stream whose blocks all hold no samples is no audio, whatever total it states. A walk the element
// limit stops before it meets a block with samples cannot tell, so a stream opening with a metadata
// block reads as audio under any limit.
func TestWavPackNoAudioOnlyWhenNoBlockHoldsSamples(t *testing.T) {
	blk := func(flags, index, samples uint32) []byte {
		return wvGroupBlock(1|wvRate44k|flags, 8820, index, samples, make([]byte, 8))
	}
	group := uint32(wvInitial | wvFinal)
	if doc := mustParseBytes(t, slices.Concat(blk(0, 0, 0), blk(0, 0, 0))); !hasWarning(doc, wl.WarnNoAudioFrames) {
		t.Errorf("metadata blocks alone under a stated total: warnings %v, want no-audio", doc.Warnings())
	}
	src := slices.Concat(blk(0, 0, 0), blk(group, 0, 4410), blk(group, 4410, 4410))
	for _, limits := range []wl.Limits{{MaxElements: 1}, {}} {
		doc := mustParseLimited(t, src, limits)
		if hasWarning(doc, wl.WarnNoAudioFrames) {
			t.Errorf("MaxElements %d: warnings %v, want no no-audio", limits.MaxElements, doc.Warnings())
			continue
		}
		if _, err := doc.Edit().Set(tag.Title, "T").Prepare(); err != nil {
			t.Errorf("MaxElements %d: a tag edit was refused: %v", limits.MaxElements, err)
		}
	}
}

// A cut reads what its first block's sub-blocks say, as a stream from sample 0 does: a rate stored in an
// ID_SAMPLE_RATE sub-block, and DSD.
func TestWavPackCutReadsItsSubBlocks(t *testing.T) {
	for _, c := range []struct {
		name        string
		flags       uint32
		body        []byte
		rate, depth int
	}{
		{"rate sub-block", wvRateUnknown, wvSubBlock(0x27, []byte{0xB0, 0x93, 0x00}), 37808, 16},
		{"DSD", wvRateUnknown | wvDSD, slices.Concat(wvSubBlock(0x0E, []byte{0x01}), wvSubBlock(0x27, []byte{0x00, 0x11, 0x2B})), 2822400, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			blk := func(index uint32) []byte {
				return wvGroupBlock(1|c.flags|wvInitial|wvFinal, 0, index, 4410, c.body)
			}
			tr := mustParseBytes(t, slices.Concat(blk(441000), blk(445410))).Properties().First()
			if tr.SampleRate != c.rate || tr.BitsPerSample != c.depth {
				t.Errorf("track = %+v, want %d Hz at %d bits", tr, c.rate, c.depth)
			}
		})
	}
}

// A multichannel stream's channels add up over its first sample group whatever index it starts at, so a
// cut out of a 6-channel stream reads 6 channels.
func TestWavPackMultichannelCutChannels(t *testing.T) {
	blk := func(flags, index uint32) []byte {
		return wvGroupBlock(1|wvRate44k|flags, 8820, index, 4410, make([]byte, 8))
	}
	src := slices.Concat(blk(wvInitial, 441000), blk(0, 441000), blk(wvFinal, 441000),
		blk(wvInitial, 445410), blk(0, 445410), blk(wvFinal, 445410))
	if got := mustParseBytes(t, src).Properties().First().Channels; got != 6 {
		t.Errorf("channels = %d, want the 6 of the first group's three stereo blocks", got)
	}
}

// A file joined from two WavPack streams keeps its digest and bitrate across a tag edit: the first
// stream ends where it says, so the second, junk to it, is not taken for audio once the edit moves it
// up behind the blocks.
func TestJoinedWavPackDigestStableAcrossAnEdit(t *testing.T) {
	sample := readFixture(t, sampleWV)
	src := slices.Concat(sample, readFixture(t, notagsWV))
	essence := essenceOf(t, sample)
	doc := mustParseBytes(t, src)
	if !essenceOf(t, src).Equal(essence) {
		t.Error("the joined file's digest differs from the first stream's")
	}
	plan, err := doc.Edit().Set(tag.Album, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !essenceOf(t, out).Equal(essence) {
		t.Error("the digest changed across the edit")
	}
	if before, after := doc.Properties().First().Bitrate, mustParseBytes(t, out).Properties().First().Bitrate; before != after {
		t.Errorf("bitrate %d before the edit, %d after", before, after)
	}
}

// A block header the source fails to read is an I/O fault inside the file: the parse fails with it
// rather than leaving the WavPack audio end unknown, which would give the digest of another layout.
func TestWavPackUnreadableBlockFailsTheParse(t *testing.T) {
	src := &testfile.Faulty{Src: wl.BytesSource(readFixture(t, sampleWV)), Fails: testfile.Covering(14744)} // the second block's header
	if _, err := wl.Parse(context.Background(), src); !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want the read's own failure", err)
	}
}

// wvpk marker with no usable block header is corrupt input, not a format WaxLabel cannot read.
func TestWavPackTruncatedHeaderRejected(t *testing.T) {
	_, err := wl.Parse(context.Background(), wl.BytesSource([]byte("wvpk\x10\x00\x00\x00")))
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want ErrInvalidData", err)
	}
}

func TestWavPackUnsupportedVersionRefused(t *testing.T) {
	data := slices.Clone(readFixture(t, sampleWV))
	data[8], data[9] = 0xFF, 0x0F // version 0x0FFF, above the supported range
	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("err = %v, want ErrUnsupportedFormat", err)
	}
}

// independent read-back proof: ffprobe is a different codebase, so unlike a self round-trip it
// cannot share a misreading of the APE layout.
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

// the rewritten tail must not disturb the blocks; ffmpeg fails if the audio extent moved.
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
