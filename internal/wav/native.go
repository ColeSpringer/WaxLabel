package wav

import (
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// chunk records one top-level RIFF chunk by identifier and source byte range.
// Only small chunks (fmt, LIST, id3) have their bodies decoded into the doc; the
// data chunk and every ancillary chunk are kept here only as ranges and copied
// verbatim on rewrite, so a multi-megabyte data chunk is never read into memory.
type chunk struct {
	id      [4]byte
	bodyOff int64 // source offset of the body (after the 8-byte chunk header)
	bodyLen int64 // declared body length, excluding any trailing pad byte
	// dupTag marks a redundant duplicate tag container (a second LIST/INFO or id3
	// chunk). Only the first of each kind is authoritative; duplicates are
	// preserved verbatim on a no-op but dropped when the file is rewritten.
	dupTag bool
}

// id4 returns the chunk identifier as a string.
func (c chunk) id4() string { return string(c.id[:]) }

// infoItem is one LIST/INFO sub-chunk: its 4CC and the raw value bytes (the ZSTR
// content up to the first NUL, as stored). Keeping the raw bytes - rather than a
// decoded string - lets an unmapped item round-trip byte-for-byte (no
// Latin-1->UTF-8 re-encoding) while a mapped item is decoded on demand via text().
// Items whose 4CC is not in the canonical mapping are still kept so they survive.
type infoItem struct {
	id  [4]byte
	raw []byte
}

func (it infoItem) id4() string { return string(it.id[:]) }

// text decodes the value bytes for projection: UTF-8 when valid (what the ffmpeg
// family and modern taggers write), else Latin-1 (the historical RIFF code
// page), so a legacy é (0xE9) is a valid 'é' in the canonical model rather than
// an invalid-UTF-8 string.
func (it infoItem) text() string {
	if utf8.Valid(it.raw) {
		return string(it.raw)
	}
	r := make([]rune, len(it.raw))
	for i, c := range it.raw {
		r[i] = rune(c) // Latin-1: each byte is its own code point
	}
	return string(r)
}

// fmtChunk is the decoded "fmt " chunk: the decoder-critical geometry used for
// properties and the essence-digest configuration.
type fmtChunk struct {
	audioFormat   uint16
	channels      uint16
	sampleRate    uint32
	byteRate      uint32
	blockAlign    uint16
	bitsPerSample uint16
}

// doc is the WAV native document: every top-level chunk in order (with source
// ranges), the decoded fmt geometry, the decoded LIST/INFO items and embedded
// ID3v2 tag, and the data-chunk extent. It is the preservation-first base for
// rewrites and satisfies core.NativeDoc.
type doc struct {
	chunks  []chunk // every top-level chunk, in file order
	infoIdx int     // index in chunks of the LIST/INFO chunk, or -1
	id3Idx  int     // index in chunks of the "id3 " chunk, or -1
	dataIdx int     // index in chunks of the "data" chunk, or -1

	info []infoItem // decoded INFO items in order (nil if no INFO chunk)
	id3  *id3.Tag   // decoded embedded ID3v2 tag (nil if no id3 chunk)

	dataOff int64 // data chunk body offset (audio essence start)
	dataLen int64 // data chunk body length (audio essence length)
	// dataTruncated records that the data chunk's declared size ran past EOF (and was
	// not the 0xFFFFFFFF "size unknown" streaming sentinel) - a truncated file. It is
	// set where the walk already clamps the overrun, so the overrun is acted on where
	// it is first known rather than reconstructed afterward.
	dataTruncated bool

	// trailingOff/trailingLen capture leftover bytes inside the RIFF chunk after
	// the last well-formed chunk (rare: a corrupt region), preserved verbatim and
	// counted in the RIFF size.
	trailingOff int64
	trailingLen int64
	// outerOff/outerLen capture bytes after the RIFF chunk - data appended outside
	// the declared RIFF size (e.g. a tacked-on ID3v1). Preserved verbatim but kept
	// outside the recomputed RIFF size so a strict reader does not misparse them.
	outerOff int64
	outerLen int64

	fmtCfg fmtChunk
	track  core.AudioTrack
	size   int64
}

func (d *doc) Format() core.Format { return core.FormatWAV }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.chunks = slices.Clone(d.chunks)
	c.info = slices.Clone(d.info)
	if d.id3 != nil {
		c.id3 = d.id3.Clone()
	}
	return &c
}

// Describe summarizes the native chunk structure for the dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	out := make([]core.NativeEntry, 0, len(d.chunks)+len(d.info))
	for i, ch := range d.chunks {
		switch i {
		case d.infoIdx:
			out = append(out, core.NativeEntry{
				Kind: "LIST/INFO", Size: int(ch.bodyLen),
				Note: fmt.Sprintf("%d items", len(d.info)),
			})
			for _, it := range d.info {
				out = append(out, core.NativeEntry{Kind: "  " + it.id4(), Size: len(it.raw)})
			}
		case d.id3Idx:
			note := "0 frames"
			if d.id3 != nil {
				note = fmt.Sprintf("ID3v2.%d, %d frames", d.id3.SrcVersion(), len(d.id3.Frames()))
			}
			out = append(out, core.NativeEntry{Kind: "id3 chunk", Size: int(ch.bodyLen), Note: note})
		case d.dataIdx:
			out = append(out, core.NativeEntry{Kind: "data", Size: int(ch.bodyLen), Note: d.track.Codec})
		default:
			out = append(out, core.NativeEntry{Kind: ch.id4(), Size: int(ch.bodyLen), Note: "preserved"})
		}
	}
	return out
}
