package core

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Properties describes the audio stream(s). Tracks is a slice because Matroska and MP4 can be multi-track.
type Properties struct {
	Container string
	Tracks    []AudioTrack
}

// AudioTrack is one audio stream's technical properties. Sample rate, channels, bits per sample,
// and FLAC block-size bounds also feed the audio-essence digest.
type AudioTrack struct {
	Index int
	// Codec is the container-neutral name (AAC, MP3, FLAC, ...). CodecProfile keeps the
	// container spelling when it adds detail the canonical name drops (fourcc, AAC object type,
	// MPEG layer). Both come from [CanonicalCodec].
	Codec         string
	CodecProfile  string
	SampleRate    int
	Channels      int
	BitsPerSample int
	TotalSamples  uint64
	Duration      time.Duration
	Bitrate       int // average bits per second

	// FLAC STREAMINFO fields used for fidelity and essence hashing.
	MinBlockSize int
	MaxBlockSize int
	MD5          [16]byte // decoded-audio MD5 from STREAMINFO

	// OutputGain is Opus output_gain as signed Q7.8 dB (256 = +1 dB). Read from Ogg Opus only;
	// Matroska A_OPUS CodecPrivate and MP4 dOps are not decoded, so those report 0 and
	// [Capabilities.OutputGain] is AccessNone.
	OutputGain int
}

// OutputGainDecibels converts Q7.8 output gain to decibels.
func OutputGainDecibels(gain int) float64 { return float64(gain) / 256 }

// OutputGainDB renders Q7.8 gain as dB. Uses at least two decimals; adds more when needed so a
// real gain change never prints as an identical before/after (~0.0039 dB per Q7.8 step).
func OutputGainDB(gain int) string {
	s := strings.TrimRight(fmt.Sprintf("%.4f", OutputGainDecibels(gain)), "0")
	if n := strings.IndexByte(s, '.'); n >= 0 && len(s)-n < 3 {
		s = fmt.Sprintf("%.2f", OutputGainDecibels(gain))
	}
	return s + " dB"
}

// OutputGainUnsupportedMessage is the drop warning for a format that cannot write output gain.
// Matroska/MP4 Opus may carry a header gain; this parser neither reads nor writes it.
func OutputGainUnsupportedMessage(f Format) string {
	return fmt.Sprintf("an output gain cannot be written to %s %s file; the gain was dropped",
		IndefiniteArticle(f.String()), f)
}

// AverageBitrate returns average bits/s for audioBytes over secs, or 0 if either input is
// non-positive. Caps below MaxInt32 so a near-zero duration cannot overflow int on 32-bit.
// Shared by every codec that derives average bitrate.
func AverageBitrate(audioBytes int64, secs float64) int {
	if audioBytes <= 0 || secs <= 0 {
		return 0
	}
	if bps := float64(audioBytes) * 8 / secs; bps < math.MaxInt32 {
		return int(bps)
	}
	return 0
}

// SamplesToDuration converts a PCM sample count at rate Hz to a duration, or 0 for a
// non-positive rate or a count that would overflow int64 nanoseconds. Shared by codecs that
// derive duration from sample count (see also [AverageBitrate]).
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

// CanonicalCodec splits a raw codec name into the container-neutral name and optional profile.
// Profile is the raw name when it differs from the canonical form, else "". Applied once after
// parse so text, JSON, and the model agree.
func CanonicalCodec(raw string) (codec, profile string) {
	canon := canonicalCodecName(raw)
	if canon != raw {
		return canon, raw
	}
	return raw, ""
}

// canonicalCodecName maps a raw codec name to its canonical form (case-insensitive), or returns
// it unchanged when already canonical.
func canonicalCodecName(raw string) string {
	up := strings.ToUpper(raw)
	switch up {
	case "MP4A":
		return "AAC"
	case "ALAC":
		return "ALAC" // normalize MP4 "alac" to Matroska "ALAC"
	case "FLAC":
		return "FLAC" // normalize lowercase "flac"
	case "AC-3":
		return "AC-3" // normalize MP4 "ac-3" to Matroska "AC-3"
	case "EC-3", "EAC3":
		return "E-AC-3" // Dolby Digital Plus: MP4 "ec-3" / Matroska "EAC3"
	case "WAVPACK DSD":
		return "WavPack" // DSD mode is profile, not a separate codec
	case "MUSEPACK SV7", "MUSEPACK SV8":
		return "Musepack" // stream version is profile
	case "MPEG-1 LAYER 3", "MPEG-2 LAYER 3", "MPEG-2.5 LAYER 3", ".MP3":
		return "MP3" // ".mp3" is QuickTime's fourcc for the same stream
	case "MPEG-1 LAYER 2", "MPEG-2 LAYER 2", "MPEG-2.5 LAYER 2", ".MP2":
		return "MP2"
	case "MPEG-1 LAYER 1", "MPEG-2 LAYER 1", "MPEG-2.5 LAYER 1", ".MP1":
		return "MP1"
	// QuickTime/ISOBMFF PCM-family fourccs (AIFF-C same). Storage detail stays in profile.
	// "RAW " has a significant trailing space.
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
	// MACE has no descriptive alias; fold case to Apple's spelling (ffmpeg accepts "mac3").
	case "MAC3":
		return "MAC3"
	case "MAC6":
		return "MAC6"
	case "HE-AAC", "HE-AAC V2", "XHE-AAC":
		// MP4 esds SBR/PS spellings: still AAC; extension stays in profile.
		return "AAC"
	}
	// "AAC LC", "AAC Main", etc. -> "AAC"; bare "AAC" falls through unchanged.
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

// Duration returns the longest track duration (playable length).
func (p Properties) Duration() time.Duration {
	var max time.Duration
	for _, t := range p.Tracks {
		if t.Duration > max {
			max = t.Duration
		}
	}
	return max
}

// WaveFormatCodec maps a WAVEFORMATEX format tag to a codec name. Shared by RIFF "fmt " and
// ASF Stream Properties so the same tag names the same codec. Unrecognized tags report hex.
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

// SampleLayout is how a fixed-layout audio fourcc stores samples. Depth is the width the
// fourcc fixes, or 0 when a container field carries it. FramesPerPacket/PacketBytes describe
// packetized types; byte-linear types use one frame per packet and PacketBytes 0.
type SampleLayout struct {
	Depth           int
	FramesPerPacket int
	PacketBytes     int
}

// FourccSampleLayout reports the layout for a QuickTime/ISOBMFF sample-entry fourcc (or the
// matching AIFF-C compression type). Shared by MP4 and AIFF-C so width and packet geometry
// agree. Also covers QuickTime "ms"+WAVE-tag spellings of PCM/float/A-law/mu-law. Case-insensitive.
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
	// QuickTime sound-description constants (ffmpeg AIFF demuxer).
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

// QuickTimeWaveFormatTag reports the WAVE format tag in a QuickTime "ms"+tag fourcc
// (big-endian last two bytes). MP4 and AIFF-C name the codec via [WaveFormatCodec].
func QuickTimeWaveFormatTag(fourcc string) (uint16, bool) {
	if len(fourcc) != 4 || !strings.HasPrefix(fourcc, "ms") {
		return 0, false
	}
	return uint16(fourcc[2])<<8 | uint16(fourcc[3]), true
}
