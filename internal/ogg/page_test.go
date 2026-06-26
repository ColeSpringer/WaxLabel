package ogg

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"unsafe"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestRawPageBytesMatchesStructSize keeps the retained-byte accounting honest. The
// budget constant must match the real struct size so future field additions or padding
// changes cannot silently undercount the scan budget.
func TestRawPageBytesMatchesStructSize(t *testing.T) {
	if got := int(unsafe.Sizeof(rawPage{})); got != rawPageBytes {
		t.Errorf("unsafe.Sizeof(rawPage{}) = %d, but rawPageBytes = %d; update the const or pack the struct", got, rawPageBytes)
	}
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*131 + 7)
	}
	return b
}

// TestPatchCRCMatchesRecompute is the linchpin of the audio-page renumber path:
// patching a page's CRC after only its sequence number changed must equal a full
// recomputation from the page bytes. If this drifts, renumbered files get
// silently-corrupt checksums.
func TestPatchCRCMatchesRecompute(t *testing.T) {
	body := pattern(5000)
	var lacing []byte
	for n := len(body); n >= maxSegments; n -= maxSegments {
		lacing = append(lacing, maxSegments)
	}
	lacing = append(lacing, byte(len(body)%maxSegments))

	const serial = uint32(0xABCD1234)
	cases := []struct{ old, new uint32 }{
		{0, 1}, {5, 9}, {2, 1}, {100, 3}, {0xFFFFFFF0, 0x00000003}, {1, 0xFFFFFFFF},
	}
	for _, c := range cases {
		oldPage := buildPage(0, 12345, serial, c.old, lacing, body)
		newPage := buildPage(0, 12345, serial, c.new, lacing, body)
		oldCRC := binary.LittleEndian.Uint32(oldPage[22:26])
		want := binary.LittleEndian.Uint32(newPage[22:26])
		got := patchCRC(oldCRC, c.old, c.new, int64(len(oldPage)))
		if got != want {
			t.Errorf("patchCRC(old seq %d -> %d): got %#08x, want %#08x", c.old, c.new, got, want)
		}
	}
}

// TestPaginateRoundTrip checks that packets laid out by paginate re-scan to the
// exact same packet bytes (including a packet large enough to span pages and one
// whose length is an exact multiple of 255, which needs a trailing 0 lacing), and
// that every emitted page carries a valid CRC.
func TestPaginateRoundTrip(t *testing.T) {
	packets := [][]byte{
		pattern(100),
		pattern(70000),       // spans several pages (255*255 = 65025 body bytes max/page)
		pattern(255 * 3),     // exact multiple of 255 -> explicit 0 boundary lacing
		pattern(0),           // empty packet
		pattern(255*255 - 1), // fills a page to the brim
	}
	out, count := paginate(7, 1, packets)
	if count < 3 {
		t.Fatalf("expected several pages for ~135 KiB of packets, got %d", count)
	}

	src := core.BytesSource(out)
	pages, end, err := scanPages(context.Background(), src, int64(len(out)), bits.DefaultLimits.MaxAllocBytes, maxOggScanBytes)
	if err != nil {
		t.Fatal(err)
	}
	if end != int64(len(out)) {
		t.Errorf("scan ended at %d, want %d", end, len(out))
	}

	// Every page's stored CRC must match a recomputation over the page with its
	// checksum field zeroed.
	for _, p := range pages {
		raw, _ := bits.ReadSlice(src, p.off, p.total(), 1<<20)
		stored := binary.LittleEndian.Uint32(raw[22:26])
		for i := 22; i < 26; i++ {
			raw[i] = 0
		}
		if got := bits.OggCRC(raw); got != stored {
			t.Errorf("page seq %d CRC = %#08x, recomputed %#08x", p.seq, stored, got)
		}
	}

	// Reassemble packets and compare to the input.
	var got [][]byte
	var cur []byte
	for _, p := range pages {
		body, _ := bits.ReadSlice(src, p.bodyOff(), p.bodyLen, 1<<20)
		o := 0
		for _, lac := range p.segs {
			cur = append(cur, body[o:o+int(lac)]...)
			o += int(lac)
			if lac < maxSegments {
				got = append(got, cur)
				cur = nil
			}
		}
	}
	if len(got) != len(packets) {
		t.Fatalf("reassembled %d packets, want %d", len(got), len(packets))
	}
	for i := range packets {
		if string(got[i]) != string(packets[i]) {
			t.Errorf("packet %d round-trip mismatch (len %d vs %d)", i, len(got[i]), len(packets[i]))
		}
	}
}

// TestPaginateSequenceAndContinuation verifies sequence numbers increment from
// the start value and that pages continuing a packet set the continued flag
// while fresh-packet pages do not.
func TestPaginateSequenceAndContinuation(t *testing.T) {
	out, _ := paginate(1, 5, [][]byte{pattern(70000)}) // one packet across pages
	src := core.BytesSource(out)
	pages, _, err := scanPages(context.Background(), src, int64(len(out)), 1<<20, maxOggScanBytes)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pages {
		if p.seq != uint32(5+i) {
			t.Errorf("page %d seq = %d, want %d", i, p.seq, 5+i)
		}
		wantCont := i > 0 // a single big packet continues onto every page after the first
		if got := p.flags&flagContinued != 0; got != wantCont {
			t.Errorf("page %d continued flag = %v, want %v", i, got, wantCont)
		}
	}
}

func TestScanPagesRejectsNonOgg(t *testing.T) {
	if _, _, err := scanPages(context.Background(), core.BytesSource([]byte("not an ogg file at all")), 22, 1<<20, maxOggScanBytes); err == nil {
		t.Error("expected an error for non-Ogg input")
	}
}

// TestScanPagesManyPagesUncapped guards against someone later applying the
// metadata element cap (bits.Limits.MaxElements, default 100000) to the Ogg page
// loop. One apage is recorded per audio page, so the count scales with audio
// duration - a long Opus/Vorbis stream legitimately has far more than the cap.
// scanPages must accept them all.
func TestScanPagesManyPagesUncapped(t *testing.T) {
	const n = 120000 // exceeds the 100000 metadata cap; an audio-granularity loop must not honor it
	lacing := []byte{1}
	body := []byte{0xAA}
	buf := make([]byte, 0, n*(pageFixedHdr+len(lacing)+len(body)))
	for i := 0; i < n; i++ {
		buf = append(buf, buildPage(0, uint64(i), 0xCAFEBABE, uint32(i), lacing, body)...)
	}
	src := core.BytesSource(buf)
	pages, end, err := scanPages(context.Background(), src, int64(len(buf)), bits.DefaultLimits.MaxAllocBytes, maxOggScanBytes)
	if err != nil {
		t.Fatalf("scanPages on %d pages: %v", n, err)
	}
	if len(pages) != n {
		t.Fatalf("scanned %d pages, want %d", len(pages), n)
	}
	if end != int64(len(buf)) {
		t.Errorf("scan ended at %d, want %d", end, len(buf))
	}
}

// TestScanPagesBoundsRetainedBytes checks that scanPages uses a byte budget, not a
// page count, for retained page descriptors. The lacing table is charged too: the same
// page count that fits as empty pages overflows once each page carries a full lacing
// table.
func TestScanPagesBoundsRetainedBytes(t *testing.T) {
	const budget = 64 << 10 // small budget so the bound trips quickly

	build := func(n int, lacing []byte) []byte {
		bodyLen := 0
		for _, v := range lacing {
			bodyLen += int(v)
		}
		body := make([]byte, bodyLen)
		var buf []byte
		for i := 0; i < n; i++ {
			buf = append(buf, buildPage(0, uint64(i), 0xCAFEBABE, uint32(i), lacing, body)...)
		}
		return buf
	}
	scan := func(buf []byte) error {
		_, _, err := scanPages(context.Background(), core.BytesSource(buf), int64(len(buf)), bits.DefaultLimits.MaxAllocBytes, budget)
		return err
	}

	// An empty-page flood past the budget is rejected before it can retain unbounded
	// descriptors. 72 B per descriptor * 1000 pages = 72000 B > the 65536 B budget.
	if err := scan(build(1000, nil)); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("empty-page flood: got err %v, want ErrSizeTooLarge", err)
	}

	// The lacing table is counted, not just the struct. 500 * 72 = 36000 B, but
	// 500 * (72+255) = 163500 B.
	const n = 500
	maxLacing := make([]byte, maxSegments) // 255 lacing values (zero-length segments)
	if err := scan(build(n, nil)); err != nil {
		t.Fatalf("%d empty pages within budget: unexpected err %v", n, err)
	}
	if err := scan(build(n, maxLacing)); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("%d max-lacing pages: got err %v, want ErrSizeTooLarge (lacing bytes must count)", n, err)
	}
}
