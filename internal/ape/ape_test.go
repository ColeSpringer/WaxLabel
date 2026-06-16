package ape

import (
	"encoding/binary"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

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
	tagSize := len(body) + footerLen
	foot := make([]byte, footerLen)
	copy(foot[0:8], preamble)
	binary.LittleEndian.PutUint32(foot[8:12], 2000)
	binary.LittleEndian.PutUint32(foot[12:16], uint32(tagSize))
	binary.LittleEndian.PutUint32(foot[16:20], uint32(count))
	binary.LittleEndian.PutUint32(foot[20:24], 0) // footer only, no header
	return append(body, foot...)
}

func TestParseAPE(t *testing.T) {
	data := buildAPE(map[string]string{"Title": "APE Title", "Artist": "APE Artist"})
	src := core.BytesSource(data)

	tg, ok, err := ParseAt(src, int64(len(data)), 1<<20)
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
	if _, ok, _ := ParseAt(src, 40, 1<<20); ok {
		t.Error("ParseAt should report absence for non-APE data")
	}
}
