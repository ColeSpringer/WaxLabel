// Package ogg implements Ogg Vorbis/Opus/FLAC metadata for waxlabel. Internal.
// Tags are Vorbis comments; art is METADATA_BLOCK_PICTURE (via internal/vorbis)
// except the FLAC mapping's native PICTURE blocks. Page layer is Ogg-specific.
//
// Write invariant: audio packet payloads stay byte-identical (re-pagination OK;
// page checksums are not payload). From RFC 3533, Vorbis I / comments, RFC 7845.

package ogg

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
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
	granule uint64
}

func (p apage) bodyOff() int64 { return p.off + (p.total - p.bodyLen) }

// doc is the Ogg native document: verbatim decoder-critical headers, comments,
// pictures, and per-audio-page descriptors (headers only). Implements core.NativeDoc.

type doc struct {
	format core.Format // FormatOggVorbis or FormatOggOpus
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

	// origCommentPacketLen: parsed comment packet length. Write floors its size guard here
	// so data already readable under the parse limit stays writable under a lower write limit.

	origCommentPacketLen int64

	page0Len    int64 // BOS page length (the id packet, alone; copied verbatim)
	headerPages int   // number of pages in the header region
	audioStart  int64 // first audio-page offset (== end of the header region)
	audioPages  []apage
	audioEnd    int64 // one past the last audio page
	trailingLen int64 // bytes after the last page (preserved by copying from the source; usually 0)

	clean   bool // header and audio are cleanly page-aligned (writable)
	chained bool // chained or multiplexed stream (read best-effort; write refused)
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
	out := []core.NativeEntry{
		{Kind: idKind, Size: len(d.idPacket), Note: idNote},
		{Kind: commentKind, Note: "vendor=" + d.vendor},
	}
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
	if len(d.commentPad) > 0 {
		out = append(out, core.NativeEntry{Kind: "comment padding", Size: len(d.commentPad), Note: "preserved"})
	}
	if d.trailingLen > 0 {
		out = append(out, core.NativeEntry{Kind: "trailing bytes", Size: int(d.trailingLen), Note: "preserved"})
	}
	return out
}
