package wavpack

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Fixed 32-byte block header: "wvpk", size, version, 40-bit sample count and block
// index, block samples, flag word, decoded-data CRC.
const (
	blockMagic     = "wvpk"
	blockHeaderLen = 32
	// Supported stream version range.
	minVersion = 0x402
	maxVersion = 0x410
)

// Flag-word bits this package reports. Other bits are decoder state.
const (
	flagBytesPerSampleMask = 0x3
	flagMono               = 1 << 2
	flagHybrid             = 1 << 3
	flagFloat              = 1 << 7
	flagShiftShift         = 13
	flagShiftMask          = 0x1f
	flagRateIndexShift     = 23
	flagRateIndexMask      = 0xf
	flagInitialBlock       = 1 << 11
	flagFinalBlock         = 1 << 12
	flagDSD                = 1 << 31
	// totalSamplesUnknown: all-ones low uint32 when length was unknown at encode.
	// Check that field alone; the high byte is 0, so the assembled 40-bit value
	// is not the sentinel and would report ~27h of audio.
	totalSamplesUnknown = 1<<32 - 1
	// rateIndexUnknown: rate comes from an ID_SAMPLE_RATE sub-block.
	rateIndexUnknown = 15
)

// standardRates is the 4-bit rate-index table.
var standardRates = [15]int{
	6000, 8000, 9600, 11025, 12000, 16000, 22050, 24000,
	32000, 44100, 48000, 64000, 88200, 96000, 192000,
}

// Sub-block id bits and the two ids this parser reads. Low 6 bits = function;
// ID_LARGE / ID_ODD_SIZE adjust declared size.
const (
	subIDMask     = 0x3f
	subIDOddSize  = 0x40
	subIDLarge    = 0x80
	subIDSampleRt = 0x27 // ID_SAMPLE_RATE: 24-bit non-standard rate
	subIDDSD      = 0x0e // ID_DSD_BLOCK
)

// blockHeader is one decoded WavPack block header.
type blockHeader struct {
	blockSize uint32
	version   uint16
	// totalSamples is the 40-bit count; totalSamples32 is the low uint32 (unknown sentinel).
	totalSamples   uint64
	totalSamples32 uint32
	blockIndex     uint64
	blockSamples   uint32
	flags          uint32
}

// parseBlockHeader decodes the 32-byte header. avail is remaining bytes from this
// offset; a declared size past avail is refused (totalLen is the trailing-tag floor).
// Pass 0 to skip that check.
func parseBlockHeader(b []byte, avail int64) (blockHeader, error) {
	var h blockHeader
	if len(b) < blockHeaderLen {
		return h, fmt.Errorf("%w: WavPack block header is %d bytes, need %d", waxerr.ErrInvalidData, len(b), blockHeaderLen)
	}
	if string(b[0:4]) != blockMagic {
		return h, fmt.Errorf("%w: missing wvpk block marker", waxerr.ErrInvalidData)
	}
	h.blockSize = binary.LittleEndian.Uint32(b[4:8])
	h.version = binary.LittleEndian.Uint16(b[8:10])
	// 40-bit counts: a high byte and a low uint32. The encoder stores the total plus
	// total/0xFFFFFFFF, which keeps the low word off the all-ones escape, so the high byte is
	// subtracted back out.
	h.totalSamples32 = binary.LittleEndian.Uint32(b[12:16])
	h.totalSamples = uint64(h.totalSamples32) + uint64(b[11])<<32 - uint64(b[11])
	h.blockIndex = uint64(b[10])<<32 | uint64(binary.LittleEndian.Uint32(b[16:20]))
	h.blockSamples = binary.LittleEndian.Uint32(b[20:24])
	h.flags = binary.LittleEndian.Uint32(b[24:28])
	if h.version < minVersion || h.version > maxVersion {
		return h, fmt.Errorf("%w: WavPack stream version %#x is outside the supported range %#x-%#x",
			waxerr.ErrUnsupportedFormat, h.version, minVersion, maxVersion)
	}
	// blockSize is bytes after the first 8; must cover the rest of the header.
	if h.blockSize < blockHeaderLen-8 {
		return h, fmt.Errorf("%w: WavPack block declares %d bytes, too small for its header", waxerr.ErrInvalidData, h.blockSize)
	}
	if avail > 0 && h.totalLen() > avail {
		return h, fmt.Errorf("%w: WavPack block declares %d bytes but only %d remain in the file",
			waxerr.ErrInvalidData, h.totalLen(), avail)
	}
	return h, nil
}

// totalLen is on-disk length: 8 bytes before the size field plus the declared size.
func (h blockHeader) totalLen() int64 { return 8 + int64(h.blockSize) }

// statesTotal reports whether the block states the stream's total samples: the encoder writes the
// all-ones escape when it never learned the length, and ffmpeg leaves 0 when it writes to a pipe.
func (h blockHeader) statesTotal() bool {
	return h.totalSamples32 != totalSamplesUnknown && h.totalSamples != 0
}

// chain is what a walk of the block headers finds.
type chain struct {
	end     int64  // where the stream ends, -1 when unknown
	samples uint64 // the samples its blocks hold, when end is known
	audio   bool   // a block holds samples
}

// tailWindow bounds the bytes before the tags searched for the final block of a stream whose first
// block states its total.
const tailWindow = 64 << 10

// blockChainEnd finds where the stream of blocks whose first block is at start ends: after the final
// block of the sample group that reaches the total a first block at index 0 states, before a block
// whose index restarts or goes backward, or before a position that opens no block. A stream that
// starts past index 0 is a cut, whose stated total places no end: ffmpeg's -ss back-patches the cut's
// own count, a byte carve keeps the original's. The blocks of one multichannel sample group share its
// index. A block running past boundary, where the tags begin, a header past the end of the file, or
// more than maxBlocks headers leave the end unknown. With a total every block states, the blocks in
// the tailWindow bytes before boundary settle the end when they reach it ([finalBlock]). They are
// checked only against one another, so a break, or a stream of that total joined on, at or before
// the first of their headers goes unseen, and such a stream ends at boundary. Otherwise the headers
// are walked from start ([walkBlocks]). Reads are as [bits.ReadWithin] makes them: a failed read or
// one over limit is an error.
func blockChainEnd(ctx context.Context, src core.ReaderAtSized, start, boundary, limit int64, maxBlocks int) (chain, error) {
	if err := ctx.Err(); err != nil {
		return chain{}, err
	}
	if c, ok, err := finalBlock(src, start, boundary, limit); err != nil || ok {
		return c, err
	}
	return walkBlocks(ctx, src, start, boundary, limit, maxBlocks)
}

// finalBlock settles the end of a stream whose first block, at start, is at index 0, holds samples
// and states the total, from the tailWindow bytes before boundary alone. The first block header among
// them decides: from there the blocks, each of the first one's version and stating its total, must run
// back to back to boundary, the first of them holding samples, follow one another as a walk requires,
// open no stream past start at index 0, and end with the group that first reaches the total, which
// starts before it. Then the stream ends at boundary, as a walk ends it when the blocks before that
// header lead into it. Anything else, or a window that would read over limit, leaves it to the walk;
// ok is false then.
func finalBlock(src core.ReaderAtSized, start, boundary, limit int64) (chain, bool, error) {
	b, err := bits.ReadWithin(src, start, blockHeaderLen, limit)
	if err != nil || b == nil {
		return chain{}, false, err
	}
	first, err := parseBlockHeader(b, 0)
	if err != nil || first.blockIndex != 0 || first.blockSamples == 0 || !first.statesTotal() {
		return chain{}, false, nil
	}
	lo := max(start, boundary-tailWindow)
	if boundary-lo > limit {
		return chain{}, false, nil
	}
	w, err := bits.ReadWithin(src, lo, boundary-lo, limit)
	if err != nil || w == nil {
		return chain{}, false, err
	}
	for i := 0; i < len(w); i++ {
		j := bytes.Index(w[i:], []byte(blockMagic))
		if j < 0 {
			break
		}
		i += j
		if _, err := parseBlockHeader(w[i:min(i+blockHeaderLen, len(w))], 0); err != nil {
			continue // no block header opens here
		}
		hs := backToBack(w[i:], first)
		if hs == nil {
			return chain{}, false, nil
		}
		if hs[0].blockSamples == 0 {
			return chain{}, false, nil // metadata blocks lead, behind which a joined stream would hide
		}
		g := groups{total: first.totalSamples}
		off := lo + int64(i)
		for _, h := range hs {
			before := g.reached
			if !g.next(h) || !before && g.reached && g.index >= g.total ||
				h.blockSamples > 0 && h.blockIndex == 0 && h.flags&flagInitialBlock != 0 && off > start {
				return chain{}, false, nil
			}
			off += h.totalLen()
		}
		if !g.reached {
			return chain{}, false, nil
		}
		return chain{end: boundary, samples: g.index + uint64(g.samples), audio: true}, true, nil
	}
	return chain{}, false, nil
}

// backToBack decodes the headers of the blocks w holds back to back from its start, each of first's
// version and stating its total, and returns them when the last ends at w's end, else nil.
func backToBack(w []byte, first blockHeader) []blockHeader {
	var hs []blockHeader
	for i := 0; i < len(w); {
		if len(w)-i < blockHeaderLen {
			return nil
		}
		h, err := parseBlockHeader(w[i:i+blockHeaderLen], int64(len(w)-i))
		if err != nil || h.version != first.version || !h.statesTotal() || h.totalSamples != first.totalSamples {
			return nil
		}
		hs = append(hs, h)
		i += int(h.totalLen())
	}
	return hs
}

// walkBlocks walks the block headers from the first block at start, one read each, and ends the
// stream as [blockChainEnd] says.
func walkBlocks(ctx context.Context, src core.ReaderAtSized, start, boundary, limit int64, maxBlocks int) (chain, error) {
	var g groups
	unknown := func() (chain, error) { return chain{end: -1, audio: g.seen}, nil }
	off := start
	for n := 0; boundary-off >= blockHeaderLen; n++ {
		if err := ctx.Err(); err != nil {
			return chain{}, err
		}
		if maxBlocks > 0 && n >= maxBlocks {
			return unknown()
		}
		b, err := bits.ReadWithin(src, off, blockHeaderLen, limit)
		if err != nil {
			return chain{}, err
		}
		if b == nil {
			return unknown()
		}
		h, err := parseBlockHeader(b, 0)
		if err != nil {
			break
		}
		if n == 0 && h.blockIndex == 0 && h.statesTotal() {
			g.total = h.totalSamples
		}
		if !g.next(h) {
			break
		}
		if h.totalLen() > boundary-off {
			return unknown()
		}
		off += h.totalLen()
	}
	c := chain{end: off, audio: g.seen}
	if g.seen {
		c.samples = g.index + uint64(g.samples) - g.first
	}
	return c, nil
}

// groups follows the sample groups of a stream's blocks in file order.
type groups struct {
	total   uint64 // stated by a first block at index 0
	first   uint64 // the first sample group's index
	index   uint64 // the last group's
	samples uint32 // the last group's
	seen    bool   // a sample group began
	inGroup bool   // its final block is still to come
	reached bool   // a group reached total
}

// next takes the next block's header and reports whether the block belongs to the stream. A block of
// no samples holds the stream's metadata (an MD5 sum, a RIFF trailer) and belongs at any index, so one
// that is junk after a tag is taken for stream once a rewrite moves it up behind the audio. One with
// samples continues its group, or opens the next past the last one before a group reaches the total.
func (g *groups) next(h blockHeader) bool {
	if h.blockSamples == 0 {
		return true
	}
	if g.inGroup && h.flags&flagInitialBlock == 0 {
		if h.blockIndex != g.index {
			return false
		}
	} else if g.seen && (g.reached || h.blockIndex <= g.index) {
		return false
	}
	if !g.seen {
		g.first = h.blockIndex
	}
	g.seen, g.index, g.samples, g.inGroup = true, h.blockIndex, h.blockSamples, h.flags&flagFinalBlock == 0
	g.reached = g.reached || !g.inGroup && g.total > 0 && g.index+uint64(g.samples) >= g.total
	return true
}

// channels is 1 (mono) or 2. File channel count is the sum over the sample group.
func (h blockHeader) channels() int {
	if h.flags&flagMono != 0 {
		return 1
	}
	return 2
}

// bitsPerSample is storage width minus post-decode left shift (what players report).
// Not the magnitude field (that tracks peak sample value / loudness).
func (h blockHeader) bitsPerSample() int {
	bits := int(h.flags&flagBytesPerSampleMask+1) * 8
	if shift := int((h.flags >> flagShiftShift) & flagShiftMask); shift < bits {
		bits -= shift
	}
	return bits
}

// rateIndex is the 4-bit table index, or rateIndexUnknown.
func (h blockHeader) rateIndex() int { return int((h.flags >> flagRateIndexShift) & flagRateIndexMask) }

// sampleRate resolves the standard table, or 0 when ID_SAMPLE_RATE is required.
func (h blockHeader) sampleRate() int {
	if i := h.rateIndex(); i != rateIndexUnknown {
		return standardRates[i]
	}
	return 0
}

func (h blockHeader) hybrid() bool { return h.flags&flagHybrid != 0 }
func (h blockHeader) dsd() bool    { return h.flags&flagDSD != 0 }
func (h blockHeader) float() bool  { return h.flags&flagFloat != 0 }

// maxSubBlockScan bounds the body read for sample rate (sub-blocks sit at the front).
const maxSubBlockScan = 64 << 10

// subBlockRate finds a non-standard rate in metadata sub-blocks after the header.
// DSD carries a multiplier; when ID_SAMPLE_RATE is absent, rate is left unset.
func subBlockRate(body []byte) (rate int, isDSD bool) {
	for pos := 0; pos+2 <= len(body); {
		id := body[pos]
		var size int
		if id&subIDLarge != 0 {
			if pos+4 > len(body) {
				return rate, isDSD
			}
			size = (int(body[pos+1]) | int(body[pos+2])<<8 | int(body[pos+3])<<16) * 2
			pos += 4
		} else {
			size = int(body[pos+1]) * 2
			pos += 2
		}
		if id&subIDOddSize != 0 {
			size--
		}
		if size < 0 || size > len(body)-pos {
			return rate, isDSD
		}
		data := body[pos : pos+size]
		switch id & subIDMask {
		case subIDSampleRt:
			if len(data) >= 3 {
				rate = int(data[0]) | int(data[1])<<8 | int(data[2])<<16
			}
		case subIDDSD:
			isDSD = true
		}
		pos += size + (size & 1) // word-aligned
	}
	return rate, isDSD
}
