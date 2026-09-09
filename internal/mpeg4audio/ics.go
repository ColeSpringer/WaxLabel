package mpeg4audio

// icsInfo is what an ics_info() (ISO/IEC 14496-3 4.4.6) declares about one channel's window
// layout: how many scalefactor bands are coded, how the eight short windows are grouped, and
// which band offsets apply.
type icsInfo struct {
	eightShort   bool
	maxSfb       int
	groupLengths []int    // windows per group, one entry per group
	swbOffset    []uint16 // band offsets for this window shape, ending with the window length
}

// numWindows is the total number of windows across every group.
func (i *icsInfo) numWindows() int {
	n := 0
	for _, l := range i.groupLengths {
		n += l
	}
	return n
}

// windowSequence values (Table 4.128). Only EIGHT_SHORT_SEQUENCE changes the band layout.
const eightShortSequence = 2

// icsInfo reads an ics_info(). AAC LC has no predictor, so a config that declares one is a
// Main-profile stream this parser does not walk.
func (p *blockParser) icsInfo() (*icsInfo, error) {
	r := p.r
	if !r.skip(1) { // ics_reserved_bit
		return nil, errInvalidBlock
	}
	windowSequence, ok := r.read(2)
	if !ok || !r.skip(1) { // window_shape
		return nil, errInvalidBlock
	}
	out := &icsInfo{eightShort: windowSequence == eightShortSequence}
	if out.eightShort {
		maxSfb, ok1 := r.read(4)
		grouping, ok2 := r.read(7)
		if !ok1 || !ok2 {
			return nil, errInvalidBlock
		}
		out.maxSfb = maxSfb
		out.groupLengths = shortWindowGroups(grouping)
		out.swbOffset = swbOffsetShort[p.cfg.SampleRateIdx]
	} else {
		maxSfb, ok := r.read(6)
		if !ok {
			return nil, errInvalidBlock
		}
		predictor, ok := r.bit()
		if !ok {
			return nil, errInvalidBlock
		}
		if predictor == 1 {
			return nil, ErrUnsupported // predictor_data_present: AAC Main only
		}
		out.maxSfb = maxSfb
		out.groupLengths = []int{1}
		out.swbOffset = swbOffsetLong[p.cfg.SampleRateIdx]
	}
	if out.maxSfb > len(out.swbOffset)-1 {
		return nil, errInvalidBlock
	}
	return out, nil
}

// shortWindowGroups turns the seven scale_factor_grouping bits into the window count of each
// group. Window 0 opens the first group; bit 6-i of the field says whether window i+1 joins
// the group in progress or opens a new one.
func shortWindowGroups(grouping int) []int {
	groups := []int{1}
	for i := range 7 {
		if grouping>>(6-i)&1 == 1 {
			groups[len(groups)-1]++
		} else {
			groups = append(groups, 1)
		}
	}
	return groups
}

// section is one run of scalefactor bands sharing a codebook, as section_data() codes it.
type section struct {
	codebook   int
	start, end int // band range, [start, end)
}

// ics reads an individual_channel_stream() for AAC LC. A channel pair with a common window
// passes the shared ics_info in rather than reading its own.
func (p *blockParser) ics(commonWindow bool, shared *icsInfo) error {
	r := p.r
	if !r.skip(8) { // global_gain
		return errInvalidBlock
	}
	info := shared
	if !commonWindow {
		var err error
		info, err = p.icsInfo()
		if err != nil {
			return err
		}
	}
	if info == nil {
		return errInvalidBlock
	}
	sections, bandBooks, err := p.sectionData(info)
	if err != nil {
		return err
	}
	if err := p.scaleFactorData(info, bandBooks); err != nil {
		return err
	}
	pulse, ok := r.bit()
	if !ok {
		return errInvalidBlock
	}
	if pulse == 1 {
		if err := p.pulseData(); err != nil {
			return err
		}
	}
	tns, ok := r.bit()
	if !ok {
		return errInvalidBlock
	}
	if tns == 1 {
		if err := p.tnsData(info); err != nil {
			return err
		}
	}
	gain, ok := r.bit()
	if !ok {
		return errInvalidBlock
	}
	if gain == 1 {
		return ErrUnsupported // gain_control_data_present: SSR only
	}
	return p.spectralData(info, sections)
}

// sectionData reads section_data(), the run-length coded map from scalefactor band to
// codebook. It returns the runs in order per group and the per-band codebook the scalefactor
// walk needs.
func (p *blockParser) sectionData(info *icsInfo) ([][]section, [][]int, error) {
	sectBits, esc := 5, 31
	if info.eightShort {
		sectBits, esc = 3, 7
	}
	sections := make([][]section, len(info.groupLengths))
	bandBooks := make([][]int, len(info.groupLengths))
	for g := range info.groupLengths {
		bandBooks[g] = make([]int, info.maxSfb)
		for k := 0; k < info.maxSfb; {
			cb, ok := p.r.read(4)
			if !ok {
				return nil, nil, errInvalidBlock
			}
			n := 0
			for {
				inc, ok := p.r.read(sectBits)
				if !ok {
					return nil, nil, errInvalidBlock
				}
				n += inc
				if inc != esc {
					break
				}
				if k+n > info.maxSfb {
					return nil, nil, errInvalidBlock
				}
			}
			if n == 0 || k+n > info.maxSfb {
				return nil, nil, errInvalidBlock
			}
			sections[g] = append(sections[g], section{codebook: cb, start: k, end: k + n})
			for sfb := k; sfb < k+n; sfb++ {
				bandBooks[g][sfb] = cb
			}
			k += n
		}
	}
	return sections, bandBooks, nil
}

// Codebook numbers with their own scalefactor rule: ZERO codes no band at all, and NOISE
// carries a PCM start value for its first band. The two intensity books (14 and 15) read one
// Huffman-coded delta per band like an ordinary one.
const (
	cbZero  = 0
	cbNoise = 13
)

// scaleFactorData reads scale_factor_data(): one scalefactor codebook symbol per coded band,
// with the noise codebook's first band carrying a nine-bit PCM value instead.
func (p *blockParser) scaleFactorData(info *icsInfo, bandBooks [][]int) error {
	dec := decoders().scalefactor
	noisePCMRead := false
	for g := range info.groupLengths {
		for sfb := range info.maxSfb {
			switch cb := bandBooks[g][sfb]; {
			case cb == cbZero:
				continue
			case cb == cbNoise && !noisePCMRead:
				if !p.r.skip(9) {
					return errInvalidBlock
				}
				noisePCMRead = true
			default:
				if _, ok := dec.decode(p.r); !ok {
					return errInvalidBlock
				}
			}
		}
	}
	return nil
}

// pulseData reads pulse_data(), the optional pulse escape for a long window.
func (p *blockParser) pulseData() error {
	n, ok := p.r.read(2)
	if !ok || !p.r.skip(6) { // pulse_start_sfb
		return errInvalidBlock
	}
	if !p.r.skip((n + 1) * 9) { // pulse_offset(5) and pulse_amp(4) per pulse
		return errInvalidBlock
	}
	return nil
}

// tnsData reads tns_data(), the temporal noise shaping filters.
func (p *blockParser) tnsData(info *icsInfo) error {
	nFiltBits, lenBits, orderBits := 2, 6, 5
	if info.eightShort {
		nFiltBits, lenBits, orderBits = 1, 4, 3
	}
	for range info.numWindows() {
		nFilt, ok := p.r.read(nFiltBits)
		if !ok {
			return errInvalidBlock
		}
		coefRes := 0
		if nFilt > 0 {
			res, ok := p.r.bit()
			if !ok {
				return errInvalidBlock
			}
			coefRes = int(res)
		}
		for range nFilt {
			if !p.r.skip(lenBits) {
				return errInvalidBlock
			}
			order, ok := p.r.read(orderBits)
			if !ok {
				return errInvalidBlock
			}
			if order == 0 {
				continue
			}
			if !p.r.skip(1) { // direction
				return errInvalidBlock
			}
			compress, ok := p.r.bit()
			if !ok {
				return errInvalidBlock
			}
			if !p.r.skip(order * (3 + coefRes - int(compress))) {
				return errInvalidBlock
			}
		}
	}
	return nil
}

// spectrumParams are the per-codebook decoding parameters of Table 4.151: how many
// coefficients one codeword carries, whether the values need a sign bit, and the modulus and
// offset that unpack a symbol into signed values.
var spectrumParams = [12]struct {
	width    int
	unsigned bool
	mod, off int
}{
	1:  {4, false, 3, 1},
	2:  {4, false, 3, 1},
	3:  {4, true, 3, 0},
	4:  {4, true, 3, 0},
	5:  {2, false, 9, 4},
	6:  {2, false, 9, 4},
	7:  {2, true, 8, 0},
	8:  {2, true, 8, 0},
	9:  {2, true, 13, 0},
	10: {2, true, 13, 0},
	11: {2, true, 17, 0},
}

// cbEscape is the codebook whose largest magnitude escapes to a variable-length value, and
// escapeValue is that magnitude. The book is unsigned, so the value is never negative here.
const (
	cbEscape    = 11
	escapeValue = 16
)

// maxEscapePrefix bounds the escape sequence's leading ones. The escape codes a value below
// 8192, so a prefix past this length cannot describe a legal coefficient.
const maxEscapePrefix = 16

// spectralData reads spectral_data(), the quantized coefficients. Nothing is reconstructed:
// the walk exists to land exactly on the end of the element, which is what proves the block
// was read correctly.
func (p *blockParser) spectralData(info *icsInfo, sections [][]section) error {
	books := decoders()
	for g, groupLen := range info.groupLengths {
		for _, s := range sections[g] {
			cb := s.codebook
			if cb == cbZero || cb >= 12 {
				continue // ZERO, the reserved 12, NOISE and the intensity books carry no spectrum
			}
			params := spectrumParams[cb]
			coefficients := (int(info.swbOffset[s.end]) - int(info.swbOffset[s.start])) * groupLen
			if coefficients < 0 {
				return errInvalidBlock
			}
			dec := books.spectrum[cb]
			for k := 0; k < coefficients; k += params.width {
				sym, ok := dec.decode(p.r)
				if !ok {
					return errInvalidBlock
				}
				if err := p.spectrumValues(cb, params.width, params.unsigned, params.mod, params.off, sym); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// spectrumValues consumes what a codeword's values call for after the codeword itself: the
// sign bits of every non-zero value, all of them together, and then the escape sequences of
// any value at the escape codebook's largest magnitude. The order is the syntax's own -
// every sign, then every escape - and not sign-and-escape per value.
func (p *blockParser) spectrumValues(cb, width int, unsigned bool, mod, off, sym int) error {
	var values [4]int
	if width == 4 {
		values[0] = sym/(mod*mod*mod) - off
		values[1] = sym/(mod*mod)%mod - off
		values[2] = sym/mod%mod - off
		values[3] = sym%mod - off
	} else {
		values[0] = sym/mod - off
		values[1] = sym%mod - off
	}
	if unsigned {
		for _, v := range values[:width] {
			if v != 0 && !p.r.skip(1) {
				return errInvalidBlock
			}
		}
	}
	if cb != cbEscape {
		return nil
	}
	for _, v := range values[:width] {
		if v != escapeValue {
			continue
		}
		// A run of ones, a zero, then that many plus four bits of magnitude.
		n := 4
		for {
			bit, ok := p.r.bit()
			if !ok {
				return errInvalidBlock
			}
			if bit == 0 {
				break
			}
			n++
			if n > maxEscapePrefix {
				return errInvalidBlock
			}
		}
		if !p.r.skip(n) {
			return errInvalidBlock
		}
	}
	return nil
}
