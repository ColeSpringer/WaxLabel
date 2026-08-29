package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintReportsChapterIssues: set raises chapter-past-duration and duplicate-chapter on
// its own input, and set --help tells the user to lint the saved file. lint has to answer,
// or that pointer is a dead end. Both are warnings, so the file lints non-clean.
func TestLintReportsChapterIssues(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	// sample.flac is ~1 s, so 1:00:00 is unambiguously past the end. Two chapters at 0:00
	// collide. Both are written faithfully; they are defects to report, not to refuse.
	if _, errb, code := runCLI(t, "set", f,
		"--add-chapter", "0:00=A", "--add-chapter", "0:00.000=B", "--add-chapter", "1:00:00=Late"); code != 0 {
		t.Fatalf("writing the chapters: exit = %d\n%s", code, errb)
	}

	out, _, code := runCLI(t, "--json", "lint", f)
	if code != 1 {
		t.Fatalf("lint exit = %d, want 1 (findings present)\n%s", code, out)
	}
	jl := decodeJSONList[jsonLint](t, out)
	if len(jl) != 1 {
		t.Fatalf("want one lint result, got %d: %s", len(jl), out)
	}
	want := map[string]bool{"chapter-past-duration": false, "duplicate-chapter": false}
	for _, f := range jl[0].Findings {
		if _, ok := want[f.Code]; ok {
			want[f.Code] = true
			if f.Severity != "warning" {
				t.Errorf("%s severity = %q, want warning", f.Code, f.Severity)
			}
		}
	}
	for code, found := range want {
		if !found {
			t.Errorf("lint did not report %s:\n%s", code, out)
		}
	}
}

// TestLintChapterCleanFileStaysClean is the control for the check above: a file whose
// chapters are distinct and inside the duration reports neither code, so the new coverage
// does not flip every chaptered file to exit 1.
func TestLintChapterCleanFileStaysClean(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "lint", sampleM4B)
	if code > 1 {
		t.Fatalf("lint exit = %d\n%s", code, out)
	}
	jl := decodeJSONList[jsonLint](t, out)
	if len(jl) != 1 {
		t.Fatalf("want one lint result, got %d: %s", len(jl), out)
	}
	for _, f := range jl[0].Findings {
		if f.Code == "chapter-past-duration" || f.Code == "duplicate-chapter" {
			t.Errorf("a well-formed chapter list should report no %s: %+v", f.Code, f)
		}
	}
}

// TestLintReportsChainedStream: dump names the chained stream and then tells the user to
// run lint "for the full issue set". A partial read plus a refused write is exactly what a
// linter should surface, so lint must report it too.
func TestLintReportsChainedStream(t *testing.T) {
	t.Parallel()
	first, err := os.ReadFile(td("sample.ogg"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(td("notags.ogg"))
	if err != nil {
		t.Fatal(err)
	}
	chained := filepath.Join(t.TempDir(), "chained.ogg")
	if err := os.WriteFile(chained, append(append([]byte{}, first...), second...), 0o644); err != nil {
		t.Fatal(err)
	}

	dump, _, _ := runCLI(t, "dump", chained)
	if !strings.Contains(dump, "chained-stream") {
		t.Fatalf("setup: dump should already name the chained stream:\n%s", dump)
	}
	out, _, code := runCLI(t, "--json", "lint", chained)
	if code != 1 {
		t.Fatalf("lint exit = %d, want 1\n%s", code, out)
	}
	jl := decodeJSONList[jsonLint](t, out)
	if len(jl) != 1 {
		t.Fatalf("want one lint result, got %d: %s", len(jl), out)
	}
	found := false
	for _, f := range jl[0].Findings {
		if f.Code == "chained-stream" {
			found = true
			if f.Severity != "warning" {
				t.Errorf("chained-stream severity = %q, want warning", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("lint did not report chained-stream:\n%s", out)
	}
}
