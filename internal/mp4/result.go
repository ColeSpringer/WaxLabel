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
	if base.chpl != nil {
		c := *base.chpl
		if c.offset >= regionEnd {
			c.offset += delta
		}
		nd.chpl = &c
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
// chunk-offset table entry past the insertion point plus any offset-table atom,
// mdat range, and top-level atom that lies at or past regionEnd. Both the
// ilst-only and the chapter (udta) rewrite paths share it, so the two result
// builders cannot drift in how they keep the media playable. (insertion is the
// region start — chunk offsets at or before it never move; regionEnd is where
// trailing atoms begin to shift.)
func shiftStructure(nd, base *doc, insertion, regionEnd, delta int64) {
	moov := *base.moov
	moov.size += delta
	nd.moov = &moov

	for _, t := range base.offTables {
		nt := t
		if t.offset >= regionEnd {
			nt.offset = t.offset + delta
		}
		nt.entries = slices.Clone(t.entries)
		for i, e := range nt.entries {
			nt.entries[i] = shiftOffset(e, insertion, delta)
		}
		nd.offTables = append(nd.offTables, nt)
	}

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
		return out
	}
	// A udta was created: the region bytes are the new udta atom, payload after the
	// 8-byte header.
	if lay.created && base.udta == nil && base.meta == nil && len(lay.regionBytes) >= 8 {
		return slices.Clone(lay.regionBytes[8:])
	}
	return nil
}
