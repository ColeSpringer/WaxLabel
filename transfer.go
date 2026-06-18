package waxlabel

import (
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// PlanTransfer simulates copying this document's canonical metadata (tags,
// pictures, and chapters) into a file of format dst, reporting - without writing
// or even needing a destination file - what each piece would carry, downgrade,
// or lose. It consults dst's capabilities under the given write options, so an
// option-dependent destination (one whose support changes with, say, the legacy
// or multi-value policy) is judged exactly as a real write would be.
//
// A read-only destination format reports everything dropped; an unimplemented
// destination is an error. Use [Document.PrepareTransfer] when you have an actual
// destination file and want an executable plan as well.
func (d *Document) PlanTransfer(dst Format, opts ...WriteOption) (TransferReport, error) {
	codec, ok := core.ForFormat(dst)
	if !ok {
		return TransferReport{}, fmt.Errorf("%w: %s", waxerr.ErrUnsupportedFormat, dst)
	}
	caps := codec.Capabilities(resolveWriteOptions(opts))
	return TransferReport{
		Source: d.media.Format,
		Dest:   dst,
		Items:  core.ProjectTransfer(d.media, caps),
	}, nil
}

// PrepareTransfer projects this document's canonical metadata onto dst and
// resolves the result into a ready-to-execute [Plan] that writes dst, returning
// the plan together with the [TransferReport] describing the projection. The
// report is computed from the same projection the plan applies - every carried or
// downgraded item is set on the destination edit and every dropped item is left
// off - so the report cannot disagree with what executing the plan produces.
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
// replaces that key in the destination, the source's pictures replace the
// destination's (when the source has any and the destination can store them), and
// likewise for chapters; destination keys the source does not carry are kept. dst
// is not modified - only [Plan.Execute] writes.
func (d *Document) PrepareTransfer(dst *Document, opts ...WriteOption) (*Plan, TransferReport, error) {
	caps := dst.Capabilities(opts...)
	items := core.ProjectTransfer(d.media, caps)
	report := TransferReport{Source: d.media.Format, Dest: dst.media.Format, Items: items}

	ed := dst.Edit()
	for _, it := range items {
		if it.Disposition == Dropped {
			continue
		}
		switch it.Kind {
		case core.TransferField:
			if vals, ok := d.media.Tags.Get(it.Key); ok {
				ed.Set(it.Key, vals...)
			}
		case core.TransferPicture:
			ed.ClearPictures()
			for _, p := range core.ClonePictures(d.media.Pictures) {
				ed.AddPicture(p)
			}
		case core.TransferChapter:
			ed.SetChapters(core.CloneChapters(d.media.Chapters)...)
		}
	}

	plan, err := ed.Prepare(opts...)
	if err != nil {
		return nil, report, err
	}
	return plan, report, nil
}
