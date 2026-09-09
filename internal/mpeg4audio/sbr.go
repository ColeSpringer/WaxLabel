package mpeg4audio

// This file walks the SBR extension payload of a single channel element far enough to see
// whether its extended data carries the parametric stereo extension, which is what makes an
// implicitly signalled HE-AAC stream HE-AAC v2. Nothing is reconstructed; the walk exists so
// the extension flag is read from where the specification puts it rather than guessed.

// sbrHeader is what an sbr_header() declares. The fields not read here (the limiter and
// smoothing settings) affect only reconstruction, so they are skipped rather than stored.
type sbrHeader struct {
	ampRes     uint32
	startFreq  int
	stopFreq   int
	xoverBand  int
	freqScale  int
	alterScale uint32
	noiseBands int
}

// SBRState carries the header across frames; a frame without a header reuses the last one.
// A caller walking a stream keeps one per element and passes it to every frame.
type SBRState struct {
	header    sbrHeader
	bands     sbrBands
	hasHeader bool
}

// extensionIDPS is the bs_extension_id of the parametric stereo extension (Table 4.112).
const extensionIDPS = 2

// maxEnvelopes is the envelope count 4.6.18.3.6 allows in one frame: five for VARVAR, and
// four for FIXFIX, which grid() enforces separately since only that class can spell more. A
// grid declaring more is a payload this parser did not read correctly.
const maxEnvelopes = 5

// ParseSBRSingleChannel parses the sbr_extension_data of a single channel element (the bits
// after the extension_type nibble) and reports whether its extended data carries the
// parametric stereo extension. ok is false when the payload does not parse or no header has
// been seen yet.
//
// payloadBits is the whole fill element after the nibble, which can hold further extension
// payloads behind the SBR one, so the walk is not expected to reach its end - and since
// sbr_extension_data pads itself to a byte boundary, where it lands is not itself evidence.
// What catches a misread is the walk running out of payload: a wrong band count or a wrong
// grid sends the Huffman decoder past the end, and the header derivation refuses a frequency
// range the QMF bank cannot hold. A stream is read frame by frame against one shared state,
// so a misread that survives one frame rarely survives the next.
func ParseSBRSingleChannel(payload []byte, payloadBits int, crc bool, sbrRateIdx int, st *SBRState) (ps bool, ok bool) {
	if st == nil || payloadBits <= 0 || payloadBits > len(payload)*8 {
		return false, false
	}
	fsSBR := SampleRate(sbrRateIdx)
	if fsSBR == 0 {
		return false, false
	}
	p := &sbrParser{r: &bitReader{b: payload[:(payloadBits+7)/8]}, bits: payloadBits, fs: fsSBR, st: st}
	if crc && !p.r.skip(10) {
		return false, false
	}
	headerFlag, okBit := p.r.bit()
	if !okBit {
		return false, false
	}
	if headerFlag == 1 && !p.readHeader() {
		return false, false
	}
	if !st.hasHeader {
		return false, false // a frame that never carried a header cannot be sized
	}
	ps, okData := p.singleChannelElement()
	if !okData || p.r.overrun || p.r.position() > payloadBits {
		return false, false
	}
	return ps, true
}

type sbrParser struct {
	r    *bitReader
	bits int
	fs   int
	st   *SBRState
}

// readHeader reads an sbr_header() and recomputes the band tables it implies.
func (p *sbrParser) readHeader() bool {
	r := p.r
	ampRes, ok1 := r.bit()
	startFreq, ok2 := r.read(4)
	stopFreq, ok3 := r.read(4)
	xover, ok4 := r.read(3)
	if !(ok1 && ok2 && ok3 && ok4) || !r.skip(2) { // bs_reserved
		return false
	}
	h := sbrHeader{ampRes: ampRes, startFreq: startFreq, stopFreq: stopFreq, xoverBand: xover,
		freqScale: 2, alterScale: 1, noiseBands: 2}
	extra1, ok5 := r.bit()
	extra2, ok6 := r.bit()
	if !(ok5 && ok6) {
		return false
	}
	if extra1 == 1 {
		freqScale, okA := r.read(2)
		alterScale, okB := r.bit()
		noiseBands, okC := r.read(2)
		if !(okA && okB && okC) {
			return false
		}
		h.freqScale, h.alterScale, h.noiseBands = freqScale, alterScale, noiseBands
	}
	if extra2 == 1 && !r.skip(6) { // limiter bands, limiter gains, interpol freq, smoothing
		return false
	}
	bands, ok := deriveBands(h, p.fs)
	if !ok {
		return false
	}
	p.st.header, p.st.bands, p.st.hasHeader = h, bands, true
	return true
}

// singleChannelElement reads an sbr_single_channel_element() (Table 4.65).
func (p *sbrParser) singleChannelElement() (ps bool, ok bool) {
	r := p.r
	extra, okBit := r.bit()
	if !okBit {
		return false, false
	}
	if extra == 1 && !r.skip(4) { // bs_reserved
		return false, false
	}
	g, ok := p.grid()
	if !ok {
		return false, false
	}
	dfEnv, dfNoise, ok := p.dtdf(g)
	if !ok {
		return false, false
	}
	if !r.skip(2 * p.st.bands.numNoise) { // sbr_invf
		return false, false
	}
	if !p.envelope(g, dfEnv) {
		return false, false
	}
	if !p.noise(g, dfNoise) {
		return false, false
	}
	addHarmonic, okBit := r.bit()
	if !okBit {
		return false, false
	}
	if addHarmonic == 1 && !r.skip(p.st.bands.numHigh) { // sbr_sinusoidal_coding
		return false, false
	}
	extended, okBit := r.bit()
	if !okBit {
		return false, false
	}
	if extended == 0 {
		return false, true
	}
	return p.extendedData()
}

// sbrGrid is what an sbr_grid() declares: how many envelopes and noise floors the frame
// carries, each envelope's frequency resolution, and the amplitude resolution in force,
// which a single-envelope FIXFIX frame forces to zero whatever the header said.
type sbrGrid struct {
	numEnv   int
	numNoise int
	freqRes  [maxEnvelopes]uint32
	ampRes   uint32
}

// Frame classes (bs_frame_class).
const (
	frameFixFix = 0
	frameFixVar = 1
	frameVarFix = 2
	frameVarVar = 3
)

func (p *sbrParser) grid() (sbrGrid, bool) {
	r := p.r
	var g sbrGrid
	g.ampRes = p.st.header.ampRes
	class, ok := r.read(2)
	if !ok {
		return g, false
	}
	switch class {
	case frameFixFix:
		tmp, ok := r.read(2)
		// bs_num_env is 2^tmp, so the field can spell 8 - but 4.6.18.3.6 caps a FIXFIX
		// frame at four envelopes, so 8 is a value no conforming stream carries and this
		// walk cannot size. Refusing it is the requirement, not an oversight.
		if !ok || tmp == 3 {
			return g, false
		}
		g.numEnv = 1 << tmp
		if g.numEnv == 1 {
			g.ampRes = 0
		}
		res, ok := r.bit()
		if !ok {
			return g, false
		}
		for i := range g.numEnv {
			g.freqRes[i] = res
		}
	case frameFixVar, frameVarFix:
		if !r.skip(2) { // the variable border
			return g, false
		}
		numRel, ok := r.read(2)
		if !ok {
			return g, false
		}
		g.numEnv = numRel + 1
		if !r.skip(2 * numRel) { // the relative borders
			return g, false
		}
		// FIXVAR writes its resolutions from the last envelope back to the first, since its
		// variable border is the frame's trailing edge; VARFIX writes them in order.
		if !p.readPointerAndRes(&g, class == frameFixVar) {
			return g, false
		}
	case frameVarVar:
		if !r.skip(4) { // both variable borders
			return g, false
		}
		numRel0, ok1 := r.read(2)
		numRel1, ok2 := r.read(2)
		if !(ok1 && ok2) {
			return g, false
		}
		g.numEnv = numRel0 + numRel1 + 1
		if g.numEnv > maxEnvelopes || !r.skip(2*(numRel0+numRel1)) {
			return g, false
		}
		if !p.readPointerAndRes(&g, false) {
			return g, false
		}
	}
	if g.numEnv < 1 || g.numEnv > maxEnvelopes {
		return g, false
	}
	g.numNoise = 1
	if g.numEnv > 1 {
		g.numNoise = 2
	}
	return g, true
}

// readPointerAndRes reads bs_pointer and the per-envelope frequency resolutions the three
// variable frame classes share. reverse assigns them from the last envelope backwards, which
// is how FIXVAR stores them.
func (p *sbrParser) readPointerAndRes(g *sbrGrid, reverse bool) bool {
	if g.numEnv < 1 || g.numEnv > maxEnvelopes {
		return false
	}
	if !p.r.skip(ceilLog2(g.numEnv + 1)) { // bs_pointer
		return false
	}
	for i := range g.numEnv {
		res, ok := p.r.bit()
		if !ok {
			return false
		}
		if reverse {
			g.freqRes[g.numEnv-1-i] = res
		} else {
			g.freqRes[i] = res
		}
	}
	return true
}

// ceilLog2 is the specification's ceil(log2(n)) for the bs_pointer width.
func ceilLog2(n int) int {
	bits := 0
	for 1<<bits < n {
		bits++
	}
	return bits
}

// dtdf reads sbr_dtdf(): whether each envelope and noise floor is coded against time or
// against frequency, which decides its codebook and whether it opens with a start value.
func (p *sbrParser) dtdf(g sbrGrid) (dfEnv, dfNoise [maxEnvelopes]uint32, ok bool) {
	for i := range g.numEnv {
		v, okBit := p.r.bit()
		if !okBit {
			return dfEnv, dfNoise, false
		}
		dfEnv[i] = v
	}
	for i := range g.numNoise {
		v, okBit := p.r.bit()
		if !okBit {
			return dfEnv, dfNoise, false
		}
		dfNoise[i] = v
	}
	return dfEnv, dfNoise, true
}

// envelope reads sbr_envelope() for an uncoupled channel.
func (p *sbrParser) envelope(g sbrGrid, dfEnv [maxEnvelopes]uint32) bool {
	books := decoders()
	tHuff, fHuff := books.tEnv15, books.fEnv15
	startBits := 7
	if g.ampRes == 1 {
		tHuff, fHuff, startBits = books.tEnv30, books.fEnv30, 6
	}
	for e := range g.numEnv {
		n := p.st.bands.numEnvBands(g.freqRes[e])
		if n <= 0 {
			return false
		}
		if dfEnv[e] == 0 {
			if !p.r.skip(startBits) {
				return false
			}
			if !decodeRun(fHuff, p.r, n-1) {
				return false
			}
			continue
		}
		if !decodeRun(tHuff, p.r, n) {
			return false
		}
	}
	return true
}

// noise reads sbr_noise() for an uncoupled channel. The frequency-direction noise book is
// the envelope's own 3.0 dB book, which is what Table 4.A.78 says f_huffman_noise_3_0dB is.
func (p *sbrParser) noise(g sbrGrid, dfNoise [maxEnvelopes]uint32) bool {
	books := decoders()
	n := p.st.bands.numNoise
	for f := range g.numNoise {
		if dfNoise[f] == 0 {
			if !p.r.skip(5) { // bs_noise_start_value_level
				return false
			}
			if !decodeRun(books.fEnv30, p.r, n-1) {
				return false
			}
			continue
		}
		if !decodeRun(books.tNoise30, p.r, n) {
			return false
		}
	}
	return true
}

func decodeRun(d *huffDecoder, r *bitReader, n int) bool {
	for range n {
		if _, ok := d.decode(r); !ok {
			return false
		}
	}
	return true
}

// extendedData reads the bs_extended_data block and reports whether its first extension is
// the parametric stereo one. Only the first id is read, which is all the question needs: an
// extension that is not parametric stereo consumes the rest of the block as fill (8.A.2), so
// no other extension can follow it, and one that is parametric stereo has already answered.
// The block declares its own byte count, so skipping it needs no per-extension length.
func (p *sbrParser) extendedData() (ps bool, ok bool) {
	cnt, okRead := p.r.read(4)
	if !okRead {
		return false, false
	}
	if cnt == 15 {
		esc, okEsc := p.r.read(8)
		if !okEsc {
			return false, false
		}
		cnt += esc
	}
	end := p.r.position() + 8*cnt
	if cnt > 0 {
		id, okID := p.r.read(2)
		if !okID {
			return false, false
		}
		ps = id == extensionIDPS
	}
	if !p.r.seek(end) {
		return false, false
	}
	return ps, true
}
