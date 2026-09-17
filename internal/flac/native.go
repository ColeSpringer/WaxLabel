// Package flac implements reading and writing FLAC metadata for the public
// waxlabel package. Internal; reimplemented from the FLAC specification.
package flac

import (
	"encoding/binary"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// Block type codes and 24-bit body limit, shared with the Ogg mapping via internal/vorbis.
const (
	blkStreamInfo    = vorbis.BlockStreamInfo
	blkPadding       = vorbis.BlockPadding
	blkApplication   = vorbis.BlockApplication
	blkSeekTable     = vorbis.BlockSeekTable
	blkVorbisComment = vorbis.BlockVorbisComment
	blkCueSheet      = vorbis.BlockCueSheet
	blkPicture       = vorbis.BlockPicture
	blkInvalid       = vorbis.BlockInvalid

	streamInfoLen = vorbis.StreamInfoLen
	maxBlockBody  = vorbis.MaxBlockBody
)

func blockName(code byte) string { return vorbis.BlockName(code) }

// block is one raw metadata block without its header. Body is kept verbatim so
// unedited blocks (SEEKTABLE, CUESHEET, APPLICATION, unknown types) round-trip.
type block struct {
	code byte
	body []byte
}

func (b block) clone() block { return block{code: b.code, body: slices.Clone(b.body)} }

// comment is one Vorbis "NAME=value" entry. Name spelling is kept for unedited
// comments. unseparated marks an entry with no "=": empty name, value holds the
// entry bytes verbatim (see [vorbis.Comment]).
type comment struct {
	name        string
	value       string
	unseparated bool
}

// doc is the parsed FLAC native document. Implements [core.NativeDoc].
type doc struct {
	leadingID3    []byte // stray ID3v2 before "fLaC", preserved
	trailingID3v1 []byte // 128-byte ID3v1 after audio, preserved

	blocks   []block   // all metadata blocks, in original order
	vendor   string    // Vorbis comment vendor string
	comments []comment // decoded Vorbis comments, in order (picture comments stripped)
	// commentPictures: covers from base64 METADATA_BLOCK_PICTURE comments (Ogg-style
	// in FLAC). Stripped from comments and projected into Media.Pictures; on a
	// metadata rewrite the writer emits them as native PICTURE blocks so a tag-only
	// edit does not drop the cover.
	commentPictures []core.Picture
	// malformedPictureBlocks: native PICTURE bodies that failed decode at parse
	// (warned, omitted from Media.Pictures). On a picture edit the writer re-appends
	// them verbatim. Stored at parse so write-time alloc limits cannot reclassify
	// them. Parse aliases blocks entries; Clone copies independently.
	malformedPictureBlocks [][]byte
	// dupContent: payload of each extra Vorbis comment block. Rewrite keeps only
	// the first; the writer grades these against what it stores.
	dupContent []core.DuplicateContent

	streamInfo core.AudioTrack

	flacStart  int64 // offset of "fLaC" (== len(leadingID3))
	audioStart int64 // first audio byte (after last metadata block)
	audioEnd   int64 // one past last audio byte (excludes trailingJunk and trailingID3v1)
	// trailingJunk: bytes between the last frame and any ID3v1 trailer
	// (frameTailWarnings). Outside the audio extent; copied on rewrite.
	trailingJunk int64
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
		// so a picture edit on the clone still re-appends undecodable blocks
		malformedPictureBlocks: cloneByteSlices(d.malformedPictureBlocks),
		dupContent:             slices.Clone(d.dupContent),
		flacStart:              d.flacStart,
		audioStart:             d.audioStart,
		audioEnd:               d.audioEnd,
		trailingJunk:           d.trailingJunk,
	}
	c.blocks = make([]block, len(d.blocks))
	for i, b := range d.blocks {
		c.blocks[i] = b.clone()
	}
	return c
}

// cloneByteSlices deep-copies a [][]byte. Nil in yields nil out.
func cloneByteSlices(in [][]byte) [][]byte {
	if in == nil {
		return nil
	}
	out := make([][]byte, len(in))
	for i, b := range in {
		out[i] = slices.Clone(b)
	}
	return out
}

// Describe summarizes native blocks for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	var out []core.NativeEntry
	if len(d.leadingID3) > 0 {
		out = append(out, core.NativeEntry{Kind: "ID3v2 (leading)", Size: len(d.leadingID3), Note: "preserved"})
	}
	for _, b := range d.blocks {
		e := core.NativeEntry{Kind: blockName(b.code), Size: len(b.body)}
		switch b.code {
		case blkVorbisComment:
			// Per-block vendor; d.vendor is only the first block's.
			e.Note = "vendor=" + vendorOf(b.body)
		case blkPicture:
			e.Note = "embedded picture"
		}
		out = append(out, e)
	}
	if d.trailingJunk > 0 {
		out = append(out, core.NativeEntry{Kind: "trailing bytes", Size: int(d.trailingJunk), Note: "preserved"})
	}
	if len(d.trailingID3v1) > 0 {
		out = append(out, core.NativeEntry{Kind: "ID3v1 (trailing)", Size: len(d.trailingID3v1), Note: "preserved"})
	}
	return out
}

// PaddingBytes is the reusable padding region for in-place rewrite (PaddingAfter).
// Not a sum of PADDING bodies: rewrite drops source PADDING and fills the same
// region, so k blocks collapse and k-1 headers become payload. Budget the whole
// region through paddingBlocks so the plan matches the writer.
func (d *doc) PaddingBytes() int64 {
	budget := 0
	for _, b := range d.blocks {
		if b.code == blkPadding {
			budget += 4 + len(b.body) // header is reusable space too
		}
	}
	if budget == 0 {
		return 0
	}
	var total int64
	for _, pb := range paddingBlocks(budget) {
		total += int64(len(pb.body))
	}
	return total
}

// vendorOf reads the vendor from a VORBIS_COMMENT body (LE uint32 length, then
// bytes) without a full parse. Short or overrun bodies fall back to remaining
// bytes so a duplicate block reports its own vendor. sz<0 matches int(uint32)
// overflow on 32-bit. Renderers sanitize control bytes, so raw return is fine.
func vendorOf(body []byte) string {
	if len(body) < 4 {
		return string(body)
	}
	sz := int(binary.LittleEndian.Uint32(body[:4]))
	if sz < 0 || sz > len(body)-4 {
		return string(body[4:])
	}
	return string(body[4 : 4+sz])
}
