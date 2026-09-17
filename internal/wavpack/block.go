package wavpack

import (
	"encoding/binary"
	"fmt"

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
	// 40-bit counts: high byte + low uint32. Sample-count high nibble is a subtractive
	// correction, not a shift-in.
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
