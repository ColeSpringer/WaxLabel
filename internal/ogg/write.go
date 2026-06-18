package ogg

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan computes the byte-level rewrite that turns the original stream into the
// edited media. It is preservation-first and packet-preserving: only the comment
// header is rebuilt, the identification and (for Vorbis) setup headers are kept
// verbatim, and every audio packet payload is copied unchanged. The BOS page is
// copied as-is; the comment/setup headers are re-paginated. If that changes the
// header-region page count, the following audio pages are renumbered - their
// sequence number rewritten and CRC patched - without re-reading the audio.
func (c Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("ogg: edited media has no Ogg native document")
	}

	changed := vorbis.DiffKeys(base.Tags, edited.Tags)
	tagsChanged := len(changed) > 0
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)

	report := core.WriteReport{Format: d.format, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. Emit a full verbatim copy (so SaveAsFile and
	// WriteTo still produce a whole file) but flag NoOp so SaveBack skips it. This
	// runs before the chained/alignment guards: copying a file unchanged is always
	// safe, even for streams we will not rewrite.
	if !tagsChanged && !picturesChanged {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// An actual rewrite is refused for stream shapes we cannot edit safely.
	if d.chained {
		return nil, fmt.Errorf("%w: refusing to rewrite a chained or multiplexed Ogg stream", waxerr.ErrChainedStream)
	}
	if !d.clean {
		return nil, fmt.Errorf("%w: Ogg header and audio are not cleanly page-aligned; cannot rewrite safely", waxerr.ErrInvalidData)
	}

	// Rebuild the comment list: tag comments (minimal-change) followed by one
	// METADATA_BLOCK_PICTURE comment per edited picture.
	newComments := d.comments
	if tagsChanged {
		newComments = vorbis.Rebuild(d.comments, edited.Tags, changed)
		report.Operations = append(report.Operations, "rewrote comments")
	}
	full := slices.Clone(newComments)
	for _, p := range edited.Pictures {
		full = append(full, vorbis.Comment{
			Name:  pictureComment,
			Value: base64.StdEncoding.EncodeToString(vorbis.RenderPicture(p)),
		})
	}
	if picturesChanged {
		report.Operations = append(report.Operations, fmt.Sprintf("pictures: %d", len(edited.Pictures)))
	}

	// Re-paginate the header tail (everything after the BOS id page): the new
	// comment packet, plus the Vorbis setup packet preserved verbatim.
	tailPackets := [][]byte{d.buildCommentPacket(full)}
	if d.kind == kindVorbis {
		tailPackets = append(tailPackets, d.setupPacket)
	}
	tailBytes, tailPages := paginate(d.serial, 1, tailPackets)
	newHeaderPages := 1 + tailPages
	delta := newHeaderPages - d.headerPages

	newAudioStart := d.page0Len + int64(len(tailBytes))
	shift := newAudioStart - d.audioStart

	// Page 0 (the id packet, alone) is copied verbatim; then the new header tail.
	segs := []bits.Segment{bits.Copy(0, d.page0Len), bits.Lit(tailBytes)}

	newAudioPages := make([]apage, len(d.audioPages))
	if delta == 0 {
		// Header page count unchanged: audio page sequence numbers are unaffected,
		// so the whole audio region copies verbatim.
		segs = append(segs, bits.Copy(d.audioStart, d.audioEnd-d.audioStart))
		for i, ap := range d.audioPages {
			ap.off += shift
			newAudioPages[i] = ap
		}
	} else {
		// Page count changed: every following page shifts by delta, so each audio
		// page's sequence number is rebased and its CRC patched in place - the body
		// is still copied byte-for-byte, only the 8 header bytes change. The patch
		// bytes for all pages share one backing slice (a single allocation, not one
		// per page); each page's literal segment is a distinct 8-byte window into it.
		patches := make([]byte, 8*len(d.audioPages))
		for i, ap := range d.audioPages {
			newSeq := ap.seq + uint32(delta)
			newCRC := patchCRC(ap.crc, ap.seq, newSeq, ap.total)
			p8 := patches[i*8 : i*8+8 : i*8+8]
			binary.LittleEndian.PutUint32(p8[0:4], newSeq)
			binary.LittleEndian.PutUint32(p8[4:8], newCRC)
			segs = append(segs,
				bits.Copy(ap.off, 18),             // "OggS" .. serial number
				bits.Lit(p8),                      // sequence number + CRC
				bits.Copy(ap.off+26, ap.total-26), // segment table + body
			)
			ap.off += shift
			ap.seq = newSeq
			ap.crc = newCRC
			newAudioPages[i] = ap
		}
		report.Operations = append(report.Operations, fmt.Sprintf("renumbered %d audio pages", len(d.audioPages)))
	}

	if d.trailingLen > 0 {
		segs = append(segs, bits.Copy(d.audioEnd, d.trailingLen))
	}

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize
	report.PaddingAfter = int64(len(d.commentPad))

	result := buildResult(edited, d, newComments, newAudioPages, newHeaderPages, newAudioStart, shift, newSize)
	return &core.WritePlan{
		Segments: segs,
		NoOp:     false,
		Report:   report,
		Result:   result,
	}, nil
}

// buildCommentPacket frames a comment list as a full comment header packet: the
// per-codec signature, the comment-list body, and the trailing framing bit
// (Vorbis) or preserved padding (Opus).
func (d *doc) buildCommentPacket(comments []vorbis.Comment) []byte {
	body := vorbis.RenderCommentList(d.vendor, comments)
	if d.kind == kindVorbis {
		pkt := make([]byte, 0, len(vorbisComment)+len(body)+1)
		pkt = append(pkt, vorbisComment...)
		pkt = append(pkt, body...)
		return append(pkt, 0x01) // framing bit
	}
	pkt := make([]byte, 0, len(opusTags)+len(body)+len(d.commentPad))
	pkt = append(pkt, opusTags...)
	pkt = append(pkt, body...)
	return append(pkt, d.commentPad...)
}

// buildResult constructs the post-write Media so the engine can return a
// Document without re-parsing. The audio pages keep their bodies (and thus the
// essence) and only shift in offset (and, when renumbered, sequence/CRC), so the
// result equals a fresh parse of the written bytes.
func buildResult(edited *core.Media, base *doc, newComments []vorbis.Comment,
	newAudioPages []apage, newHeaderPages int, newAudioStart, shift, newSize int64) *core.Media {

	nd := &doc{
		format:      base.format,
		kind:        base.kind,
		serial:      base.serial,
		vendor:      base.vendor,
		comments:    newComments,
		pictures:    core.ClonePictures(edited.Pictures),
		idPacket:    base.idPacket,
		setupPacket: base.setupPacket,
		commentPad:  base.commentPad,
		page0Len:    base.page0Len,
		headerPages: newHeaderPages,
		audioStart:  newAudioStart,
		audioPages:  newAudioPages,
		audioEnd:    base.audioEnd + shift,
		trailingLen: base.trailingLen,
		clean:       true,
	}
	tags, families := vorbis.Project(newComments)
	media := &core.Media{
		Format:     base.format,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Families:   families,
		Pictures:   core.ClonePictures(edited.Pictures),
		Warnings:   core.CloneWarnings(edited.Warnings),
		Native:     nd,
		Identity:   core.Identity{Size: newSize},
		AudioStart: newAudioStart,
		AudioEnd:   nd.audioEnd,
	}
	for _, ap := range newAudioPages {
		media.AudioRanges = append(media.AudioRanges, [2]int64{ap.bodyOff(), ap.bodyOff() + ap.bodyLen})
	}
	return media
}
