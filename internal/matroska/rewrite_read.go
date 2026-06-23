package matroska

import (
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// This file captures the byte-level rewrite base at parse time: the raw bytes and
// in-buffer offsets the write path patches or re-renders. Plan runs without the
// source (the Codec contract), so everything it needs - SeekHead/Cues position
// slots, the Info Title location, the CRC spans - is recorded here while the
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
// a re-render reproduces that integrity convention.
func firstChildIsCRC(src core.ReaderAtSized, el element, limit int64) bool {
	c, ok := readElement(src, el.dataStart, el.dataEnd, limit)
	return ok && c.id == idCRC32
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
	ci := &cuesIndex{start: fileStart, end: fileStart + int64(len(raw)), raw: raw, crc: rawCRC(raw, int(root.dataStart))}
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
