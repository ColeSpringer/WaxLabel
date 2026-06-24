package matroska

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// planShift is the fallback when absorption does not apply (no reserved Void, or
// the edited header does not fit one). It re-renders the changed children in
// place and lets the tail shift by the size delta, then repoints the moved
// segment-relative positions: CueClusterPosition and the SeekHead SeekPositions
// of elements after the edit. It also recomputes the affected CRC-32s and the
// Segment size. Because the clusters move, the result records their new range.
//
// Both indexes are patched in place at their original width when every moved
// position fits (no element size changes, so no cascade); when a position would
// overflow its width or its target was dropped, that index, SeekHead or Cues, is
// re-encoded at minimal width, and the resulting size changes are resolved together
// by a short bounded layout fixpoint (resolveShiftLayout). The common art operation
// (embedding an album cover that pushes the clusters past a VINT-width boundary)
// takes the Cues rebuild path. Only a genuinely unrebuildable index is refused.
func planShift(d *doc, base, edited *core.Media, ch changes, ek map[tag.Key]bool, report core.WriteReport) (*core.WritePlan, error) {
	wb := d.wb
	r, err := renderChanged(d, base, edited, ch, ek)
	if err != nil {
		return nil, err
	}

	items, seekIdx, cuesIdx := buildShiftItems(wb, ch, r)

	lay, _, ok := resolveShiftLayout(wb, items, seekIdx, cuesIdx)
	if !ok {
		return nil, overflowErr()
	}

	report.BytesAfter = lay.size
	report.Operations = shiftOps(ch, len(edited.Pictures), lay.delta)
	result := buildResult(d, edited, r, ch, lay)
	return &core.WritePlan{Segments: lay.segs, NoOp: false, Report: report, Result: result}, nil
}

// shiftIndex wraps one segment-relative index, either SeekHead or Cues. itemIdx is
// its outItem slot; a negative value means the index is absent. raw/crc/entries
// feed the in-place patch path, rebuild handles the minimal-width fallback, and
// force latches that fallback once it has been needed.
type shiftIndex struct {
	itemIdx int // its outItem index; <0 if the index is absent
	raw     []byte
	crc     *crcSpot
	entries []seekEntry // flat slots driving the in-place patch fast path
	rebuild func(oldToNew map[int64]int64, inSeg, outSeg int64) ([]byte, int64, bool)
	force   bool // monotone latch: once a rebuild was needed, stays set
}

// emit returns the index bytes for the current layout iteration. It patches in
// place while possible; after force is set, or if a slot cannot fit or its target
// is gone, it calls rebuild. ok is false only when rebuild cannot faithfully
// produce an index.
func (ix *shiftIndex) emit(oldToNew map[int64]int64, inSeg, outSeg int64) (out []byte, n int64, rebuilt, ok bool) {
	if ix.itemIdx < 0 {
		return nil, 0, false, true
	}
	if !ix.force {
		if patched, ok := patchPositions(ix.raw, ix.crc, ix.entries, oldToNew, inSeg, outSeg); ok {
			return patched, int64(len(patched)), false, true
		}
	}
	out, n, ok = ix.rebuild(oldToNew, inSeg, outSeg)
	return out, n, true, ok
}

// buildShiftIndexes prepares SeekHead and Cues indexes for the layout fixpoint.
// The Cues rebuild closure caches buildCuePoints because parsing ci.raw is
// independent of output offsets; SeekHead rebuilds directly from its flat entry
// list. The closures keep state local to this write attempt and do not mutate the
// shared writeBase.
func buildShiftIndexes(wb *writeBase, seekIdx, cuesIdx int) []*shiftIndex {
	seek := &shiftIndex{itemIdx: seekIdx}
	if seekIdx >= 0 {
		sh := wb.seek
		seek.raw, seek.crc, seek.entries = sh.raw, sh.crc, sh.entries
		seek.rebuild = func(oldToNew map[int64]int64, inSeg, outSeg int64) ([]byte, int64, bool) {
			return rebuildSeekHead(sh, oldToNew, inSeg, outSeg)
		}
	}

	cues := &shiftIndex{itemIdx: cuesIdx}
	if cuesIdx >= 0 {
		ci := wb.cues
		cues.raw, cues.crc, cues.entries = ci.raw, ci.crc, ci.clusters
		var (
			points   []cuePoint
			pointsOK bool
			parsed   bool
		)
		cues.rebuild = func(oldToNew map[int64]int64, inSeg, outSeg int64) ([]byte, int64, bool) {
			if !parsed {
				points, pointsOK = buildCuePoints(ci) // parse ci.raw once per edit
				parsed = true
			}
			if !pointsOK {
				return nil, 0, false
			}
			return encodeCues(points, ci.crc, len(ci.raw), oldToNew, inSeg, outSeg)
		}
	}

	return []*shiftIndex{seek, cues}
}

// resolveShiftLayout runs the bounded layout fixpoint that places the shifted
// children and repoints the moved positions. Each iteration recomputes the old->new
// offset map from the current item sizes, emits both indexes (SeekHead and Cues),
// installs the fresh bytes, and loops if either index changed size. A size change
// moves later elements, which changes the offsets stored in both indexes.
//
// Re-encoding (a full rebuild) latches per index: once a position overflows its
// in-place slot, or an entry to a dropped element must go, that index re-encodes at
// minimal width every iteration thereafter. Minimal-width re-encoding is a monotone
// fixpoint; flipping back to in-place patching could oscillate around a width
// boundary. The bytes are installed every iteration even when the size is unchanged
// (see installIndex): a same-length re-encode can still hold different position
// values after a prior growth shifted a cluster, so the size comparison decides only
// whether to loop again, never whether to install.
//
// emit and installIndex run for each index on every iteration, accumulating one
// resized flag. emit reads only immutable writeBase state and oldToNew, so
// installing one index cannot affect another emission until the next iteration.
//
// iters is returned for regression tests. ok is false when an index cannot be
// re-encoded (an uncaptured Cues tree, a SeekHead missing a SeekID, or a degenerate
// empty result), which the caller surfaces as overflowErr.
func resolveShiftLayout(wb *writeBase, items []outItem, seekIdx, cuesIdx int) (lay layout, iters int, ok bool) {
	indexes := buildShiftIndexes(wb, seekIdx, cuesIdx)
	for iters = 1; iters <= 8; iters++ {
		segLead, outSegStart, oldToNew, bodyLen := computeShiftLayout(wb, items)

		resized := false
		for _, ix := range indexes {
			b, n, rebuilt, iok := ix.emit(oldToNew, wb.segDataStart, outSegStart)
			if !iok {
				return layout{}, iters, false
			}
			ix.force = ix.force || rebuilt
			if installIndex(items, ix.itemIdx, b, n) {
				resized = true
			}
		}
		if resized {
			continue // an index size changed: re-lay-out and recompute every position
		}
		return assembleShift(wb, items, segLead, outSegStart, bodyLen), iters, true
	}
	return layout{}, iters, false
}

// installIndex writes a re-encoded index's bytes into its output item and reports
// whether its size changed (which forces another layout pass). It always installs
// the bytes, even on an unchanged size, because a same-length re-encode can still
// carry different position values after a prior cluster shift. Installing only on
// a size change would leave a silently corrupt index that still parses.
func installIndex(items []outItem, idx int, b []byte, n int64) (resized bool) {
	if idx < 0 {
		return false
	}
	resized = n != items[idx].n
	items[idx].n, items[idx].lit, items[idx].srcOff = n, b, 0
	return resized
}

// buildShiftItems produces the output item list for every top-level child, with
// the changed children substituted and any newly created Tags/Attachments/Chapters
// placed immediately before the cluster tail. It returns the SeekHead and Cues item indices.
func buildShiftItems(wb *writeBase, ch changes, r *rendered) (items []outItem, seekIdx, cuesIdx int) {
	seekIdx, cuesIdx = -1, -1
	insertAt := -1
	tagsPlaced, attachPlaced, chaptersPlaced := false, false, false
	for _, c := range wb.children {
		if insertAt < 0 && c.start >= wb.clusterStart {
			insertAt = len(items)
		}
		switch {
		case c.id == idSeekHead && wb.seek != nil:
			seekIdx = len(items)
			items = append(items, copyItem(c, itemSeek))
		case c.id == idCues && wb.cues != nil:
			cuesIdx = len(items)
			items = append(items, copyItem(c, itemOther))
		case c.id == idInfo && ch.title:
			items = append(items, litItem(idInfo, r.info, c.start, itemInfo))
		case c.id == idTags && ch.simple:
			if tagsPlaced || r.tags == nil {
				continue // dropped, or a second Tags master (the first carries every group)
			}
			tagsPlaced = true
			items = append(items, litItem(idTags, r.tags, c.start, itemTags))
		case c.id == idAttachments && ch.pictures:
			if attachPlaced || r.attach == nil {
				continue // dropped, or a second Attachments master
			}
			attachPlaced = true
			items = append(items, litItem(idAttachments, r.attach, c.start, itemAttach))
		case c.id == idChapters && ch.chapters:
			if chaptersPlaced || r.chapters == nil {
				continue // dropped (cleared), or a second Chapters element
			}
			chaptersPlaced = true
			items = append(items, litItem(idChapters, r.chapters, c.start, itemChapters))
		default:
			items = append(items, copyItem(c, itemOther))
		}
	}
	if insertAt < 0 {
		insertAt = len(items)
	}
	// Newly created top-level elements get origStart -1; they are appended to the
	// output but not added to an existing SeekHead. The index is
	// preserved/patched at a stable size, never regenerated to gain entries, so
	// adding one would grow the SeekHead and perturb the size-preserving layout.
	// SeekHead is an optional index per RFC 9559 and readers locate level-1
	// elements by scanning, so an unindexed new Tags/Attachments/Chapters is still
	// found. This is the same deliberate limitation for all three created element kinds.
	var created []outItem
	if ch.simple && !tagsPlaced && r.tags != nil {
		created = append(created, litItem(idTags, r.tags, -1, itemTags))
	}
	if ch.pictures && !attachPlaced && r.attach != nil {
		created = append(created, litItem(idAttachments, r.attach, -1, itemAttach))
	}
	if ch.chapters && !chaptersPlaced && r.chapters != nil {
		created = append(created, litItem(idChapters, r.chapters, -1, itemChapters))
	}
	if len(created) > 0 {
		items = insertItems(items, insertAt, created)
		if seekIdx >= insertAt {
			seekIdx += len(created)
		}
		if cuesIdx >= insertAt {
			cuesIdx += len(created)
		}
	}
	return items, seekIdx, cuesIdx
}

// computeShiftLayout recomputes the Segment lead (the recomputed size VINT), the
// output data start, the old->new start map, and the new Segment body length from
// the current item sizes.
func computeShiftLayout(wb *writeBase, items []outItem) (segLead []bits.Segment, outSegStart int64, oldToNew map[int64]int64, bodyLen int64) {
	for _, it := range items {
		bodyLen += it.n
	}
	segLead, outSegStart = segmentLead(wb, bodyLen)
	oldToNew = make(map[int64]int64, len(items))
	off := outSegStart
	for i := range items {
		items[i].outOff = off
		if items[i].origStart >= 0 {
			oldToNew[items[i].origStart] = off
		}
		off += items[i].n
	}
	return segLead, outSegStart, oldToNew, bodyLen
}

// rebuildSeekHead re-encodes the SeekHead from its entries with each SeekPosition
// at minimal width and entries to dropped targets omitted. It needs each entry's
// captured SeekID; ok is false if one is missing.
func rebuildSeekHead(sh *seekHead, oldToNew map[int64]int64, inSegStart, outSegStart int64) ([]byte, int64, bool) {
	var content []byte
	for _, e := range sh.entries {
		newOff, ok := oldToNew[inSegStart+int64(e.target)]
		if !ok {
			continue // target dropped; omit the stale entry
		}
		if e.idRaw == nil {
			return nil, 0, false
		}
		seek := encElement(idSeekID, e.idRaw)
		seek = append(seek, uintElement(idSeekPosition, uint64(newOff-outSegStart))...)
		content = append(content, encElement(idSeek, seek)...)
	}
	out := masterElement(idSeekHead, content, sh.crc != nil)
	return out, int64(len(out)), true
}

// rebuildCues re-encodes the Cues element at minimal width. It builds the
// CuePoint tree from the captured raw bytes and passes that tree to encodeCues.
// Unit tests call this uncached path; resolveShiftLayout caches the tree across
// the fixpoint. ok is false when the tree cannot be captured faithfully or the
// result would be an invalid empty Cues.
func rebuildCues(ci *cuesIndex, oldToNew map[int64]int64, inSegStart, outSegStart int64) ([]byte, int64, bool) {
	points, ok := buildCuePoints(ci)
	if !ok {
		return nil, 0, false
	}
	return encodeCues(points, ci.crc, len(ci.raw), oldToNew, inSegStart, outSegStart)
}

// encodeCues re-encodes a Cues element from a captured CuePoint tree with every
// CueClusterPosition at minimal width. A cluster offset fits in eight bytes, so
// overflow is not possible here. Non-position children such as CueTime, CueTrack,
// and CueRelativePosition are emitted from the captured prefix/pre/post slices so
// unmodeled fields survive byte-for-byte. A CueTrackPositions whose target cluster
// was dropped is omitted; a CuePoint left with no positions disappears. crc != nil
// reproduces a leading CRC-32, and sizeHint pre-sizes the accumulator. ok is false
// when the result would be an invalid empty Cues.
func encodeCues(points []cuePoint, crc *crcSpot, sizeHint int, oldToNew map[int64]int64, inSeg, outSeg int64) (out []byte, length int64, ok bool) {
	// The rebuilt element is usually close in size to the original, so size the
	// accumulator from the captured bytes to avoid repeated growth.
	content := make([]byte, 0, sizeHint)
	for _, p := range points {
		// Clone prefix/pre so the appends below never write into the captured tree,
		// which may be reused across fixpoint iterations.
		pc := slices.Clone(p.prefix)
		tracks := 0
		for _, tp := range p.tracks {
			tc := slices.Clone(tp.pre)
			if tp.hasPos {
				newOff, present := oldToNew[inSeg+int64(tp.target)]
				if !present {
					continue // target cluster dropped: omit this CueTrackPositions
				}
				if newOff < outSeg {
					return nil, 0, false // (unreachable) a target before the segment data start would underflow
				}
				tc = append(tc, uintElement(idCueClusterPos, uint64(newOff-outSeg))...)
			}
			tc = append(tc, tp.post...)
			pc = append(pc, masterElement(idCueTrackPos, tc, tp.hasCRC)...)
			tracks++
		}
		if tracks == 0 {
			continue // a CuePoint with no surviving CueTrackPositions disappears
		}
		content = append(content, masterElement(idCuePoint, pc, p.hasCRC)...)
	}
	if len(content) == 0 {
		return nil, 0, false // an empty Cues is invalid; refuse rather than write one
	}
	out = masterElement(idCues, content, crc != nil)
	return out, int64(len(out)), true
}

func copyItem(c l1elem, kind itemKind) outItem {
	return outItem{id: c.id, srcOff: c.start, n: c.total(), origStart: c.start, kind: kind}
}

func insertItems(items []outItem, at int, extra []outItem) []outItem {
	out := make([]outItem, 0, len(items)+len(extra))
	out = append(out, items[:at]...)
	out = append(out, extra...)
	out = append(out, items[at:]...)
	return out
}

// segmentLead returns the bytes preceding the Segment data in the output (the
// EBML header, Segment ID, and the recomputed size VINT) and the resulting data
// start. An unknown-size Segment keeps its form, so its size is never rewritten.
func segmentLead(wb *writeBase, bodyLen int64) ([]bits.Segment, int64) {
	if wb.segUnknown {
		return []bits.Segment{bits.Copy(0, wb.segDataStart)}, wb.segDataStart
	}
	sz, ok := sizeVINTWidthOK(uint64(bodyLen), int(wb.segSizeLen))
	if !ok {
		sz = sizeVINT(uint64(bodyLen))
	}
	lead := []bits.Segment{bits.Copy(0, wb.segSizeOff), bits.Lit(sz)}
	return lead, wb.segSizeOff + int64(len(sz))
}

// patchPositions rewrites each position whose target moved (per oldToNew) in
// place at its original width and recomputes the CRC. ok is false if a value
// overflows its slot or its target was dropped (not in oldToNew).
func patchPositions(raw []byte, crc *crcSpot, entries []seekEntry, oldToNew map[int64]int64, inSegStart, outSegStart int64) ([]byte, bool) {
	out := make([]byte, len(raw))
	copy(out, raw)
	for _, e := range entries {
		newOff, ok := oldToNew[inSegStart+int64(e.target)]
		if !ok {
			return nil, false
		}
		val := uintDataWidth(uint64(newOff-outSegStart), e.valLen)
		if val == nil {
			return nil, false
		}
		copy(out[e.valOff:e.valOff+e.valLen], val)
	}
	recomputeCRC(out, crc)
	return out, true
}

// assembleShift builds the shift segment list (the recomputed Segment lead, each
// child literal-or-copy, then any post-Segment trailing) and the result layout.
func assembleShift(wb *writeBase, items []outItem, segLead []bits.Segment, outSegStart, bodyLen int64) layout {
	trailing := wb.size - wb.segDataEnd
	lay := layout{
		segs:     append([]bits.Segment{}, segLead...),
		size:     outSegStart + bodyLen + trailing,
		children: make([]l1elem, 0, len(items)),
	}
	lay.delta = lay.size - wb.size
	for _, it := range items {
		if it.lit != nil {
			lay.segs = append(lay.segs, bits.Lit(it.lit))
		} else {
			lay.segs = append(lay.segs, bits.Copy(it.srcOff, it.n))
		}
		lay.children = append(lay.children, l1elem{id: it.id, start: it.outOff, dataEnd: it.outOff + it.n})
		switch it.kind {
		case itemSeek:
			lay.seekRaw, lay.seekStart = it.lit, it.outOff
		case itemInfo:
			lay.infoRaw, lay.infoStart = it.lit, it.outOff
		case itemAttach:
			lay.hasAttach = true
			lay.attach = attachBlock{start: it.outOff, end: it.outOff + it.n, hasCRC: wb.attach != nil && wb.attach.hasCRC}
		case itemChapters:
			lay.chaptersRaw = it.lit
		}
		if it.id == idCluster && lay.clusterStart == 0 {
			lay.clusterStart = it.outOff
		}
		if it.id == idCues && wb.cues != nil {
			lay.cuesRaw, lay.cuesStart = it.lit, it.outOff
		}
	}
	if trailing > 0 {
		lay.segs = append(lay.segs, bits.Copy(wb.segDataEnd, trailing))
	}
	if lay.clusterStart == 0 {
		lay.clusterStart = lay.size
	}
	if wb.seek != nil && lay.seekRaw == nil {
		lay.seekRaw, lay.seekStart = wb.seek.raw, childStart(lay.children, idSeekHead)
	}
	if wb.info != nil && lay.infoRaw == nil {
		lay.infoRaw, lay.infoStart = wb.info.raw, childStart(lay.children, idInfo)
	}
	return lay
}

// overflowErr is returned when an index cannot be rebuilt or the bounded fixpoint
// does not settle. Ordinary width growth is handled by minimal-width re-encoding;
// this path is for malformed or unsupported index layouts, such as a SeekHead
// missing a SeekID or an unmodeled Cues tree.
func overflowErr() error {
	return fmt.Errorf("%w: a Matroska SeekHead/Cues index could not be re-encoded after the edit",
		waxerr.ErrUnsupportedTag)
}

func shiftOps(ch changes, pics int, delta int64) []string {
	var ops []string
	if ch.simple {
		ops = append(ops, "Tags rewrite (clusters shifted)")
	}
	if ch.title {
		ops = append(ops, "Info.Title rewrite")
	}
	if ch.pictures {
		ops = append(ops, "Attachments rewrite", "pictures: "+strconv.Itoa(pics))
	}
	if ch.chapters {
		ops = append(ops, "Chapters rewrite")
	}
	ops = append(ops, fmt.Sprintf("%d-byte tail shift", delta))
	return ops
}
