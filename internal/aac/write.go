package aac

import (
	"context"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/tail"
)

// Plan builds a preservation-first rewrite: re-render the front ID3v2 tag
// (source version; unchanged/unmodelled frames kept) and drop the duplicate tags
// behind it, copy the bytes before the first frame and the ADTS frames verbatim,
// then the tail, its legacy tags unless stripped.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("aac: edited media has no AAC native document")
	}

	tagsChanged := !base.Tags.Equal(edited.Tags) || !core.EqualTextItems(base, edited)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	syncedLyricsChanged := !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics)
	stripLegacy := opts.Legacy == core.LegacyStrip
	legacyChange := stripLegacy && d.layout.HasTags()

	report := core.WriteReport{Format: core.FormatAAC, BytesBefore: edited.Identity.Size}

	// Preserve source version; format default for a new tag or one replacing an ignored
	// tag, which holds nothing a rewrite keeps. Above fast path so the encoding predicate
	// can see source frames.
	version := id3.WriteVersionOr(d.id3, core.FormatAAC)
	srcTag := d.id3
	if srcTag == nil || srcTag.Ignored() != "" {
		srcTag = id3.NewEmpty(version)
	}
	// Same WriteOpts for predicate and rebuild so they cannot disagree.
	wopts := id3.WriteOpts{Multi: opts.ID3Multi, NumericGenre: opts.NumericGenre}
	// --numeric-genre changes storage, not the value; tag Equal cannot see it.
	encodingRewrite := id3.EncodingRewriteNeeded(srcTag, edited.Tags, wopts)

	// NoOpPlan: verbatim copy for SaveAsFile/WriteTo; SaveBack skips. Explicit
	// padding and chapter/synced-lyrics-only edits defeat the gate.
	if !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !legacyChange && !opts.PaddingExplicit && !encodingRewrite {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}
	// CTOC count check only when chapters change (unchanged keep source frames).
	if chaptersChanged {
		if err := id3.CheckChapterCount(edited.Chapters); err != nil {
			return nil, err
		}
	}

	newFrames, info := id3.RebuildFrames(srcTag.Frames(), base.Tags, edited.Tags, version,
		id3.StructuredEdit{
			Pictures: edited.Pictures, PicturesChanged: picturesChanged,
			Chapters: edited.Chapters, ChaptersChanged: chaptersChanged,
			SyncedLyrics: edited.SyncedLyrics, SyncedLyricsChanged: syncedLyricsChanged,
			Comments: edited.Comments, CommentsChanged: !slices.Equal(base.Comments, edited.Comments),
			Lyrics: edited.Lyrics, LyricsChanged: !slices.Equal(base.Lyrics, edited.Lyrics),
			Carried:             opts.Carried,
			SyncedLyricsCleared: opts.SyncedLyricsCleared,
			MediaDuration:       edited.Properties.Duration(),
		}, wopts)
	if err := id3.CheckSize(version, newFrames, bits.DefaultLimits.MaxElements); err != nil {
		return nil, err
	}
	if err := id3.RebuildError(info); err != nil {
		return nil, err
	}

	// Drop empty tag entirely (shared id3.RenderFrontTag policy with MP3).
	ft := id3.RenderFrontTag(id3.FrontTagEdit{
		SrcTag: srcTag, Front: d.id3, Version: version, Frames: newFrames, Info: info, Padding: opts.Padding,
		SrcTagLen: d.id3Len, Gap: d.firstFrameOff - d.frontEnd,
		TagsChanged:     tagsChanged,
		PicturesChanged: picturesChanged, Pictures: len(edited.Pictures),
		ChaptersChanged: chaptersChanged, Chapters: len(edited.Chapters),
		SyncedLyricsChanged: syncedLyricsChanged, SyncedLyrics: len(edited.SyncedLyrics),
	})
	report.PaddingAfter = ft.Padding
	report.Operations = append(report.Operations, ft.Operations...)
	report.Warnings = append(report.Warnings, ft.Warnings...)
	if len(d.dups) > 0 {
		report.Operations = append(report.Operations, "duplicate ID3v2 drop")
	}
	// Padding-only edit: size change is the edit.
	regionDiffers := int64(len(ft.Bytes)) != d.id3Len
	if regionDiffers && !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !legacyChange {
		report.Operations = append(report.Operations, core.PaddingOp(id3.FrontTagPadding(d.id3), ft.Padding))
	}
	if encodingRewrite {
		report.Operations = append(report.Operations, core.EncodingRewriteOp("genre"))
	}

	var segs []bits.Segment
	if ft.Bytes != nil {
		segs = append(segs, bits.Lit(ft.Bytes))
	}
	segs = append(segs, bits.Copy(d.frontEnd, d.layout.Start-d.frontEnd))
	segs = append(segs, tail.Segments(d.layout, stripLegacy)...)
	report.Operations = append(report.Operations, tail.Operations(d.layout, stripLegacy)...)

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize

	result := buildResult(edited, d, ft.Tag, int64(len(ft.Bytes)), stripLegacy)
	// Rebuild losses not visible in bytes (e.g. v2.3 date precision). Fresh tags are v2.4.
	report.Warnings = id3.AppendRebuildWarnings(report.Warnings, info, result.Tags)
	report.Warnings = id3.AppendMalformedTailDropped(report.Warnings, d.id3, ft.Bytes != nil)
	report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "ID3v2 tag", base.Tags, result.Tags, id3.DupLosses(d.dups, base, result, srcTag.Frames(), newFrames))
	// Collapse when rebuild matches base; a legacy strip or encoding rewrite alone still forces write.
	if np := core.DowngradeNoOp(core.FormatAAC, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), legacyChange || regionDiffers || encodingRewrite, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// buildResult builds the plan's result. Frames are re-projected so the result matches a
// fresh parse of the output, whose essence starts past the new tag of tagLen bytes and the
// bytes kept before the first frame.
func buildResult(edited *core.Media, base *doc, newTag *id3.Tag, tagLen int64, strip bool) *core.Media {
	proj := id3.Project(newTag)
	m := &core.Media{
		Format:       core.FormatAAC,
		Properties:   edited.Properties.Clone(),
		Tags:         proj.Tags,
		Pictures:     core.ClonePictures(edited.Pictures),
		Chapters:     core.ChaptersOpenedPastDuration(proj.Chapters, base.track.Duration),
		SyncedLyrics: proj.SyncedLyrics,
		Comments:     proj.Comments,
		Lyrics:       proj.Lyrics,
	}
	base.layout.SetMovedEssence(m, base.firstFrameOff, tagLen+base.firstFrameOff-base.frontEnd, strip)
	core.SyncTextItems(m)
	return m
}
