package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// webmCleanTrackEncoder copies sample.webm with its track-scoped ENCODER replaced by a real
// encoder name of the same byte length, so every enclosing EBML size stays valid. The
// shipped value is itself a stamp, which --fix now clears, leaving no survivor to check
// scope retention on.
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

// TestLintFixKeepsMatroskaTrackScope: the file carries a muxer ENCODER at album scope and a
// real encoder name at track scope, so lint flags the cross-scope conflict and --fix resolves
// it to the clean value. That fix must leave the surviving value in the track Tag block it
// already lives in rather than relocating it to album scope, and a second run must find
// nothing to do.
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

// TestLintFixClearsMatroskaStampPair: the shipped fixture's ENCODER is a stamp at both
// scopes ("Lavf61.7.100" muxer, "Lavc61.19.101 libopus" codec). Both are transcoder stamps,
// so --fix clears the key outright - the same result FLAC, MP3, MP4, WAV and WavPack already
// give - rather than keeping the Lavc value and calling the file clean.
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
