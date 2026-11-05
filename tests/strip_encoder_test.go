package waxlabel_test

import (
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// the inherited-encoder finding also fires on a bare transcoder vendor string, so lint --fix must
// not clear a clean, user-set ENCODER tag as collateral.
func TestPlanLintFixPreservesCleanEncoder(t *testing.T) {
	data := testfile.TaggedFLAC("Lavf58.76.100", "ENCODER=MyTagger 1.0", "TITLE=Song")
	doc := mustParseBytes(t, data)
	if !hasInheritedEncoder(doc) {
		t.Fatal("setup: a Lavf-vendor FLAC should flag inherited-encoder")
	}
	fix := doc.PlanLintFix()
	// The clean ENCODER (not a transcoder stamp) must not be cleared.
	if fix.Patch.Touches(tag.Encoder) {
		t.Errorf("PlanLintFix cleared a clean ENCODER as collateral; patch keys = %v", fix.Patch.Keys())
	}
	plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if v, _ := re.Get(tag.Encoder); len(v) != 1 || v[0] != "MyTagger 1.0" {
		t.Errorf("clean ENCODER = %v, want [MyTagger 1.0] (must survive lint --fix)", v)
	}
	if hasInheritedEncoder(re) {
		t.Error("re-lint after the fix still flags inherited-encoder: the Lavf vendor was not neutralized")
	}
}

// companion: when the ENCODER value is a transcoder stamp, lint --fix still clears it (the gate
// reuses the linter's own noise test, so it can never disagree with the finding).
func TestPlanLintFixClearsStampEncoder(t *testing.T) {
	data := testfile.TaggedFLAC("reference libFLAC", "ENCODER=Lavf58.76.100", "TITLE=Song")
	doc := mustParseBytes(t, data)
	if !hasInheritedEncoder(doc) {
		t.Fatal("setup: a Lavf ENCODER comment should flag inherited-encoder")
	}
	if fix := doc.PlanLintFix(); !fix.Patch.Touches(tag.Encoder) {
		t.Errorf("PlanLintFix must clear a transcoder-stamp ENCODER; patch keys = %v", fix.Patch.Keys())
	}
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

// when ENCODER carries several values and a later one is a transcoder stamp, lint --fix removes
// only the stamp value, so a re-lint of the saved file is clean and the clean value survives.
func TestPlanLintFixMultiValueEncoderRemovesStamp(t *testing.T) {
	// A clean ENCODER value first, then a Lavf stamp (a FLAC with two ENCODER comments); the
	// vendor is a non-Lavf string so only the ENCODER comment carries the stamp.
	data := testfile.TaggedFLAC("reference libFLAC", "ENCODER=MyTagger 1.0", "ENCODER=Lavf58.76.100", "TITLE=Song")
	doc := mustParseBytes(t, data)
	if !hasInheritedEncoder(doc) {
		t.Fatal("setup: a stamped ENCODER value should flag inherited-encoder")
	}
	plan, err := doc.Edit().Apply(doc.PlanLintFix().Patch).Prepare(doc.PlanLintFix().Options...)
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if v, _ := re.Get(tag.Encoder); len(v) != 1 || v[0] != "MyTagger 1.0" {
		t.Errorf("ENCODER after fix = %v, want [MyTagger 1.0] (only the stamp value removed)", v)
	}
	if hasInheritedEncoder(re) {
		t.Error("re-lint after the fix still flags inherited-encoder: the stamp ENCODER value was not removed")
	}
}

// --strip-encoder rewrites a transcoder-stamped FLAC vendor even when no ENCODER comment is
// present, so the written file lints cleanly.
func TestStripEncoderNeutralizesFlacVendor(t *testing.T) {
	data := testfile.TaggedFLAC("Lavf58.76.100", "TITLE=Song")
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

	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if hasInheritedEncoder(re) {
		t.Error("re-parsed FLAC still flags inherited-encoder after --strip-encoder")
	}
	// The tag is untouched by the strip.
	if v, _ := re.Get(tag.Title); len(v) != 1 || v[0] != "Song" {
		t.Errorf("TITLE = %v, want [Song] (strip must not disturb tags)", v)
	}
}
