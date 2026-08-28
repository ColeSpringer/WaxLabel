package wavpack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// Plan computes the byte-level rewrite that turns the original file into the edited
// media. It is preservation-first: the WavPack blocks are copied verbatim and only
// the tail is rebuilt - the APEv2 tag from the edited model (unknown and non-text
// items kept, with their flags), then any legacy ID3v1 exactly as it was found.
//
// The write itself is [ape.PlanTrailingWrite], shared with the other containers whose
// metadata is a trailing APEv2 tag; only the post-write document is built here, since
// the native type is this package's.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("wavpack: edited media has no WavPack native document")
	}
	w := ape.TrailingWrite{Format: core.FormatWavPack, Trailer: d.trailer, Size: d.size}
	return ape.PlanTrailingWrite(w, base, edited, opts, func(tp ape.TrailerPlan, _, newSize int64) *core.Media {
		return buildResult(edited, d, tp, newSize)
	})
}

// buildResult constructs the post-write Media so the engine can return a Document
// without re-parsing. The items actually written are re-projected, so the result
// equals a fresh parse of the output bytes.
func buildResult(edited *core.Media, base *doc, tp ape.TrailerPlan, newSize int64) *core.Media {
	nd := &doc{
		trailer: tp.Result(base.trailer.Start, base.trailer),
		header:  base.header,
		track:   base.track,
		size:    newSize,
	}
	proj := ape.Project(nd.trailer.Tag)
	return &core.Media{
		Format:     core.FormatWavPack,
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
