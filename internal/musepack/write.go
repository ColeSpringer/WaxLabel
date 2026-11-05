package musepack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan copies the stream verbatim and rebuilds surrounding containers via
// [ape.PlanTrailingWrite]. Legacy strip drops both ID3 containers, never APEv2.
// Musepack is the only APE-backed codec with a Leading region.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("musepack: edited media has no Musepack native document")
	}
	// Chapters sit in the copied stream. The capability gate refuses first; this
	// check catches a bypass, worded differently so tests can tell which refused.
	if !core.EqualChapters(base.Chapters, edited.Chapters) {
		return nil, fmt.Errorf("%w: the Musepack writer copies the chapter packets verbatim and cannot apply a chapter change", waxerr.ErrUnsupportedTag)
	}
	w := ape.TrailingWrite{
		Format: core.FormatMusepack, Trailer: d.trailer, Size: d.size, Leading: d.leadingID3,
	}
	return ape.PlanTrailingWrite(w, base, edited, opts, func(tp ape.TrailerPlan, newLeadingLen int64) *core.Media {
		return buildResult(edited, d, tp, newLeadingLen)
	})
}

// buildResult builds the plan's result. The stream, chapters included, is copied verbatim
// and moves with the leading tag.
func buildResult(edited *core.Media, base *doc, tp ape.TrailerPlan, newLeadingLen int64) *core.Media {
	proj := ape.Project(&ape.Tag{Items: tp.Items})
	return &core.Media{
		Format:     core.FormatMusepack,
		Properties: edited.Properties.Clone(),
		Tags:       proj.Tags,
		Pictures:   proj.Pictures,
		Chapters:   core.CloneChapters(base.chapters),
		AudioStart: newLeadingLen,
		AudioEnd:   base.trailer.Start + newLeadingLen - base.streamAt,
	}
}
