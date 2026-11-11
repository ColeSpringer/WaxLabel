package ogg

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/waxerr"
)

// Header packet signatures.
var (
	vorbisID      = []byte("\x01vorbis")
	vorbisComment = []byte("\x03vorbis")
	opusHead      = []byte("OpusHead")
	opusTags      = []byte("OpusTags")
)

// parse reads Ogg metadata into a Media. Codec from the id header. Native doc
// keeps decoder-critical headers and audio-page descriptors for rewrite. c is the
// codec detection chose, which finds the stream behind a leading tag as detection did.
func parse(ctx context.Context, c Codec, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit, maxElements := opts.Limits.MaxAllocBytes, opts.Limits.MaxElements

	// Stray leading ID3v2, preserved.
	d := &doc{}
	var warnings []core.Warning
	var err error
	d.leadingID3, d.streamStart, warnings, err = id3.ReadFrontStray(src, size, limit, c, "Ogg")
	if err != nil {
		return nil, err
	}
	walkStart := d.streamStart

	// Where tags at the end of the file seem to begin; the walk settles whether they are tags.
	tags, err := tail.Peeled(src, size, walkStart+pageFixedHdr, limit)
	if err != nil {
		return nil, err
	}
	r := &pageReader{src: src, limit: limit, maxElements: maxElements, d: d, grouping: true, tags: tags, maxPages: math.MaxInt}
	if limit > 0 {
		r.maxPages = int(min(limit/retainedPerPage, math.MaxInt))
	}
	end, at, err := walkPages(ctx, src, walkStart, size, limit, r.suspect, r.page)
	if err != nil {
		return nil, err
	}
	partial := at == stopCut
	switch {
	case r.pages == 0:
		return nil, fmt.Errorf("%w: not an Ogg stream", waxerr.ErrInvalidData)
	case !r.chosen:
		return nil, fmt.Errorf("%w: an Ogg Skeleton with no Vorbis, Opus or FLAC stream beside it", waxerr.ErrUnsupportedFormat)
	case !r.hdr.done:
		return nil, fmt.Errorf("%w: incomplete Ogg header packets", waxerr.ErrInvalidData)
	}
	hp := r.hdr.hp
	d.pagesEnd, d.gaveUp = end, at == stopBound
	d.startGranule = r.start

	// What follows the pages: legacy tags in any order, and junk, copied on write. None
	// starts inside a page. A page the file ends inside runs up to the tags.
	d.layout, err = tail.Scan(src, size, end, limit, maxElements, func(_ core.ReaderAtSized, boundary int64) (int64, error) {
		if partial {
			return boundary, nil
		}
		return end, nil
	})
	if err != nil {
		return nil, err
	}
	d.truncatedTail = partial && d.layout.Start > end

	// Streams that begin together, or one after another: read the first audio stream; refuse write.
	d.multiplexed = len(r.serials) > 1
	if d.multiplexed {
		warnings = core.Warn(warnings, core.WarnMultiplexedStream, fmt.Sprintf(
			"%d logical streams begin this Ogg file together; its first Vorbis, Opus or FLAC stream is read and writing is refused", len(r.serials)))
	}
	if d.chained = r.chained; d.chained {
		warnings = core.Warn(warnings, core.WarnChainedStream,
			"chained Ogg stream; only its first link is read and writing is refused")
	}

	d.flacBlocks = hp.flacBlocks
	d.dupContent = hp.dupContent
	d.clean = hp.clean
	switch d.kind {
	case kindOpus:
		d.format = core.FormatOggOpus
	case kindFLAC:
		d.format = core.FormatOggFLAC
	default:
		d.format = core.FormatOggVorbis
	}

	// Native PICTURE before comments so FLAC cover order matches internal/flac.
	d.decodeFLACPictures(limit, &warnings)
	if len(d.dupContent) > 0 {
		warnings = core.Warn(warnings, core.WarnMultipleVorbisComment,
			"more than one Vorbis comment block; the first is authoritative and the extras are dropped if the file is rewritten")
	}
	if err := d.decodeComments(hp.comment, limit, maxElements, &warnings); err != nil {
		return nil, err
	}

	media := &core.Media{
		Format:     d.format,
		Native:     d,
		AudioStart: d.audioStart,
		AudioEnd:   d.audioEnd,
		// Keep each cover's stored MIME: media.Pictures is the write source, so a sniffed
		// type would rewrite an untouched cover's label on an unrelated edit. Type
		// detection runs at the display boundary (Document.Pictures and the linter, via
		// core.ProjectPictures).
		Pictures: d.pictures,
	}
	media.Tags, media.Families = vorbis.Project(d.comments)
	// Legacy tags into family entries, not canonical. Opaque: content the entries cannot
	// carry, or bytes behind the leading tag, which a strip cannot prove redundant.
	tailWarnings := d.layout.ProjectLegacy(media, "after the Ogg stream")
	leading, leadingOpaque, unread := id3.LegacyV2Families(media.Tags, d.leadingID3, maxElements)
	d.leadingUnread = unread
	media.Families = append(media.Families, leading...)
	leadingGap := d.streamStart - int64(len(d.leadingID3))
	media.LegacyOpaqueContent = media.LegacyOpaqueContent || leadingOpaque || len(d.leadingID3) > 0 && leadingGap > 0

	owned := vorbis.ProjectOwned(d.comments)
	media.Chapters, media.SyncedLyrics = owned.Chapters, owned.SyncedLyrics
	warnings = append(warnings, vorbis.EncoderNoise(d.vendor, d.comments)...)
	warnings = append(warnings, vorbis.InvalidKeyWarnings(d.comments)...)
	warnings = append(warnings, vorbis.InvalidTextWarnings(d.comments)...)
	warnings = append(warnings, owned.Warnings...)
	if skipped := d.streamStart - walkStart; skipped > 0 {
		warnings = core.Warn(warnings, core.WarnLeadingBytes,
			fmt.Sprintf("%d byte(s) precede the Ogg stream; preserved", skipped))
	}
	if d.gapCount > 0 {
		warnings = core.Warn(warnings, core.WarnPageGap, fmt.Sprintf(
			"%d byte(s) between Ogg pages, in %d place(s), belong to no page; preserved", d.gapBytes, d.gapCount))
	}
	if d.gaveUp {
		// What the read left holds pages, not bytes that belong to nothing.
		tailWarnings = slices.DeleteFunc(tailWarnings, func(w core.Warning) bool { return w.Code == core.WarnTrailingBytes })
		warnings = core.Warn(warnings, core.WarnPageGap, fmt.Sprintf(
			"too much junk between Ogg pages: the read stops at offset %d, the duration counts the pages up to there, and what follows is not read; writing is refused", end))
	}
	if d.truncatedTail {
		warnings = core.Warn(warnings, core.WarnTruncatedAudio, fmt.Sprintf(
			"the file ends inside an Ogg page, %d byte(s) in; the page is not read and is preserved", d.layout.Start-end))
	}
	warnings = append(warnings, tailWarnings...)

	// Audio page bodies, interleaved with page headers, so not one contiguous run.
	media.AudioRanges = r.ranges

	// Positions that go down add nothing.
	lastPage := uint64(0)
	if r.lastGranule > r.prevGranule {
		lastPage = r.lastGranule - r.prevGranule
	}
	media.Properties = d.properties(r.lastGranule, lastPage, &warnings)
	media.Warnings = warnings
	if err := core.SetIdentity(src, media); err != nil {
		return nil, err
	}
	core.SyncTextItems(media)
	return media, nil
}

// maxStreams bounds the streams a file may open with: past it the file reads as chained.
const maxStreams = 64

// pageReader fills a doc from the pages a walk hands it, one at a time: the header packets of the
// file's first audio stream, then a descriptor and an essence range for each of its audio pages.
type pageReader struct {
	src         core.ReaderAtSized
	limit       int64
	maxElements int
	d           *doc

	pages int   // pages walked
	tags  int64 // where tags at the end of the file seem to begin

	// Streams that begin together open with a run of BOS pages (grouping, serials); the first
	// that is no Skeleton is read (chosen). One that begins later is a chained link (over).
	grouping bool
	serials  []uint32
	chosen   bool
	chained  bool
	over     bool

	hdr      headerAssembler
	ranges   [][2]int64
	maxPages int // audio pages the limit covers, retainedPerPage each

	// The last granule position and the one before it: the samples of the last page.
	lastGranule, prevGranule uint64

	scan  *startScan // reads the first audio pages for the start; nil when it cannot
	start uint64
}

// suspect is what the walk holds against p. The first page has to begin a stream detection
// knows, or it is bytes ahead of the stream. A page that straddles where tags seem to begin may
// be one cut short that a tag only seems to complete, and a page that would end the read of the
// stream, by beginning another or belonging to one that never began, may be junk shaped like one.
func (r *pageReader) suspect(p rawPage) (doubt, error) {
	if r.pages == 0 {
		if p.flags&flagBOS == 0 {
			return refused, nil
		}
		head, err := r.packetHead(p)
		if err != nil {
			return trusted, err
		}
		if !slices.ContainsFunc([][]byte{vorbisID, opusHead, flacID, skeletonID}, func(id []byte) bool { return bytes.HasPrefix(head, id) }) {
			return refused, nil
		}
	}
	switch {
	case p.off < r.tags && p.off+p.total() > r.tags:
		return cut, nil
	case r.pages == 0 || r.chained:
		return trusted, nil
	case p.flags&flagBOS != 0, p.serial != r.d.serial && !slices.Contains(r.serials, p.serial):
		return forged, nil
	}
	return trusted, nil
}

func (r *pageReader) page(p rawPage) error {
	d := r.d
	bos := p.flags&flagBOS != 0
	// Bytes the walk skipped ahead of the page: before the first they lead the stream, and
	// among the header pages a rewrite has to move them. A later link's are its own.
	switch {
	case p.gap == 0 || r.over:
	case r.pages == 0:
		d.streamStart = p.off
	default:
		d.gapCount++
		d.gapBytes += p.gap
		if !r.hdr.done {
			d.headerGaps = append(d.headerGaps, span{p.off - p.gap, p.gap})
		}
	}
	r.pages++
	switch {
	case bos && r.grouping && len(r.serials) == maxStreams:
		r.chained = true // more streams than are told apart
	case bos && r.grouping:
		r.serials = append(r.serials, p.serial)
		if !r.chosen {
			head, err := r.packetHead(p)
			if err != nil {
				return err
			}
			if !bytes.HasPrefix(head, skeletonID) {
				r.chosen = true
				d.serial, d.page0Len, d.page0Seq = p.serial, p.total(), p.seq
			}
		}
	case bos:
		r.chained, r.over = true, true
	default:
		r.grouping = false
		// A page of a stream that never began: what is left of an earlier link.
		if p.serial != d.serial && !slices.Contains(r.serials, p.serial) {
			r.chained = true
		}
	}
	if r.over || !r.chosen || p.serial != d.serial {
		return nil
	}
	if !r.hdr.done {
		if err := r.hdr.add(r.src, p, r.limit, r.maxElements); err != nil || !r.hdr.done {
			return err
		}
		// The header region ends with this page. Audio it holds too (a stream that is not
		// clean) is essence, but not measured for the start.
		hp := r.hdr.hp
		d.kind, d.idPacket, d.setupPacket = hp.kind, hp.id, hp.setup
		d.audioStart = p.off + p.total()
		d.audioEnd = d.audioStart
		r.essence(max(p.bodyOff(), hp.audioByteStart), p)
		if samples := d.packetSamples(); samples != nil && hp.clean {
			r.scan = &startScan{src: r.src, samples: samples}
		}
		return nil
	}
	if len(d.audioPages) == r.maxPages {
		return fmt.Errorf("%w: the Ogg stream has more audio pages than Limits.MaxAllocBytes (%d bytes) holds descriptors for",
			waxerr.ErrSizeTooLarge, r.limit)
	}
	if r.scan != nil && !r.scan.done {
		var err error
		if r.start, err = r.scan.add(p); err != nil {
			return err
		}
	}
	r.essence(p.bodyOff(), p)
	d.audioPages = append(grown(d.audioPages, r.maxPages), apage{off: p.off, total: p.total(), bodyLen: p.bodyLen, seq: p.seq, crc: p.crc})
	d.audioEnd = p.off + p.total()
	return nil
}

// packetHead reads the first bytes of the packet that opens page p: enough to tell the stream
// it begins, a Skeleton's among them, which describes the streams beside it and is not read.
func (r *pageReader) packetHead(p rawPage) ([]byte, error) {
	var lacing int64
	if len(p.segs) > 0 {
		lacing = int64(p.segs[0])
	}
	head, err := bits.ReadSlice(r.src, p.bodyOff(), min(lacing, int64(len(skeletonID))), r.limit)
	if err != nil {
		return nil, fmt.Errorf("read Ogg page at %d: %w", p.off, err)
	}
	return head, nil
}

// grown returns s with room for one more element, its capacity raised by a quarter (by 1024
// elements at least) and no further than most, so that a parse keeps what it charges and no
// more, which append's own growth would not hold to.
func grown[T any](s []T, most int) []T {
	if len(s) < cap(s) {
		return s
	}
	out := make([]T, len(s), min(cap(s)+max(cap(s)/4, 1024), max(most, len(s)+1)))
	copy(out, s)
	return out
}

// essence records the audio p holds from offset lo to the end of its body, and its granule
// position (-1 says no packet ends on the page).
func (r *pageReader) essence(lo int64, p rawPage) {
	if hi := p.bodyOff() + p.bodyLen; lo < hi {
		r.ranges = append(grown(r.ranges, r.maxPages), [2]int64{lo, hi})
	}
	if p.granule != math.MaxUint64 && p.granule != r.lastGranule {
		r.prevGranule, r.lastGranule = r.lastGranule, p.granule
	}
}

// headerPackets holds the reassembled header packets of a logical stream plus
// where they end, so the caller can split the file into header and audio
// regions.
type headerPackets struct {
	kind           kind
	id             []byte
	comment        []byte
	setup          []byte                  // Vorbis only
	flacBlocks     []fblock                // FLAC only: one metadata block per header packet
	dupContent     []core.DuplicateContent // FLAC only: what each extra comment block holds
	audioByteStart int64                   // absolute offset of the first audio byte (may sit mid-page when not clean)
	clean          bool                    // id alone on the first page and the last header packet ends at a page boundary
}

// headerAssembler reassembles a stream's header packets (3 for Vorbis, 2 for Opus, FLAC's up to
// the last-block flag) across its pages. It reports whether the stream is cleanly page-aligned:
// the id packet alone on the first page and the last header packet ending a page, so audio
// begins on a fresh page that can be copied verbatim.
type headerAssembler struct {
	hp        headerPackets
	need      int
	packets   [][]byte
	cur       []byte
	seenFirst bool
	idAlone   bool
	done      bool
}

// add takes the stream's next page; done is set once it completes the last header packet.
func (a *headerAssembler) add(src core.ReaderAtSized, p rawPage, limit int64, maxElements int) error {
	body, err := bits.ReadSlice(src, p.bodyOff(), p.bodyLen, limit)
	if fatal := bits.Fatal(err); fatal != nil {
		return fatal
	}
	if err != nil {
		return fmt.Errorf("%w: truncated header page body at %d", waxerr.ErrInvalidData, p.off)
	}
	o := 0
	completed := 0
	for si, lac := range p.segs {
		a.cur = append(a.cur, body[o:o+int(lac)]...)
		// Each page body is capped by limit, but a packet reassembled across
		// continuation pages is not, so a hostile stream could grow it to file size.
		// Cap it at the alloc limit, well above any comment header (tags + cover).
		if int64(len(a.cur)) > limit {
			return fmt.Errorf("%w: Ogg header packet exceeds the %d-byte limit", waxerr.ErrSizeTooLarge, limit)
		}
		o += int(lac)
		if lac == maxSegments {
			continue // packet continues onto the next segment/page
		}
		pkt := a.cur
		a.cur = nil
		a.packets = append(a.packets, pkt)
		completed++
		if len(a.packets) == 1 {
			k, n, derr := detectKind(pkt)
			if derr != nil {
				return derr
			}
			a.hp.kind, a.need = k, n
		}
		if headersDone(a.hp.kind, a.need, a.packets) {
			a.hp.clean = a.idAlone && si == len(p.segs)-1
			// o has advanced past this completing segment, so this is the first
			// byte after the last header packet: where audio begins, mid-page
			// when the stream is not clean.
			a.hp.audioByteStart = p.bodyOff() + int64(o)
			a.hp, err = finishHeaders(a.hp, a.packets, maxElements, limit)
			a.done = err == nil
			return err
		}
		// FLAC's header run is delimited by the last-block flag rather than a
		// count, so it needs its own bound; the other two are capped by need.
		if err := bits.CheckElementCap(len(a.packets), maxElements, "Ogg FLAC header packets"); err != nil {
			return err
		}
	}
	if !a.seenFirst {
		a.idAlone = completed == 1 && len(a.cur) == 0
		a.seenFirst = true
	}
	return nil
}

// headersDone reports whether the header run is complete. Vorbis and Opus have a
// fixed packet count; the FLAC mapping's count field may read "unknown", so its run
// ends where a metadata block sets the last-block flag, as a FLAC decoder does.
func headersDone(k kind, need int, packets [][]byte) bool {
	if k != kindFLAC {
		return len(packets) == need
	}
	if len(packets) < 2 {
		return false // the identification packet carries STREAMINFO; blocks follow it
	}
	last := packets[len(packets)-1]
	return len(last) > 0 && last[0]&0x80 != 0
}

func finishHeaders(hp headerPackets, packets [][]byte, maxElements int, limit int64) (headerPackets, error) {
	hp.id = packets[0]
	if hp.kind == kindFLAC {
		if err := checkFLACID(hp.id); err != nil {
			return hp, err
		}
		blocks, comment, dups, err := splitFLACBlocks(packets[1:], maxElements, limit)
		if err != nil {
			return hp, err
		}
		hp.flacBlocks = blocks
		hp.comment = comment
		hp.dupContent = dups
		return hp, nil
	}
	hp.comment = packets[1]
	if hp.kind == kindVorbis {
		hp.setup = packets[2]
	}
	return hp, nil
}

// detectKind identifies the codec from the identification packet and returns how
// many header packets it has.
func detectKind(pkt []byte) (kind, int, error) {
	switch {
	case bytes.HasPrefix(pkt, vorbisID):
		return kindVorbis, 3, nil
	case bytes.HasPrefix(pkt, opusHead):
		return kindOpus, 2, nil
	case bytes.HasPrefix(pkt, flacID):
		// The FLAC mapping's own count field may read "unknown", so the header run is
		// closed by the last-block flag instead; need is unused for this kind.
		return kindFLAC, 0, nil
	}
	return 0, 0, fmt.Errorf("%w: unrecognized Ogg codec (not Vorbis, Opus, or FLAC)", waxerr.ErrUnsupportedFormat)
}

// decodeFLACPictures decodes the native PICTURE blocks of a FLAC mapping. A
// malformed picture is warned and skipped, but its raw body is kept in the native
// doc and re-emitted on a picture edit.
func (d *doc) decodeFLACPictures(limit int64, warnings *[]core.Warning) {
	pics, malformed, ws := decodeFLACBlockPictures(d.flacBlocks, limit)
	d.pictures = append(d.pictures, pics...)
	d.malformedPictureBlocks = malformed
	*warnings = append(*warnings, ws...)
	for _, b := range d.flacBlocks {
		if b.code > flacBlkPicture && b.code != flacBlkInvalid {
			*warnings = core.Warn(*warnings, core.WarnUnknownBlock,
				fmt.Sprintf("metadata block type %d preserved verbatim", b.code))
		}
	}
}

// decodeComments parses the comment header packet into the vendor string, tag
// comments, and pictures, preserving any trailing padding (Opus). The comment
// list body is shared with FLAC via internal/vorbis; only the per-codec framing
// differs.
func (d *doc) decodeComments(pkt []byte, limit int64, maxElements int, warnings *[]core.Warning) error {
	// Record the raw packet length before splitting off the signature: it is the byte
	// count the header assembler caps, so the write-side whole-packet guard floors at it.
	d.origCommentPacketLen = int64(len(pkt))
	var list []byte
	switch d.kind {
	case kindVorbis:
		if !bytes.HasPrefix(pkt, vorbisComment) {
			return fmt.Errorf("%w: Vorbis comment header signature missing", waxerr.ErrInvalidData)
		}
		list = pkt[len(vorbisComment):]
	case kindFLAC:
		// The mapping requires a VORBIS_COMMENT block, but a stream missing one is read
		// with an empty comment list rather than refused; the writer inserts the block.
		if len(pkt) == 0 {
			return nil
		}
		list = pkt[4:] // strip the FLAC metadata block header
	default: // Opus
		if !bytes.HasPrefix(pkt, opusTags) {
			return fmt.Errorf("%w: OpusTags signature missing", waxerr.ErrInvalidData)
		}
		list = pkt[len(opusTags):]
	}
	vendor, comments, n, err := vorbis.ParseCommentList(list, limit, maxElements)
	if err != nil {
		return err
	}
	d.vendor = vendor
	d.origCommentCount = len(comments)
	// Preserve trailing bytes after the comment list: Opus comment-header padding
	// (RFC 7845 section 5.2). For Vorbis the next byte is the framing bit, which the
	// writer re-adds, so there is nothing to preserve there.
	if d.kind == kindOpus && n < int64(len(list)) {
		d.commentPad = slices.Clone(list[n:])
	}
	for _, cm := range comments {
		if !vorbis.IsPictureComment(cm.Name) {
			d.comments = append(d.comments, cm)
			continue
		}
		// A malformed picture is preserved as an opaque comment and warned. The decode
		// is shared with the FLAC parser.
		pic, derr := vorbis.DecodePictureComment(cm.Value, limit)
		if derr != nil {
			*warnings = core.Warn(*warnings, core.WarnInvalidPicture, derr.Error())
			d.comments = append(d.comments, cm)
			continue
		}
		d.pictures = append(d.pictures, pic)
		if d.kind == kindFLAC {
			// FLAC's native store is the PICTURE block, so record the comment-sourced
			// subset: the writer materializes these when it re-renders the comment block,
			// which strips the entry.
			d.commentPictures = append(d.commentPictures, pic)
		}
	}
	return nil
}

// properties derives the audio properties from the identification header and the granule
// positions: the last, and lastPage, the samples the last page added. A stream lasts from the
// position it starts at to the last.
func (d *doc) properties(lastGranule, lastPage uint64, warnings *[]core.Warning) core.Properties {
	t := core.AudioTrack{Codec: d.kind.String()}
	// nominalBitrate is the Vorbis identification header's encoder target, the
	// fallback applied at the end when nothing can be measured.
	var nominalBitrate int
	span := uint64(0)
	if lastGranule > d.startGranule {
		span = lastGranule - d.startGranule
	}
	switch d.kind {
	case kindFLAC:
		// STREAMINFO describes the track; its sample count, when nonzero, beats what the
		// pages hold unless that is less: by more than a page, or the stream ends inside one.
		// Pages the read did not reach, or a later link's cut page, say nothing of it.
		if si, err := vorbis.ParseStreamInfo(d.streamInfo()); err == nil {
			t = si
		}
		short := !d.gaveUp && span > 0 && span < t.TotalSamples && (d.truncatedTail && !d.chained || t.TotalSamples-span > lastPage)
		if short && !d.truncatedTail {
			*warnings = core.Warn(*warnings, core.WarnTruncatedAudio, fmt.Sprintf(
				"STREAMINFO declares %d samples, but the pages hold %d; file may be truncated, or cut from a longer stream", t.TotalSamples, span))
		}
		if t.TotalSamples == 0 || short {
			t.TotalSamples = span
			t.Duration = core.SamplesToDuration(span, t.SampleRate)
		}
	case kindVorbis:
		// \x01vorbis(7) | version(4) | channels(1) | sample_rate(4) |
		// bitrate_max(4) | bitrate_nominal(4) |...
		if len(d.idPacket) >= 16 {
			t.Channels = int(d.idPacket[11])
			t.SampleRate = int(binary.LittleEndian.Uint32(d.idPacket[12:16]))
		}
		// bitrate_nominal is the encoder's target, not the delivered rate (on a VBR file
		// it can read several times higher). It is the last resort below, for a chained
		// or unseekable stream with no known duration.
		if len(d.idPacket) >= 24 {
			nominalBitrate = int(int32(binary.LittleEndian.Uint32(d.idPacket[20:24])))
		}
		t.TotalSamples = span
		t.Duration = core.SamplesToDuration(span, t.SampleRate)
	default: // Opus
		// OpusHead(8) | version(1) | channels(1) | pre_skip(2) | input_rate(4) |
		// output_gain(2) | mapping_family(1). Opus always decodes at 48 kHz; the
		// input rate is informational.
		var preSkip uint64
		if len(d.idPacket) >= 12 {
			t.Channels = int(d.idPacket[9])
			preSkip = uint64(binary.LittleEndian.Uint16(d.idPacket[10:12]))
		}
		t.OutputGain = opusOutputGain(d.idPacket)
		t.SampleRate = 48000
		if span > preSkip {
			t.TotalSamples = span - preSkip
			t.Duration = core.SamplesToDuration(t.TotalSamples, 48000)
		}
	}
	// Derive the average from the audio page bodies, the same measured figure every
	// other container reports. bitrate_nominal is used only when nothing can be
	// measured (zero duration: a chained or unseekable stream).
	if t.Bitrate == 0 {
		var audioBytes int64
		for _, ap := range d.audioPages {
			audioBytes += ap.bodyLen
		}
		t.Bitrate = core.AverageBitrate(audioBytes, t.Duration.Seconds())
	}
	if t.Bitrate == 0 && nominalBitrate > 0 {
		t.Bitrate = nominalBitrate
	}
	return core.Properties{Container: "Ogg", Tracks: []core.AudioTrack{t}}
}
