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
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan builds the rewrite. Comment header rebuilt; id/setup and audio payloads
// verbatim. BOS copied; comment/setup re-paginated, numbered on from the BOS page. Audio
// pages that no longer follow the header pages' numbers are renumbered (seq+CRC) without
// re-reading bodies. Then the tail, its legacy tags unless stripped.
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
	// An explicit clear, or a cleared name, also removes what the file holds unread: owned
	// comments nothing reads and, in the FLAC mapping, PICTURE blocks that did not decode.
	unread := vorbis.PlanUnread(d.comments, len(d.malformedPictureBlocks), edited.Tags, opts,
		!core.EqualChapters(base.Chapters, edited.Chapters), !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics))
	chaptersChanged, syncedLyricsChanged := unread.ChaptersChanged, unread.LyricsChanged
	// coversChanged is the edit's own change to the covers; picturesChanged also counts the
	// blocks a clear takes.
	coversChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	picturesChanged := coversChanged || unread.DropBlocks
	unreadRemoved := unread.Removes()
	// Vendor neutralization bypasses no-op even if comments are unchanged.
	newVendor, vendorChanged := vorbis.NeutralizeVendor(d.vendor, opts.StripEncoderStamp)
	// The output gain lives in the OpusHead, outside the comment header, so no tag,
	// picture, chapter, or lyric comparison sees it.
	gain := edited.Properties.First().OutputGain
	gainChanged := d.kind == kindOpus && gain != base.Properties.First().OutputGain
	stripLegacy := opts.Legacy == core.LegacyStrip
	legacyChange := stripLegacy && (len(d.leadingID3) > 0 || d.layout.HasTags())
	// headerChanged: the comment header is rebuilt. Chapters/synced-lyrics-only edits count.
	headerChanged := tagsChanged || picturesChanged || chaptersChanged || syncedLyricsChanged || vendorChanged || unreadRemoved

	report := core.WriteReport{Format: d.format, BytesBefore: edited.Identity.Size}

	// NoOp: full copy for SaveAsFile/WriteTo; SaveBack skips. Before chained/alignment
	// guards.
	if !headerChanged && !gainChanged && !legacyChange {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	if err := d.refuseWrite(); err != nil {
		return nil, err
	}
	if gainChanged && len(d.idPacket) < 18 {
		return nil, fmt.Errorf("%w: OpusHead is %d bytes; it holds no output gain to patch", waxerr.ErrInvalidData, len(d.idPacket))
	}

	// A gain or legacy-tag change alone: no header re-pagination.
	if !headerChanged {
		return d.keptHeaderPlan(edited, gain, gainChanged, stripLegacy, report), nil
	}

	// Rebuild comments: tags, owned chapters/synced lyrics, then picture comments.
	newComments := d.comments
	commentsChanged := tagsChanged || chaptersChanged || syncedLyricsChanged || len(unread.Comments) != len(d.comments)
	var rebuildInfo vorbis.RebuildInfo
	if commentsChanged {
		newComments, rebuildInfo = vorbis.Rebuild(unread.Comments, edited.Tags, changed, edited.Chapters, chaptersChanged, edited.SyncedLyrics, syncedLyricsChanged)
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
	var coverBytes int64 // what the covers add to the comment header
	if d.kind != kindFLAC && len(edited.Pictures) > 0 {
		full = slices.Clone(newComments)
		for _, p := range edited.Pictures {
			cm := vorbis.Comment{
				Name:  vorbis.PictureComment,
				Value: base64.StdEncoding.EncodeToString(vorbis.RenderPicture(p)),
			}
			full = append(full, cm)
			coverBytes += int64(4 + len(cm.Name) + 1 + len(cm.Value))
		}
	}
	if err := vorbis.CheckCommentCount(len(full), d.origCommentCount); err != nil {
		return nil, err
	}
	if picturesChanged {
		report.Operations = append(report.Operations, fmt.Sprintf("pictures: %d", len(edited.Pictures)))
	}
	report.Operations = append(report.Operations, unread.Ops...)
	if vendorChanged {
		report.Operations = append(report.Operations, "vendor stamp neutralized")
	}

	// Refuse a comment packet a same-limit reader would reject. Check the whole packet
	// (covers can jointly overflow). Floor at origCommentPacketLen so already-parsed data
	// stays writable under a lower write limit. Gate on opts.Limits (a write's read-back
	// floors alloc at the output size, so this belongs at write time).
	limit := d.writeAllocLimit(opts)

	// Build header-tail packets once so the size guard matches re-pagination output.
	// Page 0 usually copied; FLAC rebuilds it when header-packet count changes.
	var flacDupContent []core.DuplicateContent
	page0 := bits.Copy(d.streamStart, d.page0Len)
	var tailPackets [][]byte
	if d.kind == kindFLAC {
		newBlocks, dupDropped := rebuildFLACBlocks(d, newVendor, newComments, edited.Pictures, commentsChanged || vendorChanged, picturesChanged, !unread.DropBlocks)
		if dupDropped {
			flacDupContent = d.dupContent
		}
		if err := checkFLACBlockSizes(newBlocks); err != nil {
			return nil, err
		}
		// The reader's cap counts the identification packet with the blocks.
		if err := vorbis.CheckBlockCount(len(newBlocks)+1, len(d.flacBlocks)+1); err != nil {
			return nil, err
		}
		tailPackets = flacHeaderPackets(newBlocks)
		// Rebuild page 0 whenever the id packet bytes change, not just the block count, so a
		// wrong or zero declared count is corrected too.
		if p := flacIDWithCount(d.idPacket, len(newBlocks)); !bytes.Equal(p, d.idPacket) {
			p0, _ := paginateBOS(d.serial, d.page0Seq, p)
			page0 = bits.Lit(p0)
		}
	} else {
		commentPacket := d.buildCommentPacket(newVendor, full)
		tailPackets = [][]byte{commentPacket}
		if d.kind == kindVorbis {
			tailPackets = append(tailPackets, d.setupPacket)
		}
		if gainChanged {
			p0, _ := paginateBOS(d.serial, d.page0Seq, opusHeadWithGain(d.idPacket, gain))
			page0 = bits.Lit(p0)
			report.Operations = append(report.Operations, "OpusHead output gain rewrite")
		}
	}
	// Per-packet guard on re-read (Vorbis/Opus: comment; FLAC: each metadata packet). A cover is
	// what is too large when the packet is its block, or a comment header that fits without the
	// covers an edit of the covers gave it; a header the edit grew beside covers it left alone is
	// content, like anything else.
	for _, pkt := range tailPackets {
		if int64(len(pkt)) <= limit {
			continue
		}
		class := waxerr.ErrContentTooLarge
		if d.kind == kindFLAC && pkt[0]&0x7F == flacBlkPicture || d.kind != kindFLAC && coversChanged && int64(len(pkt))-coverBytes <= limit {
			class = waxerr.ErrPictureTooLarge
		}
		got, max := bits.SizeOverLimit(int64(len(pkt)), limit)
		return nil, fmt.Errorf("%w: Ogg %s is %s, max %s (raise the write allocation limit to keep it)",
			class, headerPacketName(d.kind, pkt), got, max)
	}

	// Re-paginate the header tail (everything after the BOS id page).
	tailBytes, tailPages := paginate(d.serial, d.page0Seq+1, tailPackets)

	// What sat between the header pages goes ahead of the new ones as one run, which a reader
	// crosses to a page this write builds; the first audio page still follows a header page.
	segs := append(d.leadingSegments(stripLegacy, &report), page0)
	for _, g := range d.headerGaps {
		segs = append(segs, bits.Copy(g.off, g.len))
	}
	segs = append(segs, bits.Lit(tailBytes))
	shift := bits.OutputLen(segs) - d.audioStart

	// The first audio page follows the last header page; the pages after it keep their steps.
	var delta uint32
	if len(d.audioPages) > 0 {
		delta = d.page0Seq + 1 + uint32(tailPages) - d.audioPages[0].seq
	}
	// The audio runs to the tail: past the last page when the file ends inside one.
	if delta == 0 {
		segs = append(segs, bits.Copy(d.audioStart, d.layout.Start-d.audioStart))
	} else {
		if need := int64(len(d.audioPages)) * renumberPerPage; need > limit {
			return nil, fmt.Errorf("%w: renumbering %d Ogg audio pages takes %d bytes, over Limits.MaxAllocBytes (%d); raise the write allocation limit",
				waxerr.ErrSizeTooLarge, len(d.audioPages), need, limit)
		}
		// Rebase seq and patch CRC in place, from one slice of 8-byte patches. Between two the
		// source copies as one run: a page's lacing and body, then the next page's 18 bytes.
		patches := make([]byte, 8*len(d.audioPages))
		segs = slices.Grow(segs, 1+2*len(d.audioPages))
		segs = append(segs, bits.Copy(d.audioStart, d.audioPages[0].off+18-d.audioStart))
		for i, ap := range d.audioPages {
			newSeq := ap.seq + delta
			newCRC := patchCRC(ap.crc, ap.seq, newSeq, ap.total)
			p8 := patches[i*8 : i*8+8 : i*8+8]
			binary.LittleEndian.PutUint32(p8[0:4], newSeq)
			binary.LittleEndian.PutUint32(p8[4:8], newCRC)
			next := d.layout.Start
			if i+1 < len(d.audioPages) {
				next = d.audioPages[i+1].off + 18
			}
			segs = append(segs, bits.Lit(p8), bits.Copy(ap.off+26, next-(ap.off+26)))
		}
		report.Operations = append(report.Operations, fmt.Sprintf("renumbered %d audio pages", len(d.audioPages)))
	}

	segs = d.appendTail(segs, stripLegacy, &report)
	report.BytesAfter = bits.OutputLen(segs)
	report.PaddingAfter = int64(len(d.commentPad))

	// Clamp warnings before DowngradeNoOp; clamp keeps result != base.
	report.Warnings = vorbis.RebuildWarnings(report.Warnings, rebuildInfo)
	report.Warnings = append(report.Warnings, unread.Warnings...)

	result := buildResult(edited, d, newComments, shift)
	// Only the first Vorbis comment block survives; warn when an extra held content the
	// written set does not, matching native FLAC.
	report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "Vorbis comment block", base.Tags, result.Tags, flacDupContent)
	// Downgrade catches rebuild drops (e.g. empty strings). Structural, so never collapsed to
	// a no-op: vendor, gain, a legacy strip, and what an edit removes unread.
	if np := core.DowngradeNoOp(d.format, edited.Identity.Size, base, result, len(vorbis.DiffKeys(base.Tags, result.Tags)) == 0, vendorChanged || gainChanged || unreadRemoved || legacyChange, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{
		Segments: segs,
		NoOp:     false,
		Report:   report,
		Result:   result,
	}, nil
}

// refuseWrite is why d cannot be rewritten: a stream shape that cannot be edited safely. Plan
// refuses with it and the capabilities report it.
func (d *doc) refuseWrite() error {
	switch {
	case d.chained:
		return fmt.Errorf("%w: refusing to rewrite an Ogg file of chained streams", waxerr.ErrChainedStream)
	case d.multiplexed:
		return fmt.Errorf("%w: refusing to rewrite an Ogg file of multiplexed streams", waxerr.ErrChainedStream)
	case !d.clean:
		return fmt.Errorf("%w: Ogg header and audio are not cleanly page-aligned; cannot rewrite safely", waxerr.ErrUnalignedStream)
	case d.gaveUp:
		// A rewrite would renumber the pages that were read and not the ones behind them.
		return fmt.Errorf("%w: too much junk between Ogg pages: the read stops at offset %d, so the file cannot be rewritten safely", waxerr.ErrInvalidData, d.pagesEnd)
	}
	// A header rewrite writes the junk from among the header pages as one run.
	var between int64
	for _, g := range d.headerGaps {
		between += g.len
	}
	if between > core.ResyncWindow {
		return fmt.Errorf("%w: %d bytes lie between the Ogg header pages, more than the %d a reader skips to the next page; cannot rewrite safely",
			waxerr.ErrUnalignedStream, between, core.ResyncWindow)
	}
	return nil
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

// keptHeaderPlan plans a write that leaves the comment header alone: page 0 rebuilt for a new
// OpusHead gain, every page after it copied verbatim, and the legacy tags kept or stripped.
func (d *doc) keptHeaderPlan(edited *core.Media, gain int, gainChanged, strip bool, report core.WriteReport) *core.WritePlan {
	var page0 []bits.Segment
	rest := d.streamStart
	if gainChanged {
		p0, _ := paginateBOS(d.serial, d.page0Seq, opusHeadWithGain(d.idPacket, gain))
		page0, rest = []bits.Segment{bits.Lit(p0)}, d.streamStart+d.page0Len
		report.Operations = append(report.Operations, "OpusHead output gain rewrite")
	}
	segs := append(d.leadingSegments(strip, &report), page0...)
	segs = append(segs, bits.Copy(rest, d.layout.Start-rest))
	shift := bits.OutputLen(segs) - d.layout.Start
	segs = d.appendTail(segs, strip, &report)

	report.BytesAfter = bits.OutputLen(segs)
	report.PaddingAfter = int64(len(d.commentPad))

	result := buildResult(edited, d, d.comments, shift)
	return &core.WritePlan{Segments: segs, Report: report, Result: result}
}

// leadingSegments copies what precedes the first page, and names what becomes of a leading
// ID3v2 tag: a strip takes it and the bytes behind it.
func (d *doc) leadingSegments(strip bool, report *core.WriteReport) []bits.Segment {
	switch {
	case d.streamStart == 0:
		return nil
	case len(d.leadingID3) > 0 && strip:
		report.Operations = append(report.Operations, core.LeadingStripOp(int64(len(d.leadingID3)), d.streamStart))
		return nil
	case len(d.leadingID3) > 0:
		report.Operations = append(report.Operations, "leading ID3v2 preservation")
	}
	return []bits.Segment{bits.Copy(0, d.streamStart)}
}

// appendTail adds what follows the stream: the junk always, the legacy tags unless strip.
func (d *doc) appendTail(segs []bits.Segment, strip bool, report *core.WriteReport) []bits.Segment {
	report.Operations = append(report.Operations, tail.Operations(d.layout, strip)...)
	return append(segs, tail.Segments(d.layout, strip)...)
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

// buildResult builds the plan's result: the canonical view a fresh parse of the output reads, and
// the audio page bodies, which the rewrite moves by shift without changing them.
func buildResult(edited *core.Media, base *doc, newComments []vorbis.Comment, shift int64) *core.Media {
	tags, _ := vorbis.Project(newComments)
	media := &core.Media{
		Format:       base.format,
		Properties:   edited.Properties.Clone(),
		Tags:         tags,
		Pictures:     core.ClonePictures(edited.Pictures),
		Chapters:     vorbis.ProjectChapters(newComments),
		SyncedLyrics: vorbis.ProjectSyncedLyrics(newComments),
		AudioStart:   base.audioStart + shift,
		AudioEnd:     base.audioEnd + shift,
	}
	for _, ap := range base.audioPages {
		start := ap.bodyOff() + shift
		media.AudioRanges = append(media.AudioRanges, [2]int64{start, start + ap.bodyLen})
	}
	core.SyncTextItems(media)
	return media
}
