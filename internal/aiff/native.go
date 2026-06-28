package aiff

import (
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// chunk records one top-level IFF chunk by identifier and source byte range.
// Only small chunks (COMM, the native text chunks, ID3) have their bodies
// decoded into the doc; the SSND sound chunk and every ancillary chunk are kept
// here only as ranges and copied verbatim on rewrite, so a multi-megabyte sound
// chunk is never read into memory.
type chunk struct {
	id      [4]byte
	bodyOff int64 // source offset of the body (after the 8-byte chunk header)
	bodyLen int64 // declared body length, excluding any trailing pad byte
	// dupTag marks a redundant duplicate "ID3 " chunk. Only the first that parses
	// is authoritative; duplicates are preserved verbatim on a no-op but dropped
	// when the file is rewritten.
	dupTag bool
}

// id4 returns the chunk identifier as a string.
func (c chunk) id4() string { return string(c.id[:]) }

// textItem is one decoded native text chunk (NAME/AUTH/"(c) "/ANNO): its 4CC and
// the raw character bytes (the run up to the first NUL, as stored). Keeping the
// raw bytes - rather than a decoded string - lets the value decode on demand
// under the AIFF/UTF-8 fallback in text(), the same approach the wav codec uses
// for INFO items. The live chunk-index data lives in doc.textIdx.
type textItem struct {
	id  [4]byte
	raw []byte
}

func (it textItem) id4() string { return string(it.id[:]) }

// text decodes the value bytes for projection: UTF-8 when valid (what the ffmpeg
// family and modern taggers write), else Latin-1 (a reasonable fallback for the
// historical Mac-Roman/ASCII text these chunks held), so a legacy high byte is a
// valid rune in the canonical model rather than an invalid-UTF-8 string.
func (it textItem) text() string {
	if utf8.Valid(it.raw) {
		return string(it.raw)
	}
	r := make([]rune, len(it.raw))
	for i, c := range it.raw {
		r[i] = rune(c) // Latin-1: each byte is its own code point
	}
	return string(r)
}

// commChunk is the decoded "COMM" common chunk: the decoder-critical geometry
// used for properties and the essence-digest configuration. The sample rate is
// kept both decoded (for properties) and as its raw 80-bit bytes (for an exact
// essence config). compType is the AIFF-C compression type (zero for plain AIFF).
type commChunk struct {
	channels   uint16
	numFrames  uint32
	sampleSize uint16
	rateBytes  [10]byte
	sampleRate uint32
	compType   [4]byte
	isAIFC     bool
}

// doc is the AIFF native document: every top-level chunk in order (with source
// ranges), the decoded COMM geometry, the decoded native text chunks and
// embedded ID3v2 tag, and the SSND sound-frame extent. It is the
// preservation-first base for rewrites and satisfies core.NativeDoc.
type doc struct {
	chunks   []chunk // every top-level chunk, in file order
	formType [4]byte // "AIFF" or "AIFC", preserved across a rewrite

	commIdx int   // index in chunks of the COMM chunk, or -1
	ssndIdx int   // index in chunks of the SSND chunk, or -1
	id3Idx  int   // index in chunks of the authoritative "ID3 " chunk, or -1
	textIdx []int // indices in chunks of the native text chunks, in file order

	texts []textItem // decoded native text chunks in order (nil if none)
	id3   *id3.Tag   // decoded embedded ID3v2 tag (nil if no ID3 chunk)

	audioOff int64 // SSND sample-frame start (audio essence start)
	audioEnd int64 // SSND body end (audio essence end)
	// ssndAlign is the SSND "offset" field: block-alignment bytes before the first
	// sample frame. audioOff already includes it; the value is retained so post-write
	// result construction can match a fresh parse of the copied SSND.
	ssndAlign int64
	// ssndTruncated records that the SSND chunk's declared size ran past EOF (and was
	// not the 0xFFFFFFFF "size unknown" sentinel) - a truncated file. It is set where
	// the walk already clamps the overrun, so the overrun is acted on where it is
	// first known rather than reconstructed afterward.
	ssndTruncated bool
	// oversizedChunks holds non-audio chunk ids whose declared body ran past EOF and was
	// clamped, so the parser can surface a warning.
	oversizedChunks [][4]byte

	// trailingOff/trailingLen capture leftover bytes inside the FORM chunk after
	// the last well-formed chunk (rare: a corrupt region), preserved verbatim and
	// counted in the FORM size.
	trailingOff int64
	trailingLen int64
	// outerOff/outerLen capture bytes after the FORM chunk - data appended outside
	// the declared FORM size (e.g. a tacked-on tag). Preserved verbatim but kept
	// outside the recomputed FORM size so a strict reader does not misparse them.
	outerOff int64
	outerLen int64

	comm  commChunk
	track core.AudioTrack
	size  int64
}

func (d *doc) Format() core.Format { return core.FormatAIFF }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.chunks = slices.Clone(d.chunks)
	c.textIdx = slices.Clone(d.textIdx)
	c.texts = slices.Clone(d.texts)
	if d.id3 != nil {
		c.id3 = d.id3.Clone()
	}
	return &c
}

// Describe summarizes the native chunk structure for the dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	out := make([]core.NativeEntry, 0, len(d.chunks))
	for i, ch := range d.chunks {
		switch {
		case i == d.id3Idx:
			note := "0 frames"
			if d.id3 != nil {
				note = fmt.Sprintf("ID3v2.%d, %d frames", d.id3.SrcVersion(), len(d.id3.Frames()))
			}
			out = append(out, core.NativeEntry{Kind: "ID3 chunk", Size: int(ch.bodyLen), Note: note})
		case i == d.commIdx:
			out = append(out, core.NativeEntry{Kind: "COMM", Size: int(ch.bodyLen), Note: d.track.Codec})
		case i == d.ssndIdx:
			out = append(out, core.NativeEntry{Kind: "SSND", Size: int(ch.bodyLen), Note: "sound data"})
		case slices.Contains(d.textIdx, i):
			out = append(out, core.NativeEntry{Kind: ch.id4(), Size: int(ch.bodyLen), Note: "text"})
		default:
			out = append(out, core.NativeEntry{Kind: ch.id4(), Size: int(ch.bodyLen), Note: "preserved"})
		}
	}
	return out
}
