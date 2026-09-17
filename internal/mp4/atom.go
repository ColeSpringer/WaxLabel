package mp4

import (
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// An MP4 file is a tree of atoms (a.k.a. A size of 1 means a 64-bit size follows the
// type (header is then 16 bytes);

// metaSkip is the version/flags prefix inside a "meta" atom before its children.
const metaSkip = 4

// containerAtoms are the atoms this codec descends into: the path to the iTunes tag
// list (moov.udta.meta.ilst) and to the chunk-offset tables (moov.trak.mdia.minf.stbl).
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
	size      int64 // total atom length including the header (clamped to the region)
	// truncated records that this atom's declared size overran the region and was clamped
	// (only a top-level final atom can be - a truncated download). The "runs to EOF"
	// sentinel (a declared 0) is resolved to the region length before the clamp, so it
	// never reads as truncated.
	truncated bool
	children  []node
}

func (n node) id() string        { return string(n.name[:]) }
func (n node) payloadOff() int64 { return n.offset + n.headerLen }
func (n node) end() int64        { return n.offset + n.size }

// childStart returns where a container atom's children begin: the payload start, plus
// the 4-byte FullBox version/flags prefix when the atom is a "meta" box in its
// ISO/iTunes form.
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

// trailingGap returns the count of unusable bytes between where a container's children
// end and n.end(): the last child's end, or - only when childless - the child-start
// position (read lazily, since a has-children container never needs it), subtracted
// from n.end().
func trailingGap(src core.ReaderAtSized, n node, limit int64) int64 {
	var childEnd int64
	if k := len(n.children); k > 0 {
		childEnd = n.children[k-1].end()
	} else {
		childEnd = childStart(src, n, limit)
	}
	return n.end() - childEnd
}

// walkAtoms parses the atoms in [start, end) of src into a node tree, recursing into
// container atoms up to the depth guard.
func walkAtoms(src core.ReaderAtSized, start, end int64, depth *bits.Depth, limit int64, topLevel bool) ([]node, error) {
	if err := depth.Enter(); err != nil {
		return nil, err
	}
	defer depth.Leave()

	var out []node
	off := start
walkLoop:
	for off+8 <= end {
		// The depth guard's element budget bounds atoms across the whole tree, not per
		// container, since the guard is shared through recursion. The write path re-walks
		// with an uncapped guard.
		if err := depth.Count(); err != nil {
			return nil, err
		}
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
			// A declared size of 0 means "runs to the end of the enclosing region," which is
			// only meaningful for a top-level final atom (it extends to EOF).
			if !topLevel {
				break walkLoop
			}
			size = end - off
		case size < 8:
			return nil, fmt.Errorf("%w: atom %q size %d below 8", waxerr.ErrInvalidData, name, size)
		}
		// Only a top-level size==0 sentinel became end-off above (a nested one broke out
		// of the walk), so past here only a genuinely oversized atom trips the clamp below
		// and reads as truncated.
		truncated := false
		if size > end-off {
			if !topLevel {
				return nil, fmt.Errorf("%w: atom %q declares %d bytes but only %d remain in its container",
					waxerr.ErrInvalidData, name, size, end-off)
			}
			// Top-level final atom overruns end-of-file (a truncated download): clamp
			// so the complete earlier atoms still read. This stays consistent on a
			// rewrite because such an atom is last and re-clamps identically on
			// re-parse of the output.
			truncated = true
			size = end - off
		}
		n := node{name: name, offset: off, headerLen: headerLen, size: size, truncated: truncated}
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
			break // no forward progress (corrupt) - stop
		}
		off = next
	}
	// A nested container's children must exactly tile it. An exception: an all-zero
	// remainder is benign and must be kept readable - QuickTime terminates a udta
	// user-data list with a 32-bit zero, and zero padding cannot form a misaligning atom
	// header.
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

// offsetTable is a parsed chunk-offset table (stco or co64).
type offsetTable struct {
	offset    int64 // atom start in the source
	headerLen int64
	size      int64
	name      [4]byte // source atom (stco, co64, saio), for error messages
	co64      bool    // true: 64-bit entries (co64, or a version 1 saio); false: 32-bit
	// entryPrefix is the byte count between the version/flags word and entry_count: a
	// saio's optional aux_info_type pair when flags&1, and 0 for stco/co64.
	entryPrefix int64
	verFlags    [4]byte  // the FullBox version/flags following the header
	entries     []uint64 // chunk offsets
}

func (t offsetTable) id() string { return string(t.name[:]) }

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

func (r atomRef) payloadOff() int64 { return r.offset + r.headerLen }

// sizeField returns the offset from the atom start and byte width of the field that
// encodes the atom's total size. A 16-byte header is the 64-bit largesize form, whose
// real size is the 8-byte field 8 bytes in;
func (r atomRef) sizeField() (off, width int64) {
	if r.headerLen == 16 {
		return 8, 8
	}
	return 0, 4
}

// putBoxSize writes newSize into a big-endian box size field of the given width
// (4 or 8 bytes), as returned by [atomRef.sizeField].
func putBoxSize(field []byte, width, newSize int64) {
	if width == 8 {
		binary.BigEndian.PutUint64(field, uint64(newSize))
		return
	}
	binary.BigEndian.PutUint32(field, uint32(newSize))
}
