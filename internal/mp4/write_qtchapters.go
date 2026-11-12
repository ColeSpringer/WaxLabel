package mp4

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// QuickTime chapter write. The text track is built fresh (qtchapters.go), its samples
// go in an mdat behind the file's last whole atom (so audio never moves), and the sound
// and video tracks gain a tref "chap" pointing at it.

// planChaptersQT computes the rewrite when chapters change and the file has a chapter
// text track or a track to reference a new one from. It writes the chpl and rebuilds
// the QuickTime chapter track (or removes both when chapters are cleared).
func planChaptersQT(d *doc, base, edited *core.Media, needIlst, picturesChanged bool, opts core.WriteOptions, report core.WriteReport) (*core.WritePlan, error) {
	if d.udta != nil && d.udtaRaw == nil {
		return nil, fmt.Errorf("%w: MP4 udta bytes were not captured for a chapter rewrite", waxerr.ErrInvalidData)
	}

	newItems, reg, textDups, err := buildChapterUdta(d, edited, needIlst, picturesChanged, opts)
	if err != nil {
		return nil, err
	}

	clearing := len(edited.Chapters) == 0
	// The QuickTime chapter track is written at a fixed fine media timescale, decoupled
	// from the (often coarse) movie timescale, so authored millisecond starts are exact.
	mts := uint32(chapterMediaTimescale)

	// Build the moov-internal edits (with a 32-bit stco placeholder first), then,
	// once the combined delta and thus the appended mdat's offset are known, retry
	// with a 64-bit table only if that offset overflows 4 GiB.
	plan, err := assembleQT(d, edited, reg, clearing, mts, false)
	if err != nil {
		return nil, err
	}
	if !clearing && plan.mdatPayloadOff > math.MaxUint32 {
		if plan, err = assembleQT(d, edited, reg, clearing, mts, true); err != nil {
			return nil, err
		}
	}

	total := d.size + plan.fileDelta + int64(len(plan.newMdat))
	if err := checkSizes([]atomRef{*d.moov}, plan.moovDelta); err != nil {
		return nil, err
	}

	edits := plan.edits
	if plan.newMdat != nil {
		// Last, so it follows whatever else is inserted at that offset.
		edits = append(slices.Clip(edits), edit{off: d.appendAt, lit: plan.newMdat})
	}
	segs, err := assemble(edits, d.size)
	if err != nil {
		return nil, err
	}

	report.BytesAfter = total
	report.PaddingAfter = reg.freeContent
	report.Operations = qtChapterOps(d, edited, needIlst, clearing, plan.moovDelta)
	if n := truncatedTitleCount(edited.Chapters); n > 0 {
		report.Warnings = core.Warn(report.Warnings, core.WarnChapterTitleTruncated,
			fmt.Sprintf("%d chapter title(s) trimmed to %d bytes (the chapter-title length limit)", n, titleByteMax))
	}
	if plan.sttsSaturated {
		report.Warnings = core.Warn(report.Warnings, core.WarnChapterStartOverflow,
			"a chapter start or the total chapter span exceeds the QuickTime track's 32-bit duration field (~13.25 h at the 90 kHz media timescale) and was clamped; the Nero chpl keeps the exact values")
	}

	plan.resultItems = d.items
	if needIlst {
		plan.resultItems = newItems
	}
	result := buildQTChapterResult(edited, d, plan, reg)
	report.Warnings = droppedDupWarnings(report.Warnings, d, base, result, textDups, true)
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// qtPlan is the resolved QuickTime-chapter rewrite: the edit list and the derived
// facts the segment writer and the result builder need.
type qtPlan struct {
	edits []edit
	// moovDelta is the moov-INTERNAL length change (the chapter trak, tref, mvhd
	// next-track-id, and udta edits).
	moovDelta int64
	// fileDelta is the whole-file length change before the new chapter mdat is added:
	// moovDelta plus the negative delta of a reclaimed trailing chapter mdat. It drives
	// the file size and the new mdat's offset.
	fileDelta int64
	newMdat   []byte // the appended chapter-sample mdat (nil when clearing)

	// reclaimed is the prior trailing chapter-sample mdat this rewrite deletes, or nil, and
	// freed the payload offsets, ascending, of the chapter-sample mdats elsewhere that it
	// marks free. buildQTChapterResult skips both so the result equals a parse of the output.
	reclaimed *atomRef
	freed     []int64

	resultItems []item

	mdatPayloadOff int64 // absolute offset the new chapter track's stco points at

	// For the result's chapter view.
	mts          uint32
	chplChapters []core.Chapter // chplRoundTrip(edited.Chapters)

	// sttsSaturated is set when any of the chapter track's clamped duration fields
	// overflowed its 32-bit slot: a per-gap stts delta, the first-chapter start, or the
	// cumulative mdhd/tkhd/elst span (the fixed 90 kHz media field binds first, at
	// ~13.25 h).
	sttsSaturated bool
}

// assembleQT builds the full edit list for one stco width. It is called at most
// twice (32- then 64-bit), so the offset backpatch needs no fixpoint.
func assembleQT(d *doc, edited *core.Media, reg udtaRegion, clearing bool, mts uint32, co64 bool) (*qtPlan, error) {
	p := &qtPlan{mts: mts, chplChapters: chplRoundTrip(edited.Chapters)}
	udtaEdit := edit{off: reg.regionStart, oldLen: reg.regionEnd - reg.regionStart, lit: reg.regionBytes}

	var edits []edit
	var stcoOffInTrak int
	chapEditIdx := -1 // index of the edit whose lit holds the new chapter track

	if clearing {
		if d.chapTrak != nil {
			edits = append(edits, edit{off: d.chapTrak.offset, oldLen: d.chapTrak.size}) // delete
		}
		edits = append(edits, chapTrefEdits(d, 0)...)
		edits = append(edits, udtaEdit)
	} else {
		newTrackID := d.nextTrackID
		if d.chapTrak != nil {
			newTrackID = d.chapTrackID // reuse the id the "chap" lists already name
		}
		chapTrak, off, saturated := buildChapterTrak(newTrackID, mts, d.movieTimescale, d.movieDuration, edited.Chapters, co64)
		stcoOffInTrak = off
		p.sttsSaturated = saturated

		if d.chapTrak != nil {
			// Replace the existing chapter track in place. It keeps its id, so the "chap" lists
			// and the mvhd stay as they are.
			edits = append(edits, edit{off: d.chapTrak.offset, oldLen: d.chapTrak.size, lit: chapTrak})
			chapEditIdx = len(edits) - 1
			edits = append(edits, udtaEdit)
		} else {
			// Insert the new chapter track immediately before the udta (one combined
			// edit keeps it ordered ahead of the udta at the same offset). The chapter
			// track is at the front of the combined lit, so the stco offset is unchanged.
			combined := slices.Concat(chapTrak, reg.regionBytes)
			edits = append(edits, edit{off: reg.regionStart, oldLen: reg.regionEnd - reg.regionStart, lit: combined})
			chapEditIdx = len(edits) - 1

			edits = append(edits, chapTrefEdits(d, newTrackID)...)
			if d.nextTrackIDOff != 0 {
				edits = append(edits, edit{off: d.nextTrackIDOff, oldLen: 4, lit: be32u(newTrackID + 1)})
			}
		}
		p.newMdat = renderAtom(atomName("mdat"), chapterSamples(edited.Chapters))
	}

	// Output offset fixups for the audio chunk-offset tables and the moov size.
	p.moovDelta = sumDelta(edits)
	if p.moovDelta != 0 {
		es, err := patchTables(p.moovDelta, d.moov.offset,
			func(t offsetTable) bool { return withinChapTrak(d, t) }, d.offTables, d.auxTables)
		if err != nil {
			return nil, err
		}
		edits = append(edits, es...)
	}
	// A moov that reaches the end of the file by its declared size takes its real one even
	// when its length stays, or it would take in the chapter mdat behind it.
	if p.moovDelta != 0 || !clearing && d.moovUnsized {
		edits = append(edits, sizePatch(*d.moov, p.moovDelta))
	}

	// The old track's own mdats hold nothing now: the trailing one goes, the others turn free.
	trailing, others := chapterStorage(d)
	if trailing != nil {
		edits = append(edits, edit{off: trailing.offset, oldLen: trailing.size}) // nil lit = delete
		p.reclaimed = trailing
	}
	for _, a := range others {
		edits = append(edits, edit{off: a.offset + 4, oldLen: 4, lit: []byte("free")})
		p.freed = append(p.freed, a.payloadOff())
	}
	if !clearing {
		es, err := closeFinalMdat(d, p.reclaimed)
		if err != nil {
			return nil, err
		}
		edits = append(edits, es...)
	}
	// fileDelta is the whole-file change (moovDelta minus any reclaimed mdat); the
	// moov-internal patches above stay on moovDelta, so the two diverge here.
	p.fileDelta = sumDelta(edits)
	p.edits = edits

	// The chapter samples go in a fresh mdat behind the last whole atom, after the reclaimed
	// mdat's deletion. Every edit lies at or ahead of that point, so its offset moves by the
	// whole-file fileDelta.
	if !clearing {
		p.mdatPayloadOff = d.appendAt + p.fileDelta + 8
		backpatchStco(edits[chapEditIdx].lit, stcoOffInTrak, p.mdatPayloadOff, co64)
	}
	return p, nil
}

// closeFinalMdat makes room behind a final mdat that reaches the end of the file by declaration:
// one declared open-ended takes its real size, and one cut short of its declared size cannot be
// followed, as a re-read would take the appended atom into it. A reclaimed mdat needs neither.
func closeFinalMdat(d *doc, reclaimed *atomRef) ([]edit, error) {
	last := d.topLevel[len(d.topLevel)-1]
	if last.name != atomName("mdat") || reclaimed != nil && reclaimed.offset == last.offset {
		return nil, nil
	}
	switch {
	case d.mdatTruncated:
		return nil, fmt.Errorf("%w: the final mdat atom declares more bytes than the file holds, so a chapter track cannot be written after it",
			waxerr.ErrUnsupportedFormat)
	case !d.mdatOpenEnded:
		return nil, nil
	case last.size > math.MaxUint32:
		return nil, fmt.Errorf("%w: the final mdat atom runs to the end of the file and is over 4 GiB, so a chapter track cannot be written after it",
			waxerr.ErrUnsupportedFormat)
	}
	return []edit{{off: last.offset, oldLen: 4, lit: be32u(uint32(last.size))}}, nil
}

// buildQTChapterResult constructs the plan's result for a QuickTime-chapter rewrite.
func buildQTChapterResult(edited *core.Media, base *doc, p *qtPlan, reg udtaRegion) *core.Media {
	nd := &doc{items: p.resultItems, udtaTexts: udtaTextsIn(reg)}
	// The replaced chapter track's table is gone from the output and the new track's table
	// points at chapter samples only, so the relocated tables are the ones that place essence.
	moved := newShifts(p.edits)
	nd.offTables = relocateTables(base.offTables, moved, func(t offsetTable) bool { return withinChapTrak(base, t) })
	for _, m := range base.mdats {
		if p.reclaimed != nil && m[0] == p.reclaimed.payloadOff() {
			continue // the reclaimed trailing chapter mdat is deleted from the output
		}
		if _, freed := slices.BinarySearch(p.freed, m[0]); freed {
			continue
		}
		nd.mdats = append(nd.mdats, [2]int64{m[0] + moved.at(m[0]), m[1]})
	}
	// The replaced track's samples fill no mdat any more; the new track's fill the appended one.
	if p.newMdat != nil {
		nd.mdats = append(nd.mdats, [2]int64{p.mdatPayloadOff, int64(len(p.newMdat)) - 8})
		nd.chapMdats = []int64{p.mdatPayloadOff}
	}
	tags, pics, _, _ := project(nd)
	out := &core.Media{
		Format:     core.FormatMP4,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
	}
	// A fresh parse decodes both tables and merges them. They always agree for a
	// WaxLabel-written file (the track carries the first chapter's start in a leading empty
	// edit, so its starts are absolute), and the merge takes the chpl's exact starts and
	// the QuickTime track's recovered last end.
	if len(edited.Chapters) > 0 {
		// qtWriteRoundTrip reports whether any stts delta clamped (the same MaxUint32 the
		// reader detects), so the merge picks chpl over a lossy QuickTime track as the
		// reparse would.
		qt, qtSaturated := qtWriteRoundTrip(edited.Chapters, p.mts, base.movieTimescale, base.movieDuration)
		out.Chapters, _ = mergeChapters(p.chplChapters, true, qt, true, qtSaturated)
	}
	setEssence(nd, out)
	core.SyncTextItems(out)
	return out
}

// chapTrefEdits rewrites the "chap" lists for a write that creates a chapter track, of id created,
// or removes it (created 0). A removal leaves each list its video ids. A creation writes one list,
// the track then every thumbnail track, to each trak that names or keeps a list: ffmpeg takes the
// last list it reads for the whole movie.
func chapTrefEdits(d *doc, created uint32) []edit {
	var list []uint32
	if created != 0 {
		list = []uint32{created}
		seen := map[uint32]bool{}
		for _, ref := range d.chapRefs {
			for _, id := range ref.video {
				if !seen[id] {
					list, seen[id] = append(list, id), true
				}
			}
		}
	}
	var edits []edit
	for _, ref := range d.chapRefs {
		if d.chapTrak != nil && ref.trak.offset >= d.chapTrak.offset && ref.trak.offset < d.chapTrak.end() {
			continue // the chapter track itself, removed whole
		}
		ids := ref.video
		if created != 0 {
			ids = nil
			if ref.names || len(ref.video) > 0 {
				ids = list
			}
		}
		payload := trefWithChap(ref.raw, ids)
		if bytes.Equal(payload, ref.raw) {
			continue
		}
		var e edit
		switch {
		case ref.tref == nil:
			e = edit{off: ref.mdiaOff, lit: renderAtom(atomName("tref"), payload)}
		case len(payload) == 0:
			e = edit{off: ref.tref.offset, oldLen: ref.tref.size}
		default:
			e = edit{off: ref.tref.offset, oldLen: ref.tref.size, lit: renderAtom(atomName("tref"), payload)}
		}
		edits = append(edits, e, sizePatch(ref.trak, int64(len(e.lit))-e.oldLen))
	}
	return edits
}

// trefWithChap returns a tref payload whose "chap" entries are replaced by one naming ids, where
// the first stood or else behind the last whole reference, and by none when ids is empty. Bytes
// that form no whole reference stay last.
func trefWithChap(payload []byte, ids []uint32) []byte {
	var chap []byte
	if len(ids) > 0 {
		list := make([]byte, 0, 4*len(ids))
		for _, id := range ids {
			list = binary.BigEndian.AppendUint32(list, id)
		}
		chap = renderAtom(atomName("chap"), list)
	}
	out := make([]byte, 0, len(payload)+len(chap))
	ref, rest, ok := cutAtom(payload)
	for ; ok; ref, rest, ok = cutAtom(rest) {
		if string(ref[4:8]) != "chap" {
			out = append(out, ref...)
			continue
		}
		out = append(out, chap...)
		chap = nil
	}
	return append(append(out, chap...), rest...)
}

// backpatchStco writes the resolved chunk offset into a placeholder stco/co64
// entry inside the rendered chapter track.
func backpatchStco(trak []byte, off int, value int64, co64 bool) {
	if co64 {
		binary.BigEndian.PutUint64(trak[off:off+8], uint64(value))
		return
	}
	binary.BigEndian.PutUint32(trak[off:off+4], uint32(value))
}

// relocateTables moves offset tables to where a QuickTime-chapter rewrite's output
// places them, dropping any the skip predicate rejects.
func relocateTables(src []offsetTable, moved shifts, skip func(offsetTable) bool) []offsetTable {
	out := make([]offsetTable, 0, len(src))
	for _, t := range src {
		if skip != nil && skip(t) {
			continue
		}
		nt := t
		nt.offset = t.offset + moved.at(t.offset)
		nt.entries = slices.Clone(t.entries)
		for i, e := range nt.entries {
			nt.entries[i] = uint64(int64(e) + moved.at(int64(e)))
		}
		out = append(out, nt)
	}
	return out
}

// withinChapTrak reports whether an offset table lives inside the chapter track being
// replaced. Its bytes are rewritten wholesale, so it must not be patched separately.
func withinChapTrak(d *doc, t offsetTable) bool {
	return d.chapTrak != nil && t.offset >= d.chapTrak.offset && t.offset < d.chapTrak.end()
}

// chapterStorage returns the mdats that hold the chapter track's samples and nothing a table
// names, media or auxiliary, which a rewrite of the track leaves unreferenced: the one ending
// where a new chapter mdat goes, to remove, and the others, which can only be marked free.
func chapterStorage(d *doc) (trailing *atomRef, others []atomRef) {
	if len(d.chapMdats) == 0 {
		return nil, nil
	}
	named := chunkStarts(d.mdats, slices.Concat(nonChapterTables(d), d.auxTables))
	i := -1
	for _, a := range d.topLevel {
		if a.name != atomName("mdat") {
			continue
		}
		i++ // d.mdats holds the top-level mdats in this order
		if !d.chapterMdat(a.payloadOff()) || named[i] >= 0 {
			continue
		}
		if a.end() == d.appendAt {
			trailing = &a
		} else {
			others = append(others, a)
		}
	}
	return trailing, others
}

// sumDelta totals the length change of an edit list.
func sumDelta(edits []edit) int64 {
	var d int64
	for _, e := range edits {
		d += int64(len(e.lit)) - e.oldLen
	}
	return d
}

// shifts tells how far a source offset moves in the output of an edit list, without a pass over
// the edits for each offset asked: a file can hold a great many of both.
type shifts struct {
	offs []int64 // the edits' offsets, ascending
	sums []int64 // the summed length change of the edits up to and including each
}

func newShifts(edits []edit) shifts {
	sorted := slices.Clone(edits)
	slices.SortFunc(sorted, func(a, b edit) int { return cmp.Compare(a.off, b.off) })
	s := shifts{offs: make([]int64, len(sorted)), sums: make([]int64, len(sorted))}
	var sum int64
	for i, e := range sorted {
		sum += int64(len(e.lit)) - e.oldLen
		s.offs[i], s.sums[i] = e.off, sum
	}
	return s
}

// at returns the summed length change of every edit that begins strictly before pos.
func (s shifts) at(pos int64) int64 {
	if i, _ := slices.BinarySearch(s.offs, pos); i > 0 {
		return s.sums[i-1]
	}
	return 0
}

// qtChapterOps describes the QuickTime-chapter rewrite for the report.
func qtChapterOps(d *doc, edited *core.Media, needIlst, clearing bool, delta int64) []string {
	var ops []string
	switch {
	case clearing && d.chapTrak == nil:
		ops = append(ops, "chapter removal (chpl + track references)")
	case clearing:
		ops = append(ops, "chapter removal (chpl + QuickTime track)")
	case d.chapTrak != nil:
		ops = append(ops, fmt.Sprintf("%d-chapter rewrite (chpl + QuickTime track)", len(edited.Chapters)))
	default:
		ops = append(ops, fmt.Sprintf("%d-chapter write (chpl + QuickTime track)", len(edited.Chapters)))
	}
	if needIlst {
		ops = append(ops, "ilst rewrite")
	}
	ops = appendShiftOp(ops, d, delta, d.moov.offset, func(t offsetTable) bool { return withinChapTrak(d, t) })
	if len(edited.Pictures) > 0 {
		ops = append(ops, fmt.Sprintf("pictures: %d", len(edited.Pictures)))
	}
	return ops
}
