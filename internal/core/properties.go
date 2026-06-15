package core

import (
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
