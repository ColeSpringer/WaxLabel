package vorbis

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// ParsePicture decodes a FLAC PICTURE block body into a Picture. The same binary
// layout is the payload of an Ogg METADATA_BLOCK_PICTURE comment (after
// base64-decoding), so both formats share this decoder.
func ParsePicture(body []byte, limit int64) (core.Picture, error) {
	c := bits.NewCursor(bytes.NewReader(body), int64(len(body)), limit)
	var p core.Picture
	p.Type = core.PictureType(c.U32BE())
	p.MIME = string(c.Bytes(int64(c.U32BE())))
	p.Description = string(c.Bytes(int64(c.U32BE())))
	p.Width = int(c.U32BE())
	p.Height = int(c.U32BE())
	p.Depth = int(c.U32BE())
	p.Colors = int(c.U32BE())
	p.Data = c.Bytes(int64(c.U32BE()))
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
