package bits

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
)

// SHA256 hashes the concatenation of chunks.
func SHA256(chunks ...[]byte) [32]byte {
	h := sha256.New()
	for _, c := range chunks {
		h.Write(c)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// Hasher is a [Tap] that SHA-256-hashes claimed source ranges. Mix config before audio.
// Ranges must be ascending and disjoint.
type Hasher struct {
	h      hash.Hash
	ranges [][2]int64
}

// NewHasher observes the given ascending, disjoint ranges.
func NewHasher(ranges [][2]int64) *Hasher {
	return &Hasher{h: sha256.New(), ranges: ranges}
}

// Mix folds config bytes into the hash.
func (h *Hasher) Mix(p []byte) { h.h.Write(p) }

// MixUint64 folds v into the hash (big-endian).
func (h *Hasher) MixUint64(v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	h.h.Write(b[:])
}

// Observe hashes the overlap of p with claimed ranges.
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
