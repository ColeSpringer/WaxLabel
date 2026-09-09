package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"
)

// TestIgnoredV22TagIsOpaqueToLintFix: a stray leading ID3v2.2 tag ignored for its
// compression flag projects no values at all, which without care reads as "provably
// redundant" and invites lint --fix to strip it. Nothing about an unreadable tag can be
// shown to be redundant, so the container stays and the fix declines it.
func TestIgnoredV22TagIsOpaqueToLintFix(t *testing.T) {
	tagBytes := id3v2(2, frame22("TT2", []byte("\x00Compressed")))
	tagBytes[5] = 0x40
	file := slices.Concat(tagBytes, readFixture(t, notagsFLAC))
	doc := mustParseBytes(t, file)
	if !doc.HasOpaqueLegacyContent() {
		t.Error("an ignored ID3v2.2 tag holds content no strip can prove redundant")
	}
	for _, f := range doc.Lint() {
		if f.Code == "stray-leading-id3" && f.Fixable {
			t.Error("a stray leading tag that was ignored whole must not be marked fixable")
		}
	}
	if fix := doc.PlanLintFix(); len(fix.Options) != 0 {
		t.Errorf("PlanLintFix must not strip it: %+v", fix)
	}
	out := applyLintFix(t, file)
	if !bytes.Contains(out, []byte("Compressed")) {
		t.Error("lint --fix destroyed the ignored tag")
	}
}
