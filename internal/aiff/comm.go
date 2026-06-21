package aiff

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
)

// parseCOMM decodes a "COMM" common chunk. The first 18 bytes are the AIFF
// common fields (channels, sample-frame count, sample size, 80-bit sample rate);
// an AIFF-C chunk carries a 4-byte compression type after them (and then a
// pascal-string compression name, which is not needed here). isAIFC comes from
// the FORM type, since a plain AIFF COMM is exactly 18 bytes.
func parseCOMM(b []byte, isAIFC bool) (commChunk, bool) {
	if len(b) < 18 {
		return commChunk{}, false
	}
	var c commChunk
	c.channels = binary.BigEndian.Uint16(b[0:2])
	c.numFrames = binary.BigEndian.Uint32(b[2:6])
	c.sampleSize = binary.BigEndian.Uint16(b[6:8])
	copy(c.rateBytes[:], b[8:18])
	c.sampleRate = decodeSampleRate(c.rateBytes[:])
	c.isAIFC = isAIFC
	if isAIFC && len(b) >= 22 {
		copy(c.compType[:], b[18:22])
	}
	return c, true
}

// decodeSampleRate converts AIFF's 80-bit sample-rate field to a whole-number
// rate. The field is an 80-bit IEEE 754 extended-precision float (the big-endian
// SANE "extended" format): a sign bit, a 15-bit biased exponent, and a 64-bit
// mantissa whose integer bit is *explicit* (unlike an IEEE double's implicit
// one). Out-of-range, infinite, NaN, or non-positive values yield 0 - a sample
// rate that nonsensical is treated as unknown rather than wrapped on conversion.
func decodeSampleRate(b []byte) uint32 {
	f := extended80ToFloat(b)
	if f <= 0 || f >= float64(math.MaxUint32) {
		return 0
	}
	return uint32(math.Round(f))
}

// extended80ToFloat decodes a 10-byte 80-bit extended-precision float.
func extended80ToFloat(b []byte) float64 {
	if len(b) < 10 {
		return 0
	}
	sign := 1.0
	if b[0]&0x80 != 0 {
		sign = -1.0
	}
	exp := int(b[0]&0x7f)<<8 | int(b[1])
	mant := binary.BigEndian.Uint64(b[2:10])
	switch {
	case mant == 0:
		// A zero mantissa is zero for any exponent. Returning here also avoids the
		// degenerate 0 * +Inf = NaN when a malformed huge exponent overflows Ldexp:
		// a NaN would slip past decodeSampleRate's range check (every NaN comparison
		// is false) and cast to a platform-dependent uint32.
		return 0
	case exp == 0x7FFF:
		return 0 // Inf or NaN - not a usable rate
	}
	// value = sign * mantissa * 2^(exp - bias - 63), bias = 16383, and the 63
	// accounts for the mantissa's explicit integer bit weighting (bit 63).
	return sign * float64(mant) * math.Ldexp(1, exp-16383-63)
}

// buildTrack assembles audio properties from the COMM geometry and an effective
// sample-frame count. AIFF records the frame count directly in COMM, so the caller
// normally passes c.numFrames; for a truncated SSND of a constant-frame-size
// encoding it passes the smaller count the surviving bytes actually hold (C3), so
// the reported duration matches what is decodable - the WAV behavior, where the
// duration follows the present data length. c.numFrames itself is left untouched so
// the writer's COMM bytes stay verbatim.
func buildTrack(c commChunk, frames uint32) core.AudioTrack {
	t := core.AudioTrack{
		Codec: codecName(c),
		// Cap the conversions so a hostile COMM value cannot overflow into a
		// negative property on a 32-bit platform. Real geometry is far below the cap.
		SampleRate:    int(min(int64(c.sampleRate), math.MaxInt32)),
		Channels:      int(c.channels),
		BitsPerSample: int(c.sampleSize),
		TotalSamples:  uint64(frames),
	}
	if c.sampleRate > 0 {
		secs := float64(frames) / float64(c.sampleRate)
		if secs > 0 && secs < float64(math.MaxInt64)/float64(time.Second) {
			t.Duration = time.Duration(secs * float64(time.Second))
		}
		// Stage the MaxInt32 cap so neither multiply overflows int64 first: a corrupt
		// COMM could pair a ~4 GHz rate with 65535 channels and bit depth, whose raw
		// three-way product exceeds int64 and would wrap negative past a single
		// trailing min (the first product alone stays well within int64).
		bitrate := min(int64(c.sampleRate)*int64(c.channels), math.MaxInt32)
		t.Bitrate = int(min(bitrate*int64(c.sampleSize), math.MaxInt32))
	}
	return t
}

// constantFrameSize reports whether the encoding stores a fixed number of bytes per
// sample frame, so a present-byte count maps linearly to a frame count. True for
// plain AIFF (always PCM) and for the uncompressed AIFF-C encodings - PCM (NONE /
// twos / sowt) and IEEE float (fl32/FL32, fl64/FL64). It is deliberately false for
// the compressed AIFF-C types (ima4 ADPCM has no constant frame size; alaw/ulaw
// store one byte per sample while COMM's sampleSize is the *decoded* width, so the
// byte-to-frame math would be wrong), where COMM's declared numFrames is kept.
func (c commChunk) constantFrameSize() bool {
	if !c.isAIFC {
		return true
	}
	switch string(c.compType[:]) {
	case "NONE", "twos", "sowt", "\x00\x00\x00\x00", "fl32", "FL32", "fl64", "FL64":
		return true
	}
	return false
}

// frameSize returns the bytes per sample frame for a constant-frame-size encoding:
// channels times the sample width rounded up to whole bytes. It is meaningful only
// when [commChunk.constantFrameSize] holds; the caller guards on frameSize > 0. The
// round-up is done in int64 so a pathological 16-bit sampleSize cannot wrap before
// the divide.
func (c commChunk) frameSize() int64 {
	return int64(c.channels) * ((int64(c.sampleSize) + 7) / 8)
}

// codecName names the audio codec. Plain AIFF is always signed big-endian PCM;
// AIFF-C names the codec from the COMM compression type, the common ones mapped
// and the rest reported by their 4CC.
func codecName(c commChunk) string {
	if !c.isAIFC {
		return "PCM"
	}
	switch string(c.compType[:]) {
	case "NONE", "twos", "\x00\x00\x00\x00":
		return "PCM"
	case "sowt":
		return "PCM (little-endian)"
	case "fl32", "FL32":
		return "IEEE float"
	case "fl64", "FL64":
		return "IEEE float64"
	case "alaw", "ALAW":
		return "A-law"
	case "ulaw", "ULAW":
		return "mu-law"
	case "ima4":
		return "IMA ADPCM"
	default:
		return "AIFF-C " + printable4CC(c.compType)
	}
}

// printable4CC renders a compression-type 4CC, replacing non-printable bytes so
// a hostile or unusual type does not produce control characters in a codec name.
func printable4CC(id [4]byte) string {
	out := make([]byte, 4)
	for i, b := range id {
		if b >= 0x20 && b < 0x7f {
			out[i] = b
		} else {
			out[i] = '?'
		}
	}
	return string(out)
}
