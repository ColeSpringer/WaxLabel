package ape

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// apeWithFooter appends a footer-only APEv2 footer to an item body, writing footerCount into
// the item-count field (which may overstate the body's actual item count for cap tests).
func apeWithFooter(body []byte, footerCount uint32) []byte {
	foot := make([]byte, footerLen)
	copy(foot[0:8], preamble)
	binary.LittleEndian.PutUint32(foot[8:12], 2000)
	binary.LittleEndian.PutUint32(foot[12:16], uint32(len(body)+footerLen))
	binary.LittleEndian.PutUint32(foot[16:20], footerCount)
	binary.LittleEndian.PutUint32(foot[20:24], 0) // footer only, no header
	return append(body, foot...)
}

// buildAPE constructs a minimal footer-only APEv2 tag from text items.
func buildAPE(items map[string]string) []byte {
	var body []byte
	count := 0
	for k, v := range items {
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(v)))
		binary.LittleEndian.PutUint32(hdr[4:8], 0) // text item
		body = append(body, hdr[:]...)
		body = append(body, []byte(k)...)
		body = append(body, 0)
		body = append(body, []byte(v)...)
		count++
	}
	return apeWithFooter(body, uint32(count))
}

func TestParseAPE(t *testing.T) {
	data := buildAPE(map[string]string{"Title": "APE Title", "Artist": "APE Artist"})
	src := core.BytesSource(data)

	tg, ok, err := ParseAt(src, int64(len(data)), 1<<20, 1000)
	if err != nil || !ok {
		t.Fatalf("ParseAt: ok=%v err=%v", ok, err)
	}
	if tg.Version != 2000 {
		t.Errorf("version = %d, want 2000", tg.Version)
	}
	if tg.Offset != 0 || tg.Size != int64(len(data)) {
		t.Errorf("extent = [%d, +%d), want [0, +%d)", tg.Offset, tg.Size, len(data))
	}
	got := map[tag.Key]string{}
	for _, p := range tg.Pairs() {
		got[p.Key] = p.Value
	}
	if got[tag.Title] != "APE Title" || got[tag.Artist] != "APE Artist" {
		t.Errorf("pairs = %v", got)
	}
}

func TestParseAPEAbsent(t *testing.T) {
	src := core.BytesSource([]byte("not an ape tag at all, just some bytes...."))
	if _, ok, _ := ParseAt(src, 40, 1<<20, 1000); ok {
		t.Error("ParseAt should report absence for non-APE data")
	}
}

// buildAPEN builds a footer-only APE tag with n well-formed 1-byte-value text
// items. footerCount is written to the footer and may overstate n for cap and
// short-body tests.
func buildAPEN(n int, footerCount uint32) []byte {
	var body []byte
	for i := range n {
		val := "v"
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(val)))
		binary.LittleEndian.PutUint32(hdr[4:8], 0) // text item
		body = append(body, hdr[:]...)
		body = append(body, []byte(fmt.Sprintf("K%d", i))...)
		body = append(body, 0)
		body = append(body, []byte(val)...)
	}
	return apeWithFooter(body, footerCount)
}

func TestParseAPEItemCapExact(t *testing.T) {
	const capN = 4
	data := buildAPEN(capN, capN) // exactly capN well-formed items
	tg, ok, err := ParseAt(core.BytesSource(data), int64(len(data)), 1<<20, capN)
	if err != nil || !ok {
		t.Fatalf("ParseAt: ok=%v err=%v", ok, err)
	}
	if len(tg.Items) != capN {
		t.Errorf("items = %d, want %d (exactly the cap parses cleanly)", len(tg.Items), capN)
	}
}

func TestParseAPEItemCapTruncates(t *testing.T) {
	const capN = 4
	data := buildAPEN(capN+3, capN+3) // more well-formed items than the cap
	tg, ok, err := ParseAt(core.BytesSource(data), int64(len(data)), 1<<20, capN)
	if err != nil || !ok {
		t.Fatalf("ParseAt: ok=%v err=%v (truncation must not be an error)", ok, err)
	}
	if len(tg.Items) != capN {
		t.Errorf("items = %d, want %d (truncated at the cap)", len(tg.Items), capN)
	}
}

func TestParseAPEItemCapShortBytesBreaksFirst(t *testing.T) {
	// The footer overstates itemCount, but the body holds only three items. Parsing
	// should stop at end-of-body before the cap and keep all present items.
	data := buildAPEN(3, 1_000_000) // footer claims a million items; only three exist
	tg, ok, err := ParseAt(core.BytesSource(data), int64(len(data)), 1<<20, 100)
	if err != nil || !ok {
		t.Fatalf("ParseAt: ok=%v err=%v", ok, err)
	}
	if len(tg.Items) != 3 {
		t.Errorf("items = %d, want 3 (bounded by bytes, not the cap)", len(tg.Items))
	}
}

func TestParseAPEItemHugeSizeRejected(t *testing.T) {
	// An item can claim a value near 2 GiB while supplying only a few bytes. Parsing
	// should reject the item without slicing past the buffer. The overflow panic only
	// exists on 32-bit builds, but the empty decoded list is portable.
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], 0x7FFFFFFF) // claimed value size, far past the bytes
	binary.LittleEndian.PutUint32(hdr[4:8], 0)          // text flags
	var body []byte
	body = append(body, hdr[:]...)
	body = append(body, 'K', 0, 'v', 'v') // key "K" + NUL terminator + two value bytes
	data := apeWithFooter(body, 1)

	tg, ok, err := ParseAt(core.BytesSource(data), int64(len(data)), 1<<20, 1000)
	if err != nil || !ok {
		t.Fatalf("ParseAt: ok=%v err=%v", ok, err)
	}
	if len(tg.Items) != 0 {
		t.Errorf("items = %d, want 0 (an oversized item size must be rejected, not panic)", len(tg.Items))
	}
}
