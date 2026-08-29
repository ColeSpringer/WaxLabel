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

// parse reads an Ogg Vorbis or Opus stream's metadata into a neutral Media. The
// codec is detected from the stream itself (the identification header), so the
// same routine serves both registered codec instances. The native document
// preserves the decoder-critical header packets and a descriptor for every audio
// page as the base for a packet-preserving rewrite.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes

	pages, lastEnd, err := scanPages(ctx, src, size, limit, maxOggScanBytes)
	if err != nil {
		return nil, err
	}
	if pages[0].flags&flagBOS == 0 {
		return nil, fmt.Errorf("%w: first Ogg page is not a beginning-of-stream page", waxerr.ErrInvalidData)
	}

	d := &doc{serial: pages[0].serial}
	var warnings []core.Warning

	// Chained or multiplexed? More than one logical bitstream (distinct serial
	// numbers) or more than one beginning-of-stream page means we read the first
	// stream best-effort and refuse to write the file.
	serials := map[uint32]bool{}
	bosCount := 0
	for _, p := range pages {
		serials[p.serial] = true
		if p.flags&flagBOS != 0 {
			bosCount++
		}
	}
	if len(serials) > 1 || bosCount > 1 {
		d.chained = true
		warnings = core.Warn(warnings, core.WarnChainedStream,
			"chained or multiplexed Ogg stream; the first logical stream is read best-effort and writing is refused")
	}

	hp, err := reassembleHeaders(src, pages, d.serial, limit, opts.Limits.MaxElements)
	if err != nil {
		return nil, err
	}
	d.kind = hp.kind
	d.idPacket = hp.id
	d.setupPacket = hp.setup
	d.flacBlocks = hp.flacBlocks
	d.clean = hp.clean && !d.chained
	switch d.kind {
	case kindOpus:
		d.format = core.FormatOggOpus
	case kindFLAC:
		d.format = core.FormatOggFLAC
	default:
		d.format = core.FormatOggVorbis
	}

	// Header-region geometry: page 0 is the id packet alone; the region ends with
	// the page where the last header packet completes.
	d.page0Len = pages[0].total()
	d.headerPages = hp.lastHeaderPage + 1
	if d.headerPages < len(pages) {
		d.audioStart = pages[d.headerPages].off
	} else {
		d.audioStart = lastEnd
	}

	// Walk from the page where the last header packet ended, collecting the audio
	// essence ranges and the final granule for the chosen serial. For a clean
	// stream audio begins on the next page; for a non-page-aligned stream the
	// first audio packet shares this page, so its body is split at audioByteStart
	// (the page itself is not a full audio page and the stream is not writable).
	// Other multiplexed serials' pages are skipped, so the geometry reflects only
	// our stream.
	var lastGranule uint64
	var audioRanges [][2]int64
	for gi := hp.lastHeaderPage; gi < len(pages); gi++ {
		p := pages[gi]
		if p.serial != d.serial {
			continue
		}
		// Essence range: the audio portion of this page's body - the whole body for
		// a page after the header region, or only the tail past audioByteStart for a
		// page shared with the last header packet.
		if lo, hi := max(p.bodyOff(), hp.audioByteStart), p.bodyOff()+p.bodyLen; lo < hi {
			audioRanges = append(audioRanges, [2]int64{lo, hi})
		}
		if p.granule != math.MaxUint64 { // -1 == "no packet completes on this page"
			lastGranule = p.granule
		}
		// Write descriptors and the verbatim-copy extent cover only whole audio
		// pages (strictly after the header region); a shared first page is never
		// rewritten.
		if gi >= d.headerPages {
			d.audioPages = append(d.audioPages, apage{
				off: p.off, total: p.total(), bodyLen: p.bodyLen, seq: p.seq, crc: p.crc, granule: p.granule,
			})
			d.audioEnd = p.off + p.total()
		}
	}
	if len(d.audioPages) == 0 {
		d.audioEnd = d.audioStart
	}
	// Preserve any bytes after the last page of a clean single stream by recording
	// their length and copying them from the source on write. Never buffer them:
	// they could be arbitrarily large, and the Document stays detached and
	// lightweight. For a chained file the trailing region is other streams we do
	// not model, and writing is refused anyway.
	if !d.chained && size > d.audioEnd {
		d.trailingLen = size - d.audioEnd
	}

	// Native PICTURE blocks are decoded before the comment list so a FLAC stream
	// carrying both forms orders its covers native-first, matching internal/flac.
	d.decodeFLACPictures(limit, &warnings)
	if err := d.decodeComments(hp.comment, limit, opts.Limits.MaxElements, &warnings); err != nil {
		return nil, err
	}

	media := &core.Media{
		Format:     d.format,
		Native:     d,
		AudioStart: d.audioStart,
		AudioEnd:   d.audioEnd,
		// Keep each cover's stored MIME: media.Pictures is the write source (the comment is re-emitted
		// from it), so it must not carry the sniffed type or an unrelated edit would rewrite an
		// untouched cover's label. Type detection runs at the display boundary (Document.Pictures and
		// the linter, via core.ProjectPictures).
		Pictures: d.pictures,
	}
	media.Tags, media.Families = vorbis.Project(d.comments)
	media.Chapters = vorbis.ProjectChapters(d.comments)
	var syncedWarnings []core.Warning
	media.SyncedLyrics, syncedWarnings = vorbis.ProjectSyncedLyricsReport(d.comments)
	warnings = append(warnings, syncedWarnings...)
	warnings = append(warnings, vorbis.EncoderNoise(d.vendor, d.comments)...)
	warnings = append(warnings, vorbis.InvalidKeyWarnings(d.comments)...)
	// Recorded above only for a clean single stream; a chained file's tail is other
	// streams, which chained-stream already reports.
	warnings = core.WarnTrailing(warnings, d.trailingLen, "after the Ogg stream", "")

	// Essence ranges (audio page bodies, interleaved with page headers, so not one
	// contiguous run) were collected above.
	media.AudioRanges = audioRanges

	media.Properties = d.properties(lastGranule)
	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// headerPackets holds the reassembled header packets of a logical stream plus
// where they end, so the caller can split the file into header and audio
// regions.
type headerPackets struct {
	kind           kind
	id             []byte
	comment        []byte
	setup          []byte   // Vorbis only
	flacBlocks     []fblock // FLAC only: one metadata block per header packet
	lastHeaderPage int      // index into pages of the page where the last header packet ends
	audioByteStart int64    // absolute offset of the first audio byte (may sit mid-page when not clean)
	clean          bool     // id alone on the first page and the last header packet ends at a page boundary
}

// reassembleHeaders walks the chosen serial's pages, reassembling packets across
// page boundaries until all header packets (3 for Vorbis, 2 for Opus) are
// collected. It also reports whether the stream is cleanly page-aligned: the id
// packet alone on the first page and the last header packet ending exactly at a
// page boundary, so audio begins on a fresh page that can be copied verbatim.
func reassembleHeaders(src core.ReaderAtSized, pages []rawPage, serial uint32, limit int64, maxElements int) (headerPackets, error) {
	var hp headerPackets
	need := 0
	var packets [][]byte
	var cur []byte
	seenFirstPage := false
	idAlone := false

	for gi, p := range pages {
		if p.serial != serial {
			continue
		}
		body, err := bits.ReadSlice(src, p.bodyOff(), p.bodyLen, limit)
		if err != nil {
			return hp, fmt.Errorf("%w: truncated header page body at %d", waxerr.ErrInvalidData, p.off)
		}
		o := 0
		completed := 0
		for si, lac := range p.segs {
			cur = append(cur, body[o:o+int(lac)]...)
			// A packet reassembled across continuation pages is otherwise unbounded (each
			// page body is capped by limit, but the running packet is not), so a hostile
			// stream could grow it to file size. Cap the cumulative packet at the alloc
			// limit, which sits well above any real comment header (tags + embedded cover).
			if int64(len(cur)) > limit {
				return hp, fmt.Errorf("%w: Ogg header packet exceeds the %d-byte limit", waxerr.ErrSizeTooLarge, limit)
			}
			o += int(lac)
			if lac == maxSegments {
				continue // packet continues onto the next segment/page
			}
			pkt := cur
			cur = nil
			packets = append(packets, pkt)
			completed++
			if len(packets) == 1 {
				k, n, derr := detectKind(pkt)
				if derr != nil {
					return hp, derr
				}
				hp.kind, need = k, n
			}
			if headersDone(hp.kind, need, packets) {
				hp.lastHeaderPage = gi
				hp.clean = idAlone && si == len(p.segs)-1
				// o has advanced past this completing segment, so this is the first
				// byte after the last header packet - where audio begins (the page
				// body start for a clean stream, or mid-page when it is not).
				hp.audioByteStart = p.bodyOff() + int64(o)
				return finishHeaders(hp, packets, maxElements)
			}
			// FLAC's header run is delimited by the last-block flag rather than a
			// count, so it needs its own bound; the other two are capped by need.
			if err := bits.CheckElementCap(len(packets), maxElements, "Ogg FLAC header packets"); err != nil {
				return hp, err
			}
		}
		if !seenFirstPage {
			idAlone = completed == 1 && len(cur) == 0
			seenFirstPage = true
		}
	}
	return hp, fmt.Errorf("%w: incomplete Ogg header packets", waxerr.ErrInvalidData)
}

// headersDone reports whether the header run is complete. Vorbis and Opus declare
// a fixed packet count in their identification header; the FLAC mapping does not
// (its count field is allowed to read "unknown"), so its run ends where a metadata
// block sets the last-block flag - the same authority a FLAC decoder uses.
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

func finishHeaders(hp headerPackets, packets [][]byte, maxElements int) (headerPackets, error) {
	hp.id = packets[0]
	if hp.kind == kindFLAC {
		if err := checkFLACID(hp.id); err != nil {
			return hp, err
		}
		blocks, comment, err := splitFLACBlocks(packets[1:], maxElements)
		if err != nil {
			return hp, err
		}
		hp.flacBlocks = blocks
		hp.comment = comment
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
// malformed picture is warned and skipped, but its raw body is preserved in the
// native doc (and re-emitted on a picture edit) so it is not destroyed.
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
	// Record the raw comment header packet length before splitting off the signature: this is the
	// exact byte count reassembleHeaders re-reads and caps, so the write-side whole-packet guard
	// floors against it (see doc.origCommentPacketLen).
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
		// A malformed picture is preserved as an opaque comment and warned, never silently
		// dropped. The decode is shared with the FLAC parser so the two cannot drift.
		pic, derr := vorbis.DecodePictureComment(cm.Value, limit)
		if derr != nil {
			*warnings = core.Warn(*warnings, core.WarnInvalidPicture, derr.Error())
			d.comments = append(d.comments, cm)
			continue
		}
		d.pictures = append(d.pictures, pic)
		if d.kind == kindFLAC {
			// FLAC's native store is the PICTURE block, so record the comment-sourced
			// subset: the writer materializes exactly these when it re-renders the comment
			// block (which strips the entry), instead of dropping the cover.
			d.commentPictures = append(d.commentPictures, pic)
		}
	}
	return nil
}

// properties derives the audio properties from the identification header and the
// final granule position.
func (d *doc) properties(lastGranule uint64) core.Properties {
	t := core.AudioTrack{Codec: d.kind.String()}
	switch d.kind {
	case kindFLAC:
		// STREAMINFO rides in the identification packet and carries the full track
		// description, including a total sample count that beats the final granule
		// when the encoder wrote one.
		if si, err := vorbis.ParseStreamInfo(d.streamInfo()); err == nil {
			t = si
		}
		if t.TotalSamples == 0 {
			t.TotalSamples = lastGranule
			t.Duration = core.SamplesToDuration(lastGranule, t.SampleRate)
		}
	case kindVorbis:
		// \x01vorbis(7) | version(4) | channels(1) | sample_rate(4) |
		// bitrate_max(4) | bitrate_nominal(4) |...
		if len(d.idPacket) >= 16 {
			t.Channels = int(d.idPacket[11])
			t.SampleRate = int(binary.LittleEndian.Uint32(d.idPacket[12:16]))
		}
		if len(d.idPacket) >= 24 {
			if nominal := int32(binary.LittleEndian.Uint32(d.idPacket[20:24])); nominal > 0 {
				t.Bitrate = int(nominal)
			}
		}
		t.TotalSamples = lastGranule
		t.Duration = core.SamplesToDuration(lastGranule, t.SampleRate)
	default: // Opus
		// OpusHead(8) | version(1) | channels(1) | pre_skip(2) | input_rate(4) |
		// output_gain(2) | mapping_family(1). Opus always decodes at 48 kHz; the
		// input rate is informational. output_gain travels with the essence config.
		var preSkip uint64
		if len(d.idPacket) >= 12 {
			t.Channels = int(d.idPacket[9])
			preSkip = uint64(binary.LittleEndian.Uint16(d.idPacket[10:12]))
		}
		t.SampleRate = 48000
		if lastGranule > preSkip {
			t.TotalSamples = lastGranule - preSkip
			t.Duration = core.SamplesToDuration(t.TotalSamples, 48000)
		}
	}
	// A nominal bitrate from the Vorbis identification header wins; otherwise
	// derive an average from the audio page bodies via the shared core helper.
	if t.Bitrate == 0 {
		var audioBytes int64
		for _, ap := range d.audioPages {
			audioBytes += ap.bodyLen
		}
		t.Bitrate = core.AverageBitrate(audioBytes, t.Duration.Seconds())
	}
	return core.Properties{Container: "Ogg", Tracks: []core.AudioTrack{t}}
}
