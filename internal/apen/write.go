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
	return ape.PlanTrailingWrite(w, base, edited, opts, func(tp ape.TrailerPlan, _, newSize int64) *core.Media {
		return buildResult(edited, d, tp, newSize)
	})
}

// buildResult builds post-write Media without re-parsing.
func buildResult(edited *core.Media, base *doc, tp ape.TrailerPlan, newSize int64) *core.Media {
	nd := &doc{
		trailer: tp.Result(base.trailer.Start, base.trailer),
		header:  base.header,
		track:   base.track,
		size:    newSize,
	}
	proj := ape.Project(nd.trailer.Tag)
	return &core.Media{
		Format:     core.FormatMonkeysAudio,
		Properties: edited.Properties.Clone(),
		Tags:       proj.Tags,
		Families:   append(proj.Families, ape.LegacyFamilies(proj.Tags, nd.trailer.ID3v1)...),
		Pictures:   proj.Pictures,
		Warnings:   ape.CarryWarnings(edited.Warnings, proj, tp.Items, nd.trailer.ID3v1),
		Native:     nd,
		Identity:   core.Identity{Size: newSize},
		AudioStart: 0,
		AudioEnd:   nd.trailer.Start,
	}
}
