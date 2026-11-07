package apen

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/colespringer/waxlabel/internal/bits"
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
}

// totalSamples: full frames plus the final partial frame.
func (h header) totalSamples() uint64 {
	if h.totalFrames == 0 {
		return 0
	}
	return uint64(h.totalFrames-1)*uint64(h.blocksPerFrame) + uint64(h.finalFrameBlocks)
}

// parseVersion checks the marker and reads the version, refusing a layout older than the
// documented one.
func parseVersion(b []byte) (uint16, error) {
	if len(b) < 6 {
		return 0, fmt.Errorf("%w: Monkey's Audio file shorter than its marker and version", waxerr.ErrInvalidData)
	}
	if string(b[0:4]) != fileMagic {
		return 0, fmt.Errorf("%w: missing MAC marker", waxerr.ErrInvalidData)
	}
	v := binary.LittleEndian.Uint16(b[4:6])
	if v < minVersion {
		return 0, fmt.Errorf("%w: Monkey's Audio version %d.%02d predates the documented header layout",
			waxerr.ErrUnsupportedFormat, v/1000, v%1000/10)
	}
	return v, nil
}

// documentedHeaderLen is the header length of the version's layout, before any declared extension:
// the least a trailing tag must follow the marker by.
func documentedHeaderLen(version uint16) int64 {
	if version >= descriptorVersion {
		return descriptorLen + headerLen
	}
	return legacyLen
}

// descriptorBytes is APE_DESCRIPTOR.nDescriptorBytes, which locates the APE_HEADER from the marker,
// at least the documented descriptor length.
func descriptorBytes(head []byte) int64 {
	return max(int64(binary.LittleEndian.Uint32(head[8:12])), descriptorLen)
}

// descriptorEnd is where a 3.98+ stream that starts at streamAt ends: past the byte counts its
// descriptor declares for itself, the header, the seek table, the WAV header data, the frames and
// the WAV terminating data. A legacy stream declares none, so its end is unknown (-1), as is one
// whose descriptor is cut short or whose counts end before the header it locates does.
func descriptorEnd(head []byte, version uint16, streamAt int64) int64 {
	const fields = 36 // through nTerminatingDataBytes
	if version < descriptorVersion || len(head) < fields {
		return -1
	}
	count := func(at int) int64 { return int64(binary.LittleEndian.Uint32(head[at : at+4])) }
	if count(28) >= 1<<30 { // frame bytes of 2^62 or more: no file holds them
		return -1
	}
	header := streamAt + descriptorBytes(head)
	end := header + count(12) + count(16) + count(20) + (count(28)<<32 | count(24)) + count(32)
	if end < header+headerLen {
		return -1
	}
	return end
}

// readHeader decodes the version's header from head, the leading bytes of the stream at streamAt,
// reading src for a declared header past them. A declared header must end by tagStart, where the
// trailing tags begin; the peel floor keeps the tags past the fixed-size parts.
func readHeader(src io.ReaderAt, head []byte, version uint16, streamAt, tagStart, size, limit int64) (header, error) {
	h := header{version: version}
	if version < descriptorVersion {
		return parseLegacyHeader(head, h)
	}
	if len(head) < descriptorLen {
		return h, fmt.Errorf("%w: APE_DESCRIPTOR is %d bytes, need %d", waxerr.ErrInvalidData, len(head), descriptorLen)
	}
	rel := descriptorBytes(head)
	at := streamAt + rel
	switch end := at + headerLen; {
	case end > size:
		return h, fmt.Errorf("%w: the Monkey's Audio header declared at %d runs past the end of the file", waxerr.ErrInvalidData, at)
	case end > tagStart:
		return h, fmt.Errorf("%w: the Monkey's Audio header declared at %d runs into the trailing tag at %d", waxerr.ErrInvalidData, at, tagStart)
	case rel+headerLen <= int64(len(head)):
		return parseDescriptorHeader(head[rel:rel+headerLen], h), nil
	}
	hd, err := bits.ReadSlice(src, at, headerLen, limit)
	if err != nil {
		return h, err
	}
	return parseDescriptorHeader(hd, h), nil
}

// parseDescriptorHeader decodes the 3.98+ APE_HEADER in hd.
func parseDescriptorHeader(hd []byte, h header) header {
	h.compressionLevel = binary.LittleEndian.Uint16(hd[0:2])
	h.formatFlags = binary.LittleEndian.Uint16(hd[2:4])
	h.blocksPerFrame = binary.LittleEndian.Uint32(hd[4:8])
	h.finalFrameBlocks = binary.LittleEndian.Uint32(hd[8:12])
	h.totalFrames = binary.LittleEndian.Uint32(hd[12:16])
	h.bitsPerSample = binary.LittleEndian.Uint16(hd[16:18])
	h.channels = binary.LittleEndian.Uint16(hd[18:20])
	h.sampleRate = binary.LittleEndian.Uint32(hd[20:24])
	return h
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
