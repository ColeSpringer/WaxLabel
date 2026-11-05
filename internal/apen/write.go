package apen

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// Plan rebuilds the trailing APEv2 (and keeps ID3v1) while copying header and frames
// verbatim. Delegates to [ape.PlanTrailingWrite].
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("apen: edited media has no Monkey's Audio native document")
	}
	w := ape.TrailingWrite{Format: core.FormatMonkeysAudio, Trailer: d.trailer, Size: d.size}
	return ape.PlanTrailingWrite(w, base, edited, opts, func(tp ape.TrailerPlan, _ int64) *core.Media {
		return buildResult(edited, d, tp)
	})
}

// buildResult builds the plan's result.
func buildResult(edited *core.Media, base *doc, tp ape.TrailerPlan) *core.Media {
	proj := ape.Project(&ape.Tag{Items: tp.Items})
	return &core.Media{
		Format:     core.FormatMonkeysAudio,
		Properties: edited.Properties.Clone(),
		Tags:       proj.Tags,
		Pictures:   proj.Pictures,
		AudioStart: 0,
		AudioEnd:   base.trailer.Start,
	}
}
