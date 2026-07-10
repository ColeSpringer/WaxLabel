package matroska

import (
	"bytes"
	"testing"
)

// nestedSimpleTags builds one top-level SimpleTag wrapping `wraps` levels of nested
// SimpleTags, the innermost carrying a payload-byte TagString. The returned bytes are
// the top-level SimpleTag element, ready to drop inside a Tag.
func nestedSimpleTags(wraps, payload int) []byte {
	node := encElement(idSimpleTag, cat(
		stringElement(idTagName, "LEAF"),
		encElement(idTagString, bytes.Repeat([]byte{'x'}, payload)),
	))
	for i := 0; i < wraps; i++ {
		node = encElement(idSimpleTag, cat(stringElement(idTagName, "N"), node))
	}
	return node
}

// sumRawBytes totals len(raw) across the whole tag tree: each group's own bytes and
// Targets bytes, plus every SimpleTag's raw at any nesting depth. nilNestedRaw counts
// how many nested (non-top-level) SimpleTags still carry raw, which must be zero.
func sumRawBytes(d *doc) (total, nilNestedRaw int) {
	var walkSub func(st simpleTag)
	walkSub = func(st simpleTag) {
		for _, s := range st.sub {
			total += len(s.raw)
			if s.raw != nil {
				nilNestedRaw++
			}
			walkSub(s)
		}
	}
	for _, g := range d.groups {
		total += len(g.raw) + len(g.targetsRaw)
		for _, st := range g.tags {
			total += len(st.raw)
			walkSub(st)
		}
	}
	return total, nilNestedRaw
}

// TestNestedSimpleTagAllocationBounded is the CI guard for the deep-nested SimpleTag
// memory blowup: capturing raw at every recursion level retained roughly depth times
// the subtree size. Capturing only the top-level tag's raw (which already spans the
// whole nested subtree) keeps retained bytes at a small constant multiple of the file
// size regardless of nesting depth.
func TestNestedSimpleTagAllocationBounded(t *testing.T) {
	const (
		wraps   = 50        // nesting levels; stays within the 64-level depth budget
		payload = 200 << 10 // a multi-KiB leaf so per-level framing is negligible
	)
	top := nestedSimpleTags(wraps, payload)
	tags := encElement(idTags, encElement(idTag, cat(encElement(idTargets, uintElement(idTgtTypeVal, 50)), top)))
	src := segBytes(cat(mkInfo("Title"), tags, emptyCluster()))

	d := parseMKA(t, src).Native.(*doc)
	total, nestedRaw := sumRawBytes(d)

	// The retained raw bytes must stay within a small constant multiple of the file
	// size (group raw plus one top-level tag raw, each spanning the nested subtree
	// once). Before the fix this grew with nesting depth (~wraps times the subtree).
	const bound = 4
	if int64(total) > int64(bound)*int64(len(src)) {
		t.Errorf("retained raw = %d bytes for a %d-byte file (%.1fx); want <= %dx (nesting is amplifying retention)",
			total, len(src), float64(total)/float64(len(src)), bound)
	}
	// Every nested SimpleTag must leave raw nil; only the top-level tag carries it.
	if nestedRaw != 0 {
		t.Errorf("%d nested SimpleTags still carry raw bytes; want 0 (only the top-level tag keeps raw)", nestedRaw)
	}

	// The top-level tag's raw must still be present and span the whole nested subtree,
	// so the write path preserves the structure verbatim.
	if len(d.groups) != 1 || len(d.groups[0].tags) != 1 {
		t.Fatalf("parsed %d groups; want 1 group with 1 top-level tag", len(d.groups))
	}
	if topRaw := d.groups[0].tags[0].raw; !bytes.Contains(topRaw, bytes.Repeat([]byte{'x'}, payload)) {
		t.Errorf("top-level tag raw (%d bytes) does not span the nested leaf payload", len(topRaw))
	}
}
