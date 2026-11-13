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

// ffmpeg takes a binary item for a picture by the file name it stores, so a picture written under
// any role's name must show as an attached picture, tagged with its item name.
func TestWavPackDifferentialFFprobeListsRolePictures(t *testing.T) {
	requireTool(t, "ffprobe")
	path := copyToTemp(t, sampleWV)
	ed := mustParseFile(t, path).Edit()
	for i, typ := range []wl.PictureType{wl.PicFrontCover, wl.PicArtist, wl.PicPublisherLogo} {
		ed.AddPicture(wl.Picture{Type: typ, Data: append(testfile.PNG(), byte(i))})
	}
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_entries", "stream=codec_name:stream_disposition=attached_pic:stream_tags", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var probe struct {
		Streams []struct {
			CodecName   string            `json:"codec_name"`
			Disposition map[string]int    `json:"disposition"`
			Tags        map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("parse ffprobe json: %v\n%s", err, out)
	}
	var names []string
	for _, st := range probe.Streams {
		if st.Disposition["attached_pic"] != 1 || st.CodecName != "png" {
			continue
		}
		for k := range st.Tags {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	if want := []string{"Cover Art (Artist)", "Cover Art (Front)", "Cover Art (Publisher Logotype)"}; !slices.Equal(names, want) {
		t.Errorf("ffprobe attached pictures = %q, want %q\n%s", names, want, out)
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

// bareWV is notags.wv without the APE tag its encoder wrote: the blocks alone.
func bareWV(t *testing.T) []byte {
	t.Helper()
	wv := readFixture(t, notagsWV)
	return wv[:apeTagStart(wv)]
}

// An item name outside the 2 to 255 characters the APEv2 specification allows is read and reported,
// an unrelated edit keeps the item byte for byte, and a set of its key is refused with a warning,
// the item left as the file holds it.
func TestWavPackOutOfSpecItemName(t *testing.T) {
	src := slices.Concat(bareWV(t), headedAPETag(t, "X", "one", "Title", "T"))
	doc := mustParseBytes(t, src)
	if v, _ := doc.Tags().Get("X"); !slices.Equal(v, []string{"one"}) {
		t.Fatalf("X = %q, want the item read", v)
	}
	w, ok := docWarning(doc, wl.WarnMalformedTagEntry)
	if !ok || !slices.Equal(w.Keys, []tag.Key{"X"}) {
		t.Fatalf("warnings = %+v, want malformed-tag-entry keyed X", doc.Warnings())
	}

	plan, err := doc.Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if planWarns(t, plan, wl.WarnValueDropped) {
		t.Errorf("an unrelated edit warned of a drop: %+v", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, []byte("\x03\x00\x00\x00\x00\x00\x00\x00X\x00one")) {
		t.Error("the unrelated edit did not keep the X item byte for byte")
	}

	plan, err = doc.Edit().Set("X", "two").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() || !planWarns(t, plan, wl.WarnValueDropped) {
		t.Errorf("noOp=%v warnings=%+v, want a warned no-op: the name cannot be written", plan.IsNoOp(), plan.Report().Warnings)
	}
}

// headedAPETagCount is headedAPETag with the header and the footer counting n items.
func headedAPETagCount(t *testing.T, n uint32, kv ...string) []byte {
	t.Helper()
	b := headedAPETag(t, kv...)
	binary.LittleEndian.PutUint32(b[16:], n)
	binary.LittleEndian.PutUint32(b[len(b)-16:], n)
	return b
}

// Items past the count the footer states are not read, and reported; an edit of the tag drops them
// with a warning, and an edit that changes nothing leaves the file as it is.
func TestWavPackItemsPastTheFooterCount(t *testing.T) {
	src := slices.Concat(bareWV(t), headedAPETagCount(t, 1, "Title", "T", "Artist", "A", "Album", "B"))
	doc := mustParseBytes(t, src)
	if got := doc.Tags().Keys(); !slices.Equal(got, []tag.Key{tag.Title}) {
		t.Fatalf("keys = %v, want the one item the footer counts", got)
	}
	if w, ok := docWarning(doc, wl.WarnMalformedTagEntry); !ok || !strings.Contains(w.Message, "past the 1 item(s) read") {
		t.Fatalf("warnings = %+v, want malformed-tag-entry for the items past the count", doc.Warnings())
	}

	plan, err := doc.Edit().Set(tag.Title, "T").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() || planWarns(t, plan, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("noOp=%v warnings=%+v, want a no-op that drops nothing", plan.IsNoOp(), plan.Report().Warnings)
	}

	plan, err = doc.Edit().Set(tag.Genre, "Rock").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("plan warnings = %+v, want malformed-tag-entry-dropped", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	if bytes.Contains(out, []byte("Artist\x00A")) {
		t.Error("the items past the count are still in the output")
	}
	re := mustParseBytes(t, out)
	if got := re.Tags().Keys(); !slices.Equal(got, []tag.Key{tag.Title, tag.Genre}) {
		t.Errorf("keys = %v, want the title and the genre", got)
	}
	if ws := re.Warnings(); len(ws) != 0 {
		t.Errorf("warnings after the rewrite = %+v, want none", ws)
	}
}

// A tag marked read-only is edited all the same, and the rewritten tag keeps the mark in its
// header and its footer.
func TestWavPackReadOnlyTagFlagSurvivesARewrite(t *testing.T) {
	tagBytes := headedAPETag(t, "Title", "T")
	binary.LittleEndian.PutUint32(tagBytes[20:], 0xA0000001)
	binary.LittleEndian.PutUint32(tagBytes[len(tagBytes)-12:], 0x80000001)
	src := slices.Concat(bareWV(t), tagBytes)

	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "A longer title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	start := apeTagStart(out)
	if start < 0 {
		t.Fatal("no APE tag ends the output")
	}
	if got := binary.LittleEndian.Uint32(out[start+20:]); got != 0xA0000001 {
		t.Errorf("header flags = %#x, want 0xa0000001", got)
	}
	if got := binary.LittleEndian.Uint32(out[len(out)-12:]); got != 0x80000001 {
		t.Errorf("footer flags = %#x, want 0x80000001", got)
	}
	if got := mustParseBytes(t, out).Fields().Title; got != "A longer title" {
		t.Errorf("title = %q, want the edit applied", got)
	}
}

// A duplicate APE tag whose footer counts fewer items than it holds takes the items nothing read
// with it when a rewrite drops it, and the write says so.
func TestWavPackDuplicateTagUnreadItemsReported(t *testing.T) {
	dup := headedAPETagCount(t, 1, "Title", "T", "Composer", "Only Here")
	src := slices.Concat(bareWV(t), dup, headedAPETag(t, "Title", "T"))
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Album, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnDuplicateTagBlockDropped)
	if !ok || !strings.Contains(w.Message, "unread item byte(s)") {
		t.Errorf("warnings = %+v, want duplicate-tag-block-dropped naming the unread bytes", plan.Report().Warnings)
	}
}

// An item under a name outside the specification whose key has a name inside it ("Artist" with a
// tab behind it reads as ARTIST): an edit of the key writes the key's own name, and a copy that
// sets the key is graded carried and stores it.
func TestWavPackEditRenamesAnItemItCannotWriteAsNamed(t *testing.T) {
	src := slices.Concat(bareWV(t), headedAPETag(t, "Artist\t", "Old", "Title", "T"))
	doc := mustParseBytes(t, src)
	if got := doc.Fields().Artists; !slices.Equal(got, []string{"Old"}) {
		t.Fatalf("artists = %q, want the item read", got)
	}
	if !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("warnings = %+v, want malformed-tag-entry for the item name", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Artist, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() || planWarns(t, plan, wl.WarnValueDropped) {
		t.Fatalf("noOp=%v warnings=%+v, want the edit written with no drop", plan.IsNoOp(), plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, []byte("Artist\x00New")) || bytes.Contains(out, []byte("Artist\t")) {
		t.Error("the edit did not write the key under its own name in place of the item's")
	}
	if re := mustParseBytes(t, out); len(re.Warnings()) != 0 || !slices.Equal(re.Fields().Artists, []string{"New"}) {
		t.Errorf("after the edit: artists %q, warnings %+v", re.Fields().Artists, re.Warnings())
	}

	flacSrc := readFixture(t, sampleFLAC)
	seed, err := mustParseBytes(t, flacSrc).Edit().Set(tag.Artist, "Copied").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	copyPlan, report, err := mustParseBytes(t, applyToBytes(t, flacSrc, seed)).PrepareTransfer(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range report.Items {
		if it.Kind == wl.TransferField && it.Key == tag.Artist && it.Disposition != wl.Carried {
			t.Errorf("ARTIST graded %v, want carried", it.Disposition)
		}
	}
	if got := mustParseBytes(t, applyToBytes(t, src, copyPlan)).Fields().Artists; !slices.Equal(got, []string{"Copied"}) {
		t.Errorf("artists after the copy = %q, want the copied value", got)
	}
}

// A key held in two items under a name no edit can write: a refused set leaves both, and a copy of
// the file onto itself grades the key carried and writes nothing.
func TestWavPackRefusedSetKeepsEveryItemOfTheKey(t *testing.T) {
	src := slices.Concat(bareWV(t), headedAPETag(t, "X", "one", "X", "uno", "Title", "T"))
	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().Set("X", "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() || !planWarns(t, plan, wl.WarnValueDropped) {
		t.Errorf("noOp=%v warnings=%+v, want a warned no-op", plan.IsNoOp(), plan.Report().Warnings)
	}
	// The same refusal beside an edit that does write.
	plan, err = doc.Edit().Set("X", "New").Set(tag.Title, "Written").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, src, plan))
	if got, _ := re.Tags().Get("X"); !slices.Equal(got, []string{"one", "uno"}) || re.Fields().Title != "Written" {
		t.Errorf("X = %q, title %q, want both X items kept beside the new title", got, re.Fields().Title)
	}

	selfPlan, report, err := doc.PrepareTransfer(mustParseBytes(t, src))
	if err != nil {
		t.Fatal(err)
	}
	if report.HasDropped() || !selfPlan.IsNoOp() {
		t.Errorf("self-copy: dropped=%v noOp=%v, want every key carried and nothing written: %+v", report.HasDropped(), selfPlan.IsNoOp(), report.Items)
	}
}

// A legacy strip that edits nothing copies the tag as the file holds it: the items past the footer's
// count stay, with the read-only mark, and only the ID3v1 tag goes.
func TestWavPackStripAloneCopiesTheTag(t *testing.T) {
	tagBytes := headedAPETagCount(t, 1, "Title", "T", "Artist", "A")
	binary.LittleEndian.PutUint32(tagBytes[20:], 0xA0000001)
	binary.LittleEndian.PutUint32(tagBytes[len(tagBytes)-12:], 0x80000001)
	stream := bareWV(t)
	src := slices.Concat(stream, tagBytes, testfile.ID3v1("Legacy", "", "", "", "", 255))

	plan, err := mustParseBytes(t, src).Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() || planWarns(t, plan, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("noOp=%v warnings=%+v, want a write that drops no item bytes", plan.IsNoOp(), plan.Report().Warnings)
	}
	if out := applyToBytes(t, src, plan); !bytes.Equal(out, slices.Concat(stream, tagBytes)) {
		t.Errorf("output is %d bytes, want the stream and the tag as the file held them (%d)", len(out), len(stream)+len(tagBytes))
	}
}

// An edit the writer refuses, made beside a legacy strip, leaves the tag as the file holds it: the
// strip is written, the items past the footer's count stay, and only the refused value is warned.
func TestWavPackRefusedEditBesideAStripCopiesTheTag(t *testing.T) {
	tagBytes := headedAPETagCount(t, 1, "Title", "T", "Artist", "A")
	stream := bareWV(t)
	src := slices.Concat(stream, tagBytes, testfile.ID3v1("Legacy", "", "", "", "", 255))

	plan, err := mustParseBytes(t, src).Edit().Set("X", "1").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if planWarns(t, plan, wl.WarnMalformedTagEntryDropped) || !planWarns(t, plan, wl.WarnValueDropped) {
		t.Errorf("warnings = %+v, want the refused value and no dropped item bytes", plan.Report().Warnings)
	}
	if ops := plan.Report().Operations; slices.Contains(ops, "APEv2 rewrite") {
		t.Errorf("operations = %q, want no rewrite of the tag reported", ops)
	}
	if out := applyToBytes(t, src, plan); !bytes.Equal(out, slices.Concat(stream, tagBytes)) {
		t.Errorf("output is %d bytes, want the stream and the tag as the file held them (%d)", len(out), len(stream)+len(tagBytes))
	}
}

// A tag the element limit cut short: a legacy strip copies it whole, the items that were not read
// with it, and an edit of it is refused.
func TestWavPackStripCopiesATagTheElementLimitCut(t *testing.T) {
	tagBytes := headedAPETag(t, "Title", "T", "Artist", "A", "Album", "B")
	stream := bareWV(t)
	src := slices.Concat(stream, tagBytes, testfile.ID3v1("Legacy", "", "", "", "", 255))
	doc := mustParseLimited(t, src, wl.Limits{MaxElements: 2})
	if !hasWarning(doc, wl.WarnElementCap) {
		t.Fatalf("warnings = %+v, want the element limit reported", doc.Warnings())
	}
	plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if out := applyToBytes(t, src, plan); !bytes.Equal(out, slices.Concat(stream, tagBytes)) {
		t.Errorf("output is %d bytes, want the stream and the whole tag (%d)", len(out), len(stream)+len(tagBytes))
	}
	if _, err := doc.Edit().Set(tag.Genre, "Rock").Prepare(); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("an edit of the tag: err = %v, want ErrSizeTooLarge", err)
	}
}
