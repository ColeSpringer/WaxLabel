package waxlabel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// AudioDigest is a content identity for audio. Algorithm and the named,
// versioned ExtentVersion travel with the Sum so a persisted dedup hash stays
// interpretable library-wide: refining the extent definition is an opt-in new
// version, not a silent change that invalidates old hashes.
type AudioDigest struct {
	Algorithm     string
	ExtentVersion string
	TrackID       int
	Sum           []byte
}

// String renders the digest as "algorithm/extent:hex".
func (d AudioDigest) String() string {
	return fmt.Sprintf("%s/%s:%s", d.Algorithm, d.ExtentVersion, hex.EncodeToString(d.Sum))
}

// Equal reports whether two digests are comparable (same algorithm and extent)
// and equal.
func (d AudioDigest) Equal(other AudioDigest) bool {
	return d.Algorithm == other.Algorithm &&
		d.ExtentVersion == other.ExtentVersion &&
		bytes.Equal(d.Sum, other.Sum)
}

type hashOptions struct {
	source core.ReaderAtSized
}

// WithHashSource supplies the source bytes to hash for a detached document
// (one from [Parse]). Documents from [ParseFile] or [OpenSource] resolve their
// source automatically.
func WithHashSource(src ReaderAtSized) HashOption {
	return func(o *hashOptions) { o.source = src }
}

// HashAudioEssence computes the encoded-essence identity: a hash over the
// audio packets plus the decoder-critical configuration (sample rate, channel
// count, bit depth, and FLAC block-size bounds). Mixing the config means two
// files with byte-identical packets but different channel mapping are correctly
// distinct. This answers "is this the same audio?", independent of tags.
//
// It is distinct from whole-file identity ([Document.HashFile]) and from a
// decoded-PCM hash (which needs a decoder and is test-only).
func (d *Document) HashAudioEssence(ctx context.Context, opts ...HashOption) (AudioDigest, error) {
	if err := ctx.Err(); err != nil {
		return AudioDigest{}, err
	}
	var ho hashOptions
	for _, fn := range opts {
		fn(&ho)
	}
	src, closer, err := d.resolveSource(ho.source)
	if err != nil {
		return AudioDigest{}, err
	}
	defer closer()

	version, cfg := d.essenceExtent()
	sum, err := hashRange(ctx, src, cfg, d.media.AudioStart, d.media.AudioEnd, bits.DefaultLimits.MaxAllocBytes)
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

// essenceExtent fetches the codec's versioned extent name and decoder-critical
// configuration. If the format has no registered codec it falls back to a
// neutral extent with no config.
func (d *Document) essenceExtent() (version string, config []byte) {
	if codec, ok := core.ForFormat(d.media.Format); ok {
		return codec.EssenceExtent(d.media)
	}
	return "audio-extent-v1", nil
}

// HashFile computes the whole-file identity (a hash of every byte). This is the
// strictest level: it changes whenever any byte, including tags, changes.
func (d *Document) HashFile(ctx context.Context, opts ...HashOption) (AudioDigest, error) {
	if err := ctx.Err(); err != nil {
		return AudioDigest{}, err
	}
	var ho hashOptions
	for _, fn := range opts {
		fn(&ho)
	}
	src, closer, err := d.resolveSource(ho.source)
	if err != nil {
		return AudioDigest{}, err
	}
	defer closer()

	sum, err := hashRange(ctx, src, nil, 0, src.Size(), bits.DefaultLimits.MaxAllocBytes)
	if err != nil {
		return AudioDigest{}, err
	}
	return AudioDigest{Algorithm: "sha256", ExtentVersion: "whole-file-v1", Sum: sum}, nil
}

// hashRange hashes optional prefix bytes followed by src[start:end). It checks
// ctx between chunks so hashing a large extent can be cancelled. src need only
// support ReadAt (the written-output handle has no Size).
func hashRange(ctx context.Context, src io.ReaderAt, prefix []byte, start, end, limit int64) ([]byte, error) {
	if end < start {
		return nil, fmt.Errorf("%w: audio extent end before start", waxerr.ErrInvalidData)
	}
	h := sha256.New()
	h.Write(prefix)
	buf := make([]byte, 1<<16)
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
	return h.Sum(nil), nil
}
