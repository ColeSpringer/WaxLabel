package mpeg4audio

// bitReader reads MSB-first bits from a slice. Overrun sticks; check once at element end.
type bitReader struct {
	b       []byte
	pos     int // index of the next bit
	overrun bool
}

// remaining is the number of unread bits.
func (r *bitReader) remaining() int { return len(r.b)*8 - r.pos }

// position is the index of the next bit, counted from the start of the slice.
func (r *bitReader) position() int { return r.pos }

// read consumes up to 24 bits MSB-first. ok=false leaves bits unconsumed.
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

// skip advances n bits; false on overrun.
func (r *bitReader) skip(n int) bool {
	if n < 0 || r.remaining() < n {
		r.overrun = true
		return false
	}
	r.pos += n
	return true
}

// alignByte advances to byte boundary.
func (r *bitReader) alignByte() {
	if rem := r.pos & 7; rem != 0 {
		r.pos += 8 - rem
	}
}

// seek moves to absolute bit position; false if out of range.
func (r *bitReader) seek(bitPos int) bool {
	if bitPos < 0 || bitPos > len(r.b)*8 {
		r.overrun = true
		return false
	}
	r.pos = bitPos
	return true
}
