package ogg

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// parseOpusStreamWithGain builds and parses a minimal three-page Opus stream whose
// OpusHead declares the given Q7.8 output gain. The comment packet carries "TITLE=Song"
// plus the given extra comments and a few trailing padding bytes, so a rewrite that
// preserves the padding can be told from one that drops it.
func parseOpusStreamWithGain(t *testing.T, gain int, comments ...string) *core.Media {
	t.Helper()
	base, err := parse(context.Background(), core.BytesSource(buildOpusStreamWithGain(t, gain, comments...)), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return base
}

// buildOpusStreamWithGain renders the stream bytes parseOpusStreamWithGain parses.
func buildOpusStreamWithGain(t *testing.T, gain int, comments ...string) []byte {
	t.Helper()
	const serial = 0x4F4747
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xBB, 0, 0, 0, 0, 0}
	head[16], head[17] = byte(gain), byte(gain>>8)
	tags := append(opusTagsPacket("libopus 1.4", append([]string{"TITLE=Song"}, comments...)...), 0, 0, 0, 0)
	if len(tags) > 255 {
		t.Fatalf("comment packet is %d bytes; keep test covers small enough for single-segment lacing", len(tags))
	}
	audio := []byte("AUDIOPKT!!")
	return slices.Concat(
		buildPage(flagBOS, 0, serial, 0, []byte{byte(len(head))}, head),
		buildPage(0, 0, serial, 1, []byte{byte(len(tags))}, tags),
		buildPage(0, 960, serial, 2, []byte{byte(len(audio))}, audio),
	)
}

// TestParseOpusOutputGain: the OpusHead's signed Q7.8 output gain reads into the track. A
// head too short to hold the field reports none rather than reading past it.
func TestParseOpusOutputGain(t *testing.T) {
	for _, gain := range []int{-896, 0, 4096} {
		if got := parseOpusStreamWithGain(t, gain).Properties.First().OutputGain; got != gain {
			t.Errorf("OutputGain = %d, want %d", got, gain)
		}
	}
	if got := opusOutputGain([]byte("OpusHead\x01\x02\x00\x00\x80\xbb\x00\x00")); got != 0 {
		t.Errorf("16-byte head: OutputGain = %d, want 0", got)
	}
}

// TestEssenceExtentOpusMasksOutputGain: the hashed configuration zeroes the output gain,
// so a gain edit leaves the digest alone and two copies differing only in gain dedup. The
// extent name is versioned so a v1 digest never compares equal to a v2 one.
func TestEssenceExtentOpusMasksOutputGain(t *testing.T) {
	nameA, cfgA := Codec{format: core.FormatOggOpus}.EssenceExtent(parseOpusStreamWithGain(t, -896))
	nameB, cfgB := Codec{format: core.FormatOggOpus}.EssenceExtent(parseOpusStreamWithGain(t, 0))
	if nameA != "ogg-opus-packets-v2" || nameB != nameA {
		t.Errorf("extent names = %q/%q, want ogg-opus-packets-v2", nameA, nameB)
	}
	if !bytes.Equal(cfgA, cfgB) {
		t.Errorf("config differs with the gain:\n % x\n % x", cfgA, cfgB)
	}
	if len(cfgA) < 18 || cfgA[16] != 0 || cfgA[17] != 0 {
		t.Errorf("config bytes 16:18 = % x, want zeroed", cfgA[16:18])
	}
	head := parseOpusStreamWithGain(t, -896).Native.(*doc).idPacket
	if len(cfgA) != len(head) || !bytes.Equal(cfgA[:16], head[:16]) || !bytes.Equal(cfgA[18:], head[18:]) {
		t.Errorf("config must keep the whole OpusHead but the gain intact:\n % x\n % x", cfgA, head)
	}
}

// TestOpusDescribeNotesOutputGain: the native view names a non-zero gain in dB, and says
// nothing when there is none.
func TestOpusDescribeNotesOutputGain(t *testing.T) {
	find := func(m *core.Media) core.NativeEntry {
		for _, e := range m.Native.Describe() {
			if e.Kind == "OpusHead" {
				return e
			}
		}
		t.Fatal("no OpusHead entry")
		return core.NativeEntry{}
	}
	if note := find(parseOpusStreamWithGain(t, -896)).Note; !strings.Contains(note, "-3.50 dB") {
		t.Errorf("OpusHead note = %q, want it to name the gain", note)
	}
	if note := find(parseOpusStreamWithGain(t, 0)).Note; note != "" {
		t.Errorf("OpusHead note = %q, want none at gain 0", note)
	}
}

// TestOggCapabilitiesOutputGain: only Ogg Opus stores an output gain.
func TestOggCapabilitiesOutputGain(t *testing.T) {
	for _, c := range []struct {
		format core.Format
		want   core.AccessLevel
	}{
		{core.FormatOggOpus, core.AccessFull},
		{core.FormatOggVorbis, core.AccessNone},
		{core.FormatOggFLAC, core.AccessNone},
	} {
		if got := (Codec{format: c.format}).Capabilities(nil, core.WriteOptions{}).OutputGain; got != c.want {
			t.Errorf("%s OutputGain capability = %v, want %v", c.format, got, c.want)
		}
	}
}

// planOpusGain parses a gain-0 stream and plans an edit that sets gain, plus whatever else
// mutate applies. It returns the source bytes, the parsed base, and the plan.
func planOpusGain(t *testing.T, gain int, mutate func(*core.Media)) ([]byte, *core.Media, *core.WritePlan) {
	t.Helper()
	src := buildOpusStreamWithGain(t, 0)
	base, err := parse(context.Background(), core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edited := *base
	edited.Properties = base.Properties.Clone()
	edited.Properties.Tracks[0].OutputGain = gain
	if mutate != nil {
		edited.Tags = base.Tags.Clone()
		mutate(&edited)
	}
	plan, err := (Codec{format: core.FormatOggOpus}).Plan(context.Background(), base, &edited, core.WriteOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return src, base, plan
}

// renderPlan materializes a plan against the bytes it was planned from.
func renderPlan(t *testing.T, src []byte, plan *core.WritePlan) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := bits.Write(context.Background(), &buf, core.BytesSource(src), plan.Segments, nil); err != nil {
		t.Fatalf("render plan: %v", err)
	}
	return buf.Bytes()
}

// pageLen returns the total byte length of the Ogg page starting at b[0].
func pageLen(t *testing.T, b []byte) int {
	t.Helper()
	if len(b) < 27 || string(b[:4]) != "OggS" {
		t.Fatalf("not an Ogg page: % x", b[:min(len(b), 27)])
	}
	n := int(b[26])
	total := 27 + n
	for _, seg := range b[27 : 27+n] {
		total += int(seg)
	}
	return total
}

// TestPlanOpusOutputGainRewritesBOSPageOnly: a gain-only edit patches the OpusHead and
// leaves every byte after the original page 0 alone, so the audio and the comment padding
// survive on any stream, canonical or not.
func TestPlanOpusOutputGainRewritesBOSPageOnly(t *testing.T) {
	src, base, plan := planOpusGain(t, -896, nil)
	if plan.NoOp {
		t.Fatal("a gain change must not plan a no-op")
	}
	if !slices.Contains(plan.Report.Operations, "OpusHead output gain rewrite") {
		t.Errorf("operations = %v, want the OpusHead rewrite", plan.Report.Operations)
	}

	out := renderPlan(t, src, plan)
	srcPage0 := pageLen(t, src)
	outPage0 := pageLen(t, out)
	if !bytes.Equal(out[outPage0:], src[srcPage0:]) {
		t.Error("everything after page 0 must be byte-identical to the input")
	}

	head, srcHead := out[27+1:outPage0], src[27+1:srcPage0]
	if len(head) != len(srcHead) {
		t.Fatalf("OpusHead length changed: %d -> %d", len(srcHead), len(head))
	}
	if head[16] != 0x80 || head[17] != 0xFC { // -896 little-endian
		t.Errorf("patched gain bytes = % x, want 80 fc", head[16:18])
	}
	if !bytes.Equal(head[:16], srcHead[:16]) || !bytes.Equal(head[18:], srcHead[18:]) {
		t.Error("the rest of the OpusHead must be untouched")
	}

	// The rebuilt page carries a valid checksum: the CRC is computed over the page with
	// its own checksum field zeroed.
	page := slices.Clone(out[:outPage0])
	want := binary.LittleEndian.Uint32(page[22:26])
	clear(page[22:26])
	if got := bits.OggCRC(page); got != want {
		t.Errorf("page 0 CRC = %08x, want %08x", got, want)
	}

	reparsed, err := parse(context.Background(), core.BytesSource(out), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if got := reparsed.Properties.First().OutputGain; got != -896 {
		t.Errorf("re-parsed OutputGain = %d, want -896", got)
	}
	if v, ok := reparsed.Tags.Get("TITLE"); !ok || len(v) != 1 || v[0] != "Song" {
		t.Errorf("re-parsed TITLE = %v (ok=%v), want [Song]", v, ok)
	}
	if got, want := reparsed.Native.(*doc).commentPad, base.Native.(*doc).commentPad; !bytes.Equal(got, want) {
		t.Errorf("comment padding = % x, want % x", got, want)
	}

	res := plan.Result.Native.(*doc)
	if got := opusOutputGain(res.idPacket); got != -896 {
		t.Errorf("result idPacket gain = %d, want -896", got)
	}
	shift := int64(outPage0 - srcPage0)
	baseRanges, resRanges := base.EssenceRanges(), plan.Result.EssenceRanges()
	if len(baseRanges) != len(resRanges) {
		t.Fatalf("audio range counts differ: %d vs %d", len(baseRanges), len(resRanges))
	}
	for i := range baseRanges {
		if resRanges[i][0] != baseRanges[i][0]+shift || resRanges[i][1] != baseRanges[i][1]+shift {
			t.Errorf("audio range %d = %v, want %v shifted by %d", i, resRanges[i], baseRanges[i], shift)
		}
	}
}

// TestPlanOpusOutputGainWithTagEdit: a gain change alongside a comment edit patches the
// header and lands the new tag in one write.
func TestPlanOpusOutputGainWithTagEdit(t *testing.T) {
	src, _, plan := planOpusGain(t, -896, func(m *core.Media) { m.Tags.Set("TITLE", "New") })
	out := renderPlan(t, src, plan)
	reparsed, err := parse(context.Background(), core.BytesSource(out), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if got := reparsed.Properties.First().OutputGain; got != -896 {
		t.Errorf("re-parsed OutputGain = %d, want -896", got)
	}
	if v, _ := reparsed.Tags.Get("TITLE"); len(v) != 1 || v[0] != "New" {
		t.Errorf("re-parsed TITLE = %v, want [New]", v)
	}
}

// TestPlanOpusOutputGainEqualIsNoOp: setting the gain the file already carries changes
// nothing.
func TestPlanOpusOutputGainEqualIsNoOp(t *testing.T) {
	if _, _, plan := planOpusGain(t, 0, nil); !plan.NoOp {
		t.Errorf("setting the existing gain should be a no-op; operations = %v", plan.Report.Operations)
	}
}

// TestPlanOpusOutputGainShortHeadRefused: a header too short to hold the field cannot be
// patched, so the write is refused rather than silently skipped.
func TestPlanOpusOutputGainShortHeadRefused(t *testing.T) {
	const serial = 0x4F4747
	head := []byte("OpusHead\x01\x02\x00\x00\x80\xbb\x00\x00") // 16 bytes: no output_gain field
	tags := opusTagsPacket("libopus 1.4", "TITLE=Song")
	audio := []byte("AUDIOPKT!!")
	src := slices.Concat(
		buildPage(flagBOS, 0, serial, 0, []byte{byte(len(head))}, head),
		buildPage(0, 0, serial, 1, []byte{byte(len(tags))}, tags),
		buildPage(0, 960, serial, 2, []byte{byte(len(audio))}, audio),
	)
	base, err := parse(context.Background(), core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edited := *base
	edited.Properties = base.Properties.Clone()
	edited.Properties.Tracks[0].OutputGain = -896
	if _, err := (Codec{format: core.FormatOggOpus}).Plan(context.Background(), base, &edited, core.WriteOptions{}); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("Plan on a 16-byte OpusHead = %v, want ErrInvalidData", err)
	}
}

// TestPlanVorbisIgnoresOutputGain: Vorbis has no output gain, so a stray value on the
// track cannot force a rewrite.
func TestPlanVorbisIgnoresOutputGain(t *testing.T) {
	src, err := os.ReadFile("../../testdata/sample.ogg")
	if err != nil {
		t.Fatal(err)
	}
	base, err := parse(context.Background(), core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edited := *base
	edited.Properties = base.Properties.Clone()
	if len(edited.Properties.Tracks) == 0 {
		edited.Properties.Tracks = []core.AudioTrack{{}}
	}
	edited.Properties.Tracks[0].OutputGain = -896
	plan, err := (Codec{format: core.FormatOggVorbis}).Plan(context.Background(), base, &edited, core.WriteOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.NoOp {
		t.Errorf("a Vorbis stream has no output gain to change; operations = %v", plan.Report.Operations)
	}
}

// TestPlanOpusOutputGainKeepsEssenceConfig: the hashed configuration masks the gain, so a
// gain edit leaves the essence digest inputs identical.
func TestPlanOpusOutputGainKeepsEssenceConfig(t *testing.T) {
	_, base, plan := planOpusGain(t, -896, nil)
	c := Codec{format: core.FormatOggOpus}
	nameBefore, cfgBefore := c.EssenceExtent(base)
	nameAfter, cfgAfter := c.EssenceExtent(plan.Result)
	if nameBefore != nameAfter || !bytes.Equal(cfgBefore, cfgAfter) {
		t.Errorf("essence config changed: %s/% x -> %s/% x", nameBefore, cfgBefore, nameAfter, cfgAfter)
	}
}

// TestPlanOpusOutputGainWithNoTagDelta: an edit that re-sets a tag to the value it already
// holds leaves every comparison the no-op paths make equal, so only the gain can carry the
// write. It pins the gate rather than the downgrade, which a gain-only edit never reaches.
func TestPlanOpusOutputGainWithNoTagDelta(t *testing.T) {
	_, _, plan := planOpusGain(t, -896, func(m *core.Media) { m.Tags.Set("TITLE", "Song") })
	if plan.NoOp {
		t.Error("a gain change with an otherwise identical tag set must not plan a no-op")
	}
}
