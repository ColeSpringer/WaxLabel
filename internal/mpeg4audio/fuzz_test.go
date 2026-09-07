package mpeg4audio

import (
	"encoding/hex"
	"testing"
)

// maxConfigBytes bounds the prefixes the SBR check walks. The decoder reads at most a few
// dozen bits, so a longer prefix cannot reach a field a shorter one missed.
const maxConfigBytes = 24

// FuzzParseConfig checks the decoder survives arbitrary bytes and keeps the bounds its
// callers rely on: a rate that fits the 24-bit field, a channel count from the table, a
// printable profile name, parametric stereo only over a mono core, and SBR reported only
// from bits that are present - a truncated config must never claim an extension the whole
// one does not, which is what a read running off the end would produce.
func FuzzParseConfig(f *testing.F) {
	seeds := []string{
		"120856e500", "1010", "101056e500", "178061a810", "119056e580",
		"131056e500", "130856e59d4880", "2b920800", "eb098800", "29918800",
		"1390", "1180", "f94640", "eb118800", "2b968800", "2b921400",
		"f80000", "ffffffffff", "", "12", "139056e5", "17ffffff9056e5fffffff8", "1784fffb10",
	}
	for _, s := range seeds {
		b, err := hex.DecodeString(s)
		if err != nil {
			f.Fatalf("bad seed %q: %v", s, err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		c, ok := ParseConfig(b)
		if !ok {
			return
		}
		for _, r := range []int{c.SampleRate, c.ExtensionRate, c.OutputSampleRate()} {
			if r < 0 || r > 0xFFFFFF {
				t.Fatalf("rate %d out of range for config %x", r, b)
			}
		}
		if c.Channels < 0 || c.Channels > 24 || c.OutputChannels() < 0 || c.OutputChannels() > 24 {
			t.Fatalf("channels %d/%d out of range for config %x", c.Channels, c.OutputChannels(), b)
		}
		if c.ProfileName() == "" {
			t.Fatalf("empty profile name for config %x", b)
		}
		if c.PS && c.ChannelConfig != 1 {
			t.Fatalf("parametric stereo kept over channelConfiguration %d for config %x", c.ChannelConfig, b)
		}
		// Both SBR facts come from bits the config actually holds, so no prefix can claim
		// what the whole does not - not the extension, and not the denial of one.
		for n := range min(len(b), maxConfigBytes) {
			p, ok := ParseConfig(b[:n])
			if !ok {
				continue
			}
			if p.SBR && !c.SBR {
				t.Fatalf("prefix %x reports SBR that the whole config %x does not", b[:n], b)
			}
			if p.SBRSignalled && !c.SBRSignalled {
				t.Fatalf("prefix %x claims the whole config %x signalled SBR presence", b[:n], b)
			}
		}
	})
}
