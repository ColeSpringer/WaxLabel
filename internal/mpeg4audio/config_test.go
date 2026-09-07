package mpeg4audio

import (
	"encoding/hex"
	"testing"
)

// mustHex decodes a config written as hex, the form every specification example
// and every ffprobe extradata dump uses.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// TestParseConfigShapes: every AudioSpecificConfig shape a real file carries decodes to the
// geometry a player produces, including the ffmpeg-native tail that denies SBR.
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

// TestParseConfigSBRSignalled: a config that denies SBR is distinguishable from one that
// says nothing about it, which is what lets a caller trust an entry's doubled rate.
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

// TestParseConfigCoreGeometry: the core rate and channel configuration stay readable behind
// the output geometry, since the essence digest and the "entry says double" rule need them.
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

// TestParseConfigProgramConfigElement: channelConfiguration 0 puts the layout in a
// variable-length element, so the channel count is unknown and the tail is not read.
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

// TestParseConfigTooShort: fewer bits than the three fixed header fields is the only input
// ParseConfig rejects outright.
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

// TestParseConfigNonConformant: a config that breaks the specification is reported
// conservatively rather than trusted or rejected.
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

// TestParseConfigTruncatedTailIsSilence: a config cut off before sbrPresentFlag says nothing
// about SBR. Reporting it as an explicit denial is worse than reporting no tail at all,
// because a caller trusting the denial then overrides a container that was right.
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

// TestParseConfigAbsurdExplicitRate: the escape names rates the table cannot, but the field
// is 24 bits and a corrupt config fills it. A value no format could store is not a rate, and
// reporting one would override a container field that is probably right.
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

// TestSampleRateTable: the reserved and escape indices report no rate, which is what lets
// the ADTS decoder reject them with one comparison.
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

// TestChannelCountTable: configuration 0 and the reserved values report no count; 11 through
// 14 are the layouts a lookup that stops at 7 would get wrong. Checked against ffprobe, which
// reads 11 as 6.1, 12 as 7.1, 13 as 22.2, and 14 as 5.1.2.
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

// TestObjectTypeName: only the types worth distinguishing are named; the rest fall back to
// the bare codec name, which callers read as "no detail to report".
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
