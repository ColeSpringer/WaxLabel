package mpeg4audio

import "errors"

// FrameConfig is the static configuration a raw data block is parsed under.
type FrameConfig struct {
	ObjectType    int // 2 (AAC LC) is the only type parsed; others return ErrUnsupported
	SampleRateIdx int // 0..12
	ChannelConfig int
}

// FrameInfo is what one raw data block revealed. Parametric stereo is not among it: that
// lives inside the SBR payload, which [ParseSBRSingleChannel] reads.
type FrameInfo struct {
	SBR        bool   // an SBR fill element followed a channel element
	SBRPayload []byte // the SBR extension payload of the first single channel element, for the SBR parser
	SBRBits    int    // the payload's length in bits, which need not be a whole number of bytes
	SBRCRC     bool   // it was the EXT_SBR_DATA_CRC form
}

// ErrUnsupported marks a block whose syntax this parser does not walk: an object type other
// than AAC LC, a coupling channel element, or a tool (prediction, gain control) that only
// the Main and SSR profiles use. It is not a corrupt frame; nothing can be concluded from it.
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

// maxElements bounds the element walk. A raw data block holds at most one element per
// channel plus fill and data elements; sixty-four is far past any real frame and stops a
// crafted block from looping.
const maxElements = 64

// ParseRawDataBlock parses one AAC raw_data_block (ISO/IEC 14496-3 4.4.2). It succeeds only
// when the block ends exactly on the END element followed by byte alignment, so a table or
// syntax error surfaces as a failure rather than a wrong answer.
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
	// sceStart is the bit position of the single channel element whose SBR payload is
	// worth keeping, or -1 once one has been kept or none is pending.
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
			// A data or program config element between a channel element and a fill element
			// breaks the adjacency that makes the fill that channel's SBR payload, so the
			// pairing is dropped rather than carried across it.
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
			// A second fill element still follows the channel element the first one did.
		}
	}
	return errInvalidBlock
}

// cpe reads a channel_pair_element: the shared ics_info when common_window is set, the
// mid/side mask that follows it, then both channels' streams.
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
			// One bit per coded band per window group; the group lengths themselves do not
			// enter, only how many groups there are.
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

// dse reads a data_stream_element, whose payload is opaque to this parser.
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

// pce reads a program_config_element (4.4.1.1). Its layout is walked only to find its end:
// the element describes a channel mapping this parser does not need.
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
	// mono_mixdown, stereo_mixdown and matrix_mixdown each add an element number when present.
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

// fil reads a fill_element. Only the SBR extension payloads matter here, and only when the
// element follows a channel element, which is what makes them that channel's SBR data; the
// rest of the payload types are skipped whole by the count the element declares.
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

// copyBits returns n bits of b starting at bit offset from, left aligned in a fresh slice,
// so a payload that does not start on a byte boundary can be handed to a parser of its own.
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
