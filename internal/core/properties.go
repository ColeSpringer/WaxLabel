package core

import (
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
// unchanged when it is already canonical (Opus, Vorbis, PCM, the Matroska names,
// the WAV/AIFF descriptive names). Matched case-insensitively.
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
	case "MPEG-1 LAYER 3", "MPEG-2 LAYER 3", "MPEG-2.5 LAYER 3":
		return "MP3"
	case "MPEG-1 LAYER 2", "MPEG-2 LAYER 2", "MPEG-2.5 LAYER 2":
		return "MP2"
	case "MPEG-1 LAYER 1", "MPEG-2 LAYER 1", "MPEG-2.5 LAYER 1":
		return "MP1"
	}
	// The AAC object-type spellings ("AAC LC", "AAC Main", "AAC SSR") all canonicalize
	// to "AAC"; a bare "AAC" is already canonical and falls through unchanged.
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
