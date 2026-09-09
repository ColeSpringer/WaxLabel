package mpeg4audio

// bitReader reads big-endian bit fields from a byte slice. Every read is bounded by what
// remains, so a truncated stream stops the decode instead of reading past the end or
// wrapping around. A read that runs out sets overrun, which stays set: a parser walking a
// long syntax element checks it once at the end rather than at every field.
type bitReader struct {
	b       []byte
	pos     int // index of the next bit
	overrun bool
}

// remaining is the number of unread bits.
func (r *bitReader) remaining() int { return len(r.b)*8 - r.pos }

// position is the index of the next bit, counted from the start of the slice.
func (r *bitReader) position() int { return r.pos }

// read consumes the next n bits, most significant first. ok is false when fewer than n bits
// remain, in which case nothing is consumed. n is at most 24, the widest field either the
// config or a raw data block reads at once.
func (r *bitReader) read(n int) (int, bool) {
	if n <= 0 || n > 24 || r.remaining() < n {
		r.overrun = r.overrun || n > 0
		return 0, false
	}
	v := 0
	for range n {
		v = v<<1 | int(r.b[r.pos>>3]>>(7-r.pos&7)&1)
		r.pos++
	}
	return v, true
}

// bit consumes one bit. ok is false at end of data.
func (r *bitReader) bit() (uint32, bool) {
	if r.remaining() < 1 {
		r.overrun = true
		return 0, false
	}
	v := uint32(r.b[r.pos>>3] >> (7 - r.pos&7) & 1)
	r.pos++
	return v, true
}

// skip advances n bits without decoding them, for a payload whose content the parser does
// not need. It reports false when fewer than n bits remain.
func (r *bitReader) skip(n int) bool {
	if n < 0 || r.remaining() < n {
		r.overrun = true
		return false
	}
	r.pos += n
	return true
}

// alignByte advances to the next byte boundary, the byte_alignment() of the syntax.
func (r *bitReader) alignByte() {
	if rem := r.pos & 7; rem != 0 {
		r.pos += 8 - rem
	}
}

// seek moves to an absolute bit position, for a payload whose declared length the parser
// trusts over the bits it walked. It reports false for a position outside the slice.
func (r *bitReader) seek(bitPos int) bool {
	if bitPos < 0 || bitPos > len(r.b)*8 {
		r.overrun = true
		return false
	}
	r.pos = bitPos
	return true
}
