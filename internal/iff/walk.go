// Package iff walks the top-level chunk structure shared by the RIFF (WAV) and IFF
// (AIFF) container families: a 12-byte header, then a sequence of
// [4-byte id][4-byte size][body][optional pad] chunks. The two families differ only in
// byte order, the audio chunk id, and a noun used in diagnostics; [WalkChunks]
// parameterizes those via [Dialect] so the wav and aiff codecs share one walker.
package iff

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/waxerr"
)

// Chunk is one top-level chunk: its 4-byte id and body range. BodyOff is the source
// offset just past the 8-byte header; BodyLen excludes any trailing word-alignment pad.
type Chunk struct {
	ID      [4]byte
	BodyOff int64
	BodyLen int64
}

// Dialect captures the per-container differences between RIFF (WAV, little-endian) and
// IFF (AIFF, big-endian).
type Dialect struct {
	Order   binary.ByteOrder // chunk-size byte order
	AudioID [4]byte          // the audio chunk id ("data" for WAV, "SSND" for AIFF)
	Noun    string           // names the chunk family in cap/error messages ("RIFF chunks" / "IFF chunks")
}

// Result is the outcome of WalkChunks: the chunk list plus the derived regions a
// preservation-first writer needs.
type Result struct {
	Chunks []Chunk
	// AudioIdx is the index in Chunks of the first audio chunk (Dialect.AudioID), or -1.
	AudioIdx int
	// AudioTruncated records that the audio chunk's declared size ran past EOF and was not
	// the 0xFFFFFFFF "size unknown" streaming sentinel - i.e. a truncated file.
	AudioTruncated bool
	// OversizedChunks lists non-audio chunk ids whose declared body ran past EOF and was
	// clamped. Audio chunk overruns are reported separately via AudioTruncated.
	OversizedChunks [][4]byte
	// TrailingOff/Len capture leftover bytes still inside the container after the last
	// well-formed chunk (a corrupt region, or an ID3v1 trailer a writer miscounted inside
	// the container size): preserved verbatim and counted in the container size.
	TrailingOff, TrailingLen int64
	// OuterOff/Len capture bytes after the container: preserved verbatim but kept outside
	// the recomputed container size.
	OuterOff, OuterLen int64
}

// WalkChunks records every top-level chunk in [12, end) by id and source range, reading
// only chunk headers (never bodies) so a large audio chunk costs nothing. size is the
// whole-file length and end the container boundary (riffEnd/formEnd), which the caller has
// already clamped to size. It stops at a miscounted trailer after the audio chunk so the
// trailing-region copy can preserve it verbatim, and returns [waxerr.ErrInvalidData] when
// no chunk is found.
func WalkChunks(ctx context.Context, r io.ReaderAt, size, end, limit int64, maxElements int, d Dialect) (Result, error) {
	res := Result{AudioIdx: -1}
	off := int64(12)
	// Require the full 8-byte header within the container (off+8 <= end, and end <= size):
	// a partial header straddling the boundary becomes trailing, not a chunk.
	for off+8 <= end {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if err := bits.CheckElementCap(len(res.Chunks), maxElements, d.Noun); err != nil {
			return Result{}, err
		}
		head, err := bits.ReadSlice(r, off, 8, limit)
		if err != nil {
			return Result{}, err
		}
		var id [4]byte
		copy(id[:], head[0:4])
		declaredLen := int64(d.Order.Uint32(head[4:8]))
		bodyOff := off + 8
		// Stop at a miscounted trailer so the trailing-region copy keeps [off, end) verbatim,
		// rather than appending a phantom chunk whose header the writer would rewrite
		// (splitting the marker). Two shapes are caught:
		//   - A contiguous 128-byte ID3v1 "TAG" at the very tail. Its declared-length bytes
		//     can be small (a short or empty title), so it need not overrun; recognized by
		//     shape, it does not depend on an audio chunk having been seen (a malformed
		//     no-audio container can still carry one).
		//   - A pseudo-chunk whose body overruns the container - but only AFTER the audio
		//     chunk, since the audio chunk's own declared size legitimately overruns when the
		//     file is truncated or streaming (AudioIdx is set only past this point, so that
		//     chunk clamps-and-keeps as the last chunk instead of breaking the walk).
		if isID3v1Tail(id, off, end) || (res.AudioIdx >= 0 && bodyOff+declaredLen > end) {
			break
		}
		bodyLen := declaredLen
		// Clamp a declared size that runs past EOF (corrupt or streaming "unknown" size) so
		// the range stays valid; this becomes the last chunk.
		overran := bodyLen > size-bodyOff
		if overran {
			bodyLen = size - bodyOff
		}
		idx := len(res.Chunks)
		res.Chunks = append(res.Chunks, Chunk{ID: id, BodyOff: bodyOff, BodyLen: bodyLen})
		if id == d.AudioID && res.AudioIdx < 0 {
			res.AudioIdx = idx
			// The declared audio size ran past EOF: a truncated file. The 0xFFFFFFFF "size
			// unknown" streaming sentinel also overruns but is not truncation; a 0 size never
			// overruns (it reads as no-audio).
			res.AudioTruncated = overran && declaredLen != 0xFFFFFFFF
		} else if overran && declaredLen != 0xFFFFFFFF {
			// Record clamped non-audio chunks so callers can warn. The streaming sentinel
			// means "size unknown", not an overrun.
			res.OversizedChunks = append(res.OversizedChunks, id)
		}
		next := bodyOff + bodyLen + (bodyLen & 1) // word-alignment pad byte
		if next <= off {
			break // no forward progress (corrupt) - stop and preserve the rest
		}
		off = next
	}
	// Leftover bytes still inside the container: preserved and counted in its size.
	if off < end {
		res.TrailingOff = off
		res.TrailingLen = end - off
	}
	// Bytes after the container: preserved verbatim but kept outside the recomputed size.
	// max(off, end) avoids double-counting a final chunk whose declared body straddled the
	// boundary.
	if outerStart := max(off, end); outerStart < size {
		res.OuterOff = outerStart
		res.OuterLen = size - outerStart
	}
	if len(res.Chunks) == 0 {
		return Result{}, fmt.Errorf("%w: no %s", waxerr.ErrInvalidData, d.Noun)
	}
	return res, nil
}

// isID3v1Tail reports whether a chunk header at off begins a contiguous ID3v1 "TAG"
// trailer occupying the last 128 bytes of the container: the 3-byte "TAG" magic with
// exactly 128 bytes remaining to end. Recognizing it by shape (not by the declared-length
// overrun proxy) catches a tag with a short or empty title, whose declared-length bytes
// are small and would not overrun, so the walk preserves it verbatim instead of shredding
// it into phantom chunks.
//
// The bare "TAG" magic is deliberate here and NOT the strict [id3.LooksLikeID3v1] gate the
// FLAC/MP3 trailing-tag detection uses: this fires only when the RIFF/IFF chunk walk has
// already consumed every declared chunk and exactly 128 bytes remain at a chunk boundary, so
// there is no essence to false-positive against - the container structure, not a sniff, says
// the region is trailing metadata. The strict gate matters only where a tag is inferred by
// probing raw bytes at size-128 with no structural boundary.
func isID3v1Tail(id [4]byte, off, end int64) bool {
	return id[0] == 'T' && id[1] == 'A' && id[2] == 'G' && end-off == 128
}
