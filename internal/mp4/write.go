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
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan builds a preservation-first rewrite: re-render ilst (and QT stores as needed),
// reuse free padding when possible, shift stco/co64 on growth; mdat copied verbatim.
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

	// --numeric-genre swaps the text "\xa9gen" atom for the numeric "gnre" one. The value
	// itself is unchanged, so the tag comparison above cannot see it.
	encodingRewrite := opts.NumericGenre && genreEncodingChanged(d.items, edited.Tags)
	// The encoding the rebuild writes: what this edit asked for, or the numeric form the file
	// already holds, so an unrelated edit does not undo an earlier --numeric-genre run.
	numericGenre := numericGenreEncoding(d.items, edited.Tags, opts.NumericGenre)

	// What the rebuild renders. trkn/disk numbers are fixed binary uint16s and the integer and
	// BPM atoms fixed-width too, so an edit that makes a slot unstorable would clear it and
	// erase a good value: such a slot keeps the value it has.
	itunesAtoms := !mdtaStore(d)
	stored := edited
	var kept map[tag.Key]bool
	if itunesAtoms {
		if patched, keys := restoreUnstorableSlots(base.Tags, edited.Tags); len(keys) > 0 {
			ec := *edited
			ec.Tags = patched
			stored, kept = &ec, keys
		}
	}
	// An edit that names a key several items hold settles it, even with the value it has; one
	// whose value for it is not stored settles nothing.
	named := namedKeys(opts.Touched, kept)
	cf := ilstConflicts(d, base, stored, opts, named)
	extraOps := cf.ops()
	if encodingRewrite {
		extraOps = append(extraOps, core.EncodingRewriteOp("genre"))
	}
	// What a rewrite changes that the canonical view cannot show.
	structural := encodingRewrite || cf.settled || storesDisagree(d, stored.Tags, named)

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy (so SaveAsFile/ WriteTo
	// still produce a whole file) flagged NoOp so SaveBack skips it.
	if !tagsChanged && !picturesChanged && !chaptersChanged && !opts.PaddingExplicit && !structural {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// The unwritable shapes, shared with Capabilities so the two cannot disagree.
	if err := d.refuseWrite(); err != nil {
		return nil, err
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

	// Surface canonical values the iTunes atoms cannot represent. The predicates read the
	// strings the encoder consumes, and both rewrite paths build from edited.Tags, so the
	// shared report covers both.
	// A frozen key the edit leaves as it was is kept byte for byte: nothing of it is dropped or
	// coerced. One the edit gave a value that is not stored is still the edit's to hear about.
	// One the second store alone holds is text there, whatever the store written would make of it.
	rendered := edited.Tags.Clone()
	for k := range cf.frozen {
		was, _ := base.Tags.Get(k)
		if now, _ := rendered.Get(k); slices.Equal(was, now) {
			rendered.Delete(k)
		}
	}
	for k := range cf.elsewhere {
		rendered.Delete(k)
	}
	for _, dv := range itunesDroppedValues(rendered, itunesAtoms) {
		msg := fmt.Sprintf("%s value %q cannot be represented in this format and was dropped", dv.Key, dv.Value)
		if dv.ZeroUnset {
			// The 0 bytes are written (0/N), but decodePair treats a 0 slot as unset and reads
			// it back as absent: a round-trip loss, not an unrepresentable value.
			msg = fmt.Sprintf("%s value %q is treated as unset in this format and reads back as absent", dv.Key, dv.Value)
		}
		report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueDropped, msg, dv.Key)
	}
	for _, cv := range itunesCoercedValues(rendered, itunesAtoms) {
		// The boolean atoms (cpil, pgap, shwm) each hold a single byte, so a non-boolean is
		// stored as 0 (false), not dropped.
		var msg string
		if tag.IsBooleanKey(cv.Key) {
			msg = fmt.Sprintf("%s value %q is not a valid boolean and was stored as 0 (false)", cv.Key, cv.Value)
		} else {
			msg = fmt.Sprintf("%s value %q is not an integer and was rounded to %s", cv.Key, cv.Value, cv.Stored)
		}
		report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueCoerced, msg, cv.Key)
	}
	// A structured single-atom key given more than one value stores only the first.
	for _, ev := range itunesExtraStructuredValues(rendered, itunesAtoms) {
		report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueDropped,
			fmt.Sprintf("%s holds multiple values but its MP4 atom stores only the first; dropped %q", ev.Key, ev.Value), ev.Key)
	}
	// Warn for each multi-valued field this edit wrote. The ilst stores it as several data
	// atoms under one item; WaxLabel round-trips them, but many readers show only the first.
	for _, k := range multiValueDataKeys(rendered) {
		vals, _ := rendered.Get(k)
		if bv, _ := base.Tags.Get(k); slices.Equal(bv, vals) {
			continue // the field was already multi-valued and this edit did not touch it
		}
		report.Warnings = core.WarnKeyed(report.Warnings, core.WarnMP4MultiValue,
			fmt.Sprintf("%s has %d values, stored as %d MP4 data atoms; some readers surface only the first", k, len(vals), len(vals)), k)
	}

	edited = stored

	// A chapter edit rewrites the whole moov.udta (folding any ilst change into one
	// delta); a tag/picture-only edit keeps the lighter in-place ilst path.
	if chaptersChanged {
		// An encoding rewrite forces the ilst rebuild too: without needIlst, buildChapterUdta
		// splices the source ilst back in verbatim and drops the request.
		pl, err := planChapters(d, base, edited, tagsChanged || picturesChanged || structural, picturesChanged, cf, opts, report)
		if err != nil || pl == nil {
			return pl, err
		}
		// Appended afterwards: both chapter paths take report by value and assign
		// report.Operations wholesale, so a line added before the call would be discarded.
		pl.Report.Operations = append(pl.Report.Operations, extraOps...)
		// Collapse a chapter edit that re-projected to base's exact chapters, tags, and
		// pictures into a no-op, so re-applying an identical list does not rewrite the
		// file. --add-chapter builds chapters with End==0 while a parse derives End, so
		// core.EqualChapters always reports a change. Compare pl.Result instead: its
		// chapters, tags and pictures are the round-tripped read view a fresh reparse of
		// the output returns. A real title/start/end edit still differs and writes.
		//
		// Skip the collapse when the source was conflicted. If chpl and the QuickTime
		// table disagreed at parse, re-applying the preferred list rewrites the stale
		// table, and DowngradeNoOp lacks WarnChapterSourceConflict, so the two would
		// compare equal. After that write the file is consistent.
		conflicted := len(core.WarningsWithCode(base.Warnings, core.WarnChapterSourceConflict)) > 0
		if !conflicted {
			// pl.Report.Warnings (not the outer report): planChapters took report by value, so
			// warnings appended during planning live only on pl.Report.
			if np := core.DowngradeNoOp(core.FormatMP4, edited.Identity.Size, base, pl.Result,
				base.Tags.Equal(pl.Result.Tags), structural, pl.Report.Warnings); np != nil {
				return np, nil
			}
		}
		return pl, nil
	}

	// Re-render the ilst from the edited canonical set, keeping the preserved items
	// (unknown atoms, foreign freeforms) verbatim. An unchanged covr is carried as parsed,
	// so a tag-only edit never rewrites a cover through coverType's JPEG default.
	covr := coverItemsToWrite(edited.Pictures, d.items, picturesChanged)
	newItems, newKeys, respelled := buildIlstItems(d, edited.Tags, covr, numericGenre, cf)
	extraOps = append(extraOps, respelledOps(respelled)...)
	if err := checkBuiltItems(newItems, d.items, opts.Limits.MaxAllocBytes); err != nil {
		return nil, err
	}
	// Resolve which QuickTime store this edit writes to and what changes outside the ilst
	// region: a grown mdta keys index, udta-level text atoms and a second store synced to the
	// canonical value.
	// A padding floor is reserved beside an ilst, so with one the udta text atoms alone will not do.
	qw, err := planQTMeta(d, edited, newKeys, opts.Padding.Min <= 0, picturesChanged)
	if err != nil {
		return nil, err
	}
	if qw.needsRegions() {
		return planQTMetaWrite(d, base, edited, newItems, qw, extraOps, structural, opts, report)
	}
	// A file with no ilst gains no empty one: only a padding floor reserves space there. What
	// the edit asked to store and could not is still reported.
	if d.ilst == nil && len(newItems) == 0 && opts.Padding.Min <= 0 {
		np := core.NoOpPlan(core.WriteReport{Format: core.FormatMP4, BytesBefore: edited.Identity.Size}, edited.Identity.Size, base)
		np.Report.Warnings = report.Warnings
		return np, nil
	}
	var ilstPayload []byte
	for _, it := range newItems {
		ilstPayload = append(ilstPayload, itemBytes(it)...)
	}
	newIlst := renderAtom(atomName("ilst"), ilstPayload)
	// Check the ilst total against the 32-bit box-size field renderAtom/renderData write.
	// checkSizes covers only 8-byte-header ancestors, so it cannot see an ilst wrap inside
	// a 64-bit moov.
	if err := checkBoxSize32(atomName("ilst"), 8+int64(len(ilstPayload))); err != nil {
		return nil, err
	}

	lay, err := planLayout(d, newIlst, opts)
	if err != nil {
		return nil, err
	}
	report.Warnings = paddingClampWarning(report.Warnings, lay.paddingClamped)
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
		es, err := patchTables(delta, lay.regionStart, nil, d.offTables, d.auxTables)
		if err != nil {
			return nil, err
		}
		edits = append(edits, es...)
	}
	segs, err := assemble(edits, d.size)
	if err != nil {
		return nil, err
	}

	report.BytesAfter = total
	report.PaddingAfter = lay.freeContent
	report.Operations = append(operations(d, lay, delta, len(edited.Pictures)), extraOps...)

	result := buildResult(edited, d, newItems, lay)
	// Collapse to a no-op when the ilst rebuild re-projected to base's values: an edit that
	// stores nothing writes nothing. A size change stands only where the padding was asked for.
	if np := core.DowngradeNoOp(core.FormatMP4, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), structural || opts.PaddingExplicit && delta != 0, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// namedKeys returns the keys an edit names, less those in kept: a key whose value the edit does
// not get stored is not named.
func namedKeys(touched, kept map[tag.Key]bool) map[tag.Key]bool {
	named := map[tag.Key]bool{}
	for k := range touched {
		if !kept[k] {
			named[k] = true
		}
	}
	return named
}

// ilstConflicts settles the values the file's items hold unread against what an edit changes or
// names. A genre re-encoding rewrites the genre.
func ilstConflicts(d *doc, base, edited *core.Media, opts core.WriteOptions, named map[tag.Key]bool) conflicts {
	changed := core.ChangedKeys(base.Tags, edited.Tags, named)
	if opts.NumericGenre && genreEncodingChanged(d.items, edited.Tags) {
		changed[tag.Genre] = true
	}
	cf := resolveConflicts(d.reading(), changed)
	cf.elsewhere = viewStores(d).secondOnly
	return cf
}

// storesDisagree reports whether a udta text atom or the second store holds, for a key the edit
// names, another value than the one written: naming the key settles it, as any other write would.
func storesDisagree(d *doc, tags tag.TagSet, named map[tag.Key]bool) bool {
	for _, u := range d.udtaTexts {
		if vals, _ := tags.Get(u.key); named[u.key] {
			if _, changed := udtaTextEdit(u, fitting(vals)); changed {
				return true
			}
		}
	}
	if d.second == nil {
		return false
	}
	read, _, _, _ := storeContribs(readItems(d.second.items))
	held := core.BuildTagSet(read)
	tag.NormalizeNumberPairs(&held)
	for _, k := range held.Keys() {
		was, _ := held.Get(k)
		if now, _ := tags.Get(k); named[k] && !slices.Equal(was, now) {
			return true
		}
	}
	return false
}

// paddingClampWarning appends the clamp warning shared by every path that emits fresh
// padding, so the in-place ilst layout and the udta region rebuild word it identically.
func paddingClampWarning(ws []core.Warning, clamped bool) []core.Warning {
	if !clamped {
		return ws
	}
	return core.Warn(ws, core.WarnPaddingClamped,
		fmt.Sprintf("requested padding exceeded the %d-byte limit and was clamped to it", maxPadding))
}

// checkCoverFormats rejects a cover whose image format an MP4 covr atom cannot label.
// Another format (WebP, GIF, ...) would be stored with a JPEG type flag over non-JPEG
// bytes, a corrupt cover.
func checkCoverFormats(pics []core.Picture) error {
	for _, p := range pics {
		if !coverMIMESupported(p.MIME) {
			return fmt.Errorf("%w: MP4 cover art must be JPEG, PNG, or BMP (got %q)",
				waxerr.ErrUnsupportedTag, p.MIME)
		}
	}
	return nil
}

// preservedItems returns the items a rebuild keeps verbatim: those it does not own (unknown
// atoms, foreign-mean freeforms, parse failures) and those holding a frozen key.
func preservedItems(rd reading, frozen map[tag.Key]bool) []item {
	var out []item
	for i, it := range rd.items {
		if !rd.results[i].owned || slices.ContainsFunc(rd.keys[i], func(k tag.Key) bool { return frozen[k] }) {
			out = append(out, it)
		}
	}
	return out
}

// layout is the resolved placement of the rewritten tag region: the source span
// it replaces, the literal bytes that replace it, and the enclosing atoms whose sizes
// grow.
type layout struct {
	regionStart, regionEnd int64
	regionBytes            []byte
	ancestors              []atomRef
	freeContent            int64 // free atom payload length (padding bytes)
	created                bool  // true when the tag path was created (no prior ilst)
	paddingClamped         bool  // a too-large padding Target was clamped to maxPadding (a free atom was emitted)
}

// maxPadding caps the metadata padding the MP4 writer allocates for a free atom.
const maxPadding = 256 << 20

// ilstPadding sizes the padding beside a rebuilt ilst of newLen bytes in a region of regionLen:
// what the region has left, when the policy reuses it in place and that is no padding or a whole
// free atom of at least the policy's floor, and else the policy's target, capped at maxPadding.
func ilstPadding(newLen, regionLen int64, pol core.PaddingPolicy) (padContent int64, inPlace bool) {
	left := regionLen - newLen
	switch {
	case !pol.ReuseInPlace:
	case left == 0 && pol.Min <= 0:
		return 0, true
	case left >= freeAtomHeaderLen && left-freeAtomHeaderLen >= pol.Min:
		return left - freeAtomHeaderLen, true
	}
	// Does not fit, leaves a 1-7 byte remainder a free atom cannot represent, falls below the
	// floor, or is not to be reused.
	return min(pol.ClampTarget(), maxPadding), false
}

// padClamped reports whether fresh padding under pol is cut to maxPadding.
func padClamped(pol core.PaddingPolicy) bool { return pol.ClampTarget() > maxPadding }

// planLayout decides how to place the new ilst: within the existing ilst+free region as
// ilstPadding sizes it (in place, so the media never moves, when the policy reuses it), or by
// creating the moov/udta/meta/ilst path when the file had no tags.
func planLayout(d *doc, newIlst []byte, opts core.WriteOptions) (layout, error) {
	if d.ilst != nil {
		regionStart, regionEnd := d.ilstRegion()
		pad, inPlace := ilstPadding(int64(len(newIlst)), regionEnd-regionStart, opts.Padding)
		lay := layout{regionStart: regionStart, regionEnd: regionEnd, ancestors: d.tagAncestors()}
		lay.regionBytes, lay.freeContent = appendFree(newIlst, pad)
		lay.paddingClamped = !inPlace && padClamped(opts.Padding)
		return lay, nil
	}

	// No ilst: create the missing path. Insert into the deepest existing of
	// moov/udta/meta, at its end (the new atom becomes that container's last child;
	// everything after shifts and the ancestor sizes grow).
	inner, withFree, freeContent, err := buildCreated(d, newIlst, min(opts.Padding.ClampTarget(), maxPadding))
	if err != nil {
		return layout{}, err
	}
	at := inner.end()
	regionStart := at
	// When inserting into an existing udta, place the new atom after its last complete
	// child, not at its raw end: a udta body can carry a tolerated trailing 32-bit zero,
	// QuickTime's user-data list terminator.
	if inner.name == atomName("udta") && d.udtaRaw != nil {
		if clean := d.udta.offset + d.udta.headerLen + udtaCleanLen(d.udtaRaw); clean < at {
			regionStart = clean
		}
	}
	return layout{
		regionStart: regionStart, regionEnd: at, regionBytes: withFree,
		ancestors: createdAncestors(d), freeContent: freeContent, created: true,
		paddingClamped: padClamped(opts.Padding),
	}, nil
}

// buildCreated renders the atom(s) to insert when the file had no ilst, choosing the
// wrapper depth from which containers already exist.
func buildCreated(d *doc, newIlst []byte, pad int64) (inner atomRef, bytes []byte, freeContent int64, err error) {
	ilstAndFree, freeContent := appendFree(newIlst, pad)
	switch {
	case d.meta != nil:
		// Append ilst(+free) inside the existing meta. The ilst is already size-checked, and
		// the existing meta/udta/moov grow via sizePatch, which is 64-bit-aware.
		return *d.meta, ilstAndFree, freeContent, nil
	case d.udta != nil:
		metaInner := append(hdlrAtom(), ilstAndFree...)
		// The fresh meta has a 32-bit size field and is not an ancestor checkSizes can see,
		// so check it here. meta total = metaPrefix() + len(metaInner).
		if err := checkBoxSize32(atomName("meta"), int64(metaPrefix()+len(metaInner))); err != nil {
			return atomRef{}, nil, 0, err
		}
		meta := renderFullBox(atomName("meta"), metaInner)
		return *d.udta, meta, freeContent, nil
	default:
		metaInner := append(hdlrAtom(), ilstAndFree...)
		// The fresh udta wraps the fresh meta. Both have 32-bit size fields and are invisible
		// to checkSizes.
		if err := checkBoxSize32(atomName("udta"), int64(8+metaPrefix()+len(metaInner))); err != nil {
			return atomRef{}, nil, 0, err
		}
		meta := renderFullBox(atomName("meta"), metaInner)
		return *d.moov, renderAtom(atomName("udta"), meta), freeContent, nil
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
// returning the combined bytes and the free atom's payload size. A non-positive
// payload yields no free atom.
func appendFree(ilst []byte, content int64) (combined []byte, freeContent int64) {
	if content <= 0 {
		return ilst, 0
	}
	free := renderAtom(atomName("free"), make([]byte, content))
	return append(slices.Clone(ilst), free...), content
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

// checkSizes fails if a 32-bit enclosing atom's size field would overflow after adding
// delta (a >4 GiB moov, which would need a 64-bit rewrite this version does not do).
// Only a grow (delta > 0) can overflow.
func checkSizes(ancestors []atomRef, delta int64) error {
	for _, anc := range ancestors {
		if anc.headerLen == 8 && anc.size+delta > math.MaxUint32 {
			return fmt.Errorf("%w: atom %q would exceed the 4 GiB 32-bit size limit",
				waxerr.ErrSizeTooLarge, anc.name)
		}
	}
	return nil
}

// checkBoxSize32 fails when a freshly-rendered box's total size would overflow the
// 32-bit box-size field renderAtom/renderData/renderFullBox write (they cast size to
// uint32 unchecked).
func checkBoxSize32(name [4]byte, totalLen int64) error {
	if totalLen > math.MaxUint32 {
		return fmt.Errorf("%w: %s atom would exceed the 4 GiB 32-bit size limit",
			waxerr.ErrSizeTooLarge, string(name[:]))
	}
	return nil
}

// checkItemSizes rejects any ilst item whose payload exceeds the alloc limit.
// readPayloadWhole caps an ilst item read at the same limit, so without this check the
// writer could emit a cover or freeform it cannot read back.
func checkItemSizes(items []item, limit int64) error {
	if limit <= 0 {
		limit = bits.DefaultLimits.MaxAllocBytes
	}
	for _, it := range items {
		body := int64(len(it.payload))
		if body <= limit {
			continue
		}
		got, max := bits.SizeOverLimit(body, limit)
		if it.name == atomName("covr") {
			return fmt.Errorf("%w: cover art is %s, max %s", waxerr.ErrPictureTooLarge, got, max)
		}
		return fmt.Errorf("%w: ilst item %q is %s, max %s", waxerr.ErrSizeTooLarge, string(it.name[:]), got, max)
	}
	return nil
}

// checkBuiltItems is the one check both write paths pass buildItems' output through, so
// the size limit and its floor are applied the same way at every call site.
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

// patchTables renders the offset-fixup edits for every table in the given groups,
// skipping any the skip predicate rejects (nil skips none).
func patchTables(delta, insertion int64, skip func(offsetTable) bool, groups ...[]offsetTable) ([]edit, error) {
	return patchTablesBy(func(e uint64) (uint64, bool) { return shiftOffset(e, insertion, delta) }, skip, groups...)
}

// patchTablesBy is patchTables for any rule that moves a chunk offset.
func patchTablesBy(shift func(uint64) (uint64, bool), skip func(offsetTable) bool, groups ...[]offsetTable) ([]edit, error) {
	var out []edit
	for _, g := range groups {
		for _, t := range g {
			if skip != nil && skip(t) {
				continue
			}
			e, err := offsetPatch(t, shift)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// closeMoov completes region edits that all lie inside moov: its size follows their summed length
// change, and every chunk offset moves by that of the edits ahead of it.
func closeMoov(d *doc, edits []edit) ([]edit, int64, error) {
	delta := sumDelta(edits)
	if delta == 0 {
		return edits, 0, nil
	}
	if err := checkSizes([]atomRef{*d.moov}, delta); err != nil {
		return nil, 0, err
	}
	moved := newShifts(edits)
	es, err := patchTablesBy(func(e uint64) (uint64, bool) {
		if e > math.MaxInt64 {
			return e, true
		}
		shifted := int64(e) + moved.at(int64(e))
		return uint64(shifted), shifted >= 0
	}, nil, d.offTables, d.auxTables)
	if err != nil {
		return nil, 0, err
	}
	return append(append(slices.Clip(edits), sizePatch(*d.moov, delta)), es...), delta, nil
}

// firstOffset returns the lowest offset an edit starts at, 0 for none.
func firstOffset(edits []edit) int64 {
	if len(edits) == 0 {
		return 0
	}
	first := edits[0].off
	for _, e := range edits[1:] {
		first = min(first, e.off)
	}
	return first
}

// shiftedTableCount counts the tables patchTables moves an entry of: those the skip predicate
// keeps that hold an entry past the insertion point.
func shiftedTableCount(insertion int64, skip func(offsetTable) bool, groups ...[]offsetTable) int {
	n := 0
	for _, g := range groups {
		for _, t := range g {
			if skip != nil && skip(t) {
				continue
			}
			if slices.ContainsFunc(t.entries, func(e uint64) bool { return e > uint64(insertion) }) {
				n++
			}
		}
	}
	return n
}

// appendShiftOp adds the report line for the offset tables a resize by delta moves an entry of.
func appendShiftOp(ops []string, d *doc, delta, insertion int64, skip func(offsetTable) bool) []string {
	if delta == 0 {
		return ops
	}
	if n := shiftedTableCount(insertion, skip, d.offTables, d.auxTables); n > 0 {
		ops = append(ops, fmt.Sprintf("%d offset table shift(s)", n))
	}
	return ops
}

// offsetPatch re-renders an offset table (a chunk-offset stco/co64, or a
// sample-auxiliary saio) with every entry moved by shift, so the media resolves to its
// new position after the metadata changed size.
func offsetPatch(t offsetTable, shift func(uint64) (uint64, bool)) (edit, error) {
	width := 4
	if t.co64 {
		width = 8
	}
	buf := make([]byte, len(t.entries)*width)
	for i, e := range t.entries {
		e, ok := shift(e)
		if !ok {
			return edit{}, fmt.Errorf("%w: an offset in %s does not resolve to a position in the rewritten file; it points inside the metadata region being replaced",
				waxerr.ErrInvalidData, t.id())
		}
		if t.co64 {
			binary.BigEndian.PutUint64(buf[i*8:], e)
		} else {
			if e > math.MaxUint32 {
				// Name the box rather than prescribe a remedy: a 32-bit stco widens to a co64,
				// but a version 0 saio widens to a version 1 saio, so "the file needs a co64"
				// would be wrong for half the tables that reach here.
				return edit{}, fmt.Errorf("%w: an offset in %s would exceed 4 GiB; the file needs 64-bit offset tables",
					waxerr.ErrSizeTooLarge, t.id())
			}
			binary.BigEndian.PutUint32(buf[i*4:], uint32(e))
		}
	}
	// After the 4-byte version/flags, any per-box prefix (a saio's aux_info_type pair),
	// and the 4-byte count.
	entriesOff := t.offset + t.headerLen + 8 + t.entryPrefix
	return edit{off: entriesOff, oldLen: int64(len(t.entries) * width), lit: buf}, nil
}

// shiftOffset moves a chunk offset past the insertion point by delta, so the media chunk
// resolves to its new position after the metadata changed size. Both the offset bytes
// and the plan's result use it, so the two cannot disagree.
func shiftOffset(e uint64, insertion, delta int64) (uint64, bool) {
	if e > uint64(insertion) {
		shifted := int64(e) + delta
		if shifted < 0 {
			return e, false
		}
		return uint64(shifted), true
	}
	return e, true
}

// assemble turns the sorted, disjoint edits into a rewrite segment list: copy
// the gaps from the source, emit each edit's literal bytes.
func assemble(edits []edit, size int64) ([]bits.Segment, error) {
	// Order by offset. Emitting a replace before an insert at the same offset would advance
	// pos past it and trip the e.off < pos overlap check below, so the oldLen tie-break
	// forces insert-before-replace.
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
		ops = append(ops, fmt.Sprintf("ilst rewrite (%+d bytes metadata)", delta))
	}
	ops = appendShiftOp(ops, d, delta, lay.regionStart, nil)
	if pics > 0 {
		ops = append(ops, fmt.Sprintf("pictures: %d", pics))
	}
	return ops
}
