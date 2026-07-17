package id3

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// onlyInvolvedFrame asserts exactly one frame with id is present in out and returns its
// decoded function/name pairs, so a test can assert the rebuilt involved-people body directly.
func onlyInvolvedFrame(t *testing.T, out []Frame, id string) []involvedPerson {
	t.Helper()
	var bodies [][]byte
	for _, f := range out {
		if f.ID == id {
			bodies = append(bodies, f.Body)
		}
	}
	if len(bodies) != 1 {
		t.Fatalf("%s frame count = %d, want exactly 1 (%+v)", id, len(bodies), out)
	}
	return decodeInvolvedPeople(bodies[0])
}

// TestInvolvedPeopleRoundTrip is the base case for both versions: the modeled roles project
// (folding the Picard functions), an unknown involvement is not projected, and editing one
// role re-renders a single frame that keeps the untouched sibling roles and preserves the
// unknown involvement. A conformant multi-person frame must not trip the v2.3 multi flag.
func TestInvolvedPeopleRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version byte
		frameID string
	}{
		{"v2.4 TIPL", 4, "TIPL"},
		{"v2.3 IPLS", 3, "IPLS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := []Frame{{ID: tc.frameID, Body: encodeTextFrame(encLatin1,
				[]string{"producer", "Alice", "mix", "Bob", "DJ-mix", "Cara", "mastering", "Dave"})}}

			base := Project(buildTag(t, tc.version, orig)).Tags
			for _, c := range []struct {
				key  tag.Key
				want []string
			}{
				{tag.Producer, []string{"Alice"}},
				{tag.Mixer, []string{"Bob"}},
				{tag.DJMixer, []string{"Cara"}},
			} {
				if got, _ := base.Get(c.key); !slices.Equal(got, c.want) {
					t.Errorf("%s = %v, want %v", c.key, got, c.want)
				}
			}
			for _, k := range base.Keys() {
				if vals, _ := base.Get(k); slices.Contains(vals, "Dave") {
					t.Errorf("unknown involvement projected under %s = %v; want unprojected", k, vals)
				}
			}

			edited := base.Clone()
			edited.Set(tag.Producer, "Alice2")
			out, info := RebuildFrames(orig, base, edited, tc.version, StructuredEdit{}, WriteOpts{})
			if info.UsedV23Multi {
				t.Error("a conformant multi-person involved-people frame must not set UsedV23Multi")
			}
			got := onlyInvolvedFrame(t, out, tc.frameID)
			want := []involvedPerson{
				{"producer", "Alice2"}, {"mix", "Bob"}, {"DJ-mix", "Cara"}, {"mastering", "Dave"},
			}
			if !slices.Equal(got, want) {
				t.Errorf("rebuilt %s pairs = %v, want %v", tc.frameID, got, want)
			}
		})
	}
}

// TestInvolvedPeopleUntouchedSiblingSurvives is the crux: editing one role must not drop an
// untouched sibling role stored in the same frame. It pins the property the no-StructuredEdit
// design rests on (renderUnit gathers all role keys from `edited`, not just the changed ones).
func TestInvolvedPeopleUntouchedSiblingSurvives(t *testing.T) {
	orig := []Frame{{ID: "TIPL", Body: encodeTextFrame(encLatin1,
		[]string{"producer", "Alice", "engineer", "Eve"})}}
	base := Project(buildTag(t, 4, orig)).Tags
	edited := base.Clone()
	edited.Set(tag.Producer, "Alice2") // ENGINEER untouched
	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})
	got := onlyInvolvedFrame(t, out, "TIPL")
	want := []involvedPerson{{"producer", "Alice2"}, {"engineer", "Eve"}}
	if !slices.Equal(got, want) {
		t.Errorf("rebuilt TIPL = %v, want %v (the untouched ENGINEER sibling must survive)", got, want)
	}
}

// TestInvolvedPeopleCapitalizedFunctionNoDuplicate covers the case-folded known-check: a
// capitalized "Producer" already projects to PRODUCER and re-emits from `edited`, so it must
// not also be preserved as an unknown, which would duplicate the credit. On rewrite it also
// normalizes to the canonical lowercase Picard spelling.
func TestInvolvedPeopleCapitalizedFunctionNoDuplicate(t *testing.T) {
	orig := []Frame{{ID: "TIPL", Body: encodeTextFrame(encLatin1,
		[]string{"Producer", "Alice"})}}
	base := Project(buildTag(t, 4, orig)).Tags
	if got, _ := base.Get(tag.Producer); !slices.Equal(got, []string{"Alice"}) {
		t.Fatalf("capitalized Producer projected %v, want [Alice]", got)
	}
	edited := base.Clone()
	edited.Set(tag.Engineer, "Eve") // an unrelated role edit forces the frame to re-render
	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})
	got := onlyInvolvedFrame(t, out, "TIPL")
	want := []involvedPerson{{"producer", "Alice"}, {"engineer", "Eve"}}
	if !slices.Equal(got, want) {
		t.Errorf("rebuilt TIPL = %v, want %v (capitalized Producer must not duplicate, and normalizes to canonical)", got, want)
	}
}

// TestInvolvedPeopleUnknownDedup checks a non-conformant frame that repeats the same unknown
// pair writes it only once after a rewrite.
func TestInvolvedPeopleUnknownDedup(t *testing.T) {
	orig := []Frame{{ID: "TIPL", Body: encodeTextFrame(encLatin1,
		[]string{"producer", "Alice", "mastering", "Dave", "mastering", "Dave"})}}
	base := Project(buildTag(t, 4, orig)).Tags
	edited := base.Clone()
	edited.Set(tag.Producer, "Alice2")
	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})
	got := onlyInvolvedFrame(t, out, "TIPL")
	want := []involvedPerson{{"producer", "Alice2"}, {"mastering", "Dave"}}
	if !slices.Equal(got, want) {
		t.Errorf("rebuilt TIPL = %v, want %v (a duplicate unknown must dedup to one)", got, want)
	}
}

// TestInvolvedPeopleUnknownOrderPreserved checks two distinct unknown involvements survive an
// edit in their original order.
func TestInvolvedPeopleUnknownOrderPreserved(t *testing.T) {
	orig := []Frame{{ID: "TIPL", Body: encodeTextFrame(encLatin1,
		[]string{"producer", "Alice", "mastering", "Dave", "recording", "Rita"})}}
	base := Project(buildTag(t, 4, orig)).Tags
	edited := base.Clone()
	edited.Set(tag.Producer, "Alice2")
	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})
	got := onlyInvolvedFrame(t, out, "TIPL")
	want := []involvedPerson{{"producer", "Alice2"}, {"mastering", "Dave"}, {"recording", "Rita"}}
	if !slices.Equal(got, want) {
		t.Errorf("rebuilt TIPL = %v, want %v (unknowns preserved in original order)", got, want)
	}
}

// TestInvolvedPeopleV23MultiPersonNoWarning checks that a multi-person IPLS on v2.3 (several
// people under one role) is treated as a conformant involved-people frame, NOT the de-facto
// v2.3 NUL-separated multi-value extension, so it does not set UsedV23Multi.
func TestInvolvedPeopleV23MultiPersonNoWarning(t *testing.T) {
	orig := []Frame{{ID: "IPLS", Body: encodeTextFrame(encLatin1,
		[]string{"producer", "Alice", "producer", "Bob"})}}
	base := Project(buildTag(t, 3, orig)).Tags
	if got, _ := base.Get(tag.Producer); !slices.Equal(got, []string{"Alice", "Bob"}) {
		t.Fatalf("two-producer IPLS projected %v, want [Alice Bob]", got)
	}
	edited := base.Clone()
	edited.Set(tag.Producer, "Alice", "Bob", "Cara") // still multi-person
	out, info := RebuildFrames(orig, base, edited, 3, StructuredEdit{}, WriteOpts{})
	if info.UsedV23Multi {
		t.Error("multi-person IPLS must not set UsedV23Multi (it is a conformant frame, not the v2.3 extension)")
	}
	got := onlyInvolvedFrame(t, out, "IPLS")
	want := []involvedPerson{{"producer", "Alice"}, {"producer", "Bob"}, {"producer", "Cara"}}
	if !slices.Equal(got, want) {
		t.Errorf("rebuilt IPLS = %v, want %v", got, want)
	}
}

// TestInvolvedPeopleCrossVersionDrop covers a role edit that changes the write version: a
// source IPLS (v2.3) rewritten as v2.4 must leave one TIPL and no stale IPLS, and must carry
// the unknown involvement across the version switch.
func TestInvolvedPeopleCrossVersionDrop(t *testing.T) {
	orig := []Frame{{ID: "IPLS", Body: encodeTextFrame(encLatin1,
		[]string{"producer", "Alice", "mastering", "Dave"})}}
	base := Project(buildTag(t, 3, orig)).Tags
	edited := base.Clone()
	edited.Set(tag.Producer, "Alice2")
	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})
	for _, f := range out {
		if f.ID == "IPLS" {
			t.Errorf("stale IPLS frame survived a v2.4 rewrite: %+v", out)
		}
	}
	got := onlyInvolvedFrame(t, out, "TIPL")
	want := []involvedPerson{{"producer", "Alice2"}, {"mastering", "Dave"}}
	if !slices.Equal(got, want) {
		t.Errorf("rebuilt TIPL = %v, want %v (unknown must carry across the version switch)", got, want)
	}
}

// TestInvolvedPeopleEmptyValueDropped checks that an empty credit value in an involved-people
// role - which the function/name pairing cannot store at any position, unlike a plain
// multi-value text frame that keeps interior empties - is dropped from the frame AND reported as
// a single value-dropped warning: not silent, and not double-counted by the trailing-empty path.
func TestInvolvedPeopleEmptyValueDropped(t *testing.T) {
	for _, tc := range []struct {
		name string
		vals []string
		want []string // surviving names in the rebuilt frame
	}{
		{"interior", []string{"A", "", "B"}, []string{"A", "B"}},
		{"leading", []string{"", "A", "B"}, []string{"A", "B"}},
		{"trailing", []string{"A", "B", ""}, []string{"A", "B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited := tag.NewTagSet()
			edited.Set(tag.Producer, tc.vals...)
			out, info := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})

			var names []string
			for _, p := range onlyInvolvedFrame(t, out, "TIPL") {
				names = append(names, p.Name)
			}
			if !slices.Equal(names, tc.want) {
				t.Errorf("rebuilt TIPL names = %v, want %v (empties dropped)", names, tc.want)
			}
			if !slices.Contains(info.DroppedInvolvedEmpties, tag.Producer) {
				t.Errorf("DroppedInvolvedEmpties = %v, want it to contain PRODUCER", info.DroppedInvolvedEmpties)
			}
			// Exactly one value-dropped warning: the drop is surfaced, and the trailing case is not
			// also counted by detectDroppedTrailingValues (which excludes involved-people roles).
			ws := AppendRebuildWarnings(nil, info, tag.NewTagSet())
			if n := len(core.WarningsWithCode(ws, core.WarnValueDropped)); n != 1 {
				t.Errorf("value-dropped warnings = %d, want exactly 1 (warned, not double-counted)", n)
			}
		})
	}
}

// TestKeyRenderIDsInvolvedRoles pins the version-branched render targets: the five roles dirty
// TIPL on v2.4 and IPLS on v2.3, while WRITER rides the generic TXXX fallback.
func TestKeyRenderIDsInvolvedRoles(t *testing.T) {
	for _, k := range []tag.Key{tag.Producer, tag.Engineer, tag.Mixer, tag.Arranger, tag.DJMixer} {
		if got := keyRenderIDs(k, 4); !slices.Equal(got, []string{"TIPL"}) {
			t.Errorf("keyRenderIDs(%s, 4) = %v, want [TIPL]", k, got)
		}
		if got := keyRenderIDs(k, 3); !slices.Equal(got, []string{"IPLS"}) {
			t.Errorf("keyRenderIDs(%s, 3) = %v, want [IPLS]", k, got)
		}
	}
	if got := keyRenderIDs(tag.Writer, 4); !slices.Equal(got, []string{"TXXX\x00WRITER"}) {
		t.Errorf("keyRenderIDs(WRITER, 4) = %v, want [TXXX\\x00WRITER]", got)
	}
}
