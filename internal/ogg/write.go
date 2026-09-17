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

// Plan builds the rewrite. Comment header rebuilt; id/setup and audio payloads
// verbatim. BOS copied; comment/setup re-paginated. If header page count changes,
// audio pages are renumbered (seq+CRC) without re-reading bodies.

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
	// Vendor neutralization bypasses no-op even if comments are unchanged.

	newVendor, vendorChanged := vorbis.NeutralizeVendor(d.vendor, opts.StripEncoderStamp)
	// The output gain lives in the OpusHead, outside the comment header, so no tag,
	// picture, chapter, or lyric comparison sees it.
	gain := edited.Properties.First().OutputGain
	gainChanged := d.kind == kindOpus && gain != base.Properties.First().OutputGain

	report := core.WriteReport{Format: d.format, BytesBefore: edited.Identity.Size}

	// NoOp: full copy for SaveAsFile/WriteTo; SaveBack skips. Before chained/alignment
	// guards. Chapters/synced-lyrics-only edits must defeat this gate too.

	if !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !vendorChanged && !gainChanged {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// An actual rewrite is refused for stream shapes we cannot edit safely.
	if d.chained {
		return nil, fmt.Errorf("%w: refusing to rewrite a chained or multiplexed Ogg stream", waxerr.ErrChainedStream)
	}
	if !d.clean {
		return nil, fmt.Errorf("%w: Ogg header and audio are not cleanly page-aligned; cannot rewrite safely", waxerr.ErrUnalignedStream)
	}
	if gainChanged && len(d.idPacket) < 18 {
		return nil, fmt.Errorf("%w: OpusHead is %d bytes; it holds no output gain to patch", waxerr.ErrInvalidData, len(d.idPacket))
	}

	// Gain-only: rebuild page 0; copy everything after original page 0 (comment/audio
	// byte-identical; no header re-pagination).

	if gainChanged && !tagsChanged && !picturesChanged && !chaptersChanged && !syncedLyricsChanged && !vendorChanged {
		return gainOnlyPlan(edited, d, gain, report, d.writeAllocLimit(opts)), nil
	}

	// Rebuild comments: tags, owned chapters/synced lyrics, then picture comments.

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
	// One METADATA_BLOCK_PICTURE per picture (stored MIME, not sniffed). Clone only when
	// appending; otherwise aliasing newComments is safe.

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

	// Refuse a comment packet a same-limit reader would reject. Check the whole packet
	// (covers can jointly overflow). Floor at origCommentPacketLen so already-parsed data
	// stays writable under a lower write limit. Gate on opts.Limits (--verify re-parse
	// floors alloc at output size, so this belongs at write time).

	limit := d.writeAllocLimit(opts)

	// Build header-tail packets once so the size guard matches re-pagination output.
	// Page 0 usually copied; FLAC rebuilds it when header-packet count changes.

	newBlocks := d.flacBlocks
	var flacDupContent []core.DuplicateContent
	page0 := bits.Copy(0, d.page0Len)
	page0Len := d.page0Len
	idPacket := d.idPacket
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
		// Rebuild page 0 when id packet bytes change (not just block count), so result
		// matches a fresh parse even if the declared count was wrong/zero.

		if p := flacIDWithCount(d.idPacket, len(newBlocks)); !bytes.Equal(p, d.idPacket) {
			idPacket = p
			p0, _ := paginateBOS(d.serial, idPacket)
			page0, page0Len = bits.Lit(p0), int64(len(p0))
		}
	} else {
		commentPacket := d.buildCommentPacket(newVendor, full)
		tailPackets = [][]byte{commentPacket}
		if d.kind == kindVorbis {
			tailPackets = append(tailPackets, d.setupPacket)
		}
		if gainChanged {
			idPacket = opusHeadWithGain(d.idPacket, gain)
			p0, _ := paginateBOS(d.serial, idPacket)
			page0, page0Len = bits.Lit(p0), int64(len(p0))
			report.Operations = append(report.Operations, "OpusHead output gain rewrite")
		}
	}
	// Per-packet guard on re-read (Vorbis/Opus: comment; FLAC: each metadata packet).

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
		// Page count changed: rebase seq and patch CRC in place (bodies unchanged).
		// One backing slice for all 8-byte patches.

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

	// Clamp warnings before DowngradeNoOp; clamp keeps result != base.

	report.Warnings = vorbis.RebuildWarnings(report.Warnings, rebuildInfo)

	result := buildResult(edited, d, newVendor, newComments, newBlocks, newAudioPages, newHeaderPages, idPacket, page0Len, newAudioStart, shift, newSize, limit)
	// Only the first Vorbis comment block survives; warn when an extra held content the
	// written set does not, matching native FLAC.
	report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "Vorbis comment block", result.Tags, flacDupContent)
	// Downgrade catches rebuild drops (e.g. empty strings). Vendor/gain are structural
	// so a combined drop is not collapsed to a no-op that keeps the old gain.

	if np := core.DowngradeNoOp(d.format, edited.Identity.Size, base, result, len(vorbis.DiffKeys(base.Tags, result.Tags)) == 0, vendorChanged || gainChanged, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{
		Segments: segs,
		NoOp:     false,
		Report:   report,
		Result:   result,
	}, nil
}

// writeAllocLimit: per-header-packet ceiling, floored at the largest existing header
// packet (so setup copied verbatim is not refused under a lower write limit).

func (d *doc) writeAllocLimit(opts core.WriteOptions) int64 {
	limit := opts.Limits.MaxAllocBytes
	if limit <= 0 {
		limit = bits.DefaultLimits.MaxAllocBytes
	}
	return max(limit, d.origMaxHeaderPacket())
}

// gainOnlyPlan: rebuild page 0 for OpusHead gain; copy the rest verbatim.

func gainOnlyPlan(edited *core.Media, d *doc, gain int, report core.WriteReport, limit int64) *core.WritePlan {
	idPacket := opusHeadWithGain(d.idPacket, gain)
	p0, _ := paginateBOS(d.serial, idPacket)
	segs := []bits.Segment{bits.Lit(p0), bits.Copy(d.page0Len, d.audioEnd-d.page0Len)}
	if d.trailingLen > 0 {
		segs = append(segs, bits.Copy(d.audioEnd, d.trailingLen))
	}

	shift := int64(len(p0)) - d.page0Len
	newAudioPages := make([]apage, len(d.audioPages))
	for i, ap := range d.audioPages {
		ap.off += shift
		newAudioPages[i] = ap
	}
	newSize := bits.OutputLen(segs)
	report.Operations = append(report.Operations, "OpusHead output gain rewrite")
	report.BytesAfter = newSize
	report.PaddingAfter = int64(len(d.commentPad))

	result := buildResult(edited, d, d.vendor, d.comments, d.flacBlocks, newAudioPages, d.headerPages,
		idPacket, int64(len(p0)), d.audioStart+shift, shift, newSize, limit)
	return &core.WritePlan{Segments: segs, Report: report, Result: result}
}

// buildCommentPacket: signature + comment body + Vorbis framing bit or Opus padding.

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

// buildResult builds post-write Media without re-parsing (audio bodies unchanged).

func buildResult(edited *core.Media, base *doc, newVendor string, newComments []vorbis.Comment, newBlocks []fblock,
	newAudioPages []apage, newHeaderPages int, idPacket []byte, newPage0Len, newAudioStart, shift, newSize, limit int64) *core.Media {

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
		// Picture state from written blocks (not source fields) so chained edits match re-parse.

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
		// Encoder warnings from written vendor/comments; other warnings carry for now.

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
