package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fixturePath builds a testdata path for fixtures without a shared constant.
func fixturePath(name string) string { return filepath.Join("..", "..", "testdata", name) }

// lintHasEncoderNoise reports whether a lint of path still flags the inherited
// encoder stamp (finding code inherited-encoder, the canonical parse-warning code
// lint now reuses).
func lintHasEncoderNoise(t *testing.T, path string) bool {
	t.Helper()
	out, _, _ := runCLI(t, "lint", path)
	return strings.Contains(out, "inherited-encoder")
}

// --- WAV ISFT encoder stamp is clearable from the CLI ---

// TestWAVEncoderStampClearedBySetEdits checks each of the three set-side triggers drops
// the WAV ISFT transcoder stamp so a re-lint is clean of inherited-encoder.
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

// TestWAVLintFixClearsEncoderStamp confirms lint --fix reaches the WAV ISFT stamp and a
// re-lint is clean of inherited-encoder.
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

// TestWAVSetEncoderNoSplitBrain checks that setting ENCODER drops the old ISFT stamp
// rather than leaving a new id3 ENCODER beside a surviving ISFT (split-brain).
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

// --- native counts render with a unit, not as bytes ---

// TestNativeOggPagesUnit checks the Ogg "audio pages" count renders as "N pages", never
// as a byte size.
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

// TestNativeMatroskaCountsAndNoBareBytes checks a Matroska native view renders the Tag
// count with a "tags" unit and shows no misleading bare "0 B" (the EBML header and
// Info.Title have no byte size).
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

// nativeLine returns the first native-blocks line containing kind.
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

// --- chapter writes on former refusal fixtures ---
//
// The chapter-unsupported CLI path is not reachable for these fixtures: MP3/AAC/AIFF/WAV
// use ID3 CHAP/CTOC, FLAC/Ogg use VorbisComment CHAPTERxxx, and MP4/Matroska use their
// native chapter stores. The indefinite-article helper is covered in internal/core.

// TestAddChapterAcrossFormats checks that --add-chapter succeeds on the formats that used
// to reject chapters, and that the chapter survives a re-parse.
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

// --- long values elided in human output, full in JSON ---

// TestLongValueElidedHumanFullJSON checks a pathologically long value is elided (with a
// length hint) in the human plan preview, while --json keeps the exact bytes.
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

// --- set -o - is rejected ---

// TestSetOutputDashRejected checks "set -o -" is a usage error, not a write to a file
// literally named "-".
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

// --- caps --format webm reports the cover-refusing variant ---

// TestCapsFormatWebM checks caps --format webm is accepted and reports cover write as
// unsupported, while plain matroska still reports it writable.
func TestCapsFormatWebM(t *testing.T) {
	out, _, code := runCLI(t, "caps", "--format", "webm")
	if code != 0 {
		t.Fatalf("caps --format webm exit %d", code)
	}
	if !strings.Contains(out, "pictures: read full, write none") {
		t.Errorf("caps --format webm should report pictures write none:\n%s", out)
	}
	mka, _, _ := runCLI(t, "caps", "--format", "matroska")
	if !strings.Contains(mka, "pictures: read full, write full") {
		t.Errorf("caps --format matroska should still report pictures write full:\n%s", mka)
	}
}

// firstLines returns the first n lines of s, for compact failure output.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
