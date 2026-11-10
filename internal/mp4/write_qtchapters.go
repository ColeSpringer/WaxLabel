package mp4

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// QuickTime chapter write. The text track is built fresh (qtchapters.go), its samples
// go in an mdat appended at end-of-file (so audio never moves), and the audio track
// gains a tref "chap" pointing at it.

// planChaptersQT computes the rewrite when chapters change and the file has an
// audio track to anchor a chapter text track to. It writes the chpl and rebuilds
// the QuickTime chapter track (or removes both when chapters are cleared).
func planChaptersQT(d *doc, edited *core.Media, needIlst, picturesChanged bool, opts core.WriteOptions, report core.WriteReport) (*core.WritePlan, error) {
	if d.udta != nil && d.udtaRaw == nil {
		return nil, fmt.Errorf("%w: MP4 udta bytes were not captured for a chapter rewrite", waxerr.ErrInvalidData)
	}

	newItems, reg, err := buildChapterUdta(d, edited, needIlst, picturesChanged, opts)
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

	segs, err := assemble(plan.edits, d.size)
	if err != nil {
		return nil, err
	}
	if plan.newMdat != nil {
		segs = append(segs, bits.Lit(plan.newMdat))
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
	result := buildQTChapterResult(edited, d, plan)
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// qtPlan is the resolved QuickTime-chapter rewrite: the edit list and the derived
// facts the segment writer and the result builder need.
type qtPlan struct {
	edits []edit
	// moovDelta is the moov-INTERNAL length change (the chapter trak, tref, mvhd
	// next-track-id, and udta edits).
	moovDelta int64
	// fileDelta is the whole-file length change before the new chapter mdat is appended:
	// moovDelta plus the negative delta of a reclaimed trailing chapter mdat. It drives
	// the file size and the appended mdat's offset.
	fileDelta int64
	newMdat   []byte // the appended chapter-sample mdat (nil when clearing)

	// reclaimed is the prior standalone trailing chapter-sample mdat this rewrite
	// deletes, or nil (a first chapter write, or a layout the safety check rejects).
	// buildQTChapterResult skips it so the result equals a fresh parse of the output.
	reclaimed *atomRef

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
		edits = append(edits, audioTrefEdits(d, 0)...)
		edits = append(edits, udtaEdit)
	} else {
		newTrackID := d.nextTrackID
		if d.chapTrak != nil {
			newTrackID = d.chapTrackID // reuse the id the audio tref already points at
		}
		chapTrak, off, saturated := buildChapterTrak(newTrackID, mts, d.movieTimescale, d.movieDuration, edited.Chapters, co64)
		stcoOffInTrak = off
		p.sttsSaturated = saturated

		if d.chapTrak != nil {
			// Replace the existing chapter track in place; the audio tref already
			// references its id, so no tref or mvhd change is needed.
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

			edits = append(edits, audioTrefEdits(d, newTrackID)...)
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
		edits = append(edits, sizePatch(*d.moov, p.moovDelta))
	}

	// Reclaim the prior chapter-sample mdat.
	if rec, ok := standaloneTrailingChapterMdat(d); ok {
		edits = append(edits, edit{off: rec.offset, oldLen: rec.size}) // nil lit = delete
		p.reclaimed = &rec
	}
	// fileDelta is the whole-file change (moovDelta minus any reclaimed mdat); the
	// moov-internal patches above stay on moovDelta, so the two diverge here.
	p.fileDelta = sumDelta(edits)
	p.edits = edits

	// The chapter samples go in a fresh mdat at end-of-file, after the reclaimed mdat's
	// deletion, so its offset uses the whole-file fileDelta.
	if !clearing {
		p.mdatPayloadOff = d.size + p.fileDelta + 8
		backpatchStco(edits[chapEditIdx].lit, stcoOffInTrak, p.mdatPayloadOff, co64)
	}
	return p, nil
}

// buildQTChapterResult constructs the plan's result for a QuickTime-chapter rewrite.
func buildQTChapterResult(edited *core.Media, base *doc, p *qtPlan) *core.Media {
	nd := &doc{items: p.resultItems}
	// The replaced chapter track's table is gone from the output and the new track's table
	// points at chapter samples only, so the relocated tables are the ones that place essence.
	nd.offTables = relocateTables(base.offTables, p.edits, func(t offsetTable) bool { return withinChapTrak(base, t) })
	for _, m := range base.mdats {
		if p.reclaimed != nil && m[0] == p.reclaimed.payloadOff() {
			continue // the reclaimed trailing chapter mdat is deleted from the output
		}
		nd.mdats = append(nd.mdats, [2]int64{m[0] + netShift(p.edits, m[0]), m[1]})
	}
	if p.newMdat != nil {
		nd.mdats = append(nd.mdats, [2]int64{base.size + p.fileDelta + 8, int64(len(p.newMdat)) - 8})
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

// audioTrefForChapter returns the audio track's tref atom after a chapter write: for id
// != 0 a "chap" reference to id plus any non-"chap" references the existing tref held;
// for id 0 only those kept references, or nil when there are none.
func audioTrefForChapter(existing []byte, id uint32) []byte {
	kept := trefChapless(existing)
	if id == 0 {
		if len(kept) == 0 {
			return nil
		}
		return renderAtom(atomName("tref"), kept)
	}
	body := append(kept, renderAtom(atomName("chap"), be32u(id))...)
	return renderAtom(atomName("tref"), body)
}

// trefChapless returns a tref payload's sub-atoms other than "chap", verbatim.
func trefChapless(payload []byte) []byte {
	var kept []byte
	n := int64(len(payload))
	for pos := int64(0); pos+8 <= n; {
		size := int64(binary.BigEndian.Uint32(payload[pos : pos+4]))
		if size < 8 || pos+size > n {
			break
		}
		if string(payload[pos+4:pos+8]) != "chap" {
			kept = append(kept, payload[pos:pos+size]...)
		}
		pos += size
	}
	return kept
}

// audioTrefEdits makes the audio track reference the chapter track id (or, when id is
// 0, stop referencing it on a clear), returning the tref edit(s) and the audio trak
// size-field patch (via sizePatch, which handles a 64-bit header).
func audioTrefEdits(d *doc, id uint32) []edit {
	if d.audioTrak == nil {
		return nil
	}
	newTref := audioTrefForChapter(d.audioTrefRaw, id) // d.audioTrefRaw is nil when there is no tref
	var trefEdit edit
	switch {
	case d.audioTref != nil: // replace the existing tref in place
		trefEdit = edit{off: d.audioTref.offset, oldLen: d.audioTref.size, lit: newTref}
	case newTref == nil: // clearing a track that had no tref: nothing to do
		return nil
	default: // insert a fresh tref before the audio track's mdia
		trefEdit = edit{off: d.audioMdiaOff, oldLen: 0, lit: newTref}
	}
	return []edit{trefEdit, sizePatch(*d.audioTrak, int64(len(trefEdit.lit))-trefEdit.oldLen)}
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
func relocateTables(src []offsetTable, edits []edit, skip func(offsetTable) bool) []offsetTable {
	out := make([]offsetTable, 0, len(src))
	for _, t := range src {
		if skip != nil && skip(t) {
			continue
		}
		nt := t
		nt.offset = t.offset + netShift(edits, t.offset)
		nt.entries = slices.Clone(t.entries)
		for i, e := range nt.entries {
			nt.entries[i] = uint64(int64(e) + netShift(edits, int64(e)))
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

// standaloneTrailingChapterMdat returns the prior chapter-sample mdat that is safe to
// reclaim on a replace/clear, and whether one was found.
func standaloneTrailingChapterMdat(d *doc) (atomRef, bool) {
	t, ok := trackOffTable(d, d.chapTrak)
	if !ok || len(t.entries) != 1 {
		return atomRef{}, false
	}
	chapOff := int64(t.entries[0])
	for _, a := range d.topLevel {
		if a.name != atomName("mdat") || a.payloadOff() != chapOff || a.end() != d.size {
			continue
		}
		// Refuse an mdat that also carries another track's samples: the chapter chunk may sit
		// at the front of an mdat shared with a second audio, video, or subtitle track, and
		// deleting it would drop that media too and leave its stco/co64 dangling.
		if mdatHoldsNonChapterChunk(d, [2]int64{a.payloadOff(), a.end()}) {
			return atomRef{}, false
		}
		// Sample auxiliary information (CENC) also lives in an mdat, but a saio is kept out
		// of nonChapterTables (an aux offset is not a media chunk, so it must not move the
		// essence trim).
		if mdatHoldsChunk([2]int64{a.payloadOff(), a.end()}, d.auxTables) {
			return atomRef{}, false
		}
		return a, true
	}
	return atomRef{}, false
}

// sumDelta totals the length change of an edit list.
func sumDelta(edits []edit) int64 {
	var d int64
	for _, e := range edits {
		d += int64(len(e.lit)) - e.oldLen
	}
	return d
}

// netShift returns how far a source offset moves in the output: the summed length
// change of every edit that begins strictly before it.
func netShift(edits []edit, pos int64) int64 {
	var d int64
	for _, e := range edits {
		if e.off < pos {
			d += int64(len(e.lit)) - e.oldLen
		}
	}
	return d
}

// qtChapterOps describes the QuickTime-chapter rewrite for the report.
func qtChapterOps(d *doc, edited *core.Media, needIlst, clearing bool, delta int64) []string {
	var ops []string
	switch {
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
	if delta != 0 {
		ops = append(ops, fmt.Sprintf("%d offset table shift(s)", len(d.offTables)+len(d.auxTables)))
	}
	if len(edited.Pictures) > 0 {
		ops = append(ops, fmt.Sprintf("pictures: %d", len(edited.Pictures)))
	}
	return ops
}
