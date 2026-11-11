package ogg

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
)

// TestOpusPacketSamples: a packet's length in 48 kHz samples follows its TOC byte: the frame size
// of its configuration times its frame count.
func TestOpusPacketSamples(t *testing.T) {
	toc := func(config, code int, rest ...byte) []byte { return append([]byte{byte(config<<3 | code)}, rest...) }
	for _, c := range []struct {
		name string
		pkt  []byte
		want int
		ok   bool
	}{
		{"SILK 10 ms", toc(0, 0), 480, true},
		{"SILK 20 ms", toc(1, 0), 960, true},
		{"SILK 40 ms", toc(6, 0), 1920, true},
		{"SILK 60 ms", toc(11, 0), 2880, true},
		{"hybrid 10 ms", toc(12, 0), 480, true},
		{"hybrid 20 ms", toc(15, 0), 960, true},
		{"CELT 2.5 ms", toc(16, 0), 120, true},
		{"CELT 5 ms", toc(21, 0), 240, true},
		{"CELT 10 ms", toc(26, 0), 480, true},
		{"CELT 20 ms", toc(31, 0), 960, true},
		{"two equal frames", toc(31, 1), 1920, true},
		{"two frames of different sizes", toc(31, 2), 1920, true},
		{"a counted run of frames", toc(31, 3, 6), 5760, true},
		{"a count under padding and VBR flags", toc(16, 3, 0xC0|48), 5760, true},
		{"a counted run with no count byte", toc(31, 3), 0, false},
		{"a count of no frames", toc(31, 3, 0), 0, false},
		{"over 120 ms", toc(11, 3, 3), 0, false},
		{"an empty packet", nil, 0, false},
	} {
		if got, ok := opusPacketSamples(c.pkt); got != c.want || ok != c.ok {
			t.Errorf("%s: opusPacketSamples = %d, %v, want %d, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// vorbisSetup renders the end of a Vorbis setup header: lead bits standing for what precedes the
// mode table, then the table and the framing bit, packed low bit first as Vorbis packs them.
func vorbisSetup(lead int, flags ...bool) []byte {
	var bitsOut []byte
	put := func(v uint64, n int) {
		for i := range n {
			bitsOut = append(bitsOut, byte(v>>i&1))
		}
	}
	for i := range lead {
		put(uint64(i%3&1), 1)
	}
	put(uint64(len(flags)-1), 6)
	for i, f := range flags {
		var flag uint64
		if f {
			flag = 1
		}
		put(flag, 1)
		put(0, 32)        // window and transform type
		put(uint64(i), 8) // mapping
	}
	put(1, 1) // framing
	out := []byte("\x05vorbis")
	for i, b := range bitsOut {
		if i%8 == 0 {
			out = append(out, 0)
		}
		out[len(out)-1] |= b << (i % 8)
	}
	return out
}

// TestVorbisModeBlockFlags: the mode table that closes the setup header gives each mode's block
// flag, wherever its bits fall in their bytes.
func TestVorbisModeBlockFlags(t *testing.T) {
	for _, flags := range [][]bool{{false}, {false, true}, {true, false, true}, {false, true, true, false}} {
		for lead := 60; lead < 68; lead++ {
			got, ok := vorbisModeBlockFlags(vorbisSetup(lead, flags...))
			if !ok || !slices.Equal(got, flags) {
				t.Errorf("flags %v behind %d bits: got %v, %v", flags, lead, got, ok)
			}
		}
	}
	// The packet may be padded with zeros behind the framing bit.
	padded := append(vorbisSetup(64, false, true), make([]byte, 1<<20)...)
	if got, ok := vorbisModeBlockFlags(padded); !ok || !slices.Equal(got, []bool{false, true}) {
		t.Errorf("a padded header: got %v, %v", got, ok)
	}
	for name, setup := range map[string][]byte{
		"an empty packet":    nil,
		"no framing bit":     append([]byte("\x05vorbis"), make([]byte, 40)...),
		"no mode table":      append([]byte("\x05vorbis"), 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x01),
		"not a setup header": vorbisSetup(64, false, true)[1:],
	} {
		if got, ok := vorbisModeBlockFlags(setup); ok {
			t.Errorf("%s: got %v, want none", name, got)
		}
	}
}

// TestStartGranuleOfTheFixtures: a stream encoded from its first sample starts at granule position
// 0, which the packets of its first audio page have to add up to exactly; that pins the Opus
// frame sizes, the FLAC block sizes and the Vorbis mode table of a real libvorbis setup header.
func TestStartGranuleOfTheFixtures(t *testing.T) {
	for _, name := range []string{"sample.ogg", "notags.ogg", "sample.opus", "notags.opus", "sample.oga", "notags.oga"} {
		data, err := os.ReadFile("../../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		m, err := parse(context.Background(), Codec{}, core.BytesSource(data), core.DefaultParseOptions())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		d := m.Native.(*doc)
		if d.startGranule != 0 {
			t.Errorf("%s: start granule = %d, want 0", name, d.startGranule)
		}
		// Moved on by a second, the positions give that second as the start, where the first
		// audio page is not the trimmed last one, whose position is short of its packets.
		if len(d.audioPages) < 2 {
			continue
		}
		const moved = 48000
		for off := 0; off < len(data); off += pageLen(t, data[off:]) {
			if g := binary.LittleEndian.Uint64(data[off+6:]); g != 0 {
				binary.LittleEndian.PutUint64(data[off+6:], g+moved)
			}
		}
		m, err = parse(context.Background(), Codec{}, core.BytesSource(data), core.DefaultParseOptions())
		if err != nil {
			t.Fatalf("%s, moved: %v", name, err)
		}
		if got := m.Native.(*doc).startGranule; got != moved {
			t.Errorf("%s: start granule = %d with every position moved on by %d", name, got, moved)
		}
		if d.kind == kindVorbis {
			if flags, ok := vorbisModeBlockFlags(d.setupPacket); !ok || !slices.Equal(flags, []bool{false, true}) {
				t.Errorf("%s: mode block flags = %v, %v, want a short and a long mode", name, flags, ok)
			}
		}
	}
}

// TestDurationCountsFromTheStartGranule: a stream whose granule positions do not start at 0, cut
// from a longer one, lasts from its first sample to its last. An Opus stream's pre-skip comes off
// once.
func TestDurationCountsFromTheStartGranule(t *testing.T) {
	const serial, preSkip, offset = 0x4F4747, 312, 480000
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, byte(preSkip & 0xFF), preSkip >> 8, 0x80, 0xBB, 0, 0, 0, 0, 0}
	tags := opusTagsPacket("libopus 1.4", "TITLE=Song")
	stream := slices.Concat(
		buildPage(flagBOS, 0, serial, 0, []byte{byte(len(head))}, head),
		buildPage(0, 0, serial, 1, []byte{byte(len(tags))}, tags),
		// Three 20 ms packets, then two more.
		buildPage(0, offset+2880, serial, 2, []byte{1, 1, 1}, []byte{0x08, 0x08, 0x08}),
		buildPage(0, offset+4800, serial, 3, []byte{1, 1}, []byte{0x08, 0x08}),
	)
	m, err := parse(context.Background(), Codec{}, core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	tr := m.Properties.First()
	if want := uint64(4800 - preSkip); tr.TotalSamples != want {
		t.Errorf("total samples = %d, want %d", tr.TotalSamples, want)
	}
	if want := core.SamplesToDuration(4800-preSkip, 48000); tr.Duration != want || tr.Duration > 100*time.Millisecond {
		t.Errorf("duration = %v, want %v", tr.Duration, want)
	}
	if want := core.AverageBitrate(5, tr.Duration.Seconds()); tr.Bitrate != want {
		t.Errorf("bitrate = %d, want %d over the duration", tr.Bitrate, want)
	}
}

// TestFLACStreamInfoAgainstGranules: STREAMINFO's sample count is the stream's length unless the
// pages end more than a page short of it; then the file holds less than it declares, which is
// reported, and the length is what the pages hold.
func TestFLACStreamInfoAgainstGranules(t *testing.T) {
	const serial, pageSamples, pages = 0x57ac7557, 4096, 3
	for _, c := range []struct {
		name      string
		declared  uint64
		want      uint64
		truncated bool
	}{
		{"as declared", pages * pageSamples, pages * pageSamples, false},
		{"none declared", 0, pages * pageSamples, false},
		{"one page short", (pages + 1) * pageSamples, (pages + 1) * pageSamples, false},
		{"over a page short", (pages+1)*pageSamples + 1, pages * pageSamples, true},
		{"pages past the declared count", (pages - 1) * pageSamples, (pages - 1) * pageSamples, false},
	} {
		stream := testfile.OggFLAC(testfile.CommentBlock("v", "TITLE=x"))
		// STREAMINFO's 36-bit sample count ends 18 bytes into the block, which the
		// identification packet holds 17 bytes in, behind a one-segment page header.
		si := pageFixedHdr + 1 + flacStreamInfoOff
		binary.BigEndian.PutUint32(stream[si+14:], uint32(c.declared))
		testfile.OggSealPage(stream[:pageLen(t, stream)])
		for i := 2; i <= pages; i++ {
			stream = append(stream, buildPage(0, uint64(i*pageSamples), serial, uint32(i+1), []byte{5}, []byte("FRAME"))...)
		}
		m, err := parse(context.Background(), Codec{}, core.BytesSource(stream), core.DefaultParseOptions())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := m.Properties.First().TotalSamples; got != c.want {
			t.Errorf("%s: total samples = %d, want %d", c.name, got, c.want)
		}
		if got := len(core.WarningsWithCode(m.Warnings, core.WarnTruncatedAudio)) == 1; got != c.truncated {
			t.Errorf("%s: truncated-audio reported = %v, want %v (%v)", c.name, got, c.truncated, m.Warnings)
		}
	}
}

// TestStartScanReadsUnderALowLimit: measuring the start takes the first bytes of each packet, not
// the page bodies, so an allocation limit below an audio page's size does not refuse the file.
func TestStartScanReadsUnderALowLimit(t *testing.T) {
	data, err := os.ReadFile("../../testdata/sample.opus")
	if err != nil {
		t.Fatal(err)
	}
	want, err := parse(context.Background(), Codec{}, core.BytesSource(data), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	opts := core.DefaultParseOptions()
	opts.Limits.MaxAllocBytes = 4096
	if first := want.Native.(*doc).audioPages[0]; first.bodyLen <= opts.Limits.MaxAllocBytes {
		t.Fatalf("setup: the first audio page holds %d bytes, within the limit", first.bodyLen)
	}
	got, err := parse(context.Background(), Codec{}, core.BytesSource(data), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Properties.First().Duration != want.Properties.First().Duration {
		t.Errorf("duration = %v, want %v", got.Properties.First().Duration, want.Properties.First().Duration)
	}
}

// TestFLACStreamInfoCheckSurvivesGranulesThatGoDown: the page tolerance of the STREAMINFO check
// is the samples the last page added, none when its position is below the one before it.
func TestFLACStreamInfoCheckSurvivesGranulesThatGoDown(t *testing.T) {
	const serial = 0x57ac7557
	stream := testfile.OggFLAC(testfile.CommentBlock("v", "TITLE=x"))
	binary.BigEndian.PutUint32(stream[pageFixedHdr+1+flacStreamInfoOff+14:], 100000)
	testfile.OggSealPage(stream[:pageLen(t, stream)])
	stream = append(stream, buildPage(0, 9000, serial, 3, []byte{5}, []byte("FRAME"))...)
	stream = append(stream, buildPage(0, 8192, serial, 4, []byte{5}, []byte("FRAME"))...)
	m, err := parse(context.Background(), Codec{}, core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(core.WarningsWithCode(m.Warnings, core.WarnTruncatedAudio)) != 1 {
		t.Errorf("warnings = %v, want truncated-audio: STREAMINFO declares 100000 samples and the pages end at 8192", m.Warnings)
	}
}

// TestStartScanDropsAPacketThePagesDoNotFinish: a page that does not continue the packet the
// page before it left open starts a new one, measured by its own first bytes; the unfinished
// packet adds nothing, as in a decoder.
func TestStartScanDropsAPacketThePagesDoNotFinish(t *testing.T) {
	const serial, offset = 0x4F4747, 480000
	open := append([]byte{0x18}, make([]byte, 254)...) // the first 255 bytes of a 60 ms packet
	stream := slices.Concat(
		buildOpusStreamAt(t, 0, 0, 0),
		buildPage(0, math.MaxUint64, serial, 2, []byte{255}, open),
		// The page that finished it is lost: this one opens with a 20 ms packet of its own.
		buildPage(0, offset+960, serial, 4, []byte{1}, []byte{0x08}),
	)
	m, err := parse(context.Background(), Codec{}, core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Native.(*doc).startGranule; got != offset {
		t.Errorf("start granule = %d, want %d", got, offset)
	}
}

// TestFLACStreamCutFromALongerOne: an Ogg FLAC stream whose positions start past 0 holds the
// samples from its start to its last position. STREAMINFO's count stands when it agrees with
// that, and is reported and set aside when it still declares the longer stream.
func TestFLACStreamCutFromALongerOne(t *testing.T) {
	const moved, held = 441000, 66150 // the fixture's 1.5 s, ten seconds into a longer stream
	for _, c := range []struct {
		declared  uint32
		truncated bool
	}{{held, false}, {moved + held, true}} {
		data, err := os.ReadFile("../../testdata/sample.oga")
		if err != nil {
			t.Fatal(err)
		}
		binary.BigEndian.PutUint32(data[pageFixedHdr+1+flacStreamInfoOff+14:], c.declared)
		for off := 0; off < len(data); {
			n := pageLen(t, data[off:])
			if g := binary.LittleEndian.Uint64(data[off+6:]); g != 0 {
				binary.LittleEndian.PutUint64(data[off+6:], g+moved)
			}
			testfile.OggSealPage(data[off : off+n])
			off += n
		}
		m, err := parse(context.Background(), Codec{}, core.BytesSource(data), core.DefaultParseOptions())
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Properties.First().TotalSamples; got != held {
			t.Errorf("STREAMINFO declaring %d: total samples = %d, want %d", c.declared, got, held)
		}
		if got := len(core.WarningsWithCode(m.Warnings, core.WarnTruncatedAudio)) == 1; got != c.truncated {
			t.Errorf("STREAMINFO declaring %d: truncated-audio reported = %v, want %v", c.declared, got, c.truncated)
		}
	}
}
