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
		// Near-zero duration would overflow int cast; capped at 0.
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
		{"mp4a", "AAC", "mp4a"},
		{"AAC LC", "AAC", "AAC LC"},
		{"AAC", "AAC", ""},
		{"AAC LTP", "AAC", "AAC LTP"},
		{"HE-AAC", "AAC", "HE-AAC"},
		{"HE-AAC v2", "AAC", "HE-AAC v2"},
		{"xHE-AAC", "AAC", "xHE-AAC"},
		{"alac", "ALAC", "alac"},
		{"flac", "FLAC", "flac"},
		{"FLAC", "FLAC", ""},
		{"MPEG-1 Layer 3", "MP3", "MPEG-1 Layer 3"},
		{"MPEG-2.5 Layer 3", "MP3", "MPEG-2.5 Layer 3"},
		{"MPEG-1 Layer 2", "MP2", "MPEG-1 Layer 2"},
		{"MPEG-1 Layer 1", "MP1", "MPEG-1 Layer 1"},
		{"MP3", "MP3", ""},
		{"ac-3", "AC-3", "ac-3"},
		{"AC-3", "AC-3", ""},
		{"ec-3", "E-AC-3", "ec-3"},
		{"EAC3", "E-AC-3", "EAC3"},
		{"Opus", "Opus", ""},
		{"Vorbis", "Vorbis", ""},
		{"PCM", "PCM", ""},
		{"WAVPACK4", "WAVPACK4", ""},
		{".mp3", "MP3", ".mp3"},
		{".mp2", "MP2", ".mp2"},
		{"sowt", "PCM", "sowt"},
		{"twos", "PCM", "twos"},
		{"lpcm", "PCM", "lpcm"},
		{"ipcm", "PCM", "ipcm"},
		{"in24", "PCM", "in24"},
		{"NONE", "PCM", "NONE"},
		{"raw ", "PCM", "raw "},
		{"fl32", "IEEE float", "fl32"},
		{"fpcm", "IEEE float", "fpcm"},
		{"fl64", "IEEE float64", "fl64"},
		{"IEEE float", "IEEE float", ""},
		{"ulaw", "mu-law", "ulaw"},
		{"alaw", "A-law", "alaw"},
		{"ima4", "IMA ADPCM", "ima4"},
		{"MAC3", "MAC3", ""},
		{"mac3", "MAC3", "mac3"},
		{"MAC6", "MAC6", ""},
	}
	for _, c := range cases {
		codec, profile := CanonicalCodec(c.raw)
		if codec != c.codec || profile != c.profile {
			t.Errorf("CanonicalCodec(%q) = (%q, %q), want (%q, %q)", c.raw, codec, profile, c.codec, c.profile)
		}
	}
}

// TestWaveFormatCodec: WAVE format tag to codec name (shared by RIFF, ASF, QuickTime ms readers).
func TestWaveFormatCodec(t *testing.T) {
	for _, c := range []struct {
		tag  uint16
		want string
	}{
		{0x0050, "MP2"},
		{0x0055, "MP3"},
		{0x0163, "WMA Lossless"},
		{0x1234, "WAVE format 0x1234"},
	} {
		if got := WaveFormatCodec(c.tag); got != c.want {
			t.Errorf("WaveFormatCodec(%#04x) = %q, want %q", c.tag, got, c.want)
		}
	}
}

// TestFourccSampleLayout: fourcc to sample layout (MP4 and AIFF-C share table).
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
		{"ms\x00\x55", SampleLayout{}, false},
		{"mp4a", SampleLayout{}, false}, {".mp3", SampleLayout{}, false}, {"QDM2", SampleLayout{}, false}, {"", SampleLayout{}, false},
	} {
		if got, ok := FourccSampleLayout(c.fourcc); got != c.want || ok != c.ok {
			t.Errorf("FourccSampleLayout(%q) = (%+v, %v), want (%+v, %v)", c.fourcc, got, ok, c.want, c.ok)
		}
	}
}

// TestQuickTimeWaveFormatTag: "ms" + WAVE tag fourcc to big-endian tag.
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
		// Q7.8 steps are ~0.0039 dB; need four decimals.
		{-897, "-3.5039 dB"},
		{1, "0.0039 dB"},
	} {
		if got := OutputGainDB(c.gain); got != c.want {
			t.Errorf("OutputGainDB(%d) = %q, want %q", c.gain, got, c.want)
		}
	}
}

// TestOutputGainWarningDiscardClassification: unsupported gain is discard; R128 advisory is not.
func TestOutputGainWarningDiscardClassification(t *testing.T) {
	if !IsDiscardWarning(WarnOutputGainUnsupported) {
		t.Error("WarnOutputGainUnsupported should be a discard warning")
	}
	if IsDiscardWarning(WarnOutputGainR128Tags) {
		t.Error("WarnOutputGainR128Tags is advisory, not a discard")
	}
}
