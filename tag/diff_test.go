package tag

import (
	"slices"
	"testing"
)

func TestDiff(t *testing.T) {
	var base, edited TagSet
	base.Set(Title, "Old")
	base.Set(Artist, "A")
	base.Set(Encoder, "Lavf") // removed in edited
	edited.Set(Title, "New")  // changed
	edited.Set(Artist, "A")   // unchanged: no Change
	edited.Set(Album, "Alb")  // added

	got := Diff(base, edited)

	// Removed/changed come first in base order, then added in edited order; an
	// unchanged key yields nothing.
	want := []Change{
		{Key: Title, Kind: ChangeChanged, Old: []string{"Old"}, New: []string{"New"}},
		{Key: Encoder, Kind: ChangeRemoved, Old: []string{"Lavf"}},
		{Key: Album, Kind: ChangeAdded, New: []string{"Alb"}},
	}
	if len(got) != len(want) {
		t.Fatalf("Diff() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Key != want[i].Key || got[i].Kind != want[i].Kind ||
			!slices.Equal(got[i].Old, want[i].Old) || !slices.Equal(got[i].New, want[i].New) {
			t.Errorf("Diff()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDiffNoOp(t *testing.T) {
	var base TagSet
	base.Set(Title, "X")
	base.Add(Artist, "A", "B")
	if got := Diff(base, base.Clone()); len(got) != 0 {
		t.Errorf("Diff of identical sets = %v, want none", got)
	}
}

// TestDiffMultiValueOrderSignificant: a reordered multi-value field is a change
// (the diff uses the same order-significant equality a codec uses to detect an
// edit).
func TestDiffMultiValueOrderSignificant(t *testing.T) {
	var base, edited TagSet
	base.Add(Artist, "A", "B")
	edited.Add(Artist, "B", "A")
	got := Diff(base, edited)
	if len(got) != 1 || got[0].Kind != ChangeChanged {
		t.Fatalf("Diff() = %v, want one changed", got)
	}
}

func TestChangeKindString(t *testing.T) {
	for kind, want := range map[ChangeKind]string{
		ChangeAdded: "added", ChangeRemoved: "removed", ChangeChanged: "changed",
	} {
		if got := kind.String(); got != want {
			t.Errorf("ChangeKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

// TestChangeZeroValue: the zero value is the explicit ChangeUnknown sentinel, so
// a never-set kind does not masquerade as a real one.
func TestChangeZeroValue(t *testing.T) {
	var c Change
	if c.Kind != ChangeUnknown {
		t.Errorf("zero Change.Kind = %v, want ChangeUnknown", c.Kind)
	}
	if got := ChangeUnknown.String(); got != "unknown" {
		t.Errorf("ChangeUnknown.String() = %q, want %q", got, "unknown")
	}
}
