package waxlabel_test

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// Native COMMENT and DESCRIPTION must not merge into one canonical field. A mapping-level
// round-trip can miss that, so one document carries both fields and only COMMENT is edited.
func TestVorbisDescriptionDistinctFromComment(t *testing.T) {
	// No fixture carries both native fields, so synthesize one.
	data := testfile.TaggedFLAC("test", "COMMENT=original comment", "DESCRIPTION=the blurb")

	doc := mustParseBytes(t, data)
	if got, _ := doc.Tags().Get(tag.Comment); len(got) != 1 || got[0] != "original comment" {
		t.Fatalf("COMMENT on parse = %v, want [original comment]", got)
	}
	if got, _ := doc.Tags().Get(tag.Description); len(got) != 1 || got[0] != "the blurb" {
		t.Fatalf("DESCRIPTION on parse = %v, want [the blurb] (must not fold into COMMENT)", got)
	}

	// Edit only COMMENT; DESCRIPTION must be untouched.
	plan, err := doc.Edit().Set(tag.Comment, "edited comment").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	re := mustParseBytes(t, out)
	if got, _ := re.Tags().Get(tag.Comment); len(got) != 1 || got[0] != "edited comment" {
		t.Errorf("COMMENT after edit = %v, want [edited comment]", got)
	}
	if got, _ := re.Tags().Get(tag.Description); len(got) != 1 || got[0] != "the blurb" {
		t.Errorf("DESCRIPTION after editing only COMMENT = %v, want [the blurb] (data loss!)", got)
	}
}
