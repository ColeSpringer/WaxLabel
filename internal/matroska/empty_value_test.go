package matroska

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestManagedTagWithoutTagStringStaysNative guards the A1 empty-value change against
// over-reach: a managed SimpleTag (its name maps to a canonical key) that carries only
// a TagBinary or only nested sub-tags - no TagString of its own - must stay native-only,
// not surface as a spurious empty canonical value (ARTIST=[""]). parseSimpleTag leaves
// value=="" for both an empty TagString and an absent one, so the projection gates on
// the hasValue presence bit, not the empty string.
func TestManagedTagWithoutTagStringStaysNative(t *testing.T) {
	const limit = int64(1 << 20)
	cases := []struct {
		name    string
		payload []byte
	}{
		{"binary only", append(stringElement(idTagName, "ARTIST"), encElement(idTagBinary, []byte{1, 2, 3})...)},
		{"nested only", append(stringElement(idTagName, "ARTIST"),
			encElement(idSimpleTag, append(stringElement(idTagName, "SUBKEY"), stringElement(idTagString, "x")...))...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := core.BytesSource(encElement(idSimpleTag, c.payload))
			root, ok := readElement(src, 0, src.Size(), limit)
			if !ok {
				t.Fatal("readElement failed")
			}
			st, err := parseSimpleTag(src, root, bits.NewDepth(8), limit)
			if err != nil {
				t.Fatalf("parseSimpleTag: %v", err)
			}
			if st.hasValue {
				t.Errorf("hasValue = true, want false (the SimpleTag has no TagString of its own)")
			}
			// project() must skip it: a doc with this single tag in an album group must
			// yield no canonical ARTIST.
			d := &doc{groups: []tagGroup{{scope: core.ScopeAlbum, tags: []simpleTag{st}}}}
			if ts, _ := project(d); ts.Has(tag.Artist) {
				t.Errorf("binary/nested-only ARTIST projected into the canonical set; want native-only")
			}
		})
	}
}

// TestEmptySimpleTagRoundTrip pins A1's SimpleTag half: the writer emits a present
// empty value ([""], what `set ARTIST=` produces) as a real zero-length SimpleTag,
// and the parser reads it back as a present empty value - not absent. Before the fix
// the writer skipped it, making `set ARTIST=` indistinguishable from `--clear ARTIST`.
func TestEmptySimpleTagRoundTrip(t *testing.T) {
	const limit = int64(1 << 20)
	b := simpleTagBytes("ARTIST", "") // writer output for a present-empty value
	src := core.BytesSource(b)
	root, ok := readElement(src, 0, src.Size(), limit)
	if !ok || root.id != idSimpleTag {
		t.Fatalf("readElement: ok=%v id=%#x, want a SimpleTag", ok, root.id)
	}
	st, err := parseSimpleTag(src, root, bits.NewDepth(8), limit)
	if err != nil {
		t.Fatalf("parseSimpleTag: %v", err)
	}
	if st.name != "ARTIST" || st.value != "" {
		t.Fatalf("round-trip = name %q value %q, want ARTIST/empty", st.name, st.value)
	}
	got := projectTag(st.name, st.value, core.ScopeAlbum)
	if len(got) != 1 || got[0].key != tag.Artist || got[0].value != "" {
		t.Fatalf("projectTag = %+v, want one Artist=\"\"", got)
	}
}

// TestBuildAlbumGroupKeepsEmptyValue exercises the changed writer function directly:
// buildAlbumGroup must emit a present-empty canonical value as a SimpleTag rather than
// drop the whole group (the regression that made `set ARTIST=` == `--clear ARTIST`).
func TestBuildAlbumGroupKeepsEmptyValue(t *testing.T) {
	const limit = int64(1 << 20)
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	edited.Set(tag.Artist, "") // set ARTIST=
	_, gb := buildAlbumGroup(nil, base, edited, nil)
	if gb == nil {
		t.Fatal("buildAlbumGroup dropped a present-empty ARTIST (set KEY= must not equal --clear KEY)")
	}
	src := core.BytesSource(gb)
	root, ok := readElement(src, 0, src.Size(), limit)
	if !ok || root.id != idTag {
		t.Fatalf("not a Tag element: ok=%v id=%#x", ok, root.id)
	}
	g, err := parseTag(src, root, bits.NewDepth(8), limit)
	if err != nil {
		t.Fatalf("parseTag: %v", err)
	}
	var got []scopedContribution
	for _, st := range g.tags {
		got = append(got, projectTag(st.name, st.value, core.ScopeAlbum)...)
	}
	if len(got) != 1 || got[0].key != tag.Artist || got[0].value != "" {
		t.Fatalf("projected %+v, want one Artist=\"\"", got)
	}
}

// TestRenderInfoTitlePresence pins A1's Info.Title half: a present-but-empty title
// (`set TITLE=`) writes a zero-length <Title> that parses back as present, while an
// absent title (`--clear TITLE`) removes the element - the two stay distinguishable.
func TestRenderInfoTitlePresence(t *testing.T) {
	const limit = int64(1 << 20)

	// Case 1: an Info with no Title (the insert path, titleOff < 0).
	noTitle := infoFromRaw(encElement(idInfo, uintElement(idTimestampScl, 1000000)), 0, bits.NewDepth(8), limit)
	if noTitle == nil {
		t.Fatal("infoFromRaw returned nil")
	}
	// present=true, title="" inserts a zero-length Title that parses back as present "".
	if d := parseInfoElement(t, mustRender(t, noTitle, "", true), limit); !d.hasSegTitle || d.segTitle != "" {
		t.Fatalf("insert empty title: hasSegTitle=%v segTitle=%q, want true/empty", d.hasSegTitle, d.segTitle)
	}
	// present=false inserts nothing (there is no Title to keep).
	if d := parseInfoElement(t, mustRender(t, noTitle, "", false), limit); d.hasSegTitle {
		t.Fatalf("no title + present=false: hasSegTitle=true, want false")
	}

	// Case 2: an Info that ALREADY has a Title (the replace/remove path, titleOff >= 0)
	// - the branch a titleless fixture never exercises.
	withTitle := infoFromRaw(
		encElement(idInfo, append(uintElement(idTimestampScl, 1000000), stringElement(idSegTitle, "Old")...)),
		0, bits.NewDepth(8), limit)
	if withTitle == nil || withTitle.titleOff < 0 {
		t.Fatalf("infoFromRaw did not capture the existing Title (titleOff=%d)", withTitle.titleOff)
	}
	// present=false strips the existing <Title> (a real --clear TITLE).
	if d := parseInfoElement(t, mustRender(t, withTitle, "", false), limit); d.hasSegTitle {
		t.Errorf("--clear TITLE over an existing title: hasSegTitle=true, want false (stripped)")
	}
	// present=true, title="New" replaces the existing title.
	if d := parseInfoElement(t, mustRender(t, withTitle, "New", true), limit); !d.hasSegTitle || d.segTitle != "New" {
		t.Errorf("replace title: hasSegTitle=%v segTitle=%q, want true/New", d.hasSegTitle, d.segTitle)
	}
	// present=true, title="" turns the existing title into a present-empty value.
	if d := parseInfoElement(t, mustRender(t, withTitle, "", true), limit); !d.hasSegTitle || d.segTitle != "" {
		t.Errorf("set TITLE= over an existing title: hasSegTitle=%v segTitle=%q, want true/empty", d.hasSegTitle, d.segTitle)
	}
}

// mustRender renders the Info with the given title/presence and fails if it could not.
func mustRender(t *testing.T, ib *infoBlock, title string, present bool) []byte {
	t.Helper()
	raw, _ := renderInfo(ib, title, present)
	if raw == nil {
		t.Fatal("renderInfo returned nil")
	}
	return raw
}

// parseInfoElement parses a rendered Info element back into a doc.
func parseInfoElement(t *testing.T, raw []byte, limit int64) *doc {
	t.Helper()
	src := core.BytesSource(raw)
	root, ok := readElement(src, 0, src.Size(), limit)
	if !ok || root.id != idInfo {
		t.Fatalf("not an Info element: ok=%v id=%#x", ok, root.id)
	}
	d := &doc{}
	if _, err := parseInfo(src, root, bits.NewDepth(8), limit, d); err != nil {
		t.Fatalf("parseInfo: %v", err)
	}
	return d
}
