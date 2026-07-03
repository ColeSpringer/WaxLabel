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

// pictureCommentBase64Error is the warning message for a METADATA_BLOCK_PICTURE comment whose
// value is not valid base64. The FLAC and Ogg parsers both preserve such a comment verbatim, so
// sharing the wording (via DecodePictureComment) keeps the two from drifting.
const pictureCommentBase64Error = "METADATA_BLOCK_PICTURE is not valid base64; preserved as a comment"

// IsPictureComment reports whether a comment name is the cover-art picture comment,
// case-insensitively. Lowercase spellings are decoded as pictures at parse time, so the tag
// projector must skip them the same way.
func IsPictureComment(name string) bool {
	return strings.EqualFold(name, PictureComment)
}

// DecodePictureComment base64-decodes a METADATA_BLOCK_PICTURE comment value and parses it into a
// Picture. On failure it returns a descriptive error - the shared base64 message, or ParsePicture's
// own error - so the caller warns with WarnInvalidPicture and preserves the comment verbatim. It is
// the shared core of the FLAC and Ogg picture-comment decoders, so neither the flow nor the base64
// wording can drift between the two formats.
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
	// The on-disk type is a 32-bit field, but the defined + reserved role space is a single
	// byte (matches ID3 APIC). A value past 255 is non-conformant; clamp it to PicOther (the
	// honest "undefined role" default) rather than letting the narrowing conversion wrap it
	// into a misleading valid role (e.g. 259 -> "Front cover"). Picture bytes are unaffected.
	typ := c.U32BE()
	if typ > 255 {
		typ = 0 // PicOther; out of the single-byte ID3/FLAC type space
	}
	p.Type = core.PictureType(typ)
	p.MIME = string(c.Bytes(int64(c.U32BE())))
	// The description is stored as raw bytes; a non-conformant file can hold invalid UTF-8.
	// Sanitize it into the model like the tag-value read paths, so a transfer that re-adds
	// this picture is not rejected by the write-time UTF-8 guard and --json stays valid.
	p.Description = core.SanitizeUTF8(string(c.Bytes(int64(c.U32BE()))))
	p.Width = int(c.U32BE())
	p.Height = int(c.U32BE())
	p.Depth = int(c.U32BE())
	p.Colors = int(c.U32BE())
	p.Data = c.Bytes(int64(c.U32BE()))
	// The MIME and dimensions come back as stored, not sniffed. This decoder doubles as the
	// re-serialization source (FLAC materializes a comment cover into a native block, and Ogg
	// re-emits the comment), so correcting a mislabeled MIME here would write the sniffed value back
	// on an unrelated edit. Type detection happens on the display copy instead, where the FLAC and
	// Ogg parsers hand media.Pictures to core.ProjectPictures. id3/mp4/matroska sniff at read too,
	// but their writers preserve the picture verbatim, so the sniffed type never reaches disk.
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

func writeU32BE(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}
