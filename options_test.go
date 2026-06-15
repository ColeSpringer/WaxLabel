package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

func TestWithLimitsBoundsAllocation(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	// A limit smaller than the STREAMINFO body must be refused, not allocated.
	_, err := wl.Parse(context.Background(), wl.BytesSource(src),
		wl.WithLimits(wl.Limits{MaxAllocBytes: 20, MaxDepth: 64}))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("err = %v, want ErrSizeTooLarge", err)
	}
}

// withTrailingID3v1 appends a 128-byte ID3v1 tag to FLAC bytes.
func withTrailingID3v1(src []byte) []byte {
	id3 := make([]byte, 128)
	copy(id3, "TAG")
	copy(id3[3:], "Legacy Title")
	return append(append([]byte{}, src...), id3...)
}

// withLeadingID3v2 prepends a minimal empty ID3v2 tag (10-byte header, size 0).
func withLeadingID3v2(src []byte) []byte {
	hdr := []byte{'I', 'D', '3', 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	return append(hdr, src...)
}

func TestLegacyTrailingID3v1(t *testing.T) {
	ctx := context.Background()
	src := withTrailingID3v1(readFixture(t, sampleFLAC))

	doc, err := wl.Parse(ctx, wl.BytesSource(src))
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(doc, wl.WarnTrailingID3v1) {
		t.Error("expected a trailing-ID3v1 warning")
	}

	// Default policy preserves it: a no-op write keeps the bytes.
	t.Run("preserve", func(t *testing.T) {
		plan, _ := doc.Edit().Prepare()
		out := applyToBytes(t, src, plan)
		if !bytes.HasSuffix(out, src[len(src)-128:]) {
			t.Error("default policy should preserve trailing ID3v1")
		}
	})

	// Strip policy removes it.
	t.Run("strip", func(t *testing.T) {
		plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
		if err != nil {
			t.Fatal(err)
		}
		if plan.IsNoOp() {
			t.Fatal("stripping a present legacy tag is not a no-op")
		}
		out := applyToBytes(t, src, plan)
		if bytes.Contains(out[len(out)-128:], []byte("TAG")) {
			t.Error("strip policy should remove trailing ID3v1")
		}
		// Audio and tags still intact.
		re := mustParseBytes(t, out)
		if re.Fields().Title != "Original Title" {
			t.Errorf("tags lost after strip: %q", re.Fields().Title)
		}
	})
}

func TestLegacyLeadingID3v2(t *testing.T) {
	ctx := context.Background()
	src := withLeadingID3v2(readFixture(t, sampleFLAC))
	// A leading-ID3 FLAC is detected by its .flac extension (the fLaC marker is
	// past the sniff window, and claiming "ID3" would misroute MP3), so write it
	// to a real file and use ParseFile.
	path := writeTempFile(t, "lead.flac", src)

	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(doc, wl.WarnStrayLeadingID3) {
		t.Error("expected a stray-leading-ID3 warning")
	}
	if doc.Fields().Title != "Original Title" {
		t.Errorf("Title behind leading ID3 = %q", doc.Fields().Title)
	}

	plan, _ := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	out := readFixture(t, path)
	if !bytes.HasPrefix(out, []byte("fLaC")) {
		t.Error("strip policy should remove the leading ID3v2 (output should start with fLaC)")
	}
}

func TestWithPaddingAndReport(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().Set(tag.Title, "Padded").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 1000, Max: 1 << 20}))
	if err != nil {
		t.Fatal(err)
	}
	rep := plan.Report()
	if rep.PaddingAfter != 1000 {
		t.Errorf("PaddingAfter = %d, want 1000", rep.PaddingAfter)
	}
	if rep.NoOp || len(rep.Operations) == 0 {
		t.Errorf("report should describe a non-no-op write: %+v", rep)
	}
	if rep.BytesBefore == 0 || rep.BytesAfter == 0 {
		t.Errorf("report sizes unset: %+v", rep)
	}
}

func TestMinimalPresetStripsAndShrinks(t *testing.T) {
	src := withTrailingID3v1(readFixture(t, sampleFLAC))
	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().Set(tag.Title, "Min").Prepare(wl.Minimal)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Report().PaddingAfter; got != 0 {
		t.Errorf("Minimal preset PaddingAfter = %d, want 0", got)
	}
	out := applyToBytes(t, src, plan)
	if bytes.Contains(out[len(out)-128:], []byte("TAG")) {
		t.Error("Minimal preset should strip trailing ID3v1")
	}
}

func hasWarning(doc *wl.Document, code wl.WarningCode) bool {
	for _, w := range doc.Warnings() {
		if w.Code == code {
			return true
		}
	}
	return false
}
