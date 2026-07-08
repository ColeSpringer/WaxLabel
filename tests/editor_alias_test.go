package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestEditorSetResolvesAlias is a regression guard: the key-taking editor methods resolve tag
// aliases, so Set(tag.Key("DATE"), ...) lands on the canonical RECORDINGDATE on every format
// instead of a custom DATE field - callers no longer have to pre-resolve.
func TestEditorSetResolvesAlias(t *testing.T) {
	data := flacWithComments("TITLE=Song") // no date yet
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Key("DATE"), "2021").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	assertChangeOnCanonicalDate(t, plan)
}

// TestEditorApplyResolvesAlias covers the Apply(tag.TagPatch) path, which takes a pre-built
// patch and would otherwise bypass alias resolution.
func TestEditorApplyResolvesAlias(t *testing.T) {
	data := flacWithComments("TITLE=Song")
	p := tag.NewPatch()
	p.Set(tag.Key("YEAR"), "2021") // YEAR is an alias for RECORDINGDATE too
	plan, err := mustParseBytes(t, data).Edit().Apply(p).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	assertChangeOnCanonicalDate(t, plan)
}

func assertChangeOnCanonicalDate(t *testing.T, plan *wl.Plan) {
	t.Helper()
	sawCanonical := false
	for _, c := range plan.Changes() {
		switch c.Key {
		case tag.RecordingDate:
			sawCanonical = true
		case tag.Key("DATE"), tag.Key("YEAR"):
			t.Errorf("alias stored as a custom key %q instead of resolving to RECORDINGDATE", c.Key)
		}
	}
	if !sawCanonical {
		t.Errorf("edit did not land on the canonical RECORDINGDATE; changes = %v", plan.Changes())
	}
}

// TestEditorSetAliasRoundTripsMP4 pins the end-to-end effect on a format where a custom key
// and the canonical field differ: on MP4 an unresolved DATE would be a freeform atom, so this
// proves the resolved edit reads back as the canonical RECORDINGDATE, with no stray custom key.
func TestEditorSetAliasRoundTripsMP4(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "Book"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Key("DATE"), "2021").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if v, ok := re.Get(tag.RecordingDate); !ok || len(v) != 1 || v[0] != "2021" {
		t.Errorf("RecordingDate = %v (ok=%v), want [2021] (DATE must resolve to the canonical field)", v, ok)
	}
	if v, ok := re.Get(tag.Key("DATE")); ok {
		t.Errorf("a stray custom DATE key survived: %v", v)
	}
}
