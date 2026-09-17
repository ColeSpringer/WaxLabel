package mpeg4audio

// icsInfo is ics_info() window layout (4.4.6).
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

// icsInfo reads ics_info(). Predictor => ErrUnsupported (Main profile).
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

// shortWindowGroups decodes scale_factor_grouping into group window counts.
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

// section is one section_data() band run.
type section struct {
	codebook   int
	start, end int // band range, [start, end)
}

// ics reads individual_channel_stream(). commonWindow reuses shared ics_info.
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

// sectionData reads section_data(); returns runs and per-band codebooks.
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

// ZERO skips bands; NOISE has PCM start; intensity books 14/15 use Huffman deltas.
const (
	cbZero  = 0
	cbNoise = 13
)

// scaleFactorData reads scale_factor_data().
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

// pulseData reads pulse_data().
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

// tnsData reads tns_data().
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

// spectrumParams is Table 4.151 per-codebook decode parameters.
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

// cbEscape book escapes at escapeValue (unsigned).
const (
	cbEscape    = 11
	escapeValue = 16
)

// maxEscapePrefix bounds escape leading ones (legal escape < 8192).
const maxEscapePrefix = 16

// spectralData walks spectral_data() to element end (no reconstruction).
func (p *blockParser) spectralData(info *icsInfo, sections [][]section) error {
	books := decoders()
	for g, groupLen := range info.groupLengths {
		for _, s := range sections[g] {
			cb := s.codebook
			if cb == cbZero || cb >= 12 {
				continue // no spectrum in ZERO/NOISE/intensity/reserved 12
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

// spectrumValues reads sign bits then escapes (syntax order: all signs, then all escapes).
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
		// Escape: ones, zero, then n+4 magnitude bits.
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
