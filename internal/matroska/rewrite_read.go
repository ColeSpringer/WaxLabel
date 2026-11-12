package matroska

import (
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// This file captures the byte-level rewrite base at parse time: the raw bytes and
// in-buffer offsets the write path patches or re-renders.

// captureRaw reads an element's full bytes, nil for one that overruns its parent: placed ahead of
// anything, such bytes would take it for their own. A read that fails fatally ([bits.Fatal]) is
// the error; another read failure returns nil, and the writer then refuses the edit.
func captureRaw(src core.ReaderAtSized, el element, limit int64) ([]byte, error) {
	if el.overruns() {
		return nil, nil
	}
	return captureHeld(src, el, limit)
}

// captureHeld reads the bytes the file holds of an element ([start, dataEnd)), which fall short
// of one that overruns its parent. They are safe only where they stand.
func captureHeld(src core.ReaderAtSized, el element, limit int64) ([]byte, error) {
	b, err := bits.ReadSlice(src, el.start, el.dataEnd-el.start, limit)
	if err != nil {
		return nil, bits.Fatal(err)
	}
	return b, nil
}

// firstChildIsCRC reports whether a master element's first child is a CRC-32, so a
// re-render preserves that integrity convention. A header read that fails fatally is the error.
func firstChildIsCRC(src core.ReaderAtSized, el element, limit int64) (bool, error) {
	c, ok, err := readElement(src, el.dataStart, el.dataEnd, limit)
	return ok && c.id == idCRC32 && c.dataEnd-c.dataStart == 4, err
}

// rawCRC finds a master element's leading CRC-32 within its own captured bytes.
// raw is the full element; contentOff is where its payload begins (after the
// ID and size VINT). It returns nil when no CRC-32 leads the payload.
func rawCRC(raw []byte, contentOff int) *crcSpot {
	// Accept the CRC only when its size VINT decodes to exactly 4, whatever its width.
	if contentOff+1 >= len(raw) || raw[contentOff] != byte(idCRC32) {
		return nil
	}
	length := vintLen(raw[contentOff+1])
	// The local bound keeps the mask shift and decode safe even if vintLen changes.
	if length == 0 || length > 8 || contentOff+1+length+4 > len(raw) {
		return nil
	}
	mask := byte(0x80 >> (length - 1))
	val := uint64(raw[contentOff+1] &^ mask)
	for i := 1; i < length; i++ {
		val = val<<8 | uint64(raw[contentOff+1+i])
	}
	if val != 4 {
		return nil
	}
	return &crcSpot{valOff: contentOff + 1 + length, contentStart: contentOff + 1 + length + 4}
}

// captureSeekHead records the SeekHead's SeekPosition slots so the writer can
// shift a moved target in place at its original width. An index or Info the end of the file cuts
// is captured as far as it goes: it is patched where it stands, the last element of the file.
func captureSeekHead(src core.ReaderAtSized, el element, depth *bits.Depth, limit int64) (*seekHead, error) {
	raw, err := captureHeld(src, el, limit)
	if raw == nil {
		return nil, err
	}
	return seekFromRaw(raw, el.start, depth, limit), nil
}

// seekFromRaw parses a SeekHead from its own bytes, recording the element's file
// position (fileStart) and the in-raw position-slot offsets.
func seekFromRaw(raw []byte, fileStart int64, depth *bits.Depth, limit int64) *seekHead {
	rs := core.BytesSource(raw)
	root, ok, err := readElement(rs, 0, int64(len(raw)), limit)
	if !ok || err != nil {
		return nil
	}
	sh := &seekHead{start: fileStart, end: fileStart + int64(len(raw)), raw: raw, crc: rawCRC(raw, int(root.dataStart)), cut: root.clamped}
	_ = eachChild(rs, root.dataStart, root.dataEnd, depth, limit, func(c element) error {
		if c.id != idSeek {
			return nil
		}
		// A per-Seek leading CRC-32 is valid but is not recomputed by the in-place
		// patch. Latch the flag once and rebuild instead.
		if !sh.hasNestedCRC {
			sh.hasNestedCRC, _ = firstChildIsCRC(rs, c, limit) // in memory: no read fails
		}
		var e seekEntry
		var have bool
		_ = eachChild(rs, c.dataStart, c.dataEnd, depth, limit, func(s element) error {
			switch s.id {
			case idSeekID:
				if b, err := bits.ReadSlice(rs, s.dataStart, s.dataLen(), limit); err == nil {
					e.idRaw = b
				}
			case idSeekPosition:
				if !s.overruns() && s.dataLen() > 0 && s.dataLen() <= 8 {
					e.valOff, e.valLen = int(s.dataStart), int(s.dataLen())
					e.target, _ = readUint(rs, s, limit) // in memory: no read fails
					have = true
				}
			}
			return nil
		})
		if have {
			sh.entries = append(sh.entries, e)
		}
		return nil
	})
	return sh
}

// captureCues records the CueClusterPosition slots (segment-relative cluster
// offsets) the same way, so a tag edit that shifts the clusters can repoint them.
func captureCues(src core.ReaderAtSized, el element, depth *bits.Depth, limit int64) (*cuesIndex, error) {
	raw, err := captureHeld(src, el, limit)
	if raw == nil {
		return nil, err
	}
	return cuesFromRaw(raw, el.start, depth, limit), nil
}

func cuesFromRaw(raw []byte, fileStart int64, depth *bits.Depth, limit int64) *cuesIndex {
	rs := core.BytesSource(raw)
	root, ok, err := readElement(rs, 0, int64(len(raw)), limit)
	if !ok || err != nil {
		return nil
	}
	ci := &cuesIndex{
		start: fileStart, end: fileStart + int64(len(raw)), raw: raw,
		crc: rawCRC(raw, int(root.dataStart)), maxDepth: depth.Max(), limit: limit, cut: root.clamped,
	}
	// Record only the flat position list used by patchPositions.
	var walk func(start, end int64)
	walk = func(start, end int64) {
		_ = eachChild(rs, start, end, depth, limit, func(c element) error {
			switch c.id {
			case idCuePoint, idCueTrackPos:
				// A leading CRC-32 on CuePoint or CueTrackPositions is valid but is not
				// recomputed by patchPositions. Latch the flag once and rebuild instead.
				if !ci.hasNestedCRC {
					ci.hasNestedCRC, _ = firstChildIsCRC(rs, c, limit) // in memory: no read fails
				}
				walk(c.dataStart, c.dataEnd) // descend to the CueClusterPosition leaves
			case idCueClusterPos:
				if !c.overruns() && c.dataLen() > 0 && c.dataLen() <= 8 {
					target, _ := readUint(rs, c, limit) // in memory: no read fails
					ci.clusters = append(ci.clusters, seekEntry{
						valOff: int(c.dataStart), valLen: int(c.dataLen()), target: target,
					})
				}
			}
			return nil
		})
	}
	walk(root.dataStart, root.dataEnd)
	return ci
}

// buildCuePoints parses a captured Cues element into the tree needed for a full
// rebuild.
func buildCuePoints(ci *cuesIndex) (points []cuePoint, ok bool) {
	rs := core.BytesSource(ci.raw)
	root, found, err := readElement(rs, 0, int64(len(ci.raw)), ci.limit)
	if !found || err != nil {
		return nil, false
	}
	// Use a fresh Depth.
	points, ok = capturePoints(rs, ci.raw, root, bits.NewDepth(ci.maxDepth), ci.limit)
	// The tree must hold exactly the positions of the flat list, or a rebuild cannot trust it.
	if countTrackPos(points) != len(ci.clusters) {
		ok = false
	}
	return points, ok
}

// capturePoints parses the Cues element's CuePoint children into the rebuild tree.
func capturePoints(rs core.ReaderAtSized, raw []byte, root element, depth *bits.Depth, limit int64) ([]cuePoint, bool) {
	var points []cuePoint
	ok := true
	idx := 0
	err := eachChild(rs, root.dataStart, root.dataEnd, depth, limit, func(c element) error {
		switch {
		case c.overruns():
			ok = false // cut short: not a child a rebuild can render again
		case c.id == idCRC32:
			if idx > 0 {
				ok = false // a non-leading CRC ci.crc does not reproduce
			}
		case c.id == idVoid:
			// padding: dropped on rebuild (the flat clusters walk ignores it too)
		case c.id == idCuePoint:
			pt, pok := capturePoint(rs, raw, c, depth, limit)
			points = append(points, pt)
			ok = ok && pok
		default:
			ok = false // an unexpected top-level Cues child the rebuild cannot reproduce
		}
		idx++
		return nil
	})
	return points, ok && err == nil
}

// capturePoint parses one CuePoint: its CueTrackPositions into the tracks list and
// every other meaningful child (CueTime, unmodeled fields) verbatim into prefix, with a
// leading CRC stripped (re-added via hasCRC) and Void dropped as padding.
func capturePoint(rs core.ReaderAtSized, raw []byte, cp element, depth *bits.Depth, limit int64) (cuePoint, bool) {
	hasCRC, _ := firstChildIsCRC(rs, cp, limit) // in memory: no read fails
	pt := cuePoint{hasCRC: hasCRC}
	ok := true
	seenTrack := false
	idx := 0
	err := eachChild(rs, cp.dataStart, cp.dataEnd, depth, limit, func(c element) error {
		switch {
		case c.overruns():
			ok = false
		case c.id == idCRC32:
			if idx > 0 {
				ok = false // a non-leading CRC hasCRC does not reproduce
			}
		case c.id == idVoid:
			// padding: dropped on rebuild
		case c.id == idCueTrackPos:
			tp, tok := captureTrackPos(rs, raw, c, depth, limit)
			pt.tracks = append(pt.tracks, tp)
			ok = ok && tok
			seenTrack = true
		case seenTrack:
			ok = false // a non-position child after the tracks: a flat render reorders it
			pt.prefix = append(pt.prefix, raw[c.start:c.dataEnd]...)
		default:
			pt.prefix = append(pt.prefix, raw[c.start:c.dataEnd]...)
		}
		idx++
		return nil
	})
	return pt, ok && err == nil && len(pt.tracks) > 0
}

// captureTrackPos parses one CueTrackPositions, keeping the children before the
// CueClusterPosition in pre and those after it in post (both verbatim) and recording
// the position target.
func captureTrackPos(rs core.ReaderAtSized, raw []byte, ctp element, depth *bits.Depth, limit int64) (cueTrackPos, bool) {
	hasCRC, _ := firstChildIsCRC(rs, ctp, limit) // in memory: no read fails
	tp := cueTrackPos{hasCRC: hasCRC}
	positions, bad := 0, false
	idx := 0
	err := eachChild(rs, ctp.dataStart, ctp.dataEnd, depth, limit, func(c element) error {
		switch {
		case c.overruns():
			bad = true
		case c.id == idCRC32:
			if idx > 0 {
				bad = true // a non-leading CRC hasCRC does not reproduce
			}
		case c.id == idVoid:
			// padding: dropped on rebuild
		case c.id == idCueClusterPos:
			positions++
			if c.dataLen() > 0 && c.dataLen() <= 8 {
				tp.target, _ = readUint(rs, c, limit) // in memory: no read fails
				tp.hasPos = true
			} else {
				bad = true
			}
		case !tp.hasPos:
			tp.pre = append(tp.pre, raw[c.start:c.dataEnd]...)
		default:
			tp.post = append(tp.post, raw[c.start:c.dataEnd]...)
		}
		idx++
		return nil
	})
	return tp, err == nil && positions == 1 && !bad
}

// countTrackPos totals the positions held across a captured CuePoint tree, used to
// cross-check the tree against the flat clusters list.
func countTrackPos(points []cuePoint) int {
	n := 0
	for _, p := range points {
		for _, t := range p.tracks {
			if t.hasPos {
				n++
			}
		}
	}
	return n
}

// captureInfo records the Info element's bytes, CRC, and Title-child location, so a
// Title edit can splice, insert, or remove the Title and recompute the CRC while the
// other children (Duration, SegmentUID, ...) stay verbatim.
func captureInfo(src core.ReaderAtSized, el element, depth *bits.Depth, limit int64) (*infoBlock, error) {
	raw, err := captureHeld(src, el, limit)
	if raw == nil {
		return nil, err
	}
	return infoFromRaw(raw, el.start, depth, limit), nil
}

func infoFromRaw(raw []byte, fileStart int64, depth *bits.Depth, limit int64) *infoBlock {
	rs := core.BytesSource(raw)
	root, ok, err := readElement(rs, 0, int64(len(raw)), limit)
	if !ok || err != nil {
		return nil
	}
	ib := &infoBlock{start: fileStart, end: fileStart + int64(len(raw)), raw: raw, crc: rawCRC(raw, int(root.dataStart)), titleOff: -1, titleEnd: -1, wholeEnd: len(raw)}
	ib.insertOff = int(root.dataStart)
	if ib.crc != nil {
		ib.insertOff = ib.crc.contentStart // keep the CRC first
	}
	stop, _ := eachChildReport(rs, root.dataStart, root.dataEnd, depth, limit, func(c element) error {
		switch {
		case c.overruns():
			ib.wholeEnd = int(c.start)
		case c.id == idSegTitle:
			ib.titleOff, ib.titleEnd = int(c.start), int(c.dataEnd)
		}
		return nil
	})
	ib.wholeEnd = min(ib.wholeEnd, int(stop))
	return ib
}
