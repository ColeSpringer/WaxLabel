package matroska

import (
	"fmt"
	"strconv"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// planShift is the fallback when absorption does not apply (no reserved Void, or
// the edited header does not fit one). It re-renders the changed children in
// place and lets the tail shift by the size delta, then repoints the
// segment-relative positions that moved - CueClusterPosition and the SeekHead
// SeekPositions of elements after the edit - recomputing the affected CRC-32s and
// the Segment size. Because the clusters move, the result records their new range.
//
// SeekHead positions are patched in place at their original width when they fit
// (no element size changes, so no cascade); when a position would overflow its
// width or its target was dropped, the SeekHead is re-encoded at minimal width,
// and its resulting size change is resolved by a short bounded layout fixpoint
// (the plan's "fall back to a full re-encode"). A CueClusterPosition that would
// overflow - astronomically rare, since cluster offsets are already wide - is
// refused cleanly rather than corrupting the index.
func planShift(d *doc, base, edited *core.Media, ch changes, ek map[tag.Key]bool, report core.WriteReport) (*core.WritePlan, error) {
	wb := d.wb
	r, err := renderChanged(d, base, edited, ch, ek)
	if err != nil {
		return nil, err
	}

	items, seekIdx, cuesIdx := buildShiftItems(wb, ch, r)

	// Resolve the layout, converging the SeekHead size if a re-encode changes it.
	// Once a re-encode is needed (a position overflowed its width, or an entry to a
	// dropped element must go), latch into rebuild mode: re-encoding always yields
	// valid minimal-width positions, so staying in it gives a monotone fixpoint -
	// flipping back to in-place patching could oscillate around a width boundary.
	var lay layout
	converged, forceRebuild := false, false
	for iter := 0; iter < 8; iter++ {
		segLead, outSegStart, oldToNew, bodyLen := computeShiftLayout(wb, items)

		seekBytes, seekLen, rebuilt, ok := makeSeek(wb, seekIdx, oldToNew, outSegStart, forceRebuild)
		if !ok {
			return nil, overflowErr()
		}
		if rebuilt {
			forceRebuild = true
		}
		if seekIdx >= 0 && seekLen != items[seekIdx].n {
			items[seekIdx].n, items[seekIdx].lit = seekLen, seekBytes
			continue // size changed: re-lay-out and recompute positions
		}
		if seekIdx >= 0 {
			items[seekIdx].lit, items[seekIdx].srcOff = seekBytes, 0
		}
		if cuesIdx >= 0 {
			cb, ok := patchPositions(wb.cues.raw, wb.cues.crc, wb.cues.clusters, oldToNew, wb.segDataStart, outSegStart)
			if !ok {
				return nil, overflowErr()
			}
			items[cuesIdx].lit, items[cuesIdx].srcOff = cb, 0
		}
		lay = assembleShift(wb, items, segLead, outSegStart, bodyLen)
		converged = true
		break
	}
	if !converged {
		return nil, overflowErr()
	}

	report.BytesAfter = lay.size
	report.Operations = shiftOps(ch, len(edited.Pictures), lay.delta)
	result := buildResult(d, edited, r, ch, lay)
	return &core.WritePlan{Segments: lay.segs, NoOp: false, Report: report, Result: result}, nil
}

// buildShiftItems produces the output item list for every top-level child, with
// the changed children substituted and any newly created Tags/Attachments/Chapters
// placed just before the cluster tail. It returns the SeekHead and Cues item indices.
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
	// output but intentionally not added to an existing SeekHead. The index is
	// preserved/patched at a stable size, never regenerated to gain entries, so
	// adding one would grow the SeekHead and perturb the size-preserving layout.
	// SeekHead is an optional index per RFC 9559 and readers locate level-1
	// elements by scanning, so an unindexed new Tags/Attachments/Chapters is still
	// found - the same deliberate limitation for all three created element kinds.
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

// makeSeek produces the output SeekHead bytes: patched in place at original width
// when every moved position fits (and forceRebuild is not set), else re-encoded at
// minimal width (which also drops entries to elements that were removed). It
// reports whether it re-encoded (so the caller can latch into rebuild mode) and
// ok=false only when a re-encode is needed but a SeekID was not captured.
func makeSeek(wb *writeBase, seekIdx int, oldToNew map[int64]int64, outSegStart int64, forceRebuild bool) (out []byte, length int64, rebuilt, ok bool) {
	if seekIdx < 0 {
		return nil, 0, false, true
	}
	sh := wb.seek
	if !forceRebuild {
		if patched, ok := patchPositions(sh.raw, sh.crc, sh.entries, oldToNew, wb.segDataStart, outSegStart); ok {
			return patched, int64(len(patched)), false, true
		}
	}
	out, length, ok = rebuildSeekHead(sh, oldToNew, wb.segDataStart, outSegStart)
	return out, length, true, ok
}

// rebuildSeekHead re-encodes the SeekHead from its entries with each SeekPosition
// at minimal width and entries to dropped targets omitted. It needs each entry's
// captured SeekID; ok is false if one is missing.
func rebuildSeekHead(sh *seekHead, oldToNew map[int64]int64, inSegStart, outSegStart int64) ([]byte, int64, bool) {
	var content []byte
	for _, e := range sh.entries {
		newOff, ok := oldToNew[inSegStart+int64(e.target)]
		if !ok {
			continue // target dropped - omit the stale entry
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

func overflowErr() error {
	return fmt.Errorf("%w: a Cues/SeekHead position would not fit after the edit (this rare layout is not yet writable)",
		waxerr.ErrUnsupportedTag)
}

func shiftOps(ch changes, pics int, delta int64) []string {
	var ops []string
	if ch.simple {
		ops = append(ops, "rewrote Tags (clusters shifted)")
	}
	if ch.title {
		ops = append(ops, "rewrote Info.Title")
	}
	if ch.pictures {
		ops = append(ops, "rewrote Attachments", "pictures: "+strconv.Itoa(pics))
	}
	if ch.chapters {
		ops = append(ops, "rewrote Chapters")
	}
	ops = append(ops, fmt.Sprintf("shifted tail by %d bytes", delta))
	return ops
}
