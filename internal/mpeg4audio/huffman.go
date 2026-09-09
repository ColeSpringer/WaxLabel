package mpeg4audio

import "sync"

// huffCode is one entry of a codebook: the codeword's bit length and its value, right
// aligned. Symbol numbers are implicit in the table's order, matching the specification's
// index column.
type huffCode struct {
	Len  uint8
	Code uint32
}

// huffBook is a codebook indexed by symbol.
type huffBook []huffCode

// huffDecoder reads one codeword from a bit stream. The books are small and prefix-free, so
// a map per codeword length is both compact and enough: a codeword is recognized as soon as
// the bits read so far match an entry of that length, which for a prefix-free book is the
// only reading.
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

// decode reads one codeword bit by bit. ok is false at end of data or after maxLen bits
// with no match, which a prefix-free, complete book makes impossible for real data.
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

// bookDecoders holds a decoder for every generated codebook. They are built on first use
// rather than in an init, so a program that parses no AAC frames pays nothing for them.
type bookDecoders struct {
	scalefactor *huffDecoder
	spectrum    [12]*huffDecoder
	// The SBR envelope and noise-floor decoders, named after the specification's tables.
	tEnv15, fEnv15, tEnv30, fEnv30 *huffDecoder
	tNoise30                       *huffDecoder
	// The balance books are read only by sbr_channel_pair_element, which this package does
	// not parse; they are built anyway so the table tests cover every generated book.
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
