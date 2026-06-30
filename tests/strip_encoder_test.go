package waxlabel_test

import (
	"context"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// flacWithVendor builds a minimal FLAC whose Vorbis comment block carries the given vendor
// string and comments.
func flacWithVendor(vendor string, entries ...string) []byte {
	le := func(n int) []byte { return []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)} }
	vc := append(le(len(vendor)), vendor...)
	vc = append(vc, le(len(entries))...)
	for _, e := range entries {
		vc = append(vc, le(len(e))...)
		vc = append(vc, e...)
	}
	out := []byte("fLaC")
	out = append(out, flacBlock(0, false, validStreamInfo())...)
	out = append(out, flacBlock(4, false, vc)...)
	out = append(out, flacBlock(1, true, make([]byte, 4))...)
	return append(out, 0xFF, 0xF8)
}

// hasInheritedEncoder reports whether a document's lint flags an inherited-encoder stamp.
func hasInheritedEncoder(doc *wl.Document) bool {
	for _, f := range doc.Lint() {
		if f.Code == "inherited-encoder" {
			return true
		}
	}
	return false
}

// TestStripEncoderNeutralizesFlacVendor checks that --strip-encoder rewrites a
// transcoder-stamped FLAC vendor even when no ENCODER comment is present. The returned
// document and a fresh parse of the written bytes should both lint cleanly.
func TestStripEncoderNeutralizesFlacVendor(t *testing.T) {
	data := flacWithVendor("Lavf58.76.100", "TITLE=Song")
	src := mustParseBytes(t, data)
	if !hasInheritedEncoder(src) {
		t.Fatal("setup: a Lavf-vendor FLAC should flag inherited-encoder")
	}

	plan, err := src.Edit().Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("--strip-encoder on a transcoded-but-otherwise-clean FLAC must not be a no-op")
	}
	if !slices.Contains(plan.Report().Operations, "vendor stamp neutralized") {
		t.Errorf("Operations missing 'vendor stamp neutralized': %v", plan.Report().Operations)
	}

	// The returned in-memory document's Lint is based on the neutralized vendor.
	var w writerTo
	resDoc, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	if hasInheritedEncoder(resDoc) {
		t.Error("the returned document still flags inherited-encoder in-memory")
	}
	// A fresh parse of the written bytes is clean too (the vendor is neutralized on disk).
	if hasInheritedEncoder(mustParseBytes(t, w.b)) {
		t.Error("re-parsed FLAC still flags inherited-encoder after --strip-encoder")
	}
	// The real tag is untouched by the strip.
	if v, _ := mustParseBytes(t, w.b).Get(tag.Title); len(v) != 1 || v[0] != "Song" {
		t.Errorf("TITLE = %v, want [Song] (strip must not disturb tags)", v)
	}
}
