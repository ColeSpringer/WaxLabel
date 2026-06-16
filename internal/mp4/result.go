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
	insertion := lay.regionStart

	// shiftAfter moves an atom start that lies entirely past the rewritten region.
	shiftAfter := func(off int64) int64 {
		if off >= regionEnd {
			return off + delta
		}
		return off
	}
	grow := func(r *atomRef) *atomRef {
		c := *r
		c.size += delta
		return &c
	}

	nd := &doc{
		size:     total,
		cfg:      base.cfg,
		track:    base.track,
		chapters: base.chapters,
		items:    newItems,
	}

	// New ilst and free locations within the rewritten file.
	nd.ilst = &atomRef{name: atomName("ilst"), offset: lay.regionStart + lay.ilstOff, headerLen: 8, size: newIlstLen}
	if lay.freeLen > 0 {
		nd.free = &atomRef{name: atomName("free"), offset: lay.regionStart + lay.freeOff, headerLen: 8, size: lay.freeLen}
	}

	// Enclosing atoms: moov always grows; udta/meta either grow (preexisting) or
	// are newly created inside the inserted region.
	nd.moov = grow(base.moov)
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

	for _, t := range base.offTables {
		nt := t
		nt.offset = shiftAfter(t.offset)
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

	tags, pics, families, numericGenre := project(nd)
	out := &core.Media{
		Format:     core.FormatMP4,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
		Families:   families,
		Warnings:   mediaWarnings(tags, numericGenre),
		Native:     nd,
		Identity:   core.Identity{Size: total},
	}
	setEssence(nd, out)
	return out
}
