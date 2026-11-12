package mp4

import (
	"math"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// stores is what a result projects its tags and pictures from: the items of the store written,
// those of a second store, and the udta text atoms.
type stores struct {
	items, second []item
	texts         []udtaText
}

func (st stores) doc() *doc {
	nd := &doc{items: st.items, udtaTexts: st.texts}
	if st.second != nil {
		nd.second = &metaStore{items: st.second}
	}
	return nd
}

// buildResult constructs the plan's result for an in-place ilst rewrite. Nothing outside the ilst
// region changes on that path (the keys index gained nothing, and no udta text atom or second
// store needed syncing), so those project as parsed.
func buildResult(edited *core.Media, base *doc, newItems []item, lay layout) *core.Media {
	region := edit{off: lay.regionStart, oldLen: lay.regionEnd - lay.regionStart, lit: lay.regionBytes}
	return tagResult(edited, base, stores{items: newItems, second: base.secondItems(), texts: base.udtaTexts},
		newShifts([]edit{region}), core.CloneChapters(base.chapters))
}

// tagResult constructs the result of a write whose edits leave the chapter track alone: what the
// stores project, the chapters given, and the essence where the edits move it.
func tagResult(edited *core.Media, base *doc, st stores, moved shifts, chapters []core.Chapter) *core.Media {
	nd := st.doc()
	shiftEssence(nd, base, moved)
	tags, pics, _, _ := project(nd)
	out := &core.Media{
		Format:     core.FormatMP4,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
		Chapters:   chapters,
	}
	setEssence(nd, out)
	core.SyncTextItems(out)
	return out
}

// shiftEssence sets what setEssence reads, as a write's edits leave it: every chunk offset past
// an edit's start, and every table atom, mdat, chapter-only mdat and chapter track at or past an
// edit's end, moves by that edit's length change.
func shiftEssence(nd, base *doc, moved shifts) {
	shiftSoundChunks(nd, base, moved)
	nd.offTables = make([]offsetTable, 0, len(base.offTables))
	for _, t := range base.offTables {
		nt := t
		nt.offset = t.offset + moved.past(t.offset)
		nt.entries = slices.Clone(t.entries)
		for i, e := range nt.entries {
			// A table whose offset would underflow is refused in offsetPatch before any result
			// is built, so the shifted value is never negative here.
			if e <= math.MaxInt64 {
				nt.entries[i] = uint64(int64(e) + moved.at(int64(e)))
			}
		}
		nd.offTables = append(nd.offTables, nt)
	}
	nd.mdats = make([][2]int64, len(base.mdats))
	for i, m := range base.mdats {
		off := m[0] + moved.past(m[0])
		nd.mdats[i] = [2]int64{off, m[1]}
		if base.chapterMdat(m[0]) {
			nd.chapMdats = append(nd.chapMdats, off)
		}
	}
	if base.chapTrak != nil {
		t := *base.chapTrak
		t.offset += moved.past(t.offset)
		nd.chapTrak = &t
	}
}

// shiftSoundChunks moves the sound tracks' chunk ranges as their chunk offsets move. No edit
// falls inside one, so each moves whole.
func shiftSoundChunks(nd, base *doc, moved shifts) {
	nd.sampleExtent = base.sampleExtent
	nd.soundChunks = make([][2]int64, len(base.soundChunks))
	for i, r := range base.soundChunks {
		by := moved.at(r[0])
		nd.soundChunks[i] = [2]int64{r[0] + by, r[1] + by}
	}
}
