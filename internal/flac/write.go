package flac

import (
	"context"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan builds the rewrite from source to edited media. Unchanged blocks and
// audio (plus legacy ID3) stay verbatim. NoOp is true when nothing changed.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("flac: edited media has no FLAC native document")
	}

	legacyPresent := len(d.leadingID3) > 0 || len(d.trailingID3v1) > 0

	changed := diffKeys(base.Tags, edited.Tags)
	vorbisChanged := len(changed) > 0
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	syncedLyricsChanged := !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics)
	// Chapters/synced lyrics live as Vorbis comments; either edit rewrites the block.
	commentsChanged := vorbisChanged || chaptersChanged || syncedLyricsChanged
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
		newComments, rebuildInfo = rebuildComments(d.comments, edited.Tags, changed, edited.Chapters, chaptersChanged, edited.SyncedLyrics, syncedLyricsChanged)
	}

	newBlocks, ops, commentsReRendered, dupDropped := rebuildBlocks(d, newVendor, newComments, edited.Pictures, commentsChanged, vendorChanged, picturesChanged)
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
	metaBytes, padSize, finalBlocks, padClamped := serializeMetadata(newBlocks, d, opts.Padding)

	origRegion := d.audioStart - (d.flacStart + 4)
	regionDiffers := int64(len(metaBytes)) != origRegion
	report.Operations = append(report.Operations, ops...)
	report.PaddingAfter = int64(padSize)
	if padClamped {
		report.Warnings = core.Warn(report.Warnings, core.WarnPaddingClamped,
			fmt.Sprintf("requested padding exceeded FLAC's %d-byte metadata-block limit and was clamped to it", maxBlockBody))
	}
	// Padding-only report: gate on commentsChanged (tags OR chapters), not vorbisChanged alone.
	if regionDiffers && !commentsChanged && !picturesChanged && !legacyChange && !vendorChanged {

		report.Operations = append(report.Operations, core.PaddingOp(origRegion, int64(len(metaBytes))-int64(padSize), int64(padSize)))
	}

	var segs []bits.Segment
	newLeadingLen := d.flacStart
	if stripLegacy && len(d.leadingID3) > 0 {
		report.Operations = append(report.Operations, fmt.Sprintf("leading ID3v2 strip (%d bytes)", len(d.leadingID3)))
		newLeadingLen = 0
	} else if len(d.leadingID3) > 0 {
		segs = append(segs, bits.Copy(0, d.flacStart))
		report.Operations = append(report.Operations, "leading ID3v2 preservation")
	}
	segs = append(segs, bits.Lit(slices.Clone(flacMagic)), bits.Lit(metaBytes))

	audioLen := d.audioEnd - d.audioStart
	audioOutStart := newLeadingLen + 4 + int64(len(metaBytes))
	segs = append(segs, bits.Copy(d.audioStart, audioLen))

	// Trailing junk is not a legacy tag; --legacy does not strip it.
	if d.trailingJunk > 0 {
		segs = append(segs, bits.Copy(d.audioEnd, d.trailingJunk))
	}

	newTrailingLen := int64(len(d.trailingID3v1))
	if stripLegacy && len(d.trailingID3v1) > 0 {
		report.Operations = append(report.Operations, "trailing ID3v1 strip")
		newTrailingLen = 0
	} else if len(d.trailingID3v1) > 0 {
		segs = append(segs, bits.Copy(d.audioEnd+d.trailingJunk, newTrailingLen))
		report.Operations = append(report.Operations, "trailing ID3v1 preservation")
	}

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize

	// Clamp warnings before DowngradeNoOp; clamp keeps result != base so write proceeds.
	report.Warnings = vorbis.RebuildWarnings(report.Warnings, rebuildInfo)

	// Verbatim comment clone still holds comment-embedded covers for a chained edit.
	keepCommentPics := !commentsReRendered && !picturesChanged
	result := buildResult(edited, d, newVendor, finalBlocks, newComments, newLeadingLen, audioOutStart, audioLen, newTrailingLen, newSize, opts.Limits.MaxElements, keepCommentPics)
	if dupDropped {

		report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "Vorbis comment block", result.Tags, d.dupContent)
	}

	// Downgrade catches rebuild drops (e.g. empty strings). Legacy strip and vendor
	// neutralization stay real. ReuseInPlace can absorb a shorter vendor without
	// changing region length, so vendorChanged is structural.
	if np := core.DowngradeNoOp(core.FormatFLAC, edited.Identity.Size, base, result, len(diffKeys(base.Tags, result.Tags)) == 0, legacyChange || regionDiffers || vendorChanged, report.Warnings); np != nil {
		return np, nil
	}

	return &core.WritePlan{
		Segments: segs,
		NoOp:     false,
		Report:   report,
		Result:   result,
	}, nil
}

// checkBlockSizes rejects bodies over the 24-bit length field. Oversized pictures
// report ErrPictureTooLarge.
func checkBlockSizes(blocks []block) error {
	for _, b := range blocks {
		if len(b.body) <= maxBlockBody {
			continue
		}
		if b.code == blkPicture {
			return fmt.Errorf("%w: picture block is %s (max %s)",
				waxerr.ErrPictureTooLarge, bits.HumanBytes(int64(len(b.body))), bits.HumanBytes(int64(maxBlockBody)))
		}
		return fmt.Errorf("%w: %s block is %s, exceeding the 24-bit limit %s",
			waxerr.ErrInvalidData, blockName(b.code), bits.HumanBytes(int64(len(b.body))), bits.HumanBytes(int64(maxBlockBody)))
	}
	return nil
}

// rebuildBlocks builds the new block list (no padding). Untouched blocks keep raw
// bytes. commentsChanged covers tag or chapter edits; vendorChanged forces re-render
// with newVendor. Third return: comment block re-rendered (vs cloned). Fourth: an
// extra Vorbis comment block was dropped.
func rebuildBlocks(d *doc, newVendor string, newComments []comment, pictures []core.Picture, commentsChanged, vendorChanged, picturesChanged bool) ([]block, []string, bool, bool) {
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
	if picturesChanged {
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
	return out, ops, commentBlockReRendered, dupDropped
}

// paddingBlocks fills exactly budget bytes (4-byte header + body per block),
// splitting when over the 24-bit body limit. Each step leaves ≥4 bytes for the
// next header; caller ensures budget >= 4.
func paddingBlocks(budget int) []block {
	var out []block
	for budget > 4+maxBlockBody {
		body := budget - 8 // reserve header for following block
		if body > maxBlockBody {
			body = maxBlockBody
		}
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
// fills the original region exactly when content fits. Returns final blocks
// (with padding) for the post-write native view.
func serializeMetadata(blocks []block, d *doc, pol core.PaddingPolicy) (out []byte, padSize int, all []block, clamped bool) {
	nonPad := 0
	for _, b := range blocks {
		nonPad += 4 + len(b.body)
	}
	origRegion := d.audioStart - (d.flacStart + 4)

	// Reuse only if leftover padding ≥ Min; else ClampTarget (also floors to Min).
	var padBlocks []block
	if pol.ReuseInPlace && int64(nonPad)+4 <= origRegion && origRegion-(int64(nonPad)+4) >= pol.Min {
		// Fill region exactly; may need several PADDING blocks. Region-derived, no clamp warn.
		padBlocks = paddingBlocks(int(origRegion - int64(nonPad)))
	} else {
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

	all = append(slices.Clone(blocks), padBlocks...)
	for i, b := range all {
		out = append(out, renderBlock(b.code, i == len(all)-1, b.body)...)
	}
	return out, padSize, all, clamped
}

// buildResult builds post-write Media without re-parsing (needed for io.Writer).
func buildResult(edited *core.Media, orig *doc, newVendor string, newBlocks []block, newComments []comment,
	newLeadingLen, audioStart, audioLen, trailingLen, newSize int64, maxElements int, keepCommentPics bool) *core.Media {

	nd := &doc{
		vendor:     newVendor,
		comments:   newComments,
		streamInfo: orig.streamInfo,
		flacStart:  newLeadingLen,
		audioStart: audioStart,
		audioEnd:   audioStart + audioLen,

		trailingJunk: orig.trailingJunk,
		// Written output re-appended these; keep for chained picture edit without re-parse.
		malformedPictureBlocks: cloneByteSlices(orig.malformedPictureBlocks),
	}
	if newLeadingLen > 0 {
		nd.leadingID3 = slices.Clone(orig.leadingID3)
	}
	// Carry comment covers only while they still live in a verbatim-cloned block.
	if keepCommentPics {
		nd.commentPictures = core.ClonePictures(orig.commentPictures)
	}
	if trailingLen > 0 {
		nd.trailingID3v1 = slices.Clone(orig.trailingID3v1)
	}

	nd.blocks = slices.Clone(newBlocks)

	tags, families := projectComments(newComments)

	legacyFams, legacyOpaque := flacLegacyFamilies(tags, nd.leadingID3, nd.trailingID3v1, maxElements)
	families = append(families, legacyFams...)
	return &core.Media{
		Format:              core.FormatFLAC,
		Properties:          edited.Properties.Clone(),
		Tags:                tags,
		Families:            families,
		LegacyOpaqueContent: legacyOpaque,
		Pictures:            core.ClonePictures(edited.Pictures),
		Chapters:            projectChapters(newComments),
		SyncedLyrics:        projectSyncedLyrics(newComments),
		// Encoder warnings from written vendor/comments; other warnings carry for now.
		Warnings:   vorbis.CarryEncoderWarnings(edited.Warnings, newVendor, toVorbis(newComments)),
		Native:     nd,
		Identity:   core.Identity{Size: newSize},
		AudioStart: nd.audioStart,
		AudioEnd:   nd.audioEnd,
	}
}
