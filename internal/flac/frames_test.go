package flac

import (
	"context"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// fixtureInfo mirrors testdata/sample.flac's STREAMINFO: 44.1 kHz, stereo,
// 16-bit, fixed 4608-sample blocks.
func fixtureInfo() core.AudioTrack {
	return core.AudioTrack{
		SampleRate: 44100, Channels: 2, BitsPerSample: 16,
		MinBlockSize: 4608, MaxBlockSize: 4608, TotalSamples: 44100,
	}
}

// TestDecodeFrameHeader drives the header decoder with the two real headers
// from testdata/sample.flac (frame 0, and the short last frame whose block
// size is 16-bit coded) plus a hand-built variable-strategy header, and with
// corruptions of each field that must be rejected.
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
	for ch := 0; ch < 2; ch++ {
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

// TestFrameTailWarnings drives the tail walk over synthetic audio regions:
// clean streams stay silent, appended bytes are reported as a trailing region
// with their exact count, and missing audio (whole frames or a cut inside the
// final frame) is reported as a truncation.
func TestFrameTailWarnings(t *testing.T) {
	const total, block = 44100, 4608
	si := fixtureInfo()
	clean := synthTailAudio(total, block, false, 0x1234)
	cleanVar := synthTailAudio(total, block, true, 0x1234)
	junk := func(audio []byte, n int) []byte {
		return append(append([]byte(nil), audio...), make([]byte, n)...)
	}
	noTotal := si
	noTotal.TotalSamples = 0

	// The growth case: enough zero junk that the first 64 KiB window holds no
	// frame header, forcing a wider read.
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

	// STREAMINFO totals smaller than the frames present: the stream overruns
	// its declaration, so neither verdict is safe. 39492 lands inside frame 8;
	// 8*block lands exactly on frame 8's start, the shape that would otherwise
	// read the tail frames as junk.
	underSi := si
	underSi.TotalSamples = uint64(7*block + 4000)
	underBoundarySi := si
	underBoundarySi.TotalSamples = uint64(9 * block)

	// A well-formed variable-strategy header planted inside junk large enough
	// that the first window sees only it: one uncorroborated header must not
	// end the search, and the final count must still cover the whole region.
	falseHdr := []byte{0xFF, 0xF9, 0xC9, 0x18, 0xCF, 0xA8}
	falseHdr = append(falseHdr, crc8(0, falseHdr))
	bigJunk := make([]byte, 68<<10)
	copy(bigJunk[40000:], falseHdr)
	junkWithFalseHdr := append(append([]byte(nil), clean...), bigJunk...)

	// Deterministic non-zero junk (a tiny LCG), the shape real appended tags
	// and text take, unlike the all-zeros cases above.
	noisyJunk := make([]byte, 4096)
	x := uint32(1)
	for i := range noisyJunk {
		x = x*1103515245 + 12345
		noisyJunk[i] = byte(x >> 16)
	}

	for _, tc := range []struct {
		name      string
		audio     []byte
		si        core.AudioTrack
		wantCode  core.WarningCode
		wantInMsg string
		wantJunk  int64
	}{
		{name: "clean fixed", audio: clean, si: si},
		{name: "clean variable", audio: cleanVar, si: si},
		{name: "payload full of sync bytes", audio: synthTailAudio(total, block, false, 0xFFF8), si: si},
		{name: "trailing junk", audio: junk(clean, 4096), si: si,
			wantCode: core.WarnTrailingBytes, wantInMsg: "4096 byte(s) after the FLAC stream", wantJunk: 4096},
		{name: "junk wider than the first window", audio: junk(bigClean, 100<<10), si: bigSi,
			wantCode: core.WarnTrailingBytes, wantInMsg: "102400 byte(s)", wantJunk: 100 << 10},
		{name: "whole frames cut", audio: clean[:frameOffset(clean, si, 8)], si: si,
			wantCode: core.WarnTruncatedAudio, wantInMsg: "STREAMINFO"},
		{name: "cut inside the final frame", audio: clean[:len(clean)-5], si: si,
			wantCode: core.WarnTruncatedAudio, wantInMsg: "STREAMINFO"},
		{name: "single surviving frame", audio: clean[:frameOffset(clean, si, 1)], si: si,
			wantCode: core.WarnTruncatedAudio, wantInMsg: "STREAMINFO"},
		{name: "unknown total samples stays silent", audio: junk(clean, 4096), si: noTotal},
		{name: "final frame CRC ends in zero byte", audio: synthTailAudio(total, block, false, zeroTailVal), si: si},
		{name: "stream overruns declared total", audio: clean, si: underSi},
		{name: "overrun with declared total on a frame boundary", audio: clean, si: underBoundarySi},
		{name: "false header inside large junk", audio: junkWithFalseHdr, si: si,
			wantCode: core.WarnTrailingBytes, wantInMsg: "69632 byte(s)", wantJunk: 68 << 10},
		{name: "noisy junk", audio: append(append([]byte(nil), clean...), noisyJunk...), si: si,
			wantCode: core.WarnTrailingBytes, wantInMsg: "4096 byte(s)", wantJunk: 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &doc{audioEnd: int64(len(tc.audio)), streamInfo: tc.si}
			ws, gotJunk, err := frameTailWarnings(context.Background(), core.BytesSource(tc.audio), d, bits.DefaultLimits.MaxAllocBytes)
			if err != nil {
				t.Fatalf("frameTailWarnings: %v", err)
			}
			if gotJunk != tc.wantJunk {
				t.Errorf("junk length = %d, want %d", gotJunk, tc.wantJunk)
			}
			if tc.wantCode == 0 {
				if len(ws) != 0 {
					t.Fatalf("want no warnings, got %v", ws)
				}
				return
			}
			if len(ws) != 1 || ws[0].Code != tc.wantCode || !strings.Contains(ws[0].Message, tc.wantInMsg) {
				t.Fatalf("want one %v warning containing %q, got %v", tc.wantCode, tc.wantInMsg, ws)
			}
		})
	}
}

// TestFrameTailRespectsAllocLimit pins the walk's behavior under a
// MaxAllocBytes smaller than its windows: it stays silent (the junk keeps
// riding inside the audio extent, as before the walk existed) rather than
// exceeding the caller's read bound.
func TestFrameTailRespectsAllocLimit(t *testing.T) {
	audio := append(synthTailAudio(44100, 4608, false, 0x1234), make([]byte, 20<<10)...)
	d := &doc{audioEnd: int64(len(audio)), streamInfo: fixtureInfo()}
	ws, junk, err := frameTailWarnings(context.Background(), core.BytesSource(audio), d, 16<<10)
	if err != nil || len(ws) != 0 || junk != 0 {
		t.Fatalf("under a small alloc limit: ws=%v junk=%d err=%v, want silence", ws, junk, err)
	}
}

// TestFrameTailCancellation checks that a cancelled context surfaces as an
// error rather than as a silent no-findings result, so a cancelled parse
// cannot succeed with a different audio extent than an uncancelled one.
func TestFrameTailCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	audio := synthTailAudio(44100, 4608, false, 0x1234)
	d := &doc{audioEnd: int64(len(audio)), streamInfo: fixtureInfo()}
	if _, _, err := frameTailWarnings(ctx, core.BytesSource(audio), d, bits.DefaultLimits.MaxAllocBytes); err == nil {
		t.Fatal("cancelled context returned no error")
	}
}

// frameOffset returns the byte offset of frame n in a synthetic fixed-strategy
// audio region, located by re-walking the frame headers.
func frameOffset(audio []byte, si core.AudioTrack, n int) int {
	off := 0
	for i := 0; i < n; i++ {
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
