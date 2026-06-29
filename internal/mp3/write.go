package mp3

import (
	"context"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// Plan computes the byte-level rewrite that turns the original file into the
// edited media. It is preservation-first: only the front ID3v2 tag is
// re-rendered (at the source's version, with unchanged and unmodelled frames
// kept), the MPEG audio is copied verbatim, and any trailing legacy containers
// are preserved unless explicitly stripped.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("mp3: edited media has no MP3 native document")
	}

	legacyPresent := len(d.ape) > 0 || len(d.id3v1) > 0

	tagsChanged := !base.Tags.Equal(edited.Tags)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	stripLegacy := opts.Legacy == core.LegacyStrip
	legacyChange := stripLegacy && legacyPresent

	report := core.WriteReport{Format: core.FormatMP3, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy (so SaveAsFile/
	// WriteTo still produce a whole file) flagged NoOp so SaveBack skips it. Explicit
	// padding requests run the front-tag renderer below so a padding-only edit can take
	// effect. A chapters-only edit (CHAP/CTOC) must defeat the no-op gate too.
	if !tagsChanged && !picturesChanged && !chaptersChanged && !legacyChange && !opts.PaddingExplicit {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}
	// Re-check the ID3 CTOC count at the codec boundary. Only a chapter edit re-renders
	// the CTOC, so unchanged chapters can keep their source frames.
	if chaptersChanged {
		if err := id3.CheckChapterCount(edited.Chapters); err != nil {
			return nil, err
		}
	}

	// Choose the ID3v2 version (preserve the source's; the format default for a
	// brand-new tag) and rebuild the frame list.
	srcTag := d.id3
	if srcTag == nil {
		srcTag = id3.NewEmpty(core.DefaultID3Version(core.FormatMP3))
	}
	version := srcTag.WriteVersion()
	newFrames, info := id3.RebuildFrames(srcTag.Frames(), base.Tags, edited.Tags, version,
		id3.StructuredEdit{
			Pictures: edited.Pictures, PicturesChanged: picturesChanged,
			Chapters: edited.Chapters, ChaptersChanged: chaptersChanged,
		}, id3.WriteOpts{Multi: opts.ID3Multi, NumericGenre: opts.NumericGenre})
	if err := id3.CheckSize(version, newFrames, bits.DefaultLimits.MaxElements); err != nil {
		return nil, err
	}

	// Size and render the front ID3v2 tag, dropping it entirely when no frame survives (an
	// edit that clears every frame, or a --legacy strip on a tagless file) rather than
	// fabricating an empty, padding-only container. The drop-empty-tag policy lives in the
	// shared id3.RenderFrontTag so MP3 and AAC cannot diverge.
	ft := id3.RenderFrontTag(srcTag, version, newFrames, info, opts.Padding, d.id3Len,
		d.id3 != nil, tagsChanged, picturesChanged, len(edited.Pictures), chaptersChanged, len(edited.Chapters))
	report.PaddingAfter = ft.Padding
	report.Operations = append(report.Operations, ft.Operations...)
	report.Warnings = append(report.Warnings, ft.Warnings...)
	// Compare the rendered front-tag size with the source region. When padding is the
	// only request, a changed size is the edit.
	regionDiffers := int64(len(ft.Bytes)) != d.id3Len
	if regionDiffers && !tagsChanged && !picturesChanged && !chaptersChanged && !legacyChange {
		report.Operations = append(report.Operations, core.PaddingOp(d.id3Len, int64(len(ft.Bytes))-ft.Padding, ft.Padding))
	}

	// Assemble the output: the new ID3v2 tag (when any), the verbatim audio, then the
	// preserved (or stripped) trailing legacy containers.
	audioLen := d.audioEnd - d.audioStart
	var segs []bits.Segment
	if ft.Bytes != nil {
		segs = append(segs, bits.Lit(ft.Bytes))
	}
	segs = append(segs, bits.Copy(d.audioStart, audioLen))

	apeLen := int64(len(d.ape))
	id3v1Len := int64(len(d.id3v1))
	if stripLegacy {
		if apeLen > 0 {
			report.Operations = append(report.Operations, "APEv2 strip")
			apeLen = 0
		}
		if id3v1Len > 0 {
			report.Operations = append(report.Operations, "ID3v1 strip")
			id3v1Len = 0
		}
	} else {
		if apeLen > 0 {
			segs = append(segs, bits.Copy(d.apeOffset, apeLen))
			report.Operations = append(report.Operations, "APEv2 preservation")
		}
		if id3v1Len > 0 {
			segs = append(segs, bits.Copy(d.size-128, 128))
			report.Operations = append(report.Operations, "ID3v1 preservation")
		}
	}

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize

	result := buildResult(edited, d, ft.Tag, ft.Bytes, audioLen, apeLen, id3v1Len, newSize)
	// Surface ID3 rebuild losses the bytes cannot show. MP3 has no other tag container, so
	// an ID3 date drop or reduction is always a file-level loss.
	report.Warnings = id3.AppendRebuildWarnings(report.Warnings, info, result.Tags)
	// Collapse to a true no-op when the ID3 rebuild re-projected to base's values
	// (e.g. GENRE=17 -> Rock); a legacy strip stays a real write. DowngradeNoOp carries
	// the value-dropped warning forward so a dropped date still surfaces on a no-op.
	if np := core.DowngradeNoOp(core.FormatMP3, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), legacyChange || regionDiffers, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// buildResult constructs the post-write Media so the engine can return a
// Document without re-parsing. The frames actually written are re-projected, so
// the result equals a fresh parse of the bytes for the canonical view.
func buildResult(edited *core.Media, base *doc, newTag *id3.Tag, tagBytes []byte,
	audioLen, apeLen, id3v1Len, newSize int64) *core.Media {

	id3Len := int64(len(tagBytes))
	nd := &doc{
		id3:         newTag,
		id3Len:      id3Len,
		audioStart:  id3Len,
		audioEnd:    id3Len + audioLen,
		firstHeader: base.firstHeader,
		track:       base.track,
		size:        newSize,
	}
	if apeLen > 0 {
		nd.ape = slices.Clone(base.ape)
		nd.apeOffset = nd.audioEnd
		nd.apeTag = base.apeTag
	}
	if id3v1Len > 0 {
		nd.id3v1 = slices.Clone(base.id3v1)
	}
	proj := id3.Project(newTag)
	// Re-add the preserved legacy containers to the family view so the returned
	// document matches a fresh parse of the written bytes (conflicts recomputed
	// against the new ID3v2 values).
	families := append(proj.Families, legacyFamilies(proj.Tags, nd.id3v1, nd.apeTag)...)
	// Carry source-parse warnings forward, but drop a stale chapter-flatten note when the
	// written tag no longer flattens. AAC uses the same helper for the same front-tag path.
	warnings := id3.CarryChapterWarnings(edited.Warnings, proj.Warnings)
	return &core.Media{
		Format:     core.FormatMP3,
		Properties: edited.Properties.Clone(),
		Tags:       proj.Tags,
		Families:   families,
		Pictures:   core.ClonePictures(edited.Pictures),
		Chapters:   proj.Chapters,
		Warnings:   warnings,
		Native:     nd,
		Identity:   core.Identity{Size: newSize},
		AudioStart: nd.audioStart,
		AudioEnd:   nd.audioEnd,
	}
}
