package apen

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/tail"
)

// Plan rebuilds the trailing APEv2 while copying header and frames verbatim; legacy strip drops
// the ID3 containers and Lyrics3 blocks. Delegates to [tail.PlanTrailingWrite].
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("apen: edited media has no Monkey's Audio native document")
	}
	w := tail.TrailingWrite{Format: core.FormatMonkeysAudio, Trailer: d.trailer, Leading: d.leadingID3, StreamAt: d.streamAt}
	return tail.PlanTrailingWrite(w, base, edited, opts, func(tp tail.TrailerPlan, audioStart, audioEnd int64) *core.Media {
		return buildResult(edited, tp, audioStart, audioEnd)
	})
}

// buildResult builds the plan's result. The output's essence is [audioStart, audioEnd): the header
// and frames, copied verbatim, and any junk it keeps.
func buildResult(edited *core.Media, tp tail.TrailerPlan, audioStart, audioEnd int64) *core.Media {
	proj := ape.Project(&ape.Tag{Items: tp.Items})
	return &core.Media{
		Format:     core.FormatMonkeysAudio,
		Properties: edited.Properties.Clone(),
		Tags:       proj.Tags,
		Pictures:   proj.Pictures,
		AudioStart: audioStart,
		AudioEnd:   audioEnd,
	}
}
