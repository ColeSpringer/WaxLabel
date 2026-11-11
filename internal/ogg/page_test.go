package ogg

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestRetainedPerPageMatchesWhatAPageKeeps checks that retainedPerPage is what a parse keeps for
// an audio page, its descriptor and its essence range: at least that on every platform, so the
// budget cannot undercount, and exactly that on 64-bit, the tight case.
func TestRetainedPerPageMatchesWhatAPageKeeps(t *testing.T) {
	got := int(unsafe.Sizeof(apage{}) + unsafe.Sizeof([2]int64{}))
	if got > retainedPerPage {
		t.Errorf("an audio page keeps %d bytes, over retainedPerPage = %d", got, retainedPerPage)
	}
	if strconv.IntSize == 64 && got != retainedPerPage {
		t.Errorf("an audio page keeps %d bytes, but retainedPerPage = %d", got, retainedPerPage)
	}
}

// manyPageOpus is an Opus stream of n audio pages, a one-byte packet each.
func manyPageOpus(n int) []byte {
	const serial = 0x4F4747
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xBB, 0, 0, 0, 0, 0}
	tags := opusTagsPacket("libopus 1.4", "TITLE=Song")
	out := make([]byte, 0, 128+n*(pageFixedHdr+2))
	out = append(out, buildPage(flagBOS, 0, serial, 0, []byte{byte(len(head))}, head)...)
	out = append(out, buildPage(0, 0, serial, 1, []byte{byte(len(tags))}, tags)...)
	// An Opus packet of one byte is a 20 ms frame with no payload.
	for i := range n {
		out = append(out, buildPage(0, uint64(960*(i+1)), serial, uint32(i+2), []byte{1}, []byte{0x08})...)
	}
	return out
}

// TestScanBudgetFollowsLimits: what the page walk may keep is the caller's allocation limit, and
// a stream over it is refused as too large, naming the limit to raise.
func TestScanBudgetFollowsLimits(t *testing.T) {
	const pages = 2000
	stream := manyPageOpus(pages)
	opts := core.DefaultParseOptions()
	opts.Limits.MaxAllocBytes = pages * retainedPerPage
	if _, err := parse(context.Background(), Codec{}, core.BytesSource(stream), opts); err != nil {
		t.Fatalf("limit %d holds %d pages: %v", opts.Limits.MaxAllocBytes, pages, err)
	}
	opts.Limits.MaxAllocBytes--
	_, err := parse(context.Background(), Codec{}, core.BytesSource(stream), opts)
	if !errors.Is(err, waxerr.ErrSizeTooLarge) || !strings.Contains(err.Error(), "Limits.MaxAllocBytes") {
		t.Errorf("one byte under: err = %v, want ErrSizeTooLarge naming Limits.MaxAllocBytes", err)
	}
}

// TestParseKeepsWhatItCharges: the page descriptors and essence ranges are grown to capacities
// the allocation limit covers, not to whatever append rounds up to.
func TestParseKeepsWhatItCharges(t *testing.T) {
	const pages = 5000
	opts := core.DefaultParseOptions()
	opts.Limits.MaxAllocBytes = pages * retainedPerPage
	m, err := parse(context.Background(), Codec{}, core.BytesSource(manyPageOpus(pages)), opts)
	if err != nil {
		t.Fatal(err)
	}
	kept := cap(m.Native.(*doc).audioPages)*int(unsafe.Sizeof(apage{})) + cap(m.AudioRanges)*int(unsafe.Sizeof([2]int64{}))
	if int64(kept) > opts.Limits.MaxAllocBytes {
		t.Errorf("the parse keeps %d bytes for %d pages, over the limit of %d", kept, pages, opts.Limits.MaxAllocBytes)
	}
}

// TestRenumberPerPageMatchesWhatAPageTakes pins renumberPerPage to the two segments and the
// patch a renumbered page takes, as retainedPerPage is pinned.
func TestRenumberPerPageMatchesWhatAPageTakes(t *testing.T) {
	got := 2*int(unsafe.Sizeof(bits.Segment{})) + 8
	if got > renumberPerPage || strconv.IntSize == 64 && got != renumberPerPage {
		t.Errorf("a renumbered page takes %d bytes, but renumberPerPage = %d", got, renumberPerPage)
	}
}

// TestRenumberRefusedOverTheWriteLimit: renumbering takes memory for every audio page, which the
// write's allocation limit bounds like any allocation; an edit that moves no page number is not
// held to it.
func TestRenumberRefusedOverTheWriteLimit(t *testing.T) {
	const pages = 2000
	src := manyPageOpus(pages)
	base, err := parse(context.Background(), Codec{}, core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	plan := func(limit int64, comment string) error {
		edited := base.Clone()
		edited.Tags.Set("COMMENT", comment)
		opts := core.DefaultWriteOptions()
		opts.Limits.MaxAllocBytes = limit
		_, err := NewOpus().Plan(context.Background(), base, edited, opts)
		return err
	}
	long := strings.Repeat("c", 70000) // spills the comment header onto a second page
	if err := plan(pages*renumberPerPage, long); err != nil {
		t.Fatalf("a limit that covers the renumber: %v", err)
	}
	err = plan(pages*renumberPerPage-1, long)
	if !errors.Is(err, waxerr.ErrSizeTooLarge) || !strings.Contains(err.Error(), "Limits.MaxAllocBytes") {
		t.Errorf("one byte under: err = %v, want ErrSizeTooLarge naming Limits.MaxAllocBytes", err)
	}
	if err := plan(pages*renumberPerPage-1, "short"); err != nil {
		t.Errorf("an edit that renumbers nothing: %v", err)
	}
}

// TestLongStreamParsesAndRenumbersUnderDefaultLimits: a stream of over a million pages reads
// under the default limits, and an edit that moves every page number writes them contiguous with
// two segments a page.
func TestLongStreamParsesAndRenumbersUnderDefaultLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a stream of a million pages")
	}
	const pages = 1_000_002
	src := manyPageOpus(pages)
	base, err := parse(context.Background(), Codec{}, core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(base.Native.(*doc).audioPages); got != pages {
		t.Fatalf("audio pages = %d, want %d", got, pages)
	}
	if got, want := base.Properties.First().Duration, pages*20*time.Millisecond; got != want {
		t.Errorf("duration = %v, want %v", got, want)
	}

	edited := base.Clone()
	edited.Tags.Set("COMMENT", strings.Repeat("c", 70000))
	plan, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(plan.Segments), 3+2*pages; got != want {
		t.Errorf("segments = %d, want %d", got, want)
	}
	for i, seq := range scanSeqs(t, renderPlan(t, src, plan)) {
		if seq != uint32(i) {
			t.Fatalf("page %d has sequence number %d", i, seq)
		}
	}
}

// collectPages walks b and returns its pages, each with its own copy of the lacing values, and
// where the walk stopped.
func collectPages(b []byte) (pages []rawPage, end int64, err error) {
	trust := func(rawPage) (doubt, error) { return trusted, nil }
	end, _, err = walkPages(context.Background(), core.BytesSource(b), 0, int64(len(b)), bits.DefaultLimits.MaxAllocBytes, trust, func(p rawPage) error {
		p.segs = slices.Clone(p.segs)
		pages = append(pages, p)
		return nil
	})
	return pages, end, err
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*131 + 7)
	}
	return b
}

// TestPatchCRCMatchesRecompute checks that patching a page's CRC after only its
// sequence number changed equals a full recomputation, or renumbered files get corrupt
// checksums.
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

// TestPaginateRoundTrip checks that packets laid out by paginate re-scan to the same
// bytes (including one spanning pages and one whose length is a multiple of 255, which
// needs a trailing 0 lacing), and that every page carries a valid CRC.
func TestPaginateRoundTrip(t *testing.T) {
	packets := [][]byte{
		pattern(100),
		pattern(200000),      // spans several pages (255*255 = 65025 body bytes max/page), two of them whole
		pattern(255 * 3),     // exact multiple of 255 -> explicit 0 boundary lacing
		pattern(0),           // empty packet
		pattern(255*255 - 1), // fills a page to the brim
	}
	out, count := paginate(7, 1, packets)
	if count < 3 {
		t.Fatalf("expected several pages for ~265 KiB of packets, got %d", count)
	}
	if id, _ := paginateBOS(7, 0, pattern(30)); binary.LittleEndian.Uint64(id[6:14]) != 0 {
		t.Errorf("id page granule = %#x, want 0", binary.LittleEndian.Uint64(id[6:14]))
	}

	src := core.BytesSource(out)
	pages, end, err := collectPages(out)
	if err != nil {
		t.Fatal(err)
	}
	if end != int64(len(out)) {
		t.Errorf("scan ended at %d, want %d", end, len(out))
	}

	// Every page's stored CRC must match a recomputation over the page with its
	// checksum field zeroed. A page no packet ends on holds granule position -1, the
	// others the header pages' 0.
	for _, p := range pages {
		want := uint64(0)
		if !slices.ContainsFunc(p.segs, func(lacing byte) bool { return lacing < maxSegments }) {
			want = math.MaxUint64
		}
		if p.granule != want {
			t.Errorf("page seq %d granule = %#x, want %#x", p.seq, p.granule, want)
		}
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

// TestPaginateSequenceAndContinuation checks that sequence numbers increment from the
// start value and only pages continuing a packet set the continued flag.
func TestPaginateSequenceAndContinuation(t *testing.T) {
	out, _ := paginate(1, 5, [][]byte{pattern(70000)}) // one packet across pages
	pages, _, err := collectPages(out)
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

func TestParseRejectsNonOgg(t *testing.T) {
	_, err := parse(context.Background(), Codec{}, core.BytesSource([]byte("not an ogg file at all")), core.DefaultParseOptions())
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want invalid data for non-Ogg input", err)
	}
}

// TestParseManyPagesUncapped checks that the metadata element cap (bits.Limits.MaxElements,
// default 100000) does not apply to audio pages: a long stream has far more than the cap.
func TestParseManyPagesUncapped(t *testing.T) {
	const n = 120000
	m, err := parse(context.Background(), Codec{}, core.BytesSource(manyPageOpus(n)), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse of %d pages: %v", n, err)
	}
	if got := len(m.Native.(*doc).audioPages); got != n {
		t.Errorf("audio pages = %d, want %d", got, n)
	}
}

// scanSeqs returns the sequence numbers of the pages of b, which must be pages back to back.
func scanSeqs(t *testing.T, b []byte) []uint32 {
	t.Helper()
	var seqs []uint32
	for off := 0; off < len(b); off += pageLen(t, b[off:]) {
		seqs = append(seqs, binary.LittleEndian.Uint32(b[off+18:]))
	}
	return seqs
}

// TestPlanContinuesPageNumbering: every rewrite path numbers its pages on from the first page's
// sequence number, wrapping where the 32-bit counter does.
func TestPlanContinuesPageNumbering(t *testing.T) {
	for _, seq0 := range []uint32{0, 100, math.MaxUint32 - 2} {
		for _, c := range []struct {
			name string
			edit func(*core.Media)
		}{
			{"tags", func(m *core.Media) { m.Tags.Set("TITLE", "New") }},
			{"cover spilling onto more header pages", func(m *core.Media) {
				m.Pictures = []core.Picture{{Type: 3, MIME: "image/png", Data: pattern(70000)}}
			}},
			{"output gain", func(m *core.Media) { m.Properties.Tracks[0].OutputGain = -512 }},
			{"output gain and tags", func(m *core.Media) {
				m.Properties.Tracks[0].OutputGain = -512
				m.Tags.Set("TITLE", "New")
			}},
		} {
			src := buildOpusStreamAt(t, seq0, 3, 0)
			base, err := parse(context.Background(), Codec{}, core.BytesSource(src), core.DefaultParseOptions())
			if err != nil {
				t.Fatal(err)
			}
			edited := base.Clone()
			c.edit(edited)
			plan, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions())
			if err != nil {
				t.Fatalf("seq0 %d, %s: %v", seq0, c.name, err)
			}
			for i, seq := range scanSeqs(t, renderPlan(t, src, plan)) {
				if want := seq0 + uint32(i); seq != want {
					t.Fatalf("seq0 %d, %s: page %d has sequence number %d, want %d", seq0, c.name, i, seq, want)
				}
			}
		}
	}
}

// TestRenumberCopiesEachPageBodyWithTheNextPrefix: the renumber path patches eight bytes of each
// audio page and copies what lies between two patches, a page's lacing and body and the next
// page's first 18 bytes, as one segment.
func TestRenumberCopiesEachPageBodyWithTheNextPrefix(t *testing.T) {
	const audioPages = 5
	src := buildOpusStreamAt(t, 0, audioPages, 0)
	base, err := parse(context.Background(), Codec{}, core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	edited := base.Clone()
	edited.Pictures = []core.Picture{{Type: 3, MIME: "image/png", Data: pattern(70000)}}
	plan, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatal(err)
	}
	// Page 0, the header tail, the first audio page's prefix, then a patch and a copy per page.
	if got, want := len(plan.Segments), 3+2*audioPages; got != want {
		t.Errorf("segments = %d, want %d", got, want)
	}
	out := renderPlan(t, src, plan)
	if _, err := parse(context.Background(), Codec{}, core.BytesSource(out), core.DefaultParseOptions()); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	for off := 0; off < len(out); {
		n := pageLen(t, out[off:])
		page := slices.Clone(out[off : off+n])
		want := binary.LittleEndian.Uint32(page[22:26])
		clear(page[22:26])
		if got := bits.OggCRC(page); got != want {
			t.Fatalf("page at %d: checksum %08x, want %08x", off, got, want)
		}
		off += n
	}
}
