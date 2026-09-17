// Package iff walks RIFF (WAV) and IFF (AIFF) top-level chunks via [Dialect].
package iff

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/waxerr"
)

// Chunk is one top-level chunk. BodyOff is past the 8-byte header; BodyLen excludes pad.
type Chunk struct {
	ID      [4]byte
	BodyOff int64
	BodyLen int64
}

// Dialect is RIFF vs IFF byte order and chunk ids.
type Dialect struct {
	Order   binary.ByteOrder // chunk-size byte order
	AudioID [4]byte          // the audio chunk id ("data" for WAV, "SSND" for AIFF)
	// FormatID is "fmt " or "COMM"; recovery requires it plus AudioID.
	FormatID [4]byte
	Noun     string // "RIFF chunks" / "IFF chunks" in errors
}

// Result is WalkChunks output for preservation-first rewrite.
type Result struct {
	Chunks []Chunk
	// AudioIdx is the index in Chunks of the first audio chunk (Dialect.AudioID), or -1.
	AudioIdx int
	// AudioTruncated: audio declared size past EOF, not 0xFFFFFFFF sentinel.
	AudioTruncated bool
	// OversizedChunks: non-audio chunks clamped at EOF (not sentinel).
	OversizedChunks [][4]byte
	// UnknownSizeChunks: 0xFFFFFFFF with no SizeOverride; not truncation but swallows rest.
	UnknownSizeChunks [][4]byte
	// TrailingOff/Len: bytes inside container after last chunk (corrupt or ID3v1 tail).
	TrailingOff, TrailingLen int64
	// TrailingIsID3v1: trailing region is ID3v1, not corruption.
	TrailingIsID3v1 bool
	// OuterOff/Len: bytes after container boundary, outside recomputed size.
	OuterOff, OuterLen int64
}

// WalkOptions configures a chunk walk.
type WalkOptions struct {
	// Size is file length; End is container boundary (<= Size).
	Size, End int64
	// Limit caps a single read; MaxElements caps the chunk count.
	Limit       int64
	MaxElements int
	Dialect     Dialect
	// SizeOverride resolves RF64/BW64 ds64 sizes. nil keeps 0xFFFFFFFF as streaming sentinel.
	// May be stateful; one function per walk.
	SizeOverride func(id [4]byte, declared uint32) (int64, bool)
	// TrustedEnd: End from ds64; skip recovery retry.
	TrustedEnd bool
}

// WalkChunksRecovering retries to file Size when End < Size. Adopted only if the wide walk
// accounts for the whole file (format+audio, no trailing/clamps). distrusted=true on adopt.
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

// accountsForWholeFile reports a complete chunk tiling with format chunk and no remainder.
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

// WalkChunks lists top-level chunks in [12, End); headers only. Stops at ID3v1/overrun tail.
func WalkChunks(ctx context.Context, r io.ReaderAt, opts WalkOptions) (Result, error) {
	size, end, limit, maxElements, d := opts.Size, opts.End, opts.Limit, opts.MaxElements, opts.Dialect
	res := Result{AudioIdx: -1}
	off := int64(12)
	// Partial header at boundary becomes trailing, not a chunk.
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
		// Resolved RF64 size overrun is truncation, not sentinel.
		if opts.SizeOverride != nil {
			if n, ok := opts.SizeOverride(id, declared); ok {
				if n < 0 {
					break // unrepresentable override; preserve rest
				}
				declaredLen = n
			}
		}
		// Streaming sentinel: 0xFFFFFFFF without SizeOverride. RF64 uses override instead;
		// unresolved RF64 chunks clamp like normal overruns.
		sizeUnknown := opts.SizeOverride == nil && declared == 0xFFFFFFFF
		bodyOff := off + 8
		// Stop before phantom chunks: ID3v1 tail (128 B "TAG" at end) or post-audio overrun.
		// Pre-audio overrun clamps; audio chunk overrun is the last chunk.
		if isID3v1Tail(id, off, end) {
			res.TrailingIsID3v1 = true
			break
		}
		if res.AudioIdx >= 0 && bodyOff+declaredLen > end {
			break
		}
		bodyLen := declaredLen
		// Clamp overrun to EOF; last chunk.
		overran := bodyLen > size-bodyOff
		if overran {
			bodyLen = size - bodyOff
		}
		idx := len(res.Chunks)
		res.Chunks = append(res.Chunks, Chunk{ID: id, BodyOff: bodyOff, BodyLen: bodyLen})
		if id == d.AudioID && res.AudioIdx < 0 {
			res.AudioIdx = idx
			// Truncated audio unless streaming sentinel.
			res.AudioTruncated = overran && !sizeUnknown
		} else if overran && !sizeUnknown {
			// Clamped non-audio; sentinel exempt.
			res.OversizedChunks = append(res.OversizedChunks, id)
		}
		if sizeUnknown && overran {
			// Sentinel: exempt from truncation/clamp signals.
			res.UnknownSizeChunks = append(res.UnknownSizeChunks, id)
		}
		next := bodyOff + bodyLen + (bodyLen & 1)
		if next <= off {
			break // no forward progress
		}
		off = next
	}
	// Trailing bytes inside container.
	if off < end {
		res.TrailingOff = off
		res.TrailingLen = end - off
	}
	// Outer bytes after container. max(off,end) avoids double-count at boundary.
	if outerStart := max(off, end); outerStart < size {
		res.OuterOff = outerStart
		res.OuterLen = size - outerStart
	}
	if len(res.Chunks) == 0 {
		return Result{}, fmt.Errorf("%w: no %s", waxerr.ErrInvalidData, d.Noun)
	}
	return res, nil
}

// isID3v1Tail: "TAG" chunk header with exactly 128 bytes to container end.
// Shape-based (short title need not overrun). Bare "TAG" magic, not [id3.LooksLikeID3v1]:
// chunk walk already consumed declared chunks; structure bounds false positives.
func isID3v1Tail(id [4]byte, off, end int64) bool {
	return id[0] == 'T' && id[1] == 'A' && id[2] == 'G' && end-off == 128
}
