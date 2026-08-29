package main

import (
	"testing"
)

// TestTwoFileIconsIsARefusedWrite: authoring a second type-1 picture leaves the source
// file perfectly readable and only the requested write impossible, which is exit 3
// (unsupported-tag), not exit 4 (invalid-data). The distinction matters beyond the number:
// invalid-data outranks every exit-3 class in a batch run's aggregate, so grading a bad
// flag combination as corruption would let it mask a genuinely corrupt file.
func TestTwoFileIconsIsARefusedWrite(t *testing.T) {
	t.Parallel()
	png := writeTempImage(t, "icon.png", minimalPNG())
	f := copyFixture(t, sampleFLAC)

	if _, errb, code := runCLI(t, "set", f, "--add-picture", "file-icon="+png); code != 0 {
		t.Fatalf("adding the first file icon: exit = %d\n%s", code, errb)
	}
	out, errb, code := runCLI(t, "--json", "set", f, "--add-picture", "file-icon="+png)
	if code != 3 {
		t.Fatalf("a second file icon: exit = %d, want 3\n%s%s", code, out, errb)
	}
	jr := decodeJSONList[jsonReport](t, out)
	if len(jr) != 1 || jr[0].Error == nil {
		t.Fatalf("want one JSON report carrying an error, got %s", out)
	}
	if jr[0].Error.Code != "unsupported-tag" {
		t.Errorf("error code = %q, want unsupported-tag", jr[0].Error.Code)
	}
}
