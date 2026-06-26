package waxlabel_test

import (
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// notagsFLAC is a FLAC with no canonical tags, so an edit's changes are unambiguous.
const notagsFLAC = "../testdata/notags.flac"

// TestTrackNumberSlashIsLibrarySemantic (A2): the "n/total" split is a public-library
// normalization, not just a CLI nicety - a caller doing Set(tag.TrackNumber, "3/12")
// gets the canonical pair, here observed through the plan's field-level changes.
func TestTrackNumberSlashIsLibrarySemantic(t *testing.T) {
	doc := mustParseFile(t, copyToTemp(t, notagsFLAC))
	plan, err := doc.Edit().Set(tag.TrackNumber, "3/12").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	got := map[tag.Key][]string{}
	for _, c := range plan.Changes() {
		got[c.Key] = c.New
	}
	if v := got[tag.TrackNumber]; len(v) != 1 || v[0] != "3" {
		t.Errorf("TRACKNUMBER change new = %v, want [3]", v)
	}
	if v := got[tag.TrackTotal]; len(v) != 1 || v[0] != "12" {
		t.Errorf("TRACKTOTAL change new = %v, want [12] (the slash total split out)", v)
	}
}

// TestTrackNumberNULRejected (A2): a NUL in a slash number is rejected by Prepare
// before the split, not smuggled into the derived TRACKTOTAL (which rejectInvalidValues
// does not scan, since it is not a patched key). This is the load-bearing reason
// splitNumberPairs runs *after* the NUL guard. execve blocks a NUL in CLI argv, so
// this footgun can only be reached - and tested - through the library.
func TestTrackNumberNULRejected(t *testing.T) {
	doc := mustParseFile(t, copyToTemp(t, notagsFLAC))
	if _, err := doc.Edit().Set(tag.TrackNumber, "3/\x00").Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Fatalf("Set(TRACKNUMBER, \"3/\\x00\").Prepare() err = %v, want ErrInvalidData", err)
	}
}
