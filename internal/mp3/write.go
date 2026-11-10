package mp3

import (
	"context"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/tail"
)

// Plan builds the rewrite. Front ID3v2 re-rendered (source version; untouched
// frames kept) and the duplicate tags behind it dropped; the bytes before the first
// frame and the MPEG frames copied, then the tail, its legacy tags unless stripped.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("mp3: edited media has no MP3 native document")
	}

	legacyPresent := d.layout.HasTags()

	tagsChanged := !base.Tags.Equal(edited.Tags) || !core.EqualTextItems(base, edited)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	syncedLyricsChanged := !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics)
	stripLegacy := opts.Legacy == core.LegacyStrip
	legacyChange := stripLegacy && legacyPresent

	report := core.WriteReport{Format: core.FormatMP3, BytesBefore: edited.Identity.Size}

	// Preserve the source version; format default for a new tag or one replacing an ignored
	// tag, which holds nothing a rewrite keeps. Above the fast path so the encoding predicate
	// can see the source frames.
	version := id3.WriteVersionOr(d.id3, core.FormatMP3)
	srcTag := d.id3
	if srcTag == nil || srcTag.Ignored() != "" {
		srcTag = id3.NewEmpty(version)
	}
	// One WriteOpts for both the predicate and the rebuild, so they cannot disagree.
	wopts := id3.WriteOpts{Multi: opts.ID3Multi, NumericGenre: opts.NumericGenre}
	// Write-encoding (--numeric-genre) is invisible to tag compare; force a rewrite.

	encodingRewrite := id3.EncodingRewriteNeeded(srcTag, edited.Tags, wopts)

	// NoOp: full copy; SaveBack skips. Explicit padding and chapters/SYLT-only defeat this.

	if !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !legacyChange && !opts.PaddingExplicit && !encodingRewrite {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}
	// Re-check the ID3 CTOC count at the codec boundary. Only a chapter edit re-renders
	// the CTOC, so unchanged chapters can keep their source frames.
	if chaptersChanged {
		if err := id3.CheckChapterCount(edited.Chapters); err != nil {
			return nil, err
		}
	}

	// Rebuild the frame list.
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

	// Render front tag; drop empty (shared id3.RenderFrontTag policy).

	ft := id3.RenderFrontTag(srcTag, version, newFrames, info, opts.Padding, d.id3Len,
		d.id3, tagsChanged, picturesChanged, len(edited.Pictures), chaptersChanged, len(edited.Chapters),
		syncedLyricsChanged, len(edited.SyncedLyrics))
	report.PaddingAfter = ft.Padding
	report.Operations = append(report.Operations, ft.Operations...)
	report.Warnings = append(report.Warnings, ft.Warnings...)
	if len(d.dups) > 0 {
		report.Operations = append(report.Operations, "duplicate ID3v2 drop")
	}
	// Compare the rendered front-tag size with the source region. When padding is the
	// only request, a changed size is the edit.
	regionDiffers := int64(len(ft.Bytes)) != d.id3Len
	if regionDiffers && !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !legacyChange {
		report.Operations = append(report.Operations, core.PaddingOp(id3.FrontTagPadding(d.id3), ft.Padding))
	}
	if encodingRewrite {
		report.Operations = append(report.Operations, core.EncodingRewriteOp("genre"))
	}

	// Assemble the output: the new ID3v2 tag (when any), the bytes between the duplicates and the
	// first frame and the verbatim audio, then the tail, its legacy tags preserved or stripped.
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
	// Surface ID3 rebuild losses the bytes cannot show. MP3 has no other tag container, so
	// an ID3 date drop or reduction is always a file-level loss.
	report.Warnings = id3.AppendRebuildWarnings(report.Warnings, info, result.Tags)
	report.Warnings = id3.AppendMalformedTailDropped(report.Warnings, d.id3, ft.Bytes != nil)
	report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "ID3v2 tag", base.Tags, result.Tags, id3.DupLosses(d.dups, base, result, srcTag.Frames(), newFrames))
	// Downgrade when rebuild re-projects to base. Legacy strip and encoding rewrite stay real.

	if np := core.DowngradeNoOp(core.FormatMP3, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), legacyChange || regionDiffers || encodingRewrite, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// buildResult constructs the plan's result. The written frames are re-projected, so the
// canonical view equals a fresh parse of the bytes, and the essence moves to just past the new tag
// of tagLen bytes and the bytes kept before the first frame.
func buildResult(edited *core.Media, base *doc, newTag *id3.Tag, tagLen int64, strip bool) *core.Media {
	proj := id3.Project(newTag)
	m := &core.Media{
		Format:       core.FormatMP3,
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
