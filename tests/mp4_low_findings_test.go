package waxlabel_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestMP4CompilationCoercionWarns verifies that COMPILATION is a single boolean byte (cpil), so a
// non-boolean value is silently coerced to false. The write must surface a value-dropped
// warning naming the key rather than losing the user's intent at exit 0; a recognized
// boolean spelling stores faithfully and must not warn.
func TestMP4CompilationCoercionWarns(t *testing.T) {
	base := mp4Tagged(mp4Text("\xa9nam", "T"))

	hasDropped := func(p *wl.Plan) bool {
		for _, w := range p.Report().Warnings {
			if w.Code == wl.WarnValueDropped && slices.Contains(w.Keys, tag.Compilation) {
				return true
			}
		}
		return false
	}

	for _, v := range []string{"2", "maybe"} {
		p, err := mustParseBytes(t, base).Edit().Set(tag.Compilation, v).Prepare()
		if err != nil {
			t.Fatalf("COMPILATION=%q: %v", v, err)
		}
		if !hasDropped(p) {
			t.Errorf("non-boolean COMPILATION=%q must warn value-dropped; got %v", v, p.Report().Warnings)
		}
	}
	for _, v := range []string{"1", "true", "0", "no"} {
		p, err := mustParseBytes(t, base).Edit().Set(tag.Compilation, v).Prepare()
		if err != nil {
			t.Fatalf("COMPILATION=%q: %v", v, err)
		}
		if hasDropped(p) {
			t.Errorf("recognized boolean COMPILATION=%q must not warn value-dropped; got %v", v, p.Report().Warnings)
		}
	}
}

// TestMP4TruncatedMdatOverrunsTrailingMoov verifies that a final mdat whose declared size runs
// past EOF is clamped, swallowing whatever follows it. When a moov sits after such an
// mdat the parser never sees it, so the failure must be reported as truncation (the real
// cause) rather than the misleading "no moov box".
func TestMP4TruncatedMdatOverrunsTrailingMoov(t *testing.T) {
	ftyp := mp4Ftyp()
	moov := mp4Moov(nil, 0) // valid, but it will be swallowed by the over-declared mdat
	content := []byte{0xA7, 0xA7}
	// Declare the mdat larger than the bytes that actually remain (content + moov),
	// so it overruns EOF and the trailing moov falls inside its clamped extent.
	declared := 8 + len(content) + len(moov) + 100
	mdat := append(mp4be32(declared), []byte("mdat")...)
	mdat = append(mdat, content...)
	data := slices.Concat(ftyp, mdat, moov)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if err == nil {
		t.Fatal("expected a parse error for a truncated mdat overrunning the moov")
	}
	if !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "truncat") {
		t.Errorf("error = %v, want a truncation diagnostic (not a bare 'no moov box')", err)
	}
}
