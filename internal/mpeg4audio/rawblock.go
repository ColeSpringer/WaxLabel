package mpeg4audio

import "errors"

// FrameConfig is the static configuration a raw data block is parsed under.
type FrameConfig struct {
	ObjectType    int // 2 (AAC LC) is the only type parsed; others return ErrUnsupported
	SampleRateIdx int // 0..12
	ChannelConfig int
}

// FrameInfo from one raw data block. PS is in SBR payload ([ParseSBRSingleChannel]).
type FrameInfo struct {
	SBR        bool   // an SBR fill element followed a channel element
	SBRPayload []byte // the SBR extension payload of the first single channel element, for the SBR parser
	SBRBits    int    // the payload's length in bits, which need not be a whole number of bytes
	SBRCRC     bool   // it was the EXT_SBR_DATA_CRC form
}

// ErrUnsupported: syntax not walked (non-LC AOT, CCE, Main/SSR tools). Not corruption.
var ErrUnsupported = errors.New("mpeg4audio: unsupported syntax")

// errInvalidBlock marks a block whose syntax this parser walked and found inconsistent.
var errInvalidBlock = errors.New("mpeg4audio: malformed raw data block")

// Syntactic element identifiers (ISO/IEC 14496-3 Table 4.71).
const (
	idSCE = 0
	idCPE = 1
	idCCE = 2
	idLFE = 3
	idDSE = 4
	idPCE = 5
	idFIL = 6
	idEND = 7
)

// The extension_type values of a fill element payload that carry SBR data.
const (
	extSBRData    = 0xD
	extSBRDataCRC = 0xE
)

// maxElements caps element loop (64 >> real frames).
const maxElements = 64

// ParseRawDataBlock parses raw_data_block (4.4.2). Success requires exact END + alignment.
func ParseRawDataBlock(data []byte, cfg FrameConfig) (FrameInfo, error) {
	if cfg.ObjectType != 2 {
		return FrameInfo{}, ErrUnsupported
	}
	if cfg.SampleRateIdx < 0 || cfg.SampleRateIdx >= len(swbOffsetLong) {
		return FrameInfo{}, ErrUnsupported
	}
	p := &blockParser{r: &bitReader{b: data}, cfg: cfg}
	if err := p.run(); err != nil {
		return FrameInfo{}, err
	}
	return p.info, nil
}

type blockParser struct {
	r    *bitReader
	cfg  FrameConfig
	info FrameInfo
	// sceStart is SCE bit position for SBR capture, or -1.
	sceStart int
}

func (p *blockParser) run() error {
	p.sceStart = -1
	lastWasChannel := false
	for range maxElements {
		id, ok := p.r.read(3)
		if !ok {
			return errInvalidBlock
		}
		switch id {
		case idEND:
			p.r.alignByte()
			if p.r.overrun || p.r.position() != len(p.r.b)*8 {
				return errInvalidBlock
			}
			return nil
		case idSCE, idLFE:
			if _, ok := p.r.read(4); !ok { // element_instance_tag
				return errInvalidBlock
			}
			start := p.r.position()
			if err := p.ics(false, nil); err != nil {
				return err
			}
			if id == idSCE && p.info.SBRPayload == nil {
				p.sceStart = start
			}
			lastWasChannel = true
		case idCPE:
			if err := p.cpe(); err != nil {
				return err
			}
			p.sceStart = -1
			lastWasChannel = true
		case idCCE:
			return ErrUnsupported
		case idDSE:
			if err := p.dse(); err != nil {
				return err
			}
			// DSE/PCE between channel and fill breaks SBR adjacency.
			lastWasChannel = false
			p.sceStart = -1
		case idPCE:
			if err := p.pce(); err != nil {
				return err
			}
			lastWasChannel = false
			p.sceStart = -1
		case idFIL:
			if err := p.fil(lastWasChannel); err != nil {
				return err
			}
		}
	}
	return errInvalidBlock
}

// cpe reads channel_pair_element.
func (p *blockParser) cpe() error {
	if _, ok := p.r.read(4); !ok { // element_instance_tag
		return errInvalidBlock
	}
	common, ok := p.r.bit()
	if !ok {
		return errInvalidBlock
	}
	var shared *icsInfo
	if common == 1 {
		info, err := p.icsInfo()
		if err != nil {
			return err
		}
		shared = info
		msMask, ok := p.r.read(2)
		if !ok {
			return errInvalidBlock
		}
		if msMask == 1 {
			// MS mask: one bit per band per group.
			if !p.r.skip(len(shared.groupLengths) * shared.maxSfb) {
				return errInvalidBlock
			}
		}
	}
	if err := p.ics(common == 1, shared); err != nil {
		return err
	}
	return p.ics(common == 1, shared)
}

// dse skips data_stream_element payload.
func (p *blockParser) dse() error {
	if _, ok := p.r.read(4); !ok { // element_instance_tag
		return errInvalidBlock
	}
	align, ok := p.r.bit()
	if !ok {
		return errInvalidBlock
	}
	n, ok := p.r.read(8)
	if !ok {
		return errInvalidBlock
	}
	if n == 255 {
		extra, ok := p.r.read(8)
		if !ok {
			return errInvalidBlock
		}
		n += extra
	}
	if align == 1 {
		p.r.alignByte()
	}
	if !p.r.skip(8 * n) {
		return errInvalidBlock
	}
	return nil
}

// pce walks program_config_element to its end.
func (p *blockParser) pce() error {
	r := p.r
	if !r.skip(4 + 2) { // element_instance_tag, object_type
		return errInvalidBlock
	}
	if !r.skip(4) { // sampling_frequency_index
		return errInvalidBlock
	}
	front, ok1 := r.read(4)
	side, ok2 := r.read(4)
	back, ok3 := r.read(4)
	lfe, ok4 := r.read(2)
	assoc, ok5 := r.read(3)
	cc, ok6 := r.read(4)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
		return errInvalidBlock
	}
	// Optional mixdown element numbers.
	for _, width := range []int{4, 4, 3} {
		present, ok := r.bit()
		if !ok {
			return errInvalidBlock
		}
		if present == 1 && !r.skip(width) {
			return errInvalidBlock
		}
	}
	if !r.skip(5*(front+side+back) + 4*(lfe+assoc) + 5*cc) {
		return errInvalidBlock
	}
	r.alignByte()
	comment, ok := r.read(8)
	if !ok || !r.skip(8*comment) {
		return errInvalidBlock
	}
	return nil
}

// fil reads fill_element; captures SBR when adjacent after channel element.
func (p *blockParser) fil(lastWasChannel bool) error {
	cnt, ok := p.r.read(4)
	if !ok {
		return errInvalidBlock
	}
	if cnt == 15 {
		extra, ok := p.r.read(8)
		if !ok {
			return errInvalidBlock
		}
		cnt += extra - 1
	}
	start := p.r.position()
	end := start + 8*cnt
	if cnt > 0 {
		ext, ok := p.r.read(4)
		if !ok {
			return errInvalidBlock
		}
		if lastWasChannel && (ext == extSBRData || ext == extSBRDataCRC) {
			p.info.SBR = true
			if p.sceStart >= 0 && p.info.SBRPayload == nil {
				p.info.SBRPayload, p.info.SBRBits = copyBits(p.r.b, start+4, 8*cnt-4)
				p.info.SBRCRC = ext == extSBRDataCRC
				p.sceStart = -1
			}
		}
	}
	if !p.r.seek(end) {
		return errInvalidBlock
	}
	return nil
}

// copyBits extracts n bits from bit offset from, left-aligned in a new slice.
func copyBits(b []byte, from, n int) ([]byte, int) {
	if n <= 0 || from < 0 || from+n > len(b)*8 {
		return nil, 0
	}
	out := make([]byte, (n+7)/8)
	for i := range n {
		src := from + i
		if b[src>>3]>>(7-src&7)&1 == 1 {
			out[i>>3] |= 1 << (7 - i&7)
		}
	}
	return out, n
}
