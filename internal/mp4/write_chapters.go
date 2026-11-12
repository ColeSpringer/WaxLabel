package mp4

import (
	"fmt"
	"slices"
	"sort"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxChplChapters is the Nero chpl chapter cap: the count is an 8-bit field, so a
// list longer than this cannot be written. The editor rejects it at Prepare.
const maxChplChapters = 255

// planChapters computes the rewrite when the chapter list changed.
func planChapters(d *doc, base, edited *core.Media, needIlst, picturesChanged bool, cf conflicts, opts core.WriteOptions, report core.WriteReport) (*core.WritePlan, error) {
	if d.udta != nil && d.udtaRaw == nil {
		return nil, fmt.Errorf("%w: MP4 udta bytes were not captured for a chapter rewrite", waxerr.ErrInvalidData)
	}

	// The QuickTime chapter track is rebuilt alongside the chpl, so iTunes and Apple Books see
	// the edit too. A new one needs a free track id and a track to name it; a clear with no
	// track to remove still takes what is no video track out of the "chap" lists.
	writing := len(edited.Chapters) > 0
	creatable := d.nextTrackID > 0 && slices.ContainsFunc(d.chapRefs, func(r trefChap) bool { return r.names })
	if d.chapTrak != nil || writing && creatable || !writing && len(chapTrefEdits(d, 0)) > 0 {
		return planChaptersQT(d, base, edited, needIlst, picturesChanged, cf, opts, report)
	}

	cu, err := buildChapterUdta(d, edited, needIlst, picturesChanged, cf, opts)
	if err != nil {
		return nil, err
	}
	reg := cu.reg
	if err := checkBoxSize32(atomName("udta"), 8+int64(len(reg.udtaPayload))); err != nil {
		return nil, err
	}
	edits := append([]edit{reg.edit()}, cu.extra...)
	first := firstOffset(edits)
	edits, delta, err := closeMoov(d, edits)
	if err != nil {
		return nil, err
	}
	segs, err := assemble(edits, d.size)
	if err != nil {
		return nil, err
	}

	report.BytesAfter = d.size + delta
	report.PaddingAfter = reg.freeContent
	report.Warnings = reducedTextWarnings(paddingClampWarning(report.Warnings, reg.paddingClamped), cu.reduced)
	report.Operations = append(chapterOps(d, edited, needIlst, delta, first), cu.ops...)
	if n := truncatedTitleCount(edited.Chapters); n > 0 {
		report.Warnings = core.Warn(report.Warnings, core.WarnChapterTitleTruncated,
			fmt.Sprintf("%d chapter title(s) trimmed to %d bytes (the Nero chpl length prefix is one byte)", n, titleByteMax))
	}
	// No WarnChaptersStale here: this fallback runs only when the file has no QuickTime
	// chapter track, so there is none to leave stale.

	// The written chpl round-trips through its 100 ns / 255-byte encoding.
	result := tagResult(edited, d, cu.stores(d, needIlst), newShifts(edits), chplRoundTrip(edited.Chapters))
	report.Warnings = droppedDupWarnings(report.Warnings, d, base, result, cu.textDups, true)
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// droppedDupWarnings reports what the duplicates a write removes hold that the file holds nowhere
// else: the text atoms dropped with the atom of their name, and on a chapter edit the chpl atoms.
func droppedDupWarnings(ws []core.Warning, d *doc, base, result *core.Media, texts []core.DuplicateContent, chapterEdit bool) []core.Warning {
	ws = core.AppendDuplicateBlockDropped(ws, "moov.udta text atom", base.Tags, result.Tags, texts)
	if !chapterEdit {
		return ws
	}
	held := func(list []core.Chapter, ch core.Chapter) bool {
		return slices.ContainsFunc(list, func(o core.Chapter) bool { return o.Start == ch.Start && o.Title == ch.Title })
	}
	var lost core.DuplicateContent
	unread := int64(0)
	for _, dup := range d.chplDups {
		// A chpl is a udta child, so its payload lies in the captured udta bytes.
		ups := d.udta.payloadOff()
		_, chapters, ok := parseChpl(d.udtaRaw[dup.payloadOff()-ups:dup.end()-ups], &titleReader{page: &windows1252})
		if !ok {
			unread++
		}
		for _, ch := range chapters {
			if !held(base.Chapters, ch) && !held(result.Chapters, ch) {
				lost.Chapters++
			}
		}
	}
	if unread > 0 {
		lost.Other = []core.ContentCount{{Kind: "unreadable chpl atom(s)", N: unread}}
	}
	return core.AppendDuplicateBlockDropped(ws, "chpl atom", base.Tags, result.Tags, []core.DuplicateContent{lost})
}

// chapterUdta is the udta a chapter write renders, with what the write changes beside it.
type chapterUdta struct {
	reg      udtaRegion
	items    []item // the rebuilt ilst items, when the tags or pictures changed
	second   []item // the second store's items as written
	extra    []edit // the store edits that lie outside udta, under moov.meta
	ops      []string
	textDups []core.DuplicateContent // what the duplicate text atoms the region drops hold
	reduced  []tag.Key               // the keys whose text atom went, unable to hold the value
}

// stores is what the written file projects its tags from; the ilst read stands when it is not rebuilt.
func (cu chapterUdta) stores(d *doc, needIlst bool) stores {
	st := stores{items: d.items, second: cu.second, texts: udtaTextsIn(cu.reg)}
	if needIlst {
		st.items = cu.items
	}
	return st
}

// buildChapterUdta renders the new ilst (when tags or pictures changed) and the udta region (the
// chpl, and the ilst spliced in) that both chapter-write paths, the chpl-only fallback and the
// QuickTime path, start from.
func buildChapterUdta(d *doc, edited *core.Media, needIlst, picturesChanged bool, cf conflicts, opts core.WriteOptions) (chapterUdta, error) {
	cu := chapterUdta{second: d.secondItems()}
	var newIlst []byte
	var newKeys []string
	var qw qtMetaWrite
	if needIlst {
		covr := coverItemsToWrite(edited.Pictures, d.items, picturesChanged)
		// The same encoding decision the ilst path makes, recomputed rather than threaded
		// through: a chapter edit must not undo an earlier --numeric-genre run either.
		numericGenre := numericGenreEncoding(d.items, edited.Tags, opts.NumericGenre)
		var respelled int
		cu.items, newKeys, respelled = buildIlstItems(d, edited.Tags, covr, numericGenre, cf)
		cu.ops = respelledOps(respelled)
		if err := checkBuiltItems(cu.items, d.items, opts.Limits.MaxAllocBytes); err != nil {
			return cu, err
		}
		var err error
		if newIlst, err = renderIlst(cu.items); err != nil {
			return cu, err
		}
		// The same store bookkeeping the tag-only path does: an mdta keys index that gained
		// an entry must be rewritten too, or the fresh items would name an index past the
		// table; udta-level text atoms and a second store are kept in sync with the ilst.
		if qw, err = planQTMeta(d, edited, newKeys, false, picturesChanged); err != nil {
			return cu, err
		}
		cu.second, cu.extra, cu.ops = qw.second.items, qw.second.edits, append(cu.ops, qw.second.ops()...)
		cu.textDups, cu.reduced = qw.textDups, qw.reducedTexts
	}
	inUdta := needIlst && !d.metaAtMoov
	var err error
	cu.reg, err = buildUdtaRegion(d, udtaWrite{
		ilst: newIlst, needIlst: inUdta, chapters: edited.Chapters, chapterEdit: true,
		reps: qw.reps, appends: qw.appends, metaDelta: qw.metaDelta,
	}, opts)
	if err != nil || inUdta || !needIlst {
		return cu, err
	}
	es, free, clamped, err := metaEdits(d, newIlst, newKeys, opts.Padding)
	cu.extra = append(cu.extra, es...)
	cu.reg.freeContent, cu.reg.paddingClamped = free, clamped
	return cu, err
}

// udtaRegion is the resolved placement of the rewritten user-data box: the source
// span it replaces (or the insertion point when udta is created), the bytes that
// replace it, the new udta payload (re-walked for its text atoms), and the
// enclosing atoms whose sizes grow.
type udtaRegion struct {
	regionStart, regionEnd int64
	regionBytes            []byte
	udtaPayload            []byte
	ancestors              []atomRef
	freeContent            int64
	paddingClamped         bool
}

// edit is the region as one replacement of the source bytes.
func (r udtaRegion) edit() edit {
	return edit{off: r.regionStart, oldLen: r.regionEnd - r.regionStart, lit: r.regionBytes}
}

// udtaWrite is what one udta region rebuild must place. chapterEdit gates the chpl
// entirely: a tag-only rebuild must neither re-render it from the merged list nor read
// an empty list as a clear.
type udtaWrite struct {
	ilst        []byte
	needIlst    bool
	chapters    []core.Chapter
	chapterEdit bool
	reps        []byteRep
	appends     []byte
	// metaDelta is the net byte change of the reps that sit inside the meta box (a
	// rewritten keys index).
	metaDelta int64
}

// buildUdtaRegion produces the new moov.udta: it splices the new ilst (when tags
// or pictures changed), the new chpl, and any caller-supplied replacements into the
// preserved udta bytes, creating the wrappers when they are absent and dropping the chpl
// when the chapter list is cleared.
func buildUdtaRegion(d *doc, w udtaWrite, opts core.WriteOptions) (udtaRegion, error) {
	newIlst, needIlst, chapters := w.ilst, w.needIlst, w.chapters
	var padClamped bool
	// An empty ilst, just its 8-byte header, is created only to hold a padding floor.
	hasIlst := needIlst && (len(newIlst) > 8 || opts.Padding.Min > 0)
	needChpl := w.chapterEdit && len(chapters) > 0

	if d.udta == nil {
		var payload []byte
		var freeContent int64
		if hasIlst {
			region, fc, cl := fitIlst(newIlst, 0, opts.Padding)
			freeContent, padClamped = fc, padClamped || cl
			payload = append(payload, renderFullBox(atomName("meta"), append(hdlrAtom(), region...))...)
		}
		if needChpl {
			payload = append(payload, renderChpl(d.chplVersion, chapters)...)
		}
		payload = append(payload, w.appends...)
		at := d.moov.end()
		reg := udtaRegion{regionStart: at, regionEnd: at, ancestors: []atomRef{*d.moov}}
		if len(payload) == 0 {
			return reg, nil // nothing to write (e.g. clearing QuickTime-only chapters)
		}
		reg.regionBytes = renderAtom(atomName("udta"), payload)
		reg.udtaPayload = payload
		reg.freeContent = freeContent
		reg.paddingClamped = padClamped
		return reg, nil
	}

	ups := d.udta.offset + d.udta.headerLen
	reps := append([]byteRep(nil), w.reps...)
	appends := append([]byte(nil), w.appends...)
	var freeContent int64

	if needIlst {
		switch {
		case d.ilst != nil:
			startR, endR := ilstRegionRel(d, ups)
			region, fc, cl := fitIlst(newIlst, endR-startR, opts.Padding)
			freeContent, padClamped = fc, padClamped || cl
			reps = append(reps, byteRep{start: startR, oldLen: endR - startR, repl: region})
			reps = append(reps, metaSizeRep(d, ups, d.meta.size+int64(len(region))-(endR-startR)+w.metaDelta))
		case d.meta != nil && hasIlst:
			region, fc, cl := fitIlst(newIlst, 0, opts.Padding)
			freeContent, padClamped = fc, padClamped || cl
			insR := d.meta.end() - ups
			reps = append(reps, byteRep{start: insR, oldLen: 0, repl: region})
			reps = append(reps, metaSizeRep(d, ups, d.meta.size+int64(len(region))+w.metaDelta))
		case hasIlst:
			region, fc, cl := fitIlst(newIlst, 0, opts.Padding)
			freeContent, padClamped = fc, padClamped || cl
			appends = append(appends, renderFullBox(atomName("meta"), append(hdlrAtom(), region...))...)
		}
	} else {
		// The ilst region is not rewritten (a chapters-only edit), so the file's existing
		// free atom survives verbatim and its padding is what the write leaves.
		freeContent = d.PaddingBytes()
	}

	if w.chapterEdit {
		switch {
		case d.chpl != nil && needChpl:
			reps = append(reps, byteRep{start: d.chpl.offset - ups, oldLen: d.chpl.size, repl: renderChpl(d.chplVersion, chapters)})
		case d.chpl != nil: // cleared: drop the chpl
			reps = append(reps, byteRep{start: d.chpl.offset - ups, oldLen: d.chpl.size})
		case needChpl: // no existing chpl: append a new one
			appends = append(appends, renderChpl(d.chplVersion, chapters)...)
		}
		for _, dup := range d.chplDups {
			reps = append(reps, byteRep{start: dup.offset - ups, oldLen: dup.size})
		}
	}

	payload, err := spliceBytes(d.udtaRaw, reps)
	if err != nil {
		return udtaRegion{}, err
	}
	if len(appends) > 0 {
		// Append after the last complete child, dropping any tolerated trailing zero
		// (QuickTime terminator / padding) that would otherwise shift the new atom.
		clean := udtaCleanLen(payload)
		payload = append(payload[:clean:clean], appends...)
	}

	reg := udtaRegion{
		regionStart: d.udta.offset, regionEnd: d.udta.end(),
		ancestors: []atomRef{*d.moov}, freeContent: freeContent, paddingClamped: padClamped,
	}
	if len(payload) == 0 {
		// The udta became empty (e.g. ClearChapters on a chpl-only udta): drop the whole
		// atom.
		return reg, nil
	}
	reg.regionBytes = renderAtom(atomName("udta"), payload)
	reg.udtaPayload = payload
	return reg, nil
}

// byteRep is one in-place replacement within the udta payload: replace oldLen
// bytes at start with repl (a nil repl deletes the range).
type byteRep struct {
	start  int64
	oldLen int64
	repl   []byte
}

// spliceBytes applies the (disjoint) replacements to src, copying every byte not
// covered by a replacement, so udta siblings and meta children outside the ilst/chpl
// ranges survive a chapter rewrite verbatim.
func spliceBytes(src []byte, reps []byteRep) ([]byte, error) {
	// Order by start. Emitting a replace before an insert at the same start would advance
	// pos past it and trip the r.start<pos check below, so the oldLen tie-break forces
	// insert-before-replace.
	sort.SliceStable(reps, func(i, j int) bool {
		if reps[i].start != reps[j].start {
			return reps[i].start < reps[j].start
		}
		return reps[i].oldLen < reps[j].oldLen
	})
	n := int64(len(src))
	out := make([]byte, 0, n)
	pos := int64(0)
	for _, r := range reps {
		if r.start < pos || r.oldLen < 0 || r.start+r.oldLen > n {
			return nil, fmt.Errorf("%w: invalid udta splice at %d (len %d)", waxerr.ErrInvalidData, r.start, r.oldLen)
		}
		out = append(out, src[pos:r.start]...)
		out = append(out, r.repl...)
		pos = r.start + r.oldLen
	}
	out = append(out, src[pos:]...)
	return out, nil
}

// ilstRegionRel returns the existing ilst+adjacent-free region relative to the
// udta payload start.
func ilstRegionRel(d *doc, ups int64) (start, end int64) {
	s, e := d.ilstRegion()
	return s - ups, e - ups
}

// metaSizeRep returns the replacement that rewrites the meta atom's size field
// (32- or 64-bit) to newSize, relative to the udta payload start.
func metaSizeRep(d *doc, ups, newSize int64) byteRep {
	off, width := d.meta.sizeField()
	b := make([]byte, width)
	putBoxSize(b, width, newSize)
	return byteRep{start: d.meta.offset - ups + off, oldLen: width, repl: b}
}

// fitIlst places the new ilst within a region of oldRegionLen bytes with the padding ilstPadding
// gives it, so chapter and tag edits leave the same slack.
func fitIlst(newIlst []byte, oldRegionLen int64, pol core.PaddingPolicy) (region []byte, freeContent int64, clamped bool) {
	pad, inPlace := ilstPadding(int64(len(newIlst)), oldRegionLen, pol)
	region, freeContent = appendFree(newIlst, pad)
	return region, freeContent, !inPlace && padClamped(pol)
}

// truncatedTitleCount reports how many chapter titles exceed the Nero chpl
// single-byte length prefix and will therefore be trimmed on write.
func truncatedTitleCount(chapters []core.Chapter) int {
	n := 0
	for _, ch := range chapters {
		if len(ch.Title) > titleByteMax {
			n++
		}
	}
	return n
}

// chapterOps describes the chapter rewrite for the report.
func chapterOps(d *doc, edited *core.Media, needIlst bool, delta, insertion int64) []string {
	var ops []string
	switch {
	case len(edited.Chapters) == 0:
		ops = append(ops, "chapter removal (chpl)")
	case d.chpl != nil:
		ops = append(ops, fmt.Sprintf("%d-chapter rewrite (chpl)", len(edited.Chapters)))
	default:
		ops = append(ops, fmt.Sprintf("%d-chapter write (chpl)", len(edited.Chapters)))
	}
	if needIlst {
		ops = append(ops, "ilst rewrite")
	}
	ops = appendShiftOp(ops, d, delta, insertion, nil)
	if len(edited.Pictures) > 0 {
		ops = append(ops, fmt.Sprintf("pictures: %d", len(edited.Pictures)))
	}
	return ops
}

// udtaTextsIn decodes the QuickTime text atoms of a rewritten udta from its rendered payload,
// as a fresh parse reads them. Their refs are payload-relative: project reads only each atom's
// name and entries.
func udtaTextsIn(reg udtaRegion) []udtaText {
	var texts []udtaText
	for _, k := range walkUdta(reg.udtaPayload) {
		lo, hi := k.offset+k.headerLen, k.offset+k.size
		if lo < 0 || hi < lo || hi > int64(len(reg.udtaPayload)) {
			continue
		}
		if u, ok := decodeUdtaText(refOf(k), reg.udtaPayload[lo:hi]); ok {
			texts = append(texts, u)
		}
	}
	return foldUdtaTextDups(texts)
}

// chplRoundTrip simulates the chpl encode->decode round trip (a start rounded to the
// 100 ns chpl unit, a title trimmed to the chpl byte cap, ends filled from the next
// start), so it equals decodeChpl(renderChpl(chapters)).
func chplRoundTrip(chapters []core.Chapter) []core.Chapter {
	if len(chapters) == 0 {
		return nil
	}
	out := make([]core.Chapter, len(chapters))
	for i, ch := range chapters {
		out[i] = core.Chapter{
			Start: scaleToDuration(durationToUnits(ch.Start, chplStartUnit), chplStartUnit),
			Title: truncateUTF8(ch.Title, titleByteMax),
		}
	}
	core.FillInteriorEnds(out)
	return out
}

// walkUdta parses the rendered udta payload (a small in-memory buffer) into its
// atom tree so the result builder can find its text atoms the same way a fresh
// parse would.
func walkUdta(payload []byte) []node {
	nodes, err := walkAtoms(core.BytesSource(payload), 0, int64(len(payload)),
		bits.NewDepth(bits.DefaultLimits.MaxDepth), maxMetaChunk, true)
	if err != nil {
		return nil
	}
	return nodes
}

// udtaCleanLen returns the length of a udta payload up to the end of its last complete
// child atom, excluding any tolerated trailing zero (QuickTime terminates its user-data
// list with a 32-bit zero, and parse keeps that padding).
func udtaCleanLen(payload []byte) int64 {
	nodes, err := walkAtoms(core.BytesSource(payload), 0, int64(len(payload)),
		bits.NewDepth(bits.DefaultLimits.MaxDepth), maxMetaChunk, false)
	if err != nil {
		return int64(len(payload))
	}
	if len(nodes) == 0 {
		return 0
	}
	return nodes[len(nodes)-1].end()
}
