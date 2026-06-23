package mp4

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan computes the byte-level rewrite that turns the original MP4 into the
// edited media. It is preservation-first: only the iTunes tag list (ilst) is
// re-rendered, and a neighbouring free padding atom absorbs the size change so
// the media data usually does not move at all (delta == 0, no offset fixups).
// When the new tag list cannot fit the available padding, the enclosing
// moov/udta/meta atom sizes are patched and every track's stco/co64 chunk-offset
// table is shifted so the media stays playable - no atom is reordered and the
// mdat bytes are copied verbatim.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("mp4: edited media has no MP4 native document")
	}

	tagsChanged := !base.Tags.Equal(edited.Tags)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	report := core.WriteReport{Format: core.FormatMP4, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy (so SaveAsFile/
	// WriteTo still produce a whole file) flagged NoOp so SaveBack skips it.
	if !tagsChanged && !picturesChanged && !chaptersChanged {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	if d.moov == nil {
		return nil, fmt.Errorf("%w: MP4 has no moov box to write tags into", waxerr.ErrInvalidData)
	}
	if len(edited.Chapters) > maxChplChapters {
		return nil, fmt.Errorf("%w: %d chapters exceeds the %d a Nero chpl can store",
			waxerr.ErrUnsupportedTag, len(edited.Chapters), maxChplChapters)
	}
	if picturesChanged {
		if err := checkCoverFormats(edited.Pictures); err != nil {
			return nil, err
		}
		if core.PicturesLoseMetadata(edited.Pictures, core.PictureLossRoleAndDescription) {
			report.Warnings = core.Warn(report.Warnings, core.WarnPictureMetadataDropped,
				"MP4 stores cover art as image data only; picture role and description are not preserved (covers are read back as front cover)")
		}
	}

	// Surface canonical numeric values the iTunes atoms cannot represent and the
	// encoder therefore drops (trkn/disk are uint16; stik is uint32) as a plan warning
	// per dropped key, so the loss is visible before the write rather than vanishing
	// with exit 0 (F1). droppedValues reads the same raw canonical strings the encoder
	// consumes, so it cannot desync from what buildItems drops; both the ilst and the
	// chapter rewrite paths build the ilst from edited.Tags, so computing it here (before
	// the branch, into the shared report) covers both.
	for _, dv := range droppedValues(edited.Tags) {
		report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueDropped,
			fmt.Sprintf("%s value %q cannot be represented in this format and was dropped", dv.Key, dv.Value),
			dv.Key)
	}

	// A chapter edit rewrites the whole moov.udta (folding any ilst change into one
	// delta); a tag/picture-only edit keeps the lighter in-place ilst path.
	if chaptersChanged {
		return planChapters(d, edited, tagsChanged || picturesChanged, opts, report)
	}

	// Re-render the ilst from the edited canonical set, keeping the preserved
	// items (unknown atoms, foreign freeforms) verbatim.
	newItems := buildItems(edited.Tags, edited.Pictures, preservedItems(d.items))
	var ilstPayload []byte
	for _, it := range newItems {
		ilstPayload = append(ilstPayload, itemBytes(it)...)
	}
	newIlst := renderAtom(atomName("ilst"), ilstPayload)

	lay, err := planLayout(d, newIlst, opts)
	if err != nil {
		return nil, err
	}
	delta := int64(len(lay.regionBytes)) - (lay.regionEnd - lay.regionStart)
	total := d.size + delta
	if err := checkSizes(lay.ancestors, delta); err != nil {
		return nil, err
	}

	edits := []edit{{off: lay.regionStart, oldLen: lay.regionEnd - lay.regionStart, lit: lay.regionBytes}}
	if delta != 0 {
		for _, anc := range lay.ancestors {
			edits = append(edits, sizePatch(anc, delta))
		}
		for _, t := range d.offTables {
			e, err := offsetPatch(t, delta, lay.regionStart)
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
	report.PaddingAfter = lay.freeContent
	report.Operations = operations(d, lay, delta, len(edited.Pictures))

	result := buildResult(edited, d, newItems, lay, delta, total, int64(len(newIlst)))
	// Collapse to a true no-op when the ilst rebuild re-projected to base's values
	// (e.g. TRACKNUMBER=03 -> 3, or an unrepresentable value the encoder dropped); a
	// chapter edit took the planChapters path above, so nothing structural remains here.
	// See core.DowngradeNoOp.
	if np := core.DowngradeNoOp(core.FormatMP4, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), false); np != nil {
		// Carry the input-rejection warnings through the downgrade: the edit produced no
		// net byte change, but the user's value or cover description was still rejected and
		// must be surfaced (and --strict still escalates value-dropped). MP4 stamps only
		// these input-rejection warnings (value-dropped, picture-metadata-dropped) before
		// this point - not a write-characteristic warning a verbatim no-op would make moot
		// (as ID3MultiValue is for the other codecs) - so carrying the whole set is safe.
		// DowngradeNoOp builds a fresh report, so re-attach.
		np.Report.Warnings = append(np.Report.Warnings, report.Warnings...)
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// checkCoverFormats rejects a cover whose image format an MP4 covr atom cannot
// label. Only JPEG, PNG, and BMP have type codes; another format (WebP, GIF, ...)
// would be stored with a JPEG type flag over non-JPEG bytes - a corrupt cover -
// so fail loudly here rather than silently mislabel it.
func checkCoverFormats(pics []core.Picture) error {
	for _, p := range pics {
		if !coverMIMESupported(p.MIME) {
			return fmt.Errorf("%w: MP4 cover art must be JPEG, PNG, or BMP (got %q)",
				waxerr.ErrUnsupportedTag, p.MIME)
		}
	}
	return nil
}

// preservedItems returns the items the canonical rebuild does not own (unknown
// atoms, foreign-mean freeforms, parse failures), to be kept verbatim.
func preservedItems(items []item) []item {
	var out []item
	for _, it := range items {
		if !owned(it) {
			out = append(out, it)
		}
	}
	return out
}

// layout is the resolved placement of the rewritten tag region: the source span
// it replaces, the literal bytes that replace it, the enclosing atoms whose sizes
// grow, and where the new ilst/free land within the replacement bytes.
type layout struct {
	regionStart, regionEnd int64
	regionBytes            []byte
	ancestors              []atomRef
	ilstOff                int64 // offset of the new ilst atom within regionBytes
	freeOff                int64 // offset of the new free atom within regionBytes, or -1
	freeLen                int64 // total length of the new free atom (0 if none)
	freeContent            int64 // free atom payload length (padding bytes)
	created                bool  // true when the tag path was created (no prior ilst)
}

// planLayout decides how to place the new ilst: reuse the existing ilst+free
// region in place when it fits (delta 0, media never moves), grow it with fresh
// padding when it does not, or create the moov/udta/meta/ilst path when the file
// had no tags.
func planLayout(d *doc, newIlst []byte, opts core.WriteOptions) (layout, error) {
	newLen := int64(len(newIlst))
	pad := opts.Padding.ClampTarget()

	if d.ilst != nil {
		regionStart := d.ilst.offset
		regionEnd := d.ilst.end()
		if d.free != nil {
			regionStart = min(regionStart, d.free.offset)
			regionEnd = max(regionEnd, d.free.end())
		}
		regionLen := regionEnd - regionStart
		leftover := regionLen - newLen
		// floor is the --padding "reserve at least N" minimum (PaddingPolicy.Min); a
		// reuse-in-place path may only keep the existing region while its leftover
		// padding still meets it, otherwise the region must grow. Min defaults to 0,
		// leaving the prior reuse behavior unchanged. Mirrors the FLAC/ID3 reuse floor.
		floor := opts.Padding.Min

		lay := layout{regionStart: regionStart, regionEnd: regionEnd, ilstOff: 0, freeOff: -1}
		lay.ancestors = []atomRef{*d.moov, *d.udta, *d.meta}
		switch {
		case leftover == 0 && floor <= 0:
			// Exact fit: just the ilst, no padding - but only when no floor is requested
			// (a zero-leftover region cannot satisfy a positive floor).
			lay.regionBytes = newIlst
		case leftover >= 8 && leftover-8 >= floor:
			// Fits with room for a free atom whose padding still meets the floor: reuse
			// the region in place (delta 0).
			lay.regionBytes, lay.freeOff, lay.freeLen, lay.freeContent = appendFree(newIlst, leftover-8)
		default:
			// Does not fit, leaves a 1-7 byte remainder a free atom cannot represent, or
			// the leftover would fall below the floor: grow with fresh padding (ClampTarget
			// floors it to Min) so a later edit fits in place again.
			lay.regionBytes, lay.freeOff, lay.freeLen, lay.freeContent = appendFree(newIlst, pad)
		}
		return lay, nil
	}

	// No ilst: create the missing path. Insert into the deepest existing of
	// moov/udta/meta, at its end (the new atom becomes that container's last child;
	// everything after shifts and the ancestor sizes grow).
	inner, withFree, ilstOff, freeOff, freeLen, freeContent := buildCreated(d, newIlst, pad)
	at := inner.end()
	regionStart := at
	// When inserting into an existing udta, place the new atom after its last
	// complete child rather than at its raw end. A udta body can carry a tolerated
	// trailing zero (QuickTime terminates its user-data list with a 32-bit zero;
	// parse keeps it), and an atom appended after those zeros is shifted out of
	// alignment, corrupting every following atom on re-parse. Replacing
	// [clean, end) drops the stray tail. d.udtaRaw is nil only for an oversized
	// udta, where the append-at-end behavior is kept.
	if inner.name == atomName("udta") && d.udtaRaw != nil {
		if clean := d.udta.offset + d.udta.headerLen + udtaCleanLen(d.udtaRaw); clean < at {
			regionStart = clean
		}
	}
	return layout{
		regionStart: regionStart, regionEnd: at, regionBytes: withFree,
		ancestors: createdAncestors(d), ilstOff: ilstOff,
		freeOff: freeOff, freeLen: freeLen, freeContent: freeContent, created: true,
	}, nil
}

// buildCreated renders the atom(s) to insert when the file had no ilst, choosing
// the wrapper depth from which containers already exist. It returns the
// innermost existing container to insert into, the bytes to insert, and where
// the ilst/free land within those bytes.
func buildCreated(d *doc, newIlst []byte, pad int64) (inner atomRef, bytes []byte, ilstOff, freeOff, freeLen, freeContent int64) {
	ilstAndFree, fOff, fLen, fContent := appendFree(newIlst, pad)
	switch {
	case d.meta != nil:
		// Append ilst(+free) directly inside the existing meta.
		return *d.meta, ilstAndFree, 0, fOff, fLen, fContent
	case d.udta != nil:
		metaInner := append(hdlrAtom(), ilstAndFree...)
		meta := renderFullBox(atomName("meta"), metaInner)
		base := metaPrefix() + len(hdlrAtom())
		return *d.udta, meta, int64(base), int64(base) + fOff, fLen, fContent
	default:
		metaInner := append(hdlrAtom(), ilstAndFree...)
		meta := renderFullBox(atomName("meta"), metaInner)
		udta := renderAtom(atomName("udta"), meta)
		base := 8 + metaPrefix() + len(hdlrAtom()) // udta header + meta prefix + hdlr
		return *d.moov, udta, int64(base), int64(base) + fOff, fLen, fContent
	}
}

// createdAncestors returns the existing atoms (moov plus udta/meta if present)
// whose sizes grow when a new tag path is inserted.
func createdAncestors(d *doc) []atomRef {
	out := []atomRef{*d.moov}
	if d.udta != nil {
		out = append(out, *d.udta)
	}
	if d.meta != nil {
		out = append(out, *d.meta)
	}
	return out
}

// appendFree appends a free atom of the given payload length to ilst bytes,
// returning the combined bytes and the free atom's offset/total/payload sizes. A
// non-positive payload yields no free atom.
func appendFree(ilst []byte, content int64) (combined []byte, freeOff, freeLen, freeContent int64) {
	if content <= 0 {
		return ilst, -1, 0, 0
	}
	free := renderAtom(atomName("free"), make([]byte, content))
	combined = append(slices.Clone(ilst), free...)
	return combined, int64(len(ilst)), int64(len(free)), content
}

// hdlrAtom builds the iTunes metadata handler atom required inside a freshly
// created meta box ("mdir"/"appl"), matching what iTunes writes.
func hdlrAtom() []byte {
	payload := make([]byte, 0, 25)
	payload = append(payload, make([]byte, 8)...) // version/flags + pre_defined
	payload = append(payload, "mdirappl"...)      // handler_type "mdir" + "appl"
	payload = append(payload, make([]byte, 9)...) // reserved + empty name
	return renderAtom(atomName("hdlr"), payload)
}

// renderFullBox wraps content in an atom with a leading 4-byte version/flags
// field (a FullBox, as "meta" requires).
func renderFullBox(name [4]byte, content []byte) []byte {
	return renderAtom(name, append(make([]byte, metaSkip), content...))
}

// metaPrefix is the byte distance from a meta atom's start to its first child:
// the 8-byte header plus the 4-byte version/flags.
func metaPrefix() int { return 8 + metaSkip }

// edit is one byte-range replacement in the rewrite: replace oldLen source bytes
// at off with lit. Most edits are same-length patches (atom sizes, offset
// tables); the tag region is a resize.
type edit struct {
	off    int64
	oldLen int64
	lit    []byte
}

// sizePatch rewrites an enclosing atom's size field by delta, handling both
// 32-bit and 64-bit (size == 1) atom headers.
func sizePatch(anc atomRef, delta int64) edit {
	if anc.headerLen == 16 {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(anc.size+delta))
		return edit{off: anc.offset + 8, oldLen: 8, lit: b[:]}
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(anc.size+delta))
	return edit{off: anc.offset, oldLen: 4, lit: b[:]}
}

// checkSizes fails loudly if a 32-bit enclosing atom's size field would overflow
// after adding delta (a >4 GiB moov, which would need a 64-bit rewrite this
// version does not do). Only a grow (delta > 0) can overflow; a shrink only makes
// the size smaller, so it is always safe.
func checkSizes(ancestors []atomRef, delta int64) error {
	for _, anc := range ancestors {
		if anc.headerLen == 8 && anc.size+delta > math.MaxUint32 {
			return fmt.Errorf("%w: atom %q would exceed the 4 GiB 32-bit size limit",
				waxerr.ErrSizeTooLarge, anc.name)
		}
	}
	return nil
}

// offsetPatch re-renders a chunk-offset table with every entry past the
// insertion point shifted by delta, so the media chunks resolve to their new
// positions after the metadata grew.
func offsetPatch(t offsetTable, delta, insertion int64) (edit, error) {
	width := 4
	if t.co64 {
		width = 8
	}
	buf := make([]byte, len(t.entries)*width)
	for i, e := range t.entries {
		e = shiftOffset(e, insertion, delta)
		if t.co64 {
			binary.BigEndian.PutUint64(buf[i*8:], e)
		} else {
			if e > math.MaxUint32 {
				return edit{}, fmt.Errorf("%w: a chunk offset would exceed 4 GiB; the file needs a 64-bit (co64) table",
					waxerr.ErrSizeTooLarge)
			}
			binary.BigEndian.PutUint32(buf[i*4:], uint32(e))
		}
	}
	entriesOff := t.offset + t.headerLen + 8 // after the 4-byte version/flags and 4-byte count
	return edit{off: entriesOff, oldLen: int64(len(t.entries) * width), lit: buf}, nil
}

// shiftOffset moves a chunk offset that lies past the insertion point by delta,
// so the media chunk resolves to its new position after the metadata changed
// size. delta is usually a grow (positive) but can be a small shrink (negative)
// - a just-smaller tag list written with zero padding leaves a 1-7 byte gap a
// free atom cannot fill - so the adjustment is signed. The same rule is used to
// rewrite the offset bytes and to update the returned document, so the two
// cannot disagree.
func shiftOffset(e uint64, insertion, delta int64) uint64 {
	if e > uint64(insertion) {
		return uint64(int64(e) + delta)
	}
	return e
}

// assemble turns the sorted, disjoint edits into a rewrite segment list: copy
// the gaps from the source, emit each edit's literal bytes.
func assemble(edits []edit, size int64) ([]bits.Segment, error) {
	sort.Slice(edits, func(i, j int) bool { return edits[i].off < edits[j].off })
	var segs []bits.Segment
	pos := int64(0)
	for _, e := range edits {
		if e.off < pos {
			return nil, fmt.Errorf("%w: overlapping MP4 rewrite edits at %d", waxerr.ErrInvalidData, e.off)
		}
		if e.off > pos {
			segs = append(segs, bits.Copy(pos, e.off-pos))
		}
		segs = append(segs, bits.Lit(e.lit))
		pos = e.off + e.oldLen
	}
	if pos > size {
		return nil, fmt.Errorf("%w: MP4 rewrite edit runs past EOF", waxerr.ErrInvalidData)
	}
	if pos < size {
		segs = append(segs, bits.Copy(pos, size-pos))
	}
	return segs, nil
}

// operations describes the rewrite for the report.
func operations(d *doc, lay layout, delta int64, pics int) []string {
	var ops []string
	switch {
	case lay.created:
		ops = append(ops, "moov.udta.meta.ilst creation")
	case delta == 0:
		ops = append(ops, "ilst rewrite in place (media not moved)")
	default:
		ops = append(ops, fmt.Sprintf("ilst rewrite (+%d bytes metadata)", delta))
		ops = append(ops, fmt.Sprintf("%d chunk-offset table shift(s)", len(d.offTables)))
	}
	if pics > 0 {
		ops = append(ops, fmt.Sprintf("pictures: %d", pics))
	}
	return ops
}
