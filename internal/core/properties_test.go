package core

import "testing"

func TestAverageBitrate(t *testing.T) {
	cases := []struct {
		name       string
		audioBytes int64
		secs       float64
		want       int
	}{
		{"typical", 1_000_000, 100, 80_000},
		{"zero duration", 1_000_000, 0, 0},
		{"negative duration", 1_000_000, -1, 0},
		{"zero bytes", 0, 10, 0},
		{"negative bytes", -5, 10, 0},
		// A near-zero (e.g. adversarial) duration would overflow the int cast; the
		// MaxInt32 cap suppresses the absurd value instead of returning garbage.
		{"tiny duration capped", 1_000_000, 1e-9, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AverageBitrate(tc.audioBytes, tc.secs); got != tc.want {
				t.Errorf("AverageBitrate(%d, %g) = %d, want %d", tc.audioBytes, tc.secs, got, tc.want)
			}
		})
	}
}

func TestCanonicalCodec(t *testing.T) {
	cases := []struct{ raw, codec, profile string }{
		{"mp4a", "AAC", "mp4a"},     // MP4 fourcc
		{"AAC LC", "AAC", "AAC LC"}, // raw-AAC object type
		{"AAC", "AAC", ""},          // already canonical (Matroska)
		{"alac", "ALAC", "alac"},    // MP4 fourcc, case
		{"flac", "FLAC", "flac"},    // FLAC's lowercase
		{"FLAC", "FLAC", ""},        // already canonical (Matroska)
		{"MPEG-1 Layer 3", "MP3", "MPEG-1 Layer 3"},
		{"MPEG-2.5 Layer 3", "MP3", "MPEG-2.5 Layer 3"},
		{"MPEG-1 Layer 2", "MP2", "MPEG-1 Layer 2"},
		{"MPEG-1 Layer 1", "MP1", "MPEG-1 Layer 1"},
		{"MP3", "MP3", ""},         // already canonical (Matroska)
		{"ac-3", "AC-3", "ac-3"},   // MP4 fourcc -> matches Matroska "AC-3"
		{"AC-3", "AC-3", ""},       // already canonical (Matroska)
		{"ec-3", "E-AC-3", "ec-3"}, // MP4 Dolby Digital Plus fourcc
		{"EAC3", "E-AC-3", "EAC3"}, // Matroska A_EAC3 stripped form
		{"Opus", "Opus", ""},
		{"Vorbis", "Vorbis", ""},
		{"PCM", "PCM", ""},
		{"PCM (little-endian)", "PCM (little-endian)", ""}, // AIFF detail kept as-is
		{"WAVPACK4", "WAVPACK4", ""},                       // Matroska, no canonical mapping
	}
	for _, c := range cases {
		codec, profile := CanonicalCodec(c.raw)
		if codec != c.codec || profile != c.profile {
			t.Errorf("CanonicalCodec(%q) = (%q, %q), want (%q, %q)", c.raw, codec, profile, c.codec, c.profile)
		}
	}
}
