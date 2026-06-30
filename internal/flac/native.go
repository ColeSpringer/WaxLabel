// Package flac implements reading and writing FLAC metadata for the public
// waxlabel package. The codec itself is internal. It is reimplemented from the
// FLAC format specification; reference implementations were consulted for
// design only.
package flac

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// FLAC metadata block type codes.
const (
	blkStreamInfo    = 0
	blkPadding       = 1
	blkApplication   = 2
	blkSeekTable     = 3
	blkVorbisComment = 4
	blkCueSheet      = 5
	blkPicture       = 6
	blkInvalid       = 127

	streamInfoLen = 34
	maxBlockBody  = 1<<24 - 1 // 24-bit length field
)

func blockName(code byte) string {
	switch code {
	case blkStreamInfo:
		return "STREAMINFO"
	case blkPadding:
		return "PADDING"
	case blkApplication:
		return "APPLICATION"
	case blkSeekTable:
		return "SEEKTABLE"
	case blkVorbisComment:
		return "VORBIS_COMMENT"
	case blkCueSheet:
		return "CUESHEET"
	case blkPicture:
		return "PICTURE"
	default:
		return "UNKNOWN"
	}
}

// block is one raw metadata block, header excluded. The body is preserved
// verbatim so unedited blocks (SEEKTABLE, CUESHEET, APPLICATION, unknown
// types) round-trip byte-for-byte.
type block struct {
	code byte
	body []byte
}

func (b block) clone() block { return block{code: b.code, body: slices.Clone(b.body)} }

// comment is one Vorbis "NAME=value" entry, keeping the original name spelling
// so unedited comments preserve their exact form.
type comment struct {
	name  string
	value string
}

// doc is the FLAC native document: the parsed blocks in original order plus
// the decoded Vorbis comments and pictures. It is the preservation-first base
// for rewrites and satisfies [core.NativeDoc].
type doc struct {
	leadingID3    []byte // stray ID3v2 before "fLaC", preserved
	trailingID3v1 []byte // 128-byte ID3v1 after audio, preserved

	blocks   []block   // all metadata blocks, in original order
	vendor   string    // Vorbis comment vendor string
	comments []comment // decoded Vorbis comments, in order (picture comments stripped)
	// commentPictures holds covers decoded from base64 METADATA_BLOCK_PICTURE comments (the
	// Ogg form some encoders use in FLAC). They are stripped from comments above and projected
	// into Media.Pictures; the writer materializes exactly these into native PICTURE blocks on
	// a metadata-rewriting edit, so a tag-only edit does not silently drop the cover.
	commentPictures []core.Picture

	streamInfo core.AudioTrack

	flacStart  int64 // offset of "fLaC" (== len(leadingID3))
	audioStart int64 // first audio byte (after last metadata block)
	audioEnd   int64 // one past last audio byte (== size - len(trailingID3v1))
}

func (d *doc) Format() core.Format { return core.FormatFLAC }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := &doc{
		leadingID3:      slices.Clone(d.leadingID3),
		trailingID3v1:   slices.Clone(d.trailingID3v1),
		vendor:          d.vendor,
		comments:        slices.Clone(d.comments),
		commentPictures: core.ClonePictures(d.commentPictures),
		streamInfo:      d.streamInfo,
		flacStart:       d.flacStart,
		audioStart:      d.audioStart,
		audioEnd:        d.audioEnd,
	}
	c.blocks = make([]block, len(d.blocks))
	for i, b := range d.blocks {
		c.blocks[i] = b.clone()
	}
	return c
}

// Describe summarizes the native blocks for the dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	var out []core.NativeEntry
	if len(d.leadingID3) > 0 {
		out = append(out, core.NativeEntry{Kind: "ID3v2 (leading)", Size: len(d.leadingID3), Note: "preserved"})
	}
	for _, b := range d.blocks {
		e := core.NativeEntry{Kind: blockName(b.code), Size: len(b.body)}
		switch b.code {
		case blkVorbisComment:
			e.Note = "vendor=" + d.vendor
		case blkPicture:
			e.Note = "embedded picture"
		}
		out = append(out, e)
	}
	if len(d.trailingID3v1) > 0 {
		out = append(out, core.NativeEntry{Kind: "ID3v1 (trailing)", Size: len(d.trailingID3v1), Note: "preserved"})
	}
	return out
}
