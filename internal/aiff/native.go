package aiff

import (
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// chunk is one top-level IFF chunk (id + source range). Small chunks are
// decoded; SSND and ancillary chunks stay as ranges and are copied on rewrite.
type chunk struct {
	id      [4]byte
	bodyOff int64 // source offset of the body (after the 8-byte chunk header)
	bodyLen int64 // declared body length, excluding any trailing pad byte
	// dupTag: redundant duplicate "ID3 " chunk. First parse wins; dropped on rewrite.
	dupTag bool
	// dupContent: graded against the written set at write time.
	dupContent core.DuplicateContent
}

// id4 returns the chunk identifier as a string.
func (c chunk) id4() string { return string(c.id[:]) }

// textItem is one native text chunk (NAME/AUTH/"(c) "/ANNO): 4CC and raw bytes
// up to the first NUL. Decoded on demand via text() (UTF-8, else Latin-1).
type textItem struct {
	id  [4]byte
	raw []byte
}

func (it textItem) id4() string { return string(it.id[:]) }

// text: UTF-8 when valid, else Latin-1 (legacy Mac-Roman/ASCII).
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

// commChunk: COMM geometry for properties and essence digest. Rate kept decoded
// and as raw 80-bit bytes. compType is AIFF-C compression (zero for AIFF).
// numFrames is numSampleFrames (packet count for packetized types).
type commChunk struct {
	channels   uint16
	numFrames  uint32
	sampleSize uint16
	rateBytes  [10]byte
	sampleRate uint32
	compType   [4]byte
	isAIFC     bool
}

// doc is the AIFF native document (chunks, COMM, text, ID3, SSND extent).
// Preservation-first rewrite base; satisfies [core.NativeDoc].
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
	// ssndAlign: SSND "offset" field (alignment before first frame). audioOff
	// includes it; kept so post-write results match a fresh parse.
	ssndAlign int64
	// ssndTruncated: SSND declared size past EOF (not size-unknown sentinel).
	ssndTruncated bool
	// oversizedChunks: non-audio chunks clamped at EOF.
	oversizedChunks [][4]byte
	// unknownSizeChunks: chunks with 0xFFFFFFFF size (extent = rest of file).
	unknownSizeChunks [][4]byte

	// trailingOff/trailingLen: leftover inside FORM after last chunk (preserved).
	trailingOff int64
	trailingLen int64
	// trailingID3v1: walk stopped on ID3v1 trailer, not corruption.
	trailingID3v1 bool
	// outerOff/outerLen: bytes after FORM (preserved outside recomputed FORM size).
	outerOff int64
	outerLen int64

	comm  commChunk
	track core.AudioTrack
	size  int64
}

func (d *doc) Format() core.Format { return core.FormatAIFF }

// Clone deep-copies so Document accessors stay detached.
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

// Describe summarizes native chunks for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	out := make([]core.NativeEntry, 0, len(d.chunks))
	for i, ch := range d.chunks {
		switch {
		case i == d.id3Idx:
			note := "0 frames"
			var frames []id3.Frame
			if d.id3 != nil {
				frames = d.id3.Frames()
				note = fmt.Sprintf("ID3v2.%d, ", d.id3.SrcVersion()) + id3.FramesNote(d.id3)
			}
			out = append(out, core.NativeEntry{Kind: "ID3 chunk", Size: int(ch.bodyLen), Note: note})
			// List frames like MP3/AAC for technical-description denylist use.
			for _, f := range frames {
				out = append(out, core.NativeEntry{Kind: "  " + f.ID, Size: len(f.Body), Note: id3.FrameNote(f)})
			}
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

// trailingWhat names the in-container trailing region, or "" if unknown.
func (d *doc) trailingWhat() string {
	if d.trailingID3v1 {
		return core.TrailingID3v1What
	}
	return ""
}
