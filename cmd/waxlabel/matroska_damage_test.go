package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// patchedChapters is chapters.mka with one size byte changed so that its element runs past the
// element holding it: at 577 the first SimpleTag of the album Tag, at 457 the EditionFlagDefault.
func patchedChapters(t *testing.T, at int, b byte) string {
	t.Helper()
	data, err := os.ReadFile("../../testdata/chapters.mka")
	if err != nil {
		t.Fatal(err)
	}
	data[at] = b
	return writeTempFile(t, "damaged.mka", data)
}

// A SimpleTag that runs past its Tag is reported by dump, and an edit of the tags drops it with
// a warning, or refuses under --strict and writes nothing.
func TestMatroskaUnreadableTagRegionCLI(t *testing.T) {
	t.Parallel()
	file := patchedChapters(t, 577, 0x17)
	before, _ := os.ReadFile(file)
	if out, errb, code := runCLI(t, "dump", file); code != 0 || !strings.Contains(out, "[malformed-tag-entry] 54 byte(s) of the Tags element") {
		t.Errorf("dump exit %d, want 0 with the malformed-tag-entry warning:\n%s%s", code, out, errb)
	}
	edit := []string{"set", file, "--set", "COMPOSER=Bach", "--set", "TRACKNUMBER=9"}
	if _, errb, code := runCLI(t, append(edit, "--strict")...); code != 2 || !strings.Contains(errb, "54 byte(s) of the Tags element that could not be read are not carried") {
		t.Errorf("strict exit %d, want 2 naming the bytes the write would drop: %s", code, errb)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Error("the refused edit changed the file")
	}
	if out, errb, code := runCLI(t, edit...); code != 0 || !strings.Contains(out, "[malformed-tag-entry-dropped]") {
		t.Errorf("set exit %d, want 0 warning the drop:\n%s%s", code, out, errb)
	}
	out, errb, code := runCLI(t, "dump", file)
	if code != 0 || !strings.Contains(out, "COMPOSER") || !strings.Contains(out, "Bach") || strings.Contains(out, "malformed-tag-entry") {
		t.Errorf("dump of the written file, exit %d:\n%s%s", code, out, errb)
	}
}

// Chapters added to an edition whose leading bytes are no element are planned and counted, with
// the drop of those bytes warned.
func TestMatroskaUnreadableEditionCLI(t *testing.T) {
	t.Parallel()
	file := patchedChapters(t, 457, 0x01)
	if out, errb, code := runCLI(t, "dump", file); code != 0 || !strings.Contains(out, "[malformed-tag-entry] 102 byte(s) of the Chapters element") {
		t.Errorf("dump exit %d, want 0 with the malformed-tag-entry warning:\n%s%s", code, out, errb)
	}
	add := []string{file, "--add-chapter", "0:00=One", "--add-chapter", "0:00.3=Two"}
	out, errb, code := runCLI(t, append([]string{"plan"}, add...)...)
	if code != 0 || !strings.Contains(out, "+ chapters: 2") || !strings.Contains(out, "[malformed-tag-entry-dropped]") {
		t.Errorf("plan exit %d, want 0 with two chapters added and the drop warned:\n%s%s", code, out, errb)
	}
	if _, errb, code := runCLI(t, append([]string{"set", "--strict"}, add...)...); code != 2 {
		t.Errorf("strict exit %d, want 2: %s", code, errb)
	}
	if _, errb, code := runCLI(t, append([]string{"set"}, add...)...); code != 0 {
		t.Fatalf("set exit %d: %s", code, errb)
	}
	if out, _, _ := runCLI(t, "dump", file); !strings.Contains(out, "chapters (2)") || strings.Contains(out, "malformed-tag-entry") {
		t.Errorf("dump of the written file:\n%s", out)
	}
}

// A value read up to its NUL copies onto a format that refuses a NUL in a value.
func TestCopyOfMatroskaValueCutAtNUL(t *testing.T) {
	t.Parallel()
	src := writeTempFile(t, "nul.mka", testfile.Matroska("matroska", "T",
		testfile.El(testfile.IDTags, testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "Alpha\x00Beta")))))
	dst := copyFixture(t, "../../testdata/notags.flac")
	if out, errb, code := runCLI(t, "copy", src, dst); code != 0 {
		t.Fatalf("copy exit %d:\n%s%s", code, out, errb)
	}
	if out, _, _ := runCLI(t, "dump", dst); !strings.Contains(out, "Alpha") || strings.Contains(out, "Beta") {
		t.Errorf("dump of the destination:\n%s", out)
	}
}
