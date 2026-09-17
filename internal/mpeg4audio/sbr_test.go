package mpeg4audio

import (
	"testing"
)

// doubledRateIndex is SBR rate index (2x core); false above 48 kHz core.
func doubledRateIndex(i int) (int, bool) {
	want := SampleRate(i) * 2
	for j := range 13 {
		if SampleRate(j) == want {
			return j, true
		}
	}
	return 0, false
}

// sbrFramesOf returns SCE SBR payloads and SBR rate index from a fixture.
func sbrFramesOf(t *testing.T, file string) ([]FrameInfo, int) {
	t.Helper()
	frames := walkADTS(t, readFixture(t, file))
	if len(frames) == 0 {
		t.Fatalf("%s: no AAC LC frames", file)
	}
	sbrIdx, ok := doubledRateIndex(frames[0].cfg.SampleRateIdx)
	if !ok {
		t.Fatalf("%s: core rate %d Hz has no SBR rate", file, SampleRate(frames[0].cfg.SampleRateIdx))
	}
	var out []FrameInfo
	for i, f := range frames {
		info, err := ParseRawDataBlock(f.block, f.cfg)
		if err != nil {
			t.Fatalf("%s frame %d: %v", file, i, err)
		}
		if info.SBRPayload != nil {
			out = append(out, info)
		}
	}
	return out, sbrIdx
}

// TestSBRFixtureFramesParse: heaac_v2 SBR payloads parse with PS.
func TestSBRFixtureFramesParse(t *testing.T) {
	infos, sbrIdx := sbrFramesOf(t, "heaac_v2.aac")
	if len(infos) == 0 {
		t.Fatal("no single channel element carried an SBR payload")
	}
	var st SBRState
	for i, info := range infos {
		ps, ok := ParseSBRSingleChannel(info.SBRPayload, info.SBRBits, info.SBRCRC, sbrIdx, &st)
		if !ok {
			t.Fatalf("frame %d: payload did not parse", i)
		}
		if !ps {
			t.Errorf("frame %d: parametric stereo not reported", i)
		}
	}
}

// TestSBRExtensionIDDecidesPS: flipping bs_extension_id flips PS result.
func TestSBRExtensionIDDecidesPS(t *testing.T) {
	infos, sbrIdx := sbrFramesOf(t, "heaac_v2.aac")
	if len(infos) == 0 {
		t.Fatal("no SBR payload")
	}
	info := infos[0]
	var st SBRState
	if _, ok := ParseSBRSingleChannel(info.SBRPayload, info.SBRBits, info.SBRCRC, sbrIdx, &st); !ok {
		t.Fatal("the unmodified payload must parse")
	}
	// Find the extension id by walking to it, then rewrite those two bits to a value that is
	// not EXTENSION_ID_PS and confirm the same walk reports no parametric stereo.
	pos, found := extensionIDPosition(t, info, sbrIdx)
	if !found {
		t.Fatal("the parametric-stereo fixture must carry extended data to locate its id in")
	}
	flipped := append([]byte(nil), info.SBRPayload...)
	setBits2(flipped, pos, 0)
	var st2 SBRState
	ps, ok := ParseSBRSingleChannel(flipped, info.SBRBits, info.SBRCRC, sbrIdx, &st2)
	if !ok {
		t.Fatal("the flipped payload must still parse")
	}
	if ps {
		t.Error("a non-PS extension id must not report parametric stereo")
	}
}

// extensionIDPosition finds bs_extension_id bit offset by re-walking to extended data.
func extensionIDPosition(t *testing.T, info FrameInfo, sbrIdx int) (int, bool) {
	t.Helper()
	p := &sbrParser{r: &bitReader{b: info.SBRPayload}, bits: info.SBRBits, fs: SampleRate(sbrIdx), st: &SBRState{}}
	if info.SBRCRC && !p.r.skip(10) {
		return 0, false
	}
	headerFlag, ok := p.r.bit()
	if !ok {
		return 0, false
	}
	if headerFlag == 1 && !p.readHeader() {
		return 0, false
	}
	if !p.st.hasHeader {
		return 0, false
	}
	// Re-walk the element up to the extended-data flag, then step over the size field.
	extra, ok := p.r.bit()
	if !ok || (extra == 1 && !p.r.skip(4)) {
		return 0, false
	}
	g, ok := p.grid()
	if !ok {
		return 0, false
	}
	dfEnv, dfNoise, ok := p.dtdf(g)
	if !ok || !p.r.skip(2*p.st.bands.numNoise) || !p.envelope(g, dfEnv) || !p.noise(g, dfNoise) {
		return 0, false
	}
	addHarmonic, ok := p.r.bit()
	if !ok || (addHarmonic == 1 && !p.r.skip(p.st.bands.numHigh)) {
		return 0, false
	}
	extended, ok := p.r.bit()
	if !ok || extended == 0 {
		return 0, false
	}
	cnt, ok := p.r.read(4)
	if !ok {
		return 0, false
	}
	if cnt == 15 {
		if _, ok := p.r.read(8); !ok {
			return 0, false
		}
	}
	return p.r.position(), true
}

// setBits2 writes a two-bit value at a bit offset.
func setBits2(b []byte, pos int, v uint32) {
	for i := range 2 {
		bit := (v >> (1 - i)) & 1
		idx := pos + i
		mask := byte(1) << (7 - idx&7)
		if bit == 1 {
			b[idx>>3] |= mask
		} else {
			b[idx>>3] &^= mask
		}
	}
}

// TestSBRFrequencyBandCounts: hand-computed deriveBands cases from 4.6.18.3.2.
func TestSBRFrequencyBandCounts(t *testing.T) {
	cases := []struct {
		name                          string
		h                             sbrHeader
		fsSBR                         int
		wantMaster, wantHigh, wantLow int
		wantNoise                     int
	}{
		{"fixture header", sbrHeader{ampRes: 1, startFreq: 10, stopFreq: 9, xoverBand: 0, freqScale: 2, alterScale: 1, noiseBands: 2}, 48000, 12, 12, 6, 3},
		{"single region", sbrHeader{startFreq: 5, stopFreq: 0, xoverBand: 0, freqScale: 2, alterScale: 1, noiseBands: 2}, 22050, 10, 10, 5, 2},
		{"stop 15 triples k0", sbrHeader{startFreq: 5, stopFreq: 15, xoverBand: 2, freqScale: 2, alterScale: 1, noiseBands: 1}, 22050, 14, 12, 6, 1},
		{"stop 14 doubles k0", sbrHeader{startFreq: 10, stopFreq: 14, xoverBand: 1, freqScale: 2, alterScale: 1, noiseBands: 1}, 48000, 10, 9, 5, 1},
		{"linear scale", sbrHeader{startFreq: 10, stopFreq: 9, xoverBand: 1, freqScale: 0, alterScale: 0, noiseBands: 2}, 48000, 26, 25, 13, 2},
		{"linear scale, doubled step", sbrHeader{startFreq: 10, stopFreq: 9, xoverBand: 1, freqScale: 0, alterScale: 1, noiseBands: 2}, 48000, 14, 13, 7, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, ok := deriveBands(c.h, c.fsSBR)
			if !ok {
				t.Fatal("header should derive")
			}
			if b.numMaster != c.wantMaster || b.numHigh != c.wantHigh || b.numLow != c.wantLow || b.numNoise != c.wantNoise {
				t.Errorf("got master=%d high=%d low=%d noise=%d, want %d/%d/%d/%d",
					b.numMaster, b.numHigh, b.numLow, b.numNoise, c.wantMaster, c.wantHigh, c.wantLow, c.wantNoise)
			}
		})
	}
}

// TestSBRRejectsImpossibleRange: invalid QMF range refused.
func TestSBRRejectsImpossibleRange(t *testing.T) {
	// 33 QMF subbands at 48 kHz, one past the limit 4.6.18.3.6 sets for that rate.
	if _, ok := deriveBands(sbrHeader{startFreq: 15, stopFreq: 15, freqScale: 2, alterScale: 1, noiseBands: 2}, 48000); ok {
		t.Error("a range wider than the rate allows should be refused")
	}
	// A crossover band past the end of the master table it indexes.
	if _, ok := deriveBands(sbrHeader{startFreq: 15, stopFreq: 0, xoverBand: 7, freqScale: 2, alterScale: 1, noiseBands: 2}, 16000); ok {
		t.Error("a crossover past the master table should be refused")
	}
	// A rate with no index, which is what a core rate above 48 kHz doubles to.
	var st SBRState
	if _, ok := ParseSBRSingleChannel([]byte{0, 0, 0, 0}, 32, false, 13, &st); ok {
		t.Error("an unknown SBR rate should be refused")
	}
}
