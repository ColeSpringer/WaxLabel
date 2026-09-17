package waxlabel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// AudioDigest is a content identity for audio. Algorithm and versioned
// ExtentVersion travel with Sum so persisted digests stay interpretable:
// refining the extent is an opt-in new version, not a silent break.
type AudioDigest struct {
	Algorithm     string
	ExtentVersion string
	TrackID       int
	Sum           []byte
}

// String renders as "algorithm/extent:hex", or "algorithm/extent#trackID:hex"
// when TrackID is non-zero.
func (d AudioDigest) String() string {
	if d.TrackID != 0 {
		return fmt.Sprintf("%s/%s#%d:%s", d.Algorithm, d.ExtentVersion, d.TrackID, hex.EncodeToString(d.Sum))
	}
	return fmt.Sprintf("%s/%s:%s", d.Algorithm, d.ExtentVersion, hex.EncodeToString(d.Sum))
}

// Equal reports whether two digests have the same algorithm, extent, TrackID, and sum.
func (d AudioDigest) Equal(other AudioDigest) bool {
	return d.Algorithm == other.Algorithm &&
		d.ExtentVersion == other.ExtentVersion &&
		d.TrackID == other.TrackID &&
		bytes.Equal(d.Sum, other.Sum)
}

type hashOptions struct {
	source core.ReaderAtSized
}

// WithHashSource supplies bytes to hash for a detached [Parse] document.
// [ParseFile] and [OpenSource] resolve their source automatically.
func WithHashSource(src ReaderAtSized) HashOption {
	return func(o *hashOptions) { o.source = src }
}

// HashAudioEssence hashes encoded audio packets plus decoder-critical config
// (sample rate, channels, bit depth, FLAC block-size bounds). Independent of
// tags. Container-scoped: the same FLAC frames in .flac and .oga differ.
// Distinct from [Document.HashFile] and from a decoded-PCM hash.
func (d *Document) HashAudioEssence(ctx context.Context, opts ...HashOption) (AudioDigest, error) {
	if err := checkContext(ctx); err != nil {
		return AudioDigest{}, err
	}
	var ho hashOptions
	for _, fn := range opts {
		fn(&ho)
	}
	src, closer, err := d.resolveSource(ho.source, "supply the bytes to hash via WithHashSource")
	if err != nil {
		return AudioDigest{}, err
	}
	defer closer()

	version, cfg := d.essenceExtent()
	ranges := d.media.EssenceRanges()
	// Refuse no-audio files: empty ranges would mint colliding digests, and a
	// non-empty range still flagged WarnNoAudioFrames (e.g. text named .mp3)
	// would hash non-audio. Descending ranges fall through to hashRanges.
	if noEssence(ranges) || hasNoAudioWarning(d.media) {
		return AudioDigest{}, fmt.Errorf("%w: no audio essence to hash", waxerr.ErrInvalidData)
	}
	sum, err := hashRanges(ctx, src, cfg, ranges)
	if err != nil {
		return AudioDigest{}, err
	}
	return AudioDigest{
		Algorithm:     "sha256",
		ExtentVersion: version,
		TrackID:       0,
		Sum:           sum,
	}, nil
}

// essenceExtent returns the codec's versioned extent name and decoder config,
// or a neutral extent with no config if unknown.
func (d *Document) essenceExtent() (version string, config []byte) {
	if codec, ok := core.ForFormat(d.media.Format); ok {
		return codec.EssenceExtent(d.media)
	}
	return "audio-extent-v1", nil
}

// HashFile hashes every byte of the file (tags included).
func (d *Document) HashFile(ctx context.Context, opts ...HashOption) (AudioDigest, error) {
	if err := checkContext(ctx); err != nil {
		return AudioDigest{}, err
	}
	var ho hashOptions
	for _, fn := range opts {
		fn(&ho)
	}
	src, closer, err := d.resolveSource(ho.source, "supply the bytes to hash via WithHashSource")
	if err != nil {
		return AudioDigest{}, err
	}
	defer closer()

	sum, err := hashRanges(ctx, src, nil, [][2]int64{{0, src.Size()}})
	if err != nil {
		return AudioDigest{}, err
	}
	return AudioDigest{Algorithm: "sha256", ExtentVersion: "whole-file-v1", Sum: sum}, nil
}

// noEssence reports whether every range is empty (start == end). Paired with
// [hasNoAudioWarning] for the no-audio gate. Descending ranges are left for
// hashRanges, not treated as empty.
func noEssence(ranges [][2]int64) bool {
	for _, r := range ranges {
		if r[1] != r[0] {
			return false
		}
	}
	return true
}

// hasNoAudioWarning reports whether the parser set WarnNoAudioFrames. Digest,
// VerifyEssence, and Prepare consult it so flagged files are not hashed,
// verified, or rewritten.
func hasNoAudioWarning(media *core.Media) bool {
	for _, w := range media.Warnings {
		if w.Code == core.WarnNoAudioFrames {
			return true
		}
	}
	return false
}

// hashRanges hashes optional prefix bytes then src over each [start,end) range.
// Checks ctx between chunks. src need only support ReadAt.
func hashRanges(ctx context.Context, src io.ReaderAt, prefix []byte, ranges [][2]int64) ([]byte, error) {
	h := sha256.New()
	h.Write(prefix)
	buf := make([]byte, 1<<16)
	prevEnd := int64(-1)
	for _, r := range ranges {
		start, end := r[0], r[1]
		if end < start {
			return nil, fmt.Errorf("%w: audio extent end before start", waxerr.ErrInvalidData)
		}
		// Extents must be ascending and disjoint for a stable digest.
		if start < prevEnd {
			return nil, fmt.Errorf("%w: audio extents overlap or are out of order", waxerr.ErrInvalidData)
		}
		prevEnd = end
		off := start
		for off < end {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n := int64(len(buf))
			if rem := end - off; rem < n {
				n = rem
			}
			if _, err := src.ReadAt(buf[:n], off); err != nil {
				return nil, fmt.Errorf("%w: essence read at %d: %v", waxerr.ErrInvalidData, off, err)
			}
			h.Write(buf[:n])
			off += n
		}
	}
	return h.Sum(nil), nil
}
