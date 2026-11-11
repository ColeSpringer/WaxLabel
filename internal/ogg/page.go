package ogg

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

var oggMagic = []byte("OggS")

// Page header_type flags and sizes. EOS (0x04) unused; final granule from last page.
const (
	flagContinued = 0x01 // first packet on the page continues from the previous page
	flagBOS       = 0x02 // beginning of stream
	pageFixedHdr  = 27   // bytes before the segment table
	maxSegments   = 255  // a lacing value of 255 means "packet continues"
)

// retainedPerPage is what a parse keeps for an audio page: its descriptor and its essence range.
// Pages cannot be coalesced, as a renumber rewrites each one's sequence number and checksum.
const retainedPerPage = 48

// renumberPerPage is what a rewrite takes to renumber an audio page: two segments and a patch.
const renumberPerPage = 88

// The walk resyncs past bytes that open no page, at most maxGaps times and until
// maxResyncMisses candidates fail it, so junk cannot make it read the file many times over.
const (
	maxGaps         = 256
	maxResyncMisses = 64
)

// rawPage is a page header as the walk reads it; the body is not read. segs is the walk's
// buffer, valid until the callback returns.
type rawPage struct {
	off     int64  // absolute offset of the "OggS" capture pattern
	hdrLen  int64  // 27 + segment count
	bodyLen int64  // sum of the lacing values
	gap     int64  // bytes the walk skipped right before the page, belonging to no page
	segs    []byte // the segment table (lacing values)
	granule uint64
	serial  uint32
	seq     uint32
	crc     uint32
	flags   byte
}

func (p rawPage) total() int64   { return p.hdrLen + p.bodyLen }
func (p rawPage) bodyOff() int64 { return p.off + p.hdrLen }

// doubt is what a walk's caller holds against a whole page.
type doubt uint8

const (
	trusted doubt = iota // counts unverified
	forged               // counts only if its checksum holds, else it is junk
	cut                  // counts only if its checksum holds, else it is a page the file ends inside
	refused              // is no page of this walk, whatever its checksum: junk
)

// stop is what a walk ended at.
type stop uint8

const (
	stopTail  stop = iota // the end of the file, or bytes with no page within reach behind them
	stopCut               // a page the file ends inside
	stopBound             // its resync bounds: pages may follow, unread
)

// walkPages hands fn each page of src from start and returns where it ended and at what. A page
// follows the one before it, verified only where suspect doubts it; where none does, the walk
// skips to the first page within [core.ResyncWindow] that suspect does not refuse and whose
// checksum holds.
func walkPages(ctx context.Context, src core.ReaderAtSized, start, size, limit int64, suspect func(rawPage) (doubt, error), fn func(rawPage) error) (end int64, at stop, err error) {
	w, err := newWalker(src, size, limit)
	if err != nil {
		return start, stopTail, err
	}
	off := start
	for off < size {
		if err := ctx.Err(); err != nil {
			return off, stopTail, err
		}
		p, whole, opens, err := w.pageAt(off)
		if err != nil {
			return off, stopTail, err
		}
		if whole {
			why, err := suspect(p)
			if err != nil {
				return off, stopTail, err
			}
			switch why {
			case refused:
				whole, opens = false, false
			case forged, cut:
				if whole, err = w.checksumHolds(p); err != nil {
					return off, stopTail, err
				}
				opens = why == cut
			}
		}
		if !whole {
			var ok bool
			p, ok, err = w.resync(off, suspect)
			switch {
			case err != nil:
				return off, stopTail, err
			case w.bounded:
				return off, stopBound, nil
			case !ok && opens:
				return off, stopCut, nil
			case !ok:
				return off, stopTail, nil
			}
			p.gap = p.off - off
		}
		if err := fn(p); err != nil {
			return off, stopTail, err
		}
		off = p.off + p.total()
	}
	return off, stopTail, nil
}

// scanBytes is the buffer a walker searches and checksums through: small, so that neither a
// run of junk nor a page behind it is held whole.
const scanBytes = 4 << 10

// walker reads page headers for walkPages through buffers it reuses.
type walker struct {
	src     core.ReaderAtSized
	size    int64
	hdr     []byte // a page header and its segment table
	scan    []byte // what a resync searches and checksums through
	gaps    int
	misses  int
	bounded bool // a resync gave up at maxGaps or maxResyncMisses
}

// newWalker returns a walker over the first size bytes of src, its buffers within limit.
func newWalker(src core.ReaderAtSized, size, limit int64) (*walker, error) {
	if err := bits.CheckAlloc(pageFixedHdr+maxSegments, limit); err != nil {
		return nil, err
	}
	scan := int64(scanBytes)
	if limit > 0 {
		scan = min(scan, limit)
	}
	return &walker{src: src, size: size, hdr: make([]byte, pageFixedHdr+maxSegments), scan: make([]byte, scan)}, nil
}

// pageAt reads the header of the page at off. whole is false when the bytes there are no page
// that ends by size; opens then says they begin like one.
func (w *walker) pageAt(off int64) (p rawPage, whole, opens bool, err error) {
	hdr := w.hdr[:min(int64(len(w.hdr)), w.size-off)]
	if _, err := bits.FillWithin(w.src, hdr, off); err != nil {
		return p, false, false, fmt.Errorf("read page header at %d: %w", off, err)
	}
	if len(hdr) <= len(oggMagic) || !bytes.Equal(hdr[:4], oggMagic) || hdr[4] != 0 {
		return p, false, false, nil
	}
	if len(hdr) < pageFixedHdr || pageFixedHdr+int(hdr[26]) > len(hdr) {
		return p, false, true, nil
	}
	segCount := int(hdr[26])
	p = rawPage{
		off:     off,
		hdrLen:  int64(pageFixedHdr + segCount),
		segs:    hdr[pageFixedHdr : pageFixedHdr+segCount],
		flags:   hdr[5],
		granule: binary.LittleEndian.Uint64(hdr[6:14]),
		serial:  binary.LittleEndian.Uint32(hdr[14:18]),
		seq:     binary.LittleEndian.Uint32(hdr[18:22]),
		crc:     binary.LittleEndian.Uint32(hdr[22:26]),
	}
	for _, v := range p.segs {
		p.bodyLen += int64(v)
	}
	return p, p.total() <= w.size-off, true, nil
}

// resync finds the first page that starts past off, within core.ResyncWindow of it, that suspect
// does not refuse and whose checksum holds. One gap past maxGaps, or out of candidates to try,
// it gives up (bounded).
func (w *walker) resync(off int64, suspect func(rawPage) (doubt, error)) (rawPage, bool, error) {
	p, ok, err := w.seek(off, 1, func(p rawPage) (bool, error) {
		why, err := suspect(p)
		return why != refused, err
	})
	switch {
	case err != nil:
		return rawPage{}, false, err
	case !ok:
		w.bounded = w.misses == maxResyncMisses
		return rawPage{}, false, nil
	case w.gaps == maxGaps:
		w.bounded = true // the page is there and is not read
		return rawPage{}, false, nil
	}
	w.gaps++
	return p, true, nil
}

// seek finds the first page that starts first bytes or more past off, no further than
// core.ResyncWindow past it, that accept takes and whose checksum holds. Each candidate that
// fails counts toward maxResyncMisses, where the search ends.
func (w *walker) seek(off int64, first int, accept func(rawPage) (bool, error)) (rawPage, bool, error) {
	// A candidate may start on the window's last byte.
	end := min(off+core.ResyncWindow+int64(len(oggMagic)), w.size)
	for pos := off + int64(first); w.misses < maxResyncMisses && end-pos >= int64(len(oggMagic)); {
		chunk := w.scan[:min(int64(len(w.scan)), end-pos)]
		if _, err := bits.FillWithin(w.src, chunk, pos); err != nil {
			return rawPage{}, false, fmt.Errorf("read %d bytes at %d for an Ogg page: %w", len(chunk), pos, err)
		}
		i := bytes.Index(chunk, oggMagic)
		if i < 0 {
			pos += int64(len(chunk) - len(oggMagic) + 1) // the pattern may straddle two chunks
			continue
		}
		pos += int64(i)
		p, whole, _, err := w.pageAt(pos)
		if err != nil {
			return rawPage{}, false, err
		}
		if whole {
			if ok, err := accept(p); err != nil {
				return rawPage{}, false, err
			} else if ok {
				if ok, err = w.checksumHolds(p); err != nil || ok {
					return p, ok, err
				}
			}
		}
		w.misses++
		pos++
	}
	return rawPage{}, false, nil
}

// checksumHolds reads the whole page p through the scan buffer and verifies its CRC, computed
// with the field zeroed.
func (w *walker) checksumHolds(p rawPage) (bool, error) {
	var crc uint32
	for at, end := p.off, p.off+p.total(); at < end; {
		chunk := w.scan[:min(int64(len(w.scan)), end-at)]
		if _, err := bits.FillWithin(w.src, chunk, at); err != nil {
			return false, fmt.Errorf("read Ogg page at %d: %w", p.off, err)
		}
		if at == p.off {
			clear(chunk[22:26])
		}
		crc = bits.UpdateOggCRC(crc, chunk)
		at += int64(len(chunk))
	}
	return crc == p.crc, nil
}

// buildPage assembles one Ogg page (header + body) with a correct CRC. The CRC
// is computed over the whole page with its checksum field zeroed, as the spec
// requires; granule, serial, sequence number, flags, and lacing are supplied.
func buildPage(flags byte, granule uint64, serial, seq uint32, lacing, body []byte) []byte {
	page := make([]byte, pageFixedHdr+len(lacing)+len(body))
	copy(page, oggMagic)
	page[4] = 0 // stream structure version
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], serial)
	binary.LittleEndian.PutUint32(page[18:22], seq)
	// page[22:26] (CRC) stays zero for the computation below.
	page[26] = byte(len(lacing))
	copy(page[27:], lacing)
	copy(page[27+len(lacing):], body)
	binary.LittleEndian.PutUint32(page[22:26], bits.OggCRC(page))
	return page
}

// paginate lays header packets into pages from startSeq: granule 0, or -1 on a page no packet
// ends on. Returns bytes and page count. Sets continued when a page continues a packet.
func paginate(serial, startSeq uint32, packets [][]byte) (out []byte, pageCount int) {
	// Lacing: floor(len/255)×255 then len%255. Multiple of 255 ends with 0
	// (packet boundary so it does not merge with the next).
	var lacing, body []byte
	for _, pkt := range packets {
		n := len(pkt)
		for n >= maxSegments {
			lacing = append(lacing, maxSegments)
			n -= maxSegments
		}
		lacing = append(lacing, byte(n))
		body = append(body, pkt...)
	}

	seq := startSeq
	bodyPos := 0
	continued := false
	for i := 0; i < len(lacing); {
		hi := min(i+maxSegments, len(lacing))
		pageLac := lacing[i:hi]
		pl := 0
		for _, v := range pageLac {
			pl += int(v)
		}
		var flags byte
		if continued {
			flags = flagContinued
		}
		var granule uint64
		if !slices.ContainsFunc(pageLac, func(v byte) bool { return v < maxSegments }) {
			granule = math.MaxUint64
		}
		out = append(out, buildPage(flags, granule, serial, seq, pageLac, body[bodyPos:bodyPos+pl])...)
		bodyPos += pl
		seq++
		pageCount++
		// Last lacing 255 ⇒ packet continues on the next page.
		continued = pageLac[len(pageLac)-1] == maxSegments
		i = hi
	}
	return out, pageCount
}

// paginateBOS builds the BOS page, numbered seq, for a single id packet. Used when id bytes
// change (FLAC header-packet count, Opus gain); otherwise page 0 is copied verbatim.
func paginateBOS(serial, seq uint32, pkt []byte) ([]byte, int) {
	var lacing []byte
	n := len(pkt)
	for n >= maxSegments {
		lacing = append(lacing, maxSegments)
		n -= maxSegments
	}
	lacing = append(lacing, byte(n))
	if len(lacing) > maxSegments {
		return nil, 0 // real id packets are far smaller
	}
	return buildPage(flagBOS, 0, serial, seq, lacing, pkt), 1
}

// patchCRC updates CRC after seq (offset 18) changes, without re-reading the body.
// Ogg CRC is linear (init 0, no final XOR): new = old XOR CRC(delta at 18:22,
// zeros through page end).
func patchCRC(oldCRC, oldSeq, newSeq uint32, pageLen int64) uint32 {
	var d [4]byte
	binary.LittleEndian.PutUint32(d[:], oldSeq^newSeq)
	adj := bits.UpdateOggCRC(0, d[:])
	adj = bits.UpdateOggCRCZeros(adj, pageLen-22)
	return oldCRC ^ adj
}
