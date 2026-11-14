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
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
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

// TestFormatLabelKeepsTheSpecificName: a container name stands in for the format only when it
// says more than the format's own name. "Ogg" says less than "Ogg Vorbis", so the three Ogg
// mappings keep their names in the copy header, dump and caps; WMA is still labelled ASF.
func TestFormatLabelKeepsTheSpecificName(t *testing.T) {
	t.Parallel()
	for src, label := range map[string]string{
		"sample.ogg": "Ogg Vorbis", "sample.opus": "Ogg Opus", "sample.oga": "Ogg FLAC", "sample.wma": "ASF",
	} {
		out, _, code := runCLI(t, "copy", td(src), copyFixture(t, notagsFLAC), "--dry-run")
		if code != 0 || !strings.Contains(out, "transfer "+label+" -> FLAC\n") {
			t.Errorf("copy %s -> flac exit %d, want the header to read 'transfer %s -> FLAC':\n%s", src, code, label, out)
		}
		for _, cmd := range []string{"dump", "caps"} {
			if out, _, _ := runCLI(t, cmd, td(src)); !lineWithFields(out, "format:", label) {
				t.Errorf("%s %s does not print 'format: %s':\n%s", cmd, src, label, out)
			}
		}
	}
	if out, _, _ := runCLI(t, "copy", sampleFLAC, copyFixture(t, td("notags.opus")), "--dry-run"); !strings.Contains(out, "transfer FLAC -> Ogg Opus\n") {
		t.Errorf("an Ogg destination is not named:\n%s", out)
	}
}

// lineWithFields reports whether out has a line that reads label then value, whatever the spacing.
func lineWithFields(out, label, value string) bool {
	for line := range strings.Lines(out) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), label); ok && strings.TrimSpace(rest) == value {
			return true
		}
	}
	return false
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

// TestDiffErrorsRankAboveDifferences: failures exit above 1 (scripts distinguish broke vs differs).
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
	if err := os.WriteFile(pngA, testfile.PNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pngB, append(testfile.PNG(), 0x00), 0o644); err != nil {
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

// copy X X writes nothing and loses nothing for every fixture, the read-only and no-audio ones
// too, and --strict agrees.
func TestCopyOntoItselfIsANoOp(t *testing.T) {
	t.Parallel()
	fixtures, err := filepath.Glob(filepath.Join("..", "..", "testdata", "*"))
	if err != nil || len(fixtures) < 40 {
		t.Fatalf("fixtures: %d found, err %v", len(fixtures), err)
	}
	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			t.Parallel()
			path := copyFixture(t, fixture)
			old := time.Now().Add(-time.Hour).Truncate(time.Second)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			out, stderr, code := runCLI(t, "--json", "copy", "--strict", path, path)
			if code != 0 {
				t.Fatalf("exit = %d\n%s\n%s", code, out, stderr)
			}
			var jc jsonCopy
			if err := json.Unmarshal([]byte(out), &jc); err != nil {
				t.Fatalf("copy JSON: %v\n%s", err, out)
			}
			if !jc.NoOp || jc.Committed || jc.Error != nil {
				t.Errorf("noOp %v, committed %v, error %+v; want a no-op", jc.NoOp, jc.Committed, jc.Error)
			}
			for _, it := range jc.Transfer {
				if it.Disposition == "lossy" || it.Disposition == "dropped" {
					t.Errorf("%s %s is %s (%s)", it.Kind, it.Key, it.Disposition, it.Reason)
				}
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || !after.ModTime().Equal(old) {
				t.Errorf("the file was replaced or rewritten (modified %s, was %s)", after.ModTime(), old)
			}
			if human, _, code := runCLI(t, "copy", path, path); code != 0 || !strings.Contains(human, "no changes (already up to date)") {
				t.Errorf("human copy exit = %d\n%s", code, human)
			}
		})
	}
}

// A destination no write reaches gets the report first, with what it lacks dropped, then exit 3.
func TestCopyOntoChainedOggReportsThenRefuses(t *testing.T) {
	t.Parallel()
	first, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sample.ogg"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join("..", "..", "testdata", "notags.ogg"))
	if err != nil {
		t.Fatal(err)
	}
	dst := writeTempFile(t, "chained.ogg", append(first, second...))
	src := rawFLAC(t, "TITLE=Other", "GENRE=New")
	out, stderr, code := runCLI(t, "copy", src, dst)
	if code != 3 {
		t.Fatalf("exit = %d, want 3\n%s\n%s", code, out, stderr)
	}
	if !strings.Contains(out, "0 carried, 0 lossy, 2 dropped") || !strings.Contains(out, "dropped TITLE: destination is read-only") {
		t.Errorf("the report should come before the refusal with both fields dropped:\n%s", out)
	}
	if !strings.Contains(stderr, displayName(dst)) {
		t.Errorf("the refusal should name the destination:\n%s", stderr)
	}
	jout, _, code := runCLI(t, "--json", "copy", src, dst)
	var jc jsonCopy
	if err := json.Unmarshal([]byte(jout), &jc); err != nil || code != 3 {
		t.Fatalf("copy --json exit %d, JSON %v\n%s", code, err, jout)
	}
	if jc.Error == nil || jc.Error.Code != "unsupported-stream" || len(jc.Transfer) != 2 {
		t.Errorf("JSON: error %+v, %d transfer items; want the refusal beside the report", jc.Error, len(jc.Transfer))
	}
}

// A copy that could store nothing says so, where it used to print the dropped items and then
// "already up to date".
func TestCopyNothingStoredHeadline(t *testing.T) {
	t.Parallel()
	coverOnly := buildTransferSource(t, notagsMP3, func(e *wl.Editor) *wl.Editor {
		return e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()})
	})
	dst := copyFixture(t, sampleWebMF)
	out, stderr, code := runCLI(t, "copy", coverOnly, dst)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if !strings.Contains(out, "dropped pictures (1)") || !strings.Contains(out, "no changes written; 1 item(s) could not be stored") {
		t.Errorf("want the dropped picture and a headline that says nothing was stored:\n%s", out)
	}
	if strings.Contains(out, "already up to date") {
		t.Errorf("a copy that dropped its only item is not up to date:\n%s", out)
	}

	lyricsOnly := buildTransferSource(t, notagsMP3, func(e *wl.Editor) *wl.Editor {
		return e.SetSyncedLyrics(
			wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: time.Second, Text: "la"}}},
			wl.SyncedLyrics{Language: "deu", Lines: []wl.SyncedLine{{Time: time.Second, Text: "la"}}})
	})
	out, _, code = runCLI(t, "copy", lyricsOnly, copyFixture(t, notagsM4A))
	if code != 0 || !strings.Contains(out, "no changes written; 2 item(s) could not be stored") {
		t.Errorf("synced lyrics onto MP4, exit %d:\n%s", code, out)
	}
	// A copy with nothing to bring is still up to date.
	out, _, _ = runCLI(t, "copy", copyFixture(t, notagsMP3), copyFixture(t, notagsM4A))
	if !strings.Contains(out, "no changes (already up to date)") {
		t.Errorf("an empty copy:\n%s", out)
	}
	// So is a repeat of a copy that stored all the destination can hold: the second TITLE has
	// nowhere to go in Matroska, the lossy line says so, and no edit was "discarded".
	twoTitles := rawFLAC(t, "TITLE=T1", "TITLE=T2")
	mka := copyFixture(t, notagsMKA)
	if _, stderr, code := runCLI(t, "copy", twoTitles, mka); code != 0 {
		t.Fatalf("first copy exit = %d\n%s", code, stderr)
	}
	out, _, code = runCLI(t, "copy", twoTitles, mka)
	if code != 0 || !strings.Contains(out, "lossy   TITLE") || !strings.Contains(out, "no changes (already up to date)") ||
		!strings.Contains(out, "No changes; ") || strings.Contains(out, "discarded") {
		t.Errorf("repeat copy, exit %d:\n%s", code, out)
	}
}

// A copy the destination's writer refuses names the destination, in the per-file form the other
// commands use, and --json carries the refusal in the copy envelope beside the report.
func TestCopyRefusalNamesTheDestination(t *testing.T) {
	t.Parallel()
	src := buildTransferSource(t, filepath.Join("..", "..", "testdata", "notags.ogg"), func(e *wl.Editor) *wl.Editor {
		return e.Set(tag.Comment, strings.Repeat("c", 9<<20)).Set(tag.Lyrics, strings.Repeat("l", 9<<20))
	})
	dst := copyFixture(t, notagsFLAC)
	out, stderr, code := runCLI(t, "copy", src, dst)
	if code != 3 {
		t.Fatalf("exit = %d, want 3\n%s", code, stderr)
	}
	if want := "waxlabel: " + displayName(dst) + ": content too large"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to open %q", stderr, want)
	}
	if !strings.Contains(out, "2 carried, 0 lossy, 0 dropped") {
		t.Errorf("the report should still print:\n%s", out)
	}
	jout, _, code := runCLI(t, "--json", "copy", src, dst)
	var jc jsonCopy
	if err := json.Unmarshal([]byte(jout), &jc); err != nil || code != 3 {
		t.Fatalf("copy --json exit %d, JSON %v\n%.300s", code, err, jout)
	}
	if jc.Error == nil || jc.Error.Code != "content-too-large" || jc.File != dst || len(jc.Transfer) < 2 {
		t.Errorf("JSON: file %q, error %+v, %d transfer items", jc.File, jc.Error, len(jc.Transfer))
	}
}

// A copy whose save fails names the destination and its directory, and --json reports the
// failure in the copy envelope.
func TestCopyWriteFailureNamesTheDestination(t *testing.T) {
	t.Parallel()
	requireUnwritableDir(t)
	roDir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(notagsFLAC)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(roDir, "dst.flac")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) })
	src := rawFLAC(t, "TITLE=T")

	_, stderr, code := runCLI(t, "copy", src, dst)
	if code != 6 {
		t.Fatalf("exit = %d, want 6\n%s", code, stderr)
	}
	// An existing destination is written through its symlink-resolved path, so the directory
	// named is the resolved one: macOS temp dirs sit under /var, a link to /private/var.
	realDir, err := filepath.EvalSymlinks(roDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "waxlabel: "+displayName(dst)+": ") || !strings.Contains(stderr, "create temp file in "+realDir+": ") {
		t.Errorf("stderr should name the destination and its directory: %q", stderr)
	}
	if strings.Contains(stderr, ".waxlabel-") {
		t.Errorf("internal temp pattern should not leak: %q", stderr)
	}
	jout, _, code := runCLI(t, "--json", "copy", src, dst)
	var jc jsonCopy
	if err := json.Unmarshal([]byte(jout), &jc); err != nil || code != 6 {
		t.Fatalf("copy --json exit %d, JSON %v\n%s", code, err, jout)
	}
	if jc.Error == nil || jc.Error.Code != "io" || jc.Committed || len(jc.Transfer) != 1 {
		t.Errorf("JSON: error %+v, committed %v, %d transfer items; want the io failure beside the report", jc.Error, jc.Committed, len(jc.Transfer))
	}
	// The plan whose save failed is in the object too: what would have been written.
	if len(jc.Changes) != 1 || jc.Changes[0].Key != "TITLE" || jc.NoOp {
		t.Errorf("JSON: changes %+v, noOp %v; want the planned TITLE change", jc.Changes, jc.NoOp)
	}
}
