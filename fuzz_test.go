package waxlabel_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// FuzzParse asserts the parser never panics on arbitrary input (risk #5) and
// that whatever it accepts is internally consistent: a no-op write reproduces
// the input bytes, and re-parsing succeeds. Run with:
//
//	go test -run x -fuzz FuzzParse
func FuzzParse(f *testing.F) {
	// Seed with the real fixtures and a few hand-built malformations.
	for _, p := range []string{sampleFLAC, "testdata/notags.flac"} {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("fLaC"))                                                       // marker only, no blocks
	f.Add([]byte("fLaC\x00\x00\x00\x22"))                                       // STREAMINFO header, no body
	f.Add([]byte("fLaC\x80\xff\xff\xff"))                                       // last block, absurd length
	f.Add(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x0a"), []byte("fLaC")...)) // stray ID3 then truncated
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

		// An edit on accepted input must also round-trip and re-parse.
		plan2, err := doc.Edit().Set(tag.Title, "fuzz").Prepare()
		if err != nil {
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
