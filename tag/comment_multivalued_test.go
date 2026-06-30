package tag

import (
	"slices"
	"testing"
)

// TestCommentMultivalued checks that COMMENT behaves like the other canonical list fields.
// The typed projection keeps every value, Patch round-trips the list, and Merge does not cap
// comments to one value.
func TestCommentMultivalued(t *testing.T) {
	if !Comment.Multivalued() {
		t.Fatal("Comment.Multivalued() = false, want true")
	}
	if Comment.SingleValuedMulti(2) {
		t.Error("Comment.SingleValuedMulti(2) = true; a multi-valued key is never a violation")
	}

	// Project keeps the whole list, not just the first value.
	ts := NewTagSet()
	ts.Set(Comment, "first", "second")
	want := []string{"first", "second"}
	if got := Project(ts).Comment; !slices.Equal(got, want) {
		t.Errorf("Project Comment = %v, want %v", got, want)
	}

	// Project -> Patch -> Apply round-trips every value.
	out := Project(ts).Patch().Apply(NewTagSet())
	if got, _ := out.Get(Comment); !slices.Equal(got, want) {
		t.Errorf("Project->Patch Comment = %v, want %v", got, want)
	}

	// A Union merge keeps both distinct comments rather than capping to one.
	base := NewTagSet()
	base.Set(Comment, "a")
	incoming := NewTagSet()
	incoming.Set(Comment, "a", "b")
	merged, _ := Merge(base, incoming, Union)
	if got, _ := merged.Get(Comment); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("merged Comment = %v, want [a b] (multi-valued: not capped to one)", got)
	}
}
