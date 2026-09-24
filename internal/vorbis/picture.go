package vorbis

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// PictureComment is the Vorbis comment name that carries base64-encoded cover art in Ogg
// Vorbis and Opus. The value is a FLAC PICTURE block payload.
const PictureComment = "METADATA_BLOCK_PICTURE"

// pictureCommentBase64Error is the warning message for a METADATA_BLOCK_PICTURE comment
// whose value is not valid base64. The FLAC and Ogg parsers share it via
// DecodePictureComment.
const pictureCommentBase64Error = "METADATA_BLOCK_PICTURE is not valid base64; preserved as a comment"

// IsPictureComment reports whether a comment name is the cover-art picture comment,
// case-insensitively. Lowercase spellings are decoded as pictures at parse time, so the tag
// projector must skip them the same way.
func IsPictureComment(name string) bool {
	return strings.EqualFold(name, PictureComment)
}

// DecodePictureComment base64-decodes a METADATA_BLOCK_PICTURE comment value and parses it
// into a Picture. On failure it returns the shared base64 message or ParsePicture's error,
// and the caller warns WarnInvalidPicture and preserves the comment verbatim. The FLAC and
// Ogg picture-comment decoders share it.
func DecodePictureComment(value string, limit int64) (core.Picture, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return core.Picture{}, errors.New(pictureCommentBase64Error)
	}
	return ParsePicture(raw, limit)
}

// ParsePicture decodes a FLAC PICTURE block body into a Picture. The same binary
// layout is the payload of an Ogg METADATA_BLOCK_PICTURE comment (after
// base64-decoding), so both formats share this decoder.
func ParsePicture(body []byte, limit int64) (core.Picture, error) {
	c := bits.NewCursor(bytes.NewReader(body), int64(len(body)), limit)
	var p core.Picture
	// The on-disk type is a 32-bit field, but the role space is a single byte (as in
	// ID3 APIC). Clamp a value past 255 to PicOther; a narrowing conversion would wrap
	// it into a valid role (259 -> "Front cover"). Picture bytes are unaffected.
	typ := c.U32BE()
	if typ > 255 {
		typ = 0 // PicOther; out of the single-byte ID3/FLAC type space
	}
	p.Type = core.PictureType(typ)
	p.MIME = string(c.Bytes(int64(c.U32BE())))
	// A non-conformant file can hold an invalid-UTF-8 description. Sanitize it like the
	// tag-value read paths, so the write-time UTF-8 guard does not reject a transfer that
	// re-adds this picture and --json stays valid.
	p.Description = core.SanitizeUTF8(string(c.Bytes(int64(c.U32BE()))))
	p.Width = int(c.U32BE())
	p.Height = int(c.U32BE())
	p.Depth = int(c.U32BE())
	p.Colors = int(c.U32BE())
	p.Data = c.Bytes(int64(c.U32BE()))
	// MIME and dimensions come back as stored, not sniffed. This decoder is also the
	// re-serialization source (FLAC materializes a comment cover into a native block, Ogg
	// re-emits the comment), so a corrected MIME would be written back on an unrelated
	// edit. Type detection runs on the display copy, where the FLAC and Ogg parsers hand
	// media.Pictures to core.ProjectPictures. id3/mp4/matroska can sniff at read because
	// their writers preserve the picture verbatim.
	if c.Err() != nil {
		return core.Picture{}, fmt.Errorf("picture block: %w", c.Err())
	}
	return p, nil
}

// RenderPicture encodes a Picture into a PICTURE block body (big-endian
// lengths). Deterministic.
func RenderPicture(p core.Picture) []byte {
	var buf bytes.Buffer
	writeU32BE(&buf, uint32(p.Type))
	writeU32BE(&buf, uint32(len(p.MIME)))
	buf.WriteString(p.MIME)
	writeU32BE(&buf, uint32(len(p.Description)))
	buf.WriteString(p.Description)
	writeU32BE(&buf, uint32(p.Width))
	writeU32BE(&buf, uint32(p.Height))
	writeU32BE(&buf, uint32(p.Depth))
	writeU32BE(&buf, uint32(p.Colors))
	writeU32BE(&buf, uint32(len(p.Data)))
	buf.Write(p.Data)
	return buf.Bytes()
}

// PictureCommentLen returns the size of the picture as a base64 METADATA_BLOCK_PICTURE
// comment value, so a write-side size check can measure a cover without rendering it.
// It equals base64.StdEncoding.EncodedLen(len(RenderPicture(p))), computed from
// RenderPicture's layout (eight 32-bit fields, then MIME, description, and image bytes).
// TestPictureCommentLenMatchesRender checks the arithmetic against RenderPicture.
func PictureCommentLen(p core.Picture) int64 {
	const fixedFields = 8 * 4 // the eight 32-bit fields RenderPicture writes before the image data
	rendered := fixedFields + len(p.MIME) + len(p.Description) + len(p.Data)
	return int64(base64.StdEncoding.EncodedLen(rendered))
}

func writeU32BE(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}
