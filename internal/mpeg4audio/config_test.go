package mpeg4audio

import (
	"encoding/hex"
	"testing"
)

// mustHex decodes hex config bytes.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// TestParseConfigShapes: real config shapes decode to output geometry.
func TestParseConfigShapes(t *testing.T) {
	cases := []struct {
		name     string
		hex      string
		rate     int
		channels int
		profile  string
	}{
		{"lc 44100 mono, tail denies sbr", "120856e500", 44100, 1, "AAC LC"}, // testdata/sample.m4a
		{"lc 96000 stereo, no tail", "1010", 96000, 2, "AAC LC"},
		{"lc 96000 stereo, tail denies sbr", "101056e500", 96000, 2, "AAC LC"}, // ffmpeg -ar 96000
		{"explicit 24-bit rate", "178061a810", 50000, 2, "AAC LC"},
		{"backward-compatible sbr", "119056e580", 96000, 2, "HE-AAC"},
		{"backward-compatible sbr denied", "131056e500", 24000, 2, "AAC LC"},
		{"backward-compatible sbr and ps", "130856e59d4880", 48000, 2, "HE-AAC v2"},
		{"hierarchical sbr", "2b920800", 44100, 2, "HE-AAC"},   // fdk-aac HE-AAC v1
		{"hierarchical ps", "eb098800", 48000, 2, "HE-AAC v2"}, // fdk-aac HE-AAC v2
		{"downsampled sbr does not double", "29918800", 48000, 2, "HE-AAC"},
		{"implicit sbr says nothing", "1390", 22050, 2, "AAC LC"},
		{"object type escape", "f94640", 48000, 2, "xHE-AAC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := ParseConfig(mustHex(t, tc.hex))
			if !ok {
				t.Fatalf("ParseConfig(%s) not ok", tc.hex)
			}
			if got := c.OutputSampleRate(); got != tc.rate {
				t.Errorf("OutputSampleRate = %d, want %d", got, tc.rate)
			}
			if got := c.OutputChannels(); got != tc.channels {
				t.Errorf("OutputChannels = %d, want %d", got, tc.channels)
			}
			if got := c.ProfileName(); got != tc.profile {
				t.Errorf("ProfileName = %q, want %q", got, tc.profile)
			}
		})
	}
}

// TestParseConfigSBRSignalled: SBR denial vs silence.
func TestParseConfigSBRSignalled(t *testing.T) {
	cases := []struct {
		name           string
		hex            string
		signalled, sbr bool
	}{
		{"tail denies sbr", "120856e500", true, false},
		{"tail declares sbr", "119056e580", true, true},
		{"no tail at all", "1390", false, false},
		{"hierarchical", "2b920800", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := ParseConfig(mustHex(t, tc.hex))
			if !ok {
				t.Fatalf("ParseConfig(%s) not ok", tc.hex)
			}
			if c.SBRSignalled != tc.signalled || c.SBR != tc.sbr {
				t.Errorf("SBRSignalled/SBR = %v/%v, want %v/%v", c.SBRSignalled, c.SBR, tc.signalled, tc.sbr)
			}
		})
	}
}

// TestParseConfigCoreGeometry: core fields readable behind output geometry.
func TestParseConfigCoreGeometry(t *testing.T) {
	c, ok := ParseConfig(mustHex(t, "eb098800"))
	if !ok {
		t.Fatal("ParseConfig not ok")
	}
	if c.ObjectType != 2 || c.SampleRate != 24000 || c.ChannelConfig != 1 || c.Channels != 1 {
		t.Errorf("core = {aot %d rate %d chanCfg %d ch %d}, want {2 24000 1 1}",
			c.ObjectType, c.SampleRate, c.ChannelConfig, c.Channels)
	}
	if c.ExtensionRate != 48000 {
		t.Errorf("ExtensionRate = %d, want 48000", c.ExtensionRate)
	}
}

// TestParseConfigProgramConfigElement: channelConfiguration 0 skips tail.
func TestParseConfigProgramConfigElement(t *testing.T) {
	c, ok := ParseConfig(mustHex(t, "1180"))
	if !ok {
		t.Fatal("ParseConfig not ok")
	}
	if c.ChannelConfig != 0 || c.Channels != 0 || c.OutputChannels() != 0 {
		t.Errorf("channels = %d/%d, want 0/0", c.Channels, c.OutputChannels())
	}
	if c.SampleRate != 48000 || c.SBRSignalled {
		t.Errorf("rate/signalled = %d/%v, want 48000/false", c.SampleRate, c.SBRSignalled)
	}
}

// TestParseConfigTooShort: only too-short input rejected outright.
func TestParseConfigTooShort(t *testing.T) {
	for _, b := range [][]byte{nil, {}, {0x12}} {
		if _, ok := ParseConfig(b); ok {
			t.Errorf("ParseConfig(%x) ok, want not ok", b)
		}
	}
	if _, ok := ParseConfig([]byte{0x12, 0x08}); !ok {
		t.Error("ParseConfig of the two bytes holding the fixed fields not ok")
	}
	// Index 15 spends 24 bits on the rate, so the channel configuration needs five bytes.
	if _, ok := ParseConfig(mustHex(t, "178061a8")); ok {
		t.Error("ParseConfig of an explicit-rate config cut before channelConfiguration ok, want not ok")
	}
}

// TestParseConfigNonConformant: non-conformant configs reported conservatively.
func TestParseConfigNonConformant(t *testing.T) {
	cases := []struct {
		name     string
		hex      string
		rate     int
		channels int
		profile  string
	}{
		// Parametric stereo needs a mono core; over a stereo one it is meaningless.
		{"ps over a stereo core", "eb118800", 48000, 2, "HE-AAC"},
		// A reserved extension index declares no rate, so the core rate stands.
		{"reserved extension index", "2b968800", 22050, 2, "HE-AAC"},
		// An SBR wrapper around another one makes the whole extension block suspect.
		{"sbr wrapping sbr", "2b921400", 22050, 2, "HE-AAC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := ParseConfig(mustHex(t, tc.hex))
			if !ok {
				t.Fatalf("ParseConfig(%s) not ok", tc.hex)
			}
			if c.OutputSampleRate() != tc.rate || c.OutputChannels() != tc.channels || c.ProfileName() != tc.profile {
				t.Errorf("got %d/%d/%q, want %d/%d/%q", c.OutputSampleRate(), c.OutputChannels(),
					c.ProfileName(), tc.rate, tc.channels, tc.profile)
			}
		})
	}
}

// TestParseConfigTruncatedTailIsSilence: truncated before sbrPresentFlag is silence, not denial.
func TestParseConfigTruncatedTailIsSilence(t *testing.T) {
	full, ok := ParseConfig(mustHex(t, "139056e580"))
	if !ok || !full.SBRSignalled || !full.SBR {
		t.Fatalf("the whole config should declare SBR; got %+v", full)
	}
	// The same bytes without the flag byte.
	cut, ok := ParseConfig(mustHex(t, "139056e5"))
	if !ok {
		t.Fatal("ParseConfig not ok")
	}
	if cut.SBRSignalled || cut.SBR {
		t.Errorf("SBRSignalled/SBR = %v/%v, want false/false (the flag was never read)", cut.SBRSignalled, cut.SBR)
	}
}

// TestParseConfigAbsurdExplicitRate: absurd explicit rate reads as 0; max valid accepted.
func TestParseConfigAbsurdExplicitRate(t *testing.T) {
	c, ok := ParseConfig(mustHex(t, "17ffffff9056e5fffffff8"))
	if !ok {
		t.Fatal("ParseConfig not ok")
	}
	if c.SampleRate != 0 || c.OutputSampleRate() != 0 {
		t.Errorf("rate = %d/%d, want 0 (declares no usable rate)", c.SampleRate, c.OutputSampleRate())
	}
	// The highest rate any supported format stores is still accepted.
	if r := SampleRate(0); r != 96000 {
		t.Errorf("SampleRate(0) = %d, want the table's 96000", r)
	}
	if c, ok := ParseConfig(mustHex(t, "1784fffb10")); !ok || c.SampleRate != 655350 {
		t.Errorf("655350 should be accepted; got %d (ok=%v)", c.SampleRate, ok)
	}
}

// TestSampleRateTable: reserved/escape indices return 0.
func TestSampleRateTable(t *testing.T) {
	want := []int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
	for i, w := range want {
		if got := SampleRate(i); got != w {
			t.Errorf("SampleRate(%d) = %d, want %d", i, got, w)
		}
	}
	for _, i := range []int{-1, 13, 14, 15, 16, 1 << 20} {
		if got := SampleRate(i); got != 0 {
			t.Errorf("SampleRate(%d) = %d, want 0", i, got)
		}
	}
}

// TestChannelCountTable: PCE/reserved return 0; layouts 11-14 match ffprobe.
func TestChannelCountTable(t *testing.T) {
	want := []int{0, 1, 2, 3, 4, 5, 6, 8, 0, 0, 0, 7, 8, 24, 8, 0}
	for i, w := range want {
		if got := ChannelCount(i); got != w {
			t.Errorf("ChannelCount(%d) = %d, want %d", i, got, w)
		}
	}
	for _, i := range []int{-1, 16, 1 << 20} {
		if got := ChannelCount(i); got != 0 {
			t.Errorf("ChannelCount(%d) = %d, want 0", i, got)
		}
	}
}

// TestObjectTypeName: named AOTs and "AAC" fallback.
func TestObjectTypeName(t *testing.T) {
	named := map[int]string{
		1: "AAC Main", 2: "AAC LC", 3: "AAC SSR", 4: "AAC LTP",
		23: "AAC LD", 39: "AAC ELD", 42: "xHE-AAC",
	}
	for aot, want := range named {
		if got := ObjectTypeName(aot); got != want {
			t.Errorf("ObjectTypeName(%d) = %q, want %q", aot, got, want)
		}
	}
	for _, aot := range []int{-1, 0, 5, 22, 29, 31, 32, 1 << 20} {
		if got := ObjectTypeName(aot); got != "AAC" {
			t.Errorf("ObjectTypeName(%d) = %q, want \"AAC\"", aot, got)
		}
	}
}
