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

// Ogg page header_type flag bits and fixed sizes. (Bit 0x04, end-of-stream, is
// not acted on: the final granule is read from the last page regardless.)
const (
	flagContinued = 0x01 // first packet on the page continues from the previous page
	flagBOS       = 0x02 // beginning of stream
	pageFixedHdr  = 27   // bytes before the segment table
	maxSegments   = 255  // a lacing value of 255 means "packet continues"
)

// maxOggScanBytes bounds the heap retained by rawPage descriptors during scanPages.
// Ogg pages cannot be coalesced because the renumber path rewrites each audio page's
// sequence number and CRC. Without a budget, a stream made of minimum-size pages would
// make even dump and verify retain memory proportional to page count. The metadata
// element cap is not a good fit here because long real streams can legitimately have
// many audio pages.
//
// 64 MiB holds about 930k empty page descriptors. That is far above normal Vorbis and
// Opus pagination, but it rejects adversarial one-packet-per-page streams before they
// can exhaust memory. Tests can pass a smaller scanBudget directly.
const maxOggScanBytes = 64 << 20

// rawPageBytes is one retained rawPage's fixed heap cost on 64-bit builds. The lacing
// table is counted separately as len(p.segs), so max-lacing pages are charged for
// their real footprint.
const rawPageBytes = 72

// rawPage is one parsed Ogg page header plus the location of its body. Audio
// page bodies are never read during parsing - only their byte range is recorded
// - so scanning a long file does not buffer the audio. Field order keeps the
// single-byte flags last so the struct packs to rawPageBytes with no extra padding.
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

// scanPages reads every Ogg page header in src in order, recording each page's
// location and lacing without reading audio bodies. It stops at the first
// position that is not a valid page (trailing junk, or the end of the data),
// returning that offset as end. The first bytes must be a valid Ogg page. It
// checks ctx between pages so scanning a very large file can be cancelled.
func scanPages(ctx context.Context, src core.ReaderAtSized, size, limit, scanBudget int64) (pages []rawPage, end int64, err error) {
	off := int64(0)
	var retained int64 // cumulative heap cost of the rawPage descriptors below
	for off+pageFixedHdr <= size {
		if err := ctx.Err(); err != nil {
			return nil, off, err
		}
		hdr, e := bits.ReadSlice(src, off, pageFixedHdr, limit)
		if e != nil {
			// The loop guard guarantees these 27 bytes are within the file, so a
			// failure here is a real I/O error (e.g. concurrent truncation), not a
			// clean end of stream - surface it, as the segment-table read below does.
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

// paginate lays packets out into Ogg pages starting at sequence number startSeq,
// all with granule position 0 - correct for the Vorbis/Opus header pages this
// builds. It returns the concatenated page bytes and the page count. The
// continued flag is set on any page whose first segment continues a packet from
// the previous page.
func paginate(serial, startSeq uint32, packets [][]byte) (out []byte, pageCount int) {
	// Build the lacing values and the body byte stream. Each packet contributes
	// floor(len/255) lacing values of 255 then one final value of len%255, so a
	// packet whose length is a multiple of 255 ends with an explicit 0 - the
	// packet-boundary marker that keeps it from merging with the next packet.
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
		// The page ends mid-packet exactly when its last lacing value is 255 (the
		// packet has more data); the next page is then a continuation.
		continued = pageLac[len(pageLac)-1] == maxSegments
		i = hi
	}
	return out, pageCount
}

// patchCRC recomputes a page's CRC after only its 4-byte sequence number (at
// offset 18) changed, without re-reading the page body. The Ogg CRC has init 0
// and no final XOR, so it is linear: the new CRC is the old CRC XOR the CRC of a
// "difference" page that is zero everywhere except those 4 bytes. Leading zeros
// before offset 18 contribute nothing (the running CRC stays 0 over zero bytes
// from a zero state), so the difference reduces to the 4 XOR-delta bytes
// followed by zeros to the end of the page (offset 22 through pageLen).
func patchCRC(oldCRC, oldSeq, newSeq uint32, pageLen int64) uint32 {
	var d [4]byte
	binary.LittleEndian.PutUint32(d[:], oldSeq^newSeq)
	adj := bits.UpdateOggCRC(0, d[:])
	adj = bits.UpdateOggCRCZeros(adj, pageLen-22)
	return oldCRC ^ adj
}
