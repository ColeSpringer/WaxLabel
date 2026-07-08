package waxlabel_test

import (
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestValidWritableText exercises the public single-source validator behind both the editor's
// authored-text rejection and the CLI's boundary check: clean text passes, a NUL byte (valid
// UTF-8 U+0000, the case a UTF-8-only check would miss) is rejected, and invalid UTF-8 is rejected -
// both wrapping ErrInvalidData.
func TestValidWritableText(t *testing.T) {
	for _, s := range []string{"", "hello", "Ünîçödé ✓", "multi\nline"} {
		if err := wl.ValidWritableText(s); err != nil {
			t.Errorf("ValidWritableText(%q) = %v, want nil", s, err)
		}
	}
	if err := wl.ValidWritableText("a\x00b"); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("ValidWritableText(NUL) = %v, want ErrInvalidData", err)
	}
	if err := wl.ValidWritableText("a\xffb"); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("ValidWritableText(invalid UTF-8) = %v, want ErrInvalidData", err)
	}
}

// TestWritableTextReason covers the exported bare-phrase helper the CLI boundary reads directly
// (instead of parsing ValidWritableText's error string): clean text yields "", and the NUL and
// invalid-UTF-8 cases yield their exact phrases.
func TestWritableTextReason(t *testing.T) {
	if r := wl.WritableTextReason("clean"); r != "" {
		t.Errorf(`WritableTextReason("clean") = %q, want ""`, r)
	}
	if r := wl.WritableTextReason("a\x00b"); r != "contains a NUL byte" {
		t.Errorf("WritableTextReason(NUL) = %q, want %q", r, "contains a NUL byte")
	}
	if r := wl.WritableTextReason("a\xffb"); r != "contains invalid UTF-8" {
		t.Errorf("WritableTextReason(invalid UTF-8) = %q, want %q", r, "contains invalid UTF-8")
	}
}
