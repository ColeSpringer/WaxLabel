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
		{"AAC LTP", "AAC", "AAC LTP"},
		{"HE-AAC", "AAC", "HE-AAC"},       // SBR, from an esds AudioSpecificConfig
		{"HE-AAC v2", "AAC", "HE-AAC v2"}, // SBR + parametric stereo
		{"xHE-AAC", "AAC", "xHE-AAC"},
		{"alac", "ALAC", "alac"}, // MP4 fourcc, case
		{"flac", "FLAC", "flac"}, // FLAC's lowercase
		{"FLAC", "FLAC", ""},     // already canonical (Matroska)
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
		{"WAVPACK4", "WAVPACK4", ""}, // Matroska, no canonical mapping
		// The QuickTime/ISOBMFF fourccs, which AIFF-C spells the same way. The byte order,
		// width and signedness each names is profile detail of one codec, so a stream
		// reads the same whichever container carried it.
		{".mp3", "MP3", ".mp3"}, // QuickTime's MP3 fourcc, the esds twin of "MP3"
		{".mp2", "MP2", ".mp2"},
		{"sowt", "PCM", "sowt"},
		{"twos", "PCM", "twos"},
		{"lpcm", "PCM", "lpcm"},
		{"ipcm", "PCM", "ipcm"},
		{"in24", "PCM", "in24"},
		{"NONE", "PCM", "NONE"},
		{"raw ", "PCM", "raw "}, // the trailing space is part of the fourcc
		{"fl32", "IEEE float", "fl32"},
		{"fpcm", "IEEE float", "fpcm"},
		{"fl64", "IEEE float64", "fl64"},
		{"IEEE float", "IEEE float", ""}, // already canonical (WAV, AIFF-C)
		{"ulaw", "mu-law", "ulaw"},
		{"alaw", "A-law", "alaw"},
		{"ima4", "IMA ADPCM", "ima4"},
		{"MAC3", "MAC3", ""},     // the fourcc is the name
		{"mac3", "MAC3", "mac3"}, // folded to Apple's spelling, the raw one kept
		{"MAC6", "MAC6", ""},
	}
	for _, c := range cases {
		codec, profile := CanonicalCodec(c.raw)
		if codec != c.codec || profile != c.profile {
			t.Errorf("CanonicalCodec(%q) = (%q, %q), want (%q, %q)", c.raw, codec, profile, c.codec, c.profile)
		}
	}
}

// TestWaveFormatCodec: a format tag names one codec whatever container carried the
// WAVEFORMATEX, so the RIFF, ASF and QuickTime "ms" readers share this table.
func TestWaveFormatCodec(t *testing.T) {
	for _, c := range []struct {
		tag  uint16
		want string
	}{
		{0x0050, "MP2"}, // MPEG Layer 2; without it the tag read as "WAVE format 0x0050"
		{0x0055, "MP3"},
		{0x0163, "WMA Lossless"},
		{0x1234, "WAVE format 0x1234"}, // unrecognized tags report themselves, not a guess
	} {
		if got := WaveFormatCodec(c.tag); got != c.want {
			t.Errorf("WaveFormatCodec(%#04x) = %q, want %q", c.tag, got, c.want)
		}
	}
}

// TestFourccSampleLayout: one fourcc has one layout whichever container carried it, so the
// MP4 and AIFF-C readers share this table; it folds case as the codec table does; the
// packetized QuickTime types carry their geometry here rather than as a case elsewhere; and
// the "ms" + WAVE-tag spellings of the byte-linear tags resolve to that layout.
func TestFourccSampleLayout(t *testing.T) {
	linear := func(depth int) SampleLayout { return SampleLayout{Depth: depth, FramesPerPacket: 1} }
	for _, c := range []struct {
		fourcc string
		want   SampleLayout
		ok     bool
	}{
		{"in24", linear(24), true}, {"IN24", linear(24), true}, {"in32", linear(32), true},
		{"fl32", linear(32), true}, {"FL32", linear(32), true}, {"fl64", linear(64), true},
		{"ulaw", linear(8), true}, {"ALAW", linear(8), true},
		{"sowt", linear(0), true}, {"twos", linear(0), true}, {"NONE", linear(0), true},
		{"raw ", linear(0), true}, {"lpcm", linear(0), true}, {"ipcm", linear(0), true}, {"fpcm", linear(0), true},
		{"ima4", SampleLayout{Depth: 4, FramesPerPacket: 64, PacketBytes: 34}, true},
		{"IMA4", SampleLayout{Depth: 4, FramesPerPacket: 64, PacketBytes: 34}, true},
		{"MAC3", SampleLayout{FramesPerPacket: 6, PacketBytes: 2}, true},
		{"mac6", SampleLayout{FramesPerPacket: 6, PacketBytes: 1}, true},
		{"ms\x00\x01", linear(0), true}, {"ms\x00\x03", linear(0), true},
		{"ms\x00\x06", linear(8), true}, {"ms\x00\x07", linear(8), true},
		{"ms\x00\x55", SampleLayout{}, false}, // MP3 by tag: no fixed layout
		{"mp4a", SampleLayout{}, false}, {".mp3", SampleLayout{}, false}, {"QDM2", SampleLayout{}, false}, {"", SampleLayout{}, false},
	} {
		if got, ok := FourccSampleLayout(c.fourcc); got != c.want || ok != c.ok {
			t.Errorf("FourccSampleLayout(%q) = (%+v, %v), want (%+v, %v)", c.fourcc, got, ok, c.want, c.ok)
		}
	}
}

// TestQuickTimeWaveFormatTag: the "ms" + tag spelling yields the big-endian tag, and only a
// four-byte fourcc opening with "ms" has that shape.
func TestQuickTimeWaveFormatTag(t *testing.T) {
	for _, c := range []struct {
		fourcc string
		tag    uint16
		ok     bool
	}{
		{"ms\x00\x55", 0x0055, true}, {"ms\x01\x61", 0x0161, true}, {"ms\x20\x00", 0x2000, true},
		{"msad", 0x6164, true}, {"ms", 0, false}, {"ms\x00\x55x", 0, false}, {"mp4a", 0, false}, {"", 0, false},
	} {
		if tag, ok := QuickTimeWaveFormatTag(c.fourcc); tag != c.tag || ok != c.ok {
			t.Errorf("QuickTimeWaveFormatTag(%q) = (%#04x, %v), want (%#04x, %v)", c.fourcc, tag, ok, c.tag, c.ok)
		}
	}
}

// TestOutputGainDB renders the Opus Q7.8 output gain as the dB figure the CLI speaks.
func TestOutputGainDB(t *testing.T) {
	for _, c := range []struct {
		gain int
		want string
	}{
		{0, "0.00 dB"},
		{-896, "-3.50 dB"},
		{256, "1.00 dB"},
		{-32768, "-128.00 dB"},
		{32767, "127.9961 dB"},
		// Adjacent Q7.8 steps are ~0.0039 dB apart, so two decimals would render a real
		// change as no change at all.
		{-897, "-3.5039 dB"},
		{1, "0.0039 dB"},
	} {
		if got := OutputGainDB(c.gain); got != c.want {
			t.Errorf("OutputGainDB(%d) = %q, want %q", c.gain, got, c.want)
		}
	}
}

// TestOutputGainWarningDiscardClassification: an unwritable gain is a discard (nothing was
// stored), while the R128 advisory rides along with an edit that did apply.
func TestOutputGainWarningDiscardClassification(t *testing.T) {
	if !IsDiscardWarning(WarnOutputGainUnsupported) {
		t.Error("WarnOutputGainUnsupported should be a discard warning")
	}
	if IsDiscardWarning(WarnOutputGainR128Tags) {
		t.Error("WarnOutputGainR128Tags is advisory, not a discard")
	}
}
