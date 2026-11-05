package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/testfile"
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

// partially-specified Limits keeps the default bound for any zero field rather than reading zero as
// "reject everything" (a literal zero MaxAllocBytes would refuse the STREAMINFO allocation).
func TestWithLimitsZeroFieldUsesDefault(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	// Only MaxDepth is set; MaxAllocBytes is zero and must fall back to the default.
	if _, err := wl.Parse(context.Background(), wl.BytesSource(src),
		wl.WithLimits(wl.Limits{MaxDepth: 64})); err != nil {
		t.Errorf("partial Limits{MaxDepth:64} should parse with the default MaxAllocBytes, got: %v", err)
	}
	// Symmetrically, only MaxAllocBytes set keeps the default MaxDepth.
	if _, err := wl.Parse(context.Background(), wl.BytesSource(src),
		wl.WithLimits(wl.Limits{MaxAllocBytes: 1 << 20})); err != nil {
		t.Errorf("partial Limits{MaxAllocBytes:1MiB} should parse with the default MaxDepth, got: %v", err)
	}
}

// A write from a Document whose parse a limit cut short commits and keeps what that parse left
// unread: Musepack chapters past the packet walk's or the chapter run's cap through a title edit,
// and an APEv2 cover past the item cap through a no-op copy.
func TestWriteFromCappedParseCommits(t *testing.T) {
	ctx := context.Background()
	audio := testfile.MPCAudio(64)
	viaSeekTable := testfile.MPCStream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable(),
		testfile.MPCTitled(0, "a"), testfile.MPCTitled(1152, "b"), testfile.MPCTitled(2304, "c"), testfile.MPCEnd())
	apeTag, err := ape.Render([]ape.Item{{Key: "Title", Value: "T"}, {Key: "Artist", Value: "A"}, {Key: "Album", Value: "B"},
		{Key: "Cover Art (Front)", Data: append([]byte("cover.png\x00"), testfile.PNG()...), Flags: 2}}, 2000, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, title string // title is what the edit sets; empty for a no-op
		data        []byte
	}{
		{"chapters past the walk cap", "Capped", testfile.MPCChaptered()},
		{"chapters past the run cap", "Capped", viaSeekTable},
		{"cover past the item cap", "", slices.Concat(readFixture(t, notagsAPE), apeTag)},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, err := wl.Parse(ctx, wl.BytesSource(c.data), wl.WithLimits(wl.Limits{MaxElements: 2}))
			if err != nil {
				t.Fatal(err)
			}
			if !hasWarning(doc, wl.WarnElementCap) {
				t.Fatal("setup: the element cap should cut the parse short")
			}
			ed := doc.Edit()
			if c.title != "" {
				ed.Set(tag.Title, c.title)
			}
			plan, err := ed.Prepare()
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if _, res, err := plan.Execute(ctx, wl.WriteTo(&out, wl.BytesSource(c.data))); err != nil || !res.Committed {
				t.Fatalf("Execute = (committed %v, err %v), want it written", res.Committed, err)
			}
			src, got := mustParseBytes(t, c.data), mustParseBytes(t, out.Bytes())
			if !slices.Equal(got.Chapters(), src.Chapters()) || len(got.Pictures()) != len(src.Pictures()) {
				t.Errorf("output holds %d chapters and %d pictures, want the source's %d and %d",
					len(got.Chapters()), len(got.Pictures()), len(src.Chapters()), len(src.Pictures()))
			}
			if c.title == "" && !bytes.Equal(out.Bytes(), c.data) {
				t.Error("the no-op copy changed the bytes")
			}
			if c.title != "" && got.Fields().Title != c.title {
				t.Errorf("title = %q, want %q", got.Fields().Title, c.title)
			}
		})
	}
}

// withTrailingID3v1 appends a 128-byte ID3v1 tag to FLAC bytes.
func withTrailingID3v1(src []byte) []byte {
	return slices.Concat(src, testfile.ID3v1("Legacy Title", "", "", "", "", 0))
}

// withLeadingID3v2 prepends a minimal empty ID3v2 tag (10-byte header, size 0).
func withLeadingID3v2(src []byte) []byte { return append(testfile.ID3v2(4), src...) }

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
	// Detection looks past the leading ID3v2 to the fLaC marker; a real file lets the strip below
	// save back.
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

func TestPaddingFloorWiring(t *testing.T) {
	src := readFixture(t, sampleFLAC)

	// A floor (Min == Target) grows the region: an edit that would fit the fixture's small existing
	// padding must reserve at least Min instead of reusing the smaller leftover. Min gates the
	// reuse branch.
	floorPlan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Floor").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 200000, Min: 200000, ReuseInPlace: true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := floorPlan.Report().PaddingAfter; got < 200000 {
		t.Fatalf("PaddingAfter under a floor = %d, want >= 200000", got)
	}
	grown := applyToBytes(t, src, floorPlan)

	// A region already past the floor reuses in place: re-editing the grown file under a small floor
	// keeps the large region (Min only floors a grow, it never forces a shrink toward Target).
	reusePlan, err := mustParseBytes(t, grown).Edit().Set(tag.Title, "Reuse").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 1000, Min: 1000, ReuseInPlace: true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := reusePlan.Report().PaddingAfter; got < 100000 {
		t.Errorf("PaddingAfter on reuse = %d, want the large region reused (not shrunk to 1000)", got)
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
