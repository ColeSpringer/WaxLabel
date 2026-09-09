package main

import (
	"strings"
	"testing"
)

// TestSetID3MultiFlag: the three library policies are reachable from set and copy; the
// default stays NUL-separated with its advisory, repeat writes one frame per value and drops
// the advisory, and an unknown value is a usage error.
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
	// An explicitly empty value is a usage error, as it is for the sibling write-shaping
	// flags: it is otherwise indistinguishable from leaving the flag off.
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
