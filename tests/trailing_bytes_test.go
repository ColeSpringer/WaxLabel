package waxlabel_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// junk is the appended region the trailing-bytes tests look for.
var junk = []byte("TRAILINGJUNK")

// TestTrailingBytesReported checks that bytes belonging to no chunk or page are surfaced
// rather than silently preserved. Byte preservation was already right; the silence was the
// finding, since lint advertises the issues a tagger would want to see.
func TestTrailingBytesReported(t *testing.T) {
	for _, tc := range []struct{ name, fixture string }{
		{"wav", "sample.wav"},
		{"aiff", "sample.aiff"},
		{"ogg", "sample.ogg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clean, err := os.ReadFile("../testdata/" + tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			if lintHasCode(mustParseBytes(t, clean), "trailing-bytes") {
				t.Fatal("setup: the pristine fixture should have no trailing region")
			}
			data := append(bytes.Clone(clean), junk...)
			doc := mustParseBytes(t, data)
			if !lintHasCode(doc, "trailing-bytes") {
				t.Errorf("appended bytes went unreported; lint = %v", doc.Lint())
			}
			if !hasWarning(doc, wl.WarnTrailingBytes) {
				t.Error("dump should carry the same warning lint promotes")
			}
			// The point of the code: preserved verbatim, and still reported afterwards.
			plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, data, plan)
			if !bytes.HasSuffix(out, junk) {
				t.Error("the trailing region did not survive an edit byte for byte")
			}
			if !lintHasCode(mustParseBytes(t, out), "trailing-bytes") {
				t.Error("the rewritten file no longer reports its trailing region")
			}
		})
	}
}

// TestTrailingBytesInsideContainer covers the other RIFF region: bytes after the last
// chunk but still inside the declared container size, which the writer keeps counted in
// the recomputed size rather than moving outside it.
func TestTrailingBytesInsideContainer(t *testing.T) {
	body := append([]byte("WAVE"), wavFmtPCM()...)
	body = append(body, wavData(400)...)
	body = append(body, junk...)
	data := append(append([]byte("RIFF"), wavLE32(len(body))...), body...)

	doc := mustParseBytes(t, data)
	if !lintHasCode(doc, "trailing-bytes") {
		t.Fatalf("an in-container trailing region went unreported; lint = %v", doc.Lint())
	}
	plan, err := doc.Edit().Set(tag.Title, "T").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, junk) {
		t.Error("the in-container trailing region did not survive an edit")
	}
}

// TestTrailingBytesNotStrictEscalating pins the boundary --strict draws: trailing bytes
// describe the state of the file the user was handed, not something the edit lost, so they
// must not fail a write the way an edit-time loss does.
func TestTrailingBytesNotStrictEscalating(t *testing.T) {
	clean, err := os.ReadFile(notagsWAV)
	if err != nil {
		t.Fatal(err)
	}
	data := append(bytes.Clone(clean), junk...)
	if _, err := mustParseBytes(t, data).Edit().Set(tag.Title, "T").Prepare(); err != nil {
		t.Errorf("preparing an edit on a file with trailing bytes failed: %v", err)
	}
}

// TestMP4AppendedJunkReported: an MP4 walk clamps a final top-level atom whose declared size
// overruns EOF, which is exactly what bytes appended after the last atom look like - the
// first four read as an enormous size and the remainder becomes one phantom atom. Absorbing
// it in silence is the condition this reports, under the same code RIFF uses for a clamped
// chunk. mdat and moov are excluded: they carry their own truncated-audio warnings.
func TestMP4AppendedJunkReported(t *testing.T) {
	clean, err := os.ReadFile("../testdata/sample.m4a")
	if err != nil {
		t.Fatal(err)
	}
	if lintHasCode(mustParseBytes(t, clean), "oversized-chunk") {
		t.Fatal("setup: the pristine fixture should tile cleanly")
	}
	data := append(bytes.Clone(clean), junk...)
	doc := mustParseBytes(t, data)
	if !lintHasCode(doc, "oversized-chunk") {
		t.Fatalf("appended bytes went unreported; lint = %v", doc.Lint())
	}
	// Preserved verbatim across an edit, and still reported afterwards.
	plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.HasSuffix(out, junk) {
		t.Error("the appended region did not survive an edit byte for byte")
	}
	if !lintHasCode(mustParseBytes(t, out), "oversized-chunk") {
		t.Error("the rewritten file no longer reports it")
	}
}

// TestInContainerID3v1NamedNotCalledJunk: the RIFF/IFF walk stops on a well-formed ID3v1
// trailer by shape, so it knows exactly what those 128 bytes are. Reporting them as bytes
// that belong to nothing would be false. The code stays trailing-bytes rather than
// trailing-id3v1: that one drives PlanLintFix's legacy strip, which on WAV means
// "consolidate LIST/INFO into the id3 chunk" and would restructure the file without
// removing the trailer the finding is about.
func TestInContainerID3v1NamedNotCalledJunk(t *testing.T) {
	body := append([]byte("WAVE"), wavFmtPCM()...)
	body = append(body, wavData(400)...)
	body = append(body, id3v1("Title", "", "", "2021", "", 255)...)
	data := append(append([]byte("RIFF"), wavLE32(len(body))...), body...)

	doc := mustParseBytes(t, data)
	var msg string
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnTrailingBytes {
			msg = w.Message
		}
	}
	if msg == "" {
		t.Fatalf("the trailer went unreported: %v", doc.Warnings())
	}
	if !strings.Contains(msg, "ID3v1") {
		t.Errorf("message = %q, want it to name the tag rather than call it unattributed bytes", msg)
	}
	// lint --fix must not restructure the file chasing a trailer it cannot remove.
	fix := doc.PlanLintFix()
	if len(fix.Options) != 0 || fix.Patch.Keys() != nil {
		t.Errorf("lint --fix proposed work for an unremovable trailer: %+v", fix)
	}
}
