package main

import (
	"os"
	"strings"
	"testing"
)

// TestLintFixKeepsMatroskaTrackScope: sample.webm carries a
// muxer ENCODER at album scope and the real codec ENCODER at track scope, so lint
// flags the cross-scope conflict and --fix resolves it to the codec value. That fix
// must leave the surviving value in the track Tag block it already lives in rather
// than relocating it to album scope, and a second run must find nothing to do.
func TestLintFixKeepsMatroskaTrackScope(t *testing.T) {
	file := copyFixture(t, "../../testdata/sample.webm")

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
	value := strings.Index(blocks, "Lavc61.19.101 libopus")
	if marker < 0 || value < 0 {
		t.Fatalf("native view lost the track group or the codec encoder:\n%s", dump)
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
	if string(before) != string(after) {
		t.Errorf("a second lint --fix rewrote the file (%d -> %d bytes)", len(before), len(after))
	}
}
