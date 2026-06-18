package core

import (
	"math"
	"slices"
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
	Index         int
	Codec         string
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
