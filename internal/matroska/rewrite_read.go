package matroska

import (
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// This file captures the byte-level rewrite base at parse time: the raw bytes and
// in-buffer offsets the write path patches or re-renders. Plan runs without the
// source (the Codec contract), so everything it needs, including SeekHead/Cues
// position slots, the Info Title location, and CRC spans, is recorded here while the
// source is open. All offsets are within the captured raw buffer.

// captureRaw reads an element's full bytes ([start, dataEnd)) bounded by the
// alloc limit. A failure (truncation, over-limit) returns nil so the writer
// falls back to refusing the edit rather than splicing partial bytes.
func captureRaw(src core.ReaderAtSized, el element, limit int64) []byte {
	b, err := bits.ReadSlice(src, el.start, el.dataEnd-el.start, limit)
	if err != nil {
		return nil
	}
	return b
}

// firstChildIsCRC reports whether a master element's first child is a CRC-32, so
// a re-render preserves that integrity convention. It requires the CRC payload to
// be exactly 4 bytes, matching rawCRC's malformed-payload rejection. If the two
// checks disagreed, a rewrite could drop or duplicate a malformed integrity field.
func firstChildIsCRC(src core.ReaderAtSized, el element, limit int64) bool {
	c, ok := readElement(src, el.dataStart, el.dataEnd, limit)
	return ok && c.id == idCRC32 && c.dataEnd-c.dataStart == 4
}

// rawCRC finds a master element's leading CRC-32 within its own captured bytes.
// raw is the full element; contentOff is where its payload begins (after the
// ID and size VINT). It returns nil when no CRC-32 leads the payload.
func rawCRC(raw []byte, contentOff int) *crcSpot {
	// Decode the size VINT from raw and accept the CRC only when its decoded value is
	// exactly 4, regardless of VINT width. firstChildIsCRC accepts any valid VINT
	// encoding; demanding the literal 0x84 here would miss an overlong CRC size such as
	// 0x40 0x04 and leave a stale CRC. Requiring the decoded value to be 4 still rejects
	// malformed elements whose oversized payload merely clamps to four readable bytes.
	if contentOff+1 >= len(raw) || raw[contentOff] != byte(idCRC32) {
		return nil
	}
	length := vintLen(raw[contentOff+1])
	// length is the VINT width in [1,8]. vintLen guarantees that, but keep the local
	// bound so the mask shift and value decode stay safe even if that helper changes.
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
// shift a moved target in place at its original width.
func captureSeekHead(src core.ReaderAtSized, el element, depth *bits.Depth, limit int64) *seekHead {
	raw := captureRaw(src, el, limit)
	if raw == nil {
		return nil
	}
	return seekFromRaw(raw, el.start, depth, limit)
}

// seekFromRaw parses a SeekHead from its own bytes, recording the element's file
// position (fileStart) and the in-raw position-slot offsets. buildResult reuses
// it on the patched bytes so the result equals a fresh parse.
func seekFromRaw(raw []byte, fileStart int64, depth *bits.Depth, limit int64) *seekHead {
	rs := core.BytesSource(raw)
	root, ok := readElement(rs, 0, int64(len(raw)), limit)
	if !ok {
		return nil
	}
	sh := &seekHead{start: fileStart, end: fileStart + int64(len(raw)), raw: raw, crc: rawCRC(raw, int(root.dataStart))}
	_ = eachChild(rs, root.dataStart, root.dataEnd, depth, limit, func(c element) error {
		if c.id != idSeek {
			return nil
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
				if s.dataLen() > 0 && s.dataLen() <= 8 {
					e.valOff, e.valLen, e.target = int(s.dataStart), int(s.dataLen()), readUint(rs, s, limit)
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
func captureCues(src core.ReaderAtSized, el element, depth *bits.Depth, limit int64) *cuesIndex {
	raw := captureRaw(src, el, limit)
	if raw == nil {
		return nil
	}
	return cuesFromRaw(raw, el.start, depth, limit)
}

func cuesFromRaw(raw []byte, fileStart int64, depth *bits.Depth, limit int64) *cuesIndex {
	rs := core.BytesSource(raw)
	root, ok := readElement(rs, 0, int64(len(raw)), limit)
	if !ok {
		return nil
	}
	ci := &cuesIndex{
		start: fileStart, end: fileStart + int64(len(raw)), raw: raw,
		crc: rawCRC(raw, int(root.dataStart)), maxDepth: depth.Max(), limit: limit,
	}
	// Record only the flat position list used by patchPositions. If a moved
	// position no longer fits in place, buildCuePoints reconstructs the nested
	// tree from ci.raw at that point. Keeping this path flat avoids retaining
	// per-child byte copies during ordinary parses while preserving the exact
	// in-place behavior.
	var walk func(start, end int64)
	walk = func(start, end int64) {
		_ = eachChild(rs, start, end, depth, limit, func(c element) error {
			switch c.id {
			case idCuePoint, idCueTrackPos:
				walk(c.dataStart, c.dataEnd) // descend to the CueClusterPosition leaves
			case idCueClusterPos:
				if c.dataLen() > 0 && c.dataLen() <= 8 {
					ci.clusters = append(ci.clusters, seekEntry{
						valOff: int(c.dataStart), valLen: int(c.dataLen()), target: readUint(rs, c, limit),
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
// rebuild. The work stays lazy because offset shifts do not affect ci.raw, so
// callers may cache the result for the duration of one layout fixpoint. ok is false
// when the tree cannot be modeled faithfully, causing the writer to refuse the
// rebuild rather than emit a reordered, partial, or wrong index.
func buildCuePoints(ci *cuesIndex) (points []cuePoint, ok bool) {
	rs := core.BytesSource(ci.raw)
	root, found := readElement(rs, 0, int64(len(ci.raw)), ci.limit)
	if !found {
		return nil, false
	}
	// Use a fresh Depth. Depth tracks current recursion, so reusing a consumed guard
	// would leak state into this walk. The saved maxDepth can be slightly more
	// permissive than the original Segment-nested parse; the countTrackPos check
	// below still requires the lazy tree to match the flat list captured on the
	// original walk.
	points, ok = capturePoints(rs, ci.raw, root, bits.NewDepth(ci.maxDepth), ci.limit)
	// The tree must account for exactly the same positions as the flat list. If it
	// does not, it is not a faithful basis for a rebuild.
	if countTrackPos(points) != len(ci.clusters) {
		ok = false
	}
	return points, ok
}

// capturePoints parses the Cues element's CuePoint children into the rebuild tree.
// A leading CRC-32 and Void padding are tolerated (the CRC is carried on ci.crc and
// re-added; Void is dropped on rebuild, matching rebuildSeekHead, which emits only
// its captured entries). ok is false when a child cannot be modeled faithfully (an
// unexpected non-CuePoint child, a non-leading CRC the rebuild cannot reproduce, or
// a CuePoint a flat re-render would reorder) or the walk was truncated by the depth
// bound (eachChild's only error), so the caller refuses a rebuild rather than
// emitting a reordered, partial, or wrong index.
func capturePoints(rs core.ReaderAtSized, raw []byte, root element, depth *bits.Depth, limit int64) ([]cuePoint, bool) {
	var points []cuePoint
	ok := true
	idx := 0
	err := eachChild(rs, root.dataStart, root.dataEnd, depth, limit, func(c element) error {
		switch c.id {
		case idCRC32:
			if idx > 0 {
				ok = false // a non-leading CRC ci.crc does not reproduce
			}
		case idVoid:
			// padding: dropped on rebuild (the flat clusters walk ignores it too)
		case idCuePoint:
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
// every other meaningful child (CueTime, unmodeled fields) verbatim into prefix, with
// a leading CRC stripped (re-added via hasCRC) and Void dropped as padding. ok is
// false when a non-position, non-padding child trails a CueTrackPositions (the
// prefix-then-tracks render would reorder it), a non-leading CRC appears, or the
// point carries no CueTrackPositions.
func capturePoint(rs core.ReaderAtSized, raw []byte, cp element, depth *bits.Depth, limit int64) (cuePoint, bool) {
	pt := cuePoint{hasCRC: firstChildIsCRC(rs, cp, limit)}
	ok := true
	seenTrack := false
	idx := 0
	err := eachChild(rs, cp.dataStart, cp.dataEnd, depth, limit, func(c element) error {
		switch {
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

// captureTrackPos parses one CueTrackPositions, splitting its children at the
// CueClusterPosition into pre (before, e.g. CueTrack) and post (after, e.g.
// CueRelativePosition) kept verbatim, recording the position target. A leading CRC is
// stripped (re-added via hasCRC) and Void dropped as padding. ok is false unless it
// holds exactly one well-formed CueClusterPosition and no non-leading CRC.
func captureTrackPos(rs core.ReaderAtSized, raw []byte, ctp element, depth *bits.Depth, limit int64) (cueTrackPos, bool) {
	tp := cueTrackPos{hasCRC: firstChildIsCRC(rs, ctp, limit)}
	positions, bad := 0, false
	idx := 0
	err := eachChild(rs, ctp.dataStart, ctp.dataEnd, depth, limit, func(c element) error {
		switch {
		case c.id == idCRC32:
			if idx > 0 {
				bad = true // a non-leading CRC hasCRC does not reproduce
			}
		case c.id == idVoid:
			// padding: dropped on rebuild
		case c.id == idCueClusterPos:
			positions++
			if c.dataLen() > 0 && c.dataLen() <= 8 {
				tp.target, tp.hasPos = readUint(rs, c, limit), true
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

// captureInfo records the Info element's bytes, CRC, and Title-child location so
// a Title edit can splice a new Title (or remove/insert one) and recompute the
// CRC without re-deriving the other Info children (Duration, SegmentUID, ...),
// which are preserved verbatim within raw.
func captureInfo(src core.ReaderAtSized, el element, depth *bits.Depth, limit int64) *infoBlock {
	raw := captureRaw(src, el, limit)
	if raw == nil {
		return nil
	}
	return infoFromRaw(raw, el.start, depth, limit)
}

func infoFromRaw(raw []byte, fileStart int64, depth *bits.Depth, limit int64) *infoBlock {
	rs := core.BytesSource(raw)
	root, ok := readElement(rs, 0, int64(len(raw)), limit)
	if !ok {
		return nil
	}
	ib := &infoBlock{start: fileStart, end: fileStart + int64(len(raw)), raw: raw, crc: rawCRC(raw, int(root.dataStart)), titleOff: -1, titleEnd: -1}
	ib.insertOff = int(root.dataStart)
	if ib.crc != nil {
		ib.insertOff = ib.crc.contentStart // keep the CRC first
	}
	_ = eachChild(rs, root.dataStart, root.dataEnd, depth, limit, func(c element) error {
		if c.id == idSegTitle {
			ib.titleOff, ib.titleEnd = int(c.start), int(c.dataEnd)
		}
		return nil
	})
	return ib
}
