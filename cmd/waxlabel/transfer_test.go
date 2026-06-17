package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Additional cross-format fixtures (the shared FLAC/M4B ones live in cli_test.go).
var (
	notagsM4A  = filepath.Join("..", "..", "testdata", "notags.m4a")
	sampleMP3  = filepath.Join("..", "..", "testdata", "sample.mp3")
	notagsOpus = filepath.Join("..", "..", "testdata", "notags.opus")
	sampleWAV  = filepath.Join("..", "..", "testdata", "sample.wav")
	notagsAIFF = filepath.Join("..", "..", "testdata", "notags.aiff")
	notagsMKA  = filepath.Join("..", "..", "testdata", "notags.mka")
)

// assertCopyAgrees runs "copy src dst" for real and checks the strongest
// contract: every field the JSON report marks carried actually lands in the
// written destination with the source's exact values, and a dropped chapter set
// leaves no chapters behind. The report and the bytes Execute produced cannot
// disagree.
func assertCopyAgrees(t *testing.T, src, dstFixture string) {
	t.Helper()
	dst := copyFixture(t, dstFixture)

	cout, _, code := runCLI(t, "--json", "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy %s -> %s exit = %d, want 0\n%s", src, dstFixture, code, cout)
	}
	var jc jsonCopy
	if err := json.Unmarshal([]byte(cout), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, cout)
	}
	if !jc.Committed {
		t.Errorf("%s -> %s: copy reported not committed", src, dstFixture)
	}
	// The write record is embedded (like set's --json), not a hand-copied subset:
	// a real cross-format copy writes tags, so it reports operations and sizes.
	if !jc.NoOp && len(jc.Operations) == 0 {
		t.Errorf("%s -> %s: copy --json omitted write operations", src, dstFixture)
	}
	if jc.BytesAfter == 0 {
		t.Errorf("%s -> %s: copy --json omitted byte sizes", src, dstFixture)
	}

	srcDoc := dumpJSON(t, src)
	resDoc := dumpJSON(t, dst)

	carried := 0
	for _, it := range jc.Transfer {
		switch {
		case it.Kind == "field" && it.Disposition == "carried":
			carried++
			want := tagValues(srcDoc, it.Key)
			got := tagValues(resDoc, it.Key)
			if !slices.Equal(got, want) {
				t.Errorf("%s -> %s: carried %s = %v, want source values %v", src, dstFixture, it.Key, got, want)
			}
		case it.Kind == "chapter" && it.Disposition == "dropped":
			if len(resDoc.Chapters) != 0 {
				t.Errorf("%s -> %s: chapters reported dropped but result has %d", src, dstFixture, len(resDoc.Chapters))
			}
		}
	}
	if carried == 0 {
		t.Errorf("%s -> %s: expected at least one carried field", src, dstFixture)
	}
}

func dumpJSON(t *testing.T, path string) jsonDocument {
	t.Helper()
	out, _, code := runCLI(t, "--json", "dump", path)
	if code != 0 {
		t.Fatalf("dump %s exit = %d", path, code)
	}
	var jd jsonDocument
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("dump JSON for %s: %v", path, err)
	}
	return jd
}

// TestCopyReportMatchesResult covers the representative format pairs the plan
// names (FLAC->MP4, MP3->Opus, WAV->AIFF) plus the chapter-dropping M4B->FLAC.
func TestCopyReportMatchesResult(t *testing.T) {
	t.Parallel()
	pairs := []struct{ src, dst string }{
		{sampleFLAC, notagsM4A},
		{sampleMP3, notagsOpus},
		{sampleWAV, notagsAIFF},
		{sampleM4B, notagsFLAC}, // tags carry, chapters drop
	}
	for _, p := range pairs {
		assertCopyAgrees(t, p.src, p.dst)
	}
}

// TestCopyDryRunLeavesDestUnchanged: --dry-run previews but writes nothing.
func TestCopyDryRunLeavesDestUnchanged(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, notagsM4A)
	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLI(t, "copy", sampleFLAC, dst, "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "transfer FLAC -> MP4") || !strings.Contains(out, "Dry run") {
		t.Errorf("dry-run output unexpected:\n%s", out)
	}
	after, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("dry-run modified the destination")
	}
}

// TestCopyToReadOnlyFormatFails: copying onto a not-yet-writable format reports
// every item dropped and fails with the unsupported-format exit code.
func TestCopyToReadOnlyFormatFails(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, notagsMKA)
	out, _, code := runCLI(t, "copy", sampleFLAC, dst)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (unsupported-format)", code)
	}
	// The loss report still prints so the user sees why nothing transferred.
	if !strings.Contains(out, "dropped") || !strings.Contains(out, "read-only") {
		t.Errorf("expected a dropped/read-only report:\n%s", out)
	}
}

// TestDiffExitCodes pins the diff(1)-style contract: 0 identical, 1 differs.
func TestDiffExitCodes(t *testing.T) {
	t.Parallel()
	if _, _, code := runCLI(t, "diff", sampleFLAC, sampleFLAC); code != 0 {
		t.Errorf("identical exit = %d, want 0", code)
	}
	if _, _, code := runCLI(t, "diff", sampleFLAC, notagsFLAC); code != 1 {
		t.Errorf("differ exit = %d, want 1", code)
	}
	// --quiet: only the exit code, no output.
	qout, qerr, code := runCLI(t, "diff", "-q", sampleFLAC, notagsFLAC)
	if code != 1 {
		t.Errorf("quiet differ exit = %d, want 1", code)
	}
	if qout != "" || qerr != "" {
		t.Errorf("--quiet should print nothing, got stdout=%q stderr=%q", qout, qerr)
	}
	if _, _, code := runCLI(t, "diff", "-q", sampleFLAC, sampleFLAC); code != 0 {
		t.Errorf("quiet identical exit = %d, want 0", code)
	}
}

// TestDiffGoldenOutput checks the -/+/~ markers against known fixture deltas.
func TestDiffGoldenOutput(t *testing.T) {
	t.Parallel()
	// Removed keys: present in A (sample), absent in B (notags).
	out, _, code := runCLI(t, "diff", sampleFLAC, notagsFLAC)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"- TITLE: Original Title", "- ARTIST: Original Artist"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q\n%s", want, out)
		}
	}

	// A controlled changed value: same file, one edited title.
	a := copyFixture(t, sampleFLAC)
	b := copyFixture(t, sampleFLAC)
	if _, _, c := runCLI(t, "set", b, "--set", "TITLE=Changed"); c != 0 {
		t.Fatalf("set exit = %d", c)
	}
	cout, _, code := runCLI(t, "diff", a, b)
	if code != 1 {
		t.Fatalf("changed-diff exit = %d, want 1", code)
	}
	if !strings.Contains(cout, "~ TITLE: Original Title -> Changed") {
		t.Errorf("expected a changed-title line:\n%s", cout)
	}
}

// TestDiffJSON checks the machine-readable diff shape and exit code.
func TestDiffJSON(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "diff", sampleFLAC, notagsFLAC)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	var jd jsonDiff
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if jd.Identical {
		t.Error("identical = true, want false")
	}
	if len(jd.Tags) == 0 {
		t.Error("expected tag diffs")
	}
	// An identical pair still emits a well-formed object, with exit 0.
	iout, _, code := runCLI(t, "--json", "diff", sampleFLAC, sampleFLAC)
	if code != 0 {
		t.Fatalf("identical exit = %d, want 0", code)
	}
	var ij jsonDiff
	if err := json.Unmarshal([]byte(iout), &ij); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, iout)
	}
	if !ij.Identical {
		t.Error("identical = false, want true")
	}
}

// TestDiffErrorsRankAboveDifferences: a real failure (missing file, junk input)
// must exceed exit 1 so scripts can tell "differs" from "broke".
func TestDiffErrorsRankAboveDifferences(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	if _, _, code := runCLI(t, "diff", sampleFLAC, missing); code != 6 {
		t.Errorf("missing-file exit = %d, want 6 (io)", code)
	}
	junk := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(junk, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCLI(t, "diff", sampleFLAC, junk); code != 3 {
		t.Errorf("junk-file exit = %d, want 3 (unsupported-format)", code)
	}
}

// TestRenderCountDelta pins the picture/chapter delta rendering, especially the
// equal-count case where a bare "N -> N" would read as a no-op.
func TestRenderCountDelta(t *testing.T) {
	t.Parallel()
	cases := []struct {
		differ bool
		a, b   int
		want   string
	}{
		{true, 1, 1, "  pictures: 1 (contents differ)\n"},
		{true, 3, 0, "  pictures: 3 -> 0\n"},
		{true, 0, 2, "  pictures: 0 -> 2\n"},
		{false, 1, 1, ""},
	}
	for _, tc := range cases {
		var b bytes.Buffer
		renderCountDelta(&b, "pictures", tc.differ, tc.a, tc.b)
		if got := b.String(); got != tc.want {
			t.Errorf("renderCountDelta(%v,%d,%d) = %q, want %q", tc.differ, tc.a, tc.b, got, tc.want)
		}
	}
}

// TestDiffPictureContentsDiffer: two files with one cover each but different bytes
// must not read as "1 -> 1" (a no-op); the diff says the contents differ.
func TestDiffPictureContentsDiffer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pngA := filepath.Join(dir, "a.png")
	pngB := filepath.Join(dir, "b.png")
	if err := os.WriteFile(pngA, minimalPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pngB, append(minimalPNG(), 0x00), 0o644); err != nil {
		t.Fatal(err)
	}

	a := copyFixture(t, notagsFLAC)
	b := copyFixture(t, notagsFLAC)
	if _, _, c := runCLI(t, "set", a, "--add-cover", pngA); c != 0 {
		t.Fatalf("set a exit = %d", c)
	}
	if _, _, c := runCLI(t, "set", b, "--add-cover", pngB); c != 0 {
		t.Fatalf("set b exit = %d", c)
	}

	out, _, code := runCLI(t, "diff", a, b)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "pictures: 1 (contents differ)") {
		t.Errorf("expected a contents-differ line:\n%s", out)
	}
}

// minimalPNG is a 1x1 PNG, enough to sniff as image/png for a cover.
func minimalPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
	}
}

// TestCopyUsageErrors: copy and diff need exactly two paths.
func TestCopyDiffArgCounts(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{"copy", sampleFLAC},
		{"diff", sampleFLAC},
		{"copy"},
		{"diff", sampleFLAC, notagsFLAC, sampleFLAC},
	}
	for _, args := range cases {
		if _, _, code := runCLI(t, args...); code != 2 {
			t.Errorf("%v exit = %d, want 2 (usage)", args, code)
		}
	}
}
