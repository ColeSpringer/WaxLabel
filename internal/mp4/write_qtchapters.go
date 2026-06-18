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

// QuickTime chapter write. A chapter edit rewrites both chapter
// representations so the result is visible everywhere: the Nero chpl (in
// moov.udta, handled by write_chapters.go) and a QuickTime chapter text track -
// the form iTunes and Apple Books read. The text track is built fresh
// (qtchapters.go), its samples go in an mdat appended at end-of-file (so audio
// never moves), and the audio track gains a tref "chap" pointing at it.
//
// The rewrite stays within the existing single-delta offset machinery: every
// moov-internal change (a tref, the chapter trak, the mvhd next-track-id, the
// udta) is one edit at its original offset, and a generalized netShift relocates
// the result document's structures. The audio mdat (if it follows moov) shifts by
// the one combined delta via the existing offsetPatch. The new chapter track's
// stco is the single new offset table; because that table is the last atom in the
// track its address is backpatched after the moov delta - and thus the appended
// mdat's offset - is known, with no fixpoint iteration.

// planChaptersQT computes the rewrite when chapters change and the file has an
// audio track to anchor a chapter text track to. It writes the chpl and rebuilds
// the QuickTime chapter track (or removes both when chapters are cleared).
func planChaptersQT(d *doc, edited *core.Media, needIlst bool, opts core.WriteOptions, report core.WriteReport) (*core.WritePlan, error) {
	if d.udta != nil && d.udtaRaw == nil {
		return nil, fmt.Errorf("%w: MP4 udta bytes were not captured for a chapter rewrite", waxerr.ErrInvalidData)
	}

	newItems, reg, err := buildChapterUdta(d, edited, needIlst, opts)
	if err != nil {
		return nil, err
	}

	clearing := len(edited.Chapters) == 0
	mts := d.movieTimescale
	if mts == 0 {
		mts = 1000 // a sane default when the movie header lacked a timescale
	}

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

	total := d.size + plan.totalDelta + int64(len(plan.newMdat))
	if err := checkSizes([]atomRef{*d.moov}, plan.totalDelta); err != nil {
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
	report.Operations = qtChapterOps(d, edited, needIlst, clearing, plan.totalDelta)
	if n := truncatedTitleCount(edited.Chapters); n > 0 {
		report.Warnings = core.Warn(report.Warnings, core.WarnChapterTitleTruncated,
			fmt.Sprintf("%d chapter title(s) trimmed to %d bytes (the chapter-title length limit)", n, titleByteMax))
	}

	plan.resultItems = d.items
	if needIlst {
		plan.resultItems = newItems
	}
	result := buildQTChapterResult(edited, d, plan)
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// qtPlan is the resolved QuickTime-chapter rewrite: the edit list and the derived
// facts both the segment writer and the result builder need.
type qtPlan struct {
	edits      []edit
	totalDelta int64
	newMdat    []byte // the appended chapter-sample mdat (nil when clearing)

	resultItems []item

	// udta (re-rendered chpl/ilst); udtaNewOff is its output offset (0 if dropped).
	udtaPayload []byte
	udtaNewOff  int64

	// The new chapter text track and its appended-mdat data.
	chapTrakNewOff int64  // output offset of the new chapter trak (0 when clearing)
	chapTrakLen    int64  // length of the new chapter trak
	chapStcoOff    int64  // output offset of the new stco/co64 atom
	chapStcoSize   int64  // size of that atom
	chapStcoCo64   bool   // 64-bit chunk offsets
	mdatPayloadOff int64  // absolute offset the chapter stco points at
	newTrackID     uint32 // the chapter track's id

	// The audio track's size change from a tref insert/remove.
	audioTrakDelta int64

	// For the post-write chapter view.
	mts          uint32
	chplChapters []core.Chapter // chplRoundTrip(edited.Chapters), for the result's conflict check
}

// assembleQT builds the full edit list for one stco width. It is called at most
// twice (32- then 64-bit), so the offset backpatch needs no fixpoint.
func assembleQT(d *doc, edited *core.Media, reg udtaRegion, clearing bool, mts uint32, co64 bool) (*qtPlan, error) {
	p := &qtPlan{mts: mts, chplChapters: chplRoundTrip(edited.Chapters)}
	udtaEdit := edit{off: reg.regionStart, oldLen: reg.regionEnd - reg.regionStart, lit: reg.regionBytes}

	var edits []edit
	var stcoOffInTrak int
	chapEditIdx := -1      // index of the edit whose lit holds the new chapter track
	udtaPrefix := int64(0) // bytes before the udta within its (possibly combined) edit

	if clearing {
		if d.chapTrak != nil {
			edits = append(edits, edit{off: d.chapTrak.offset, oldLen: d.chapTrak.size}) // delete
		}
		trefEdits, delta := audioTrefEdits(d, 0)
		edits = append(edits, trefEdits...)
		p.audioTrakDelta = delta
		edits = append(edits, udtaEdit)
	} else {
		switch {
		case d.chapTrak != nil:
			p.newTrackID = d.chapTrackID // reuse the id the audio tref already points at
		default:
			p.newTrackID = d.nextTrackID
		}
		chapTrak, off := buildChapterTrak(p.newTrackID, mts, d.movieDuration, edited.Chapters, co64)
		stcoOffInTrak = off
		p.chapTrakLen = int64(len(chapTrak))

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
			udtaPrefix = int64(len(chapTrak))

			trefEdits, delta := audioTrefEdits(d, p.newTrackID)
			edits = append(edits, trefEdits...)
			p.audioTrakDelta = delta
			if d.nextTrackIDOff != 0 {
				edits = append(edits, edit{off: d.nextTrackIDOff, oldLen: 4, lit: be32u(p.newTrackID + 1)})
			}
		}
		p.newMdat = renderAtom(atomName("mdat"), chapterSamples(edited.Chapters))
	}

	// Output offset fixups for the audio chunk-offset tables and the moov size. The
	// replaced chapter track's own table is skipped - it is being rewritten
	// wholesale (and its old samples are abandoned), so patching it would both
	// overlap that edit and point at dead bytes.
	p.totalDelta = sumDelta(edits)
	for _, t := range d.offTables {
		if withinChapTrak(d, t) {
			continue
		}
		e, err := offsetPatch(t, p.totalDelta, d.moov.offset)
		if err != nil {
			return nil, err
		}
		edits = append(edits, e)
	}
	if p.totalDelta != 0 {
		edits = append(edits, sizePatch(*d.moov, p.totalDelta))
	}
	p.edits = edits

	// Derived output offsets (computed before backpatching, which preserves
	// lengths). The chapter samples land in a fresh mdat at end-of-file.
	if !clearing {
		p.mdatPayloadOff = d.size + p.totalDelta + 8
		backpatchStco(edits[chapEditIdx].lit, stcoOffInTrak, p.mdatPayloadOff, co64)
		p.chapStcoCo64 = co64
		// The new chapter track lands either replacing the old one or at the udta
		// insertion point (computed before the udta in the combined edit).
		insOff := reg.regionStart
		if d.chapTrak != nil {
			insOff = d.chapTrak.offset
		}
		p.chapTrakNewOff = insOff + netShift(edits, insOff)
		// The offset table is the track's last atom, so its entry sits 16 bytes
		// (header 8 + version/flags 4 + count 4) past the atom start.
		stcoAtomRel := int64(stcoOffInTrak) - 16
		p.chapStcoOff = p.chapTrakNewOff + stcoAtomRel
		p.chapStcoSize = p.chapTrakLen - stcoAtomRel
	}
	if len(reg.udtaPayload) > 0 {
		p.udtaPayload = reg.udtaPayload
		p.udtaNewOff = reg.regionStart + netShift(edits, reg.regionStart) + udtaPrefix
	}
	return p, nil
}

// buildQTChapterResult constructs the post-write Media for a QuickTime-chapter
// rewrite so the engine returns it without a reparse. It equals a fresh parse of
// the output: the chapters are the QuickTime round-trip (that track wins on
// read), every structure is relocated by the same edit list the bytes used, the
// new chapter offset table and appended mdat are added, and the chapter-write
// refs are carried so a further edit needs no source.
func buildQTChapterResult(edited *core.Media, base *doc, p *qtPlan) *core.Media {
	clearing := len(edited.Chapters) == 0
	total := base.size + p.totalDelta + int64(len(p.newMdat))
	nd := &doc{
		size:        total,
		cfg:         base.cfg,
		track:       base.track,
		majorBrand:  base.majorBrand,
		items:       p.resultItems,
		chplVersion: base.chplVersion,
	}

	// A fresh parse prefers the QuickTime track (it carries End), which we wrote,
	// so the result reflects its decode. A chpl/QuickTime disagreement (e.g. a
	// first chapter not at zero, which the track normalizes to zero) reparses as a
	// source conflict, mirrored here.
	if !clearing {
		nd.chapters = qtWriteRoundTrip(edited.Chapters, p.mts, base.movieDuration)
		nd.hasQTChapters = true
		nd.chplCount = len(p.chplChapters)
		nd.chapterConflict = !chaptersAgree(p.chplChapters, nd.chapters)
	}

	// moov grows by the one combined delta; everything else relocates by netShift.
	moov := *base.moov
	moov.size += p.totalDelta
	nd.moov = &moov

	for _, t := range base.offTables {
		if withinChapTrak(base, t) {
			continue // the replaced/removed chapter track's table is gone from the output
		}
		nt := t
		nt.offset = t.offset + netShift(p.edits, t.offset)
		nt.entries = slices.Clone(t.entries)
		for i, e := range nt.entries {
			nt.entries[i] = uint64(int64(e) + netShift(p.edits, int64(e)))
		}
		nd.offTables = append(nd.offTables, nt)
	}
	if !clearing {
		nd.offTables = append(nd.offTables, offsetTable{
			offset: p.chapStcoOff, headerLen: 8, size: p.chapStcoSize,
			co64: p.chapStcoCo64, entries: []uint64{uint64(p.mdatPayloadOff)},
		})
	}

	for _, m := range base.mdats {
		nd.mdats = append(nd.mdats, [2]int64{m[0] + netShift(p.edits, m[0]), m[1]})
	}
	if p.newMdat != nil {
		nd.mdats = append(nd.mdats, [2]int64{base.size + p.totalDelta + 8, int64(len(p.newMdat)) - 8})
	}

	for _, a := range base.topLevel {
		if a.offset == base.moov.offset {
			a.size += p.totalDelta
		} else {
			a.offset += netShift(p.edits, a.offset)
		}
		nd.topLevel = append(nd.topLevel, a)
	}
	if p.newMdat != nil {
		nd.topLevel = append(nd.topLevel, atomRef{
			name: atomName("mdat"), offset: base.size + p.totalDelta, headerLen: 8, size: int64(len(p.newMdat)),
		})
	}

	// Recover the udta children by re-walking the rendered payload, positioned at
	// the udta's output offset (exactly what a fresh parse would find).
	if len(p.udtaPayload) > 0 {
		nd.udta = &atomRef{name: atomName("udta"), offset: p.udtaNewOff, headerLen: 8, size: 8 + int64(len(p.udtaPayload))}
		nd.udtaRaw = p.udtaPayload
		ups := p.udtaNewOff + 8
		for _, k := range walkUdta(p.udtaPayload) {
			switch k.id() {
			case "meta":
				m := atomRefAt(k, ups)
				nd.meta = &m
				for _, mk := range k.children {
					switch mk.id() {
					case "ilst":
						r := atomRefAt(mk, ups)
						nd.ilst = &r
					case "free":
						r := atomRefAt(mk, ups)
						nd.free = &r
					}
				}
			case "chpl":
				r := atomRefAt(k, ups)
				nd.chpl = &r
			}
		}
	}

	// Chapter-write refs for a follow-up edit (no reparse). These must equal what a
	// fresh parse of the output would capture, or a chained chapter edit corrupts
	// the moov - in particular the audio tref, which this rewrite may have inserted
	// (create), replaced, or dropped (clear).
	nd.movieTimescale = base.movieTimescale
	nd.movieDuration = base.movieDuration
	nd.mvhd = shiftRef(base.mvhd, p.edits)
	nd.audioMdiaOff = base.audioMdiaOff + netShift(p.edits, base.audioMdiaOff)
	if base.audioTrak != nil {
		at := *base.audioTrak
		at.offset += netShift(p.edits, at.offset)
		at.size += p.audioTrakDelta
		nd.audioTrak = &at
	}
	if base.nextTrackIDOff != 0 {
		nd.nextTrackIDOff = base.nextTrackIDOff + netShift(p.edits, base.nextTrackIDOff)
	}
	nd.nextTrackID = base.nextTrackID
	if !clearing {
		nd.chapTrak = &atomRef{name: atomName("trak"), offset: p.chapTrakNewOff, headerLen: 8, size: p.chapTrakLen}
		nd.chapTrackID = p.newTrackID
		if p.newTrackID >= nd.nextTrackID {
			nd.nextTrackID = p.newTrackID + 1
		}
	}

	// The audio tref after this rewrite: a replace leaves it untouched; a create or
	// clear rebuilt it via the same audioTrefForChapter used for the byte edit, so
	// the recorded ref matches the bytes exactly.
	if !clearing && base.chapTrak != nil {
		nd.audioTref = shiftRef(base.audioTref, p.edits)
		nd.audioTrefRaw = slices.Clone(base.audioTrefRaw)
		nd.audioHasChap = true
	} else {
		id := uint32(0)
		if !clearing {
			id = p.newTrackID
		}
		newTref := audioTrefForChapter(base.audioTrefRaw, id)
		anchor, inserted := base.audioMdiaOff, base.audioTref == nil
		if base.audioTref != nil {
			anchor = base.audioTref.offset
		}
		if newTref == nil {
			nd.audioTref, nd.audioTrefRaw = nil, nil
		} else {
			nd.audioTref = &atomRef{name: atomName("tref"), offset: anchor + netShift(p.edits, anchor),
				headerLen: 8, size: int64(len(newTref))}
			nd.audioTrefRaw = slices.Clone(newTref[8:])
			if inserted {
				nd.audioMdiaOff += int64(len(newTref)) // the inserted tref sits before mdia
			}
		}
		nd.audioHasChap = !clearing
	}

	tags, pics, families, numericGenre := project(nd)
	out := &core.Media{
		Format:     core.FormatMP4,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
		Chapters:   nd.chapters,
		Families:   families,
		Warnings:   chapterWarnings(mediaWarnings(tags, numericGenre), nd.chapterConflict),
		Native:     nd,
		Identity:   core.Identity{Size: total},
	}
	setEssence(nd, out)
	return out
}

// shiftRef relocates an atom reference by the edit list's net shift at its offset.
func shiftRef(r *atomRef, edits []edit) *atomRef {
	if r == nil {
		return nil
	}
	c := *r
	c.offset += netShift(edits, c.offset)
	return &c
}

// carryChapterRefs copies the QuickTime chapter-write references into a document
// rewritten by a single-region edit (a tag edit or a chpl-only chapter edit), so
// a chapter edit can follow it without a reparse and rebuild the QuickTime track.
// The refs sit before the tag/udta region in every normal layout, so one at or
// past the edited region is shifted by delta and the rest are copied unchanged.
func carryChapterRefs(nd, base *doc, regionEnd, delta int64) {
	shiftOff := func(o int64) int64 {
		if o >= regionEnd {
			return o + delta
		}
		return o
	}
	shift := func(r *atomRef) *atomRef {
		if r == nil {
			return nil
		}
		c := *r
		c.offset = shiftOff(c.offset)
		return &c
	}
	nd.audioTrak = shift(base.audioTrak)
	nd.audioMdiaOff = shiftOff(base.audioMdiaOff)
	nd.audioTref = shift(base.audioTref)
	nd.audioTrefRaw = slices.Clone(base.audioTrefRaw)
	nd.audioHasChap = base.audioHasChap
	nd.chapTrak = shift(base.chapTrak)
	nd.chapTrackID = base.chapTrackID
	nd.mvhd = shift(base.mvhd)
	nd.nextTrackIDOff = shiftOff(base.nextTrackIDOff)
	nd.movieTimescale = base.movieTimescale
	nd.movieDuration = base.movieDuration
	nd.nextTrackID = base.nextTrackID
}

// audioTrefForChapter returns the audio track's tref atom after a chapter write:
// for id != 0 a "chap" reference to id with any non-"chap" references the existing
// tref held preserved; for id == 0 (clearing) the existing references with "chap"
// removed. It returns nil when the result would be an empty tref (no references) -
// i.e. no tref atom should exist. It is the single source of the output tref bytes
// for both the byte edit and the result document, so the two cannot disagree.
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

// audioTrefEdits makes the audio track reference the chapter track id (or, when
// id is 0, stop referencing it on a clear), returning the tref edit(s), the audio
// trak size-field patch (via sizePatch, which handles a 64-bit header), and the
// resulting size change. A replace that reuses the existing id calls this only on
// clear.
func audioTrefEdits(d *doc, id uint32) ([]edit, int64) {
	if d.audioTrak == nil {
		return nil, 0
	}
	newTref := audioTrefForChapter(d.audioTrefRaw, id) // d.audioTrefRaw is nil when there is no tref
	var trefEdit edit
	switch {
	case d.audioTref != nil: // replace the existing tref in place
		trefEdit = edit{off: d.audioTref.offset, oldLen: d.audioTref.size, lit: newTref}
	case newTref == nil: // clearing a track that had no tref: nothing to do
		return nil, 0
	default: // insert a fresh tref before the audio track's mdia
		trefEdit = edit{off: d.audioMdiaOff, oldLen: 0, lit: newTref}
	}
	delta := int64(len(trefEdit.lit)) - trefEdit.oldLen
	return []edit{trefEdit, sizePatch(*d.audioTrak, delta)}, delta
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

// withinChapTrak reports whether an offset table lives inside the chapter track
// being replaced - its bytes are rewritten wholesale, so it must not be patched
// separately.
func withinChapTrak(d *doc, t offsetTable) bool {
	return d.chapTrak != nil && t.offset >= d.chapTrak.offset && t.offset < d.chapTrak.end()
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
// change of every edit that begins strictly before it. The edits are disjoint and
// every queried position is an atom boundary, so a "strictly before" test counts
// an edit that resizes earlier bytes and excludes an insertion at the position
// itself (whose new bytes belong at the position, unshifted).
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
		ops = append(ops, "removed chapters (chpl + QuickTime track)")
	case d.chapTrak != nil:
		ops = append(ops, fmt.Sprintf("rewrote %d chapters (chpl + QuickTime track)", len(edited.Chapters)))
	default:
		ops = append(ops, fmt.Sprintf("wrote %d chapters (chpl + QuickTime track)", len(edited.Chapters)))
	}
	if needIlst {
		ops = append(ops, "rewrote ilst")
	}
	if delta != 0 {
		ops = append(ops, fmt.Sprintf("shifted %d chunk-offset table(s)", len(d.offTables)))
	}
	if len(edited.Pictures) > 0 {
		ops = append(ops, fmt.Sprintf("pictures: %d", len(edited.Pictures)))
	}
	return ops
}
