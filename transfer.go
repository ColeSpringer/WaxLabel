package waxlabel

import (
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Transfer builds a metadata copy from one document onto another, with optional
// replacements for the timeline content a caller has already remapped. It is the
// [Editor] counterpart for copying: [Document.Transfer] starts one, the Set methods
// record replacements, and [Transfer.Plan] or [Transfer.Prepare] resolves it.
//
// Without any replacement it copies the source verbatim, which is what
// [Document.PlanTransfer] and [Document.PrepareTransfer] do.
type Transfer struct {
	doc             *Document
	chapters        []Chapter
	chaptersSet     bool
	syncedLyrics    []core.SyncedLyrics
	syncedLyricsSet bool
}

// Transfer starts a transfer whose source is this document. See [Transfer].
func (d *Document) Transfer() *Transfer { return &Transfer{doc: d} }

// SetChapters replaces the chapters the transfer carries, instead of the source's own.
// The list is written as given (sorted by start, as [Editor.SetChapters] sorts): it is
// authored against the destination's timeline, so the source's run-to-end-of-file rule
// does not apply and a final chapter meant to run to the destination's end carries a zero
// End. Passing no chapters is an explicit "no chapters" and clears the destination's own
// set; a source that simply has none leaves the destination's alone.
func (t *Transfer) SetChapters(chs ...Chapter) *Transfer {
	t.chapters = normalizeChapters(chs)
	t.chaptersSet = true
	return t
}

// SetSyncedLyrics replaces the synced lyrics the transfer carries, instead of the
// source's own, with the normalization [Editor.SetSyncedLyrics] applies (empty sets
// dropped, lines sorted by time). Passing no sets - or only empty ones - is an explicit
// "no synced lyrics" and clears the destination's own; a source that simply has none
// leaves the destination's alone.
func (t *Transfer) SetSyncedLyrics(sls ...SyncedLyrics) *Transfer {
	t.syncedLyrics = normalizeSyncedLyrics(sls)
	t.syncedLyricsSet = true
	return t
}

// source returns the media the transfer grades and writes from: the source document with
// the timeline content this builder will actually carry. The copy is shallow and
// read-only - it shares the source's slices and TagSet, is never mutated, and is never
// retained past the call.
func (t *Transfer) source() *core.Media {
	m := *t.doc.media
	if t.chaptersSet {
		m.Chapters = t.chapters
	} else {
		m.Chapters = core.OpenRunToEOFEnd(m.Chapters, m.Properties.Duration())
	}
	if t.syncedLyricsSet {
		m.SyncedLyrics = t.syncedLyrics
	}
	return &m
}

// Plan simulates copying the transfer's metadata (tags, pictures, chapters, and synced
// lyrics) into a file of format dst. It reports what each piece would carry, downgrade, or
// lose without writing or needing a destination file. It consults dst's capabilities under
// the given write options, so an option-dependent destination is judged as a real write
// would be.
//
// A read-only destination format reports everything dropped; an unimplemented
// destination is an error. It does not refuse a read-only destination the way
// [Transfer.Prepare] does: there is no destination file here to refuse, and the whole
// point of a format-level simulation is to describe the projection. An explicit empty
// replacement is not a report item either - the report grades what the source carries,
// and there is no destination here to clear.
func (t *Transfer) Plan(dst Format, opts ...WriteOption) (TransferReport, error) {
	if t.doc.zero() {
		return TransferReport{}, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	codec, ok := core.ForFormat(dst)
	if !ok {
		return TransferReport{}, fmt.Errorf("%w: %s", waxerr.ErrUnsupportedFormat, dst)
	}
	m := t.source()
	// nil destination file: Plan is a pure simulation against the format, so the codec
	// answers file-agnostically (any per-file constraint, like the WebM cover refusal, is
	// judged when Prepare/copy supply a real file).
	caps := codec.Capabilities(nil, resolveWriteOptions(opts))
	return TransferReport{
		Source: m.Format,
		Dest:   dst,
		Items:  core.ProjectTransfer(m, caps),
	}, nil
}

// Prepare projects the transfer's metadata onto dst and resolves the result into a
// ready-to-execute [Plan] that writes dst, returning the plan together with the
// [TransferReport] describing the projection. The report is computed from the same
// projection the plan applies: every carried or downgraded item is set on the destination
// edit, and every dropped item is left off.
//
// The report grades the destination's representational capability per
// field/picture/chapter, including hard structural limits it models (such as the
// MP4 chapter-count cap, reported as a drop). A few codec validity checks that
// depend on the bytes themselves - an embedded image in a format the destination
// cannot label, or a structurally invalid picture set - are enforced when the plan
// is prepared and surface as an error from this call rather than as a per-item
// drop; in that case the returned report still describes the attempted projection.
//
// The transfer overlays the source onto dst: each canonical key present in the source
// replaces that key in the destination, the source's pictures replace the destination
// picture set whenever at least one source picture is representable in the destination
// (a source whose covers are all unrepresentable leaves the destination's own covers
// intact), and likewise for chapters and synced lyrics. Destination keys the source does
// not carry are kept. An explicit empty replacement clears the destination's own chapters
// or synced lyrics, subject to the same gates a direct [Editor.ClearChapters] hits. dst is
// not modified; only [Plan.Execute] writes.
func (t *Transfer) Prepare(dst *Document, opts ...WriteOption) (*Plan, TransferReport, error) {
	if t.doc.zero() || dst.zero() {
		return nil, TransferReport{}, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	m := t.source()
	caps := dst.Capabilities(opts...)
	items := core.ProjectTransfer(m, caps)
	report := TransferReport{Source: m.Format, Dest: dst.media.Format, Items: items}

	// A read-only destination that had something to store is a refused write, not a
	// clean run of per-item drops. Without this the transfer sets nothing on the editor,
	// the codec's no-op fast path returns a NoOpPlan, and the codec's own refusal is
	// never reached - so a copy onto a WMA reports every field dropped and then exits 0,
	// while the same edit through set exits 3.
	//
	// Gated on a dropped item, not on ReadOnly alone: a transfer with nothing to carry, or
	// one whose every value the destination already holds, writes nothing and legitimately
	// succeeds - refusing those would make copy stricter than set on the same file, which
	// is the inconsistency this fixes. The error is the codec's own, so ASF keeps
	// unsupported-format and a fragmented MP4 keeps unsupported-fragmentation.
	if caps.ReadOnly && report.HasDropped() {
		return nil, report, readOnlyRefusal(caps)
	}

	ed := dst.Edit()
	// The whole transfer is a faithful carry from the source, not a user-authored
	// edit, so suppress the edit-time sanity warnings (chapter past-duration/duplicate,
	// single-valued-multi): a copy must not flag metadata the user authored none of.
	ed.carried = true

	// Pictures are a set. Build the representable subset first, then replace the
	// destination's set only when the source has at least one picture the destination can
	// write. Clearing before that check would destroy a valid destination cover when every
	// source picture is unrepresentable, such as GIF or WebP copied onto an MP4 that
	// already has a PNG cover. Representable is the same per-MIME test ProjectTransfer
	// uses for picture report items.
	//
	// The block is also gated on the destination actually storing pictures: a read-only
	// format or a no-cover container like WebM cannot hold covers, so touching its picture
	// set would only mark a change the writer refuses. Either way, leaving the set
	// untouched lets tags transfer while each source cover is reported Dropped.
	if !caps.ReadOnly && caps.Pictures.Write != core.AccessNone {
		// PartitionRepresentable is the same per-image split ProjectTransfer's report and the
		// editor's drop path use, so the write filter cannot drift from what the report grades.
		// PartitionPictureSlots then applies the destination's slot selection (APE's two cover
		// names), so a picture the report graded Dropped for want of a slot is not handed to
		// the writer to drop again.
		representable, _, _ := core.PartitionRepresentable(caps.Pictures, core.ClonePictures(m.Pictures))
		representable, _, _ = core.PartitionPictureSlots(caps.Pictures, representable)
		if len(representable) > 0 {
			ed.ClearPictures()
			for _, p := range representable {
				ed.AddPicture(p)
			}
		}
	}

	for _, it := range items {
		// Dropped means the destination cannot store it. Excluded means policy keeps the
		// destination's own value. Neither is written. A slashed track/disc number arrives
		// already split into its number and total keys by the read path, so each is graded and
		// written independently here - no transfer-time slash handling is needed.
		if it.Disposition == Dropped || it.Disposition == Excluded {
			continue
		}
		switch it.Kind {
		case core.TransferField:
			if vals, ok := m.Tags.Get(it.Key); ok {
				ed.Set(it.Key, vals...)
			}
		case core.TransferChapter:
			ed.SetChapters(core.CloneChapters(m.Chapters)...)
		case core.TransferSyncedLyric:
			ed.SetSyncedLyrics(core.CloneSyncedLyrics(m.SyncedLyrics)...)
		}
	}

	// An explicit empty replacement means "no chapters" (or "no synced lyrics"), which the
	// report cannot express: it grades what the source carries, and an empty list carries
	// nothing. Route it through the editor's own clear so the destination's gates decide any
	// refusal, exactly as a direct clear would.
	if t.chaptersSet && len(m.Chapters) == 0 && len(dst.media.Chapters) > 0 {
		ed.ClearChapters()
	}
	if t.syncedLyricsSet && len(m.SyncedLyrics) == 0 && len(dst.media.SyncedLyrics) > 0 {
		ed.ClearSyncedLyrics()
	}

	// Carry the source's already-embedded pictures verbatim: ProjectTransfer already
	// graded them by the destination's capability, so an exotic-but-valid embedded
	// cover (HEIC/AVIF/JXL, which the header sniff rejects by design) must keep
	// carrying - copy has no --force to wave it through. Opt the added-picture
	// validation out on a fresh slice so the caller's opts are not mutated; no other
	// option toggles AllowUnrecognizedPictures, so prepending is order-safe.
	plan, err := ed.Prepare(append([]WriteOption{WithUnrecognizedPictures()}, opts...)...)
	if err != nil {
		return nil, report, err
	}
	return plan, report, nil
}

// PlanTransfer simulates copying this document's canonical metadata into a file of format
// dst, carrying the source's own chapters and synced lyrics. It is [Transfer.Plan] on a
// transfer with no replacements; use [Document.Transfer] to hand the copy a remapped
// timeline.
func (d *Document) PlanTransfer(dst Format, opts ...WriteOption) (TransferReport, error) {
	return d.Transfer().Plan(dst, opts...)
}

// PrepareTransfer projects this document's canonical metadata onto dst and resolves the
// result into a ready-to-execute [Plan], carrying the source's own chapters and synced
// lyrics. It is [Transfer.Prepare] on a transfer with no replacements; use
// [Document.Transfer] to hand the copy a remapped timeline.
func (d *Document) PrepareTransfer(dst *Document, opts ...WriteOption) (*Plan, TransferReport, error) {
	return d.Transfer().Prepare(dst, opts...)
}

// readOnlyRefusal returns the error to fail a transfer onto a read-only destination
// with: the codec's own refusal when it attached one, and a generic unsupported-format
// error for the [Capabilities] fallbacks that carry none (an unknown or unimplemented
// format, which has no codec to ask).
func readOnlyRefusal(caps Capabilities) error {
	if err := caps.ReadOnlyReason(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s files cannot be written", waxerr.ErrUnsupportedFormat, caps.Format)
}
