package mpeg4audio

import "testing"

// FuzzParseSBRSingleChannel asserts that walking an SBR payload over untrusted bytes never
// panics and never reports a result from a walk it could not finish. The seeds are the real
// payloads the parametric-stereo fixture carries.
func FuzzParseSBRSingleChannel(f *testing.F) {
	data, err := readFixtureFile("heaac_v2.aac")
	if err == nil {
		for _, fr := range walkADTSBytes(data) {
			info, perr := ParseRawDataBlock(fr.block, fr.cfg)
			if perr == nil && info.SBRPayload != nil {
				f.Add(info.SBRPayload, info.SBRBits, info.SBRCRC, 3)
				break
			}
		}
	}
	f.Add([]byte{0x00}, 4, false, 3)
	f.Fuzz(func(t *testing.T, payload []byte, payloadBits int, crc bool, rateIdx int) {
		var st SBRState
		ps, ok := ParseSBRSingleChannel(payload, payloadBits, crc, rateIdx, &st)
		if !ok {
			return
		}
		if ps && payloadBits <= 0 {
			t.Error("parametric stereo reported from an empty payload")
		}
		if ok && !st.hasHeader {
			t.Error("a successful parse must have seen a header")
		}
	})
}
