package mpeg4audio

import (
	"fmt"
	"testing"
)

// allBooks names every generated codebook with the entry count the specification states for
// it, so a table that came out short or long fails here rather than decoding garbage.
func allBooks() map[string]struct {
	book huffBook
	want int
} {
	m := map[string]struct {
		book huffBook
		want int
	}{
		"scalefactor":    {scalefactorBook, 121},
		"sbrTEnv15":      {sbrTEnv15, 121},
		"sbrFEnv15":      {sbrFEnv15, 121},
		"sbrTEnvBal15":   {sbrTEnvBal15, 49},
		"sbrFEnvBal15":   {sbrFEnvBal15, 49},
		"sbrTEnv30":      {sbrTEnv30, 63},
		"sbrFEnv30":      {sbrFEnv30, 63},
		"sbrTEnvBal30":   {sbrTEnvBal30, 25},
		"sbrFEnvBal30":   {sbrFEnvBal30, 25},
		"sbrTNoise30":    {sbrTNoise30, 63},
		"sbrTNoiseBal30": {sbrTNoiseBal30, 25},
	}
	sizes := [12]int{1: 81, 2: 81, 3: 81, 4: 81, 5: 81, 6: 81, 7: 64, 8: 64, 9: 169, 10: 169, 11: 289}
	for cb := 1; cb <= 11; cb++ {
		m[fmt.Sprintf("spectrum%d", cb)] = struct {
			book huffBook
			want int
		}{spectrumBooks[cb], sizes[cb]}
	}
	return m
}

// TestTableSizes checks each generated codebook against the entry count the specification
// prints for it. A transcription error is a generator bug: fix the generator, never the
// table by hand.
func TestTableSizes(t *testing.T) {
	for name, b := range allBooks() {
		if len(b.book) != b.want {
			t.Errorf("%s has %d entries, want %d", name, len(b.book), b.want)
		}
	}
}

// TestHuffmanBooksComplete checks the Kraft equality: a complete binary prefix code
// satisfies sum(2^-len) == 1 exactly. Computed in integers at a 32-bit scale, so no
// floating point rounding can hide a missing or duplicated codeword.
func TestHuffmanBooksComplete(t *testing.T) {
	for name, b := range allBooks() {
		var sum uint64
		for _, c := range b.book {
			if c.Len < 1 || c.Len > 32 {
				t.Fatalf("%s: codeword length %d out of range", name, c.Len)
			}
			sum += 1 << (32 - c.Len)
		}
		if sum != 1<<32 {
			t.Errorf("%s: Kraft sum is %d/%d, want exactly 1 (the book is incomplete or over-full)", name, sum, uint64(1)<<32)
		}
	}
}

// TestHuffmanBooksPrefixFree checks that no codeword is a prefix of a longer one, which is
// what lets the bit-by-bit decoder stop at the first match.
func TestHuffmanBooksPrefixFree(t *testing.T) {
	for name, b := range allBooks() {
		for i, a := range b.book {
			for j, c := range b.book {
				if i == j || a.Len > c.Len {
					continue
				}
				if a.Code == c.Code>>(c.Len-a.Len) {
					t.Errorf("%s: symbol %d (%d bits, %#x) is a prefix of symbol %d (%d bits, %#x)", name, i, a.Len, a.Code, j, c.Len, c.Code)
				}
			}
		}
	}
}

// TestSWBOffsetTables checks the scalefactor band offsets: strictly increasing, the band
// count the specification states per sampling-frequency index, a terminator at the window
// length, and index 12 (7350 Hz) sharing index 11's layout.
func TestSWBOffsetTables(t *testing.T) {
	longCounts := [13]int{41, 41, 47, 49, 49, 51, 47, 47, 43, 43, 43, 40, 40}
	shortCounts := [13]int{12, 12, 12, 14, 14, 14, 15, 15, 15, 15, 15, 15, 15}
	for i := range swbOffsetLong {
		checkOffsets(t, fmt.Sprintf("swbOffsetLong[%d]", i), swbOffsetLong[i], longCounts[i], 1024)
		checkOffsets(t, fmt.Sprintf("swbOffsetShort[%d]", i), swbOffsetShort[i], shortCounts[i], 128)
	}
	for _, pair := range [][2][]uint16{{swbOffsetLong[12], swbOffsetLong[11]}, {swbOffsetShort[12], swbOffsetShort[11]}} {
		if len(pair[0]) != len(pair[1]) {
			t.Fatalf("index 12 must share index 11's layout")
		}
		for i := range pair[0] {
			if pair[0][i] != pair[1][i] {
				t.Errorf("index 12 differs from index 11 at band %d", i)
			}
		}
	}
}

func checkOffsets(t *testing.T, name string, offsets []uint16, numSwb int, window uint16) {
	t.Helper()
	if len(offsets) != numSwb+1 {
		t.Errorf("%s has %d entries, want %d bands plus the window length", name, len(offsets), numSwb)
		return
	}
	if offsets[0] != 0 {
		t.Errorf("%s starts at %d, want 0", name, offsets[0])
	}
	if offsets[numSwb] != window {
		t.Errorf("%s ends at %d, want the window length %d", name, offsets[numSwb], window)
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] <= offsets[i-1] {
			t.Errorf("%s: offset %d (%d) is not above %d", name, i, offsets[i], offsets[i-1])
		}
	}
}
