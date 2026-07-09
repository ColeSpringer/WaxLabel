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
	// WriteTo still produce a whole file) flagged NoOp so SaveBack skips it. Explicit
	// padding requests run the layout below so a grow-only MP4 padding edit can take
	// effect when it enlarges the moov region.
	if !tagsChanged && !picturesChanged && !chaptersChanged && !opts.PaddingExplicit {
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

	// Surface canonical values the iTunes atoms cannot represent. droppedValues names the ones
	// genuinely lost (trkn/disk outside uint16, a non-numeric stik), coercedValues the ones
	// stored in a normalized form (a non-boolean cpil written as 0). Both read the same raw
	// canonical strings the encoder consumes, and both the ilst and chapter rewrite paths build
	// from edited.Tags, so the shared report covers both.
	for _, dv := range droppedValues(edited.Tags) {
		msg := fmt.Sprintf("%s value %q cannot be represented in this format and was dropped", dv.Key, dv.Value)
		if dv.ZeroUnset {
			// The 0 bytes ARE written (0/N), but decodePair treats a 0 slot as unset and reads it
			// back as absent, so this is a round-trip loss, not an unrepresentable value - say so
			// rather than the misleading "cannot be represented ... was dropped".
			msg = fmt.Sprintf("%s value %q is treated as unset in this format and reads back as absent", dv.Key, dv.Value)
		}
		report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueDropped, msg, dv.Key)
	}
	for _, cv := range coercedValues(edited.Tags) {
		// The wording follows the coercion kind. It gates on Normalized, the field the numeric
		// message prints, rather than on a specific key, so a coercion added to coercedValues later
		// cannot slip into the numeric branch and print an empty stored value. A trkn/disk number
		// carries its stored integer in Normalized, so show it: a bare "normalized" note would leave
		// the same "what did it store?" ambiguity this change is meant to remove. A coercion with no
		// Normalized (COMPILATION, which stores a non-boolean as 0/false) keeps the boolean wording.
		// This mirrors the ZeroUnset message-switch on the drop path above.
		msg := fmt.Sprintf("%s value %q is not a valid boolean and was stored as 0 (false)", cv.Key, cv.Value)
		if cv.Normalized != "" {
			msg = fmt.Sprintf("%s value %q was stored as %s (numbers are kept as integers)", cv.Key, cv.Value, cv.Normalized)
		}
		report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueCoerced, msg, cv.Key)
	}

	// A trkn/disk number is a fixed binary uint16, so an edit that makes a slot genuinely
	// unstorable would clear it and erase a good existing value. When base still holds a
	// storable value for that slot, restore it so the edit does not silently delete good data.
	// This runs after the warning passes above (which read the pre-restore edited.Tags, so the
	// value-dropped warning still fires) and before every downstream build, so buildItems, the
	// chapter path, and buildResult all see the restored value. Restoring base's value makes an
	// edit that changed nothing else collapse to a true no-op via DowngradeNoOp below, which
	// carries the warning forward so --strict still escalates. This diverges from the text
	// formats (MP3/AAC/AIFF/WAV), which store the raw string; the help/README note the reason.
	if patched, restored := restoreUnstorablePairSlots(base.Tags, edited.Tags); restored {
		ec := *edited
		ec.Tags = patched
		edited = &ec
	}

	// A chapter edit rewrites the whole moov.udta (folding any ilst change into one
	// delta); a tag/picture-only edit keeps the lighter in-place ilst path.
	if chaptersChanged {
		pl, err := planChapters(d, edited, tagsChanged || picturesChanged, picturesChanged, opts, report)
		if err != nil || pl == nil {
			return pl, err
		}
		// Collapse a chapter edit that re-projected to base's exact chapters/tags/
		// pictures to a true no-op, so re-applying an identical list does not churn the
		// file (new inode -> broken hard links, bumped mtime) on a byte-identical write.
		// --add-chapter builds chapters with End==0 while a parse derives End, so
		// core.EqualChapters always reports a change and this plan path never self-reports
		// NoOp; pl.Result is the round-tripped read view (it equals a fresh reparse of the
		// output, by qtWriteRoundTrip/chplRoundTrip design, now including the recovered
		// last-chapter end), so it is the honest basis for the comparison and a genuine
		// title/start/end edit still differs and writes. (A more general fix would normalize
		// the edited chapter ends at the editor boundary, but that is an adjacent change
		// spanning Matroska and the 100 ns rounding + title truncation; the localized
		// build-then-discard stays here.)
		//
		// Refuse to collapse only over a conflicted source: when the file's chpl and QuickTime
		// tables disagreed at parse, re-applying the (preferred) list rewrites the stale table
		// - a real, conflict-resolving change - and DowngradeNoOp does not carry
		// WarnChapterSourceConflict, so it would falsely compare equal. After one such write the
		// file is consistent and a later re-apply collapses normally; WaxLabel-written files are
		// never conflicted. (Before the QuickTime reader recovered the last chapter's end, an
		// explicit final End also had to block the collapse because pl.Result dropped it; the
		// reader now round-trips that end, so a real final-End edit differs in pl.Result on its
		// own and needs no special gate.)
		conflicted := len(core.WarningsWithCode(base.Warnings, core.WarnChapterSourceConflict)) > 0
		if !conflicted {
			// pl.Report.Warnings (not the outer report): planChapters took report by value, so
			// warnings appended during planning live only on pl.Report.
			if np := core.DowngradeNoOp(core.FormatMP4, edited.Identity.Size, base, pl.Result,
				base.Tags.Equal(pl.Result.Tags), false, pl.Report.Warnings); np != nil {
				return np, nil
			}
		}
		return pl, nil
	}

	// Re-render the ilst from the edited canonical set, keeping the preserved
	// items (unknown atoms, foreign freeforms) verbatim. The cover is re-encoded only
	// when the picture set changed; otherwise the parsed covr is carried verbatim so a
	// tag-only edit never rewrites a carried cover through coverType's JPEG default.
	covr := coverItemsToWrite(edited.Pictures, d.items, picturesChanged)
	newItems := buildItems(edited.Tags, covr, preservedItems(d.items), opts.NumericGenre)
	if err := checkBuiltItems(newItems, d.items, opts.Limits.MaxAllocBytes); err != nil {
		return nil, err
	}
	var ilstPayload []byte
	for _, it := range newItems {
		ilstPayload = append(ilstPayload, itemBytes(it)...)
	}
	newIlst := renderAtom(atomName("ilst"), ilstPayload)
	// Guard the aggregate ilst size against the 32-bit box-size field renderAtom/renderData write.
	// checkSizes only guards 8-byte-header ancestors, so it structurally cannot see an ilst wrap
	// inside a 64-bit moov; the created-ilst path additionally guards the fresh meta/udta wrappers
	// in planLayout. Per-item payloads are already capped at MaxAllocBytes by checkBuiltItems, so
	// only the aggregate can reach this ceiling.
	if err := checkBoxSize32(atomName("ilst"), 8+int64(len(ilstPayload))); err != nil {
		return nil, err
	}

	lay, err := planLayout(d, newIlst, opts)
	if err != nil {
		return nil, err
	}
	if lay.paddingClamped {
		report.Warnings = core.Warn(report.Warnings, core.WarnPaddingClamped,
			fmt.Sprintf("requested padding exceeded the %d-byte limit and was clamped to it", maxPadding))
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
	// DowngradeNoOp carries the input-rejection warnings (value-dropped, value-coerced,
	// picture-metadata-dropped) forward, so a rejected/coerced value or cover description still
	// surfaces (and --strict still escalates) even though the write is a no-op. A coerced
	// COMPILATION=maybe re-applied to a file already cpil=0 is exactly this case.
	// delta != 0 means the layout grew the moov region. That makes an explicit padding
	// increase a real structural change, while a reuse-in-place edit with equal tags stays
	// a no-op.
	if np := core.DowngradeNoOp(core.FormatMP4, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), delta != 0, report.Warnings); np != nil {
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
	paddingClamped         bool  // a too-large padding Target was clamped to maxPadding (a free atom was emitted)
}

// maxPadding caps the metadata padding the MP4 writer allocates for a free atom.
// PaddingPolicy.Max == 0 means "no caller ceiling - apply the format's cap", and MP4 free
// atoms have no small structural limit (unlike a FLAC PADDING block's 24-bit length), so
// without this a programmatic WithPadding(Target: huge) would make([]byte, Target) and OOM at
// the library layer (the CLI's 64 MiB cap protects only the CLI). 256 MiB matches the
// library's default allocation ceiling (bits.DefaultLimits.MaxAllocBytes) and sits far above
// any real tag-padding need.
const maxPadding = 256 << 20

// planLayout decides how to place the new ilst: reuse the existing ilst+free
// region in place when it fits (delta 0, media never moves), grow it with fresh
// padding when it does not, or create the moov/udta/meta/ilst path when the file
// had no tags.
func planLayout(d *doc, newIlst []byte, opts core.WriteOptions) (layout, error) {
	newLen := int64(len(newIlst))
	pad := opts.Padding.ClampTarget()
	// Clamp a too-large padding request before it reaches make([]byte, pad). padClamped is
	// reported only by the branches that actually emit fresh padding (grow/create), so a
	// reuse-in-place edit - where pad is unused - never raises a spurious warning.
	padClamped := false
	if pad > maxPadding {
		pad, padClamped = maxPadding, true
	}

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
			lay.paddingClamped = padClamped
		}
		return lay, nil
	}

	// No ilst: create the missing path. Insert into the deepest existing of
	// moov/udta/meta, at its end (the new atom becomes that container's last child;
	// everything after shifts and the ancestor sizes grow).
	inner, withFree, ilstOff, freeOff, freeLen, freeContent, err := buildCreated(d, newIlst, pad)
	if err != nil {
		return layout{}, err
	}
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
		paddingClamped: padClamped,
	}, nil
}

// buildCreated renders the atom(s) to insert when the file had no ilst, choosing
// the wrapper depth from which containers already exist. It returns the
// innermost existing container to insert into, the bytes to insert, and where
// the ilst/free land within those bytes.
func buildCreated(d *doc, newIlst []byte, pad int64) (inner atomRef, bytes []byte, ilstOff, freeOff, freeLen, freeContent int64, err error) {
	ilstAndFree, fOff, fLen, fContent := appendFree(newIlst, pad)
	switch {
	case d.meta != nil:
		// Append ilst(+free) directly inside the existing meta. The ilst is already size-guarded,
		// and the existing meta/udta/moov grow via sizePatch (64-bit-aware) - no fresh wrapper here.
		return *d.meta, ilstAndFree, 0, fOff, fLen, fContent, nil
	case d.udta != nil:
		metaInner := append(hdlrAtom(), ilstAndFree...)
		// The fresh meta is rendered with a 32-bit size field and is never an existing ancestor
		// checkSizes can see, so guard it here. meta total = metaPrefix() + len(metaInner).
		if err := checkBoxSize32(atomName("meta"), int64(metaPrefix()+len(metaInner))); err != nil {
			return atomRef{}, nil, 0, 0, 0, 0, err
		}
		meta := renderFullBox(atomName("meta"), metaInner)
		base := metaPrefix() + len(hdlrAtom())
		return *d.udta, meta, int64(base), int64(base) + fOff, fLen, fContent, nil
	default:
		metaInner := append(hdlrAtom(), ilstAndFree...)
		// The fresh udta wraps the fresh meta; udta is the outermost/largest box, so if it fits the
		// enclosed meta does too. Both are rendered with 32-bit size fields and are invisible to
		// checkSizes. udta total = 8 + meta total = 8 + metaPrefix() + len(metaInner).
		if err := checkBoxSize32(atomName("udta"), int64(8+metaPrefix()+len(metaInner))); err != nil {
			return atomRef{}, nil, 0, 0, 0, 0, err
		}
		meta := renderFullBox(atomName("meta"), metaInner)
		udta := renderAtom(atomName("udta"), meta)
		base := 8 + metaPrefix() + len(hdlrAtom()) // udta header + meta prefix + hdlr
		return *d.moov, udta, int64(base), int64(base) + fOff, fLen, fContent, nil
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

// checkBoxSize32 fails when a freshly-rendered box's total size would overflow the 32-bit
// box-size field renderAtom/renderData/renderFullBox write (they cast size to uint32 unchecked).
// It centralizes the 8-byte-header assumption so the ilst guard, the created-wrapper guards, and
// the chapter udta guard share one definition. It is the counterpart to checkSizes/sizePatch,
// which patch *existing* ancestors (and write a 64-bit field when the header is 64-bit); this
// guards the newly-built boxes those paths never see. totalLen is the whole box (header +
// payload).
func checkBoxSize32(name [4]byte, totalLen int64) error {
	if totalLen > math.MaxUint32 {
		return fmt.Errorf("%w: %s atom would exceed the 4 GiB 32-bit size limit",
			waxerr.ErrSizeTooLarge, string(name[:]))
	}
	return nil
}

// checkItemSizes rejects any ilst item whose payload exceeds the alloc limit - the write-side
// half of the read/write symmetry. readPayloadWhole caps an ilst item read at the same limit,
// so without this guard the writer could emit a cover or freeform it cannot read back. It
// inspects every item buildItems returns - the canonical atoms, the covr, and the preserved/
// unknown items - so an oversized preserved freeform is caught too, not only the cover. An
// oversized covr reports ErrPictureTooLarge (the realistic hi-res-cover case); any other item
// reports ErrSizeTooLarge. A zero/unset limit falls back to the 256 MiB library ceiling so a
// caller that left Limits unset still cannot write an unreadable item.
func checkItemSizes(items []item, limit int64) error {
	if limit <= 0 {
		limit = bits.DefaultLimits.MaxAllocBytes
	}
	for _, it := range items {
		body := int64(len(it.payload))
		if body <= limit {
			continue
		}
		if it.name == atomName("covr") {
			return fmt.Errorf("%w: cover art is %s (max %s)",
				waxerr.ErrPictureTooLarge, bits.HumanBytes(body), bits.HumanBytes(limit))
		}
		return fmt.Errorf("%w: ilst item %q is %s (max %s)",
			waxerr.ErrSizeTooLarge, string(it.name[:]), bits.HumanBytes(body), bits.HumanBytes(limit))
	}
	return nil
}

// checkBuiltItems is the single guard both write paths funnel buildItems' output through, so the
// size check (and the floor below) cannot be applied inconsistently or forgotten at a call site.
// It floors the write limit at the largest ilst item already present at parse: those items were
// read within the (possibly raised) parse limit, so writing them back is always safe even when
// the write limit defaults lower. Without the floor, an untouched oversized cover - parsed under
// a raised MaxAllocBytes - would make an unrelated tag edit fail. A genuinely new item larger
// than anything the file already held is still rejected.
func checkBuiltItems(items, parsed []item, limit int64) error {
	if limit <= 0 {
		limit = bits.DefaultLimits.MaxAllocBytes
	}
	if f := largestItemPayload(parsed); f > limit {
		limit = f
	}
	return checkItemSizes(items, limit)
}

// largestItemPayload returns the largest ilst item payload in items, or 0 when there are none.
func largestItemPayload(items []item) int64 {
	var maxLen int64
	for _, it := range items {
		if n := int64(len(it.payload)); n > maxLen {
			maxLen = n
		}
	}
	return maxLen
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
	// Order by offset; on a tie, a zero-width insert (oldLen==0) sorts before a same-offset
	// replace. Emitting the replace first would advance pos past the insert's offset and trip the
	// e.off < pos overlap guard below, so the oldLen tie-break forces insert-before-replace
	// regardless of input order. SliceStable (not Slice) additionally pins the order of two edits
	// sharing both offset and width: the codec avoids generating those, but a stable sort keeps the
	// output bytes reproducible if it ever does. Mirrors spliceBytes in write_chapters.go.
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].off != edits[j].off {
			return edits[i].off < edits[j].off
		}
		return edits[i].oldLen < edits[j].oldLen
	})
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
