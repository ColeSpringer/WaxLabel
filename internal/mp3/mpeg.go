package mp3

import "encoding/binary"

// mpegInfo is the decoded technical detail of the MPEG audio stream, derived
// from the first audio frame header and any VBR header inside it.
type mpegInfo struct {
	header          [4]byte
	version         int // 1, 2, or 25 (MPEG 2.5)
	layer           int // 1, 2, or 3
	sampleRate      int
	channels        int
	frameBitrate    int // the first frame's bitrate (bps); 0 for "free"
	samplesPerFrame int
	vbrFrames       uint32 // frame count from a Xing/Info/VBRI header, else 0
	vbrBytes        uint32 // stream bytes from a Xing/Info/VBRI header, this frame included, else 0
	codec           string
}

// MPEG version field (bits 4-3 of the second header byte).
const (
	mpegV25 = 0
	mpegV2  = 2
	mpegV1  = 3
)

// bitrateTable[versionGroup][layerIndex][bitrateIndex] gives kbps. versionGroup
// 0 is MPEG1, 1 is MPEG2/2.5; layerIndex is 0..2 for layers 1..3. Index 0 (free)
// and 15 (bad) are zero.
var bitrateTable = [2][3][16]int{
	{ // MPEG1
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	},
	{ // MPEG2 / 2.5
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	},
}

// sampleRateTable[versionGroup][index] gives Hz. versionGroup 0=MPEG1, 1=MPEG2,
// 2=MPEG2.5.
var sampleRateTable = [3][4]int{
	{44100, 48000, 32000, 0},
	{22050, 24000, 16000, 0},
	{11025, 12000, 8000, 0},
}

// parseMPEG finds the first valid MPEG frame in window, returning its offset and properties,
// including the VBR frame and byte counts when Xing/Info/VBRI is present. Requires a second
// agreeing frame at the computed length (padding false-sync guard).
func parseMPEG(window []byte) (int, mpegInfo, bool) {
	i, info, ok := firstFrame(window, confirmNextFrame)
	if ok {
		readVBR(window[i:], &info)
	}
	return i, info, ok
}

// firstFrame returns the offset and header of the first frame in window that confirm accepts; a
// rejected header is likely a false sync, so the scan goes on.
func firstFrame(window []byte, confirm func(window []byte, i int, info mpegInfo) bool) (int, mpegInfo, bool) {
	for i := 0; i+4 <= len(window); i++ {
		if info, ok := decodeHeader(window[i:]); ok && confirm(window, i, info) {
			return i, info, true
		}
	}
	return 0, mpegInfo{}, false
}

// confirmNextFrame: second frame at computed length agrees on version/layer/rate.
// Free-format or near window end: accept alone.
func confirmNextFrame(window []byte, i int, info mpegInfo) bool {
	agrees, checked := nextFrameAgrees(window, i, info)
	return agrees || !checked
}

// nextFrameConfirms: second frame at computed length, inside window, agrees on version/layer/rate.
// Nothing confirms a frame alone.
func nextFrameConfirms(window []byte, i int, info mpegInfo) bool {
	agrees, _ := nextFrameAgrees(window, i, info)
	return agrees
}

// nextFrameAgrees reports whether the header one computed frame length past i agrees on version,
// layer and sample rate; checked is false when there is none to compare: a free-format or unknown
// length, or a next frame past the window.
func nextFrameAgrees(window []byte, i int, info mpegInfo) (agrees, checked bool) {
	flen := frameLength(info)
	if flen <= 0 || i+flen+4 > len(window) {
		return false, false
	}
	n, ok := decodeHeader(window[i+flen:])
	return ok && sameStream(info, n), true
}

// sameStream reports whether frame h agrees with frame f on version, layer and sample rate.
func sameStream(f, h mpegInfo) bool {
	return h.version == f.version && h.layer == f.layer && h.sampleRate == f.sampleRate
}

// byteCount is the stream's byte count from f's VBR header, or 0 when it has none or one that does
// not cover f's own frame.
func (f mpegInfo) byteCount() int64 {
	if n := int64(f.vbrBytes); n > 0 && n >= int64(frameLength(f)) {
		return n
	}
	return 0
}

// frameLength returns the size in bytes of the frame described by info, or 0 for
// a free-format/invalid frame whose length cannot be derived. The padding bit is
// in the third header byte.
func frameLength(info mpegInfo) int {
	if info.frameBitrate == 0 || info.sampleRate == 0 {
		return 0
	}
	padding := int((info.header[2] >> 1) & 1)
	if info.layer == 1 {
		return (12*info.frameBitrate/info.sampleRate + padding) * 4
	}
	return (info.samplesPerFrame/8)*info.frameBitrate/info.sampleRate + padding
}

// decodeHeader decodes a 4-byte MPEG audio frame header, validating the sync,
// version, layer, bitrate, and sample-rate fields so random bytes are not
// mistaken for a frame.
func decodeHeader(b []byte) (mpegInfo, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return mpegInfo{}, false
	}
	verBits := (b[1] >> 3) & 3
	layerBits := (b[1] >> 1) & 3
	if verBits == 1 || layerBits == 0 {
		return mpegInfo{}, false // reserved
	}
	bitrateIdx := (b[2] >> 4) & 0xF
	srIdx := (b[2] >> 2) & 3
	if bitrateIdx == 15 || srIdx == 3 {
		return mpegInfo{}, false
	}

	var info mpegInfo
	info.header = [4]byte{b[0], b[1], b[2], b[3]}
	info.layer = int(4 - layerBits) // bits 3,2,1 -> layers 1,2,3
	verGroup, srGroup := 0, 0
	switch verBits {
	case mpegV1:
		info.version, verGroup, srGroup = 1, 0, 0
	case mpegV2:
		info.version, verGroup, srGroup = 2, 1, 1
	case mpegV25:
		info.version, verGroup, srGroup = 25, 1, 2
	}
	info.frameBitrate = bitrateTable[verGroup][info.layer-1][bitrateIdx] * 1000
	info.sampleRate = sampleRateTable[srGroup][srIdx]
	if (b[3]>>6)&3 == 3 {
		info.channels = 1
	} else {
		info.channels = 2
	}
	info.samplesPerFrame = samplesPerFrame(info.version, info.layer)
	info.codec = codecName(info.version, info.layer)
	return info, true
}

// samplesPerFrame returns the PCM samples a single frame decodes to.
func samplesPerFrame(version, layer int) int {
	switch layer {
	case 1:
		return 384
	case 2:
		return 1152
	default: // layer 3
		if version == 1 {
			return 1152
		}
		return 576 // MPEG2 / 2.5
	}
}

func codecName(version, layer int) string {
	v := "MPEG-1"
	switch version {
	case 2:
		v = "MPEG-2"
	case 25:
		v = "MPEG-2.5"
	}
	return v + " Layer " + string(rune('0'+layer))
}

// readVBR looks for a Xing/Info or VBRI header inside the first frame and records
// the total frame count, which gives an accurate duration for variable-bitrate
// streams, and the stream's byte count. frame points at the start of the first frame header.
func readVBR(frame []byte, info *mpegInfo) {
	// Xing/Info sits after the header and side information, plus a 2-byte MPEG CRC when
	// the protection bit is clear. Probe the CRC-less offset first, then the CRC-present
	// one; the protection bit is sometimes wrong. With a CRC present, the CRC-less
	// offset covers the last two side-info bytes and the first two of "Xing", which
	// cannot spell "Xing"/"Info", so there is no false early match.
	side := sideInfoSize(info.version, info.channels)
	for _, off := range [2]int{4 + side, 4 + 2 + side} {
		if off+12 > len(frame) {
			continue
		}
		if tag := string(frame[off : off+4]); tag == "Xing" || tag == "Info" {
			flags := binary.BigEndian.Uint32(frame[off+4 : off+8])
			field := off + 8
			if flags&0x1 != 0 { // frame count present
				info.vbrFrames = binary.BigEndian.Uint32(frame[field : field+4])
				field += 4
			}
			if flags&0x2 != 0 && field+4 <= len(frame) { // byte count present
				info.vbrBytes = binary.BigEndian.Uint32(frame[field : field+4])
			}
			return
		}
	}
	// VBRI sits at a fixed offset of 32 bytes after the header.
	if off := 4 + 32; off+18 <= len(frame) && string(frame[off:off+4]) == "VBRI" {
		info.vbrBytes = binary.BigEndian.Uint32(frame[off+10 : off+14])
		info.vbrFrames = binary.BigEndian.Uint32(frame[off+14 : off+18])
	}
}

// sideInfoSize returns the MPEG layer-3 side-information size that precedes a
// Xing/Info header.
func sideInfoSize(version, channels int) int {
	if version == 1 {
		if channels == 1 {
			return 17
		}
		return 32
	}
	if channels == 1 {
		return 9
	}
	return 17
}
