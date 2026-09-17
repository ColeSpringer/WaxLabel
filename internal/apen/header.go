package apen

import (
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/waxerr"
)

// After "MAC " and a version word: from 3.98 (3980) an APE_DESCRIPTOR then
// APE_HEADER; older files use a fixed legacy header and derive frame size from
// version/compression. Both layouts are read.
const (
	fileMagic = "MAC "
	// descriptorVersion is the first version with an APE_DESCRIPTOR.
	descriptorVersion = 3980
	// minVersion is the oldest documented layout; below it is a different container.
	minVersion = 3800

	descriptorLen = 52
	headerLen     = 24
	legacyLen     = 32
)

// Legacy-layout format flags. From 3.98 bit depth is an explicit field.
const (
	flag8Bit  = 1 << 0
	flag24Bit = 1 << 3
)

// Legacy frame sizes (not stored on disk). At 3.8, extra-high compression uses the
// larger size.
const (
	blocksPerFrameV3950 = 73728 * 4
	blocksPerFrameV3900 = 73728
	blocksPerFrameOld   = 9216
	compressionExtraHi  = 4000
)

// header is the decoded audio description in one shape for either on-disk layout.
type header struct {
	version          uint16
	compressionLevel uint16
	formatFlags      uint16
	blocksPerFrame   uint32
	finalFrameBlocks uint32
	totalFrames      uint32
	bitsPerSample    uint16
	channels         uint16
	sampleRate       uint32
	// headerLen is the on-disk descriptor+header length (floor for a trailing tag).
	headerLen int64
}

// totalSamples: full frames plus the final partial frame.
func (h header) totalSamples() uint64 {
	if h.totalFrames == 0 {
		return 0
	}
	return uint64(h.totalFrames-1)*uint64(h.blocksPerFrame) + uint64(h.finalFrameBlocks)
}

// parseHeader decodes the layout at the front of b. Short or overrunning declared
// regions are refused rather than trusted past the leading window.
func parseHeader(b []byte) (header, error) {
	var h header
	if len(b) < 6 {
		return h, fmt.Errorf("%w: Monkey's Audio file shorter than its marker and version", waxerr.ErrInvalidData)
	}
	if string(b[0:4]) != fileMagic {
		return h, fmt.Errorf("%w: missing MAC marker", waxerr.ErrInvalidData)
	}
	h.version = binary.LittleEndian.Uint16(b[4:6])
	if h.version < minVersion {
		return h, fmt.Errorf("%w: Monkey's Audio version %d.%02d predates the documented header layout",
			waxerr.ErrUnsupportedFormat, h.version/1000, h.version%1000/10)
	}
	if h.version >= descriptorVersion {
		return parseDescriptorHeader(b, h)
	}
	return parseLegacyHeader(b, h)
}

// parseDescriptorHeader: APE_DESCRIPTOR.nDescriptorBytes locates the APE_HEADER.
func parseDescriptorHeader(b []byte, h header) (header, error) {
	if len(b) < descriptorLen {
		return h, fmt.Errorf("%w: APE_DESCRIPTOR is %d bytes, need %d", waxerr.ErrInvalidData, len(b), descriptorLen)
	}
	descBytes := int64(binary.LittleEndian.Uint32(b[8:12]))
	hdrBytes := int64(binary.LittleEndian.Uint32(b[12:16]))
	// Trust the writer's descriptor length when sane; else fall back to documented
	// sizes. Clamp from above too: absurd nHeaderBytes would push headerLen past EOF,
	// peel would miss the real APEv2, and rewrite would append a second tag.
	// Clamp against room for the header after the descriptor, not only that the
	// descriptor fits: a length landing exactly at end of read would otherwise pass
	// here and fail later, flipping on whether a rewrite had already appended a tag.
	if descBytes < descriptorLen || descBytes+headerLen > int64(len(b)) {
		descBytes = descriptorLen
	}
	if hdrBytes < headerLen || descBytes+hdrBytes > int64(len(b)) {
		hdrBytes = headerLen
	}
	if descBytes+headerLen > int64(len(b)) {
		return h, fmt.Errorf("%w: Monkey's Audio header is truncated", waxerr.ErrInvalidData)
	}
	hd := b[descBytes : descBytes+headerLen]
	h.compressionLevel = binary.LittleEndian.Uint16(hd[0:2])
	h.formatFlags = binary.LittleEndian.Uint16(hd[2:4])
	h.blocksPerFrame = binary.LittleEndian.Uint32(hd[4:8])
	h.finalFrameBlocks = binary.LittleEndian.Uint32(hd[8:12])
	h.totalFrames = binary.LittleEndian.Uint32(hd[12:16])
	h.bitsPerSample = binary.LittleEndian.Uint16(hd[16:18])
	h.channels = binary.LittleEndian.Uint16(hd[18:20])
	h.sampleRate = binary.LittleEndian.Uint32(hd[20:24])
	h.headerLen = descBytes + hdrBytes
	return h, nil
}

// parseLegacyHeader: pre-3.98 32-byte geometry; frame size is derived.
func parseLegacyHeader(b []byte, h header) (header, error) {
	if len(b) < legacyLen {
		return h, fmt.Errorf("%w: legacy Monkey's Audio header is %d bytes, need %d", waxerr.ErrInvalidData, len(b), legacyLen)
	}
	h.compressionLevel = binary.LittleEndian.Uint16(b[6:8])
	h.formatFlags = binary.LittleEndian.Uint16(b[8:10])
	h.channels = binary.LittleEndian.Uint16(b[10:12])
	h.sampleRate = binary.LittleEndian.Uint32(b[12:16])
	wavHeaderBytes := int64(binary.LittleEndian.Uint32(b[16:20]))
	h.totalFrames = binary.LittleEndian.Uint32(b[24:28])
	h.finalFrameBlocks = binary.LittleEndian.Uint32(b[28:32])
	h.blocksPerFrame = legacyBlocksPerFrame(h.version, h.compressionLevel)
	switch {
	case h.formatFlags&flag8Bit != 0:
		h.bitsPerSample = 8
	case h.formatFlags&flag24Bit != 0:
		h.bitsPerSample = 24
	default:
		h.bitsPerSample = 16
	}
	// Embedded WAV header is between APE header and frames; clamp absurd lengths
	// so the trailing-tag floor stays inside the file.
	if wavHeaderBytes < 0 || legacyLen+wavHeaderBytes > int64(len(b)) {
		wavHeaderBytes = 0
	}
	h.headerLen = legacyLen + wavHeaderBytes
	return h, nil
}

// legacyBlocksPerFrame: grew at 3.90 and 3.95; at 3.8 extra-high already used the
// larger size (>=, not ==: equality would mis-size insane-level files by 8x).
func legacyBlocksPerFrame(version, compressionLevel uint16) uint32 {
	switch {
	case version >= 3950:
		return blocksPerFrameV3950
	case version >= 3900 || compressionLevel >= compressionExtraHi:
		return blocksPerFrameV3900
	default:
		return blocksPerFrameOld
	}
}
