package flac

import (
	"context"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// The tail walk reads a window at the end of the audio region and grows it
// only while no frame header is found, so a healthy file costs one small read.
// Past the cap the region is left undiagnosed rather than walking a whole
// multi-gigabyte file at parse time.
const (
	tailWindowStart = 64 << 10
	tailWindowCap   = 4 << 20
)

// tailCandidate is one validated frame header found in the tail window:
// its window offset, the sample range it implies, and the decoded header.
type tailCandidate struct {
	off        int
	start, end uint64
	hdr        frameHeader
}

// frameTailWarnings walks the frame headers at the end of the audio region and
// reports the two conditions FLAC's structure alone cannot show: audio missing
// against STREAMINFO's declared sample count (a truncation), and bytes after
// the final frame that belong to no frame (appended junk). FLAC declares no
// encoded-essence byte length, so both verdicts come from the frames
// themselves: the 14-bit sync code plus the header CRC-8 identifies frames,
// the coded frame/sample number places them on the sample timeline, and the
// whole-frame CRC-16 locates the final frame's exact end. junk is that
// positively located trailing region's length (zero otherwise), so the caller
// can carve it out of the audio region. Diagnostics are best-effort: an
// unreadable window or a stream the walk cannot place (no declared total, or a
// fixed-strategy stream without a constant block size) reports nothing rather
// than guessing. Only a context error is returned, so a cancelled parse fails
// instead of succeeding with a different audio extent.
func frameTailWarnings(ctx context.Context, src core.ReaderAtSized, d *doc, limit int64) (ws []core.Warning, junk int64, err error) {
	si := d.streamInfo
	audioLen := d.audioEnd - d.audioStart
	if si.TotalSamples == 0 || audioLen <= 0 {
		return nil, 0, nil
	}
	// Fixed-strategy headers carry a frame number, positioned on the timeline
	// only when the stream's block size is constant.
	fixedUnit := 0
	if si.MinBlockSize == si.MaxBlockSize {
		fixedUnit = si.MinBlockSize
	}

	// The read cap follows the caller's alloc limit, so a limit below the
	// window sizes narrows the search the same way the 4 MiB cap does.
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
		// A fixed-strategy stream without a constant block size cannot be
		// placed on the timeline no matter how wide the window gets.
		if !st.have && st.fixedUnplaceable {
			return nil, 0, nil
		}
		if w == audioLen || w >= effCap {
			return nil, 0, nil
		}
	}
}

// tailScan is the reduced state of one window scan. It is O(1) regardless of
// how many frames the window holds: the verdict needs only the best candidate
// (greatest implied end, earliest offset on a tie), whether an earlier
// same-strategy candidate chains below it, the window's first candidate (the
// anchor test), and two poison flags.
type tailScan struct {
	f         tailCandidate
	have      bool
	fChained  bool
	first     tailCandidate
	haveFirst bool
	// overrun records a well-formed header implying samples beyond
	// STREAMINFO's declared total: the stream disagrees with its own header
	// in the direction the walk cannot reason about.
	overrun bool
	// fixedUnplaceable records a well-formed fixed-strategy header in a
	// stream whose STREAMINFO block-size bounds differ, so frame numbers
	// cannot be placed on the sample timeline.
	fixedUnplaceable bool
	minStart         [2]uint64
	seen             [2]bool
}

// scanTailWindow folds every offset in win that parses as a frame header of
// this stream into a tailScan.
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

// tailVerdict turns one window's scan into warnings. conclusive is false when
// a wider window could still change the answer: no candidate yet, or a lone
// uncorroborated one - a single lucky checksum in junk must neither end the
// search nor be read as the whole story. Every verdict requires corroboration
// (a second header chaining below the final frame, or the window covering the
// whole audio region with frame zero at its start), and a stream that
// overruns its declared total is conclusively left undiagnosed.
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

	// The sample timeline is complete, so the stream's last frame starts at f.
	// Its byte length is not declared anywhere; locate its end with the frame
	// CRC-16's residue: a frame including its trailer checksums to zero, so
	// every p where the CRC over [f.off, p) is zero is a possible end. Zero
	// bytes are invisible to this CRC (and only zero bytes hold a zero CRC at
	// zero), so zero-padding junk keeps the residue at zero: the positions
	// form a run of consecutive hits and the frame ends where the run begins.
	// The run containing the LATEST hit is used, so an incidental mid-frame
	// hit is never read as an early end; a run of one or two is trusted at
	// its END instead, because a trailer whose own low bytes are zero extends
	// the run backward into itself (calling that a byte of junk would flag
	// one clean file in 256), while real zero junk forms a longer run. The
	// scan is bounded by a generous worst-case frame size (verbatim subframes
	// plus slack); junk beyond the bound still counts in full, since the run
	// start is the frame end. An incidental residue hit inside non-zero junk
	// can only shrink the reported region, never grow it into real audio.
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
		// Every frame is accounted for yet the final frame's trailer is
		// nowhere: its tail bytes are missing.
		return core.WarnTruncated(nil, "STREAMINFO"), 0, true
	}
	end := runStart
	if latest-runStart <= 2 {
		end = latest
	}
	if end == len(win) { // the final frame ends exactly at the audio region's end
		return nil, 0, true
	}
	n := int64(len(win) - end)
	return core.WarnTrailing(nil, n, "after the FLAC stream", "belong to no frame"), n, true
}

// FLAC frame headers are guarded by a CRC-8 (poly 0x07) and whole frames by a
// CRC-16 (poly 0x8005), both MSB-first with init 0 and no final XOR. Neither
// matches a hash/crc variant in the standard library, so the tables are built
// here, mirroring internal/bits' Ogg CRC.
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

// frameHeader is one decoded frame header. num is the frame number under the
// fixed blocking strategy and the first-sample number under the variable one;
// block is this frame's sample count and size the header length in bytes, its
// CRC-8 included.
type frameHeader struct {
	variable bool
	num      uint64
	block    int
	size     int
}

// frameRateTable maps sample-rate codes 1-11 to Hz. Code 0 defers to
// STREAMINFO, 12-14 store the rate in the header, 15 is invalid.
var frameRateTable = [12]int{0, 88200, 176400, 192000, 8000, 16000, 22050, 24000, 32000, 44100, 48000, 96000}

// frameSampleSizeTable maps sample-size codes to bits. Code 0 defers to
// STREAMINFO; code 3 is reserved.
var frameSampleSizeTable = [8]int{0, 8, 12, -1, 16, 20, 24, 32}

// decodeFrameHeader decodes and validates the frame header at the start of b:
// the sync code, every field against its reserved values, the coded
// frame/sample number, and the closing CRC-8. A header whose coded sample
// rate, channel count, or bit depth disagrees with the stream's STREAMINFO is
// rejected too, so a random byte run that happens to checksum is still not
// mistaken for a frame of this stream.
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

	// The frame number is at most 31 bits (6 coded bytes); a sample number is
	// at most 36 bits (7 coded bytes).
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

	// A frame cannot hold more samples than STREAMINFO's maximum block size:
	// the last frame of a fixed-size stream may be shorter, never longer.
	// Skipped when the bounds are absent or nonsense (below the format's
	// 16-sample floor), the shape sloppy encoders write.
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

// decodeCodedNumber decodes FLAC's UTF-8-style extended number at the start of
// b: a lead byte whose run of high one-bits gives the total length (max coded
// bytes allowed by the caller), then 6 payload bits per continuation byte.
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
		return 0, 0, false // a bare continuation byte, or too long
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

// crc8 continues a FLAC frame-header CRC over p.
func crc8(crc uint8, p []byte) uint8 {
	for _, b := range p {
		crc = crc8Table[crc^b]
	}
	return crc
}

// crc16 continues a FLAC frame CRC over p.
func crc16(crc uint16, p []byte) uint16 {
	for _, b := range p {
		crc = (crc << 8) ^ crc16Table[byte(crc>>8)^b]
	}
	return crc
}
