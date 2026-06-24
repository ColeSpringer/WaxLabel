package aac

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// Plan computes the byte-level rewrite that turns the original file into the
// edited media. It is preservation-first: only the front ID3v2 tag is
// re-rendered (at the source's version, with unchanged and unmodelled frames
// kept), and the ADTS audio stream is copied verbatim. AAC has no secondary tag
// container, so the legacy policies are inert (nothing to strip or reconcile).
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("aac: edited media has no AAC native document")
	}

	tagsChanged := !base.Tags.Equal(edited.Tags)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)

	report := core.WriteReport{Format: core.FormatAAC, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy (so SaveAsFile/
	// WriteTo still produce a whole file) flagged NoOp so SaveBack skips it.
	if !tagsChanged && !picturesChanged {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// Choose the ID3v2 version (preserve the source's; the format default for a
	// brand-new tag) and rebuild the frame list.
	srcTag := d.id3
	if srcTag == nil {
		srcTag = id3.NewEmpty(core.DefaultID3Version(core.FormatAAC))
	}
	version := srcTag.WriteVersion()
	newFrames, info := id3.RebuildFrames(srcTag.Frames(), base.Tags, edited.Tags, version,
		edited.Pictures, picturesChanged, id3.WriteOpts{Multi: opts.ID3Multi, NumericGenre: opts.NumericGenre})
	if err := id3.CheckSize(version, newFrames); err != nil {
		return nil, err
	}

	// Size the tag and its padding. Reuse the original region in place when the
	// new content fits, so the audio offset (and file size) need not change.
	nonPad := id3.RenderedSize(newFrames)
	padSize := opts.Padding.ReuseOrTarget(d.id3Len, nonPad)
	tagBytes := id3.Render(version, newFrames, int(padSize))
	report.PaddingAfter = padSize

	if tagsChanged {
		report.Operations = append(report.Operations, "ID3v2 frame rewrite")
	}
	if picturesChanged {
		report.Operations = append(report.Operations, fmt.Sprintf("pictures: %d", len(edited.Pictures)))
	}
	if d.id3 == nil {
		report.Operations = append(report.Operations, fmt.Sprintf("ID3v2.%d tag creation", version))
	}
	if info.UsedV23Multi {
		report.Operations = append(report.Operations, "v2.3 multi-value NUL-separated storage")
		report.Warnings = core.Warn(report.Warnings, core.WarnID3MultiValue,
			"a multi-value field was written NUL-separated in ID3v2.3, a de-facto extension some readers do not split")
	}
	// Assemble the output: the new ID3v2 tag, then the verbatim ADTS stream.
	audioLen := d.audioEnd - d.audioStart
	segs := []bits.Segment{bits.Lit(tagBytes), bits.Copy(d.audioStart, audioLen)}

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize

	result := buildResult(edited, d, srcTag.WithFrames(newFrames), tagBytes, audioLen, newSize)
	// A year-anchored date with no numeric year has no v2.3 TYER/TORY representation and
	// was dropped; warn unless the value survives in another container (AAC has none, and
	// a new tag is v2.4 anyway, so this fires only on a preserved v2.3 tag that dropped a
	// date). See id3.AppendDroppedDateWarnings.
	report.Warnings = id3.AppendDroppedDateWarnings(report.Warnings, info, result.Tags)
	// Collapse to a true no-op when the ID3 rebuild re-projected to base's values; AAC has
	// no strip flag, so nothing structural forces the write. DowngradeNoOp carries the
	// value-dropped warning forward so a dropped date still surfaces on a no-op.
	if np := core.DowngradeNoOp(core.FormatAAC, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), false, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// buildResult constructs the post-write Media so the engine can return a
// Document without re-parsing. The frames actually written are re-projected, so
// the result equals a fresh parse of the output bytes for the canonical view.
func buildResult(edited *core.Media, base *doc, newTag *id3.Tag, tagBytes []byte, audioLen, newSize int64) *core.Media {
	id3Len := int64(len(tagBytes))
	nd := &doc{
		id3:        newTag,
		id3Len:     id3Len,
		audioStart: id3Len,
		audioEnd:   id3Len + audioLen,
		header:     base.header,
		track:      base.track,
		size:       newSize,
	}
	proj := id3.Project(newTag)
	return &core.Media{
		Format:     core.FormatAAC,
		Properties: edited.Properties.Clone(),
		Tags:       proj.Tags,
		Families:   proj.Families,
		Pictures:   core.ClonePictures(edited.Pictures),
		Warnings:   core.CloneWarnings(edited.Warnings),
		Native:     nd,
		Identity:   core.Identity{Size: newSize},
		AudioStart: nd.audioStart,
		AudioEnd:   nd.audioEnd,
	}
}
