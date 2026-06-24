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
// place and lets the tail shift by the size delta, then repoints the
// segment-relative positions that moved - CueClusterPosition and the SeekHead
// SeekPositions of elements after the edit - recomputing the affected CRC-32s and
// the Segment size. Because the clusters move, the result records their new range.
//
// Both indexes are patched in place at their original width when every moved
// position fits (no element size changes, so no cascade); when a position would
// overflow its width or its target was dropped, that index - SeekHead or Cues - is
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

// resolveShiftLayout runs the bounded layout fixpoint that places the shifted
// children and repoints the moved positions. Each iteration recomputes the
// old->new offset map from the current item sizes, re-encodes BOTH the SeekHead and
// the Cues from it, and always installs the fresh bytes - then loops if either index
// changed size, because a size change moves the elements after it (and thus the
// offsets both indexes record).
//
// Re-encoding (a full rebuild) latches per index: once a position overflows its
// in-place slot, or an entry to a dropped element must go, that index re-encodes at
// minimal width every iteration thereafter. Minimal-width re-encoding is a monotone
// fixpoint - flipping back to in-place patching could oscillate around a width
// boundary. The bytes are installed every iteration even when the size is unchanged
// (see installIndex): a same-length re-encode can still hold different position
// values after a prior growth shifted a cluster, so the size comparison decides only
// whether to loop again, never whether to install.
//
// iters is the count it settled in - asserted small by a test, since this fix fails
// by refusing a legal edit (hitting the bound), not by crashing. ok is false when an
// index cannot be re-encoded (an uncaptured Cues tree, a SeekHead missing a SeekID,
// or a degenerate empty result), which the caller surfaces as overflowErr.
func resolveShiftLayout(wb *writeBase, items []outItem, seekIdx, cuesIdx int) (lay layout, iters int, ok bool) {
	forceSeek, forceCues := false, false
	for iters = 1; iters <= 8; iters++ {
		segLead, outSegStart, oldToNew, bodyLen := computeShiftLayout(wb, items)

		seekBytes, seekLen, seekRebuilt, sok := makeSeek(wb, seekIdx, oldToNew, outSegStart, forceSeek)
		if !sok {
			return layout{}, iters, false
		}
		forceSeek = forceSeek || seekRebuilt

		cuesBytes, cuesLen, cuesRebuilt, cok := makeCues(wb, cuesIdx, oldToNew, outSegStart, forceCues)
		if !cok {
			return layout{}, iters, false
		}
		forceCues = forceCues || cuesRebuilt

		// Install both indices every iteration - computing both resize flags before the
		// test, so neither install is short-circuited away - then loop if either changed
		// size.
		resizedSeek := installIndex(items, seekIdx, seekBytes, seekLen)
		resizedCues := installIndex(items, cuesIdx, cuesBytes, cuesLen)
		if resizedSeek || resizedCues {
			continue // an index size changed: re-lay-out and recompute every position
		}
		return assembleShift(wb, items, segLead, outSegStart, bodyLen), iters, true
	}
	return layout{}, iters, false
}

// installIndex writes a re-encoded index's bytes into its output item and reports
// whether its size changed (which forces another layout pass). It always installs
// the bytes, even on an unchanged size, because a same-length re-encode can still
// carry different position values after a prior cluster shift - installing only on a
// size change would leave a silently corrupt index that still parses.
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

// makeCues produces the output Cues bytes: patched in place at original width when
// every moved position fits (and forceCues is not set), else re-encoded at minimal
// width via rebuildCues (which also drops entries to removed clusters). It reports
// whether it re-encoded (so the caller latches into rebuild mode) and ok=false only
// when a rebuild is needed but the Cues tree was not captured faithfully.
func makeCues(wb *writeBase, cuesIdx int, oldToNew map[int64]int64, outSegStart int64, forceCues bool) (out []byte, length int64, rebuilt, ok bool) {
	if cuesIdx < 0 {
		return nil, 0, false, true
	}
	ci := wb.cues
	if !forceCues {
		if patched, ok := patchPositions(ci.raw, ci.crc, ci.clusters, oldToNew, wb.segDataStart, outSegStart); ok {
			return patched, int64(len(patched)), false, true
		}
	}
	out, length, ok = rebuildCues(ci, oldToNew, wb.segDataStart, outSegStart)
	return out, length, true, ok
}

// rebuildCues re-encodes the Cues element from its captured tree with every
// CueClusterPosition at minimal width (overflow impossible - a cluster offset fits
// an int64, <= 8 bytes), the nested analogue of rebuildSeekHead. Each non-position
// child (CueTime, CueTrack, CueRelativePosition, ...) is emitted verbatim from its
// captured prefix/pre/post so unmodeled fields survive byte-for-byte, and each
// master is rebuilt bottom-up by masterElement so sizes and CRCs come from the
// actual payloads rather than being hand-maintained. A CueTrackPositions whose
// target cluster was dropped (not in oldToNew) is omitted; a CuePoint left with no
// positions disappears. ok is false when the tree was not captured faithfully
// (capturedOK) or the result would be an empty - invalid - Cues.
func rebuildCues(ci *cuesIndex, oldToNew map[int64]int64, inSegStart, outSegStart int64) (out []byte, length int64, ok bool) {
	if !ci.capturedOK {
		return nil, 0, false
	}
	// The rebuilt element is close in size to the original (only positions widen), so
	// size the accumulator from the captured bytes to avoid repeated growth - the
	// chapters.go render precedent.
	content := make([]byte, 0, len(ci.raw))
	for _, p := range ci.points {
		// Clone prefix/pre so the appends below never write into the captured tree,
		// which is reused across iterations of the fixpoint and across re-edits.
		pc := slices.Clone(p.prefix)
		tracks := 0
		for _, tp := range p.tracks {
			tc := slices.Clone(tp.pre)
			if tp.hasPos {
				newOff, present := oldToNew[inSegStart+int64(tp.target)]
				if !present {
					continue // target cluster dropped: omit this CueTrackPositions
				}
				if newOff < outSegStart {
					return nil, 0, false // (unreachable) a target before the segment data start would underflow
				}
				tc = append(tc, uintElement(idCueClusterPos, uint64(newOff-outSegStart))...)
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
		return nil, 0, false // an empty Cues is invalid - refuse rather than write one
	}
	out = masterElement(idCues, content, ci.crc != nil)
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

// overflowErr is the residual refusal: with the SeekHead and Cues rebuild paths in
// place, ordinary width growth is handled by re-encoding at minimal width, so this is
// reached only for a genuinely unrebuildable index - one whose tree could not be
// captured faithfully (a SeekHead missing a SeekID, or an unmodeled Cues layout) - or
// the bounded fixpoint failing to settle. It fails safely (nothing is written).
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
