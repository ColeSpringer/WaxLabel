package main

import (
	"bytes"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// trackProps wraps a single audio track in a Properties for audioLine tests.
func trackProps(container string, t wl.AudioTrack) wl.Properties {
	return wl.Properties{Container: container, Tracks: []wl.AudioTrack{t}}
}

func TestAudioLineBitDepthOnlyForLossless(t *testing.T) {
	// audioLine receives the canonical codec name (CanonicalCodec runs at parse). A
	// lossy codec carrying a stored bit depth must not advertise "16-bit".
	lossy := audioLine(trackProps("MP4", wl.AudioTrack{Codec: "AAC", SampleRate: 44100, Channels: 2, BitsPerSample: 16, Bitrate: 128000}))
	if strings.Contains(lossy, "16-bit") {
		t.Errorf("lossy audio line should omit bit depth: %q", lossy)
	}
	// A lossless codec keeps it.
	flac := audioLine(trackProps("FLAC", wl.AudioTrack{Codec: "FLAC", SampleRate: 44100, Channels: 2, BitsPerSample: 24}))
	if !strings.Contains(flac, "24-bit") {
		t.Errorf("lossless audio line should keep bit depth: %q", flac)
	}
}

func TestAudioLineCodecUnknown(t *testing.T) {
	// No codec identified: name the container and say so, not a bare "MATROSKA".
	line := audioLine(trackProps("Matroska", wl.AudioTrack{SampleRate: 48000, Channels: 2}))
	if !strings.Contains(line, "Matroska (codec unknown)") {
		t.Errorf("unidentified codec line = %q, want \"Matroska (codec unknown)\"", line)
	}
}

func TestAudioLineSubKbpsOmitted(t *testing.T) {
	// A truncated file's collapsed sub-1-kbps average must not print as "0 kbps".
	line := audioLine(trackProps("WAV", wl.AudioTrack{Codec: "PCM", SampleRate: 44100, Channels: 2, BitsPerSample: 16, Bitrate: 12}))
	if strings.Contains(line, "kbps") {
		t.Errorf("sub-1-kbps bitrate should be omitted: %q", line)
	}
}

func TestBitDepthMeaningful(t *testing.T) {
	// bitDepthMeaningful receives the canonical codec name. Codecs that carry a real
	// stored sample width keep their depth - the PCM family, the lossless codecs
	// (incl. Matroska WavPack/TTA/MLP), and the companded/ADPCM forms.
	for _, c := range []string{
		"FLAC", "ALAC", "PCM", "PCM (extensible)", "IEEE float", "IEEE float64",
		"A-law", "mu-law", "IMA ADPCM", "WAVPACK4", "TTA1", "MLP",
	} {
		if !bitDepthMeaningful(c) {
			t.Errorf("bitDepthMeaningful(%q) = false, want true", c)
		}
	}
	// Lossy/perceptual codecs decode to PCM at an arbitrary depth, so any stored depth
	// is meaningless and must be suppressed.
	for _, c := range []string{"AAC", "MP3", "MP2", "MP1", "Opus", "Vorbis", "AC-3", "E-AC-3", "MPC"} {
		if bitDepthMeaningful(c) {
			t.Errorf("bitDepthMeaningful(%q) = true, want false", c)
		}
	}
}

func TestDisplayName(t *testing.T) {
	if got := displayName("-"); got != "<stdin>" {
		t.Errorf("displayName(%q) = %q, want <stdin>", "-", got)
	}
	if got := displayName("song.flac"); got != "song.flac" {
		t.Errorf("displayName(%q) = %q, want unchanged", "song.flac", got)
	}
}

func TestRenderTagsEmptyValue(t *testing.T) {
	ts := tag.NewTagSet()
	ts.Set(tag.Title, "") // present, empty string value
	var buf bytes.Buffer
	renderTags(&buf, ts)
	if !strings.Contains(buf.String(), "(empty value)") {
		t.Errorf("renderTags should label an empty value; got:\n%s", buf.String())
	}
}
