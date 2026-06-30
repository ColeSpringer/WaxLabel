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

// Plan computes the byte-level rewrite that turns the original source into the
// edited media. It is preservation-first: the native blocks are the base, only
// the fields that actually changed are re-rendered, and the audio frames plus
// any legacy ID3 are copied verbatim. The returned plan's Report describes
// exactly what Execute will do, and a semantic no-op yields NoOp=true.
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
	// Chapters (CHAPTERxxx) and synced lyrics (SYNCEDLYRICS) are stored as Vorbis comments,
	// so an edit to either rewrites the comment block just like a tag edit does.
	commentsChanged := vorbisChanged || chaptersChanged || syncedLyricsChanged
	stripLegacy := opts.Legacy == core.LegacyStrip
	legacyChange := stripLegacy && legacyPresent
	// Vendor neutralization is a real metadata edit even when the comment list is unchanged.
	// It must bypass the no-op fast path so the comment block is rendered with the neutral
	// vendor string.
	newVendor, vendorChanged := vorbis.NeutralizeVendor(d.vendor, opts.StripEncoderStamp)

	report := core.WriteReport{Format: core.FormatFLAC, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. NoOpPlan emits a full verbatim copy (so
	// SaveAsFile and WriteTo still produce a whole file) flagged NoOp so SaveBack
	// skips it. Explicit padding requests run the serializer below so a padding-only edit
	// can take effect.
	if !commentsChanged && !picturesChanged && !legacyChange && !vendorChanged && !opts.PaddingExplicit {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	newComments := d.comments
	if commentsChanged {
		newComments = rebuildComments(d.comments, edited.Tags, changed, edited.Chapters, chaptersChanged, edited.SyncedLyrics, syncedLyricsChanged)
	}

	newBlocks, ops := rebuildBlocks(d, newVendor, newComments, edited.Pictures, commentsChanged, vendorChanged, picturesChanged)
	if vendorChanged {
		ops = append(ops, "vendor stamp neutralized")
	}
	if chaptersChanged && len(edited.Chapters) > 0 {
		// Suppress the count line on a clear (the "Vorbis comment rewrite" op already
		// records the change); matches the ID3 codecs' count gate.
		ops = append(ops, fmt.Sprintf("chapters: %d", len(edited.Chapters)))
	}
	if syncedLyricsChanged && len(edited.SyncedLyrics) > 0 {
		ops = append(ops, fmt.Sprintf("synced lyrics: %d", len(edited.SyncedLyrics)))
	}
	if err := checkBlockSizes(newBlocks); err != nil {
		return nil, err
	}
	metaBytes, padSize, finalBlocks, padClamped := serializeMetadata(newBlocks, d, opts.Padding)
	// Compare the rendered metadata size with the source region. When padding is the only
	// request, a changed size is the edit.
	origRegion := d.audioStart - (d.flacStart + 4)
	regionDiffers := int64(len(metaBytes)) != origRegion
	report.Operations = append(report.Operations, ops...)
	report.PaddingAfter = int64(padSize)
	if padClamped {
		report.Warnings = core.Warn(report.Warnings, core.WarnPaddingClamped,
			fmt.Sprintf("requested padding exceeded FLAC's %d-byte metadata-block limit and was clamped to it", maxBlockBody))
	}
	// When padding is the only change, report it explicitly. Gate on commentsChanged (tags
	// OR chapters), not just vorbisChanged: a chapters-only edit grows the comment block, so
	// testing vorbisChanged alone would mislabel that growth as a padding-only change.
	if regionDiffers && !commentsChanged && !picturesChanged && !legacyChange && !vendorChanged {
		// For a padding-only edit, newContent is the region minus its new padding.
		report.Operations = append(report.Operations, core.PaddingOp(origRegion, int64(len(metaBytes))-int64(padSize), int64(padSize)))
	}

	// Assemble the output as segments: optional leading ID3, the fLaC marker
	// and new metadata, the verbatim audio, and optional trailing ID3v1.
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

	newTrailingLen := int64(len(d.trailingID3v1))
	if stripLegacy && len(d.trailingID3v1) > 0 {
		report.Operations = append(report.Operations, "trailing ID3v1 strip")
		newTrailingLen = 0
	} else if len(d.trailingID3v1) > 0 {
		segs = append(segs, bits.Copy(d.audioEnd, newTrailingLen))
		report.Operations = append(report.Operations, "trailing ID3v1 preservation")
	}

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize

	result := buildResult(edited, d, newVendor, finalBlocks, newComments, newLeadingLen, audioOutStart, audioLen, newTrailingLen, newSize)

	// FLAC stores Vorbis values verbatim, so this downgrade only catches values the rebuild
	// dropped, such as empty strings. A legacy strip or vendor neutralization remains a real
	// write. ReuseInPlace padding can absorb a shorter vendor without changing the region
	// length, so vendorChanged must be part of the structural-change flag.
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

// checkBlockSizes rejects any block whose body exceeds the 24-bit length field,
// which would otherwise be silently truncated into a corrupt file. Oversized
// pictures (the realistic case - hi-res cover art) report ErrPictureTooLarge.
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

// rebuildBlocks assembles the new metadata block list (excluding padding),
// preserving order and raw bytes for everything untouched. commentsChanged is set when a
// tag OR chapter edit changed the Vorbis comment list (chapters are CHAPTERxxx comments).
// vendorChanged forces the Vorbis comment block to be re-rendered with newVendor even when
// the comment values themselves are unchanged.
func rebuildBlocks(d *doc, newVendor string, newComments []comment, pictures []core.Picture, commentsChanged, vendorChanged, picturesChanged bool) ([]block, []string) {
	var out []block
	var ops []string
	vorbisHandled := false
	picturesEmitted := false
	// commentBlockReRendered records that the comment block was re-rendered from the (already
	// picture-comment-stripped) newComments, so the materialization below can fire on the real
	// re-render rather than hand-mirroring its trigger condition (which previously drifted and
	// dropped a cover on a vendor-only edit).
	commentBlockReRendered := false

	// A picture edit re-emits every cover as a native block (emitPictures below). If the source
	// carried a comment-embedded cover (METADATA_BLOCK_PICTURE), a verbatim clone of the comment
	// block would keep that now-stale picture comment alongside the fresh native block - a
	// duplicate cover. Force the comment block to re-render from the (already-stripped) comment
	// list in that case so the picture comment is dropped exactly once.
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
			if !commentsChanged && !vendorChanged && !dropPictureComment {
				// Nothing touched the comment block, so preserve every comment block verbatim,
				// including extras outside the canonical projection.
				out = append(out, b.clone())
				vorbisHandled = true
				continue
			}
			// Re-render the first comment block and drop any extra comment blocks. That is the
			// documented collapse for tag/chapter edits and vendor neutralization.
			if vorbisHandled {
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
			continue // a single padding block is appended per policy at the end
		default:
			out = append(out, b.clone())
		}
	}

	// Create a Vorbis comment block if one is now needed but none existed.
	if !vorbisHandled && len(newComments) > 0 {
		out = insertAfterStreamInfo(out, block{code: blkVorbisComment, body: renderVorbisComment(newVendor, newComments)})
		commentBlockReRendered = true
	}
	if picturesChanged && !picturesEmitted && len(pictures) > 0 {
		emitPictures()
	}
	// Materialize comment-sourced covers when the comment block was re-rendered (which strips the
	// METADATA_BLOCK_PICTURE entry) but pictures were not separately re-emitted. Without this a
	// tag/chapter/vendor edit would silently drop the cover. Keying off the actual re-render flag
	// (rather than re-deriving its trigger) keeps the two from drifting - a vendor-only edit
	// re-renders the block too, which a hand-mirrored condition once missed. Native PICTURE blocks
	// were already cloned verbatim above; emit only the comment-sourced subset (not all of
	// pictures) so those native blocks are not re-rendered and duplicated.
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
	return out, ops
}

// paddingBlocks returns PADDING blocks that together occupy exactly budget bytes (each
// block's 4-byte header plus its body), splitting across several blocks when budget exceeds
// one block's 24-bit body limit. It fills a reuse-in-place region whose existing padding is
// larger than a single block can represent, preserving the audio offset. Each loop iteration
// leaves at least a 4-byte header's room for the remainder, so no unrepresentable 1-3 byte
// tail is stranded; the caller's reuse guard ensures budget >= 4.
func paddingBlocks(budget int) []block {
	var out []block
	for budget > 4+maxBlockBody {
		body := budget - 8 // reserve a 4-byte header for the following block
		if body > maxBlockBody {
			body = maxBlockBody
		}
		out = append(out, block{code: blkPadding, body: make([]byte, body)})
		budget -= 4 + body
	}
	return append(out, block{code: blkPadding, body: make([]byte, budget-4)})
}

// insertAfterStreamInfo inserts b right after the STREAMINFO block (index 0),
// keeping STREAMINFO first as the format requires.
func insertAfterStreamInfo(blocks []block, b block) []block {
	if len(blocks) == 0 {
		return []block{b}
	}
	return slices.Insert(blocks, 1, b)
}

// serializeMetadata renders all blocks plus a trailing PADDING block into the
// metadata-region bytes, sizing padding per policy. When ReuseInPlace is set
// and the new content fits the original region, padding fills it exactly so the
// audio offset does not change. It also returns the final block list (padding
// included) so the post-write native view matches the bytes.
func serializeMetadata(blocks []block, d *doc, pol core.PaddingPolicy) (out []byte, padSize int, all []block, clamped bool) {
	nonPad := 0
	for _, b := range blocks {
		nonPad += 4 + len(b.body)
	}
	origRegion := d.audioStart - (d.flacStart + 4)

	// Reuse the existing region in place only while the leftover padding is still
	// >= Min; an explicit --padding floor otherwise falls to ClampTarget (which also
	// floors to Min) so a too-small region grows. The first bound (content fits the
	// region) short-circuits, so origRegion-(nonPad+4) is non-negative before the
	// floor comparison. Min defaults to 0, leaving the prior reuse behavior unchanged.
	var padBlocks []block
	if pol.ReuseInPlace && int64(nonPad)+4 <= origRegion && origRegion-(int64(nonPad)+4) >= pol.Min {
		// Reuse the existing region in place: fill it EXACTLY so the audio offset does not move.
		// A region holding more than one block's worth of padding (legal - a file may carry
		// several PADDING blocks, each individually <16 MiB) is filled with several padding
		// blocks rather than one oversized, invalid one. This padding is region-derived, not a
		// user request, so it never raises the clamped warning, even spanning multiple blocks.
		padBlocks = paddingBlocks(int(origRegion - int64(nonPad)))
	} else {
		// Grow with fresh padding (ClampTarget floors to Min). A single PADDING block body cannot
		// exceed the 24-bit block-length field, so a larger Target is clamped to it and reported
		// (the smaller-than-asked padding is not silent). The compare is in int64 space so a
		// >2 GiB request is caught rather than wrapping to a small value on a 32-bit int.
		t := pol.ClampTarget()
		if t > int64(maxBlockBody) {
			t = int64(maxBlockBody)
			clamped = true
		}
		padBlocks = []block{{code: blkPadding, body: make([]byte, int(t))}}
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

// buildResult constructs the post-write Media so the engine can return a
// Document without re-parsing (needed for the io.Writer destination).
func buildResult(edited *core.Media, orig *doc, newVendor string, newBlocks []block, newComments []comment,
	newLeadingLen, audioStart, audioLen, trailingLen, newSize int64) *core.Media {

	nd := &doc{
		vendor:     newVendor,
		comments:   newComments,
		streamInfo: orig.streamInfo,
		flacStart:  newLeadingLen,
		audioStart: audioStart,
		audioEnd:   audioStart + audioLen,
	}
	if newLeadingLen > 0 {
		nd.leadingID3 = slices.Clone(orig.leadingID3)
	}
	if trailingLen > 0 {
		nd.trailingID3v1 = slices.Clone(orig.trailingID3v1)
	}
	// Reflect the actually written blocks (including the padding block) so the
	// native view matches the file.
	nd.blocks = slices.Clone(newBlocks)

	// Derive the result's canonical view from the comments actually written, so
	// the returned Document equals a re-parse of the bytes - in particular a key
	// set present-but-empty (which Vorbis cannot store) is correctly absent here.
	// Building the Media directly also avoids cloning edited's (shared) native.
	tags, families := projectComments(newComments)
	return &core.Media{
		Format:       core.FormatFLAC,
		Properties:   edited.Properties.Clone(),
		Tags:         tags,
		Families:     families,
		Pictures:     core.ClonePictures(edited.Pictures),
		Chapters:     projectChapters(newComments),
		SyncedLyrics: projectSyncedLyrics(newComments),
		// Recompute inherited-encoder warnings from the vendor and comments that were written.
		// Other warnings carry verbatim because CHAPTERxxx and SYNCEDLYRICS projections emit
		// no warnings today; if that changes, this must rederive those warnings too.
		Warnings:   vorbis.CarryEncoderWarnings(edited.Warnings, newVendor, toVorbis(newComments)),
		Native:     nd,
		Identity:   core.Identity{Size: newSize},
		AudioStart: nd.audioStart,
		AudioEnd:   nd.audioEnd,
	}
}
