package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLintReportsFindings: noisy file exits 1; clean file exits 0.
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

// TestDumpLintCodeAlignment: dump and lint share parse-warning codes and conflicting-families
// wording; dump signposts lint for computed-only checks.
func TestDumpLintCodeAlignment(t *testing.T) {
	t.Parallel()

	// inherited-encoder and trailing-id3v1: same codes in dump and lint.
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
	// Retired lint-only aliases must not appear.
	for _, gone := range []string{"encoder-noise", "stale-legacy-tag"} {
		if strings.Contains(lintOut, gone) {
			t.Errorf("lint still uses the retired code %q:\n%s", gone, lintOut)
		}
	}

	// conflicting-families: identical wording in dump and lint (chapters.mka ENCODER conflict).
	mka := filepath.Join("..", "..", "testdata", "chapters.mka")
	dumpMka, _, _ := runCLI(t, "dump", mka)
	lintMka, _, _ := runCLI(t, "lint", mka)
	msg := "multiple source fields supplied conflicting values (ENCODER)"
	if !strings.Contains(dumpMka, msg) || !strings.Contains(lintMka, msg) {
		t.Errorf("conflicting-families should read identically in dump and lint:\ndump:\n%s\nlint:\n%s", dumpMka, lintMka)
	}
	// lint --json keeps conflicting-families Key structured (real tag key, unlike picture findings).
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

	// dump signposts lint when warnings exist; clean dump stays quiet.
	if !strings.Contains(dumpOut, `run "waxlabel lint"`) {
		t.Errorf("dump with warnings should point at lint:\n%s", dumpOut)
	}
	if cleanDump, _, _ := runCLI(t, "dump", notagsFLAC); strings.Contains(cleanDump, "waxlabel lint") {
		t.Errorf("a clean dump should not show the lint pointer:\n%s", cleanDump)
	}
}

// TestLintJSON: --json carries schemaVersion and findings.
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

// TestLintStructuralErrorOutranksFindings: structural failure (missing file) beats exit-1 findings.
func TestLintStructuralErrorOutranksFindings(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	// sampleFLAC would exit 1; missing file exits 6.
	if _, _, code := runCLI(t, "lint", sampleFLAC, missing); code != 6 {
		t.Errorf("exit = %d, want 6 (structural error outranks warning findings)", code)
	}
}

// TestLintErrorSeverityExitsInvalidData: error-severity finding (no-audio) exits 4, distinct
// from warning exit 1. no-audio plus not-found still exits 4 (broken file outranks wrong path).
func TestLintErrorSeverityExitsInvalidData(t *testing.T) {
	t.Parallel()
	// Single no-audio file: exit 4.
	if _, _, code := runCLI(t, "lint", emptyMP3); code != 4 {
		t.Errorf("lint no-audio exit = %d, want 4 (invalid-data)", code)
	}
	// no-audio (4) beside missing file (6): broken file wins; aggregate exit 4.
	missing := filepath.Join(t.TempDir(), "nope.flac")
	if _, _, code := runCLI(t, "lint", emptyMP3, missing); code != 4 {
		t.Errorf("lint (no-audio + not-found) exit = %d, want 4 (broken file outranks wrong path)", code)
	}
	// Warning-only file still exits 1.
	if _, _, code := runCLI(t, "lint", sampleFLAC); code != 1 {
		t.Errorf("lint warning-only exit = %d, want 1", code)
	}
}

// TestLintFixNeutralizesFlacVendor: --fix clears ENCODER and neutralizes transcoder vendor; re-lint clean.
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
	// Re-lint must not see ENCODER comment or vendor stamp.
	relint, _, rcode := runCLI(t, "lint", file)
	if rcode != 0 {
		t.Errorf("re-lint after --fix exit = %d, want 0 (clean):\n%s", rcode, relint)
	}
	if strings.Contains(relint, "inherited") || strings.Contains(relint, "transcoder") {
		t.Errorf("an inherited-encoder finding survived --fix:\n%s", relint)
	}
}

// TestLintFixFullyCleans: all-fixable findings end clean after --fix and re-lint.
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

// TestLintFixReportsOperations: --fix surfaces structural ops (e.g. ID3v1 strip), not just field changes.
func TestLintFixReportsOperations(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleMP3) // has ID3v1 trailer that --fix strips
	out, _, _ := runCLI(t, "lint", "--fix", file)
	if !strings.Contains(out, "ID3v1 strip") {
		t.Errorf("--fix text output missing the ID3v1 strip operation:\n%s", out)
	}
}

// TestLintFixJSONOperations: --fix JSON includes operations (aligned with plan/set).
func TestLintFixJSONOperations(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleMP3)
	out, _, _ := runCLI(t, "--json", "lint", "--fix", file)
	jf := decodeJSONOne[jsonLintFix](t, out)
	if !slices.Contains(jf.Operations, "ID3v1 strip") {
		t.Errorf("--fix JSON operations missing the ID3v1 strip: %v", jf.Operations)
	}
}

// TestLintFixNothingToFix: clean file prints "nothing to fix"; NoOpPlan "no changes" must not leak.
func TestLintFixNothingToFix(t *testing.T) {
	t.Parallel()
	out, _, _ := runCLI(t, "lint", "--fix", copyFixture(t, td("notags.mp3"))) // no findings
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

// TestLintFixNoAudioGraceful: no-audio file cannot be written; --fix reports "nothing to fix /
// left unchanged", exits 4, no Prepare refusal or JSON error envelope.
func TestLintFixNoAudioGraceful(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "lint", "--fix", copyFixture(t, td("empty.mp3")))
	if code != 4 {
		t.Errorf("lint --fix on a no-audio file exit = %d, want 4 (no-audio finding remains)", code)
	}
	for _, want := range []string{"nothing to fix", "no-audio", "left unchanged"} {
		if !strings.Contains(out, want) {
			t.Errorf("lint --fix on a no-audio file missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "refusing to write") {
		t.Errorf("lint --fix leaked the opaque Prepare write refusal:\n%s", out)
	}

	jout, _, jcode := runCLI(t, "--json", "lint", "--fix", copyFixture(t, td("empty.mp3")))
	if jcode != 4 {
		t.Errorf("--json lint --fix on a no-audio file exit = %d, want 4", jcode)
	}
	jf := decodeJSONOne[jsonLintFix](t, jout)
	if jf.Error != nil {
		t.Errorf("--json lint --fix on a no-audio file emitted an error envelope %+v, want the finding in remaining", jf.Error)
	}
	if jf.Committed {
		t.Error("--json lint --fix on a no-audio file reported committed=true, want false (left unchanged)")
	}
	var sawNoAudio bool
	for _, f := range jf.Remaining {
		if f.Code == "no-audio" {
			sawNoAudio = true
		}
	}
	if !sawNoAudio {
		t.Errorf("--json lint --fix remaining = %+v, want a no-audio finding", jf.Remaining)
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

// TestPlanChangesPreview: plan shows field-level preview in text and JSON.
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

// TestDumpEmptyMP3NoAudio: tag-only MP3 dumps successfully (exit 0) with no-audio warning.
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

// TestVerifyEmptyMP3Exit4: zero-essence verify fails (exit 4), no fake digest.
func TestVerifyEmptyMP3Exit4(t *testing.T) {
	t.Parallel()
	if _, _, code := runCLI(t, "verify", emptyMP3); code != 4 {
		t.Errorf("verify exit = %d, want 4", code)
	}
}

// TestDumpJSONSchemaVersion: dump --json objects carry schemaVersion.
func TestDumpJSONSchemaVersion(t *testing.T) {
	t.Parallel()
	out, _, _ := runCLI(t, "--json", "dump", sampleFLAC)
	jd := decodeJSONOne[jsonDocument](t, out)
	if jd.SchemaVersion != schemaVersion {
		t.Errorf("dump schemaVersion = %d, want %d", jd.SchemaVersion, schemaVersion)
	}
}

// TestLintJSONFindingFixable: fixable marker in JSON so scripts need not hardcode codes.
func TestLintJSONFindingFixable(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", f, "--set", "MYCUSTOM=x"); code != 0 {
		t.Fatalf("seed exit %d", code)
	}
	out, _, _ := runCLI(t, "--json", "lint", f)
	seen := map[string]bool{}
	for _, jl := range decodeJSONList[jsonLint](t, out) {
		for _, fd := range jl.Findings {
			seen[fd.Code] = true
			switch fd.Code {
			case "inherited-encoder":
				if !fd.Fixable {
					t.Errorf("inherited-encoder should report fixable: %+v", fd)
				}
			case "custom-key":
				if fd.Fixable {
					t.Errorf("custom-key is not auto-fixed and must not report fixable: %+v", fd)
				}
			}
		}
	}
	if !seen["inherited-encoder"] || !seen["custom-key"] {
		t.Errorf("fixture should lint both codes; saw %v", seen)
	}
}
