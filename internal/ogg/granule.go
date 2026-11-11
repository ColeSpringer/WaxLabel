package ogg

import (
	"bytes"
	"fmt"
	"math"
	mathbits "math/bits"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/flac"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// A granule position counts the samples up to the end of the last packet that ends on its page,
// so the first audio page's position less the samples of its packets is where the stream starts:
// 0 for a stream encoded from its first sample, more for one cut from a longer stream. The
// length of a stream is its last position less that start (RFC 7845 section 4, libvorbisfile).

// maxStartPages bounds the audio pages read for the start: the first position is on the first
// page a packet ends on, and a packet runs over few pages.
const maxStartPages = 8

// opusPacketSamples returns the length of an Opus packet in 48 kHz samples, from its TOC byte
// (RFC 6716 section 3.1). ok is false for a packet no decoder takes: an empty one, a frame count
// of 0, or more than 120 ms.
func opusPacketSamples(pkt []byte) (samples int, ok bool) {
	if len(pkt) == 0 {
		return 0, false
	}
	var frame int
	switch config := pkt[0] >> 3; {
	case config < 12: // SILK: 10, 20, 40, 60 ms
		frame = [...]int{480, 960, 1920, 2880}[config&3]
	case config < 16: // hybrid: 10, 20 ms
		frame = 480 << (config & 1)
	default: // CELT: 2.5, 5, 10, 20 ms
		frame = 120 << (config & 3)
	}
	frames := 2
	switch pkt[0] & 3 {
	case 0:
		frames = 1
	case 3:
		if len(pkt) < 2 {
			return 0, false
		}
		frames = int(pkt[1] & 0x3F)
	}
	if samples = frame * frames; frames == 0 || samples > 5760 {
		return 0, false
	}
	return samples, true
}

var vorbisSetupID = []byte("\x05vorbis")

// vorbisModeBlockFlags returns each mode's block flag from a Vorbis setup header: whether its
// packets are long blocks. The mode table closes the header, so it is read from the end, as FFmpeg
// and liboggz do, not through the codebooks: the framing bit, then 41-bit modes (flag, zero window
// and transform type, mapping) while well-formed, the longest run its 6-bit count agrees with.
func vorbisModeBlockFlags(setup []byte) ([]bool, bool) {
	if !bytes.HasPrefix(setup, vorbisSetupID) {
		return nil, false
	}
	// Vorbis packs bits low bit first.
	field := func(lo, n int) (v uint64) {
		for i := range n {
			v |= uint64(setup[(lo+i)/8]>>((lo+i)%8)&1) << i
		}
		return v
	}
	end := len(setup)
	for setup[end-1] == 0 { // the signature ends the run of zeros
		end--
	}
	framing := (end-1)*8 + mathbits.Len8(setup[end-1]) - 1
	const modeBits, countBits = 1 + 16 + 16 + 8, 6
	floor := len(vorbisSetupID)*8 + countBits
	count := 0
	for n, lo := 1, framing-modeBits; n <= 64 && lo >= floor; n, lo = n+1, lo-modeBits {
		if field(lo+1, 32) != 0 || field(lo+33, 8) > 63 {
			break
		}
		if int(field(lo-countBits, countBits))+1 == n {
			count = n
		}
	}
	if count == 0 {
		return nil, false
	}
	flags := make([]bool, count)
	for i := range flags {
		flags[i] = field(framing-modeBits*(count-i), 1) == 1
	}
	return flags, true
}

// headLen is how much of a packet the measuring takes: the longest FLAC frame header.
const headLen = 16

// packetSamples returns the function that gives the samples each audio packet of d adds to the
// granule position, from the packet's first bytes, in stream order; nil when d's packets cannot be
// measured. ok is false for a packet that leaves the count unknown; one a decoder skips adds 0.
func (d *doc) packetSamples() func(head []byte) (n uint64, ok bool) {
	switch d.kind {
	case kindOpus:
		return func(head []byte) (uint64, bool) {
			n, _ := opusPacketSamples(head)
			return uint64(n), true
		}
	case kindFLAC:
		si, err := vorbis.ParseStreamInfo(d.streamInfo())
		if err != nil {
			return nil
		}
		// A packet is a frame, and its header states its block size.
		return func(head []byte) (uint64, bool) {
			n, ok := flac.FrameSamples(head, si)
			return uint64(n), ok
		}
	case kindVorbis:
		flags, ok := vorbisModeBlockFlags(d.setupPacket)
		if !ok || len(d.idPacket) < 29 {
			return nil
		}
		// Block sizes: two exponents in one byte of the identification header.
		short, long := uint64(1)<<(d.idPacket[28]&0x0F), uint64(1)<<(d.idPacket[28]>>4)
		modeMask := byte(1)<<mathbits.Len(uint(len(flags)-1)) - 1
		// A packet adds a quarter of its block and the one before it; the first adds nothing.
		prev := uint64(0)
		return func(head []byte) (uint64, bool) {
			if len(head) == 0 || head[0]&1 != 0 {
				return 0, true // not an audio packet
			}
			mode := int(head[0] >> 1 & modeMask)
			if mode >= len(flags) {
				return 0, true
			}
			block := short
			if flags[mode] {
				block = long
			}
			var n uint64
			if prev != 0 {
				n = (prev + block) / 4
			}
			prev = block
			return n, true
		}
	}
	return nil
}

// startScan adds up the packets of the first audio pages, up to the first page a packet ends on,
// to find the granule position the stream starts at. It reads the first bytes of each packet.
type startScan struct {
	src     core.ReaderAtSized
	samples func(head []byte) (uint64, bool)
	pages   int
	total   uint64        // samples of the packets that ended so far
	buf     [headLen]byte // first bytes of the packet being read
	head    []byte        // nil for a packet whose start or end is gone
	inside  bool          // a packet is being read
	done    bool
}

// add takes the next audio page of the stream and returns the start once the page's granule
// position gives it; 0 until then, and for good once a packet cannot be measured.
func (s *startScan) add(p rawPage) (uint64, error) {
	s.pages++
	if continued := p.flags&flagContinued != 0; continued != s.inside {
		// The page does not go on from the one before it: the packet is missing its start or
		// its end, and decoders drop it.
		s.inside, s.head = continued, nil
	}
	at := p.bodyOff()
	for _, lacing := range p.segs {
		if !s.inside {
			s.inside = true
			s.head = s.buf[:min(int(lacing), len(s.buf))]
			if _, err := bits.FillWithin(s.src, s.head, at); err != nil {
				return 0, fmt.Errorf("read Ogg page at %d: %w", p.off, err)
			}
		}
		at += int64(lacing)
		if lacing == maxSegments {
			continue
		}
		s.inside = false
		if s.head == nil {
			continue
		}
		n, ok := s.samples(s.head)
		if !ok {
			s.done = true
			return 0, nil
		}
		s.total += n
	}
	if p.granule == math.MaxUint64 {
		s.done = s.pages >= maxStartPages
		return 0, nil
	}
	s.done = true
	// A position short of the packets is an end trim, or damage: the stream starts at 0.
	if p.granule < s.total {
		return 0, nil
	}
	return p.granule - s.total, nil
}
