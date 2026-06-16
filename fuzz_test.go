package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// FuzzParse asserts the parser never panics on arbitrary input (risk #5) and
// that whatever it accepts is internally consistent: a no-op write reproduces
// the input bytes, and re-parsing succeeds. Run with:
//
//	go test -run x -fuzz FuzzParse
func FuzzParse(f *testing.F) {
	// Seed with the real fixtures (FLAC and Ogg Vorbis/Opus) and a few hand-built
	// malformations, including Ogg page edge cases (risk #1: multi-page packets,
	// truncated pages).
	for _, p := range []string{
		sampleFLAC, "testdata/notags.flac", sampleOgg, sampleOpus, notagsOgg, "testdata/notags.opus",
		sampleMP3, sampleMP324, notagsMP3,
	} {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("ID3\x03\x00\x00\x00\x00\x00\x7f"))                                                                  // ID3v2.3 header claiming 127 body bytes it lacks
	f.Add(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x10"), []byte("TIT2")...))                                       // truncated v2.4 frame
	f.Add([]byte("\xff\xfb\x90\x00"))                                                                                 // bare MPEG-1 Layer 3 frame header, no body
	f.Add([]byte("fLaC"))                                                                                             // marker only, no blocks
	f.Add([]byte("fLaC\x00\x00\x00\x22"))                                                                             // STREAMINFO header, no body
	f.Add([]byte("fLaC\x80\xff\xff\xff"))                                                                             // last block, absurd length
	f.Add(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x0a"), []byte("fLaC")...))                                       // stray ID3 then truncated
	f.Add([]byte("OggS\x00\x02"))                                                                                     // Ogg capture pattern, truncated header
	f.Add([]byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\xff")) // page header claiming a 255-byte body it lacks
	f.Add([]byte{})

	ctx := context.Background()
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := wl.Parse(ctx, wl.BytesSource(data))
		if err != nil {
			return // rejecting malformed input is fine; panicking is not
		}

		// Accessors on a valid document must not panic.
		_ = doc.Fields()
		_ = doc.Properties()
		_ = doc.Pictures()
		_ = doc.Warnings()
		_ = doc.Inspect()

		// A no-op write must reproduce the exact input bytes.
		plan, err := doc.Edit().Prepare()
		if err != nil {
			t.Fatalf("prepare on a parsed doc failed: %v", err)
		}
		var out bytes.Buffer
		if _, _, err := plan.Execute(ctx, wl.WriteTo(&out, wl.BytesSource(data))); err != nil {
			t.Fatalf("no-op write failed: %v", err)
		}
		if plan.IsNoOp() && !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("no-op write changed bytes: in=%d out=%d", len(data), out.Len())
		}

		// An edit on accepted input must round-trip and re-parse. A codec may
		// legitimately refuse to rewrite some shapes — a chained Ogg stream
		// (ErrChainedStream) or a non-page-aligned / oversized layout
		// (ErrInvalidData) — but any other error from a parsed document is a
		// regression, so fail rather than silently accepting it.
		plan2, err := doc.Edit().Set(tag.Title, "fuzz").Prepare()
		if err != nil {
			if errors.Is(err, waxerr.ErrChainedStream) || errors.Is(err, waxerr.ErrInvalidData) {
				return
			}
			t.Fatalf("edit prepare failed: %v", err)
		}
		var out2 bytes.Buffer
		if _, _, err := plan2.Execute(ctx, wl.WriteTo(&out2, wl.BytesSource(data))); err != nil {
			t.Fatalf("edit write failed: %v", err)
		}
		if _, err := wl.Parse(ctx, wl.BytesSource(out2.Bytes())); err != nil {
			t.Fatalf("re-parse of edited output failed: %v", err)
		}
	})
}
