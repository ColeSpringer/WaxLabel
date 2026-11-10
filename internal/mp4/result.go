package mp4

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// buildResult constructs the plan's result for an in-place ilst rewrite: the tags and pictures
// the new items project, and the chapters and essence of a store that does not otherwise change.
func buildResult(edited *core.Media, base *doc, newItems []item, lay layout, delta int64) *core.Media {
	// This path runs only when the store outside the ilst region is untouched (the keys
	// index gained nothing and no udta text atom needed syncing), so the udta text atoms
	// project as parsed.
	nd := &doc{items: newItems, udtaTexts: base.udtaTexts}
	shiftEssence(nd, base, lay.regionStart, lay.regionEnd, delta)
	tags, pics, _, _ := project(nd)
	out := &core.Media{
		Format:     core.FormatMP4,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
		Chapters:   core.CloneChapters(base.chapters),
	}
	setEssence(nd, out)
	core.SyncTextItems(out)
	return out
}

// shiftEssence sets what setEssence reads, as a metadata region that changed size by delta
// leaves it: every chunk-offset table entry past the insertion point and every table atom,
// mdat range and chapter track at or past regionEnd move by delta.
func shiftEssence(nd, base *doc, insertion, regionEnd, delta int64) {
	nd.offTables = shiftTables(base.offTables, insertion, regionEnd, delta)
	nd.mdats = make([][2]int64, len(base.mdats))
	for i, m := range base.mdats {
		off := m[0]
		if off >= regionEnd {
			off += delta
		}
		nd.mdats[i] = [2]int64{off, m[1]}
	}
	if base.chapTrak != nil {
		t := *base.chapTrak
		if t.offset >= regionEnd {
			t.offset += delta
		}
		nd.chapTrak = &t
	}
}

// shiftTables relocates one group of offset tables into the plan's result: the atom's
// own offset moves when it lies at or past regionEnd, and every entry past the
// insertion point moves by delta.
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
			// offsetPatch before any result is built. shiftOffset returns the offset
			// unchanged in that case, so discarding ok keeps the inert value.
			nt.entries[i], _ = shiftOffset(e, insertion, delta)
		}
		out = append(out, nt)
	}
	return out
}
