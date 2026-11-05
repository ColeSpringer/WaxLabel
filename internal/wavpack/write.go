package wavpack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// Plan rebuilds the trailing APEv2 (and keeps ID3v1) while copying blocks verbatim.
// Delegates to [ape.PlanTrailingWrite]; only the plan's result is built here.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("wavpack: edited media has no WavPack native document")
	}
	w := ape.TrailingWrite{Format: core.FormatWavPack, Trailer: d.trailer, Size: d.size}
	return ape.PlanTrailingWrite(w, base, edited, opts, func(tp ape.TrailerPlan, _ int64) *core.Media {
		return buildResult(edited, d, tp)
	})
}

// buildResult builds the plan's result, re-projecting the written items.
func buildResult(edited *core.Media, base *doc, tp ape.TrailerPlan) *core.Media {
	proj := ape.Project(&ape.Tag{Items: tp.Items})
	return &core.Media{
		Format:     core.FormatWavPack,
		Properties: edited.Properties.Clone(),
		Tags:       proj.Tags,
		Pictures:   proj.Pictures,
		AudioStart: 0,
		AudioEnd:   base.trailer.Start,
	}
}
