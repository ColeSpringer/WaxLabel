package mp4

import (
	"bytes"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
)

// TestAssembleSameOffsetInsertBeforeReplaceDeterministic checks that when a zero-width insert and a
// same-offset replace share an offset (a combined tag+chapter edit where an insert lands exactly at
// a replaced atom's start), assemble orders the insert first regardless of input order. The oldLen
// tie-break makes this deterministic; a plain unstable off-only sort could emit the replace first,
// advancing pos past the insert's offset and tripping assemble's own overlap guard. Both input
// orders must therefore succeed and produce byte-identical segment lists.
func TestAssembleSameOffsetInsertBeforeReplaceDeterministic(t *testing.T) {
	const size = 100
	insert := edit{off: 10, oldLen: 0, lit: []byte{0xAA}}        // zero-width insert at offset 10
	replace := edit{off: 10, oldLen: 4, lit: []byte{0xBB, 0xCC}} // replace 4 source bytes at offset 10

	forward, err := assemble([]edit{insert, replace}, size)
	if err != nil {
		t.Fatalf("assemble([insert, replace]) errored: %v", err)
	}
	reverse, err := assemble([]edit{replace, insert}, size)
	if err != nil {
		// Without the tie-break this is exactly the failure mode: the replace sorts first, pos
		// advances to 14, and the insert at 10 trips the off < pos overlap guard.
		t.Fatalf("assemble([replace, insert]) errored: %v (input order must not matter)", err)
	}

	if !segmentsEqual(forward, reverse) {
		t.Fatalf("assemble ordering depends on input order:\n [insert,replace] = %v\n [replace,insert] = %v", forward, reverse)
	}

	// The insert's literal must precede the replace's literal in the output.
	insertIdx, replaceIdx := -1, -1
	for i, s := range forward {
		switch {
		case bytes.Equal(s.Literal, insert.lit):
			insertIdx = i
		case bytes.Equal(s.Literal, replace.lit):
			replaceIdx = i
		}
	}
	if insertIdx < 0 || replaceIdx < 0 {
		t.Fatalf("both literals should appear in the output; got %v", forward)
	}
	if insertIdx > replaceIdx {
		t.Errorf("insert literal at %d, replace at %d: the zero-width insert must sort first", insertIdx, replaceIdx)
	}
}

// TestAssembleSameOffsetZeroWidthInsertsStable checks that two zero-width inserts at the same offset
// stay in input order. They share both offset and width, so the oldLen tie-break cannot order them;
// SliceStable keeps input order and makes the output bytes reproducible, where plain sort.Slice
// would order them by luck. The codec does not generate such a pair today, so this pins the
// defensive guarantee.
func TestAssembleSameOffsetZeroWidthInsertsStable(t *testing.T) {
	const size = 100
	a := edit{off: 10, oldLen: 0, lit: []byte{0xA1}}
	b := edit{off: 10, oldLen: 0, lit: []byte{0xB2}}
	segs, err := assemble([]edit{a, b}, size)
	if err != nil {
		t.Fatalf("assemble errored: %v", err)
	}
	ai, bi := -1, -1
	for i, s := range segs {
		switch {
		case bytes.Equal(s.Literal, a.lit):
			ai = i
		case bytes.Equal(s.Literal, b.lit):
			bi = i
		}
	}
	if ai < 0 || bi < 0 || ai > bi {
		t.Errorf("zero-width inserts not emitted in input order: a at %d, b at %d; segs=%v", ai, bi, segs)
	}
}

// segmentsEqual compares two segment lists field-by-field (Literal bytes, Off, Len).
func segmentsEqual(a, b []bits.Segment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i].Literal, b[i].Literal) || a[i].Off != b[i].Off || a[i].Len != b[i].Len {
			return false
		}
	}
	return true
}
