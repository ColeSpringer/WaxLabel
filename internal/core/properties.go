package core

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Properties describes the audio stream(s). Most files are single-track, but
// Matroska and MP4 can carry several, so Tracks is a slice.
type Properties struct {
	Container string
	Tracks    []AudioTrack
}

// AudioTrack is one audio stream's technical properties. The decoder-critical
// subset (sample rate, channels, bits per sample, and the FLAC block-size
// bounds) also feeds the audio-essence digest.
type AudioTrack struct {
	Index int
	// Codec is the canonical, container-neutral codec name (AAC, MP3, FLAC, Opus,
	// PCM, ALAC, ...), so the same codec reads identically whatever container it
	// arrived in. CodecProfile holds the container's own spelling when it carries
	// detail the canonical name drops - the MP4 fourcc "mp4a", the AAC object type
	// "AAC LC", the MPEG version+layer "MPEG-1 Layer 3" - and is empty when the raw
	// name was already canonical. Both are filled by [CanonicalCodec].
	Codec         string
	CodecProfile  string
	SampleRate    int
	Channels      int
	BitsPerSample int
	TotalSamples  uint64
	Duration      time.Duration
	Bitrate       int // average bits per second

	// FLAC STREAMINFO detail, preserved for fidelity and essence hashing.
	MinBlockSize int
	MaxBlockSize int
	MD5          [16]byte // MD5 of the decoded audio, per STREAMINFO

	// OutputGain is the decoder-applied output gain the stream header declares, as Opus
	// output_gain stores it: signed Q7.8 dB, 256 = +1 dB. Read from Ogg Opus only; the
	// OpusHead a Matroska A_OPUS CodecPrivate or an MP4 dOps box carries is not read, so
	// it reports 0 there and [Capabilities.OutputGain] grades those containers AccessNone.
	OutputGain int
}

// OutputGainDecibels converts a Q7.8 output gain to decibels. It is the single definition
// of the scale, so the string form and every machine-readable one agree.
func OutputGainDecibels(gain int) float64 { return float64(gain) / 256 }

// OutputGainDB renders a Q7.8 output gain as decibels, the unit a front-end speaks. Two
// decimals is the readable form ("-3.50 dB"), but the Q7.8 step is ~0.0039 dB, so more are
// emitted when the value needs them: a change line must never show an identical before and
// after for a gain that did move.
func OutputGainDB(gain int) string {
	s := strings.TrimRight(fmt.Sprintf("%.4f", OutputGainDecibels(gain)), "0")
	if n := strings.IndexByte(s, '.'); n >= 0 && len(s)-n < 3 {
		s = fmt.Sprintf("%.2f", OutputGainDecibels(gain))
	}
	return s + " dB"
}

// OutputGainUnsupportedMessage returns the drop warning text for a format WaxLabel writes
// no output gain to. It speaks of the write, not the container: an Opus stream muxed into
// Matroska or MP4 does carry a header gain, which this parser does not read or write.
func OutputGainUnsupportedMessage(f Format) string {
	return fmt.Sprintf("an output gain cannot be written to %s %s file; the gain was dropped",
		IndefiniteArticle(f.String()), f)
}

// AverageBitrate returns the average bits per second for audioBytes of encoded
// audio spread over secs seconds, or 0 when either input is non-positive. The
// result is capped below MaxInt32: a malformed file declaring a near-zero
// duration over a large audio extent would otherwise produce a value past the
// int range - an implementation-defined (garbage, possibly negative) cast on
// 32-bit platforms. Real audio bitrates are far below that ceiling, so the cap
// only suppresses nonsense. Every codec that derives an average bitrate shares
// this, so their handling of the degenerate cases cannot drift apart.
func AverageBitrate(audioBytes int64, secs float64) int {
	if audioBytes <= 0 || secs <= 0 {
		return 0
	}
	if bps := float64(audioBytes) * 8 / secs; bps < math.MaxInt32 {
		return int(bps)
	}
	return 0
}

// SamplesToDuration converts a PCM sample count at rate Hz into a duration,
// returning 0 for a non-positive rate and guarding the int64-nanosecond range
// against a pathological count (a malformed file's huge declared sample total). It
// is the single definition shared by every codec that derives a duration from a
// sample count (MP3's VBR frame count, the AAC ADTS walk, Ogg's granule span), so
// their degenerate-case handling cannot drift - the duration counterpart to
// [AverageBitrate].
func SamplesToDuration(samples uint64, rate int) time.Duration {
	if rate <= 0 {
		return 0
	}
	ns := float64(samples) / float64(rate) * float64(time.Second)
	if ns < 0 || ns >= math.MaxInt64 {
		return 0
	}
	return time.Duration(ns)
}

// CanonicalCodec splits a parser's raw codec name into the canonical,
// container-neutral name and the container-specific profile detail. The canonical
// name is what the same codec should read as in every container (so "mp4a",
// "AAC LC", and "AAC" all canonicalize to "AAC"); the profile is the raw name when
// it differs - preserving the fourcc / object-type / MPEG-version detail the
// canonical name drops - and "" when the raw name was already canonical. It is the
// single source of truth for codec naming, applied once after parse, so the text
// view, JSON, and the library model cannot disagree.
func CanonicalCodec(raw string) (codec, profile string) {
	canon := canonicalCodecName(raw)
	if canon != raw {
		return canon, raw
	}
	return raw, ""
}

// canonicalCodecName maps a raw codec name to its canonical form, or returns it
// unchanged when it is already canonical (Opus, Vorbis, PCM, the Matroska names, most of
// the WAV/AIFF descriptive names). Matched case-insensitively, so a single arm covers a
// QuickTime fourcc and the descriptive spelling another container gives the same codec.
func canonicalCodecName(raw string) string {
	up := strings.ToUpper(raw)
	switch up {
	case "MP4A":
		return "AAC"
	case "ALAC":
		return "ALAC" // normalizes the MP4 "alac" fourcc to match Matroska's "ALAC"
	case "FLAC":
		return "FLAC" // normalizes FLAC's lowercase "flac"
	case "AC-3":
		return "AC-3" // normalizes the MP4 "ac-3" fourcc to match Matroska's "AC-3"
	case "EC-3", "EAC3":
		return "E-AC-3" // Dolby Digital Plus: MP4 "ec-3" / Matroska "EAC3"
	case "WAVPACK DSD":
		return "WavPack" // the DSD mode is the profile detail, not a different codec
	case "MUSEPACK SV7", "MUSEPACK SV8":
		return "Musepack" // the stream version is the profile detail, not a different codec
	case "MPEG-1 LAYER 3", "MPEG-2 LAYER 3", "MPEG-2.5 LAYER 3", ".MP3":
		return "MP3" // ".mp3" is QuickTime's fourcc for the same stream an esds names "MP3"
	case "MPEG-1 LAYER 2", "MPEG-2 LAYER 2", "MPEG-2.5 LAYER 2", ".MP2":
		return "MP2"
	case "MPEG-1 LAYER 1", "MPEG-2 LAYER 1", "MPEG-2.5 LAYER 1", ".MP1":
		return "MP1"
	// The QuickTime/ISOBMFF PCM-family fourccs, which AIFF-C spells the same way. Byte
	// order, signedness and width are storage detail of a single codec rather than
	// different codecs, so each reads "PCM" with the raw spelling kept as the profile.
	// "RAW " carries a significant trailing space.
	case "LPCM", "IPCM", "SOWT", "TWOS", "IN24", "IN32", "RAW ", "NONE":
		return "PCM"
	case "FL32", "FPCM":
		return "IEEE float"
	case "FL64":
		return "IEEE float64"
	case "ULAW":
		return "mu-law"
	case "ALAW":
		return "A-law"
	case "IMA4":
		return "IMA ADPCM"
	// MACE has no descriptive name here, so its fourcc is the name; folding it to Apple's
	// spelling keeps one codec reading as one, since ffmpeg's AIFF demuxer accepts "mac3"
	// as MACE 3:1 too and [FourccSampleLayout] sizes it so.
	case "MAC3":
		return "MAC3"
	case "MAC6":
		return "MAC6"
	case "HE-AAC", "HE-AAC V2", "XHE-AAC":
		// The SBR/PS spellings an MP4 esds AudioSpecificConfig yields: still AAC, with the
		// extension named in the profile.
		return "AAC"
	}
	// The AAC object-type spellings ("AAC LC", "AAC Main", "AAC SSR", "AAC LTP", "AAC LD",
	// "AAC ELD") all canonicalize to "AAC"; a bare "AAC" is already canonical and falls
	// through unchanged.
	if strings.HasPrefix(up, "AAC") {
		return "AAC"
	}
	return raw
}

// Clone returns an independent copy of the properties.
func (p Properties) Clone() Properties {
	return Properties{Container: p.Container, Tracks: slices.Clone(p.Tracks)}
}

// First returns the first track, or a zero track if there are none.
func (p Properties) First() AudioTrack {
	if len(p.Tracks) == 0 {
		return AudioTrack{}
	}
	return p.Tracks[0]
}

// Duration returns the longest track duration (the file's playable length).
func (p Properties) Duration() time.Duration {
	var max time.Duration
	for _, t := range p.Tracks {
		if t.Duration > max {
			max = t.Duration
		}
	}
	return max
}

// WaveFormatCodec maps a WAVEFORMATEX format tag to a codec name. The structure is
// shared: a RIFF "fmt " chunk and an ASF Stream Properties object both describe their
// audio with one, so the two containers must name the same tag the same way or one
// file's codec would read differently depending on which container carried it. An
// unrecognized tag reports its hex value rather than being guessed at.
func WaveFormatCodec(format uint16) string {
	switch format {
	case 0x0001:
		return "PCM"
	case 0x0002:
		return "ADPCM"
	case 0x0003:
		return "IEEE float"
	case 0x0006:
		return "A-law"
	case 0x0007:
		return "mu-law"
	case 0x000A:
		return "WMA Voice"
	case 0x0011:
		return "IMA ADPCM"
	case 0x0050:
		return "MP2"
	case 0x0055:
		return "MP3"
	case 0x00FF:
		return "AAC"
	case 0x0160:
		return "WMA v1"
	case 0x0161:
		return "WMA v2"
	case 0x0162:
		return "WMA Pro"
	case 0x0163:
		return "WMA Lossless"
	case 0xFFFE:
		return "PCM (extensible)"
	}
	return fmt.Sprintf("WAVE format 0x%04X", format)
}

// SampleLayout is how a fixed-layout audio fourcc stores its samples. Depth is the width
// the fourcc itself fixes, 0 when a container field carries it (a sample entry's
// samplesize, a pcmC box, COMM's sampleSize). FramesPerPacket and PacketBytes describe a
// packetized type: the frames one packet decodes to and the bytes it occupies per channel.
// A byte-linear type has one frame per packet and no PacketBytes: each frame is the width
// rounded up to whole bytes, per channel.
type SampleLayout struct {
	Depth           int
	FramesPerPacket int
	PacketBytes     int
}

// FourccSampleLayout reports the layout of a QuickTime/ISOBMFF sample-entry fourcc, or the
// AIFF-C compression type that spells the same codec, and whether the fourcc fixes a layout
// at all: the uncompressed and companded forms, and QuickTime's fixed-packet ima4 and MACE.
// The MP4 and AIFF-C readers both key off this one table so a fourcc reports one width
// whichever container carried it: a v1 sample entry stores 16 whatever the real width, and
// an ima4 COMM says 16 from QuickTime and 4 from ffmpeg. Three rules read it, and a fourcc
// added here turns all three on: the width, wherever Depth is set; MP4's fallback to the
// media timescale when one of these entries leaves its 16.16 rate field zero, on membership
// alone, since none of them carries a configuration declaring a rate; and AIFF-C's sample
// count and nominal bitrate, from FramesPerPacket and PacketBytes, so a packetized entry
// like IMA4 sits in the table with its geometry rather than as a special case somewhere
// else. A QuickTime "ms" + WAVE-format-tag spelling of PCM, IEEE float, A-law or mu-law is
// the byte-linear form that tag names. Matched case-insensitively, as [CanonicalCodec]
// matches the same fourccs and ffmpeg's demuxers accept them.
func FourccSampleLayout(fourcc string) (SampleLayout, bool) {
	linear := func(depth int) (SampleLayout, bool) {
		return SampleLayout{Depth: depth, FramesPerPacket: 1}, true
	}
	switch strings.ToUpper(fourcc) {
	case "IN24":
		return linear(24)
	case "IN32", "FL32":
		return linear(32)
	case "FL64":
		return linear(64)
	case "ULAW", "ALAW":
		return linear(8)
	case "LPCM", "IPCM", "FPCM", "SOWT", "TWOS", "RAW ", "NONE":
		return linear(0)
	// The QuickTime sound-description constants, the ones ffmpeg's AIFF demuxer applies.
	case "IMA4":
		return SampleLayout{Depth: 4, FramesPerPacket: 64, PacketBytes: 34}, true
	case "MAC3":
		return SampleLayout{FramesPerPacket: 6, PacketBytes: 2}, true
	case "MAC6":
		return SampleLayout{FramesPerPacket: 6, PacketBytes: 1}, true
	}
	if tag, ok := QuickTimeWaveFormatTag(fourcc); ok {
		switch tag {
		case 0x0001, 0x0003:
			return linear(0)
		case 0x0006, 0x0007:
			return linear(8)
		}
	}
	return SampleLayout{}, false
}

// QuickTimeWaveFormatTag reports the WAVE format tag a QuickTime "ms" + tag fourcc spells,
// big-endian in its last two bytes, and whether the fourcc has that shape. QTFF defines the
// spelling for Windows codecs in a sound description, and ffmpeg's mov demuxer reads any
// "ms"-prefixed fourcc outside its own table this way; no registered QuickTime sound
// fourcc starts with "ms", and no byte-shape guard could tell a tag from an ordinary
// fourcc, since registered tags include printable pairs (0x674F, Vorbis). The MP4 and
// AIFF-C readers both name the codec through [WaveFormatCodec] from this one rule, so a
// tag reads the same as it does from a WAV "fmt " chunk or an ASF stream.
func QuickTimeWaveFormatTag(fourcc string) (uint16, bool) {
	if len(fourcc) != 4 || !strings.HasPrefix(fourcc, "ms") {
		return 0, false
	}
	return uint16(fourcc[2])<<8 | uint16(fourcc[3]), true
}
