package flac

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// fixtureInfo mirrors testdata/sample.flac's STREAMINFO: 44.1 kHz, stereo,
// 16-bit, fixed 4608-sample blocks.
func fixtureInfo() core.AudioTrack {
	return core.AudioTrack{
		SampleRate: 44100, Channels: 2, BitsPerSample: 16,
		MinBlockSize: 4608, MaxBlockSize: 4608, TotalSamples: 44100,
	}
}

// TestDecodeFrameHeader drives the header decoder with two headers from
// testdata/sample.flac (frame 0, and the short last frame with a 16-bit coded block
// size), a hand-built variable-strategy header, and corruptions of each field.
func TestDecodeFrameHeader(t *testing.T) {
	si := fixtureInfo()
	first := []byte{0xFF, 0xF8, 0x59, 0x88, 0x00, 0x8A}
	last := []byte{0xFF, 0xF8, 0x79, 0x88, 0x09, 0x0A, 0x43, 0x26}
	// Variable strategy, 4096-sample block (code 1100), sample number 40960
	// (3-byte coded number); CRC-8 appended by the codec under test's own crc8,
	// which TestFrameCRC8 pins to known vectors.
	varHdr := []byte{0xFF, 0xF9, 0xC9, 0x88, 0xEA, 0x80, 0x80}
	varHdr = append(varHdr, crc8(0, varHdr))

	for _, tc := range []struct {
		name string
		in   []byte
		want frameHeader
	}{
		{"fixture frame 0", first, frameHeader{variable: false, num: 0, block: 4608, size: 6}},
		{"fixture last frame", last, frameHeader{variable: false, num: 9, block: 2628, size: 8}},
		{"variable strategy", varHdr, frameHeader{variable: true, num: 40960, block: 4096, size: 8}},
	} {
		got, ok := decodeFrameHeader(tc.in, si)
		if !ok || got != tc.want {
			t.Errorf("%s: decodeFrameHeader = %+v, %v; want %+v, true", tc.name, got, ok, tc.want)
		}
	}

	corrupt := func(b []byte, i int, v byte) []byte {
		out := append([]byte(nil), b...)
		out[i] = v
		return out
	}
	// 8192-sample block (code 1101) against a 4608 STREAMINFO maximum; every
	// other field valid and the CRC-8 correct, so only the block bound rejects.
	overBlock := []byte{0xFF, 0xF8, 0xD9, 0x18, 0x00}
	overBlock = append(overBlock, crc8(0, overBlock))
	other := si
	other.SampleRate = 48000
	mono := si
	mono.Channels = 1
	deep := si
	deep.BitsPerSample = 24
	for _, tc := range []struct {
		name string
		in   []byte
		si   core.AudioTrack
	}{
		{"bad sync", corrupt(first, 0, 0xFE), si},
		{"reserved bit set", corrupt(first, 1, 0xFA), si},
		{"reserved block size code", corrupt(first, 2, 0x09), si},
		{"invalid sample rate code", corrupt(first, 2, 0x5F), si},
		{"reserved channel code", corrupt(first, 3, 0xB8), si},
		{"reserved sample size code", corrupt(first, 3, 0x86), si},
		{"reserved low bit set", corrupt(first, 3, 0x89), si},
		{"bad CRC", corrupt(first, 5, 0x8B), si},
		{"bad coded-number continuation", []byte{0xFF, 0xF8, 0x59, 0x88, 0xC2, 0xFF, 0x00}, si},
		{"sample rate disagrees with STREAMINFO", first, other},
		{"channels disagree with STREAMINFO", first, mono},
		{"bit depth disagrees with STREAMINFO", first, deep},
		{"header cut short", first[:4], si},
		{"block size beyond STREAMINFO max", overBlock, si},
	} {
		if got, ok := decodeFrameHeader(tc.in, tc.si); ok {
			t.Errorf("%s: accepted as %+v, want rejection", tc.name, got)
		}
	}
}

// codedNumber encodes v in FLAC's UTF-8-style extended form (the test-side
// inverse of decodeCodedNumber).
func codedNumber(v uint64) []byte {
	if v < 0x80 {
		return []byte{byte(v)}
	}
	n := 2
	for caps := uint(11); v >= 1<<caps; caps += 5 {
		n++
	}
	out := make([]byte, n)
	out[0] = byte(0xFF<<(8-n)) | byte(v>>(6*(n-1)))
	for i := 1; i < n; i++ {
		out[i] = 0x80 | byte(v>>(6*(n-1-i)))&0x3F
	}
	return out
}

// synthTailFrame builds one 16-bit stereo frame of block constant samples,
// with an explicit 16-bit block size, 44.1 kHz rate code, and valid CRCs.
func synthTailFrame(num uint64, block int, variable bool, val uint16) []byte {
	fr := []byte{0xFF, 0xF8, 0x79, 0x18}
	if variable {
		fr[1] |= 1
	}
	fr = append(fr, codedNumber(num)...)
	fr = append(fr, byte((block-1)>>8), byte(block-1))
	fr = append(fr, crc8(0, fr))
	for range 2 {
		fr = append(fr, 0x00, byte(val>>8), byte(val)) // constant subframe
	}
	c := crc16(0, fr)
	return append(fr, byte(c>>8), byte(c))
}

// synthTailAudio builds an audio region covering total samples in blocks of
// block, the last frame short when total is not a multiple.
func synthTailAudio(total, block int, variable bool, val uint16) []byte {
	var out []byte
	for start := 0; start < total; start += block {
		b := min(block, total-start)
		num := uint64(start / block)
		if variable {
			num = uint64(start)
		}
		out = append(out, synthTailFrame(num, b, variable, val)...)
	}
	return out
}

// TestAudioEnd drives the tail walk over synthetic audio regions: a clean stream's audio ends with
// the region, appended bytes leave the end where the last frame ends, missing audio (whole frames or
// a cut inside the final frame) is a truncation whose audio runs to the region's end, and a stream
// the walk cannot place has no known end.
func TestAudioEnd(t *testing.T) {
	const total, block = 44100, 4608
	si := fixtureInfo()
	clean := synthTailAudio(total, block, false, 0x1234)
	cleanVar := synthTailAudio(total, block, true, 0x1234)
	syncPayload := synthTailAudio(total, block, false, 0xFFF8)
	noTotal := si
	noTotal.TotalSamples = 0

	// The growth case: enough junk that the first 64 KiB window holds no frame header, forcing a
	// wider read. Zeros do not force it, since the walk measures a zero run before its window.
	bigTotal := 1000 * block
	bigSi := si
	bigSi.TotalSamples = uint64(bigTotal)
	bigClean := synthTailAudio(bigTotal, block, false, 0x1234)

	// A constant sample value whose final frame's CRC-16 ends in a zero byte:
	// the residue run then reaches one byte into the trailer, which the frame
	// end pick must not mistake for one byte of junk.
	zeroTailVal := func() uint16 {
		for v := 0; ; v++ {
			fr := synthTailFrame(9, total-9*block, false, uint16(v))
			if fr[len(fr)-1] == 0x00 {
				return uint16(v)
			}
		}
	}()
	zeroTail := synthTailAudio(total, block, false, zeroTailVal)

	// STREAMINFO totals smaller than the frames present: the stream overruns
	// its declaration, so neither verdict is safe. 39492 lands inside frame 8;
	// 8*block lands exactly on frame 8's start, the shape that would otherwise
	// read the tail frames as junk.
	underSi := si
	underSi.TotalSamples = uint64(7*block + 4000)
	underBoundarySi := si
	underBoundarySi.TotalSamples = uint64(9 * block)

	// A well-formed variable-strategy header planted inside junk large enough that the first window
	// sees only it: one uncorroborated header must not end the search, and the end must still be the
	// last frame's.
	falseHdr := []byte{0xFF, 0xF9, 0xC9, 0x18, 0xCF, 0xA8}
	falseHdr = append(falseHdr, crc8(0, falseHdr))
	bigJunk := bytes.Repeat([]byte{0x4A}, 130<<10)
	copy(bigJunk[100<<10:], falseHdr)

	// Deterministic non-zero junk (a tiny LCG), the shape real appended tags
	// and text take, unlike the constant fills.
	noisyJunk := make([]byte, 4096)
	x := uint32(1)
	for i := range noisyJunk {
		x = x*1103515245 + 12345
		noisyJunk[i] = byte(x >> 16)
	}

	whole8, whole1 := frameOffset(clean, si, 8), frameOffset(clean, si, 1)
	for _, tc := range []struct {
		name      string
		audio     []byte
		si        core.AudioTrack
		want      int // where the audio ends, -1 when the walk cannot tell
		truncated bool
	}{
		{name: "clean fixed", audio: clean, si: si, want: len(clean)},
		{name: "clean variable", audio: cleanVar, si: si, want: len(cleanVar)},
		{name: "payload full of sync bytes", audio: syncPayload, si: si, want: len(syncPayload)},
		{name: "trailing zeros", audio: filled(clean, 0, 4096), si: si, want: len(clean)},
		{name: "trailing junk", audio: filled(clean, 0x4A, 4096), si: si, want: len(clean)},
		{name: "zeros wider than the first window", audio: filled(bigClean, 0, 100<<10), si: bigSi, want: len(bigClean)},
		{name: "junk wider than the first window", audio: filled(bigClean, 0x4A, 100<<10), si: bigSi, want: len(bigClean)},
		{name: "whole frames cut", audio: clean[:whole8], si: si, want: whole8, truncated: true},
		{name: "cut inside the final frame", audio: clean[:len(clean)-5], si: si, want: len(clean) - 5, truncated: true},
		{name: "single surviving frame", audio: clean[:whole1], si: si, want: whole1, truncated: true},
		{name: "unknown total samples", audio: filled(clean, 0, 4096), si: noTotal, want: -1},
		{name: "final frame CRC ends in zero byte", audio: zeroTail, si: si, want: len(zeroTail)},
		// The trailer's zero byte joins the zeros after it, so the end falls a byte early, as it
		// does with all of the run in the window.
		{name: "final frame CRC ends in zero byte, then zeros", audio: filled(zeroTail, 0, 4096), si: si, want: len(zeroTail) - 1},
		{name: "stream overruns declared total", audio: clean, si: underSi, want: -1},
		{name: "overrun with declared total on a frame boundary", audio: clean, si: underBoundarySi, want: -1},
		{name: "false header inside large junk", audio: slices.Concat(clean, bigJunk), si: si, want: len(clean)},
		{name: "noisy junk", audio: slices.Concat(clean, noisyJunk), si: si, want: len(clean)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end, ws, err := audioEnd(context.Background(), core.BytesSource(tc.audio), 0, int64(len(tc.audio)), tc.si, bits.DefaultLimits.MaxAllocBytes)
			if err != nil {
				t.Fatalf("audioEnd: %v", err)
			}
			if end != int64(tc.want) {
				t.Errorf("audio end = %d, want %d", end, tc.want)
			}
			if !tc.truncated {
				if len(ws) != 0 {
					t.Fatalf("want no warnings, got %v", ws)
				}
				return
			}
			if len(ws) != 1 || ws[0].Code != core.WarnTruncatedAudio || !strings.Contains(ws[0].Message, "STREAMINFO") {
				t.Fatalf("want one truncated-audio warning naming STREAMINFO, got %v", ws)
			}
		})
	}
}

// filled returns audio followed by n bytes of fill.
func filled(audio []byte, fill byte, n int) []byte {
	return slices.Concat(audio, bytes.Repeat([]byte{fill}, n))
}

// Zeros appended past the window cap. The walk measures the run of zeros before the tags backward in
// 64 KiB reads, as many as the allocation limit holds, and opens its window just inside the run, so
// the end is the last frame's however long the run, within that bound. A run filling the bound with
// bytes still before it stays audio, warned; one reaching the start of the frames leaves no stream.
func TestAudioEndZerosBeyondCap(t *testing.T) {
	si := fixtureInfo()
	clean := synthTailAudio(44100, 4608, false, 0x1234)
	const bound = 1 << 20
	defaultLimit := bits.DefaultLimits.MaxAllocBytes
	for _, tc := range []struct {
		name  string
		audio []byte
		limit int64
		want  int   // where the audio ends, -1 when the walk cannot tell
		warn  int64 // the scan bound a warning names, 0 for no warning
	}{
		{"5 MiB of zeros", filled(clean, 0, 5<<20), defaultLimit, len(clean), 0},
		{"8 MiB of zeros", filled(clean, 0, 8<<20), defaultLimit, len(clean), 0},
		{"zeros just inside the scan bound", filled(clean, 0, bound-1), bound, len(clean), 0},
		{"zeros filling the scan bound", filled(clean, 0, bound), bound, -1, bound},
		{"zeros past the scan bound", filled(clean, 0, 2*bound), bound, -1, bound},
		// The bound is whole 64 KiB reads, so under a limit between two multiples it is the lower one.
		{"zeros past the bound of a 100 KiB limit", filled(clean, 0, 70<<10), 100 << 10, -1, 64 << 10},
		{"nothing but zeros", make([]byte, 100<<10), defaultLimit, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end, ws, err := audioEnd(context.Background(), core.BytesSource(tc.audio), 0, int64(len(tc.audio)), si, tc.limit)
			if err != nil {
				t.Fatalf("audioEnd: %v", err)
			}
			if end != int64(tc.want) {
				t.Errorf("audio end = %d, want %d", end, tc.want)
			}
			switch want := fmt.Sprintf("%d byte(s) at the end of the FLAC stream are zeros in a run at least as long as the %d-byte scan bound", tc.warn, tc.warn); {
			case tc.warn == 0 && len(ws) != 0:
				t.Errorf("want no warnings, got %v", ws)
			case tc.warn > 0 && (len(ws) != 1 || ws[0].Code != core.WarnTrailingBytes || !strings.HasPrefix(ws[0].Message, want)):
				t.Errorf("want one trailing-bytes warning beginning %q, got %v", want, ws)
			}
		})
	}
}

// A read the walk needs over the allocation limit is refused, not narrowed: the zero scan's read under
// a limit below it, and a window step past the limit when the first window cannot place the end.
func TestAudioEndRefusesOverTheLimit(t *testing.T) {
	clean := synthTailAudio(44100, 4608, false, 0x1234)
	for _, tc := range []struct {
		name  string
		audio []byte
		limit int64
	}{
		{"zero scan read", filled(clean, 0, 20<<10), 16 << 10},
		{"window step", filled(clean, 0x4A, 100<<10), 64 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := audioEnd(context.Background(), core.BytesSource(tc.audio), 0, int64(len(tc.audio)), fixtureInfo(), tc.limit)
			if !errors.Is(err, waxerr.ErrSizeTooLarge) {
				t.Errorf("err = %v, want declared size too large", err)
			}
		})
	}
}

// counting counts the reads made of r and the bytes they ask for; after, when set, runs after each,
// and the read numbered fail, when set, fails as a device error does.
type counting struct {
	r     core.ReaderAtSized
	reads int
	bytes int64
	after func(reads int)
	fail  int
}

func (c *counting) Size() int64 { return c.r.Size() }

func (c *counting) ReadAt(p []byte, off int64) (int, error) {
	c.reads++
	c.bytes += int64(len(p))
	if c.reads == c.fail {
		return 0, syscall.EIO
	}
	n, err := c.r.ReadAt(p, off)
	if c.after != nil {
		c.after(c.reads)
	}
	return n, err
}

// The walk reads only inside the file, so a read there that fails is an I/O fault: the walk returns
// it, in the zero scan and in each window alike, rather than leave the end unknown. Bytes past the
// end of the file, where a boundary beyond it puts the windows, are absent and place no end.
func TestAudioEndFailedRead(t *testing.T) {
	clean := synthTailAudio(44100, 4608, false, 0x1234)
	for _, tc := range []struct {
		name  string
		audio []byte
		fail  int // the read that fails
	}{
		{"zero scan", filled(clean, 0, 1<<20), 2},
		{"first window", filled(clean, 0x4A, 5<<20), 2},
		{"a wider window", filled(clean, 0x4A, 5<<20), 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &counting{r: core.BytesSource(tc.audio), fail: tc.fail}
			_, _, err := audioEnd(context.Background(), src, 0, int64(len(tc.audio)), fixtureInfo(), bits.DefaultLimits.MaxAllocBytes)
			if !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("err = %v, want the read's own failure", err)
			}
			if src.reads != tc.fail {
				t.Errorf("%d reads, want %d: the walk went on past the failure", src.reads, tc.fail)
			}
		})
	}
	src := core.BytesSource(clean)
	if end, ws, err := audioEnd(context.Background(), src, 0, int64(len(clean))+100, fixtureInfo(), bits.DefaultLimits.MaxAllocBytes); end != -1 || len(ws) != 0 || err != nil {
		t.Errorf("a boundary past the end of the file: %d, %v, %v; want -1 and no error", end, ws, err)
	}
}

// The walk's reads are bounded whatever follows the frames: a zero run longer than the scan bound
// costs the bound's 64 KiB reads and opens no window, junk past the window cap costs one zero-scan
// read and the four windows up to the cap, and a long zero run within the bound one read per 64 KiB
// of it, one more that finds where it starts, and one window.
func TestAudioEndBoundsItsReads(t *testing.T) {
	clean := synthTailAudio(44100, 4608, false, 0x1234)
	n := int64(len(clean))
	for _, tc := range []struct {
		name  string
		audio []byte
		limit int64
		reads int
		bytes int64
	}{
		{"zero run past the bound", filled(clean, 0, 2<<20), 1 << 20, 16, 1 << 20},
		{"junk past the window cap", filled(clean, 0x4A, 5<<20), bits.DefaultLimits.MaxAllocBytes,
			5, 64<<10 + 64<<10 + 256<<10 + 1<<20 + 4<<20},
		{"5 MiB of zeros", filled(clean, 0, 5<<20), bits.DefaultLimits.MaxAllocBytes,
			82, 5<<20 + n + n + maxFrameHeader},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &counting{r: core.BytesSource(tc.audio)}
			if _, _, err := audioEnd(context.Background(), src, 0, int64(len(tc.audio)), fixtureInfo(), tc.limit); err != nil {
				t.Fatalf("audioEnd: %v", err)
			}
			if src.reads != tc.reads || src.bytes != tc.bytes {
				t.Errorf("%d reads of %d bytes, want %d of %d", src.reads, src.bytes, tc.reads, tc.bytes)
			}
		})
	}
}

// The walk checks the context before each read: cancelled before it starts it reads nothing, and
// cancelled during the zero scan or the window growth it makes no further read and returns the
// context's error, so a cancelled parse cannot succeed with a different audio extent.
func TestAudioEndChecksTheContext(t *testing.T) {
	clean := synthTailAudio(44100, 4608, false, 0x1234)
	for _, tc := range []struct {
		name   string
		audio  []byte
		cancel int // the read after which the context is cancelled; 0 cancels before the walk
	}{
		{"before the walk", clean, 0},
		{"during the zero scan", filled(clean, 0, 1<<20), 3},
		{"during the window growth", filled(clean, 0x4A, 5<<20), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			src := &counting{r: core.BytesSource(tc.audio), after: func(reads int) {
				if reads == tc.cancel {
					cancel()
				}
			}}
			if tc.cancel == 0 {
				cancel()
			}
			_, _, err := audioEnd(ctx, src, 0, int64(len(tc.audio)), fixtureInfo(), bits.DefaultLimits.MaxAllocBytes)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want the context's", err)
			}
			if src.reads != tc.cancel {
				t.Errorf("%d reads, want %d", src.reads, tc.cancel)
			}
		})
	}
}

// frameOffset returns the byte offset of frame n in a synthetic fixed-strategy
// audio region, located by re-walking the frame headers.
func frameOffset(audio []byte, si core.AudioTrack, n int) int {
	off := 0
	for range n {
		h, ok := decodeFrameHeader(audio[off:], si)
		if !ok {
			panic("frameOffset: synthetic region does not decode")
		}
		// header + two constant subframes + CRC-16
		off += h.size + 2*3 + 2
	}
	return off
}

// CRC vectors: the standard "123456789" check values for CRC-8 (poly 0x07) and
// CRC-16 (poly 0x8005, unreflected), plus the header of testdata/sample.flac's
// first real frame.
func TestFrameCRC8(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want uint8
	}{
		{"empty", nil, 0x00},
		{"check string", []byte("123456789"), 0xF4},
		{"real frame header", []byte{0xFF, 0xF8, 0x59, 0x88, 0x00}, 0x8A},
	} {
		if got := crc8(0, tc.in); got != tc.want {
			t.Errorf("%s: crc8 = %#02x, want %#02x", tc.name, got, tc.want)
		}
	}
}

func TestFrameCRC16(t *testing.T) {
	if got := crc16(0, []byte("123456789")); got != 0xFEE8 {
		t.Errorf("crc16 = %#04x, want 0xFEE8", got)
	}
	// Incremental updates must equal the one-shot value so a chunked scan can
	// roll the CRC across reads.
	whole := crc16(0, []byte("123456789"))
	split := crc16(crc16(0, []byte("1234")), []byte("56789"))
	if whole != split {
		t.Errorf("chunked crc16 = %#04x, one-shot = %#04x", split, whole)
	}
}
