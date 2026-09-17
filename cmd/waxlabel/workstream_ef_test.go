package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fixturePath builds a testdata path without a shared constant.
func fixturePath(name string) string { return filepath.Join("..", "..", "testdata", name) }

// lintHasEncoderNoise reports whether lint still flags inherited-encoder.
func lintHasEncoderNoise(t *testing.T, path string) bool {
	t.Helper()
	out, _, _ := runCLI(t, "lint", path)
	return strings.Contains(out, "inherited-encoder")
}

// WAV ISFT encoder stamp clearable from CLI

// TestWAVEncoderStampClearedBySetEdits: set-side triggers clear ISFT; re-lint clean.
func TestWAVEncoderStampClearedBySetEdits(t *testing.T) {
	for _, args := range [][]string{
		{"--strip-encoder"},
		{"--clear", "ENCODER"},
		{"--set", "ENCODER=MyTool"},
	} {
		f := copyFixture(t, sampleWAV)
		if lintHasEncoderNoise(t, f) != true {
			t.Fatalf("setup: %s should start with an inherited-encoder finding", f)
		}
		if _, errb, code := runCLI(t, append([]string{"set", f}, args...)...); code != 0 {
			t.Fatalf("set %v: exit %d: %s", args, code, errb)
		}
		if lintHasEncoderNoise(t, f) {
			t.Errorf("set %v: inherited-encoder persists; the ISFT stamp was not cleared", args)
		}
	}
}

// TestWAVLintFixClearsEncoderStamp: lint --fix clears ISFT; re-lint clean.
func TestWAVLintFixClearsEncoderStamp(t *testing.T) {
	f := copyFixture(t, sampleWAV)
	if !lintHasEncoderNoise(t, f) {
		t.Fatalf("setup: %s should start with an inherited-encoder finding", f)
	}
	if _, errb, code := runCLI(t, "lint", "--fix", f); code != 0 && code != 1 {
		t.Fatalf("lint --fix: exit %d: %s", code, errb)
	}
	if lintHasEncoderNoise(t, f) {
		t.Error("lint --fix did not clear the WAV ISFT encoder stamp")
	}
}

// TestWAVSetEncoderNoSplitBrain: set ENCODER drops old ISFT, not split-brain with id3 ENCODER.
func TestWAVSetEncoderNoSplitBrain(t *testing.T) {
	f := copyFixture(t, sampleWAV)
	if _, errb, code := runCLI(t, "set", f, "--set", "ENCODER=MyTool"); code != 0 {
		t.Fatalf("set: exit %d: %s", code, errb)
	}
	out, _, _ := runCLI(t, "dump", "--native", f)
	if strings.Contains(out, "Lavf") || strings.Contains(out, "inherited-encoder") {
		t.Errorf("the inherited ISFT stamp survived alongside the new ENCODER:\n%s", out)
	}
	if !strings.Contains(out, "MyTool") {
		t.Error("the new ENCODER value was not written")
	}
}

// Native counts render with unit, not bytes

// TestNativeOggPagesUnit: Ogg audio pages as "N pages", never byte size.
func TestNativeOggPagesUnit(t *testing.T) {
	out, _, code := runCLI(t, "dump", "--native", fixturePath("sample.ogg"))
	if code != 0 {
		t.Fatalf("dump exit %d", code)
	}
	line := nativeLine(t, out, "audio pages")
	if !strings.Contains(line, "pages") {
		t.Errorf("audio-pages line has no 'pages' unit: %q", line)
	}
	if regexp.MustCompile(`\d+ B\b`).MatchString(line) {
		t.Errorf("audio-pages count rendered as bytes: %q", line)
	}
}

// TestNativeMatroskaCountsAndNoBareBytes: Tag count uses "tags" unit; no bare "0 B".
func TestNativeMatroskaCountsAndNoBareBytes(t *testing.T) {
	out, _, code := runCLI(t, "dump", "--native", sampleMKA)
	if code != 0 {
		t.Fatalf("dump exit %d", code)
	}
	if !strings.Contains(out, " tags") {
		t.Errorf("Matroska Tag count missing the 'tags' unit:\n%s", out)
	}
	if regexp.MustCompile(`\b0 B\b`).MatchString(out) {
		t.Errorf("Matroska native view still shows a bare '0 B':\n%s", out)
	}
}

// nativeLine returns first native-blocks line containing kind.
func nativeLine(t *testing.T, out, kind string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, kind) {
			return l
		}
	}
	t.Fatalf("no native line containing %q in:\n%s", kind, out)
	return ""
}

// Chapter writes on former refusal fixtures (now ID3/VorbisComment/native stores).

// TestAddChapterAcrossFormats: --add-chapter succeeds on formerly rejected formats; survives re-parse.
func TestAddChapterAcrossFormats(t *testing.T) {
	for _, fixture := range []string{notagsAIFF, notagsFLAC, fixturePath("sample.mp3")} {
		f := copyFixture(t, fixture)
		if _, errb, code := runCLI(t, "set", f, "--add-chapter", "0:01=Intro"); code != 0 {
			t.Fatalf("%s: set --add-chapter exit %d, want 0, stderr=%q", fixture, code, errb)
		}
		out, _, _ := runCLI(t, "dump", f)
		if !strings.Contains(out, "Intro") {
			t.Errorf("%s: chapter did not survive round-trip:\n%s", fixture, out)
		}
	}
}

// Long values elided in human output, full in JSON

// TestLongValueElidedHumanFullJSON: huge value elided in human plan; --json keeps exact bytes.
func TestLongValueElidedHumanFullJSON(t *testing.T) {
	f := copyFixture(t, sampleFLAC)
	big := strings.Repeat("x", 100000)
	out, _, code := runCLI(t, "plan", f, "--set", "COMMENT="+big)
	if code != 0 {
		t.Fatalf("plan exit %d", code)
	}
	if strings.Contains(out, big) {
		t.Error("human plan output was not elided")
	}
	if !strings.Contains(out, "…[+") {
		t.Errorf("human plan output has no elision hint:\n%s", firstLines(out, 6))
	}
	jout, _, _ := runCLI(t, "--json", "plan", f, "--set", "COMMENT="+big)
	if !strings.Contains(jout, big) {
		t.Error("--json plan dropped the full value")
	}
	if strings.Contains(jout, "…[+") {
		t.Error("--json plan leaked the elision hint into machine output")
	}
}

// set -o - rejected

// TestSetOutputDashRejected: set -o - is usage error, not a file named "-".
func TestSetOutputDashRejected(t *testing.T) {
	f := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", f, "--set", "TITLE=X", "-o", "-")
	if code != 2 {
		t.Fatalf("exit %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "-o -") {
		t.Errorf("message %q, want it to mention -o -", errb)
	}
}

// caps --format webm reports cover-refusing variant

// TestCapsFormatWebM: webm reports pictures write none; matroska still write full.
func TestCapsFormatWebM(t *testing.T) {
	out, _, code := runCLI(t, "caps", "--format", "webm")
	if code != 0 {
		t.Fatalf("caps --format webm exit %d", code)
	}
	if !strings.Contains(out, "pictures:      read full, write none") {
		t.Errorf("caps --format webm should report pictures write none:\n%s", out)
	}
	mka, _, _ := runCLI(t, "caps", "--format", "matroska")
	if !strings.Contains(mka, "pictures:      read full, write full") {
		t.Errorf("caps --format matroska should still report pictures write full:\n%s", mka)
	}
}

// firstLines returns first n lines for compact failure output.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
