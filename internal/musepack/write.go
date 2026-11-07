package musepack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan copies the stream verbatim and rebuilds surrounding containers via
// [tail.PlanTrailingWrite]. Legacy strip drops the ID3 containers and Lyrics3 blocks, never APEv2.
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
	w := tail.TrailingWrite{
		Format: core.FormatMusepack, Trailer: d.trailer, Leading: d.leadingID3, StreamAt: d.streamAt,
	}
	return tail.PlanTrailingWrite(w, base, edited, opts, func(tp tail.TrailerPlan, audioStart, audioEnd int64) *core.Media {
		return buildResult(edited, d, tp, audioStart, audioEnd)
	})
}

// buildResult builds the plan's result. The output's essence is [audioStart, audioEnd): the stream,
// chapters included, copied verbatim, and any junk it keeps.
func buildResult(edited *core.Media, base *doc, tp tail.TrailerPlan, audioStart, audioEnd int64) *core.Media {
	proj := ape.Project(&ape.Tag{Items: tp.Items})
	return &core.Media{
		Format:     core.FormatMusepack,
		Properties: edited.Properties.Clone(),
		Tags:       proj.Tags,
		Pictures:   proj.Pictures,
		Chapters:   core.CloneChapters(base.chapters),
		AudioStart: audioStart,
		AudioEnd:   audioEnd,
	}
}
