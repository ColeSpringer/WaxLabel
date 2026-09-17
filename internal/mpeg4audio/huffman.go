package mpeg4audio

import "sync"

// huffCode is codeword length and right-aligned value; symbol order matches spec index.
type huffCode struct {
	Len  uint8
	Code uint32
}

// huffBook is a codebook indexed by symbol.
type huffBook []huffCode

// huffDecoder decodes prefix-free books via per-length maps.
type huffDecoder struct {
	byLen  [33]map[uint32]int // symbol by codeword, per length
	maxLen int
}

func newHuffDecoder(b huffBook) *huffDecoder {
	d := &huffDecoder{}
	for sym, c := range b {
		if d.byLen[c.Len] == nil {
			d.byLen[c.Len] = map[uint32]int{}
		}
		d.byLen[c.Len][c.Code] = sym
		d.maxLen = max(d.maxLen, int(c.Len))
	}
	return d
}

// decode reads one codeword bit by bit.
func (d *huffDecoder) decode(r *bitReader) (int, bool) {
	var code uint32
	for n := 1; n <= d.maxLen; n++ {
		bit, ok := r.bit()
		if !ok {
			return 0, false
		}
		code = code<<1 | bit
		if sym, ok := d.byLen[n][code]; ok {
			return sym, true
		}
	}
	return 0, false
}

// bookDecoders holds lazy-built decoders for all generated books.
type bookDecoders struct {
	scalefactor *huffDecoder
	spectrum    [12]*huffDecoder
	// SBR envelope/noise decoders.
	tEnv15, fEnv15, tEnv30, fEnv30 *huffDecoder
	tNoise30                       *huffDecoder
	// Balance books unused here; built for table test coverage.
	tEnvBal15, fEnvBal15, tEnvBal30, fEnvBal30, tNoiseBal30 *huffDecoder
}

var (
	decodersOnce sync.Once
	sharedBooks  *bookDecoders
)

func decoders() *bookDecoders {
	decodersOnce.Do(func() {
		d := &bookDecoders{
			scalefactor: newHuffDecoder(scalefactorBook),
			tEnv15:      newHuffDecoder(sbrTEnv15),
			fEnv15:      newHuffDecoder(sbrFEnv15),
			tEnv30:      newHuffDecoder(sbrTEnv30),
			fEnv30:      newHuffDecoder(sbrFEnv30),
			tNoise30:    newHuffDecoder(sbrTNoise30),
			tEnvBal15:   newHuffDecoder(sbrTEnvBal15),
			fEnvBal15:   newHuffDecoder(sbrFEnvBal15),
			tEnvBal30:   newHuffDecoder(sbrTEnvBal30),
			fEnvBal30:   newHuffDecoder(sbrFEnvBal30),
			tNoiseBal30: newHuffDecoder(sbrTNoiseBal30),
		}
		for cb := 1; cb <= 11; cb++ {
			d.spectrum[cb] = newHuffDecoder(spectrumBooks[cb])
		}
		sharedBooks = d
	})
	return sharedBooks
}
