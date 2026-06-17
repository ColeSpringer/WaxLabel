package flac

import (
	"context"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
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
	// Reconcile/UpdateExisting need an ID3 parser to migrate values, which
	// arrives with the MP3 milestone. Until then, fail loudly when there is a
	// legacy container to act on rather than silently leaving it untouched (a
	// Canonical-preset save would otherwise look like it canonicalized when it
	// did nothing). With no legacy container present they are correctly no-ops.
	if (opts.Legacy == core.LegacyReconcile || opts.Legacy == core.LegacyUpdateExisting) && legacyPresent {
		return nil, fmt.Errorf("%w: legacy policy %q is not yet implemented for FLAC and a legacy tag is present",
			waxerr.ErrUnsupportedTag, opts.Legacy)
	}

	changed := diffKeys(base.Tags, edited.Tags)
	vorbisChanged := len(changed) > 0
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	stripLegacy := opts.Legacy == core.LegacyStrip
	legacyChange := stripLegacy && legacyPresent

	report := core.WriteReport{Format: core.FormatFLAC, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. NoOpPlan emits a full verbatim copy (so
	// SaveAsFile and WriteTo still produce a whole file) flagged NoOp so SaveBack
	// skips it.
	if !vorbisChanged && !picturesChanged && !legacyChange {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	newComments := d.comments
	if vorbisChanged {
		newComments = rebuildComments(d.comments, base.Tags, edited.Tags, changed)
	}

	newBlocks, ops := rebuildBlocks(d, newComments, edited.Pictures, vorbisChanged, picturesChanged)
	if err := checkBlockSizes(newBlocks); err != nil {
		return nil, err
	}
	metaBytes, padSize, finalBlocks := serializeMetadata(newBlocks, d, opts.Padding)
	report.Operations = append(report.Operations, ops...)
	report.PaddingAfter = int64(padSize)

	// Assemble the output as segments: optional leading ID3, the fLaC marker
	// and new metadata, the verbatim audio, and optional trailing ID3v1.
	var segs []bits.Segment
	newLeadingLen := d.flacStart
	if stripLegacy && len(d.leadingID3) > 0 {
		report.Operations = append(report.Operations, fmt.Sprintf("stripped leading ID3v2 (%d bytes)", len(d.leadingID3)))
		newLeadingLen = 0
	} else if len(d.leadingID3) > 0 {
		segs = append(segs, bits.Copy(0, d.flacStart))
		report.Operations = append(report.Operations, "preserved leading ID3v2")
	}
	segs = append(segs, bits.Lit(slices.Clone(flacMagic)), bits.Lit(metaBytes))

	audioLen := d.audioEnd - d.audioStart
	audioOutStart := newLeadingLen + 4 + int64(len(metaBytes))
	segs = append(segs, bits.Copy(d.audioStart, audioLen))

	newTrailingLen := int64(len(d.trailingID3v1))
	if stripLegacy && len(d.trailingID3v1) > 0 {
		report.Operations = append(report.Operations, "stripped trailing ID3v1")
		newTrailingLen = 0
	} else if len(d.trailingID3v1) > 0 {
		segs = append(segs, bits.Copy(d.audioEnd, newTrailingLen))
		report.Operations = append(report.Operations, "preserved trailing ID3v1")
	}

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize

	result := buildResult(edited, d, finalBlocks, newComments, newLeadingLen, audioOutStart, audioLen, newTrailingLen, newSize)

	return &core.WritePlan{
		Segments: segs,
		NoOp:     false,
		Report:   report,
		Result:   result,
	}, nil
}

// checkBlockSizes rejects any block whose body exceeds the 24-bit length field,
// which would otherwise be silently truncated into a corrupt file. Oversized
// pictures (the realistic case — hi-res cover art) report ErrPictureTooLarge.
func checkBlockSizes(blocks []block) error {
	for _, b := range blocks {
		if len(b.body) <= maxBlockBody {
			continue
		}
		if b.code == blkPicture {
			return fmt.Errorf("%w: picture block is %d bytes (max %d)",
				waxerr.ErrPictureTooLarge, len(b.body), maxBlockBody)
		}
		return fmt.Errorf("%w: %s block is %d bytes, exceeding the 24-bit limit %d",
			waxerr.ErrInvalidData, blockName(b.code), len(b.body), maxBlockBody)
	}
	return nil
}

// rebuildBlocks assembles the new metadata block list (excluding padding),
// preserving order and raw bytes for everything untouched.
func rebuildBlocks(d *doc, newComments []comment, pictures []core.Picture, vorbisChanged, picturesChanged bool) ([]block, []string) {
	var out []block
	var ops []string
	vorbisHandled := false
	picturesEmitted := false

	emitPictures := func() {
		for _, p := range pictures {
			out = append(out, block{code: blkPicture, body: renderPicture(p)})
		}
		picturesEmitted = true
	}

	for _, b := range d.blocks {
		switch b.code {
		case blkVorbisComment:
			if !vorbisChanged {
				// Tags weren't edited (e.g. a picture- or legacy-only change):
				// preserve every comment block verbatim, including extras whose
				// tags are not in the canonical projection.
				out = append(out, b.clone())
				vorbisHandled = true
				continue
			}
			// Tags were edited: render the canonical comments into the first
			// block and drop any extras (the documented collapse).
			if vorbisHandled {
				continue
			}
			out = append(out, block{code: blkVorbisComment, body: renderVorbisComment(d.vendor, newComments)})
			vorbisHandled = true
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
		out = insertAfterStreamInfo(out, block{code: blkVorbisComment, body: renderVorbisComment(d.vendor, newComments)})
	}
	if picturesChanged && !picturesEmitted && len(pictures) > 0 {
		emitPictures()
	}

	if vorbisChanged {
		ops = append(ops, "rewrote Vorbis comments")
	}
	if picturesChanged {
		ops = append(ops, fmt.Sprintf("pictures: %d block(s)", len(pictures)))
	}
	return out, ops
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
func serializeMetadata(blocks []block, d *doc, pol core.PaddingPolicy) (out []byte, padSize int, all []block) {
	nonPad := 0
	for _, b := range blocks {
		nonPad += 4 + len(b.body)
	}
	origRegion := d.audioStart - (d.flacStart + 4)

	if pol.ReuseInPlace && int64(nonPad)+4 <= origRegion {
		padSize = int(origRegion - int64(nonPad) - 4)
	} else {
		padSize = int(pol.ClampTarget())
	}
	padSize = clampInt(padSize, 0, maxBlockBody)

	all = append(slices.Clone(blocks), block{code: blkPadding, body: make([]byte, padSize)})
	for i, b := range all {
		out = append(out, renderBlock(b.code, i == len(all)-1, b.body)...)
	}
	return out, padSize, all
}

// buildResult constructs the post-write Media so the engine can return a
// Document without re-parsing (needed for the io.Writer destination).
func buildResult(edited *core.Media, orig *doc, newBlocks []block, newComments []comment,
	newLeadingLen, audioStart, audioLen, trailingLen, newSize int64) *core.Media {

	nd := &doc{
		vendor:     orig.vendor,
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
	// the returned Document equals a re-parse of the bytes — in particular a key
	// set present-but-empty (which Vorbis cannot store) is correctly absent here.
	// Building the Media directly also avoids cloning edited's (shared) native.
	tags, families := projectComments(newComments)
	return &core.Media{
		Format:     core.FormatFLAC,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Families:   families,
		Pictures:   core.ClonePictures(edited.Pictures),
		Warnings:   core.CloneWarnings(edited.Warnings),
		Native:     nd,
		Identity:   core.Identity{Size: newSize},
		AudioStart: nd.audioStart,
		AudioEnd:   nd.audioEnd,
	}
}

func clampInt(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}
