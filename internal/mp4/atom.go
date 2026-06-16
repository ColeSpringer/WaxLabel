package mp4

import (
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// An MP4 file is a tree of atoms (a.k.a. boxes). Each atom is
//
//	[4-byte big-endian size][4-byte type][payload]
//
// where size counts the whole atom including the 8-byte header. A size of 1
// means a 64-bit size follows the type (header is then 16 bytes); a size of 0
// means the atom runs to end-of-file (only valid at the top level). The "meta"
// atom is a FullBox: a 4-byte version/flags field precedes its children.

// metaSkip is the version/flags prefix inside a "meta" atom before its children.
const metaSkip = 4

// containerAtoms are the atoms this codec descends into: the path to the iTunes
// tag list (moov.udta.meta.ilst) and to the chunk-offset tables
// (moov.trak.mdia.minf.stbl). Everything else (including each ilst item's "data"
// children, which are decoded separately) is treated as a leaf.
var containerAtoms = map[[4]byte]bool{
	atomName("moov"): true,
	atomName("trak"): true,
	atomName("mdia"): true,
	atomName("minf"): true,
	atomName("stbl"): true,
	atomName("udta"): true,
	atomName("meta"): true,
	atomName("ilst"): true,
}

// atomName returns the 4-byte atom identifier for a 4-character string.
func atomName(s string) [4]byte {
	var n [4]byte
	copy(n[:], s)
	return n
}

// node is one atom in the parsed tree: its identifier and source byte range, the
// header length (8 or 16), and, for container atoms, its child atoms.
type node struct {
	name      [4]byte
	offset    int64 // atom start in the source
	headerLen int64 // 8, or 16 for a 64-bit size
	size      int64 // total atom length including the header
	children  []node
}

func (n node) id() string        { return string(n.name[:]) }
func (n node) payloadOff() int64 { return n.offset + n.headerLen }
func (n node) end() int64        { return n.offset + n.size }

// childStart returns where a container atom's children begin: the payload start,
// plus the 4-byte FullBox version/flags prefix when the atom is a "meta" box in
// its ISO/iTunes form. QuickTime authors a bare "meta" with no version/flags, so
// the prefix is detected rather than assumed (assuming it would misalign child
// parsing and silently read the file as untagged): a FullBox meta's version/flags
// are 00 00 00 00, whereas a bare meta begins with its first child's non-zero
// size.
func childStart(src core.ReaderAtSized, n node, limit int64) int64 {
	po := n.payloadOff()
	if n.name != atomName("meta") || po+metaSkip > n.end() {
		return po
	}
	b, err := bits.ReadSlice(src, po, metaSkip, limit)
	if err != nil || b[0]|b[1]|b[2]|b[3] != 0 {
		return po // bare QuickTime meta (or unreadable): children start immediately
	}
	return po + metaSkip
}

// walkAtoms parses the atoms in [start, end) of src into a node tree, recursing
// into container atoms up to the depth guard. It reads only atom headers (never
// payloads), so a large mdat costs nothing. topLevel marks the outermost call:
// only there is an atom whose declared size overruns the region tolerated (a
// truncated final atom, e.g. a half-downloaded mdat, so the complete earlier
// metadata still reads); a *nested* atom that overruns its parent is structural
// corruption and is rejected, because clamping it leaves the recorded size
// inconsistent with the preserved source bytes — which would make an edit's
// rewrite emit un-reparseable output (the inserted tag path would fall inside the
// clamped-but-still-oversized child's declared extent).
func walkAtoms(src core.ReaderAtSized, start, end int64, depth *bits.Depth, limit int64, topLevel bool) ([]node, error) {
	if err := depth.Enter(); err != nil {
		return nil, err
	}
	defer depth.Leave()

	var out []node
	off := start
	for off+8 <= end {
		head, err := bits.ReadSlice(src, off, 8, limit)
		if err != nil {
			return nil, err
		}
		var name [4]byte
		copy(name[:], head[4:8])
		size := int64(binary.BigEndian.Uint32(head[0:4]))
		headerLen := int64(8)
		switch {
		case size == 1:
			// A 64-bit atom needs a 16-byte header; reject one that does not fit the
			// region rather than reading its extended size from outside it (which would
			// also let the size clamp below fall under headerLen, making payload lengths
			// like a.size-a.headerLen negative downstream).
			if off+16 > end {
				return nil, fmt.Errorf("%w: 64-bit atom %q header truncated", waxerr.ErrInvalidData, name)
			}
			ext, err := bits.ReadSlice(src, off+8, 8, limit)
			if err != nil {
				return nil, err
			}
			size = int64(binary.BigEndian.Uint64(ext))
			headerLen = 16
			if size < 16 {
				return nil, fmt.Errorf("%w: 64-bit atom %q size %d below 16", waxerr.ErrInvalidData, name, size)
			}
		case size == 0:
			// Runs to the end of the enclosing region (top-level last atom).
			size = end - off
		case size < 8:
			return nil, fmt.Errorf("%w: atom %q size %d below 8", waxerr.ErrInvalidData, name, size)
		}
		if size > end-off {
			if !topLevel {
				return nil, fmt.Errorf("%w: atom %q declares %d bytes but only %d remain in its container",
					waxerr.ErrInvalidData, name, size, end-off)
			}
			// Top-level final atom overruns end-of-file (a truncated download): clamp
			// so the complete earlier atoms still read. This stays consistent on a
			// rewrite because such an atom is last and re-clamps identically on
			// re-parse of the output.
			size = end - off
		}
		n := node{name: name, offset: off, headerLen: headerLen, size: size}
		if containerAtoms[name] {
			if cs := childStart(src, n, limit); cs <= n.end() {
				kids, err := walkAtoms(src, cs, n.end(), depth, limit, false)
				if err != nil {
					return nil, err
				}
				n.children = kids
			}
		}
		out = append(out, n)
		next := off + size
		if next <= off {
			break // no forward progress (corrupt) — stop
		}
		off = next
	}
	// A nested container's children must exactly tile it. Leftover bytes that do
	// not form a complete atom (a ragged tail) are corruption: parse would ignore
	// them, but a create/insert rewrite appends the new tag path after the
	// container's recorded end, leaving the stray bytes to misalign the re-parse of
	// the output. Top-level trailing bytes are tolerated (junk after the last atom
	// stays after everything and re-parses identically).
	//
	// An exception: an all-zero remainder is benign and must be kept readable —
	// QuickTime terminates a udta user-data list with a 32-bit zero, and zero
	// padding cannot form a misaligning atom header. Only a non-zero ragged tail is
	// rejected.
	if !topLevel && off < end {
		tail, err := bits.ReadSlice(src, off, end-off, limit)
		if err != nil {
			return nil, err
		}
		for _, b := range tail {
			if b != 0 {
				return nil, fmt.Errorf("%w: %d trailing byte(s) in a container do not form a complete atom",
					waxerr.ErrInvalidData, end-off)
			}
		}
	}
	return out, nil
}

// find returns the first child with the given name.
func (n node) find(name string) (node, bool) {
	want := atomName(name)
	for _, c := range n.children {
		if c.name == want {
			return c, true
		}
	}
	return node{}, false
}

// findAll appends every descendant (recursively) with the given name.
func (n node) findAll(name string, out []node) []node {
	want := atomName(name)
	for _, c := range n.children {
		if c.name == want {
			out = append(out, c)
		}
		out = c.findAll(name, out)
	}
	return out
}

// offsetTable is a parsed chunk-offset table (stco or co64). Its entries are
// absolute file offsets into the media data; when metadata before the media is
// resized, every entry past the insertion point is shifted. The atom's source
// location and version/flags are kept so the rewritten table lands in place.
type offsetTable struct {
	offset    int64 // atom start in the source
	headerLen int64
	size      int64
	co64      bool     // true: 64-bit entries (co64); false: 32-bit (stco)
	verFlags  [4]byte  // the FullBox version/flags following the header
	entries   []uint64 // chunk offsets
}

// fmtCfg is the decoder-critical sample-entry configuration mixed into the
// essence digest: the codec four-cc plus the audio geometry, so identical media
// bytes under a different codec or layout hash differently.
type fmtCfg struct {
	codec      [4]byte
	channels   uint16
	sampleSize uint16
	sampleRate uint32
}

// atomRef is a lightweight reference to an atom on the tag path (moov, udta,
// meta, ilst) or to the adjacent free atom: enough to copy, resize, or patch its
// size field without re-reading the tree.
type atomRef struct {
	name      [4]byte
	offset    int64
	headerLen int64
	size      int64
}

func (r atomRef) id() string { return string(r.name[:]) }

func (r atomRef) end() int64 { return r.offset + r.size }
