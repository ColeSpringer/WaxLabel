package aiff

import (
	"encoding/binary"
	"math"

	"github.com/colespringer/waxlabel/internal/core"
)

// parseCOMM decodes COMM. First 18 bytes are AIFF fields; AIFF-C adds a 4-byte
// compression type (pascal name ignored). isAIFC comes from the FORM type.
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

// decodeSampleRate converts AIFF's 80-bit SANE extended float to a whole-number
// rate. Mantissa integer bit is explicit. Out-of-range / Inf / NaN / non-positive -> 0.
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
		// Zero mantissa is zero for any exponent. Also avoids 0*+Inf=NaN from Ldexp
		// overflow (NaN would slip past decodeSampleRate's range check).
		return 0
	case exp == 0x7FFF:
		return 0 // Inf or NaN - not a usable rate
	}
	// value = sign * mantissa * 2^(exp - 16383 - 63); 63 for explicit integer bit.
	return sign * float64(mant) * math.Ldexp(1, exp-16383-63)
}

// buildTrack builds properties from COMM and SSND bytes present. For known layouts,
// numSampleFrames (packet count) is capped by packets SSND can hold so length never
// exceeds carried audio (WAV data-length rule). c.numFrames left untouched for rewrite.
// Bitrate is nominal per-packet for known layouts; unknown layouts report none.
func buildTrack(c commChunk, audioBytes int64) core.AudioTrack {
	// Cap so hostile COMM values cannot overflow to negative on 32-bit.
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
		t.TotalSamples = packets * uint64(fpp)
		// Cap before *8 so rate*bpp cannot overflow; round to nearest like ffprobe.
		bytesPerSec := min(int64(c.sampleRate)*bpp, math.MaxInt64/16)
		t.Bitrate = int(min((bytesPerSec*8+fpp/2)/fpp, math.MaxInt32))
	}
	t.Duration = core.SamplesToDuration(t.TotalSamples, rate)
	return t
}

// packetsPresent: whole packets SSND holds for a known layout; else 0.
func (c commChunk) packetsPresent(audioBytes int64) uint64 {
	if _, bpp, known := c.packet(); known {
		return uint64(max(audioBytes, 0) / bpp)
	}
	return 0
}

// overstates: COMM declares more packets than SSND holds (and SSND is non-empty).
func (c commChunk) overstates(audioBytes int64) bool {
	if _, _, known := c.packet(); !known || audioBytes <= 0 {
		return false
	}
	return c.packetsPresent(audioBytes) < uint64(c.numFrames)
}

// overstatedMessage is the warning when COMM overstates.
const overstatedMessage = "COMM declares more sample frames than the SSND chunk holds; the length reported is the chunk's"

// sampleWidth: compression-fixed depth when known (QuickTime often writes 16 into
// COMM sampleSize), else COMM's sampleSize.
func (c commChunk) sampleWidth() int {
	if c.isAIFC {
		if layout, _ := core.FourccSampleLayout(string(c.compType[:])); layout.Depth > 0 {
			return layout.Depth
		}
	}
	return int(c.sampleSize)
}

// packet: frames and SSND bytes per packet (numSampleFrames counts packets).
// Plain AIFF / zero type: byte-linear PCM. AIFF-C: shared fourcc table.
// Unknown layout or zero channels/width: ok=false (count as frames, no bitrate).
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

// codecName: container spelling ([core.CanonicalCodec] folds it). Plain AIFF is PCM.
// AIFF-C uses bare fourcc (same as .mov). Zero type -> PCM; "ms"+WAVE tag via WaveFormatCodec.
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

// printable4CC replaces non-printable 4CC bytes with '?'.
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
