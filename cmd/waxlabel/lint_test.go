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
	if !strings.Contains(out, "inherited-encoder") {
		t.Errorf("lint output missing inherited-encoder:\n%s", out)
	}

	cout, _, ccode := runCLI(t, "lint", notagsFLAC)
	if ccode != 0 {
		t.Fatalf("clean lint exit = %d, want 0", ccode)
	}
	if !strings.Contains(cout, "no issues") {
		t.Errorf("clean lint missing 'no issues':\n%s", cout)
	}
}

// TestDumpLintCodeAlignment: a condition both dump and lint surface uses
// the same code and message in each. dump prints the parse-warning codes; lint now
// reuses them verbatim (inherited-encoder, trailing-id3v1) instead of its old private
// aliases (encoder-noise, stale-legacy-tag), and one shared builder makes the
// conflicting-families message read identically. dump also signposts lint for the
// computed-only checks it does not run.
func TestDumpLintCodeAlignment(t *testing.T) {
	t.Parallel()

	// dump and lint name the same conditions with the same codes on an MP3 that
	// carries an inherited encoder stamp and a trailing ID3v1.
	dumpOut, _, _ := runCLI(t, "dump", sampleMP3)
	lintOut, _, _ := runCLI(t, "lint", sampleMP3)
	for _, code := range []string{"inherited-encoder", "trailing-id3v1"} {
		if !strings.Contains(dumpOut, code) {
			t.Errorf("dump missing %q:\n%s", code, dumpOut)
		}
		if !strings.Contains(lintOut, code) {
			t.Errorf("lint missing %q:\n%s", code, lintOut)
		}
	}
	// The old private aliases must be gone from lint.
	for _, gone := range []string{"encoder-noise", "stale-legacy-tag"} {
		if strings.Contains(lintOut, gone) {
			t.Errorf("lint still uses the retired code %q:\n%s", gone, lintOut)
		}
	}

	// the conflicting-families condition reads identically in dump and lint (shared
	// wording + the same " (KEY)" suffix). chapters.mka carries a cross-target ENCODER
	// conflict.
	mka := filepath.Join("..", "..", "testdata", "chapters.mka")
	dumpMka, _, _ := runCLI(t, "dump", mka)
	lintMka, _, _ := runCLI(t, "lint", mka)
	msg := "multiple source fields supplied conflicting values (ENCODER)"
	if !strings.Contains(dumpMka, msg) || !strings.Contains(lintMka, msg) {
		t.Errorf("conflicting-families should read identically in dump and lint:\ndump:\n%s\nlint:\n%s", dumpMka, lintMka)
	}
	// The lint finding keeps the key structured in JSON (it is a real tag key, unlike the
	// keyless picture findings), so a consumer can read it without parsing the message.
	lintJSON, _, _ := runCLI(t, "--json", "lint", mka)
	jl := decodeJSONOne[jsonLint](t, lintJSON)
	foundKey := false
	for _, f := range jl.Findings {
		if f.Code == "conflicting-families" {
			foundKey = true
			if f.Key != "ENCODER" {
				t.Errorf("conflicting-families JSON key = %q, want ENCODER", f.Key)
			}
		}
	}
	if !foundKey {
		t.Error("expected a conflicting-families finding in lint --json")
	}

	// dump signposts lint when it surfaced warnings, and stays quiet on a clean file.
	if !strings.Contains(dumpOut, `run "waxlabel lint"`) {
		t.Errorf("dump with warnings should point at lint:\n%s", dumpOut)
	}
	if cleanDump, _, _ := runCLI(t, "dump", notagsFLAC); strings.Contains(cleanDump, "waxlabel lint") {
		t.Errorf("a clean dump should not show the lint pointer:\n%s", cleanDump)
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
	// sampleFLAC has warning findings (would be exit 1); the missing file is exit 6.
	if _, _, code := runCLI(t, "lint", sampleFLAC, missing); code != 6 {
		t.Errorf("exit = %d, want 6 (structural error outranks warning findings)", code)
	}
}

// TestLintErrorSeverityExitsInvalidData verifies that an error-severity finding
// such as no-audio exits 4 (invalid-data), the same class verify gives a no-audio
// file and distinct from a warning's exit 1. The error-finding sentinel is folded
// into the same worseError comparison as a structural error, so a no-audio plus
// not-found run reports exit 4: a broken file outranks a wrong path. This is the
// control-flow case that a warning plus not-found run would not catch.
func TestLintErrorSeverityExitsInvalidData(t *testing.T) {
	t.Parallel()
	// A single no-audio file: an error-severity finding -> exit 4.
	if _, _, code := runCLI(t, "lint", emptyMP3); code != 4 {
		t.Errorf("lint no-audio exit = %d, want 4 (invalid-data)", code)
	}
	// no-audio (exit 4, rank 80) beside a missing file (exit 6, rank 55): the broken
	// file wins, so the aggregate is exit 4 - not the wrong path's exit 6.
	missing := filepath.Join(t.TempDir(), "nope.flac")
	if _, _, code := runCLI(t, "lint", emptyMP3, missing); code != 4 {
		t.Errorf("lint (no-audio + not-found) exit = %d, want 4 (broken file outranks wrong path)", code)
	}
	// A warning-only file still exits 1 (the prior behavior, preserved).
	if _, _, code := runCLI(t, "lint", sampleFLAC); code != 1 {
		t.Errorf("lint warning-only exit = %d, want 1", code)
	}
}

// TestLintFixNeutralizesFlacVendor checks that --fix clears the canonical ENCODER comment
// and neutralizes a transcoder-stamped FLAC vendor string. The saved file should re-lint
// cleanly.
func TestLintFixNeutralizesFlacVendor(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	out, _, code := runCLI(t, "lint", "--fix", file)
	if code != 0 {
		t.Fatalf("lint --fix exit = %d, want 0 (vendor stamp now neutralized):\n%s", code, out)
	}
	if !strings.Contains(out, "ENCODER") {
		t.Errorf("--fix output missing the ENCODER change:\n%s", out)
	}
	// A fresh lint should not see either the ENCODER comment or the vendor stamp.
	relint, _, rcode := runCLI(t, "lint", file)
	if rcode != 0 {
		t.Errorf("re-lint after --fix exit = %d, want 0 (clean):\n%s", rcode, relint)
	}
	if strings.Contains(relint, "inherited") || strings.Contains(relint, "transcoder") {
		t.Errorf("an inherited-encoder finding survived --fix:\n%s", relint)
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
	if !strings.Contains(out, "ID3v1 strip") {
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
	if !slices.Contains(jf.Operations, "ID3v1 strip") {
		t.Errorf("--fix JSON operations missing the ID3v1 strip: %v", jf.Operations)
	}
}

// TestLintFixNothingToFix is a regression guard: on a clean file --fix has nothing to do, so it
// prints "nothing to fix" (the NoOpPlan "no changes" sentinel no longer masks that branch) and
// the --json operations array is empty rather than leaking the sentinel.
func TestLintFixNothingToFix(t *testing.T) {
	t.Parallel()
	out, _, _ := runCLI(t, "lint", "--fix", copyFixture(t, td("notags.mp3"))) // no lint findings
	if !strings.Contains(out, "nothing to fix") {
		t.Errorf("clean --fix should print 'nothing to fix'; got:\n%s", out)
	}
	if strings.Contains(out, "no changes") {
		t.Errorf("the NoOpPlan 'no changes' sentinel leaked into --fix text output:\n%s", out)
	}
	jout, _, _ := runCLI(t, "--json", "lint", "--fix", copyFixture(t, td("notags.mp3")))
	jf := decodeJSONOne[jsonLintFix](t, jout)
	if len(jf.Operations) != 0 {
		t.Errorf("clean --fix JSON operations = %v, want empty (no 'no changes' sentinel leak)", jf.Operations)
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

// TestDumpEmptyMP3NoAudio documents dump's read contract: a tag-only MP3 renders
// metadata successfully, exits 0, and reports the no-audio condition as a warning.
func TestDumpEmptyMP3NoAudio(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", emptyMP3)
	if code != 0 {
		t.Fatalf("dump exit = %d, want 0 (a successful read of a no-audio file)", code)
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

// TestDumpJSONSchemaVersion: per-file dump objects now carry schemaVersion.
func TestDumpJSONSchemaVersion(t *testing.T) {
	t.Parallel()
	out, _, _ := runCLI(t, "--json", "dump", sampleFLAC)
	jd := decodeJSONOne[jsonDocument](t, out)
	if jd.SchemaVersion != schemaVersion {
		t.Errorf("dump schemaVersion = %d, want %d", jd.SchemaVersion, schemaVersion)
	}
}
