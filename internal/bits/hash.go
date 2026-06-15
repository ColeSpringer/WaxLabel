package bits

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
)

// SHA256 returns the SHA-256 of the concatenation of chunks. It is used for
// content-addressed identity (picture dedup, audio-essence digests) where a
// stable hash over several fields is needed.
func SHA256(chunks ...[]byte) [32]byte {
	h := sha256.New()
	for _, c := range chunks {
		h.Write(c)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// Hasher accumulates bytes into a SHA-256 and satisfies [Tap], so audio
// essence can be hashed while a rewrite copies it. Build it with the
// decoder-critical config already mixed in (via Mix) before the audio bytes
// flow, so identical packets under different config hash differently. The
// claimed ranges must be ascending and disjoint so observed bytes accumulate
// in source order.
type Hasher struct {
	h      hash.Hash
	ranges [][2]int64
}

// NewHasher returns a Hasher that observes the given source ranges (typically
// the audio frames), ascending and disjoint.
func NewHasher(ranges [][2]int64) *Hasher {
	return &Hasher{h: sha256.New(), ranges: ranges}
}

// Mix folds fixed configuration bytes into the hash before any tapped audio.
func (h *Hasher) Mix(p []byte) { h.h.Write(p) }

// MixUint64 folds a value into the hash (big-endian) for compact config.
func (h *Hasher) MixUint64(v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	h.h.Write(b[:])
}

// Observe folds the portion of p that falls within a claimed range into the
// hash, byte-precisely (a chunk wider than the claimed range contributes only
// its overlapping bytes).
func (h *Hasher) Observe(srcOff int64, p []byte) {
	end := srcOff + int64(len(p))
	for _, r := range h.ranges {
		lo := max(srcOff, r[0])
		hi := min(end, r[1])
		if lo < hi {
			h.h.Write(p[lo-srcOff : hi-srcOff])
		}
	}
}

// Sum returns the accumulated digest.
func (h *Hasher) Sum() []byte { return h.h.Sum(nil) }
