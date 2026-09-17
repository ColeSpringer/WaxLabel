package aac

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// Plan builds a preservation-first rewrite: re-render the front ID3v2 tag
// (source version; unchanged/unmodelled frames kept), copy ADTS verbatim.
// No secondary tag container; legacy policies are inert.
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
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	syncedLyricsChanged := !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics)

	report := core.WriteReport{Format: core.FormatAAC, BytesBefore: edited.Identity.Size}

	// Preserve source version; format default for a new tag. Above fast path so
	// the encoding predicate can see source frames.
	srcTag := d.id3
	if srcTag == nil {
		srcTag = id3.NewEmpty(core.DefaultID3Version(core.FormatAAC))
	}
	version := srcTag.WriteVersion()
	// Same WriteOpts for predicate and rebuild so they cannot disagree.
	wopts := id3.WriteOpts{Multi: opts.ID3Multi, NumericGenre: opts.NumericGenre}
	// --numeric-genre changes storage, not the value; tag Equal cannot see it.
	encodingRewrite := id3.EncodingRewriteNeeded(srcTag, edited.Tags, wopts)

	// NoOpPlan: verbatim copy for SaveAsFile/WriteTo; SaveBack skips. Explicit
	// padding and chapter/synced-lyrics-only edits defeat the gate.
	if !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !opts.PaddingExplicit && !encodingRewrite {
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
	ft := id3.RenderFrontTag(srcTag, version, newFrames, info, opts.Padding, d.id3Len,
		d.id3 != nil, tagsChanged, picturesChanged, len(edited.Pictures), chaptersChanged, len(edited.Chapters),
		syncedLyricsChanged, len(edited.SyncedLyrics))
	report.PaddingAfter = ft.Padding
	report.Operations = append(report.Operations, ft.Operations...)
	report.Warnings = append(report.Warnings, ft.Warnings...)
	// Padding-only edit: size change is the edit.
	regionDiffers := int64(len(ft.Bytes)) != d.id3Len
	if regionDiffers && !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged {
		report.Operations = append(report.Operations, core.PaddingOp(d.id3Len, int64(len(ft.Bytes))-ft.Padding, ft.Padding))
	}
	if encodingRewrite {
		report.Operations = append(report.Operations, core.EncodingRewriteOp("genre"))
	}

	audioLen := d.audioEnd - d.audioStart
	var segs []bits.Segment
	if ft.Bytes != nil {
		segs = append(segs, bits.Lit(ft.Bytes))
	}
	segs = append(segs, bits.Copy(d.audioStart, audioLen))

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize

	result := buildResult(edited, d, ft.Tag, ft.Bytes, audioLen, newSize)
	// Rebuild losses not visible in bytes (e.g. v2.3 date precision). Fresh tags are v2.4.
	report.Warnings = id3.AppendRebuildWarnings(report.Warnings, info, result.Tags)
	report.Warnings = id3.AppendMalformedTailDropped(report.Warnings, d.id3)
	// Collapse when rebuild matches base; encoding rewrite alone still forces write.
	if np := core.DowngradeNoOp(core.FormatAAC, edited.Identity.Size, base, result, base.Tags.Equal(result.Tags), regionDiffers || encodingRewrite, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// buildResult builds post-write Media without re-parsing. Frames are re-projected
// so the result matches a fresh parse of the output.
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
	// Carry parse warnings; drop stale chapter-flatten when the written tag no longer flattens.
	warnings := id3.CarryProjectionWarnings(edited.Warnings, proj.Warnings)
	return &core.Media{
		Format:       core.FormatAAC,
		Properties:   edited.Properties.Clone(),
		Tags:         proj.Tags,
		Families:     proj.Families,
		Pictures:     core.ClonePictures(edited.Pictures),
		Chapters:     core.ChaptersOpenedPastDuration(proj.Chapters, nd.track.Duration),
		SyncedLyrics: proj.SyncedLyrics,
		Warnings:     warnings,
		Native:       nd,
		Identity:     core.Identity{Size: newSize},
		AudioStart:   nd.audioStart,
		AudioEnd:     nd.audioEnd,
	}
}
