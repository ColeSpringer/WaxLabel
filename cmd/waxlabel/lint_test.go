package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLintReportsFindings: a file with noise lints non-clean (exit 1); a clean
// file exits 0.
func TestLintReportsFindings(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "lint", sampleFLAC)
	if code != 1 {
		t.Fatalf("lint exit = %d, want 1", code)
	}
	if !strings.Contains(out, "encoder-noise") {
		t.Errorf("lint output missing encoder-noise:\n%s", out)
	}

	cout, _, ccode := runCLI(t, "lint", notagsFLAC)
	if ccode != 0 {
		t.Fatalf("clean lint exit = %d, want 0", ccode)
	}
	if !strings.Contains(cout, "no issues") {
		t.Errorf("clean lint missing 'no issues':\n%s", cout)
	}
}

// TestLintJSON: the machine-readable shape carries the schema version and findings.
func TestLintJSON(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "lint", sampleFLAC)
	if code != 1 {
		t.Fatalf("lint --json exit = %d, want 1", code)
	}
	jl := decodeJSONOne[jsonLint](t, out)
	if jl.SchemaVersion != schemaVersion {
		t.Errorf("schemaVersion = %d, want %d", jl.SchemaVersion, schemaVersion)
	}
	if len(jl.Findings) == 0 {
		t.Error("expected findings")
	}
}

// TestLintStructuralErrorOutranksFindings: a structural failure (missing file)
// must outrank an exit-1 "issues found" so a script can tell them apart.
func TestLintStructuralErrorOutranksFindings(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	// sampleFLAC has findings (would be exit 1); the missing file is exit 6.
	if _, _, code := runCLI(t, "lint", sampleFLAC, missing); code != 6 {
		t.Errorf("exit = %d, want 6 (structural error outranks findings)", code)
	}
}

// TestLintFixHonest: --fix clears what it can and re-lints, so a stamp it cannot
// reach (the FLAC vendor string) is reported as remaining and keeps exit 1.
func TestLintFixHonest(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	out, _, code := runCLI(t, "lint", "--fix", file)
	if code != 1 {
		t.Fatalf("lint --fix exit = %d, want 1 (vendor stamp remains)", code)
	}
	if !strings.Contains(out, "ENCODER") {
		t.Errorf("--fix output missing the ENCODER change:\n%s", out)
	}
	if !strings.Contains(out, "not auto-fixed") {
		t.Errorf("--fix output missing remaining finding:\n%s", out)
	}
	// The ENCODER comment is actually gone now; re-lint must not report it again.
	relint, _, _ := runCLI(t, "lint", file)
	if strings.Contains(relint, "inherited encoder comment") {
		t.Errorf("ENCODER comment survived --fix:\n%s", relint)
	}
}

// TestLintFixFullyCleans: a file whose every finding is fixable ends clean
// (exit 0) and re-lints clean.
func TestLintFixFullyCleans(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleMP3)
	if _, _, code := runCLI(t, "lint", "--fix", file); code != 0 {
		t.Fatalf("lint --fix exit = %d, want 0", code)
	}
	if _, _, code := runCLI(t, "lint", file); code != 0 {
		t.Errorf("re-lint after fix exit = %d, want 0 (clean)", code)
	}
}

// TestLintFixReportsOperations: --fix surfaces the structural operations it
// performed (stripping a legacy ID3v1 trailer), not just the field changes -
// otherwise a legacy-container strip is invisible despite being saved.
func TestLintFixReportsOperations(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleMP3) // carries an ID3v1 trailer that --fix strips
	out, _, _ := runCLI(t, "lint", "--fix", file)
	if !strings.Contains(out, "stripped ID3v1") {
		t.Errorf("--fix text output missing the ID3v1 strip operation:\n%s", out)
	}
}

// TestLintFixJSONOperations: the structural operations also appear in the JSON
// result's operations array, bringing --fix in line with plan/set.
func TestLintFixJSONOperations(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleMP3)
	out, _, _ := runCLI(t, "--json", "lint", "--fix", file)
	jf := decodeJSONOne[jsonLintFix](t, out)
	if !slices.Contains(jf.Operations, "stripped ID3v1") {
		t.Errorf("--fix JSON operations missing the ID3v1 strip: %v", jf.Operations)
	}
}

// TestSetStripEncoder: --strip-encoder clears the ENCODER tag.
func TestSetStripEncoder(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", file, "--strip-encoder"); code != 0 {
		t.Fatalf("set --strip-encoder exit = %d, want 0", code)
	}
	out, _, _ := runCLI(t, "--json", "dump", file)
	jd := decodeJSONOne[jsonDocument](t, out)
	if vals := tagValues(jd, "ENCODER"); vals != nil {
		t.Errorf("ENCODER survived --strip-encoder: %v", vals)
	}
}

// TestPlanChangesPreview: plan shows the field-level change preview in text and
// JSON.
func TestPlanChangesPreview(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "plan", sampleFLAC, "--set", "TITLE=New", "--clear", "ENCODER")
	if code != 0 {
		t.Fatalf("plan exit = %d, want 0", code)
	}
	for _, want := range []string{"changes:", "TITLE: Original Title -> New", "ENCODER"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan preview missing %q:\n%s", want, out)
		}
	}

	jout, _, _ := runCLI(t, "--json", "plan", sampleFLAC, "--set", "TITLE=New", "--clear", "ENCODER")
	jr := decodeJSONOne[jsonReport](t, jout)
	if len(jr.Changes) != 2 {
		t.Fatalf("plan JSON changes = %v, want 2", jr.Changes)
	}
}

// TestDumpEmptyMP3NoAudio: a tag-only MP3 surfaces the no-audio warning in dump.
func TestDumpEmptyMP3NoAudio(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", emptyMP3)
	if code != 0 {
		t.Fatalf("dump exit = %d, want 0", code)
	}
	if !strings.Contains(out, "no-audio") {
		t.Errorf("dump missing no-audio warning:\n%s", out)
	}
}

// TestVerifyEmptyMP3Exit4: verifying a zero-essence file fails (exit 4) instead
// of minting a fake digest.
func TestVerifyEmptyMP3Exit4(t *testing.T) {
	t.Parallel()
	if _, _, code := runCLI(t, "verify", emptyMP3); code != 4 {
		t.Errorf("verify exit = %d, want 4", code)
	}
}

// TestDumpJSONSchemaVersion: per-file dump objects now carry schemaVersion (U6).
func TestDumpJSONSchemaVersion(t *testing.T) {
	t.Parallel()
	out, _, _ := runCLI(t, "--json", "dump", sampleFLAC)
	jd := decodeJSONOne[jsonDocument](t, out)
	if jd.SchemaVersion != schemaVersion {
		t.Errorf("dump schemaVersion = %d, want %d", jd.SchemaVersion, schemaVersion)
	}
}
