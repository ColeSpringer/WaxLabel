package waxlabel_test

import (
	"encoding/binary"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// pngOfSize returns tinyPNG with its IHDR width and height rewritten, so a test can build a
// picture of any declared shape without a real encoder. The sniffer reads the dimensions
// from IHDR, which is all these tests need.
func pngOfSize(w, h uint32) []byte {
	b := slices.Clone(tinyPNG())
	binary.BigEndian.PutUint32(b[16:20], w)
	binary.BigEndian.PutUint32(b[20:24], h)
	return b
}

// TestFileIconShapeWarns: ID3v2 section 4.14 requires a type-1 file icon to be a 32x32
// PNG. Everything the check needs is already decoded on the picture, so nothing was
// enforcing a rule the model could see.
func TestFileIconShapeWarns(t *testing.T) {
	data := readFixture(t, notagsMP3)
	plan, err := mustParseBytes(t, data).Edit().
		AddPicture(wl.Picture{Type: wl.PicFileIcon, Data: pngOfSize(64, 64)}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnNonConformingIcon)
	if !ok {
		t.Fatalf("a 64x64 file icon was accepted silently: %v", plan.Report().Warnings)
	}
	// The warning and the lint finding share a string, so the two cannot drift.
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	var found bool
	for _, f := range re.Lint() {
		if f.Code == "non-conforming-icon" {
			found = true
			if f.Severity != wl.LintWarning {
				t.Errorf("severity = %v, want LintWarning: an oversized icon is unambiguous and every reader renders it", f.Severity)
			}
			if !containsSuffix(w.Message, f.Message) {
				t.Errorf("edit warning %q and lint finding %q disagree", w.Message, f.Message)
			}
		}
	}
	if !found {
		t.Errorf("lint did not report the non-conforming icon: %v", re.Lint())
	}
	// It is a warning, not a refusal: the picture is written.
	if pics := re.Pictures(); len(pics) != 1 || pics[0].Type != wl.PicFileIcon {
		t.Errorf("pictures = %+v, want the icon written anyway", pics)
	}
}

// containsSuffix reports whether the edit-scoped message is the lint message under its
// "added " prefix, which is the only difference the two are allowed to have.
func containsSuffix(edit, lint string) bool {
	return edit == lint || edit == "added "+lint
}

// TestFileIconMIMEWarns covers the other half of the same rule: the type requires PNG.
func TestFileIconMIMEWarns(t *testing.T) {
	data := readFixture(t, notagsMP3)
	plan, err := mustParseBytes(t, data).Edit().
		AddPicture(wl.Picture{Type: wl.PicFileIcon, Data: tinyJPEG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnNonConformingIcon); !ok {
		t.Errorf("a JPEG file icon was accepted silently: %v", plan.Report().Warnings)
	}
}

// TestConformingFileIconIsClean is the negative: a 32x32 PNG is exactly what the type asks
// for, and neither surface should say anything about it.
func TestConformingFileIconIsClean(t *testing.T) {
	data := readFixture(t, notagsMP3)
	plan, err := mustParseBytes(t, data).Edit().
		AddPicture(wl.Picture{Type: wl.PicFileIcon, Data: pngOfSize(32, 32)}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnNonConformingIcon); ok {
		t.Errorf("a conforming icon warned: %v", plan.Report().Warnings)
	}
	if re := mustParseBytes(t, applyToBytes(t, data, plan)); lintHasCode(re, "non-conforming-icon") {
		t.Errorf("a conforming icon linted dirty: %v", re.Lint())
	}
}

// TestOtherPictureTypesUnaffected: the rule is about type 1 only. A front cover of any
// shape is ordinary cover art.
func TestOtherPictureTypesUnaffected(t *testing.T) {
	data := readFixture(t, notagsMP3)
	plan, err := mustParseBytes(t, data).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: pngOfSize(64, 64)}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnNonConformingIcon); ok {
		t.Errorf("a front cover was judged against the file-icon rule: %v", plan.Report().Warnings)
	}
}

// TestFileIconRuleIsFormatAgnostic: FLAC and Vorbis METADATA_BLOCK_PICTURE inherit ID3's
// picture-type vocabulary, so type 1 means the same thing there and gets the same rule.
func TestFileIconRuleIsFormatAgnostic(t *testing.T) {
	data := readFixture(t, notagsFLAC)
	plan, err := mustParseBytes(t, data).Edit().
		AddPicture(wl.Picture{Type: wl.PicFileIcon, Data: pngOfSize(64, 64)}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnNonConformingIcon); !ok {
		t.Errorf("FLAC did not apply the shared icon rule: %v", plan.Report().Warnings)
	}
	if re := mustParseBytes(t, applyToBytes(t, data, plan)); !lintHasCode(re, "non-conforming-icon") {
		t.Errorf("FLAC lint did not report the non-conforming icon: %v", re.Lint())
	}
}

// TestV23DateSeparatorIsCoerced: "2001-02-03 10:20" is stored in full but reads back as
// "2001-02-03T10:20", because TYER/TDAT/TIME store neither separator and the read path
// recomposes with 'T'. Nothing is lost, so it is neither a drop nor a reduction - but the
// stored value is not the one that was set, and --strict must see that.
func TestV23DateSeparatorIsCoerced(t *testing.T) {
	data := readFixture(t, notagsMP3)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.RecordingDate, "2001-02-03 10:20").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnValueCoerced)
	if !ok {
		t.Fatalf("the read-back normalization was not reported: %v", plan.Report().Warnings)
	}
	if !slices.Contains(w.Keys, tag.RecordingDate) {
		t.Errorf("warning keys = %v, want RECORDINGDATE", w.Keys)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tag.RecordingDate); !slices.Equal(v, []string{"2001-02-03T10:20"}) {
		t.Errorf("stored RECORDINGDATE = %v, want the normalized form the warning predicted", v)
	}
}

// TestV23DateFatesAreDistinct pins the classification boundaries, so a later pass cannot
// collapse three answers into one: a value with no year drops, a value missing a component
// reduces, a spelling change coerces, and a canonical value says nothing.
func TestV23DateFatesAreDistinct(t *testing.T) {
	for _, c := range []struct {
		value string
		want  wl.WarningCode
	}{
		{"2021", 0},                                  // exact
		{"2021-03-15", 0},                            // exact
		{"2021-03-15T10:30", 0},                      // exact
		{"20210315", wl.WarnValueDropped},            // no extractable year
		{"2021-03", wl.WarnValueReduced},             // month with no full date
		{"2021-03-15T10:30:45", wl.WarnValueReduced}, // TIME has no seconds
		{"2021-03-15 10:30", wl.WarnValueCoerced},    // separator recomposed as 'T'
	} {
		data := readFixture(t, notagsMP3)
		plan, err := mustParseBytes(t, data).Edit().Set(tag.RecordingDate, c.value).Prepare()
		if err != nil {
			t.Fatalf("%q: %v", c.value, err)
		}
		for _, code := range []wl.WarningCode{wl.WarnValueDropped, wl.WarnValueReduced, wl.WarnValueCoerced} {
			_, got := warningFor(plan, code)
			if want := code == c.want; got != want {
				t.Errorf("%q: %v = %v, want %v (warnings: %v)", c.value, code, got, want, plan.Report().Warnings)
			}
		}
	}
}
