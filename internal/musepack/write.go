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
	// Chapters sit in the copied stream. Capability gate refuses first; this is the
	// bypass backstop (worded differently so tests can tell which refused).
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

// buildResult builds post-write Media without re-parsing.
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
	// Same MaxElements as the caller's parse so result matches a fresh reparse.
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
