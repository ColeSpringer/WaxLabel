package mp4

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxChplChapters is the Nero chpl chapter cap: the count is an 8-bit field, so a
// list longer than this cannot be written. The editor rejects it at Prepare.
const maxChplChapters = 255

// planChapters computes the rewrite when the chapter list changed. When the file
// has an audio track, it rebuilds the QuickTime chapter track too (planChaptersQT).
// Otherwise it falls back to the chpl-only path below, which rewrites
// the whole moov.udta as one contiguous region - splicing the new ilst and chpl
// byte ranges into the preserved udta bytes - so a chpl resize and an ilst resize
// fold into a single delta the existing chunk-offset machinery consumes unchanged.
// In that fallback an unrewritable QuickTime track is preserved and flagged stale.
func planChapters(d *doc, edited *core.Media, needIlst bool, opts core.WriteOptions, report core.WriteReport) (*core.WritePlan, error) {
	if d.udta != nil && d.udtaRaw == nil {
		return nil, fmt.Errorf("%w: MP4 udta bytes were not captured for a chapter rewrite", waxerr.ErrInvalidData)
	}

	// When the file has an audio track to anchor a chapter text track to, rebuild
	// the QuickTime chapter track alongside the chpl so iTunes and Apple Books see
	// edits too. audioMdiaOff (the tref insertion point) is always set for
	// a resolved audio track; requiring it guards a malformed track from a bad
	// insert. The QuickTime path applies when it can rebuild/remove an existing
	// chapter track, create one (a free track id exists), or strip a dangling tref
	// "chap" on a clear. The chpl-only path below is the fallback (no audio track).
	if d.audioTrak != nil && d.audioMdiaOff > 0 {
		writing := len(edited.Chapters) > 0
		if d.chapTrak != nil || (writing && d.nextTrackID > 0) || (!writing && d.audioHasChap) {
			return planChaptersQT(d, edited, needIlst, opts, report)
		}
	}

	newItems, reg, err := buildChapterUdta(d, edited, needIlst, opts)
	if err != nil {
		return nil, err
	}

	delta := int64(len(reg.regionBytes)) - (reg.regionEnd - reg.regionStart)
	total := d.size + delta
	if err := checkSizes(reg.ancestors, delta); err != nil {
		return nil, err
	}
	if 8+int64(len(reg.udtaPayload)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: udta atom would exceed the 4 GiB 32-bit size limit", waxerr.ErrSizeTooLarge)
	}

	edits := []edit{{off: reg.regionStart, oldLen: reg.regionEnd - reg.regionStart, lit: reg.regionBytes}}
	if delta != 0 {
		for _, anc := range reg.ancestors {
			edits = append(edits, sizePatch(anc, delta))
		}
		for _, t := range d.offTables {
			e, err := offsetPatch(t, delta, reg.regionStart)
			if err != nil {
				return nil, err
			}
			edits = append(edits, e)
		}
	}
	segs, err := assemble(edits, d.size)
	if err != nil {
		return nil, err
	}

	report.BytesAfter = total
	report.PaddingAfter = reg.freeContent
	report.Operations = chapterOps(d, edited, needIlst, delta)
	if n := truncatedTitleCount(edited.Chapters); n > 0 {
		report.Warnings = core.Warn(report.Warnings, core.WarnChapterTitleTruncated,
			fmt.Sprintf("%d chapter title(s) trimmed to %d bytes (the Nero chpl length prefix is one byte)", n, titleByteMax))
	}
	// No WarnChaptersStale here: this fallback runs only when there is no audio
	// track to anchor a QuickTime chapter track to, in which case the file has no
	// QuickTime chapter track to leave stale (decoding one requires an audio track).

	resultItems := d.items // ilst unchanged: keep the parsed items verbatim
	if needIlst {
		resultItems = newItems
	}
	result := buildChapterResult(edited, d, resultItems, reg, delta, total)
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// buildChapterUdta renders the new ilst (when tags or pictures changed) and the
// udta region (the chpl, and the ilst spliced in) that both chapter-write paths -
// the chpl-only fallback and the QuickTime path - start from.
func buildChapterUdta(d *doc, edited *core.Media, needIlst bool, opts core.WriteOptions) ([]item, udtaRegion, error) {
	var newItems []item
	var newIlst []byte
	if needIlst {
		newItems = buildItems(edited.Tags, edited.Pictures, preservedItems(d.items))
		var payload []byte
		for _, it := range newItems {
			payload = append(payload, itemBytes(it)...)
		}
		newIlst = renderAtom(atomName("ilst"), payload)
	}
	reg, err := buildUdtaRegion(d, newIlst, needIlst, edited.Chapters, opts)
	return newItems, reg, err
}

// udtaRegion is the resolved placement of the rewritten user-data box: the source
// span it replaces (or the insertion point when udta is created), the bytes that
// replace it, the new udta payload (re-walked to recover child offsets), and the
// enclosing atoms whose sizes grow.
type udtaRegion struct {
	regionStart, regionEnd int64
	regionBytes            []byte
	udtaPayload            []byte
	udtaOff                int64
	udtaHeaderLen          int64
	ancestors              []atomRef
	freeContent            int64
}

// buildUdtaRegion produces the new moov.udta: it splices the new ilst (when tags
// or pictures changed) and the new chpl into the preserved udta bytes, creating
// the wrappers when they are absent and dropping the chpl when the chapter list
// is cleared.
func buildUdtaRegion(d *doc, newIlst []byte, needIlst bool, chapters []core.Chapter, opts core.WriteOptions) (udtaRegion, error) {
	pad := clampPadding(opts.Padding)
	hasIlst := needIlst && len(newIlst) > 8 // an empty ilst is just its 8-byte header
	needChpl := len(chapters) > 0

	if d.udta == nil {
		var payload []byte
		var freeContent int64
		if hasIlst {
			region, fc := fitIlst(newIlst, 0, pad)
			freeContent = fc
			payload = append(payload, renderFullBox(atomName("meta"), append(hdlrAtom(), region...))...)
		}
		if needChpl {
			payload = append(payload, renderChpl(d.chplVersion, chapters)...)
		}
		at := d.moov.end()
		reg := udtaRegion{regionStart: at, regionEnd: at, udtaOff: at, udtaHeaderLen: 8, ancestors: []atomRef{*d.moov}}
		if len(payload) == 0 {
			return reg, nil // nothing to write (e.g. clearing QuickTime-only chapters)
		}
		reg.regionBytes = renderAtom(atomName("udta"), payload)
		reg.udtaPayload = payload
		reg.freeContent = freeContent
		return reg, nil
	}

	ups := d.udta.offset + d.udta.headerLen
	var reps []byteRep
	var appends []byte
	var freeContent int64

	if needIlst {
		switch {
		case d.ilst != nil:
			startR, endR := ilstRegionRel(d, ups)
			region, fc := fitIlst(newIlst, endR-startR, pad)
			freeContent = fc
			reps = append(reps, byteRep{start: startR, oldLen: endR - startR, repl: region})
			reps = append(reps, metaSizeRep(d, ups, d.meta.size+int64(len(region))-(endR-startR)))
		case d.meta != nil:
			region, fc := fitIlst(newIlst, 0, pad)
			freeContent = fc
			insR := d.meta.end() - ups
			reps = append(reps, byteRep{start: insR, oldLen: 0, repl: region})
			reps = append(reps, metaSizeRep(d, ups, d.meta.size+int64(len(region))))
		case hasIlst:
			region, fc := fitIlst(newIlst, 0, pad)
			freeContent = fc
			appends = append(appends, renderFullBox(atomName("meta"), append(hdlrAtom(), region...))...)
		}
	}

	switch {
	case d.chpl != nil && needChpl:
		reps = append(reps, byteRep{start: d.chpl.offset - ups, oldLen: d.chpl.size, repl: renderChpl(d.chplVersion, chapters)})
	case d.chpl != nil: // cleared: drop the chpl
		reps = append(reps, byteRep{start: d.chpl.offset - ups, oldLen: d.chpl.size})
	case needChpl: // no existing chpl: append a new one
		appends = append(appends, renderChpl(d.chplVersion, chapters)...)
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
		udtaOff: d.udta.offset, udtaHeaderLen: 8,
		ancestors: []atomRef{*d.moov}, freeContent: freeContent,
	}
	if len(payload) == 0 {
		// The udta became empty (e.g. ClearChapters on a chpl-only udta): drop the
		// whole atom rather than leave an empty 8-byte udta, so the result's nil
		// udta matches the bytes and a later edit does not create a second udta.
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
// covered by a replacement - so udta siblings and meta children outside the
// ilst/chpl ranges survive a chapter rewrite verbatim.
func spliceBytes(src []byte, reps []byteRep) ([]byte, error) {
	sort.Slice(reps, func(i, j int) bool { return reps[i].start < reps[j].start })
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
	s, e := d.ilst.offset, d.ilst.end()
	if d.free != nil {
		s = min(s, d.free.offset)
		e = max(e, d.free.end())
	}
	return s - ups, e - ups
}

// metaSizeRep returns the replacement that rewrites the meta atom's size field
// (32- or 64-bit) to newSize, relative to the udta payload start.
func metaSizeRep(d *doc, ups, newSize int64) byteRep {
	start := d.meta.offset - ups
	if d.meta.headerLen == 16 {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(newSize))
		return byteRep{start: start + 8, oldLen: 8, repl: b[:]}
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(newSize))
	return byteRep{start: start, oldLen: 4, repl: b[:]}
}

// fitIlst places the new ilst within a region of oldRegionLen bytes, reusing the
// surplus as free padding when it fits in place and falling back to fresh padding
// otherwise - the same rule planLayout uses, so chapter and tag edits leave the
// same in-place slack. It returns the bytes and the free payload length.
func fitIlst(newIlst []byte, oldRegionLen, pad int64) ([]byte, int64) {
	leftover := oldRegionLen - int64(len(newIlst))
	switch {
	case leftover == 0:
		return newIlst, 0
	case leftover >= 8:
		b, _, _, fc := appendFree(newIlst, leftover-8)
		return b, fc
	default:
		b, _, _, fc := appendFree(newIlst, pad)
		return b, fc
	}
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
func chapterOps(d *doc, edited *core.Media, needIlst bool, delta int64) []string {
	var ops []string
	switch {
	case len(edited.Chapters) == 0:
		ops = append(ops, "removed chapters (chpl)")
	case d.chpl != nil:
		ops = append(ops, fmt.Sprintf("rewrote %d chapters (chpl)", len(edited.Chapters)))
	default:
		ops = append(ops, fmt.Sprintf("wrote %d chapters (chpl)", len(edited.Chapters)))
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

// buildChapterResult constructs the post-write Media for a chapter rewrite. It
// recovers the new ilst/chpl/meta atom offsets by re-walking the rendered udta
// payload (so they equal a fresh parse), and shifts the chunk-offset tables, mdat
// ranges, and top-level layout by the single combined delta.
func buildChapterResult(edited *core.Media, base *doc, items []item, reg udtaRegion, delta, total int64) *core.Media {
	// The result's chapter view must equal a fresh parse of the written bytes: the
	// chpl we wrote round-trips through its 100 ns / 255-byte encoding, and a
	// preserved QuickTime track still wins the projection (shadowing the edit).
	resultChapters, chplCount, conflict := chapterResultView(base, edited.Chapters)
	nd := &doc{
		size:            total,
		cfg:             base.cfg,
		track:           base.track,
		majorBrand:      base.majorBrand,
		items:           items,
		chapters:        resultChapters,
		chplVersion:     base.chplVersion,
		chplCount:       chplCount,
		hasQTChapters:   base.hasQTChapters,
		chapterConflict: conflict,
		udtaRaw:         reg.udtaPayload,
	}
	shiftStructure(nd, base, reg.regionStart, reg.regionEnd, delta)
	carryChapterRefs(nd, base, reg.regionEnd, delta)

	// Recover the new meta/ilst/free/chpl offsets by re-walking the rendered udta
	// payload, so they equal what a fresh parse would find.
	if len(reg.udtaPayload) > 0 {
		nd.udta = &atomRef{name: atomName("udta"), offset: reg.udtaOff, headerLen: reg.udtaHeaderLen,
			size: reg.udtaHeaderLen + int64(len(reg.udtaPayload))}
		ups := reg.udtaOff + reg.udtaHeaderLen
		for _, k := range walkUdta(reg.udtaPayload) {
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

	tags, pics, families, numericGenre := project(nd)
	out := &core.Media{
		Format:     core.FormatMP4,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
		Chapters:   nd.chapters,
		Families:   families,
		Warnings:   chapterWarnings(mediaWarnings(tags, numericGenre), conflict),
		Native:     nd,
		Identity:   core.Identity{Size: total},
	}
	setEssence(nd, out)
	return out
}

// chplRoundTrip simulates the chpl encode->decode round trip - a start rounded to
// the 100 ns chpl unit, a title trimmed to the chpl byte cap, ends filled from
// the next start - so it equals decodeChpl(renderChpl(chapters)). That lets a
// result document mirror a fresh parse of its own bytes without re-reading them.
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
	fillChapterEnds(out)
	return out
}

// chapterResultView returns the chapters and source-conflict a fresh parse of a
// chapter-edited file would yield, plus the chpl-specific count. A preserved
// QuickTime track is preferred on reparse, so the written chpl is shadowed and a
// disagreement surfaces as a conflict (the file is genuinely inconsistent until
// the QuickTime track is rewritten).
func chapterResultView(base *doc, written []core.Chapter) (chapters []core.Chapter, chplCount int, conflict bool) {
	chpl := chplRoundTrip(written)
	if base.hasQTChapters {
		qt := core.CloneChapters(base.chapters) // the preserved QuickTime track
		return qt, len(chpl), len(chpl) > 0 && !chaptersAgree(chpl, qt)
	}
	return chpl, len(chpl), false
}

// chapterWarnings appends the parse-time chapter-source-conflict to a warning set
// when the written file's chpl and preserved QuickTime track disagree, so the
// result document's warnings match a fresh parse.
func chapterWarnings(ws []core.Warning, conflict bool) []core.Warning {
	if conflict {
		ws = core.Warn(ws, core.WarnChapterSourceConflict,
			"the Nero chpl list and the QuickTime chapter text track disagree")
	}
	return ws
}

// walkUdta parses the rendered udta payload (a small in-memory buffer) into its
// atom tree so the result builder can locate the new meta/ilst/chpl offsets the
// same way a fresh parse would.
func walkUdta(payload []byte) []node {
	nodes, err := walkAtoms(core.BytesSource(payload), 0, int64(len(payload)),
		bits.NewDepth(bits.DefaultLimits.MaxDepth), maxMetaChunk, true)
	if err != nil {
		return nil
	}
	return nodes
}

// atomRefAt converts an in-payload node to an absolute atomRef given the payload
// start offset.
func atomRefAt(n node, base int64) atomRef {
	return atomRef{name: n.name, offset: base + n.offset, headerLen: n.headerLen, size: n.size}
}

// udtaCleanLen returns the length of a udta payload up to the end of its last
// complete child atom, excluding any tolerated trailing zero (QuickTime
// terminates its user-data list with a 32-bit zero, and parse keeps that
// padding). A new child must be inserted/appended at this offset - not the
// payload's raw end - or the zero tail shifts it out of alignment and corrupts
// the re-parse of the output. An all-zero payload yields 0; a payload walkAtoms
// unexpectedly rejects (parse already accepted it) yields its full length, so no
// real bytes are dropped.
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
