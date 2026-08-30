package mp4

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// buildResult constructs the post-write Media so the engine can return a
// Document without re-parsing. Its canonical view is re-projected (via the same
// project Parse uses) from the items actually written, so it equals a fresh parse
// of the output; the structural offsets (ilst/free, the enclosing atoms, every
// chunk-offset table, the mdat ranges, and the top-level layout) are shifted by
// delta so re-editing the returned document without re-parsing stays correct.
func buildResult(edited *core.Media, base *doc, newItems []item, lay layout, delta, total, newIlstLen int64) *core.Media {
	regionEnd := lay.regionEnd
	grow := func(r *atomRef) *atomRef {
		c := *r
		c.size += delta
		return &c
	}

	nd := &doc{
		size:            total,
		cfg:             base.cfg,
		track:           base.track,
		majorBrand:      base.majorBrand,
		chapters:        core.CloneChapters(base.chapters),
		chplVersion:     base.chplVersion,
		chplCount:       base.chplCount,
		hasQTChapters:   base.hasQTChapters,
		chapterConflict: base.chapterConflict,
		items:           newItems,
		// This path runs only when the store outside the ilst region is untouched (the keys
		// index gained nothing and no udta text atom needed syncing), so the decoded store
		// carries forward as-is; only the atom offsets past the region shift.
		metaHandler: base.metaHandler,
		keyNames:    slices.Clone(base.keyNames),
	}
	shiftStructure(nd, base, lay.regionStart, regionEnd, delta)

	// New ilst and free locations within the rewritten file.
	nd.ilst = &atomRef{name: atomName("ilst"), offset: lay.regionStart + lay.ilstOff, headerLen: 8, size: newIlstLen}
	if lay.freeLen > 0 {
		nd.free = &atomRef{name: atomName("free"), offset: lay.regionStart + lay.freeOff, headerLen: 8, size: lay.freeLen}
	}

	// Enclosing atoms: udta/meta either grow (preexisting) or are newly created
	// inside the inserted region (moov is grown by shiftStructure).
	switch {
	case base.udta != nil:
		nd.udta = grow(base.udta)
	default: // udta created: it is the inserted region itself
		nd.udta = &atomRef{name: atomName("udta"), offset: lay.regionStart, headerLen: 8, size: int64(len(lay.regionBytes))}
	}
	switch {
	case base.meta != nil:
		nd.meta = grow(base.meta)
	case base.udta != nil: // meta created inside existing udta: it is the region
		nd.meta = &atomRef{name: atomName("meta"), offset: lay.regionStart, headerLen: 8, size: int64(len(lay.regionBytes))}
	default: // meta created inside the new udta (8-byte udta header precedes it)
		nd.meta = &atomRef{name: atomName("meta"), offset: lay.regionStart + 8, headerLen: 8, size: int64(len(lay.regionBytes)) - 8}
	}

	// A Nero chpl is a udta sibling of meta: it does not change here, but it shifts
	// when it sits after the rewritten ilst region. Carry the captured udta bytes
	// forward (with the ilst region change applied) so a later chapter edit on this
	// result can splice into them without a reparse.
	shift := func(r atomRef) atomRef {
		if r.offset >= regionEnd {
			r.offset += delta
		}
		return r
	}
	if base.chpl != nil {
		c := shift(*base.chpl)
		nd.chpl = &c
	}
	if base.keys != nil {
		k := shift(*base.keys)
		nd.keys = &k
	}
	for _, r := range base.udtaKids {
		nd.udtaKids = append(nd.udtaKids, shift(r))
	}
	for _, u := range base.udtaTexts {
		u.ref = shift(u.ref)
		nd.udtaTexts = append(nd.udtaTexts, u)
	}
	nd.udtaRaw = resultUdtaRaw(base, lay)
	carryChapterRefs(nd, base, regionEnd, delta)

	tags, pics, families, numericGenre := project(nd)
	out := &core.Media{
		Format:     core.FormatMP4,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
		Chapters:   nd.chapters,
		Families:   families,
		Warnings:   chapterWarnings(mediaWarnings(tags, numericGenre), base.chapterConflict),
		Native:     nd,
		Identity:   core.Identity{Size: total},
	}
	setEssence(nd, out)
	return out
}

// shiftStructure relocates the parts of a rewritten document that move when a
// metadata region changes size: it grows the moov box and shifts, by delta, every
// chunk-offset and sample-auxiliary table entry past the insertion point plus any
// offset-table atom, mdat range, and top-level atom that lies at or past regionEnd. Both the
// ilst-only and the chapter (udta) rewrite paths share it, so the two result
// builders cannot drift in how they keep the media playable. (insertion is the
// region start - chunk offsets at or before it never move; regionEnd is where
// trailing atoms begin to shift.)
func shiftStructure(nd, base *doc, insertion, regionEnd, delta int64) {
	moov := *base.moov
	moov.size += delta
	nd.moov = &moov

	nd.offTables = shiftTables(base.offTables, insertion, regionEnd, delta)
	nd.auxTables = shiftTables(base.auxTables, insertion, regionEnd, delta)

	nd.mdats = make([][2]int64, len(base.mdats))
	for i, m := range base.mdats {
		off := m[0]
		if off >= regionEnd {
			off += delta
		}
		nd.mdats[i] = [2]int64{off, m[1]}
	}

	for _, a := range base.topLevel {
		switch {
		case a.offset == base.moov.offset:
			a.size += delta // the moov box grows
		case a.offset >= regionEnd:
			a.offset += delta // atoms after the region shift
		}
		nd.topLevel = append(nd.topLevel, a)
	}
}

// shiftTables relocates one group of offset tables into a result document: the atom's own
// offset moves when it lies at or past regionEnd, and every entry past the insertion point
// moves by delta. The chunk-offset and sample-auxiliary groups relocate identically, so
// both go through this one function and cannot drift apart.
func shiftTables(src []offsetTable, insertion, regionEnd, delta int64) []offsetTable {
	out := make([]offsetTable, 0, len(src))
	for _, t := range src {
		nt := t
		if t.offset >= regionEnd {
			nt.offset = t.offset + delta
		}
		nt.entries = slices.Clone(t.entries)
		for i, e := range nt.entries {
			// The ok result is unreachable here: Plan refuses an underflowing table in
			// offsetPatch before any result document is built. shiftOffset returns the offset
			// unchanged in that case, so discarding ok keeps the inert value.
			nt.entries[i], _ = shiftOffset(e, insertion, delta)
		}
		out = append(out, nt)
	}
	return out
}

// resultUdtaRaw reconstructs the post-write udta payload after a tag/picture edit
// by applying the same ilst-region change to the captured udta bytes, so a chapter
// edit on the returned document can splice into them. It returns nil only when the
// file had no udta and none was created (no chapter rewrite is then possible).
func resultUdtaRaw(base *doc, lay layout) []byte {
	if base.udta != nil && base.udtaRaw != nil {
		ups := base.udta.offset + base.udta.headerLen
		relStart := lay.regionStart - ups
		relEnd := lay.regionEnd - ups
		if relStart < 0 || relEnd < relStart || relEnd > int64(len(base.udtaRaw)) {
			return base.udtaRaw // region not within udta (unexpected): keep the old bytes
		}
		out := make([]byte, 0, int64(len(base.udtaRaw))-(relEnd-relStart)+int64(len(lay.regionBytes)))
		out = append(out, base.udtaRaw[:relStart]...)
		out = append(out, lay.regionBytes...)
		out = append(out, base.udtaRaw[relEnd:]...)

		// The splice resized the ilst region inside the enclosing meta box, so meta's
		// own size field in these bytes is stale. Patch it by the same delta as the
		// on-disk write; both paths locate the field through atomRef.sizeField. Without
		// this, a later chapter edit on the returned document would carry the wrong meta
		// size. Bounds checks keep malformed layouts from indexing out of range.
		if delta := int64(len(lay.regionBytes)) - (relEnd - relStart); base.meta != nil && delta != 0 {
			off, width := base.meta.sizeField()
			if fieldStart := base.meta.offset - ups + off; fieldStart >= 0 && fieldStart+width <= int64(len(out)) {
				putBoxSize(out[fieldStart:fieldStart+width], width, base.meta.size+delta)
			}
		}
		return out
	}
	// A udta was created: the region bytes are the new udta atom, payload after the
	// 8-byte header.
	if lay.created && base.udta == nil && base.meta == nil && len(lay.regionBytes) >= 8 {
		return slices.Clone(lay.regionBytes[8:])
	}
	return nil
}
