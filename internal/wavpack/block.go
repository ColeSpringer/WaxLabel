package wavpack

import (
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/waxerr"
)

// The WavPack block header is a fixed 32 bytes: the "wvpk" id, the block size, the
// stream version, a 40-bit total sample count and block index split across a byte
// and a uint32 each, the block's sample count, a flag word carrying the whole static
// stream configuration, and a CRC of the decoded data.
const (
	blockMagic     = "wvpk"
	blockHeaderLen = 32
	// minVersion and maxVersion bound the stream versions this parser understands.
	// A version outside the range is a format WaxLabel cannot claim to read.
	minVersion = 0x402
	maxVersion = 0x410
)

// Flag-word bit positions. Only the fields WaxLabel reports are named; the rest
// (decorrelation, noise shaping, block sequencing) are decoder state.
const (
	flagBytesPerSampleMask = 0x3
	flagMono               = 1 << 2
	flagHybrid             = 1 << 3
	flagFloat              = 1 << 7
	flagShiftShift         = 13
	flagShiftMask          = 0x1f
	flagRateIndexShift     = 23
	flagRateIndexMask      = 0xf
	flagFinalBlock         = 1 << 12
	flagDSD                = 1 << 31
	// totalSamplesUnknown is the all-ones 32-bit total-sample field a streamed file
	// carries when the encoder did not know the length in advance. The check is against
	// that field alone, not the assembled 40-bit count: the high byte extends the count
	// and is 0 on such a file, so testing the whole value would miss the sentinel and
	// report 27 hours of audio for a stream of any length.
	totalSamplesUnknown = 1<<32 - 1
	// rateIndexUnknown means the rate is not one of the standard table entries and
	// must be read from an ID_SAMPLE_RATE metadata sub-block instead.
	rateIndexUnknown = 15
)

// standardRates is the sample-rate table the 4-bit rate index selects.
var standardRates = [15]int{
	6000, 8000, 9600, 11025, 12000, 16000, 22050, 24000,
	32000, 44100, 48000, 64000, 88200, 96000, 192000,
}

// Metadata sub-block id bits and the two ids this parser reads. A sub-block's id
// byte carries its function in the low 6 bits, whether the declared size is one or
// three bytes (ID_LARGE), and whether the payload is one byte shorter than the
// word count implies (ID_ODD_SIZE).
const (
	subIDMask     = 0x3f
	subIDOddSize  = 0x40
	subIDLarge    = 0x80
	subIDSampleRt = 0x27 // ID_SAMPLE_RATE: a 24-bit non-standard rate
	subIDDSD      = 0x0e // ID_DSD_BLOCK: DSD audio, whose rate is derived from it
)

// blockHeader is one decoded WavPack block header.
type blockHeader struct {
	blockSize uint32
	version   uint16
	// totalSamples is the assembled 40-bit count; totalSamples32 is the raw low uint32,
	// kept because the "unknown" sentinel is defined on that field alone.
	totalSamples   uint64
	totalSamples32 uint32
	blockIndex     uint64
	blockSamples   uint32
	flags          uint32
	crc            uint32
}

// parseBlockHeader decodes the fixed 32-byte header at the start of b. avail is how
// many bytes remain in the file from the header's own offset, so a block declaring
// more than that is refused: totalLen is the floor a trailing tag must sit past, and
// an absurd size would push it beyond the file, making the peel reject the file's
// real APEv2 tag and a rewrite append a second one. Pass 0 to skip the check when the
// remaining length is not known.
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
	// The 40-bit counts are split across a high byte and a low uint32. For the sample
	// count only the byte's low nibble extends it; the high nibble is a small
	// subtractive correction, so it is applied that way rather than shifted in.
	h.totalSamples32 = binary.LittleEndian.Uint32(b[12:16])
	h.totalSamples = uint64(b[11]&0x0F)<<32 | uint64(h.totalSamples32)
	if corr := uint64(b[11] >> 4); corr <= h.totalSamples {
		h.totalSamples -= corr
	}
	h.blockIndex = uint64(b[10])<<32 | uint64(binary.LittleEndian.Uint32(b[16:20]))
	h.flags = binary.LittleEndian.Uint32(b[24:28])
	if h.version < minVersion || h.version > maxVersion {
		return h, fmt.Errorf("%w: WavPack stream version %#x is outside the supported range %#x-%#x",
			waxerr.ErrUnsupportedFormat, h.version, minVersion, maxVersion)
	}
	// blockSize counts everything after the first 8 bytes, so a block that cannot even
	// hold the rest of its own header is corrupt.
	if h.blockSize < blockHeaderLen-8 {
		return h, fmt.Errorf("%w: WavPack block declares %d bytes, too small for its header", waxerr.ErrInvalidData, h.blockSize)
	}
	if avail > 0 && h.totalLen() > avail {
		return h, fmt.Errorf("%w: WavPack block declares %d bytes but only %d remain in the file",
			waxerr.ErrInvalidData, h.totalLen(), avail)
	}
	return h, nil
}

// totalLen is the block's whole on-disk length: the 8 bytes before the size field
// plus the size it declares.
func (h blockHeader) totalLen() int64 { return 8 + int64(h.blockSize) }

// channels is this block's channel count: one for a mono block, two otherwise.
// Multichannel files chain several blocks per sample group, so the file's channel
// count is the sum over that group, not this one block's.
func (h blockHeader) channels() int {
	if h.flags&flagMono != 0 {
		return 1
	}
	return 2
}

// bitsPerSample is the storage width less the post-decode left shift, which is what
// the reference decoder and every player report.
//
// It is deliberately NOT the magnitude field: that records the largest value the
// decoded samples actually reach, so it tracks how loud the recording is. A
// full-scale 32-bit sine reads 13 there and a quiet one reads 20, while both are
// 32-bit audio.
func (h blockHeader) bitsPerSample() int {
	bits := int(h.flags&flagBytesPerSampleMask+1) * 8
	if shift := int((h.flags >> flagShiftShift) & flagShiftMask); shift < bits {
		bits -= shift
	}
	return bits
}

// rateIndex is the 4-bit sampling-rate table index, or rateIndexUnknown.
func (h blockHeader) rateIndex() int { return int((h.flags >> flagRateIndexShift) & flagRateIndexMask) }

// sampleRate resolves the standard rate table, returning 0 when the index says the
// rate is non-standard and must come from an ID_SAMPLE_RATE sub-block.
func (h blockHeader) sampleRate() int {
	if i := h.rateIndex(); i != rateIndexUnknown {
		return standardRates[i]
	}
	return 0
}

func (h blockHeader) hybrid() bool { return h.flags&flagHybrid != 0 }
func (h blockHeader) dsd() bool    { return h.flags&flagDSD != 0 }
func (h blockHeader) float() bool  { return h.flags&flagFloat != 0 }

// maxSubBlockScan bounds how much of a block body is read to find the sample rate.
// The sub-blocks that carry it sit at the front of the first block, so a small window
// is enough; without one, a block declaring a huge size would pull the whole
// allocation limit into memory just to answer "what rate is this".
const maxSubBlockScan = 64 << 10

// subBlockRate walks a block's metadata sub-blocks looking for the non-standard
// sample rate. body is the block's bytes after the 32-byte header.
//
// A DSD block carries a rate multiplier rather than a rate, and the decoded rate
// depends on the DSD mode, so a DSD stream reports whatever ID_SAMPLE_RATE says and
// nothing is invented when it says nothing.
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
		pos += size + (size & 1) // sub-blocks are word-aligned
	}
	return rate, isDSD
}
