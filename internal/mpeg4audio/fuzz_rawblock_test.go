package mpeg4audio

import "testing"

// FuzzParseRawDataBlock asserts the one property a metadata reader needs from a bitstream
// walk over untrusted bytes: it never panics, and it never reports a block it did not read
// to its end. The seeds are real frames from the checked-in streams, so the mutator starts
// from syntax that reaches every branch.
func FuzzParseRawDataBlock(f *testing.F) {
	for _, name := range []string{"notags.aac", "heaac_v1.aac", "heaac_v2.aac", "sample.aac"} {
		data, err := readFixtureFile(name)
		if err != nil {
			continue
		}
		frames := walkADTSBytes(data)
		if len(frames) > 0 {
			fr := frames[0]
			f.Add(fr.block, fr.cfg.SampleRateIdx, fr.cfg.ChannelConfig)
		}
	}
	f.Add([]byte{0xE0}, 4, 1) // a bare END element, byte aligned
	f.Fuzz(func(t *testing.T, block []byte, sfIndex, chanConfig int) {
		info, err := ParseRawDataBlock(block, FrameConfig{ObjectType: 2, SampleRateIdx: sfIndex, ChannelConfig: chanConfig})
		if err != nil {
			if info.SBR || info.SBRPayload != nil {
				t.Errorf("a failed parse reported %+v", info)
			}
			return
		}
		if info.SBRPayload != nil {
			if info.SBRBits <= 0 || info.SBRBits > len(info.SBRPayload)*8 {
				t.Errorf("an SBR payload of %d bytes reports %d bits", len(info.SBRPayload), info.SBRBits)
			}
			if !info.SBR {
				t.Error("a captured SBR payload must also report SBR")
			}
		}
	})
}
