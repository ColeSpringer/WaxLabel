package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// webmCleanTrackEncoder replaces the track ENCODER stamp with a same-length clean name.
func webmCleanTrackEncoder(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../testdata/sample.webm")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	const stamp, clean = "Lavc61.19.101 libopus", "opusenc 0.2 libopus 1"
	if n := bytes.Count(data, []byte(stamp)); n != 1 {
		t.Fatalf("fixture holds the track ENCODER %d times, want 1", n)
	}
	dst := filepath.Join(t.TempDir(), "sample.webm")
	if err := os.WriteFile(dst, bytes.Replace(data, []byte(stamp), []byte(clean), 1), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dst
}

// TestLintFixKeepsMatroskaTrackScope: --fix keeps ENCODER at track scope, not album.
func TestLintFixKeepsMatroskaTrackScope(t *testing.T) {
	file := webmCleanTrackEncoder(t)

	out, errb, code := runCLI(t, "lint", "--fix", file)
	if code != 0 {
		t.Fatalf("lint --fix exit = %d, want 0: %s", code, errb)
	}
	if !strings.Contains(out, "fixed:") {
		t.Fatalf("lint --fix reported no fix:\n%s", out)
	}

	dump, _, code := runCLI(t, "dump", "--native", file)
	if code != 0 {
		t.Fatalf("dump --native exit = %d, want 0", code)
	}
	nb := strings.Index(dump, "native blocks")
	if nb < 0 {
		t.Fatalf("dump --native output lost the native blocks header:\n%s", dump)
	}
	blocks := dump[nb:]
	if i := strings.Index(blocks, "families ("); i > 0 {
		blocks = blocks[:i]
	}
	if n := strings.Count(blocks, "ENCODER"); n != 1 {
		t.Errorf("ENCODER appears %d times in the native blocks, want 1:\n%s", n, dump)
	}
	marker := strings.Index(blocks, "scope=track")
	value := strings.Index(blocks, "opusenc 0.2 libopus 1")
	if marker < 0 || value < 0 {
		t.Fatalf("native view lost the track group or the clean encoder:\n%s", dump)
	}
	if value < marker {
		t.Errorf("the kept ENCODER relocated to the album block instead of staying at track scope:\n%s", dump)
	}
	if album := blocks[:marker]; strings.Contains(album, "ENCODER") {
		t.Errorf("the album block still lists ENCODER:\n%s", dump)
	}

	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, errb, code := runCLI(t, "lint", "--fix", file); code != 0 {
		t.Fatalf("second lint --fix exit = %d, want 0: %s", code, errb)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("a second lint --fix rewrote the file (%d -> %d bytes)", len(before), len(after))
	}
}

// TestLintFixClearsMatroskaStampPair: --fix clears both muxer and codec ENCODER stamps.
func TestLintFixClearsMatroskaStampPair(t *testing.T) {
	file := copyFixture(t, "../../testdata/sample.webm")

	if _, errb, code := runCLI(t, "lint", "--fix", file); code != 0 {
		t.Fatalf("lint --fix exit = %d, want 0: %s", code, errb)
	}
	dump, _, code := runCLI(t, "dump", "--native", file)
	if code != 0 {
		t.Fatalf("dump --native exit = %d, want 0", code)
	}
	if strings.Contains(dump, "ENCODER") || strings.Contains(dump, "Lavc") {
		t.Errorf("a Lavf/Lavc stamp pair must leave no ENCODER behind:\n%s", dump)
	}
	out, _, code := runCLI(t, "lint", file)
	if code != 0 {
		t.Errorf("lint after --fix exit = %d, want 0 (clean):\n%s", code, out)
	}
}

// Matroska element IDs the synthesized files use beyond testfile's.
const (
	idTimestampScale = 0x2AD7B1
	idTagLang        = 0x447A
)

func mkaStr(id uint64, s string) []byte { return testfile.El(id, []byte(s)) }

// writeMKA writes a Matroska file whose Info holds no Title, with elements behind the cluster.
func writeMKA(t *testing.T, elements ...[]byte) string {
	t.Helper()
	info := testfile.El(testfile.IDInfo, testfile.Uint(idTimestampScale, 1000000))
	data := slices.Concat(testfile.EBMLHeader("matroska"),
		testfile.El(testfile.IDSegment, info, testfile.AudioCluster(0), slices.Concat(elements...)))
	path := filepath.Join(t.TempDir(), "synth.mka")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// twoTitleTags writes a file holding its title in two languages as TITLE SimpleTags beside an
// inherited ENCODER stamp, and returns the SimpleTags' bytes.
func twoTitleTags(t *testing.T) (path string, titles []byte) {
	t.Helper()
	title := func(lang, value string) []byte {
		return testfile.El(testfile.IDSimpleTag, mkaStr(testfile.IDTagName, "TITLE"), mkaStr(idTagLang, lang), mkaStr(testfile.IDTagString, value))
	}
	titles = slices.Concat(title("eng", "Song"), title("ger", "Lied"))
	group := testfile.El(testfile.IDTag, testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, 50)),
		titles, testfile.SimpleTag("ENCODER", "Lavf61.7.100"))
	return writeMKA(t, testfile.El(testfile.IDTags, group)), titles
}

// TestStrictEditKeepsMatroskaTitleTags: an edit of another key drops no title, so --strict lets
// it through and both TITLE SimpleTags stay as they are.
func TestStrictEditKeepsMatroskaTitleTags(t *testing.T) {
	file, titles := twoTitleTags(t)
	if _, errb, code := runCLI(t, "set", "--strict", "--set", "GENRE=Rock", file); code != 0 {
		t.Fatalf("set --strict exit = %d, want 0: %s", code, errb)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, titles) {
		t.Error("the TITLE SimpleTags are not carried byte for byte")
	}
	dump, _, _ := runCLI(t, "dump", file)
	for _, want := range []string{"Song", "Lied", "Rock"} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump after the edit lacks %q:\n%s", want, dump)
		}
	}
}

// TestLintFixKeepsMatroskaTitleTags: --fix clears the encoder stamp and leaves the titles alone,
// so the two values of the single-valued key are still reported (exit 1).
func TestLintFixKeepsMatroskaTitleTags(t *testing.T) {
	file, titles := twoTitleTags(t)
	out, errb, code := runCLI(t, "lint", "--fix", file)
	if code != 1 {
		t.Fatalf("lint --fix exit = %d, want 1: %s", code, errb)
	}
	if !strings.Contains(out, "fixed:") || !strings.Contains(out, "not auto-fixed: [warning] single-valued-multi") {
		t.Fatalf("lint --fix output lacks the fix or the titles it leaves:\n%s", out)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, titles) {
		t.Error("the TITLE SimpleTags are not carried byte for byte")
	}
	if bytes.Contains(data, []byte("Lavf")) {
		t.Error("the encoder stamp is still in the file")
	}
}

// TestDumpShowsMatroskaTargetedTags: a Tag naming an attachment is listed at attachment scope in
// the native and family views, and its values are not among the file's tags.
func TestDumpShowsMatroskaTargetedTags(t *testing.T) {
	const idTagAttachUID = 0x63C6
	booklet := testfile.El(testfile.IDTag, testfile.El(testfile.IDTargets, testfile.Uint(idTagAttachUID, 0x99)),
		testfile.SimpleTag("ARTIST", "Scanner"))
	file := writeMKA(t, testfile.El(testfile.IDTags, booklet, testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "File Artist"))))

	dump, errb, code := runCLI(t, "dump", "--native", file)
	if code != 0 {
		t.Fatalf("dump --native exit = %d, want 0: %s", code, errb)
	}
	tags, native, ok := strings.Cut(dump, "native blocks")
	if !ok {
		t.Fatalf("dump --native output lost the native blocks header:\n%s", dump)
	}
	if strings.Contains(tags, "Scanner") || !strings.Contains(tags, "File Artist") {
		t.Errorf("the file's tags hold the attachment's artist, or lack the file's:\n%s", dump)
	}
	if !strings.Contains(native, "scope=attachment") || !strings.Contains(native, "Scanner") {
		t.Errorf("the native view lacks the attachment's Tag:\n%s", dump)
	}
	if strings.Contains(dump, "(conflict)") {
		t.Errorf("the attachment's artist is listed as a conflict:\n%s", dump)
	}
	if !strings.Contains(native, "Scanner  (attachment's, not the file's)") || strings.Contains(native, "File Artist  (") {
		t.Errorf("the family view does not mark the attachment's artist, or marks the file's:\n%s", dump)
	}

	out, _, code := runCLI(t, "dump", "--json", "--native", file)
	if code != 0 {
		t.Fatalf("dump --json exit = %d", code)
	}
	var docs []struct {
		Sources []struct {
			Scope    string   `json:"scope"`
			Values   []string `json:"values"`
			Targeted bool     `json:"targeted"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(out), &docs); err != nil || len(docs) != 1 {
		t.Fatalf("dump --json: %v\n%s", err, out)
	}
	for _, s := range docs[0].Sources {
		if want := s.Scope == "attachment"; s.Targeted != want {
			t.Errorf("source %v at %s scope: targeted %v, want %v", s.Values, s.Scope, s.Targeted, want)
		}
	}
	if strings.Count(out, `"targeted"`) != 1 {
		t.Errorf("dump --json states \"targeted\" for more than the one targeted source:\n%s", out)
	}
}

// TestSetWritesMatroskaLevelTags: in a file tagged by level the album is the album-level TITLE
// and the title the track-level one; set writes each where the file keeps it.
func TestSetWritesMatroskaLevelTags(t *testing.T) {
	level := func(n uint64, simples ...[]byte) []byte {
		return testfile.El(testfile.IDTag, testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, n)), slices.Concat(simples...))
	}
	file := writeMKA(t, testfile.El(testfile.IDTags,
		level(50, testfile.SimpleTag("TITLE", "The Album"), testfile.SimpleTag("ARTIST", "Album Artist")),
		level(30, testfile.SimpleTag("TITLE", "Track Title"), testfile.SimpleTag("ARTIST", "Track Artist"))))

	dump, _, code := runCLI(t, "dump", file)
	if code != 0 {
		t.Fatalf("dump exit = %d, want 0:\n%s", code, dump)
	}
	for _, line := range []string{"ALBUM        The Album", "ALBUMARTIST  Album Artist", "TITLE        Track Title", "ARTIST       Track Artist"} {
		if !strings.Contains(dump, line) {
			t.Errorf("dump lacks %q:\n%s", line, dump)
		}
	}

	if _, errb, code := runCLI(t, "set", "--strict", "--set", "TITLE=New Title", "--set", "ALBUM=New Album", file); code != 0 {
		t.Fatalf("set exit = %d, want 0: %s", code, errb)
	}
	native, _, _ := runCLI(t, "dump", "--native", file)
	_, blocks, _ := strings.Cut(native, "native blocks")
	blocks, _, _ = strings.Cut(blocks, "families (")
	album, track, ok := strings.Cut(blocks, "scope=track")
	if !ok {
		t.Fatalf("the native view lost the track-level Tag:\n%s", native)
	}
	if !strings.Contains(album, "New Album") || strings.Contains(album, "New Title") || strings.Contains(native, "Info.Title") {
		t.Errorf("the album is not the album-level TITLE, or the title left the track level:\n%s", native)
	}
	if !strings.Contains(track, "New Title") {
		t.Errorf("the track-level Tag lacks the new title:\n%s", native)
	}
}

// TestClearChaptersReportsOtherMatroskaEditions: --clear-chapters removes every edition, says so,
// and --strict refuses it.
func TestClearChaptersReportsOtherMatroskaEditions(t *testing.T) {
	const (
		idChapters      = 0x1043A770
		idEditionEntry  = 0x45B9
		idEditionUID    = 0x45BC
		idEditionFlagDf = 0x45DB
		idChapterAtom   = 0xB6
		idChapterUID    = 0x73C4
		idChapTimeStart = 0x91
	)
	atom := func(uid uint64) []byte {
		return testfile.El(idChapterAtom, testfile.Uint(idChapterUID, uid), testfile.Uint(idChapTimeStart, 0))
	}
	chapters := testfile.El(idChapters,
		testfile.El(idEditionEntry, testfile.Uint(idEditionFlagDf, 1), atom(1)),
		testfile.El(idEditionEntry, testfile.Uint(idEditionUID, 7777), atom(2)),
		testfile.El(idEditionEntry, testfile.Uint(idEditionUID, 8888), atom(3)))
	file := writeMKA(t, chapters)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	_, errb, code := runCLI(t, "set", "--strict", "--clear-chapters", file)
	if code != 2 || !strings.Contains(errb, "2 other edition(s)") {
		t.Errorf("set --strict --clear-chapters exit = %d, want 2 naming the 2 other editions: %s", code, errb)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Error("the refused clear changed the file")
	}

	out, errb, code := runCLI(t, "set", "--clear-chapters", file)
	if code != 0 {
		t.Fatalf("set --clear-chapters exit = %d, want 0: %s", code, errb)
	}
	if !strings.Contains(out+errb, "Chapters: 2 other editions removed") || !strings.Contains(out+errb, "chapter-metadata-dropped") {
		t.Errorf("the clear does not report the other editions:\n%s%s", out, errb)
	}
	if after, _ := os.ReadFile(file); bytes.Contains(after, testfile.IDBytes(idChapters)) {
		t.Error("a Chapters element is left after the clear")
	}
}
