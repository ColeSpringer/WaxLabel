package ogg

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

var oggMagic = []byte("OggS")

// Page header_type flags and sizes. EOS (0x04) unused; final granule from last page.

const (
	flagContinued = 0x01 // first packet on the page continues from the previous page
	flagBOS       = 0x02 // beginning of stream
	pageFixedHdr  = 27   // bytes before the segment table
	maxSegments   = 255  // a lacing value of 255 means "packet continues"
)

// maxOggScanBytes caps heap for rawPage descriptors in scanPages. Pages cannot be
// coalesced (renumber rewrites seq+CRC per page). 64 MiB ≈ 930k empty descriptors;
// rejects adversarial one-packet-per-page streams. Tests may pass a smaller budget.

const maxOggScanBytes = 64 << 20

// rawPageBytes: 64-bit sizeof(rawPage). 32-bit is smaller (conservative over-count).
// Lacing charged separately via len(p.segs).

const rawPageBytes = 72

// rawPage: page header plus body location. Audio bodies not buffered at parse.
// flags last so the struct packs to rawPageBytes.

type rawPage struct {
	off     int64  // absolute offset of the "OggS" capture pattern
	hdrLen  int64  // 27 + segment count
	bodyLen int64  // sum of the lacing values
	segs    []byte // the segment table (lacing values)
	granule uint64
	serial  uint32
	seq     uint32
	crc     uint32
	flags   byte
}

func (p rawPage) total() int64   { return p.hdrLen + p.bodyLen }
func (p rawPage) bodyOff() int64 { return p.off + p.hdrLen }

// scanPages records each page header and lacing (no audio bodies). Stops at first
// non-page (junk or EOF). First bytes must be a valid page. Checks ctx between pages.

func scanPages(ctx context.Context, src core.ReaderAtSized, size, limit, scanBudget int64) (pages []rawPage, end int64, err error) {
	off := int64(0)
	var retained int64 // cumulative heap cost of the rawPage descriptors below
	for off+pageFixedHdr <= size {
		if err := ctx.Err(); err != nil {
			return nil, off, err
		}
		hdr, e := bits.ReadSlice(src, off, pageFixedHdr, limit)
		if e != nil {
			// Within file by loop guard: real I/O error, not clean EOF.

			return nil, off, fmt.Errorf("%w: read page header at %d: %v", waxerr.ErrInvalidData, off, e)
		}
		if !bytes.Equal(hdr[0:4], oggMagic) || hdr[4] != 0 {
			break // not a page boundary here; stop the scan (trailing junk / next stream)
		}
		segCount := int64(hdr[26])
		segs, e := bits.ReadSlice(src, off+pageFixedHdr, segCount, limit)
		if e != nil {
			return nil, off, fmt.Errorf("%w: truncated segment table at %d", waxerr.ErrInvalidData, off)
		}
		var bodyLen int64
		for _, v := range segs {
			bodyLen += int64(v)
		}
		p := rawPage{
			off:     off,
			hdrLen:  pageFixedHdr + segCount,
			bodyLen: bodyLen,
			segs:    segs,
			flags:   hdr[5],
			granule: binary.LittleEndian.Uint64(hdr[6:14]),
			serial:  binary.LittleEndian.Uint32(hdr[14:18]),
			seq:     binary.LittleEndian.Uint32(hdr[18:22]),
			crc:     binary.LittleEndian.Uint32(hdr[22:26]),
		}
		if p.total() > size-off {
			return nil, off, fmt.Errorf("%w: Ogg page at %d overruns the file", waxerr.ErrInvalidData, off)
		}
		// Count the descriptor and its lacing table so both empty-page and max-lacing
		// floods are bounded by the memory they actually retain.
		retained += rawPageBytes + int64(len(segs))
		if retained > scanBudget {
			return nil, off, fmt.Errorf("%w: Ogg page descriptors exceed the %d-byte scan budget", waxerr.ErrSizeTooLarge, scanBudget)
		}
		pages = append(pages, p)
		off += p.total()
	}
	if len(pages) == 0 {
		return nil, 0, fmt.Errorf("%w: not an Ogg stream", waxerr.ErrInvalidData)
	}
	return pages, off, nil
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

// paginate lays packets into pages from startSeq, granule 0 (header pages).
// Returns bytes and page count. Sets continued when a page continues a packet.

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
		out = append(out, buildPage(flags, 0, serial, seq, pageLac, body[bodyPos:bodyPos+pl])...)
		bodyPos += pl
		seq++
		pageCount++
		// Last lacing 255 ⇒ packet continues on the next page.

		continued = pageLac[len(pageLac)-1] == maxSegments
		i = hi
	}
	return out, pageCount
}

// paginateBOS builds the BOS page for a single id packet. Used when id bytes
// change (FLAC header-packet count); otherwise page 0 is copied verbatim.

func paginateBOS(serial uint32, pkt []byte) ([]byte, int) {
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
	return buildPage(flagBOS, 0, serial, 0, lacing, pkt), 1
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
