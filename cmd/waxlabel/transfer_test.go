package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// Extra cross-format fixtures (shared FLAC/M4B ones live in cli_test.go).
var (
	notagsM4A   = filepath.Join("..", "..", "testdata", "notags.m4a")
	sampleMP3   = filepath.Join("..", "..", "testdata", "sample.mp3")
	notagsOpus  = filepath.Join("..", "..", "testdata", "notags.opus")
	sampleWAV   = filepath.Join("..", "..", "testdata", "sample.wav")
	notagsAIFF  = filepath.Join("..", "..", "testdata", "notags.aiff")
	notagsMKA   = filepath.Join("..", "..", "testdata", "notags.mka")
	sampleMKA   = filepath.Join("..", "..", "testdata", "sample.mka")  // carries a cover
	sampleWebMF = filepath.Join("..", "..", "testdata", "sample.webm") // DocType webm
)

// assertCopyAgrees runs copy and checks carried fields match source values; dropped chapters stay absent.
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
	// Write record is embedded like set --json (operations and byte sizes required).
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
	return decodeJSONOne[jsonDocument](t, out)
}

// TestCopyReportMatchesResult: representative format pairs plus chapter-dropping M4B->FLAC.
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

// TestCopySyncedLyricsNewlineReportsLossy: SYLT newline flattens to space in LRC store; copy
// must grade lossy. CLI cannot author embedded newlines; source built via library.
func TestCopySyncedLyricsNewlineReportsLossy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := copyFixture(t, filepath.Join("..", "..", "testdata", "notags.mp3"))
	doc, err := wl.ParseFile(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	set := wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: 0, Text: "line one\nstill line one"}}}
	plan, err := doc.Edit().SetSyncedLyrics(set).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	dst := copyFixture(t, notagsFLAC)
	out, _, code := runCLI(t, "--json", "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy exit = %d, want 0:\n%s", code, out)
	}
	var jc jsonCopy
	if err := json.Unmarshal([]byte(out), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, out)
	}
	found := false
	for _, it := range jc.Transfer {
		if it.Kind == "synced lyrics" {
			found = true
			if it.Disposition != "lossy" {
				t.Errorf("synced-lyrics disposition = %q, want lossy (embedded newline flattened by the LRC store)", it.Disposition)
			}
		}
	}
	if !found {
		t.Fatalf("copy report has no synced-lyrics item:\n%s", out)
	}
}

// TestCopyDateReductionReasonNotWrong: v2.3 date loss varies by component; transfer reason must
// stay component-agnostic (per-value [value-reduced] carries specifics).
func TestCopyDateReductionReasonNotWrong(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, date := range []string{"2021-06", "2021-06-15T10", "2021-06-15T10:30:45"} {
		src := copyFixture(t, notagsFLAC) // stores date verbatim
		doc, err := wl.ParseFile(ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := doc.Edit().Set(tag.RecordingDate, date).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
			t.Fatal(err)
		}

		dst := copyFixture(t, sampleMP3)
		out, _, code := runCLI(t, "--json", "copy", src, dst)
		if code != 0 {
			t.Fatalf("copy %s exit = %d:\n%s", date, code, out)
		}
		var jc jsonCopy
		if err := json.Unmarshal([]byte(out), &jc); err != nil {
			t.Fatalf("copy %s JSON: %v\n%s", date, err, out)
		}
		found := false
		for _, it := range jc.Transfer {
			if it.Key == "RECORDINGDATE" {
				found = true
				if it.Disposition != "lossy" {
					t.Errorf("%s: RECORDINGDATE disposition = %s, want lossy", date, it.Disposition)
				}
				if strings.Contains(it.Reason, "seconds") {
					t.Errorf("%s: transfer reason names a specific component (%q); it must be component-agnostic", date, it.Reason)
				}
			}
		}
		if !found {
			t.Fatalf("%s: no RECORDINGDATE transfer item:\n%s", date, out)
		}
	}
}

// TestCopyDryRunLeavesDestUnchanged: --dry-run previews without writing.
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

// TestCopyToMatroskaSucceeds: copy onto Matroska writes tags and exits 0.
func TestCopyToMatroskaSucceeds(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, notagsMKA)
	out, _, code := runCLI(t, "copy", sampleFLAC, dst)
	if code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "carried") {
		t.Errorf("expected a carried report:\n%s", out)
	}
	// Destination reads back source title.
	got := tagValues(dumpJSON(t, dst), "TITLE")
	want := tagValues(dumpJSON(t, sampleFLAC), "TITLE")
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Errorf("copied TITLE = %v, want %v", got, want)
	}
}

// TestCopyCoverToWebMDropsCover: WebM drops cover (Attachments outside subset) but carries tags.
// Before the file-aware fix, copy reported carried then failed at Prepare.
func TestCopyCoverToWebMDropsCover(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, sampleWebMF)
	cout, _, code := runCLI(t, "--json", "copy", sampleMKA, dst)
	if code != 0 {
		t.Fatalf("copy %s -> webm exit = %d, want 0\n%s", sampleMKA, code, cout)
	}
	var jc jsonCopy
	if err := json.Unmarshal([]byte(cout), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, cout)
	}

	var pic jsonTransferItem
	var sawPic, carried bool
	for _, it := range jc.Transfer {
		switch {
		case it.Kind == "picture":
			pic, sawPic = it, true
		case it.Kind == "field" && it.Disposition == "carried":
			carried = true
		}
	}
	if !sawPic {
		t.Fatal("expected a picture transfer item (the source carries a cover)")
	}
	if pic.Disposition != "dropped" {
		t.Errorf("cover disposition = %s, want dropped", pic.Disposition)
	}
	if pic.Reason == "" {
		t.Error("a dropped cover must carry a reason")
	}
	if !carried {
		t.Error("expected at least one carried tag (the cover gate must not block tags)")
	}

	// Report matches result: no picture, title carried.
	res := dumpJSON(t, dst)
	if len(res.Pictures) != 0 {
		t.Errorf("WebM result has %d pictures, want 0 (the cover was dropped)", len(res.Pictures))
	}
	got := tagValues(res, "TITLE")
	want := tagValues(dumpJSON(t, sampleMKA), "TITLE")
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Errorf("copied TITLE = %v, want source %v", got, want)
	}
}

// TestCopyHeaderDistinguishesWebM: Matroska family header uses container label (WebM vs Matroska).
func TestCopyHeaderDistinguishesWebM(t *testing.T) {
	t.Parallel()
	// dst webm: "Matroska -> WebM".
	dst := copyFixture(t, sampleWebMF)
	out, _, code := runCLI(t, "copy", sampleMKA, dst, "--dry-run")
	if code != 0 {
		t.Fatalf("copy mka -> webm exit = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "transfer Matroska -> WebM") {
		t.Errorf("webm destination should show 'Matroska -> WebM':\n%s", out)
	}
	// src webm: "WebM -> Matroska".
	dst2 := copyFixture(t, notagsMKA)
	out2, _, code2 := runCLI(t, "copy", sampleWebMF, dst2, "--dry-run")
	if code2 != 0 {
		t.Fatalf("copy webm -> mka exit = %d, want 0:\n%s", code2, out2)
	}
	if !strings.Contains(out2, "transfer WebM -> Matroska") {
		t.Errorf("webm source should show 'WebM -> Matroska':\n%s", out2)
	}
}

// TestDiffExitCodes: diff-style exits (0 identical, 1 differs).
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

// TestDiffGoldenOutput: -/+/~ markers against known fixture deltas.
func TestDiffGoldenOutput(t *testing.T) {
	t.Parallel()
	// Keys in A (sample), absent in B (notags).
	out, _, code := runCLI(t, "diff", sampleFLAC, notagsFLAC)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"- TITLE: Original Title", "- ARTIST: Original Artist"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q\n%s", want, out)
		}
	}

	// Same file, one edited title.
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

// TestDiffJSON: machine-readable diff shape and exit codes.
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
	// Identical pair: well-formed object, exit 0.
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
	// Count objects always present with changed discriminator; identical pair: all changed:false.
	for label, c := range map[string]jsonDiffCount{"pictures": ij.Pictures, "chapters": ij.Chapters, "syncedLyrics": ij.SyncedLyrics} {
		if c.Changed {
			t.Errorf("identical pair: %s.changed = true, want false", label)
		}
	}
}

// TestDiffErrorsRankAboveDifferences: real failures exceed exit 1 (scripts distinguish broke vs differs).
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

// TestRenderCountDelta: picture/chapter delta rendering; equal count must not read as no-op.
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

// TestDiffPictureContentsDiffer: one cover each, different bytes; must not read as "1 -> 1".
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

	// JSON: equal counts need pictures.changed, not an a!=b guess.
	jout, _, jcode := runCLI(t, "--json", "diff", a, b)
	if jcode != 1 {
		t.Fatalf("json exit = %d, want 1", jcode)
	}
	var jd jsonDiff
	if err := json.Unmarshal([]byte(jout), &jd); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jout)
	}
	if jd.Pictures.A != 1 || jd.Pictures.B != 1 || !jd.Pictures.Changed {
		t.Errorf("pictures = %+v, want {A:1 B:1 Changed:true} (equal count, contents differ)", jd.Pictures)
	}
}

// minimalPNG: 1x1 PNG, enough to sniff as image/png.
func minimalPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
	}
}

// minimalJPEG: minimal JPEG header, distinct MIME from minimalPNG (last-wins cover tests).
func minimalJPEG() []byte {
	return []byte{
		0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08,
		0x00, 0x05, 0x00, 0x03, 0x03,
		0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}
}

// TestCopyDiffArgCounts: copy and diff require exactly two paths.
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
