package flac

import (
	"context"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// Tail walk: one small end window, grow only until a frame header is found.
// Past the cap, leave undiagnosed rather than scanning a multi-GB file.
const (
	tailWindowStart = 64 << 10
	tailWindowCap   = 4 << 20
)

// tailCandidate is a validated frame header in the tail window.
type tailCandidate struct {
	off        int
	start, end uint64
	hdr        frameHeader
}

// frameTailWarnings finds truncation (samples short of STREAMINFO) and trailing
// junk after the last frame. FLAC has no encoded byte length, so both come from
// frames (sync + header CRC-8, coded number, frame CRC-16). junk is the located
// trailing length for carving. Best-effort: unreadable or unplaceable streams
// report nothing. Only context cancel is returned as err.
func frameTailWarnings(ctx context.Context, src core.ReaderAtSized, d *doc, limit int64) (ws []core.Warning, junk int64, err error) {
	si := d.streamInfo
	audioLen := d.audioEnd - d.audioStart
	if si.TotalSamples == 0 || audioLen <= 0 {
		return nil, 0, nil
	}
	// Fixed strategy: frame number maps to samples only with constant block size.
	fixedUnit := 0
	if si.MinBlockSize == si.MaxBlockSize {
		fixedUnit = si.MinBlockSize
	}

	// Alloc limit narrows the search like the 4 MiB cap.
	effCap := min(int64(tailWindowCap), limit)
	for w := int64(tailWindowStart); ; w *= 4 {
		w = min(w, audioLen, effCap)
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		win, err := bits.ReadSlice(src, d.audioEnd-w, w, limit)
		if err != nil {
			return nil, 0, nil
		}
		st := scanTailWindow(win, si, fixedUnit)
		if ws, junk, conclusive := tailVerdict(win, st, w == audioLen, si); conclusive {
			return ws, junk, nil
		}
		// Fixed strategy without constant block size cannot be placed.
		if !st.have && st.fixedUnplaceable {
			return nil, 0, nil
		}
		if w == audioLen || w >= effCap {
			return nil, 0, nil
		}
	}
}

// tailScan is O(1) scan state: best candidate, whether an earlier same-strategy
// candidate chains below it, first candidate (anchor), poison flags.
type tailScan struct {
	f         tailCandidate
	have      bool
	fChained  bool
	first     tailCandidate
	haveFirst bool
	// overrun: header implies samples past STREAMINFO total.
	overrun bool
	// fixedUnplaceable: fixed-strategy header but STREAMINFO block-size bounds differ.
	fixedUnplaceable bool
	minStart         [2]uint64
	seen             [2]bool
}

// scanTailWindow folds frame headers of this stream in win into a tailScan.
func scanTailWindow(win []byte, si core.AudioTrack, fixedUnit int) tailScan {
	var st tailScan
	for i := 0; i+6 <= len(win); i++ {
		if win[i] != 0xFF || win[i+1]&0xFE != 0xF8 {
			continue
		}
		h, ok := decodeFrameHeader(win[i:], si)
		if !ok {
			continue
		}
		start := h.num
		if !h.variable {
			if fixedUnit == 0 {
				st.fixedUnplaceable = true
				continue
			}
			start = h.num * uint64(fixedUnit)
		}
		end := start + uint64(h.block)
		if end > si.TotalSamples {
			st.overrun = true
			continue
		}
		c := tailCandidate{off: i, start: start, end: end, hdr: h}
		strat := 0
		if h.variable {
			strat = 1
		}
		if !st.haveFirst {
			st.first, st.haveFirst = c, true
		}
		chained := st.seen[strat] && st.minStart[strat] < c.start
		if !st.have || c.end > st.f.end {
			st.f, st.have, st.fChained = c, true, chained
		}
		if !st.seen[strat] || c.start < st.minStart[strat] {
			st.minStart[strat], st.seen[strat] = c.start, true
		}
	}
	return st
}

// tailVerdict turns one window scan into warnings. conclusive is false when a
// wider window might change the answer (no candidate, or one uncorroborated
// checksum). Needs a second chaining header or whole-region frame-zero anchor.
// Overrun streams are left undiagnosed.
func tailVerdict(win []byte, st tailScan, wholeRegion bool, si core.AudioTrack) ([]core.Warning, int64, bool) {
	if !st.have {
		return nil, 0, false
	}
	if st.overrun {
		return nil, 0, true
	}
	anchored := wholeRegion && st.first.off == 0 && st.first.start == 0
	if !st.fChained && !anchored {
		return nil, 0, false
	}
	f := st.f

	if f.end < si.TotalSamples {
		return core.WarnTruncated(nil, "STREAMINFO"), 0, true
	}

	// Timeline complete; last frame starts at f. End via CRC-16 residue (frame
	// incl. trailer checksums to 0). Zero padding keeps residue 0, so ends form
	// a run; use the run with the latest hit. Runs of 1-2 trust the end (trailer
	// low zeros extend the run backward; real zero junk is longer). Bound by
	// worst-case frame size; incidental hits in non-zero junk can only shrink.
	raw := f.hdr.block * si.Channels * ((si.BitsPerSample + 7) / 8)
	scanEnd := min(f.off+raw+raw/4+4096, len(win))
	minEnd := f.off + f.hdr.size + 2 // a trailer cannot sit inside the header
	latest, runStart, prev := -1, -1, -1
	c := uint16(0)
	for p := f.off; p <= scanEnd; p++ {
		if c == 0 && p >= minEnd {
			if p != prev+1 {
				runStart = p
			}
			prev, latest = p, p
		}
		if p < scanEnd {
			c = crc16(c, win[p:p+1])
		}
	}
	if latest < 0 {
		// Samples accounted for but final trailer missing.
		return core.WarnTruncated(nil, "STREAMINFO"), 0, true
	}
	end := runStart
	if latest-runStart <= 2 {
		end = latest
	}
	if end == len(win) { // frame ends at audio region end
		return nil, 0, true
	}
	n := int64(len(win) - end)
	return core.WarnTrailing(nil, n, "after the FLAC stream", "belong to no frame"), n, true
}

// Frame CRC-8 (poly 0x07) and CRC-16 (poly 0x8005), MSB-first, init 0, no final XOR.
// Not in stdlib; tables built here (same idea as bits' Ogg CRC).
const (
	frameCRC8Poly  = 0x07
	frameCRC16Poly = 0x8005
)

var (
	crc8Table  = makeCRC8Table()
	crc16Table = makeCRC16Table()
)

func makeCRC8Table() *[256]uint8 {
	var t [256]uint8
	for n := 0; n < 256; n++ {
		c := uint8(n)
		for k := 0; k < 8; k++ {
			if c&0x80 != 0 {
				c = (c << 1) ^ frameCRC8Poly
			} else {
				c <<= 1
			}
		}
		t[n] = c
	}
	return &t
}

func makeCRC16Table() *[256]uint16 {
	var t [256]uint16
	for n := 0; n < 256; n++ {
		c := uint16(n) << 8
		for k := 0; k < 8; k++ {
			if c&0x8000 != 0 {
				c = (c << 1) ^ frameCRC16Poly
			} else {
				c <<= 1
			}
		}
		t[n] = c
	}
	return &t
}

// frameHeader: num is frame number (fixed) or first-sample number (variable);
// block is sample count; size is header length including CRC-8.
type frameHeader struct {
	variable bool
	num      uint64
	block    int
	size     int
}

// Sample-rate codes 1-11 → Hz. 0 = STREAMINFO; 12-14 in header; 15 invalid.
var frameRateTable = [12]int{0, 88200, 176400, 192000, 8000, 16000, 22050, 24000, 32000, 44100, 48000, 96000}

// Sample-size codes → bits. 0 = STREAMINFO; 3 reserved.
var frameSampleSizeTable = [8]int{0, 8, 12, -1, 16, 20, 24, 32}

// decodeFrameHeader validates sync, reserved fields, coded number, CRC-8, and
// agreement with STREAMINFO rate/channels/depth.
func decodeFrameHeader(b []byte, si core.AudioTrack) (frameHeader, bool) {
	if len(b) < 6 || b[0] != 0xFF || b[1]&0xFE != 0xF8 {
		return frameHeader{}, false
	}
	h := frameHeader{variable: b[1]&1 != 0}

	blockCode := b[2] >> 4
	rateCode := b[2] & 0x0F
	chCode := b[3] >> 4
	sizeCode := (b[3] >> 1) & 0x07
	if blockCode == 0 || rateCode == 15 || chCode >= 11 || b[3]&1 != 0 {
		return frameHeader{}, false
	}
	if depth := frameSampleSizeTable[sizeCode]; depth < 0 || (depth != 0 && depth != si.BitsPerSample) {
		return frameHeader{}, false
	}
	channels := int(chCode) + 1
	if chCode >= 8 { // left/side, right/side, mid/side: always two channels
		channels = 2
	}
	if channels != si.Channels {
		return frameHeader{}, false
	}

	// Frame number ≤31 bits (6 bytes); sample number ≤36 bits (7 bytes).
	maxNum := 6
	if h.variable {
		maxNum = 7
	}
	num, n, ok := decodeCodedNumber(b[4:], maxNum)
	if !ok {
		return frameHeader{}, false
	}
	h.num = num
	pos := 4 + n

	switch {
	case blockCode == 1:
		h.block = 192
	case blockCode <= 5:
		h.block = 576 << (blockCode - 2)
	case blockCode == 6:
		if pos >= len(b) {
			return frameHeader{}, false
		}
		h.block = int(b[pos]) + 1
		pos++
	case blockCode == 7:
		if pos+2 > len(b) {
			return frameHeader{}, false
		}
		h.block = (int(b[pos])<<8 | int(b[pos+1])) + 1
		pos += 2
	default:
		h.block = 256 << (blockCode - 8)
	}

	// Frame samples ≤ STREAMINFO max block size (last frame may be shorter).
	// Skip when bounds absent or below the 16-sample floor.
	if si.MaxBlockSize >= si.MinBlockSize && si.MaxBlockSize >= 16 && h.block > si.MaxBlockSize {
		return frameHeader{}, false
	}

	rate := 0
	switch {
	case rateCode <= 11:
		rate = frameRateTable[rateCode]
	case rateCode == 12:
		if pos >= len(b) {
			return frameHeader{}, false
		}
		rate = int(b[pos]) * 1000
		pos++
	default: // 13 (Hz) or 14 (tens of Hz)
		if pos+2 > len(b) {
			return frameHeader{}, false
		}
		rate = int(b[pos])<<8 | int(b[pos+1])
		if rateCode == 14 {
			rate *= 10
		}
		pos += 2
	}
	if rate != 0 && rate != si.SampleRate {
		return frameHeader{}, false
	}

	if pos >= len(b) || crc8(0, b[:pos]) != b[pos] {
		return frameHeader{}, false
	}
	h.size = pos + 1
	return h, true
}

// decodeCodedNumber: FLAC UTF-8-style number; lead high-bits give length (≤max),
// then 6 payload bits per continuation byte.
func decodeCodedNumber(b []byte, max int) (val uint64, n int, ok bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	lead := b[0]
	if lead&0x80 == 0 {
		return uint64(lead), 1, true
	}
	n = 2
	for mask := byte(0x20); lead&mask != 0; mask >>= 1 {
		n++
	}
	if lead&0x40 == 0 || n > max || n > len(b) {
		return 0, 0, false // bare continuation or too long
	}
	val = uint64(lead & (0x7F >> n))
	for _, c := range b[1:n] {
		if c&0xC0 != 0x80 {
			return 0, 0, false
		}
		val = val<<6 | uint64(c&0x3F)
	}
	return val, n, true
}

// crc8 continues a frame-header CRC over p.
func crc8(crc uint8, p []byte) uint8 {
	for _, b := range p {
		crc = crc8Table[crc^b]
	}
	return crc
}

// crc16 continues a frame CRC over p.
func crc16(crc uint16, p []byte) uint16 {
	for _, b := range p {
		crc = (crc << 8) ^ crc16Table[byte(crc>>8)^b]
	}
	return crc
}
