package wav

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/waxerr"
)

// RF64 (EBU Tech 3306) and its BW64 successor lift RIFF's 4 GiB ceiling: the header
// id becomes "RF64"/"BW64", every size field that no longer fits reads 0xFFFFFFFF, and
// a mandatory "ds64" chunk, always the first chunk, carries the real 64-bit values.
const (
	rf64Marker  = 0xFFFFFFFF
	ds64MinBody = 8 + 8 + 8 + 4 // riffSize, dataSize, sampleCount, tableLength
	ds64Entry   = 4 + 8         // chunk id, 64-bit size
	// maxDS64Entries bounds the chunk-size table against a hostile tableLength. A file
	// needs one entry per non-data chunk above 4 GiB, so any plausible count is tiny.
	maxDS64Entries = 1 << 16
)

// ds64Size is one entry of the ds64 chunk-size table: the real 64-bit body length
// of a non-data chunk whose own size field reads the 0xFFFFFFFF marker.
type ds64Size struct {
	id   [4]byte
	size uint64
}

// ds64 is the decoded ds64 chunk plus the walk state that matches its table entries
// to chunks. A nil *ds64 is a plain RIFF file; override then declines every chunk.
type ds64 struct {
	riffSize    uint64
	dataSize    uint64
	sampleCount uint64
	table       []ds64Size
	// used counts how many table entries of each id the walk has consumed, so
	// repeated ids match in file order rather than all resolving to the first entry.
	used map[[4]byte]int
	// dataSeen marks that ds64.dataSize has been handed out. Only the first data
	// chunk gets it; a malformed file with two must not claim the same size twice.
	dataSeen bool
}

// parseDS64 reads the ds64 chunk, which RF64 requires first, at offset 12. It runs
// before the chunk walk, which needs its sizes and the container boundary.
func parseDS64(r io.ReaderAt, size, limit int64) (*ds64, error) {
	head, err := bits.ReadSlice(r, 12, 8, limit)
	if err != nil || string(head[0:4]) != "ds64" {
		return nil, fmt.Errorf("%w: RF64/BW64 file has no ds64 chunk where one is required", waxerr.ErrInvalidData)
	}
	bodyLen := int64(binary.LittleEndian.Uint32(head[4:8]))
	if bodyLen < ds64MinBody || 20+bodyLen > size {
		return nil, fmt.Errorf("%w: ds64 chunk body is %d bytes, need at least %d", waxerr.ErrInvalidData, bodyLen, ds64MinBody)
	}
	body, err := bits.ReadSlice(r, 20, bodyLen, limit)
	if err != nil {
		return nil, err
	}
	t := &ds64{
		riffSize:    binary.LittleEndian.Uint64(body[0:8]),
		dataSize:    binary.LittleEndian.Uint64(body[8:16]),
		sampleCount: binary.LittleEndian.Uint64(body[16:24]),
		used:        map[[4]byte]int{},
	}
	n := int(binary.LittleEndian.Uint32(body[24:28]))
	if n > maxDS64Entries {
		return nil, fmt.Errorf("%w: ds64 table declares %d entries", waxerr.ErrSizeTooLarge, n)
	}
	// A table declaring more entries than the body holds is truncated, not fatal: the
	// present entries still resolve their chunks; the rest keep the declared 32-bit
	// size, which the walk clamps to the file.
	for i := 0; i < n && 28+(i+1)*ds64Entry <= len(body); i++ {
		e := body[28+i*ds64Entry:]
		var id [4]byte
		copy(id[:], e[0:4])
		t.table = append(t.table, ds64Size{id: id, size: binary.LittleEndian.Uint64(e[4:12])})
	}
	return t, nil
}

// override resolves one chunk's real body length for [iff.WalkOptions.SizeOverride].
func (t *ds64) override(id [4]byte, declared uint32) (int64, bool) {
	if t == nil || declared != rf64Marker {
		return 0, false
	}
	if string(id[:]) == "data" && !t.dataSeen {
		t.dataSeen = true
		return sizeFits(t.dataSize)
	}
	seen := t.used[id]
	for _, e := range t.table {
		if e.id != id {
			continue
		}
		if seen > 0 {
			seen--
			continue
		}
		t.used[id]++
		return sizeFits(e.size)
	}
	return 0, false
}

// sizeFits converts a declared 64-bit size to the signed length the walker uses,
// reporting no-override for a value above MaxInt64, which would read as a negative
// copy range.
func sizeFits(n uint64) (int64, bool) {
	if n > math.MaxInt64 {
		return 0, false
	}
	return int64(n), true
}

// clone deep-copies the decoded chunk so a Document stays detached. The walk state
// (used/dataSeen) belongs to one walk and is not carried.
func (t *ds64) clone() *ds64 {
	if t == nil {
		return nil
	}
	c := *t
	c.table = slices.Clone(t.table)
	c.used = map[[4]byte]int{}
	return &c
}

// renderDS64 builds the ds64 chunk body for an output file: the recomputed container
// and data sizes, the source's sample count (the audio is untouched), and a table
// entry for every non-data chunk that still needs one.
func renderDS64(riffSize, dataSize, sampleCount uint64, table []ds64Size) []byte {
	body := make([]byte, ds64MinBody, ds64MinBody+len(table)*ds64Entry)
	binary.LittleEndian.PutUint64(body[0:8], riffSize)
	binary.LittleEndian.PutUint64(body[8:16], dataSize)
	binary.LittleEndian.PutUint64(body[16:24], sampleCount)
	binary.LittleEndian.PutUint32(body[24:28], uint32(len(table)))
	for _, e := range table {
		body = append(body, e.id[:]...)
		body = binary.LittleEndian.AppendUint64(body, e.size)
	}
	return body
}
