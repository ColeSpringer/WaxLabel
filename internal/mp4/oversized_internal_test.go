package mp4

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestCheckItemSizes is the unit test of the write-side oversized-item guard: a covr item
// past the limit reports ErrPictureTooLarge, any other ilst item reports ErrSizeTooLarge, an
// item within the limit passes, and a zero/unset limit falls back to the 256 MiB library ceiling
// (so a normal item still passes rather than the guard rejecting everything or panicking).
func TestCheckItemSizes(t *testing.T) {
	const limit = 1024
	big := make([]byte, 2048)
	small := make([]byte, 16)

	if err := checkItemSizes([]item{{name: atomName("covr"), payload: big}}, limit); !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Errorf("oversized covr: err = %v, want ErrPictureTooLarge", err)
	}
	if err := checkItemSizes([]item{{name: atomName("----"), payload: big}}, limit); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("oversized freeform: err = %v, want ErrSizeTooLarge", err)
	}
	if err := checkItemSizes([]item{{name: atomName("\xa9nam"), payload: small}}, limit); err != nil {
		t.Errorf("within-limit item: err = %v, want nil", err)
	}
	// A zero limit falls back to 256 MiB, so a 2 KiB cover passes.
	if err := checkItemSizes([]item{{name: atomName("covr"), payload: big}}, 0); err != nil {
		t.Errorf("zero limit (256 MiB fallback): a 2 KiB cover should pass, got %v", err)
	}
}

// TestReadPayloadWholeFailsLoudOnOversize is the discriminating read-side regression: a
// node whose declared payload exceeds the cap must fail loudly with ErrSizeTooLarge *before*
// reading or allocating, where the old min(payloadSize, cap) silently truncated to the cap. A
// tiny source proves the size check fires first (no read, no 64 MiB allocation): the old helper
// would instead try to read maxMetaChunk bytes and surface a read error, not ErrSizeTooLarge.
func TestReadPayloadWholeFailsLoudOnOversize(t *testing.T) {
	src := core.BytesSource(make([]byte, 16)) // never actually read: the size check returns first
	// 100 MiB declared payload against the 64 MiB structural cap.
	n := node{name: atomName("covr"), offset: 0, headerLen: 8, size: 100 << 20}
	if _, err := readPayloadWhole(src, n, maxMetaChunk, 256<<20); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("oversized payload: err = %v, want ErrSizeTooLarge (no silent truncation)", err)
	}
	// readPayloadPrefix, by contrast, intentionally reads only the prefix and never fails on a
	// larger atom: a 100 MiB atom still yields its 4-byte prefix.
	src2 := core.BytesSource(make([]byte, 8+4))
	if b, err := readPayloadPrefix(src2, node{offset: 0, headerLen: 8, size: 100 << 20}, 4, 256<<20); err != nil || len(b) != 4 {
		t.Fatalf("prefix read of a large atom: b=%d err=%v, want 4 bytes no error", len(b), err)
	}
}

// TestCheckBuiltItemsFloorsAtParsedItems covers the fix: an item already present at parse
// (read within the parse limit) must not be rejected on write even when the write limit is far
// smaller - checkBuiltItems floors the limit at the largest parsed item. A genuinely new item
// larger than anything the file already held is still rejected.
func TestCheckBuiltItemsFloorsAtParsedItems(t *testing.T) {
	const limit = 100
	parsed := []item{{name: atomName("covr"), payload: make([]byte, 500)}} // a large cover read at parse

	// Re-rendering that same (carried-over) item must pass despite the 100-byte limit.
	carried := []item{{name: atomName("covr"), payload: make([]byte, 500)}}
	if err := checkBuiltItems(carried, parsed, limit); err != nil {
		t.Errorf("a carried-over oversized item must pass (limit floored at the parsed size): %v", err)
	}
	// A newly authored item larger than anything parsed is still rejected.
	bigger := []item{{name: atomName("----"), payload: make([]byte, 800)}}
	if err := checkBuiltItems(bigger, parsed, limit); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("an item larger than every parsed item must still be rejected, got %v", err)
	}
}

// TestPlanRejectsOversizedItem proves the guard is wired into the write path: a custom tag value
// larger than the configured limit renders into an oversized "----" freeform that Plan must
// reject with ErrSizeTooLarge instead of writing an item it could not read back.
func TestPlanRejectsOversizedItem(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/sample.m4a")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base, err := parse(ctx, core.BytesSource(raw), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	edited, err := parse(ctx, core.BytesSource(raw), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse edited: %v", err)
	}
	edited.Tags.Set(tag.Key("HUGEFIELD"), strings.Repeat("x", 4096))

	opts := core.WriteOptions{Limits: bits.Limits{MaxAllocBytes: 1024}}
	if _, err := (Codec{}).Plan(ctx, base, edited, opts); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Plan with an oversized freeform item: err = %v, want ErrSizeTooLarge", err)
	}
}

// TestPlanChapterPathRejectsOversizedItem proves the guard is wired into the *chapter* write path
// too (write_chapters.go), not only the tag path: a chapter edit that also carries an oversized
// tag routes through buildChapterUdta, whose checkItemSizes must reject it with ErrSizeTooLarge.
func TestPlanChapterPathRejectsOversizedItem(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/sample_chapters.m4b")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base, err := parse(ctx, core.BytesSource(raw), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	edited, err := parse(ctx, core.BytesSource(raw), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse edited: %v", err)
	}
	if len(edited.Chapters) == 0 {
		t.Fatal("setup: fixture has no chapters to edit")
	}
	// A chapter change routes through the chapter write path; the oversized tag forces
	// buildChapterUdta's ilst build to produce an item checkItemSizes must reject.
	edited.Chapters[0].Title += " (edited)"
	edited.Tags.Set(tag.Key("HUGEFIELD"), strings.Repeat("x", 4096))

	opts := core.WriteOptions{Limits: bits.Limits{MaxAllocBytes: 1024}}
	if _, err := (Codec{}).Plan(ctx, base, edited, opts); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("chapter-path Plan with an oversized item: err = %v, want ErrSizeTooLarge", err)
	}
}

// mkMP4WithUdtaMeta builds a minimal parseable MP4 - ftyp, moov(udta(meta)), mdat - wrapping the
// given raw meta box (header included), so a test can exercise parse's moov.udta.meta gap check
// against a specific meta shape and, for an editable shape, round-trip a tag edit through it.
func mkMP4WithUdtaMeta(meta []byte) []byte {
	udta := renderAtom(atomName("udta"), meta)
	moov := renderAtom(atomName("moov"), udta)
	ftyp := renderAtom(atomName("ftyp"), []byte("M4A \x00\x00\x00\x00M4A mp42"))
	mdat := renderAtom(atomName("mdat"), []byte("audiodata"))
	return slices.Concat(ftyp, moov, mdat)
}

// TestParseRejectsUndersizedMetaGap covers the fix: a moov.udta.meta with a gap between where
// its children end and its own end corrupts a create-ilst edit (buildCreated appends the new ilst
// at meta.end(), but a re-parse resolves the first child earlier, so the ilst lands misaligned).
// walkAtoms tolerates an all-zero gap (the udta-terminator rule), so parse must reject it here.
// Both the bare (9-11 byte) and FullBox-with-zero-pad (13-15 byte) shapes must be caught; an empty
// bare meta (size 8), an empty FullBox meta (size 12), and a meta whose hdlr child tiles exactly to
// its end must all still parse cleanly.
func TestParseRejectsUndersizedMetaGap(t *testing.T) {
	ctx := context.Background()
	reject := map[string][]byte{
		"bare gap size 9":     renderAtom(atomName("meta"), make([]byte, 1)),
		"bare gap size 10":    renderAtom(atomName("meta"), make([]byte, 2)),
		"bare gap size 11":    renderAtom(atomName("meta"), make([]byte, 3)),
		"fullbox gap size 13": renderFullBox(atomName("meta"), make([]byte, 1)),
		"fullbox gap size 14": renderFullBox(atomName("meta"), make([]byte, 2)),
		"fullbox gap size 15": renderFullBox(atomName("meta"), make([]byte, 3)),
	}
	for name, meta := range reject {
		_, err := parse(ctx, core.BytesSource(mkMP4WithUdtaMeta(meta)), core.ParseOptions{})
		if !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("%s: parse err = %v, want ErrInvalidData", name, err)
		}
	}
	accept := map[string][]byte{
		"empty bare meta size 8":       renderAtom(atomName("meta"), nil),
		"empty fullbox meta size 12":   renderFullBox(atomName("meta"), nil),
		"meta with hdlr tiling to end": renderFullBox(atomName("meta"), hdlrAtom()),
	}
	for name, meta := range accept {
		if _, err := parse(ctx, core.BytesSource(mkMP4WithUdtaMeta(meta)), core.ParseOptions{}); err != nil {
			t.Errorf("%s: parse err = %v, want nil", name, err)
		}
	}
}

// TestEmptyMetaRoundTripsTagEdit locks in the lossless empty-meta upgrade the gap check leaves
// intact: a size-8 empty meta (no ilst, no gap) still accepts a tag edit - the create-ilst path
// inserts an ilst inside the existing meta - and the written file re-parses with the tag present.
func TestEmptyMetaRoundTripsTagEdit(t *testing.T) {
	ctx := context.Background()
	raw := mkMP4WithUdtaMeta(renderAtom(atomName("meta"), nil)) // size-8 empty bare meta
	base, err := parse(ctx, core.BytesSource(raw), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	edited, err := parse(ctx, core.BytesSource(raw), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse edited: %v", err)
	}
	edited.Tags.Set(tag.Title, "Round Trip")

	plan, err := (Codec{}).Plan(ctx, base, edited, core.WriteOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var buf bytes.Buffer
	if _, err := bits.Write(ctx, &buf, core.BytesSource(raw), plan.Segments, nil); err != nil {
		t.Fatalf("render plan: %v", err)
	}
	reparsed, err := parse(ctx, core.BytesSource(buf.Bytes()), core.ParseOptions{})
	if err != nil {
		t.Fatalf("re-parse output: %v", err)
	}
	if v, ok := reparsed.Tags.Get(tag.Title); !ok || len(v) != 1 || v[0] != "Round Trip" {
		t.Errorf("re-parsed TITLE = %v (ok=%v), want [\"Round Trip\"]", v, ok)
	}
}
