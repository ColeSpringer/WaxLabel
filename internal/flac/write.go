package flac

import (
	"context"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// Plan builds the rewrite from source to edited media. Unchanged blocks and
// audio stay verbatim, then the tail, its legacy tags unless stripped. NoOp is
// true when nothing changed.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("flac: edited media has no FLAC native document")
	}

	legacyPresent := len(d.leadingID3) > 0 || d.layout.HasTags()

	changed := diffKeys(base.Tags, edited.Tags)
	vorbisChanged := len(changed) > 0
	// An explicit clear, or a cleared name, also removes what the file holds unread: owned
	// comments nothing reads and PICTURE blocks that did not decode.
	unread := vorbis.PlanUnread(d.comments, len(d.malformedPictureBlocks), edited.Tags, opts,
		!core.EqualChapters(base.Chapters, edited.Chapters), !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics))
	chaptersChanged, syncedLyricsChanged := unread.ChaptersChanged, unread.LyricsChanged
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures) || unread.DropBlocks
	// Chapters/synced lyrics live as Vorbis comments; either edit rewrites the block.
	commentsChanged := vorbisChanged || chaptersChanged || syncedLyricsChanged || len(unread.Comments) != len(d.comments)
	stripLegacy := opts.Legacy == core.LegacyStrip
	legacyChange := stripLegacy && legacyPresent
	// Vendor neutralization must bypass the no-op path even if comments are unchanged.
	newVendor, vendorChanged := vorbis.NeutralizeVendor(d.vendor, opts.StripEncoderStamp)

	report := core.WriteReport{Format: core.FormatFLAC, BytesBefore: edited.Identity.Size}

	// NoOpPlan: full copy for SaveAsFile/WriteTo; SaveBack skips. Explicit padding
	// still runs the serializer.
	if !commentsChanged && !picturesChanged && !legacyChange && !vendorChanged && !opts.PaddingExplicit {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	newComments := d.comments
	var rebuildInfo vorbis.RebuildInfo
	if commentsChanged {
		newComments, rebuildInfo = vorbis.Rebuild(unread.Comments, edited.Tags, changed, edited.Chapters, chaptersChanged, edited.SyncedLyrics, syncedLyricsChanged)
	}
	if err := vorbis.CheckCommentCount(len(newComments), d.origCommentCount); err != nil {
		return nil, err
	}

	newBlocks, ops, dupDropped := rebuildBlocks(d, newVendor, newComments, edited.Pictures, commentsChanged, vendorChanged, picturesChanged, !unread.DropBlocks)
	ops = append(ops, unread.Ops...)
	if vendorChanged {
		ops = append(ops, "vendor stamp neutralized")
	}
	if chaptersChanged && len(edited.Chapters) > 0 {
		// Suppress count on clear; matches ID3 codecs.
		ops = append(ops, fmt.Sprintf("chapters: %d", len(edited.Chapters)))
	}
	if syncedLyricsChanged && len(edited.SyncedLyrics) > 0 {
		ops = append(ops, fmt.Sprintf("synced lyrics: %d", len(edited.SyncedLyrics)))
	}
	if err := checkBlockSizes(newBlocks); err != nil {
		return nil, err
	}
	metaBytes, padSize, padClamped, blockCount := serializeMetadata(newBlocks, d, opts.Padding)
	if err := vorbis.CheckBlockCount(blockCount, len(d.blocks)); err != nil {
		return nil, err
	}

	origRegion := d.audioStart - (d.flacStart + 4)
	regionDiffers := int64(len(metaBytes)) != origRegion
	report.Operations = append(report.Operations, ops...)
	report.PaddingAfter = int64(padSize)
	if padClamped {
		report.Warnings = core.Warn(report.Warnings, core.WarnPaddingClamped,
			fmt.Sprintf("requested padding exceeded FLAC's %d-byte metadata-block limit and was clamped to it", maxBlockBody))
	}
	// Padding-only report: gate on commentsChanged (tags OR chapters), not vorbisChanged alone.
	// A region with more padding than the policy's Max is cut to it, which the plan says too.
	paddingOnly := !commentsChanged && !picturesChanged && !legacyChange && !vendorChanged
	if before := d.PaddingBytes(); regionDiffers && (paddingOnly || opts.Padding.Max > 0 && before > opts.Padding.Max && int64(padSize) < before) {
		report.Operations = append(report.Operations, core.PaddingOp(before, int64(padSize)))
	}

	var segs []bits.Segment
	newLeadingLen := d.flacStart
	if stripLegacy && len(d.leadingID3) > 0 {
		report.Operations = append(report.Operations, core.LeadingStripOp(int64(len(d.leadingID3)), d.flacStart))
		newLeadingLen = 0
	} else if len(d.leadingID3) > 0 {
		segs = append(segs, bits.Copy(0, d.flacStart))
		report.Operations = append(report.Operations, "leading ID3v2 preservation")
	}
	segs = append(segs, bits.Lit(slices.Clone(flacMagic)), bits.Lit(metaBytes))

	audioOutStart := newLeadingLen + 4 + int64(len(metaBytes))
	segs = append(segs, bits.Copy(d.audioStart, d.layout.Start-d.audioStart))
	// The junk is no legacy tag: --legacy strip keeps it.
	segs = append(segs, tail.Segments(d.layout, stripLegacy)...)
	report.Operations = append(report.Operations, tail.Operations(d.layout, stripLegacy)...)

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize

	// Clamp warnings before DowngradeNoOp; clamp keeps result != base so write proceeds.
	report.Warnings = vorbis.RebuildWarnings(report.Warnings, rebuildInfo)
	report.Warnings = append(report.Warnings, unread.Warnings...)

	result := buildResult(edited, d, newComments, audioOutStart, stripLegacy)
	if dupDropped {

		report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "Vorbis comment block", base.Tags, result.Tags, d.dupContent)
	}

	// Downgrade catches rebuild drops (e.g. empty strings). Legacy strip and vendor
	// neutralization stay real. ReuseInPlace can absorb a shorter vendor, or what the edit
	// removes unread, without changing region length, so those are structural. A region
	// the padding policy would resize is a change only when the padding was asked for:
	// an edit that is discarded does not rewrite the file to move its padding.
	if np := core.DowngradeNoOp(core.FormatFLAC, edited.Identity.Size, base, result, len(diffKeys(base.Tags, result.Tags)) == 0,
		legacyChange || vendorChanged || unread.Removes() || regionDiffers && opts.PaddingExplicit, report.Warnings); np != nil {
		return np, nil
	}

	return &core.WritePlan{
		Segments: segs,
		NoOp:     false,
		Report:   report,
		Result:   result,
	}, nil
}

// checkBlockSizes rejects bodies over the 24-bit length field ([vorbis.CheckBlockSize]).
func checkBlockSizes(blocks []block) error {
	for _, b := range blocks {
		if err := vorbis.CheckBlockSize(b.code, len(b.body)); err != nil {
			return err
		}
	}
	return nil
}

// rebuildBlocks builds the new block list (no padding). Untouched blocks keep raw
// bytes. commentsChanged covers tag or chapter edits; vendorChanged forces re-render
// with newVendor; keepUnreadable re-appends the PICTURE blocks that did not decode on a
// picture edit. Third return: an extra Vorbis comment block was dropped.
func rebuildBlocks(d *doc, newVendor string, newComments []comment, pictures []core.Picture, commentsChanged, vendorChanged, picturesChanged, keepUnreadable bool) ([]block, []string, bool) {
	var out []block
	var ops []string
	vorbisHandled := false
	dupDropped := false
	picturesEmitted := false
	// True when comment block came from newComments (already picture-comment-stripped).
	commentBlockReRendered := false

	// Picture edit re-emits native covers; drop stale METADATA_BLOCK_PICTURE comments
	// by forcing a comment re-render when the source had any.
	dropPictureComment := picturesChanged && len(d.commentPictures) > 0

	emitPictures := func() {
		for _, p := range pictures {
			out = append(out, block{code: blkPicture, body: renderPicture(p)})
		}
		picturesEmitted = true
	}

	for _, b := range d.blocks {
		switch b.code {
		case blkVorbisComment:
			// Only the first survives; hoist above the preserve gate so padding/picture/
			// legacy-only edits still collapse duplicates.
			if vorbisHandled {
				dupDropped = true
				continue
			}
			if !commentsChanged && !vendorChanged && !dropPictureComment {

				out = append(out, b.clone())
				vorbisHandled = true
				continue
			}

			out = append(out, block{code: blkVorbisComment, body: renderVorbisComment(newVendor, newComments)})
			vorbisHandled = true
			commentBlockReRendered = true
		case blkPicture:
			if picturesChanged {
				if !picturesEmitted {
					emitPictures()
				}
				continue
			}
			out = append(out, b.clone())
		case blkPadding:
			continue // padding appended per policy at end
		default:
			out = append(out, b.clone())
		}
	}

	if !vorbisHandled && len(newComments) > 0 {
		out = insertAfterStreamInfo(out, block{code: blkVorbisComment, body: renderVorbisComment(newVendor, newComments)})
		commentBlockReRendered = true
	}
	if picturesChanged && !picturesEmitted && len(pictures) > 0 {
		emitPictures()
	}
	// On picture edit, re-append undecodable native PICTURE bodies (never in media.Pictures).
	// May move past valid pictures; FLAC does not care about order beyond STREAMINFO-first.
	if picturesChanged && keepUnreadable {
		for _, body := range d.malformedPictureBlocks {
			out = append(out, block{code: blkPicture, body: slices.Clone(body)})
		}
	}
	// Comment re-render strips METADATA_BLOCK_PICTURE; materialize those covers unless
	// pictures were already re-emitted. Emit only commentPictures (not all pictures) so
	// native blocks cloned above are not duplicated.
	if commentBlockReRendered && !picturesChanged {
		for _, p := range d.commentPictures {
			out = append(out, block{code: blkPicture, body: renderPicture(p)})
		}
	}

	if commentsChanged {
		ops = append(ops, "Vorbis comment rewrite")
	}
	if picturesChanged {
		ops = append(ops, fmt.Sprintf("pictures: %d block(s)", len(pictures)))
	}
	return out, ops, dupDropped
}

// paddingBlocks fills exactly budget bytes (4-byte header + body per block),
// splitting when over the 24-bit body limit. Each step leaves ≥4 bytes for the
// next header; caller ensures budget >= 4.
func paddingBlocks(budget int) []block {
	var out []block
	for budget > 4+maxBlockBody {
		body := min(budget-8, maxBlockBody) // reserve header for following block
		out = append(out, block{code: blkPadding, body: make([]byte, body)})
		budget -= 4 + body
	}
	return append(out, block{code: blkPadding, body: make([]byte, budget-4)})
}

// insertAfterStreamInfo inserts b after STREAMINFO (index 0).
func insertAfterStreamInfo(blocks []block, b block) []block {
	if len(blocks) == 0 {
		return []block{b}
	}
	return slices.Insert(blocks, 1, b)
}

// serializeMetadata renders blocks plus trailing PADDING per policy. ReuseInPlace
// fills the original region exactly when content fits, or leaves the policy's Max of
// a region with more free space than that. count is the blocks written,
// STREAMINFO and padding among them.
func serializeMetadata(blocks []block, d *doc, pol core.PaddingPolicy) (out []byte, padSize int, clamped bool, count int) {
	nonPad := 0
	for _, b := range blocks {
		nonPad += 4 + len(b.body)
	}
	origRegion := d.audioStart - (d.flacStart + 4)

	// Reuse only if leftover padding ≥ Min; else ClampTarget (also floors to Min).
	var padBlocks []block
	left := origRegion - (int64(nonPad) + 4)
	switch reuse := pol.ReuseInPlace && left >= 0 && left >= pol.Min; {
	case reuse && pol.Max > 0 && left > pol.Max:
		// More free space than the policy allows: keep Max of it.
		padBlocks = paddingBlocks(int(pol.Max) + 4)
	case reuse:
		// Fill region exactly; may need several PADDING blocks. Region-derived, no clamp warn.
		padBlocks = paddingBlocks(int(origRegion - int64(nonPad)))
	default:
		// ClampTarget floors to Min. Body ≤ 24-bit; larger Target clamps (int64 compare).
		t := pol.ClampTarget()
		if t > int64(maxBlockBody) {
			t = int64(maxBlockBody)
			clamped = true
		}
		// Target 0: no PADDING block (valid FLAC).
		if t > 0 {
			padBlocks = []block{{code: blkPadding, body: make([]byte, int(t))}}
		}
	}
	for _, pb := range padBlocks {
		padSize += len(pb.body)
	}

	all := append(slices.Clone(blocks), padBlocks...)
	for i, b := range all {
		out = append(out, renderBlock(b.code, i == len(all)-1, b.body)...)
	}
	return out, padSize, clamped, len(all)
}

// buildResult builds the plan's result: the canonical view a fresh parse of the output reads, and
// its essence, which moves with the frames to audioStart and keeps the tail as strip copies it.
func buildResult(edited *core.Media, base *doc, newComments []comment, audioStart int64, strip bool) *core.Media {
	tags, _ := projectComments(newComments)
	m := &core.Media{
		Format:       core.FormatFLAC,
		Properties:   edited.Properties.Clone(),
		Tags:         tags,
		Pictures:     core.ClonePictures(edited.Pictures),
		Chapters:     vorbis.ProjectChapters(newComments),
		SyncedLyrics: vorbis.ProjectSyncedLyrics(newComments),
	}
	base.layout.SetMovedEssence(m, base.audioStart, audioStart, strip)
	core.SyncTextItems(m)
	return m
}
