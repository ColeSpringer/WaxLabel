package waxlabel

import (
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// PlanTransfer simulates copying this document's canonical metadata (tags,
// pictures, chapters, and synced lyrics) into a file of format dst. It reports what each
// piece would carry, downgrade, or lose without writing or needing a destination file. It
// consults dst's capabilities under the given write options, so an option-dependent
// destination is judged as a real write would be.
//
// A read-only destination format reports everything dropped; an unimplemented
// destination is an error. Use [Document.PrepareTransfer] when you have an actual
// destination file and want an executable plan as well.
func (d *Document) PlanTransfer(dst Format, opts ...WriteOption) (TransferReport, error) {
	if d.zero() {
		return TransferReport{}, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	codec, ok := core.ForFormat(dst)
	if !ok {
		return TransferReport{}, fmt.Errorf("%w: %s", waxerr.ErrUnsupportedFormat, dst)
	}
	// nil destination file: PlanTransfer is a pure simulation against the format,
	// so the codec answers file-agnostically (any per-file constraint, like the
	// WebM cover refusal, is judged when PrepareTransfer/copy supply a real file).
	caps := codec.Capabilities(nil, resolveWriteOptions(opts))
	return TransferReport{
		Source: d.media.Format,
		Dest:   dst,
		Items:  core.ProjectTransfer(d.media, caps),
	}, nil
}

// PrepareTransfer projects this document's canonical metadata onto dst and
// resolves the result into a ready-to-execute [Plan] that writes dst, returning
// the plan together with the [TransferReport] describing the projection. The
// report is computed from the same projection the plan applies: every carried or
// downgraded item is set on the destination edit, and every dropped item is left off.
//
// The report grades the destination's representational capability per
// field/picture/chapter, including hard structural limits it models (such as the
// MP4 chapter-count cap, reported as a drop). A few codec validity checks that
// depend on the bytes themselves - an embedded image in a format the destination
// cannot label, or a structurally invalid picture set - are enforced when the plan
// is prepared and surface as an error from this call rather than as a per-item
// drop; in that case the returned report still describes the attempted projection.
//
// The transfer overlays src onto dst: each canonical key present in the source
// replaces that key in the destination, the source's pictures replace the destination
// picture set whenever at least one source picture is representable in the destination
// (a source whose covers are all unrepresentable leaves the destination's own covers
// intact), and likewise for chapters and synced lyrics. Destination keys the source does
// not carry are kept. dst is not modified; only [Plan.Execute] writes.
func (d *Document) PrepareTransfer(dst *Document, opts ...WriteOption) (*Plan, TransferReport, error) {
	if d.zero() || dst.zero() {
		return nil, TransferReport{}, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	caps := dst.Capabilities(opts...)
	items := core.ProjectTransfer(d.media, caps)
	report := TransferReport{Source: d.media.Format, Dest: dst.media.Format, Items: items}

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
		representable := make([]core.Picture, 0, len(d.media.Pictures))
		for _, p := range core.ClonePictures(d.media.Pictures) {
			if core.Representable(caps.Pictures, p) {
				representable = append(representable, p)
			}
		}
		if len(representable) > 0 {
			ed.ClearPictures()
			for _, p := range representable {
				ed.AddPicture(p)
			}
		}
	}

	// When a slash number's embedded total is dropped, copy only the number side. Passing the
	// raw "3/70000" value into the destination editor would re-create the unstorable total
	// and could overwrite the destination's valid total before the writer drops it. Explicit
	// total keys are already skipped by the main apply loop when they are dropped.
	stripEmbeddedTotal := map[tag.Key]bool{}
	for _, it := range items {
		if it.Kind != core.TransferField || it.Disposition != Dropped {
			continue
		}
		var numKey tag.Key
		switch it.Key {
		case tag.TrackTotal:
			numKey = tag.TrackNumber
		case tag.DiscTotal:
			numKey = tag.DiscNumber
		default:
			continue
		}
		if !d.media.Tags.Has(it.Key) { // a derived total, not an explicit source key
			stripEmbeddedTotal[numKey] = true
		}
	}

	for _, it := range items {
		// Dropped means the destination cannot store it. Excluded means policy keeps the
		// destination's own value. Neither is written.
		if it.Disposition == Dropped || it.Disposition == Excluded {
			continue
		}
		switch it.Kind {
		case core.TransferField:
			if vals, ok := d.media.Tags.Get(it.Key); ok {
				if stripEmbeddedTotal[it.Key] && len(vals) == 1 {
					num, _ := tag.SplitNumberTotal(vals[0])
					ed.Set(it.Key, num) // number only; the dropped total stays with the dest
				} else {
					ed.Set(it.Key, vals...)
				}
			}
		case core.TransferChapter:
			ed.SetChapters(core.CloneChapters(d.media.Chapters)...)
		case core.TransferSyncedLyric:
			ed.SetSyncedLyrics(core.CloneSyncedLyrics(d.media.SyncedLyrics)...)
		}
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
