// Package id3 implements reading and writing ID3v2 tags (v2.2, v2.3, v2.4) and
// reading ID3v1. It is internal through v0.x and shared by the container codecs
// that embed ID3 — MP3 now, WAV/AIFF later — so the frame model, text encodings,
// unsynchronisation, the numeric-genre table, and the canonical projection live
// here once. It is reimplemented from the ID3v2.2/2.3/2.4 specifications;
// reference implementations were consulted for design only.
//
// The native model is preservation-first: every frame is kept (decoded ones in
// a clean, re-renderable form; compressed/encrypted/unknown ones verbatim) in
// original order, so a tag edit rewrites only the affected frames. v2.2 is read
// in full but normalised to v2.3 frame identifiers internally and written back
// as v2.3 (v2.2 is obsolete); v2.3 and v2.4 round-trip at their own version.
package id3

import (
	"fmt"

	"github.com/colespringer/waxlabel/waxerr"
)

// Header flag bits (the ID3v2 header's sixth byte).
const (
	hdrUnsync    = 0x80 // whole-tag (v2.2/v2.3) or all-frames (v2.4) unsynchronisation
	hdrExtHeader = 0x40 // an extended header follows (v2.3/v2.4)
	hdrFooter    = 0x10 // a 10-byte footer trails the tag (v2.4)
)

// Tag is a parsed ID3v2 tag: the decoded frames in original order plus the
// version metadata needed to write them back at the right version. srcVersion
// records what was read (2, 3, or 4) for reporting; writeVersion is the version
// actually emitted (3 or 4 — a v2.2 source is modernised to v2.3).
type Tag struct {
	srcVersion   byte
	writeVersion byte
	revision     byte
	frames       []Frame
}

// SrcVersion reports the ID3v2 minor version that was parsed (2, 3, or 4).
func (t *Tag) SrcVersion() byte { return t.srcVersion }

// WriteVersion reports the version a rewrite will emit (3 or 4).
func (t *Tag) WriteVersion() byte { return t.writeVersion }

// Frames returns the decoded frames in order.
func (t *Tag) Frames() []Frame { return t.frames }

// APICCount returns the number of attached-picture (APIC) frames in the tag,
// shared by the container codecs that embed ID3v2 (MP3, WAV, AIFF) for their
// write reports. A nil tag has none.
func APICCount(t *Tag) int {
	if t == nil {
		return 0
	}
	n := 0
	for _, f := range t.frames {
		if f.ID == "APIC" {
			n++
		}
	}
	return n
}

// Clone returns a deep copy of the tag.
func (t *Tag) Clone() *Tag {
	c := *t
	c.frames = make([]Frame, len(t.frames))
	for i, f := range t.frames {
		c.frames[i] = f.Clone()
	}
	return &c
}

// WithFrames returns a copy of the tag carrying frames, for building the
// post-write document.
func (t *Tag) WithFrames(frames []Frame) *Tag {
	c := *t
	c.frames = frames
	return &c
}

// NewEmpty returns an empty tag that will be written at the given version
// (clamped to v2.3 or v2.4), for files that have no ID3v2 tag yet.
func NewEmpty(writeVersion byte) *Tag {
	if writeVersion != 4 {
		writeVersion = 3
	}
	return &Tag{srcVersion: writeVersion, writeVersion: writeVersion}
}

// TagSize returns the total on-disk length of the ID3v2 tag whose 10-byte header
// begins header, and whether header is a valid ID3v2 header. The size field is
// sync-safe (7 bits per byte) and excludes the 10-byte header; a v2.4 footer
// adds another 10 bytes.
func TagSize(header []byte) (int64, bool) {
	if len(header) < 10 || header[0] != 'I' || header[1] != 'D' || header[2] != '3' {
		return 0, false
	}
	if header[3] == 0xFF || header[4] == 0xFF {
		return 0, false // reserved; not a real version
	}
	if !syncSafeByte(header[6]) || !syncSafeByte(header[7]) ||
		!syncSafeByte(header[8]) || !syncSafeByte(header[9]) {
		return 0, false
	}
	size := syncSafe(header[6:10])
	total := int64(10) + size
	if header[5]&hdrFooter != 0 {
		total += 10
	}
	return total, true
}

// syncSafe decodes a 28-bit sync-safe integer from four bytes (each contributes
// its low 7 bits).
func syncSafe(b []byte) int64 {
	return int64(b[0]&0x7F)<<21 | int64(b[1]&0x7F)<<14 | int64(b[2]&0x7F)<<7 | int64(b[3]&0x7F)
}

func syncSafeByte(b byte) bool { return b&0x80 == 0 }

// putSyncSafe writes v as a 28-bit sync-safe integer into a 4-byte slice.
func putSyncSafe(dst []byte, v int64) {
	dst[0] = byte(v>>21) & 0x7F
	dst[1] = byte(v>>14) & 0x7F
	dst[2] = byte(v>>7) & 0x7F
	dst[3] = byte(v) & 0x7F
}

// ParseTag decodes a complete ID3v2 tag region (starting at the "ID3" header).
// It tolerates truncation (parsing what is present) but rejects a missing or
// reserved header. The caller bounds the size of data when reading it from the
// source, so parsing the in-memory region needs no further allocation limit.
func ParseTag(data []byte) (*Tag, error) {
	size, ok := TagSize(data)
	if !ok {
		return nil, fmt.Errorf("%w: not an ID3v2 header", waxerr.ErrInvalidData)
	}
	major := data[3]
	if major < 2 || major > 4 {
		return nil, fmt.Errorf("%w: unsupported ID3v2.%d", waxerr.ErrUnsupportedFormat, major)
	}
	flags := data[5]

	end := size
	if flags&hdrFooter != 0 {
		end -= 10 // the footer is not part of the frame region
	}
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	body := data[10:end]

	t := &Tag{srcVersion: major, revision: data[4]}
	t.writeVersion = major
	if major == 2 {
		t.writeVersion = 3 // modernise obsolete v2.2 on write
	}

	// v2.2/v2.3 unsynchronisation covers the whole tag; undo it before parsing.
	// v2.4 signals unsynchronisation per frame (the header bit, if set, means it
	// applies to all frames — handled in parseFrames).
	tagUnsync := flags&hdrUnsync != 0
	if major <= 3 && tagUnsync {
		body = deunsync(body)
	}
	if (major == 3 || major == 4) && flags&hdrExtHeader != 0 {
		body = skipExtHeader(body, major)
	}

	t.frames = parseFrames(body, major, tagUnsync)
	return t, nil
}

// skipExtHeader advances past the optional extended header. v2.3's size field
// counts the bytes after it (6 or 10); v2.4's sync-safe size counts the whole
// extended header including the size field. A malformed size leaves the body
// untouched rather than discarding frames.
func skipExtHeader(body []byte, major byte) []byte {
	if len(body) < 4 {
		return body
	}
	if major == 4 {
		n := syncSafe(body[0:4])
		if n >= 4 && n <= int64(len(body)) {
			return body[n:]
		}
		return body
	}
	// v2.3: 4-byte plain size of the rest of the extended header.
	n := int64(body[0])<<24 | int64(body[1])<<16 | int64(body[2])<<8 | int64(body[3])
	if total := 4 + n; total >= 4 && total <= int64(len(body)) {
		return body[total:]
	}
	return body
}
