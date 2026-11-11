package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// TestSetID3MultiFlag: set/copy expose NUL/repeat/unknown id3-multi policies.
func TestSetID3MultiFlag(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, td("notags.mp3"))
	out, _, code := runCLI(t, "set", f, "--set", "ARTIST=A1", "--add", "ARTIST=A2")
	if code != 0 || !strings.Contains(out, "id3-multi-value") {
		t.Fatalf("default: exit %d\n%s", code, out)
	}
	f2 := copyFixture(t, td("notags.mp3"))
	out, _, code = runCLI(t, "set", f2, "--set", "ARTIST=A1", "--add", "ARTIST=A2", "--id3-multi", "repeat")
	if code != 0 || strings.Contains(out, "id3-multi-value") {
		t.Fatalf("repeat: exit %d\n%s", code, out)
	}
	out, _, _ = runCLI(t, "dump", "--native", f2)
	if strings.Count(out, "TPE1") != 2 {
		t.Errorf("repeat should write two TPE1 frames:\n%s", out)
	}
	jd := decodeJSONOne[jsonDocument](t, mustDumpJSON(t, f2))
	if v := tagValues(jd, "ARTIST"); len(v) != 2 {
		t.Errorf("ARTIST = %v", v)
	}
	if _, errb, code := runCLI(t, "set", f, "--set", "ARTIST=X", "--id3-multi", "bogus"); code != 2 || !strings.Contains(errb, "id3-multi") {
		t.Errorf("bogus: exit %d stderr %q", code, errb)
	}
	if _, _, code = runCLI(t, "copy", "--dry-run", "--id3-multi", "slash", f2, f); code != 0 {
		t.Errorf("copy should accept the flag: exit %d", code)
	}
	// The flag follows the tag's version, not the container: a WAV whose id3 chunk is v2.3 honors
	// it, one whose chunk is v2.4 separates values natively.
	for version, want := range map[byte]int{3: 2, 4: 1} {
		w := filepath.Join(t.TempDir(), "tagged.wav")
		data := testfile.WAV(testfile.WAVFmtPCM(),
			testfile.WAVChunk("id3 ", testfile.ID3v2(version, testfile.TextFrame(version, "TIT2", "T"))), testfile.WAVData(4000))
		if err := os.WriteFile(w, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, code := runCLI(t, "set", w, "--set", "ARTIST=A1", "--add", "ARTIST=A2", "--id3-multi", "repeat"); code != 0 {
			t.Fatalf("WAV v2.%d: exit %d", version, code)
		}
		out, _, _ = runCLI(t, "dump", "--native", w)
		if got := strings.Count(out, "TPE1"); got != want {
			t.Errorf("WAV v2.%d: %d TPE1 frame(s), want %d:\n%s", version, got, want, out)
		}
	}
	// Empty --id3-multi is usage error (indistinguishable from unset).
	for _, cmd := range [][]string{
		{"set", f, "--set", "ARTIST=X", "--id3-multi", ""},
		{"plan", f, "--set", "ARTIST=X", "--id3-multi", ""},
		{"copy", "--dry-run", "--id3-multi", "", f2, f},
	} {
		if _, errb, code := runCLI(t, cmd...); code != 2 || !strings.Contains(errb, "id3-multi") {
			t.Errorf("%s with an empty --id3-multi: exit %d stderr %q", cmd[0], code, errb)
		}
	}
}

// TestWriteShapingHelpStatesTheRules: --id3-multi follows the tag's version in any container, and
// --legacy strip consolidates WAV and AIFF native chunks.
func TestWriteShapingHelpStatesTheRules(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"set", "plan", "copy"} {
		out, _, code := runCLI(t, cmd, "--help")
		if code != 0 {
			t.Fatalf("%s --help exit %d", cmd, code)
		}
		help := strings.Join(strings.Fields(out), " ")
		for _, want := range []string{"existing v2.3 tag in any container", "LIST/INFO", "native text chunks"} {
			if !strings.Contains(help, want) {
				t.Errorf("%s --help does not mention %q", cmd, want)
			}
		}
		// A WAV or AIFF id3 chunk can hold padding, which no padding flag changes.
		if cmd != "copy" && (strings.Contains(help, "have no padding region") || !strings.Contains(help, "id3 chunk keep what they have")) {
			t.Errorf("%s --help misstates what --no-padding does to WAV and AIFF", cmd)
		}
	}
}
