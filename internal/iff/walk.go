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
	// FormatID is the chunk describing the audio geometry ("fmt " for WAV, "COMM" for AIFF).
	// A recovered walk must find it and AudioID before its result is trusted.
	FormatID [4]byte
	Noun     string // names the chunk family in cap/error messages ("RIFF chunks" / "IFF chunks")
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
	// UnknownSizeChunks lists chunk ids that declared the 0xFFFFFFFF size-unknown sentinel
	// nothing resolved, so the extent was taken as the rest of the file. It is not
	// truncation (a non-seekable writer emits it legitimately) and so is exempt from both
	// AudioTruncated and OversizedChunks, but it costs the reader every chunk that follows,
	// which is why it is reported at all. An RF64/BW64 size resolved through ds64 is not
	// here: resolving it clears the sentinel reading.
	UnknownSizeChunks [][4]byte
	// TrailingOff/Len capture leftover bytes still inside the container after the last
	// well-formed chunk (a corrupt region, or an ID3v1 trailer a writer miscounted inside
	// the container size): preserved verbatim and counted in the container size.
	TrailingOff, TrailingLen int64
	// TrailingIsID3v1 records that the walk stopped on a recognized ID3v1 trailer rather
	// than on corruption, so a caller reporting the region can say what it is instead of
	// calling a well-formed tag bytes that belong to nothing.
	TrailingIsID3v1 bool
	// OuterOff/Len capture bytes after the container: preserved verbatim but kept outside
	// the recomputed container size.
	OuterOff, OuterLen int64
}

// WalkOptions carries the walk's inputs. They are a struct rather than positional
// parameters because there are enough of them that call sites stop being readable, and
// because SizeOverride is optional - most containers do not have one.
type WalkOptions struct {
	// Size is the whole-file length; End the container boundary (riffEnd/formEnd), which
	// the caller has already clamped to Size.
	Size, End int64
	// Limit caps a single read; MaxElements caps the chunk count.
	Limit       int64
	MaxElements int
	Dialect     Dialect
	// SizeOverride supplies a chunk's real body length when the 32-bit size field cannot
	// carry it - RF64/BW64, where the field reads 0xFFFFFFFF and the true 64-bit size lives
	// in the ds64 chunk. It is called once per chunk in file order with the declared value;
	// returning false leaves that value in force, which is what keeps plain RIFF's reading of
	// 0xFFFFFFFF as the streaming "size unknown" sentinel untouched.
	//
	// It may be stateful (RF64's matches repeated chunk ids to successive table entries in
	// file order), so a given function value serves one walk. Leave it nil, or supply a
	// function that always declines, for a container with no such extension.
	SizeOverride func(id [4]byte, declared uint32) (int64, bool)
	// TrustedEnd suppresses [WalkChunksRecovering]'s retry: End came from a 64-bit size
	// extension (RF64/BW64's ds64) rather than the header's 32-bit field, so it is
	// authoritative and a shorter-than-the-file boundary is a fact, not a stale value.
	TrustedEnd bool
}

// WalkChunksRecovering is [WalkChunks] with one retry when the container boundary is short of
// the file. A header whose declared size predates an appended chunk hides everything past it:
// the tags read as absent, and a rewrite then emits a second tag chunk beside the stranded
// one. The retry walks to Size and is adopted only when it accounts for every byte - the
// format and audio chunks present, no trailing remainder, no clamped chunk - which genuinely
// appended data (an ID3v1 trailer, junk) does not satisfy, so such a file keeps its honest
// trailing-bytes verdict.
//
// distrusted reports that the retry was adopted, so the caller can warn. The retry re-reads
// headers only, and a stateful SizeOverride is not re-entered: TrustedEnd is set exactly for
// the containers that have one.
func WalkChunksRecovering(ctx context.Context, r io.ReaderAt, opts WalkOptions) (res Result, distrusted bool, err error) {
	res, err = WalkChunks(ctx, r, opts)
	if err != nil || opts.TrustedEnd || opts.End >= opts.Size {
		return res, false, err
	}
	wide := opts
	wide.End = opts.Size
	retry, rerr := WalkChunks(ctx, r, wide)
	if rerr != nil || !accountsForWholeFile(retry, opts.Dialect) {
		return res, false, nil
	}
	return retry, true, nil
}

// accountsForWholeFile reports whether a walk tiled the file into well-formed chunks with a
// complete audio description and nothing left over.
func accountsForWholeFile(res Result, d Dialect) bool {
	if res.AudioIdx < 0 || res.TrailingLen > 0 || res.OuterLen > 0 || len(res.OversizedChunks) > 0 {
		return false
	}
	for _, c := range res.Chunks {
		if c.ID == d.FormatID {
			return true
		}
	}
	return false
}

// WalkChunks records every top-level chunk in [12, End) by id and source range, reading
// only chunk headers (never bodies) so a large audio chunk costs nothing. It stops at a
// miscounted trailer after the audio chunk so the trailing-region copy can preserve it
// verbatim, and returns [waxerr.ErrInvalidData] when no chunk is found.
func WalkChunks(ctx context.Context, r io.ReaderAt, opts WalkOptions) (Result, error) {
	size, end, limit, maxElements, d := opts.Size, opts.End, opts.Limit, opts.MaxElements, opts.Dialect
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
		declared := d.Order.Uint32(head[4:8])
		declaredLen := int64(declared)
		// A 64-bit extension supplies the real size where it has one. An overrun of a
		// resolved size is real truncation: testing the resolved value against 0xFFFFFFFF
		// instead would exempt exactly the size range RF64 exists to express.
		if opts.SizeOverride != nil {
			if n, ok := opts.SizeOverride(id, declared); ok {
				if n < 0 {
					break // an override that could not be represented as a length; preserve the rest
				}
				declaredLen = n
			}
		}
		// sizeUnknown is the streaming sentinel: a declared 0xFFFFFFFF in a container with
		// no 64-bit size extension. Such a chunk clamps to the file rather than counting as
		// truncated. The test is on the container, not on this chunk: inside an RF64/BW64 the
		// value is always that extension's marker, so a chunk ds64 does not resolve - one
		// missing from the table, or declaring a size no signed length can hold - keeps its
		// 32-bit size and clamps as any other overrun, which is what rf64.go's sizeFits says
		// it does. Reading it as the streaming sentinel there would exempt a genuinely
		// truncated RF64 from truncated-audio.
		sizeUnknown := opts.SizeOverride == nil && declared == 0xFFFFFFFF
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
		if isID3v1Tail(id, off, end) {
			res.TrailingIsID3v1 = true
			break
		}
		if res.AudioIdx >= 0 && bodyOff+declaredLen > end {
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
			res.AudioTruncated = overran && !sizeUnknown
		} else if overran && !sizeUnknown {
			// Record clamped non-audio chunks so callers can warn. The streaming sentinel
			// means "size unknown", not an overrun.
			res.OversizedChunks = append(res.OversizedChunks, id)
		}
		if sizeUnknown && overran {
			// Exempt from both signals above, but the clamp still swallows everything after
			// this chunk, so the caller gets its own code to report that.
			res.UnknownSizeChunks = append(res.UnknownSizeChunks, id)
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
