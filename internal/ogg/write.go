package ogg

import (
	"bytes"
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
	chaptersChanged := !core.EqualChapters(base.Chapters, edited.Chapters)
	syncedLyricsChanged := !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics)
	// Vendor neutralization is a real metadata edit even when the comment list is unchanged.
	// It must bypass the no-op fast path so the comment packet is rendered with the neutral
	// vendor string.
	newVendor, vendorChanged := vorbis.NeutralizeVendor(d.vendor, opts.StripEncoderStamp)

	report := core.WriteReport{Format: d.format, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. Emit a full verbatim copy (so SaveAsFile and
	// WriteTo still produce a whole file) but flag NoOp so SaveBack skips it. This
	// runs before the chained/alignment guards: copying a file unchanged is always
	// safe, even for streams we will not rewrite. A chapters- or synced-lyrics-only edit
	// (CHAPTERxxx / SYNCEDLYRICS comments) must defeat the gate too.
	if !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !vendorChanged {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// An actual rewrite is refused for stream shapes we cannot edit safely.
	if d.chained {
		return nil, fmt.Errorf("%w: refusing to rewrite a chained or multiplexed Ogg stream", waxerr.ErrChainedStream)
	}
	if !d.clean {
		return nil, fmt.Errorf("%w: Ogg header and audio are not cleanly page-aligned; cannot rewrite safely", waxerr.ErrUnalignedStream)
	}

	// Rebuild the comment list: tag comments (minimal-change), owned CHAPTERxxx chapter and
	// SYNCEDLYRICS comments, then one METADATA_BLOCK_PICTURE comment per edited picture.
	// Chapters and synced lyrics are stored as Vorbis comments, so an edit to either rebuilds
	// the list like a tag edit.
	newComments := d.comments
	commentsChanged := tagsChanged || chaptersChanged || syncedLyricsChanged
	var rebuildInfo vorbis.RebuildInfo
	if commentsChanged {
		newComments, rebuildInfo = vorbis.Rebuild(d.comments, edited.Tags, changed, edited.Chapters, chaptersChanged, edited.SyncedLyrics, syncedLyricsChanged)
		report.Operations = append(report.Operations, "Vorbis comment rewrite")
	}
	if chaptersChanged && len(edited.Chapters) > 0 {
		// Suppress the count line on a clear (the "Vorbis comment rewrite" op already
		// records the change); matches the ID3 codecs' count gate.
		report.Operations = append(report.Operations, fmt.Sprintf("chapters: %d", len(edited.Chapters)))
	}
	if syncedLyricsChanged && len(edited.SyncedLyrics) > 0 {
		report.Operations = append(report.Operations, fmt.Sprintf("synced lyrics: %d", len(edited.SyncedLyrics)))
	}
	// Re-emit one METADATA_BLOCK_PICTURE comment per picture. edited.Pictures carries each cover's
	// stored MIME (media.Pictures is the stored set, not a sniffed projection), so re-rendering it
	// preserves an untouched cover's on-disk label; a newly added cover carries the type the editor
	// reconciled with its bytes on add. Clone only when there are covers to append (the common
	// tag/chapter edit has none): buildCommentPacket below only reads full, so aliasing newComments
	// when nothing is appended is safe.
	full := newComments
	if d.kind != kindFLAC && len(edited.Pictures) > 0 {
		full = slices.Clone(newComments)
		for _, p := range edited.Pictures {
			full = append(full, vorbis.Comment{
				Name:  vorbis.PictureComment,
				Value: base64.StdEncoding.EncodeToString(vorbis.RenderPicture(p)),
			})
		}
	}
	if picturesChanged {
		report.Operations = append(report.Operations, fmt.Sprintf("pictures: %d", len(edited.Pictures)))
	}
	if vendorChanged {
		report.Operations = append(report.Operations, "vendor stamp neutralized")
	}

	// Guard against emitting a comment header a default-limit reader would then refuse. Only a
	// picture can realistically push the comment packet past the alloc ceiling (tags, chapters, and
	// synced lyrics are all bounded well below it). Check the *whole* rebuilt comment packet, not
	// each cover individually: reassembleHeaders caps the summed comment packet on re-read, so
	// several near-limit covers that each fit but jointly overflow would otherwise produce a file
	// that fails to re-parse at the same limit. The write limit floors at the whole original comment
	// packet (origCommentPacketLen): those bytes were parsed within the (possibly raised) parse
	// limit, so an unrelated edit on a file whose covers were parsed under WithLimits stays writable
	// - and flooring to the whole packet (not the largest single cover, as the old per-cover floor
	// did) also lets a file that already held two big covers rewrite. An additive edit that grows
	// the packet past the floor on an above-limit file is still (correctly) rejected. Gate on the
	// write limit (opts.Limits), like the sibling MP4 check, not a hardcoded default. --verify would
	// miss this: its structural re-parse floors the alloc cap at the output size, so the failure
	// belongs at write time.
	limit := opts.Limits.MaxAllocBytes
	if limit <= 0 {
		limit = bits.DefaultLimits.MaxAllocBytes
	}
	// Floor at the largest header packet the file already carries. Those bytes were read
	// within the (possibly raised) parse limit, so an unrelated edit must stay possible
	// under a lower write limit - and for Vorbis the setup packet is copied verbatim, so
	// guarding it against the bare limit would refuse an edit over a packet the edit does
	// not even touch.
	limit = max(limit, d.origMaxHeaderPacket())

	// Build the header tail packets (with the possibly-neutralized vendor) once, here, so
	// the whole-packet guard weighs the exact bytes the re-pagination below emits. Page 0
	// (the id packet, alone) is normally copied verbatim; the FLAC mapping rebuilds it when
	// its header-packet count changes.
	newBlocks := d.flacBlocks
	var flacDupContent []core.DuplicateContent
	page0 := bits.Copy(0, d.page0Len)
	page0Len := d.page0Len
	var tailPackets [][]byte
	if d.kind == kindFLAC {
		var dupDropped bool
		newBlocks, dupDropped = rebuildFLACBlocks(d, newVendor, newComments, edited.Pictures, commentsChanged || vendorChanged, picturesChanged)
		if dupDropped {
			flacDupContent = d.dupContent
		}
		if err := checkFLACBlockSizes(newBlocks); err != nil {
			return nil, err
		}
		tailPackets = flacHeaderPackets(newBlocks)
		// Rebuild page 0 whenever the identification packet's BYTES change, not merely
		// when the block count does. A file whose declared count was already wrong - or
		// was the spec's "unknown" zero - would otherwise keep that value on disk while
		// buildResult records the true one, breaking the result-equals-a-fresh-parse
		// promise. STREAMINFO is carried through untouched either way.
		if idPacket := flacIDWithCount(d.idPacket, len(newBlocks)); !bytes.Equal(idPacket, d.idPacket) {
			p0, _ := paginateBOS(d.serial, idPacket)
			page0, page0Len = bits.Lit(p0), int64(len(p0))
		}
	} else {
		commentPacket := d.buildCommentPacket(newVendor, full)
		tailPackets = [][]byte{commentPacket}
		if d.kind == kindVorbis {
			tailPackets = append(tailPackets, d.setupPacket)
		}
	}
	// Every header packet is reassembled (and capped) individually on re-read, so the guard
	// is per-packet: for Vorbis and Opus that is the one comment packet, for FLAC each
	// metadata block's packet - a single oversized cover must not slip through because the
	// comment block alone stayed small.
	for _, pkt := range tailPackets {
		if int64(len(pkt)) > limit {
			return nil, fmt.Errorf("%w: Ogg %s is %s (max %s; raise the write allocation limit to keep it)",
				waxerr.ErrPictureTooLarge, headerPacketName(d.kind, pkt), bits.HumanBytes(int64(len(pkt))), bits.HumanBytes(limit))
		}
	}

	// Re-paginate the header tail (everything after the BOS id page).
	tailBytes, tailPages := paginate(d.serial, 1, tailPackets)
	newHeaderPages := 1 + tailPages
	delta := newHeaderPages - d.headerPages

	newAudioStart := page0Len + int64(len(tailBytes))
	shift := newAudioStart - d.audioStart

	segs := []bits.Segment{page0, bits.Lit(tailBytes)}

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

	// An over-range chapter or synced-lyric timestamp was clamped to the codec ceiling while
	// rendering the comment list; surface it as a write-time warning (before DowngradeNoOp).
	// The clamp keeps the value readable, so result != base and the write proceeds instead of
	// collapsing to a "No metadata changes" no-op.
	report.Warnings = vorbis.RebuildWarnings(report.Warnings, rebuildInfo)

	result := buildResult(edited, d, newVendor, newComments, newBlocks, newAudioPages, newHeaderPages, page0Len, newAudioStart, shift, newSize, limit)
	// Only the first Vorbis comment block survives; warn when an extra held content the
	// written set does not, matching native FLAC.
	report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "Vorbis comment block", result.Tags, flacDupContent)
	// Ogg stores Vorbis values verbatim, so this downgrade only catches values the rebuild
	// dropped, such as empty strings. Vendor neutralization has no canonical-tag diff, so it
	// must be passed as the structural-change flag.
	if np := core.DowngradeNoOp(d.format, edited.Identity.Size, base, result, len(vorbis.DiffKeys(base.Tags, result.Tags)) == 0, vendorChanged, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{
		Segments: segs,
		NoOp:     false,
		Report:   report,
		Result:   result,
	}, nil
}

// buildCommentPacket frames a comment list as a full comment header packet: the per-codec
// signature, the comment-list body under vendor, and the trailing framing bit for Vorbis or
// preserved padding for Opus.
func (d *doc) buildCommentPacket(vendor string, comments []vorbis.Comment) []byte {
	body := vorbis.RenderCommentList(vendor, comments)
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
func buildResult(edited *core.Media, base *doc, newVendor string, newComments []vorbis.Comment, newBlocks []fblock,
	newAudioPages []apage, newHeaderPages int, newPage0Len, newAudioStart, shift, newSize, limit int64) *core.Media {

	idPacket := base.idPacket
	if base.kind == kindFLAC {
		idPacket = flacIDWithCount(idPacket, len(newBlocks))
	}
	nd := &doc{
		format:      base.format,
		kind:        base.kind,
		serial:      base.serial,
		vendor:      newVendor,
		comments:    newComments,
		pictures:    core.ClonePictures(edited.Pictures),
		flacBlocks:  newBlocks,
		idPacket:    idPacket,
		setupPacket: base.setupPacket,
		commentPad:  base.commentPad,
		page0Len:    newPage0Len,
		headerPages: newHeaderPages,
		audioStart:  newAudioStart,
		audioPages:  newAudioPages,
		audioEnd:    base.audioEnd + shift,
		trailingLen: base.trailingLen,
		clean:       true,
	}
	if base.kind == kindFLAC {
		// Re-derive the picture state from the blocks actually written, the same way a
		// fresh parse would. Carrying the source's fields instead would keep an
		// undecodable PICTURE block listed as "preserved" after it was already re-emitted
		// into newBlocks, so a second picture edit on the returned document would drop it.
		_, nd.malformedPictureBlocks, _ = decodeFLACBlockPictures(newBlocks, limit)
		nd.commentPictures = commentSourcedPictures(newComments, limit)
	}
	tags, families := vorbis.Project(newComments)
	media := &core.Media{
		Format:       base.format,
		Properties:   edited.Properties.Clone(),
		Tags:         tags,
		Families:     families,
		Pictures:     core.ClonePictures(edited.Pictures),
		Chapters:     vorbis.ProjectChapters(newComments),
		SyncedLyrics: vorbis.ProjectSyncedLyrics(newComments),
		// Recompute inherited-encoder warnings from the vendor and comments that were written.
		// Other warnings carry verbatim because CHAPTERxxx and SYNCEDLYRICS projections emit
		// no warnings today; if that changes, this must rederive those warnings too.
		Warnings:   vorbis.CarryEncoderWarnings(edited.Warnings, newVendor, newComments),
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
