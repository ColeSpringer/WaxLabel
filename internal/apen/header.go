package apen

import (
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/waxerr"
)

// A Monkey's Audio file opens with the "MAC " marker and a version word. What
// follows depends on that version: from 3.98 (3980) the file carries an
// APE_DESCRIPTOR - which sizes every later region - followed by an APE_HEADER with
// the audio geometry. Older files inline a single fixed header instead, and derive
// the frame size from the version and compression level rather than storing it.
// Both layouts are needed: 3.97 and earlier files are common in real libraries.
const (
	fileMagic = "MAC "
	// descriptorVersion is the first version that writes an APE_DESCRIPTOR.
	descriptorVersion = 3980
	// minVersion is the oldest version whose header layout is documented well enough
	// to decode. Below it the format is a different container.
	minVersion = 3800

	descriptorLen = 52
	headerLen     = 24
	legacyLen     = 32
)

// Format flag bits from the old header. From 3.98 the bit depth is an explicit
// field, so these matter only for the legacy layout.
const (
	flag8Bit  = 1 << 0
	flag24Bit = 1 << 3
)

// Frame sizes for the legacy layout, which stores none. The value depends on the
// version and, at the 3.8 boundary, on whether the extra-high compression level was
// used.
const (
	blocksPerFrameV3950 = 73728 * 4
	blocksPerFrameV3900 = 73728
	blocksPerFrameOld   = 9216
	compressionExtraHi  = 4000
)

// header is the decoded audio description, assembled from whichever layout the file
// uses so the rest of the codec sees one shape.
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
	// headerLen is the on-disk length of the descriptor and header region: the
	// earliest offset at which a trailing tag could begin.
	headerLen int64
}

// totalSamples is the decoded sample count: every frame but the last is full.
func (h header) totalSamples() uint64 {
	if h.totalFrames == 0 {
		return 0
	}
	return uint64(h.totalFrames-1)*uint64(h.blocksPerFrame) + uint64(h.finalFrameBlocks)
}

// parseHeader decodes whichever header layout b begins with. b must hold at least
// the descriptor and header (or the legacy header); a shorter read is reported as
// truncated rather than misread. b is the leading window of the file, so a declared
// region that runs past it is refused rather than trusted.
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

// parseDescriptorHeader decodes the 3.98-and-later layout: an APE_DESCRIPTOR whose
// nDescriptorBytes field locates the APE_HEADER that follows it.
func parseDescriptorHeader(b []byte, h header) (header, error) {
	if len(b) < descriptorLen {
		return h, fmt.Errorf("%w: APE_DESCRIPTOR is %d bytes, need %d", waxerr.ErrInvalidData, len(b), descriptorLen)
	}
	descBytes := int64(binary.LittleEndian.Uint32(b[8:12]))
	hdrBytes := int64(binary.LittleEndian.Uint32(b[12:16]))
	// A descriptor may be longer than the fields this version knows; trust its own
	// length so the header is found where the writer put it. A short or absurd value
	// falls back to the documented sizes rather than seeking into the audio.
	//
	// Both are clamped from above as well as below. headerLen is the floor a trailing
	// tag must sit past, so an absurd nHeaderBytes would push it beyond the file and
	// make the peel reject the file's real APEv2 tag - after which a rewrite appends a
	// second one and the result no longer parses.
	// The clamp tests the condition the USE below needs - room for the header that
	// follows the descriptor - not merely that the descriptor's own length fits. A
	// declared length landing exactly on the end of the read otherwise slips through here
	// and fails there, which made the verdict depend on how many trailing bytes the file
	// happened to have: a tag appended by a rewrite could flip it.
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

// parseLegacyHeader decodes the pre-3.98 layout, whose 32 bytes carry the geometry
// inline and leave the frame size to be derived.
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
	// The embedded WAV header sits between the APE header and the frames, so it is part
	// of the region a trailing tag must sit past. An absurd length is clamped to the
	// bare header rather than pushing that floor beyond the file.
	if wavHeaderBytes < 0 || legacyLen+wavHeaderBytes > int64(len(b)) {
		wavHeaderBytes = 0
	}
	h.headerLen = legacyLen + wavHeaderBytes
	return h, nil
}

// legacyBlocksPerFrame derives the frame size the pre-3.98 layout does not store.
// The size grew twice: at 3.90, and again at 3.95; 3.8 already used the larger frame
// at the extra-high compression level and above, which is the comparison the reference
// decoder makes (an equality test would give an insane-level file an 8x-wrong length).
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
