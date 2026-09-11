package aiff

import (
	"encoding/binary"
	"math"

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

// buildTrack assembles audio properties from the COMM geometry and the SSND audio bytes
// present. COMM's numSampleFrames is the declared packet count, and for a layout the
// reader knows it is capped by the packets those bytes can hold, whether the file is
// truncated or merely overstates, so the reported length is never audio the file does
// not carry: the WAV rule, where the data length is the truth. ffprobe trusts the
// declared count instead and reads a 23 KB ima4 file as 64 seconds when a writer stored
// frames where QuickTime stores packets. c.numFrames itself is left untouched so the
// writer's COMM bytes stay verbatim.
//
// The bitrate is the stream's nominal rate for a known layout, the figure ffprobe reports
// for an AIFF and WAV reports for PCM: a per-packet constant, so a truncated file keeps
// it, and a header fact, so an empty one does too, as WAV's does (the renderers show it
// only beside a duration). A layout the reader cannot size reports none. Its count is
// reported as the file's own statement of length, in whatever unit that writer meant, but
// a bitrate would be this reader's arithmetic on top of it, and for the one writer known
// to produce such files QuickTime stores packets there, so an average over the bytes would
// be off by the packet size.
func buildTrack(c commChunk, audioBytes int64) core.AudioTrack {
	// Cap the conversions so a hostile COMM value cannot overflow into a negative
	// property on a 32-bit platform. Real geometry is far below the cap.
	rate := int(min(int64(c.sampleRate), math.MaxInt32))
	packets := uint64(c.numFrames)
	t := core.AudioTrack{
		Codec:         codecName(c),
		SampleRate:    rate,
		Channels:      int(c.channels),
		BitsPerSample: c.sampleWidth(),
		TotalSamples:  packets,
	}
	if fpp, bpp, known := c.packet(); known {
		packets = min(packets, c.packetsPresent(audioBytes))
		// packets is at most 2^32-1 and fpp at most 64, so the product fits a uint64.
		t.TotalSamples = packets * uint64(fpp)
		// rate*bpp fits an int64 for any COMM (a ~4 GHz rate times 65535 channels of a
		// 65535-bit width), but the *8 below would not, so stage a cap that leaves room
		// for it and the rounding term. It engages only past 5e17 bytes per second, whose
		// bitrate saturates the final cap regardless, so no representable figure is ever
		// distorted. The division rounds to nearest, as ffprobe's does, so a rate the
		// packet does not divide evenly reports the same figure.
		bytesPerSec := min(int64(c.sampleRate)*bpp, math.MaxInt64/16)
		t.Bitrate = int(min((bytesPerSec*8+fpp/2)/fpp, math.MaxInt32))
	}
	t.Duration = core.SamplesToDuration(t.TotalSamples, rate)
	return t
}

// packetsPresent reports how many whole packets the SSND audio bytes hold, for a layout
// the reader knows; 0 for one it does not.
func (c commChunk) packetsPresent(audioBytes int64) uint64 {
	if _, bpp, known := c.packet(); known {
		return uint64(max(audioBytes, 0) / bpp)
	}
	return 0
}

// overstates reports whether COMM declares more packets than the SSND audio bytes hold,
// for a file that holds some: the reported length is then the chunk's, not COMM's, and
// the reader says so. No audio at all is the no-audio-frames condition instead.
func (c commChunk) overstates(audioBytes int64) bool {
	if _, _, known := c.packet(); !known || audioBytes <= 0 {
		return false
	}
	return c.packetsPresent(audioBytes) < uint64(c.numFrames)
}

// overstatedMessage is the truncated-audio warning text for a COMM that overstates.
const overstatedMessage = "COMM declares more sample frames than the SSND chunk holds; the length reported is the chunk's"

// sampleWidth is the stored width of one sample: the width the compression type fixes
// when it names one (4 bits for ima4, 8 for alaw/ulaw, 24 for in24, 32 for in32 and
// fl32, 64 for fl64), since QuickTime writes the decoded 16 into COMM's sampleSize for
// those types, else COMM's sampleSize.
func (c commChunk) sampleWidth() int {
	if c.isAIFC {
		if layout, _ := core.FourccSampleLayout(string(c.compType[:])); layout.Depth > 0 {
			return layout.Depth
		}
	}
	return int(c.sampleSize)
}

// packet reports how many sample frames one packet decodes to and how many SSND bytes it
// occupies across every channel; COMM's numSampleFrames counts packets. Plain AIFF and
// the all-zero type a COMM too short to carry one leaves behind are byte-linear PCM at
// COMM's width; an AIFF-C type takes its layout from the shared table, a packetized one
// with its own frame and byte counts, a byte-linear one at the stored width rounded up to
// whole bytes. A type the table does not know, or a geometry with no channels or no width,
// reports ok=false: the declared count is then taken as frames and no bitrate is derived.
func (c commChunk) packet() (framesPerPacket, bytesPerPacket int64, ok bool) {
	channels := int64(c.channels)
	if channels == 0 {
		return 0, 0, false
	}
	layout := core.SampleLayout{FramesPerPacket: 1}
	if comp := string(c.compType[:]); c.isAIFC && comp != "\x00\x00\x00\x00" {
		var known bool
		if layout, known = core.FourccSampleLayout(comp); !known {
			return 0, 0, false
		}
	}
	if layout.PacketBytes > 0 {
		return int64(layout.FramesPerPacket), int64(layout.PacketBytes) * channels, true
	}
	width := int64(c.sampleWidth())
	if width == 0 {
		return 0, 0, false
	}
	return 1, channels * ((width + 7) / 8), true
}

// codecName names the audio codec as the container spells it; the central
// core.CanonicalCodec folds that spelling into the canonical name with the raw one kept as
// the profile. Plain AIFF is always signed big-endian PCM. AIFF-C reports its compression
// type as the bare fourcc, so "sowt", "ima4" or ".mp3" read exactly as they do from a
// .mov, codec and profile alike, rather than through a second table of names that could
// drift from the shared one. Two spellings are not names: the all-zero type a COMM too
// short to carry one leaves behind is the AIFF-C default, PCM, and a QuickTime "ms" +
// WAVE-format-tag type names its codec through the table WAV and WMA read that tag with,
// the rule the MP4 reader applies to the same fourcc.
func codecName(c commChunk) string {
	if !c.isAIFC {
		return "PCM"
	}
	comp := string(c.compType[:])
	if comp == "\x00\x00\x00\x00" {
		return "PCM"
	}
	if tag, ok := core.QuickTimeWaveFormatTag(comp); ok {
		return core.WaveFormatCodec(tag)
	}
	return printable4CC(c.compType)
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
