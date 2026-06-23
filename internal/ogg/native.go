// Package ogg implements reading and writing metadata for Ogg Vorbis and Ogg
// Opus for the public waxlabel package. The codec itself is internal. Both
// codecs store tags as a Vorbis comment list and cover art as
// METADATA_BLOCK_PICTURE entries, so the comment and picture codecs are shared
// with FLAC via internal/vorbis; the Ogg-specific work is the page layer.
//
// The write invariant is that the audio *packet payloads* are preserved
// byte-for-byte (Ogg re-pagination is allowed, page checksums are not the
// payload). The codec is reimplemented from RFC 3533 (Ogg), the Vorbis I and
// Vorbis-comment specifications, and RFC 7845 (Ogg Opus); reference
// implementations were consulted for design only.
package ogg

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// kind distinguishes the two Ogg codecs WaxLabel writes. Both store tags as a
// Vorbis comment list; they differ in the comment-header framing and in which
// header packets are decoder-critical.
type kind uint8

const (
	kindVorbis kind = iota
	kindOpus
)

// String is the codec name surfaced in the raw model and JSON properties.codec.
// Titlecasing ("Opus"/"Vorbis") matches the Matroska reader, so Opus and Vorbis
// read identically across the Ogg and Matroska containers. The central CanonicalCodec
// step (applied after parse) normalizes the codecs that need it - e.g. FLAC's "flac"
// -> "FLAC", MP4's "mp4a" -> "AAC" - but leaves Opus/Vorbis untouched, since they are
// already the canonical names; getting their case right here is what keeps them
// consistent. The text dump uppercases independently, so this affects only the
// raw/JSON view, not display.
func (k kind) String() string {
	if k == kindOpus {
		return "Opus"
	}
	return "Vorbis"
}

// apage is an audio page descriptor: enough to copy the page verbatim and, when
// the header region's page count changes, to renumber it (rewrite its sequence
// number and patch its CRC) without re-reading the body bytes.
type apage struct {
	off     int64
	total   int64
	bodyLen int64
	seq     uint32
	crc     uint32
	granule uint64
}

func (p apage) bodyOff() int64 { return p.off + (p.total - p.bodyLen) }

// doc is the Ogg native document: the decoder-critical header packets kept
// verbatim, the decoded comment list and pictures, and a descriptor for every
// audio page (headers only - never the audio bytes). It is the preservation-first
// base for rewrites and satisfies core.NativeDoc.
type doc struct {
	format core.Format // FormatOggVorbis or FormatOggOpus
	kind   kind
	serial uint32

	vendor   string
	comments []vorbis.Comment // tag comments (METADATA_BLOCK_PICTURE excluded)
	pictures []core.Picture   // decoded from METADATA_BLOCK_PICTURE comments

	idPacket    []byte // packet 1 (Vorbis identification / OpusHead), verbatim
	setupPacket []byte // Vorbis setup header (packet 3), verbatim; nil for Opus
	commentPad  []byte // bytes after the comment list in the comment packet (Opus padding), preserved

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
	return &c
}

// Describe summarizes the native structure for the dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	idKind, commentKind := "Vorbis identification header", "Vorbis comment header"
	if d.kind == kindOpus {
		idKind, commentKind = "OpusHead", "OpusTags"
	}
	out := []core.NativeEntry{
		{Kind: idKind, Size: len(d.idPacket)},
		{Kind: commentKind, Note: "vendor=" + d.vendor},
	}
	if len(d.setupPacket) > 0 {
		out = append(out, core.NativeEntry{Kind: "Vorbis setup header", Size: len(d.setupPacket), Note: "preserved"})
	}
	for range d.pictures {
		out = append(out, core.NativeEntry{Kind: "METADATA_BLOCK_PICTURE", Note: "embedded picture"})
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
