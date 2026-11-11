// Package ogg implements Ogg Vorbis/Opus/FLAC metadata for waxlabel. Internal.
// Tags are Vorbis comments; art is METADATA_BLOCK_PICTURE (via internal/vorbis)
// except the FLAC mapping's native PICTURE blocks. Page layer is Ogg-specific.
//
// Write invariant: audio packet payloads stay byte-identical (re-pagination OK;
// page checksums are not payload). From RFC 3533, Vorbis I / comments, RFC 7845.
package ogg

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/waxerr"
)

// kind is Vorbis, Opus, or FLAC. All use Vorbis comments; they differ in header
// framing, which packets are decoder-critical, and FLAC cover art (native PICTURE
// block vs METADATA_BLOCK_PICTURE comment).
type kind uint8

const (
	kindVorbis kind = iota
	kindOpus
	kindFLAC
)

// String is the raw/JSON codec name. "Opus"/"Vorbis" match Matroska. CanonicalCodec
// normalizes others (e.g. flac→FLAC); Opus/Vorbis are already canonical. Text dump
// uppercases on its own.
func (k kind) String() string {
	switch k {
	case kindOpus:
		return "Opus"
	case kindFLAC:
		// Same raw name as native FLAC; CanonicalCodec uppercases both.
		return "flac"
	}
	return "Vorbis"
}

// apage describes an audio page for verbatim copy or renumber (seq + CRC patch)
// when the header page count changes.
type apage struct {
	off     int64
	total   int64
	bodyLen int64
	seq     uint32
	crc     uint32
}

func (p apage) bodyOff() int64 { return p.off + (p.total - p.bodyLen) }

// span is a run of source bytes.
type span struct{ off, len int64 }

// doc is the Ogg native document: verbatim decoder-critical headers, comments,
// pictures, and per-audio-page descriptors (headers only). Implements core.NativeDoc.
type doc struct {
	format core.Format // FormatOggVorbis, FormatOggOpus or FormatOggFLAC
	kind   kind
	serial uint32

	vendor   string
	comments []vorbis.Comment // tag comments (METADATA_BLOCK_PICTURE excluded)
	pictures []core.Picture   // decoded from METADATA_BLOCK_PICTURE comments

	idPacket    []byte // packet 1 (Vorbis identification / OpusHead / \x7FFLAC), verbatim
	setupPacket []byte // Vorbis setup header (packet 3), verbatim; nil for Opus and FLAC
	commentPad  []byte // bytes after the comment list in the comment packet (Opus padding), preserved

	// FLAC mapping: later header packets are FLAC metadata blocks (verbatim).
	// Cover art is PICTURE blocks here, not comment METADATA_BLOCK_PICTURE.
	flacBlocks []fblock
	// dupContent: payload of each extra Vorbis comment block (rewrite keeps first only).
	dupContent             []core.DuplicateContent
	malformedPictureBlocks [][]byte       // PICTURE bodies that failed to decode, preserved
	commentPictures        []core.Picture // covers found as METADATA_BLOCK_PICTURE comments

	// origCommentCount: how many comments the comment header was read holding, the covers
	// decoded out of it among them. The write-side count check floors at it.
	origCommentCount int

	// origCommentPacketLen: parsed comment packet length. Write floors its size guard here
	// so data already readable under the parse limit stays writable under a lower write limit.
	origCommentPacketLen int64

	leadingID3    []byte // stray ID3v2 before the first page, preserved
	leadingUnread int64  // how many bytes of leadingID3 the parse could not read
	// streamStart is where the first page sits: past leadingID3 and any bytes that precede the
	// page, kept with the tag, or alone and then copied verbatim.
	streamStart int64
	page0Len    int64  // BOS page length (the id packet, alone; copied verbatim)
	page0Seq    uint32 // BOS page sequence number; a rewrite numbers its pages on from it
	audioStart  int64  // end of the header pages: the first audio page, or bytes ahead of it
	audioPages  []apage
	audioEnd    int64 // one past the last audio page
	pagesEnd    int64 // one past the last page of any stream
	gaveUp      bool  // the read stopped there at its resync bounds, pages behind it unread
	// layout is what follows the pages, every tag in it legacy. With truncatedTail the file
	// ends inside a page, whose bytes run from pagesEnd to layout.Start.
	layout        tail.Layout
	truncatedTail bool
	// Bytes between pages, in gapCount places. A rewrite copies them where they sit, but
	// those among the header pages it re-paginates (headerGaps), which go ahead of the new ones.
	gapCount   int
	gapBytes   int64
	headerGaps []span

	// startGranule is the granule position the stream starts at: 0 unless the packets of the
	// first audio page were measured and say otherwise.
	startGranule uint64

	clean bool // header and audio are cleanly page-aligned
	// Streams one after another (chained) or beginning together (multiplexed): the first audio
	// stream is read, and a write is refused.
	chained     bool
	multiplexed bool
}

func (d *doc) Format() core.Format { return d.format }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.comments = slices.Clone(d.comments)
	c.pictures = core.ClonePictures(d.pictures)
	c.idPacket = slices.Clone(d.idPacket)
	c.setupPacket = slices.Clone(d.setupPacket)
	c.commentPad = slices.Clone(d.commentPad)
	c.audioPages = slices.Clone(d.audioPages)
	c.headerGaps = slices.Clone(d.headerGaps)
	c.leadingID3 = slices.Clone(d.leadingID3)
	c.layout = d.layout.Clone()
	// Deep-copy block/picture bodies; slice header clone would alias payloads.
	c.flacBlocks = make([]fblock, len(d.flacBlocks))
	for i, b := range d.flacBlocks {
		c.flacBlocks[i] = b.clone()
	}
	c.malformedPictureBlocks = make([][]byte, len(d.malformedPictureBlocks))
	for i, b := range d.malformedPictureBlocks {
		c.malformedPictureBlocks[i] = slices.Clone(b)
	}
	c.commentPictures = core.ClonePictures(d.commentPictures)
	c.dupContent = slices.Clone(d.dupContent)
	return &c
}

// RefuseEssence: a chained file's later links and the pages behind a read that gave up are not
// read, so the essence stands for part of the audio.
func (d *doc) RefuseEssence() error {
	switch {
	case d.chained:
		return fmt.Errorf("%w: only the first link of a chained Ogg file is read, so it has no audio essence digest", waxerr.ErrChainedStream)
	case d.gaveUp:
		return fmt.Errorf("%w: too much junk between Ogg pages: the read stops at offset %d, so the file has no audio essence digest", waxerr.ErrInvalidData, d.pagesEnd)
	}
	return nil
}

// PaddingBytes is Opus comment-packet padding (RFC 7845), round-tripped as-is.
// No padding control. FLAC PADDING under the mapping is not counted: rebuilds drop it
// (header re-pagination makes it useless).
func (d *doc) PaddingBytes() int64 { return int64(len(d.commentPad)) }

// Describe summarizes native structure for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	idKind, commentKind := "Vorbis identification header", "Vorbis comment header"
	switch d.kind {
	case kindOpus:
		idKind, commentKind = "OpusHead", "OpusTags"
	case kindFLAC:
		idKind, commentKind = "FLAC identification header", "VORBIS_COMMENT"
	}
	idNote := ""
	if g := opusOutputGain(d.idPacket); d.kind == kindOpus && g != 0 {
		idNote = "output gain " + core.OutputGainDB(g)
	}
	var out []core.NativeEntry
	if len(d.leadingID3) > 0 {
		out = append(out, core.LeadingID3Entry(len(d.leadingID3), d.leadingUnread))
	}
	if gap := d.streamStart - int64(len(d.leadingID3)); gap > 0 {
		out = append(out, core.LeadingBytesEntry(gap))
	}
	out = append(out,
		core.NativeEntry{Kind: idKind, Size: len(d.idPacket), Note: idNote},
		core.NativeEntry{Kind: commentKind, Note: "vendor=" + d.vendor})
	if len(d.setupPacket) > 0 {
		out = append(out, core.NativeEntry{Kind: "Vorbis setup header", Size: len(d.setupPacket), Note: "preserved"})
	}
	if d.kind == kindFLAC {
		for _, b := range d.flacBlocks {
			if b.code == flacBlkVorbisComment {
				continue // already listed above
			}
			out = append(out, core.NativeEntry{Kind: flacBlockName(b.code), Size: len(b.body)})
		}
	} else {
		for range d.pictures {
			out = append(out, core.NativeEntry{Kind: "METADATA_BLOCK_PICTURE", Note: "embedded picture"})
		}
	}
	out = append(out, core.NativeEntry{Kind: "audio pages", Size: len(d.audioPages), Unit: "pages"})
	if d.gapCount > 0 {
		out = append(out, core.NativeEntry{Kind: "bytes between pages", Size: int(d.gapBytes),
			Note: fmt.Sprintf("in %d place(s), preserved", d.gapCount)})
	}
	if len(d.commentPad) > 0 {
		out = append(out, core.NativeEntry{Kind: "comment padding", Size: len(d.commentPad), Note: "preserved"})
	}
	if d.truncatedTail {
		out = append(out, core.NativeEntry{Kind: "truncated page", Size: int(d.layout.Start - d.pagesEnd), Note: "preserved, not read"})
	}
	return append(out, d.layout.Entries("Ogg pages")...)
}
