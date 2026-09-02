package musepack

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan computes the byte-level rewrite that turns the original file into the edited
// media. It is preservation-first: the Musepack stream is copied verbatim and only
// the containers around it are rebuilt - the APEv2 tag from the edited model, plus
// any legacy ID3v1 or leading ID3v2 exactly as they were found.
//
// A legacy strip drops both ID3 containers. The APEv2 tag is the native store, not a
// legacy one, so the strip never touches it. The write itself is
// [ape.PlanTrailingWrite], shared with WavPack and Monkey's Audio; Musepack is the
// only one of the three with a leading region, which that helper takes as an input.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("musepack: edited media has no Musepack native document")
	}
	// The chapter packets are part of the stream this plan copies verbatim. The editor's
	// capability gate refuses a chapter change before it reaches here; this is the
	// backstop for a caller that bypasses it, worded apart from the gate so a test can
	// tell which refused.
	if !core.EqualChapters(base.Chapters, edited.Chapters) {
		return nil, fmt.Errorf("%w: the Musepack writer copies the chapter packets verbatim and cannot apply a chapter change", waxerr.ErrUnsupportedTag)
	}
	w := ape.TrailingWrite{
		Format: core.FormatMusepack, Trailer: d.trailer, Size: d.size, Leading: d.leadingID3,
	}
	return ape.PlanTrailingWrite(w, base, edited, opts, func(tp ape.TrailerPlan, newLeadingLen, newSize int64) *core.Media {
		return buildResult(edited, d, tp, newLeadingLen, newSize, opts.Limits.MaxElements)
	})
}

// buildResult constructs the post-write Media so the engine can return a Document
// without re-parsing.
func buildResult(edited *core.Media, base *doc, tp ape.TrailerPlan, newLeadingLen, newSize int64, maxElements int) *core.Media {
	leading := base.leadingID3
	if newLeadingLen == 0 {
		leading = nil
	}
	shift := newLeadingLen - base.streamAt
	nd := &doc{
		leadingID3: leading,
		streamAt:   newLeadingLen,
		trailer:    tp.Result(base.trailer.Start+shift, base.trailer),
		header:     base.header,
		track:      base.track,
		size:       newSize,
		chapters:   core.CloneChapters(base.chapters),
	}
	if base.ctEnd > base.ctStart {
		nd.ctStart, nd.ctEnd = base.ctStart+shift, base.ctEnd+shift
	}
	proj := ape.Project(nd.trailer.Tag)
	fams := append(proj.Families, ape.LegacyFamilies(proj.Tags, nd.trailer.ID3v1)...)
	// The parse limits, not the defaults: the result document must equal a fresh parse of
	// the written bytes under the SAME options the caller used.
	leadingFams, opaque := leadingID3Families(proj.Tags, leading, maxElements)
	warnings := ape.CarryWarnings(edited.Warnings, proj, tp.Items, nd.trailer.ID3v1)
	if newLeadingLen == 0 {
		warnings = core.WarningsWithoutCode(warnings, core.WarnStrayLeadingID3)
	}
	return &core.Media{
		Format:              core.FormatMusepack,
		Properties:          edited.Properties.Clone(),
		Tags:                proj.Tags,
		Families:            append(fams, leadingFams...),
		Pictures:            proj.Pictures,
		Chapters:            core.CloneChapters(nd.chapters),
		LegacyOpaqueContent: opaque,
		Warnings:            warnings,
		Native:              nd,
		Identity:            core.Identity{Size: newSize},
		AudioStart:          nd.streamAt,
		AudioEnd:            nd.trailer.Start,
	}
}
