package waxlabel_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

const (
	idTimestampScale = 0x2AD7B1
	idTagLang        = 0x447A
)

// mkLangSimple is a SimpleTag with a TagLanguage.
func mkLangSimple(name, lang, value string) []byte {
	return testfile.El(testfile.IDSimpleTag, mkStr(testfile.IDTagName, name), mkStr(idTagLang, lang), mkStr(testfile.IDTagString, value))
}

// matroskaNoTitle is a file whose Info holds no Title, with elements behind the cluster.
func matroskaNoTitle(elements ...[]byte) []byte {
	info := testfile.El(testfile.IDInfo, testfile.Uint(idTimestampScale, 1000000))
	return concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, info, testfile.AudioCluster(0), concat(elements...)))
}

// twoTitles holds a title in two languages as TITLE SimpleTags, as ffmpeg writes title-eng and
// title-ger, and no Info.Title.
func twoTitles() (src, titles []byte) {
	titles = concat(mkLangSimple("TITLE", "eng", "Song"), mkLangSimple("TITLE", "ger", "Lied"))
	group := testfile.El(testfile.IDTag, testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, 50)), titles)
	return matroskaNoTitle(testfile.El(testfile.IDTags, group)), titles
}

// An edit of another key leaves the TITLE SimpleTags as they are: nothing moves to Info.Title and
// no value is dropped.
func TestMatroskaUnrelatedEditKeepsTitleTags(t *testing.T) {
	src, titles := twoTitles()
	doc := mustParseBytes(t, src)
	if got, want := tagValues(doc, tag.Title), []string{"Song", "Lied"}; !slices.Equal(got, want) {
		t.Fatalf("TITLE = %q, want %q", got, want)
	}
	plan, err := doc.Edit().Set(tag.Genre, "Rock").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnValueDropped); ok {
		t.Errorf("plan warns value-dropped: %s", w.Message)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, titles) {
		t.Error("the TITLE SimpleTags are not carried byte for byte")
	}
	re := mustParseBytes(t, out)
	if got, want := tagValues(re, tag.Title), []string{"Song", "Lied"}; !slices.Equal(got, want) {
		t.Errorf("TITLE after the edit = %q, want %q", got, want)
	}
	if got := tagValues(re, tag.Genre); !slices.Equal(got, []string{"Rock"}) {
		t.Errorf("GENRE after the edit = %q, want Rock", got)
	}
}

// An edit of the title moves it to Info.Title: the TITLE SimpleTags go, the first value stays
// and the rest are reported.
func TestMatroskaTitleEditMovesTitleTagsToInfo(t *testing.T) {
	src, _ := twoTitles()
	plan, err := mustParseBytes(t, src).Edit().Add(tag.Title, "Third").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnValueDropped) {
		t.Errorf("plan warnings = %v, want value-dropped for the titles Info.Title cannot hold", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	if bytes.Contains(out, []byte("TITLE")) {
		t.Error("a TITLE SimpleTag is left beside the Info.Title")
	}
	if got := tagValues(mustParseBytes(t, out), tag.Title); !slices.Equal(got, []string{"Song"}) {
		t.Errorf("TITLE after the edit = %q, want Song", got)
	}
}

// A title edit that asks for more values than Info.Title holds writes the first and warns; asked
// again of its own output it changes nothing and warns again.
func TestMatroskaRepeatedMultiValueTitleIsAWarnedNoOp(t *testing.T) {
	src := testfile.Matroska("matroska", "Old", testfile.El(testfile.IDTags, testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "AA"))))
	first, err := mustParseBytes(t, src).Edit().Set(tag.Title, "One", "Two").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if first.IsNoOp() || !planWarns(t, first, wl.WarnValueDropped) {
		t.Fatalf("first edit: no-op %v, warnings %v; want a write that warns value-dropped", first.IsNoOp(), first.Report().Warnings)
	}
	out := applyToBytes(t, src, first)
	if got := tagValues(mustParseBytes(t, out), tag.Title); !slices.Equal(got, []string{"One"}) {
		t.Fatalf("TITLE after the first edit = %q, want One", got)
	}
	again, err := mustParseBytes(t, out).Edit().Set(tag.Title, "One", "Two").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !again.IsNoOp() {
		t.Errorf("the repeated edit plans a write: %v", again.Report().Operations)
	}
	if !planWarns(t, again, wl.WarnValueDropped) {
		t.Errorf("the repeated edit's warnings = %v, want value-dropped", again.Report().Warnings)
	}
}

// ffmpeg writes title-eng and title-ger as two TITLE SimpleTags; after an edit of another key
// ffprobe reads both.
func TestMatroskaUnrelatedEditKeepsTitleTagsForFFprobe(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	built := filepath.Join(t.TempDir(), "built.mka")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", sampleMKA,
		"-map", "0:a", "-map_metadata", "-1", "-c", "copy",
		"-metadata", "title-eng=Song", "-metadata", "title-ger=Lied", built)
	if log, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, log)
	}
	src, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	doc := mustParseBytes(t, src)
	if got, want := tagValues(doc, tag.Title), []string{"Song", "Lied"}; !slices.Equal(got, want) {
		t.Fatalf("TITLE of ffmpeg's file = %q, want %q", got, want)
	}
	out, _ := saveMatroska(t, src, doc.Edit().Set(tag.Genre, "Rock"))
	tags := ffprobeFormatTags(t, writeTempFile(t, "out.mka", out))
	for key, want := range map[string]string{"title-eng": "Song", "title-ger": "Lied", "genre": "Rock"} {
		if got := lookupCI(tags, key); got != want {
			t.Errorf("ffprobe %s = %q, want %q (all: %v)", key, got, want, tags)
		}
	}
}
