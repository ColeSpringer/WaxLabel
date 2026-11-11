package mp3

import (
	"bytes"
	"context"
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

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
	vbrHeader       bool   // the frame holds a Xing/Info/VBRI header, so no audio
	vbrFrames       uint32 // frame count from a Xing/Info/VBRI header, else 0
	vbrBytes        uint32 // stream bytes from a Xing/Info/VBRI header, this frame included, else 0
	// encDelay and encPadding are the samples the encoder added before and after the audio, as
	// the extension LAME and libav write behind a Xing/Info header states them, else 0.
	encDelay, encPadding int
	codec                string
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
		// The header lies inside its frame; a free-format frame's length is unknown.
		frame := window[i:]
		if n := frameLength(info); n > 0 && n < len(frame) {
			frame = frame[:n]
		}
		readVBR(frame, &info)
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
	names := &mpeg1Names
	switch version {
	case 2:
		names = &mpeg2Names
	case 25:
		names = &mpeg25Names
	}
	return names[layer-1]
}

// The codec names by layer, held whole so that decoding a header allocates nothing.
var (
	mpeg1Names  = [3]string{"MPEG-1 Layer 1", "MPEG-1 Layer 2", "MPEG-1 Layer 3"}
	mpeg2Names  = [3]string{"MPEG-2 Layer 1", "MPEG-2 Layer 2", "MPEG-2 Layer 3"}
	mpeg25Names = [3]string{"MPEG-2.5 Layer 1", "MPEG-2.5 Layer 2", "MPEG-2.5 Layer 3"}
)

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
			info.vbrHeader = true
			flags := binary.BigEndian.Uint32(frame[off+4 : off+8])
			field := off + 8
			if flags&0x1 != 0 { // frame count present
				info.vbrFrames = binary.BigEndian.Uint32(frame[field : field+4])
				field += 4
			}
			if flags&0x2 != 0 { // byte count present
				if field+4 > len(frame) {
					return
				}
				info.vbrBytes = binary.BigEndian.Uint32(frame[field : field+4])
				field += 4
			}
			if flags&0x4 != 0 {
				field += 100 // seek table
			}
			if flags&0x8 != 0 {
				field += 4 // quality
			}
			if field < len(frame) {
				readEncoderDelays(frame[field:], info)
			}
			return
		}
	}
	// VBRI sits at a fixed offset of 32 bytes after the header.
	if off := 4 + 32; off+18 <= len(frame) && string(frame[off:off+4]) == "VBRI" {
		info.vbrHeader = true
		info.vbrBytes = binary.BigEndian.Uint32(frame[off+10 : off+14])
		info.vbrFrames = binary.BigEndian.Uint32(frame[off+14 : off+18])
	}
}

// encoderDelaysOff is where the delays sit in the extension an encoder writes behind the Xing
// fields: past its 9-byte name, the revision and lowpass bytes, 8 of ReplayGain, the encoding
// flags and the bitrate.
const encoderDelaysOff = 21

// readEncoderDelays reads the encoder delay and padding, 12 bits each, from that extension. Only
// LAME and libav names are believed, the ones ffmpeg trims by.
func readEncoderDelays(ext []byte, info *mpegInfo) {
	if len(ext) < encoderDelaysOff+3 {
		return
	}
	switch string(ext[:4]) {
	case "LAME", "Lavf", "Lavc":
		v := int(ext[encoderDelaysOff])<<16 | int(ext[encoderDelaysOff+1])<<8 | int(ext[encoderDelaysOff+2])
		info.encDelay, info.encPadding = v>>12, v&0xFFF
	}
}

// mixedBitrates reports whether the frames of f's stream that follow one another from the start
// of window, where f's frame opens, hold more than one bitrate: a variable-bitrate stream.
func mixedBitrates(window []byte, f mpegInfo) bool {
	for i := 0; i+4 <= len(window); {
		h, ok := decodeHeader(window[i:])
		if !ok || !sameStream(f, h) || frameLength(h) == 0 {
			return false
		}
		if h.frameBitrate != f.frameBitrate {
			return true
		}
		i += frameLength(h)
	}
	return false
}

// walkBuffer is how much of the stream [walkMPEGFrames] reads at a time.
const walkBuffer = 64 << 10

// frameRun is what [walkMPEGFrames] counts: frames, and the bytes they hold.
type frameRun struct {
	frames uint64
	bytes  int64
}

// walkMPEGFrames counts the whole frames of f's stream in ranges, the first of which opens with
// f's frame. Past bytes that are no frame of the stream (the tags between joined streams, junk) it
// goes on at the next frame a second one confirms or that ends its range, as [chainEnd] does, so a
// stream counts the same however it was joined. It reads every byte it walks, walkBuffer at a time
// into one buffer, and nothing past the end of src.
func walkMPEGFrames(ctx context.Context, src core.ReaderAtSized, f mpegInfo, ranges [][2]int64, limit int64) (frameRun, error) {
	var longest int64
	for _, r := range ranges {
		longest = max(longest, r[1]-r[0])
	}
	if err := bits.CheckAlloc(min(longest, walkBuffer), limit); err != nil {
		return frameRun{}, err
	}
	buf := make([]byte, min(longest, walkBuffer))
	var (
		run         frameRun
		held        []byte // the bytes of buf read, which lie at bufOff
		bufOff, end int64
	)
	// frameAt reads the header at pos as a frame of the stream, 0 for its length when it is none.
	frameAt := func(pos int64) (int64, error) {
		if pos+4 > end {
			return 0, nil
		}
		if pos < bufOff || pos+4 > bufOff+int64(len(held)) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			held, bufOff = buf[:min(end-pos, int64(len(buf)))], pos
			if ok, err := bits.FillWithin(src, held, pos); err != nil || !ok {
				held, end = nil, pos
				return 0, err
			}
		}
		if h, ok := decodeHeader(held[pos-bufOff:]); ok && sameStream(f, h) {
			return int64(frameLength(h)), nil
		}
		return 0, nil
	}
	confirmed := true // the frame at pos is the stream's: f's own, or one that followed a frame
	for _, r := range ranges {
		held, end = nil, min(r[1], src.Size())
		for pos := r[0]; pos+4 <= end; {
			n, err := frameAt(pos)
			if err != nil {
				return frameRun{}, err
			}
			if n > 0 && !confirmed && pos+n != end {
				// A header found in bytes that are no frame counts only when a frame follows it.
				next, err := frameAt(pos + n)
				if err != nil {
					return frameRun{}, err
				}
				if next == 0 {
					n = 0
				}
			}
			switch {
			case n == 0:
				// Go on at the next byte that can open a header, where the buffer shows one.
				pos, confirmed = pos+1, false
				if at := pos - bufOff; at >= 0 && at < int64(len(held)) {
					if skip := bytes.IndexByte(held[at:], 0xFF); skip >= 0 {
						pos += int64(skip)
					} else {
						pos = bufOff + int64(len(held))
					}
				}
			case pos+n > end:
				pos = end // the frame the range cuts
			default:
				run.frames, run.bytes, pos, confirmed = run.frames+1, run.bytes+n, pos+n, true
			}
		}
		confirmed = false
	}
	return run, nil
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
