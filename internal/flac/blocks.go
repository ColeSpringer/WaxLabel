package flac

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// parseStreamInfo decodes the 34-byte STREAMINFO body into an AudioTrack. The
// bit-packed region holds a 20-bit sample rate, 3-bit channel count (less
// one), 5-bit bits-per-sample (less one), and 36-bit total sample count.
func parseStreamInfo(body []byte) (core.AudioTrack, error) {
	if len(body) < streamInfoLen {
		return core.AudioTrack{}, fmt.Errorf("%w: STREAMINFO is %d bytes, need %d", waxerr.ErrInvalidData, len(body), streamInfoLen)
	}
	t := core.AudioTrack{Codec: "flac"}
	t.MinBlockSize = int(binary.BigEndian.Uint16(body[0:2]))
	t.MaxBlockSize = int(binary.BigEndian.Uint16(body[2:4]))

	t.SampleRate = int(body[10])<<12 | int(body[11])<<4 | int(body[12])>>4
	t.Channels = int(body[12]>>1&0x07) + 1
	t.BitsPerSample = int((body[12]&0x01)<<4|body[13]>>4) + 1
	t.TotalSamples = uint64(body[13]&0x0F)<<32 | uint64(body[14])<<24 |
		uint64(body[15])<<16 | uint64(body[16])<<8 | uint64(body[17])
	copy(t.MD5[:], body[18:34])

	if t.SampleRate == 0 {
		return t, fmt.Errorf("%w: STREAMINFO sample rate is zero", waxerr.ErrInvalidData)
	}
	// Guard against pathological inputs (e.g. SampleRate 1 with TotalSamples
	// near 2^36) overflowing the int64 nanoseconds of time.Duration into garbage.
	if ns := float64(t.TotalSamples) / float64(t.SampleRate) * float64(time.Second); ns >= 0 && ns < math.MaxInt64 {
		t.Duration = time.Duration(ns)
	}
	return t, nil
}

// parseVorbisComment decodes a Vorbis comment block body (little-endian
// lengths, no FLAC framing bit) into a vendor string and ordered comments.
func parseVorbisComment(body []byte, limit int64) (vendor string, comments []comment, err error) {
	c := bits.NewCursor(bytes.NewReader(body), int64(len(body)), limit)
	vlen := int64(c.U32LE())
	vendor = string(c.Bytes(vlen))
	count := c.U32LE()
	for i := uint32(0); i < count; i++ {
		if c.Err() != nil {
			break
		}
		l := int64(c.U32LE())
		entry := c.Bytes(l)
		if c.Err() != nil {
			break
		}
		name, value, ok := strings.Cut(string(entry), "=")
		if !ok {
			continue // malformed entry without '='; drop from projection
		}
		comments = append(comments, comment{name: name, value: value})
	}
	if c.Err() != nil {
		return vendor, comments, fmt.Errorf("vorbis comment: %w", c.Err())
	}
	return vendor, comments, nil
}

// renderVorbisComment encodes a vendor string and comments back into a block
// body. Deterministic: same inputs produce identical bytes.
func renderVorbisComment(vendor string, comments []comment) []byte {
	var buf bytes.Buffer
	writeU32LE(&buf, uint32(len(vendor)))
	buf.WriteString(vendor)
	writeU32LE(&buf, uint32(len(comments)))
	for _, cm := range comments {
		entry := cm.name + "=" + cm.value
		writeU32LE(&buf, uint32(len(entry)))
		buf.WriteString(entry)
	}
	return buf.Bytes()
}

// parsePictureBlock decodes a PICTURE block body.
func parsePictureBlock(body []byte, limit int64) (core.Picture, error) {
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

// renderPicture encodes a Picture into a PICTURE block body.
func renderPicture(p core.Picture) []byte {
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

// renderBlock encodes a full metadata block: a 1-byte header (last-block flag
// in bit 7, type code in bits 0–6), a 24-bit big-endian length, then the body.
func renderBlock(code byte, last bool, body []byte) []byte {
	hdr := make([]byte, 4)
	hdr[0] = code & 0x7F
	if last {
		hdr[0] |= 0x80
	}
	n := len(body)
	hdr[1] = byte(n >> 16)
	hdr[2] = byte(n >> 8)
	hdr[3] = byte(n)
	return append(hdr, body...)
}

func writeU32LE(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

func writeU32BE(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}
