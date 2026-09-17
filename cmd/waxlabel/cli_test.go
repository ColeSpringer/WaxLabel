package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

var (
	sampleFLAC = filepath.Join("..", "..", "testdata", "sample.flac")
	notagsFLAC = filepath.Join("..", "..", "testdata", "notags.flac")
	sampleM4B  = filepath.Join("..", "..", "testdata", "sample_chapters.m4b")
	emptyMP3   = filepath.Join("..", "..", "testdata", "empty.mp3") // tag-only/truncated MP3
	mp3MOV     = filepath.Join("..", "..", "testdata", "mp3.mov")   // MP3 in a QuickTime ".mp3" entry
	sampleWMA  = filepath.Join("..", "..", "testdata", "sample.wma")
	lossless24 = filepath.Join("..", "..", "testdata", "lossless24.wma")
)

// Fresh dispatch per call; safe for t.Parallel.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runCLIStdin(t, "", args...)
}

func runCLIStdin(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = dispatch(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), code
}

// Skip on root (ignores mode) and Windows (writability is ACL, not mode).
func requireUnwritableDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("windows: directory writability is an ACL, not a mode; os.Chmod cannot make a directory unwritable")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not prevent the write")
	}
}

func copyFixture(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dst
}

func tagValues(jd jsonDocument, key string) []string {
	for _, tg := range jd.Tags {
		if tg.Key == key {
			return tg.Values
		}
	}
	return nil
}

// List commands always emit a JSON array.
func decodeJSONList[T any](t *testing.T, data string) []T {
	t.Helper()
	var arr []T
	if err := json.Unmarshal([]byte(data), &arr); err != nil {
		t.Fatalf("expected a JSON array: %v\n%s", err, data)
	}
	return arr
}

func decodeJSONOne[T any](t *testing.T, data string) T {
	t.Helper()
	arr := decodeJSONList[T](t, data)
	if len(arr) != 1 {
		t.Fatalf("array len = %d, want 1\n%s", len(arr), data)
	}
	return arr[0]
}

func TestDumpText(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{
		"format:  FLAC",
		"44100 Hz, 2 ch, 16-bit",
		"TITLE",
		"Original Title",
		"[inherited-encoder]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

// properties.codec is canonical; raw spelling in codecProfile, omitted when already canonical.
func TestDumpJSONCodecCanonical(t *testing.T) {
	t.Parallel()
	sampleOpus := filepath.Join("..", "..", "testdata", "sample.opus")
	cases := []struct{ file, codec, profile string }{
		{sampleM4B, "AAC", "AAC LC"}, // esds object type
		{sampleFLAC, "FLAC", "flac"},
		{sampleOpus, "Opus", ""}, // canonical: no profile
		{mp3MOV, "MP3", ".mp3"},   // QuickTime fourcc in profile
	}
	for _, c := range cases {
		out, _, code := runCLI(t, "dump", c.file, "--json")
		if code != 0 {
			t.Fatalf("%s: exit = %d\n%s", c.file, code, out)
		}
		jd := decodeJSONOne[jsonDocument](t, out)
		if jd.Properties == nil {
			t.Errorf("%s: no properties block", c.file)
			continue
		}
		if jd.Properties.Codec != c.codec || jd.Properties.CodecProfile != c.profile {
			t.Errorf("%s: codec=%q profile=%q, want %q/%q", c.file, jd.Properties.Codec, jd.Properties.CodecProfile, c.codec, c.profile)
		}
	}
}

// Lossy codecs omit bitsPerSample (container depth is noise); lossless keeps real width.
func TestDumpJSONOmitsBitDepthForLossy(t *testing.T) {
	t.Parallel()
	for _, path := range []string{sampleM4B, sampleWMA, mp3MOV} {
		t.Run("lossy "+filepath.Base(path), func(t *testing.T) {
			out, _, code := runCLI(t, "dump", path, "--json")
			if code != 0 {
				t.Fatalf("exit = %d\n%s", code, out)
			}
			if strings.Contains(out, "bitsPerSample") {
				t.Errorf("a lossy codec's dump should omit bitsPerSample:\n%s", out)
			}
			jd := decodeJSONOne[jsonDocument](t, out)
			if jd.Properties == nil {
				t.Fatal("no properties block")
			}
			if jd.Properties.BitsPerSample != 0 {
				t.Errorf("bitsPerSample = %d, want 0 (omitted)", jd.Properties.BitsPerSample)
			}
		})
	}
	for _, c := range []struct {
		path string
		want int
	}{{sampleFLAC, 16}, {lossless24, 24}} {
		t.Run("lossless "+filepath.Base(c.path), func(t *testing.T) {
			out, _, code := runCLI(t, "dump", c.path, "--json")
			if code != 0 {
				t.Fatalf("exit = %d\n%s", code, out)
			}
			jd := decodeJSONOne[jsonDocument](t, out)
			if jd.Properties == nil {
				t.Fatal("no properties block")
			}
			if jd.Properties.BitsPerSample != c.want {
				t.Errorf("bitsPerSample = %d, want %d (a real depth is kept)", jd.Properties.BitsPerSample, c.want)
			}
		})
	}
}

// Text dump: same bit-depth gate as JSON for lossy codecs.
func TestDumpTextOmitsBitDepthForLossy(t *testing.T) {
	t.Parallel()
	for _, path := range []string{sampleWMA, mp3MOV} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			out, _, code := runCLI(t, "dump", path)
			if code != 0 {
				t.Fatalf("exit = %d\n%s", code, out)
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "audio:") && strings.Contains(line, "-bit") {
					t.Errorf("audio line shows a bit depth: %s", line)
				}
			}
		})
	}
}

// paddingBytes on formats with padding regions; picture depth/colors on sample.mka cover.
func TestDumpSurfacesPaddingAndPictureDepth(t *testing.T) {
	t.Parallel()

	ftext, _, code := runCLI(t, "dump", sampleFLAC)
	if code != 0 {
		t.Fatalf("dump flac exit %d", code)
	}
	if !strings.Contains(ftext, "padding:") {
		t.Errorf("FLAC dump text should show a padding: line:\n%s", ftext)
	}
	fout, _, _ := runCLI(t, "dump", sampleFLAC, "--json")
	fd := decodeJSONOne[jsonDocument](t, fout)
	if fd.Properties == nil || fd.Properties.PaddingBytes <= 0 {
		t.Errorf("FLAC paddingBytes should be > 0; got %+v", fd.Properties)
	}

	// ID3 padding via codec, not native scan.
	mout, _, _ := runCLI(t, "dump", sampleMP3, "--json")
	md := decodeJSONOne[jsonDocument](t, mout)
	if md.Properties == nil || md.Properties.PaddingBytes <= 0 {
		t.Errorf("MP3 paddingBytes should be > 0; got %+v", md.Properties)
	}

	wout, _, _ := runCLI(t, "dump", sampleWAV, "--json")
	if strings.Contains(wout, "paddingBytes") {
		t.Errorf("WAV dump should omit paddingBytes (no padding region):\n%s", wout)
	}

	pout, _, _ := runCLI(t, "dump", sampleMKA, "--json")
	pd := decodeJSONOne[jsonDocument](t, pout)
	if len(pd.Pictures) == 0 {
		t.Fatalf("sample.mka should carry a cover:\n%s", pout)
	}
	if pd.Pictures[0].Depth != 24 {
		t.Errorf("cover depth = %d, want 24", pd.Pictures[0].Depth)
	}
	if pd.Pictures[0].Colors != 0 {
		t.Errorf("a non-indexed PNG should report colors 0 (omitted), got %d", pd.Pictures[0].Colors)
	}
}

func TestDumpChapters(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", sampleM4B)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"chapters (3)", "Opening Credits", "Chapter One", "Chapter Two"} {
		if !strings.Contains(out, want) {
			t.Errorf("dump output missing %q\n--- got ---\n%s", want, out)
		}
	}
	jout, _, code := runCLI(t, "--json", "dump", sampleM4B)
	if code != 0 {
		t.Fatalf("json dump exit = %d, want 0", code)
	}
	jd := decodeJSONOne[jsonDocument](t, jout)
	if len(jd.Chapters) != 3 {
		t.Fatalf("json chapters = %d, want 3", len(jd.Chapters))
	}
	if jd.Chapters[1].Title != "Chapter One" || jd.Chapters[1].StartMs != 3000 {
		t.Errorf("json chapter 1 = %+v, want Chapter One @ 3000ms", jd.Chapters[1])
	}
}

func TestDumpJSON(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "dump", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	jd := decodeJSONOne[jsonDocument](t, out)
	if jd.Format != "FLAC" {
		t.Errorf("format = %q, want FLAC", jd.Format)
	}
	if jd.Properties == nil || jd.Properties.SampleRate != 44100 {
		t.Errorf("properties = %+v", jd.Properties)
	}
	if jd.Properties.BitrateBps <= 1000 {
		t.Errorf("bitrateBps = %d, want raw bits/sec (>1000)", jd.Properties.BitrateBps)
	}
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Original Title" {
		t.Errorf("TITLE = %v", got)
	}
	if len(jd.Warnings) == 0 {
		t.Errorf("expected inherited-encoder warnings, got none")
	}
}

func TestDumpNative(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", "--native", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"native blocks", "STREAMINFO", "VORBIS_COMMENT", "families", "vorbis"} {
		if !strings.Contains(out, want) {
			t.Errorf("native dump missing %q\n%s", want, out)
		}
	}
}

func TestPlanReportsOperationsWithoutWriting(t *testing.T) {
	t.Parallel()
	before, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLI(t, "plan", sampleFLAC, "--set", "TITLE=Changed")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "plan") || !strings.Contains(out, "size:") {
		t.Errorf("plan report unexpected:\n%s", out)
	}
	after, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("plan modified the source file")
	}
}

func TestPlanNoOp(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "plan", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "no changes") {
		t.Errorf("expected no-op report, got:\n%s", out)
	}
}

func TestSetRoundTrip(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)

	_, _, code := runCLI(t, "set", file,
		"--set", "TITLE=Brand New",
		"--add", "ARTIST=Featured",
		"--clear", "ENCODER",
		"--verify")
	if code != 0 {
		t.Fatalf("set exit = %d, want 0", code)
	}

	out, _, code := runCLI(t, "--json", "dump", file)
	if code != 0 {
		t.Fatalf("dump exit = %d, want 0", code)
	}
	jd := decodeJSONOne[jsonDocument](t, out)
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Brand New" {
		t.Errorf("TITLE = %v, want [Brand New]", got)
	}
	if got := tagValues(jd, "ARTIST"); len(got) != 2 || got[1] != "Featured" {
		t.Errorf("ARTIST = %v, want [Original Artist Featured]", got)
	}
	if got := tagValues(jd, "ENCODER"); got != nil {
		t.Errorf("ENCODER = %v, want absent (cleared)", got)
	}
}

func TestSetNoOpWritesNothing(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Re-set to current value -> no-op (bare set with no flags is usage error).
	out, _, code := runCLI(t, "set", file, "--set", "TITLE=Original Title")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "No changes") {
		t.Errorf("expected no-op outcome, got:\n%s", out)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("no-op set rewrote the file")
	}
}

// set with no edit flags is usage error; -o verbatim copy still allowed.
func TestSetNoEditsRejected(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, stderr, code := runCLI(t, "set", file)
	if code != 2 {
		t.Fatalf("set with no edits exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "no edits given") {
		t.Errorf("stderr = %q, want it to mention 'no edits given'", stderr)
	}
	if _, _, code := runCLI(t, "set", file, "--no-padding"); code != 0 { // write-shaping flag counts as edit
		t.Errorf("set --no-padding (re-pad in place) exit = %d, want 0", code)
	}
}

func TestSetSaveAsLeavesOriginal(t *testing.T) {
	t.Parallel()
	src := copyFixture(t, sampleFLAC)
	dst := filepath.Join(t.TempDir(), "out.flac")
	before, _ := os.ReadFile(src)

	_, _, code := runCLI(t, "set", src, "--set", "ALBUM=Compilation", "-o", dst)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("output not written: %v", err)
	}
	after, _ := os.ReadFile(src)
	if !bytes.Equal(before, after) {
		t.Error("save-as modified the source file")
	}

	out, _, _ := runCLI(t, "--json", "dump", dst)
	jd := decodeJSONOne[jsonDocument](t, out)
	if got := tagValues(jd, "ALBUM"); len(got) != 1 || got[0] != "Compilation" {
		t.Errorf("ALBUM = %v, want [Compilation]", got)
	}
}

// Edit through symlink updates target; link must stay a symlink.
func TestSetThroughSymlinkUpdatesTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real := filepath.Join(dir, "real.flac")
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, data, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.flac")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	if _, _, code := runCLI(t, "set", link, "--set", "TITLE=Linked"); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	if fi, err := os.Lstat(link); err != nil {
		t.Fatalf("lstat link: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	out, _, _ := runCLI(t, "--json", "dump", real)
	jd := decodeJSONOne[jsonDocument](t, out)
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Linked" {
		t.Errorf("target TITLE = %v, want [Linked]", got)
	}
}

func TestVerifyEssenceStableAcrossTagEdit(t *testing.T) {
	t.Parallel()
	out1, _, code := runCLI(t, "--json", "verify", sampleFLAC)
	if code != 0 {
		t.Fatalf("verify exit = %d", code)
	}
	v1 := decodeJSONOne[jsonVerify](t, out1)
	if !strings.HasPrefix(v1.Essence, "sha256/flac-frames-v2:") {
		t.Errorf("essence = %q", v1.Essence)
	}

	file := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", file, "--set", "TITLE=Whatever"); code != 0 {
		t.Fatalf("set exit = %d", code)
	}
	out2, _, _ := runCLI(t, "--json", "verify", file)
	v2 := decodeJSONOne[jsonVerify](t, out2)
	if v1.Essence != v2.Essence {
		t.Errorf("essence changed after tag edit:\n before %s\n after  %s", v1.Essence, v2.Essence)
	}
}

func TestVerifyWholeFileFlag(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "verify", "--whole-file", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "essence:") || !strings.Contains(out, "whole-file:") {
		t.Errorf("verify --whole-file output:\n%s", out)
	}
	if !strings.Contains(out, "whole-file-v1:") {
		t.Errorf("missing whole-file extent name:\n%s", out)
	}
}

// verify -q: essence<TAB>path per file, pipe-friendly.
func TestVerifyQuietTSV(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "verify", "-q", sampleFLAC, notagsFLAC)
	if code != 0 {
		t.Fatalf("verify -q exit = %d, want 0", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("verify -q produced %d lines, want exactly 2 (no blank separators):\n%q", len(lines), out)
	}
	for _, line := range lines {
		cols := strings.Split(line, "\t")
		if len(cols) != 2 {
			t.Errorf("line %q has %d tab-separated columns, want 2 (essence, path)", line, len(cols))
		}
		if !strings.HasPrefix(cols[0], "sha256/flac-frames-v2:") {
			t.Errorf("first column %q is not an essence digest", cols[0])
		}
	}
	if strings.Contains(out, "essence:") {
		t.Errorf("quiet output should not carry the labeled block:\n%s", out)
	}
}

// verify -q --whole-file: three tab columns.
func TestVerifyQuietWholeFileThreeColumns(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "verify", "-q", "--whole-file", sampleFLAC)
	if code != 0 {
		t.Fatalf("verify -q --whole-file exit = %d, want 0", code)
	}
	cols := strings.Split(strings.TrimRight(out, "\n"), "\t")
	if len(cols) != 3 {
		t.Fatalf("verify -q --whole-file columns = %d, want 3 (essence, whole-file, path):\n%q", len(cols), out)
	}
	if !strings.Contains(cols[1], "whole-file-v1:") {
		t.Errorf("second column %q should be the whole-file digest", cols[1])
	}
}

// --quiet is no-op under --json (stays JSON array).
func TestVerifyQuietNoOpUnderJSON(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "verify", "-q", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(out, "\t") {
		t.Errorf("--json verify -q should stay JSON, not TSV:\n%s", out)
	}
	v := decodeJSONList[jsonVerify](t, out)
	if len(v) != 1 || v[0].Essence == "" {
		t.Errorf("--json verify -q should emit a normal JSON array: %+v", v)
	}
}

// set -q silent on success.
func TestSetQuietSilentOnSuccess(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	out, errb, code := runCLI(t, "set", "-q", file, "--set", "TITLE=Quiet")
	if code != 0 {
		t.Fatalf("set -q exit = %d, want 0", code)
	}
	if out != "" || errb != "" {
		t.Errorf("set -q should be silent on success; stdout=%q stderr=%q", out, errb)
	}
	j, _, _ := runCLI(t, "--json", "dump", file)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Quiet" {
		t.Errorf("set -q did not apply the edit: TITLE = %v", got)
	}
}

// set -q: suppresses per-file output, keeps summary and errors.
func TestSetQuietKeepsSummaryAndErrors(t *testing.T) {
	t.Parallel()
	good := copyFixture(t, sampleFLAC)
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, errb, code := runCLI(t, "set", "-q", good, missing, "--set", "TITLE=X")
	if code == 0 {
		t.Fatalf("a missing file should fail the run; exit = %d", code)
	}
	if strings.Contains(out, "plan") || strings.Contains(out, "Saved") {
		t.Errorf("quiet stdout should omit the per-file preview/outcome:\n%s", out)
	}
	if strings.TrimRight(out, "\n") != "1 changed, 0 unchanged, 1 failed" {
		t.Errorf("quiet stdout should be just the summary, got:\n%q", out)
	}
	if !strings.Contains(errb, "nope.flac") {
		t.Errorf("quiet stderr should still report the failed file:\n%s", errb)
	}
}

// Multi-line tag values: continuation lines indented.
func TestMultiLineTagValueAligns(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", file, "--set", "LYRICS=line one\nline two"); code != 0 {
		t.Fatalf("set exit = %d", code)
	}
	out, _, code := runCLI(t, "dump", file)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Fatalf("both lyric lines should appear:\n%s", out)
	}
	if strings.Contains(out, "\nline two") {
		t.Errorf("continuation line not indented:\n%s", out)
	}
}

// Exit-code and machine-code mapping for scripts.
func TestClassifyError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		code int
		mc   string
	}{
		{"canceled", context.Canceled, 130, "canceled"},
		{"deadline", context.DeadlineExceeded, 130, "timeout"},
		{"rename", &os.LinkError{Op: "rename", Err: errors.New("x")}, 6, "io"},
		{"open", &fs.PathError{Op: "open", Err: errors.New("x")}, 6, "io"},
		{"not-found", &fs.PathError{Op: "open", Path: "/x.flac", Err: fs.ErrNotExist}, 6, "not-found"},
		// Not-exist without PathError stays io: not-found needs PathError for clean message.
		{"rename-not-found", &os.LinkError{Op: "rename", Err: fs.ErrNotExist}, 6, "io"},
		{"usage", &usageError{msg: "bad"}, 2, "usage"},
		{"invalid-key", fmt.Errorf("w: %w", waxerr.ErrInvalidKey), 2, "invalid-key"},
		{"unsupported", fmt.Errorf("w: %w", waxerr.ErrUnsupportedFormat), 3, "unsupported-format"},
		{"chained-stream", fmt.Errorf("w: %w", waxerr.ErrChainedStream), 3, "unsupported-stream"},
		{"unaligned-stream", fmt.Errorf("w: %w", waxerr.ErrUnalignedStream), 3, "unsupported-alignment"},
		{"fragmented", fmt.Errorf("w: %w", waxerr.ErrFragmented), 3, "unsupported-fragmentation"},
		{"picture-too-large", fmt.Errorf("w: %w", waxerr.ErrPictureTooLarge), 3, "picture-too-large"}, // write refusal, not corruption (exit 4)
		{"invalid-data", fmt.Errorf("w: %w", waxerr.ErrInvalidData), 4, "invalid-data"},
		{"input-too-large", fmt.Errorf("w: %w", waxerr.ErrInputTooLarge), 7, "input-too-large"},
		{"source-changed", fmt.Errorf("w: %w", waxerr.ErrSourceChanged), 5, "source-changed"},
		{"unclassified", errors.New("boom"), 1, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := classifyError(tc.err)
			if c.exitCode != tc.code || c.code != tc.mc {
				t.Errorf("classify = (%d,%q), want (%d,%q)", c.exitCode, c.code, tc.code, tc.mc)
			}
		})
	}
}

// not-found message form; wrapped PathError classifies as io, not flattened to not-found.
func TestClassifyNotFoundMessage(t *testing.T) {
	t.Parallel()
	bare := &fs.PathError{Op: "open", Path: "/x.flac", Err: fs.ErrNotExist}
	if c := classifyError(bare); c.message != "/x.flac: no such file or directory" {
		t.Errorf("bare message = %q, want %q", c.message, "/x.flac: no such file or directory")
	}

	// pictureLoadError: io/exit 6, bare cause in message.
	wrapped := &pictureLoadError{label: "cover image", path: "/x.png", err: &fs.PathError{Op: "open", Path: "/x.png", Err: fs.ErrNotExist}}
	c := classifyError(wrapped)
	if c.code != "io" || c.exitCode != 6 {
		t.Errorf("wrapped class = (%d,%q), want (6,\"io\")", c.exitCode, c.code)
	}
	if want := "cover image: /x.png: " + notFoundReason; c.message != want {
		t.Errorf("wrapped message = %q, want %q", c.message, want)
	}
	if strings.Contains(c.message, "open") {
		t.Errorf("wrapped message still carries Go's \"open\" verb: %q", c.message)
	}
}

// Library sentinels must not embed "waxlabel:" prefix.
func TestSentinelsHaveNoProgramPrefix(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		waxerr.ErrUnsupportedFormat, waxerr.ErrInvalidData,
		waxerr.ErrUnsupportedTag, waxerr.ErrPictureTooLarge, waxerr.ErrSizeTooLarge,
		waxerr.ErrInputTooLarge, waxerr.ErrTooDeep, waxerr.ErrSourceChanged,
		waxerr.ErrChainedStream, waxerr.ErrUnalignedStream, waxerr.ErrFragmented,
		waxerr.ErrInvalidKey,
	} {
		if strings.HasPrefix(err.Error(), "waxlabel:") {
			t.Errorf("sentinel %q should not embed the program prefix", err.Error())
		}
	}
}

// Missing file: path once, no raw "open <path>:".
func TestDumpMissingFilePathOnce(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	_, errb, code := runCLI(t, "dump", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	if n := strings.Count(errb, missing); n != 1 {
		t.Errorf("path should appear exactly once, got %d:\n%s", n, errb)
	}
	if strings.Contains(errb, "open "+missing) {
		t.Errorf("raw 'open <path>:' should be gone:\n%s", errb)
	}
}

func TestPlanMissingFileMessage(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	_, errb, code := runCLI(t, "plan", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	if want := "waxlabel: " + missing + ": "; !strings.Contains(errb, want) {
		t.Errorf("stderr = %q, want it to contain %q", errb, want)
	}
	if n := strings.Count(errb, missing); n != 1 {
		t.Errorf("path should appear exactly once, got %d:\n%s", n, errb)
	}
	if strings.Contains(errb, "open "+missing) {
		t.Errorf("raw 'open <path>:' should be gone:\n%s", errb)
	}
}

// stderr per-file line matches JSON file+message. Benign names (control bytes would test sanitizer).
func TestPerFileStderrAndJSONAgree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.flac")
	// fLaC + truncated STREAMINFO: invalid-data, not unsupported-format.
	corrupt := filepath.Join(dir, "corrupt.flac")
	if err := os.WriteFile(corrupt, []byte("fLaC\x00\x00\x00\x22truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ code, path string }{
		{"not-found", missing},
		{"invalid-data", corrupt},
	} {
		t.Run(c.code, func(t *testing.T) {
			_, errb, humanCode := runCLI(t, "dump", c.path)
			stdout, _, jsonCode := runCLI(t, "--json", "dump", c.path)
			entry := decodeJSONOne[jsonErrorEntry](t, stdout)
			if entry.Error.Code != c.code {
				t.Fatalf("JSON code = %q, want %q\n%s", entry.Error.Code, c.code, stdout)
			}
			if humanCode != jsonCode {
				t.Errorf("exit codes disagree: human %d, --json %d", humanCode, jsonCode)
			}
			if want := fmt.Sprintf("waxlabel: %s: %s\n", entry.File, entry.Error.Message); errb != want {
				t.Errorf("stderr = %q, want %q (the JSON element's own file and message)", errb, want)
			}
		})
	}
}

// Directory without --recursive is usage error, not invalid-data.
func TestDirectoryAsInput(t *testing.T) {
	t.Parallel()
	_, errb, code := runCLI(t, "dump", t.TempDir())
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "is a directory") {
		t.Errorf("stderr should explain the directory: %q", errb)
	}
	if !strings.Contains(errb, "--recursive") {
		t.Errorf("stderr should point at --recursive: %q", errb)
	}
}

// Temp-create error names destination dir, not internal .waxlabel- pattern.
func TestTempCreateErrorNamesDir(t *testing.T) {
	t.Parallel()
	requireUnwritableDir(t)
	file := copyFixture(t, sampleFLAC)
	roDir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) })
	_, errb, code := runCLI(t, "set", file, "--set", "TITLE=X", "-o", filepath.Join(roDir, "out.flac"))
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	if !strings.Contains(errb, "create temp file in "+roDir) {
		t.Errorf("stderr should name the destination dir: %q", errb)
	}
	if strings.Contains(errb, ".waxlabel-") {
		t.Errorf("internal temp pattern should not leak: %q", errb)
	}
}

// Missing -o parent: not-found before plan. File-as-parent: usage error.
func TestSetOutputParentDirMissing(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)

	missing := filepath.Join(t.TempDir(), "no-such-dir", "out.flac")
	out, errb, code := runCLI(t, "set", file, "--set", "TITLE=X", "-o", missing)
	if code != 6 {
		t.Fatalf("missing -o parent: exit = %d, want 6 (not-found, consistent with other missing paths)", code)
	}
	if !strings.Contains(errb, "no such file or directory") {
		t.Errorf("stderr = %q, want the not-found message naming the missing directory", errb)
	}
	if strings.Contains(out, "plan") {
		t.Errorf("the not-found error should fire before the plan prints; stdout:\n%s", out)
	}

	asFile := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(asFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code = runCLI(t, "set", file, "--set", "TITLE=X", "-o", filepath.Join(asFile, "out.flac"))
	if code != 2 || !strings.Contains(errb, "not a directory") {
		t.Errorf("file-as-parent: code %d, stderr %q; want exit 2 'not a directory'", code, errb)
	}
}

// Missing cover: "cover image: <path>: <reason>", path once, no "open" verb.
func TestAddCoverMissingFileContext(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	missing := filepath.Join(t.TempDir(), "cover.png")
	_, errb, code := runCLI(t, "set", file, "--add-cover", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	want := "cover image: " + missing + ": no such file or directory"
	if !strings.Contains(errb, want) {
		t.Errorf("stderr should carry the clean cover message %q:\n%s", want, errb)
	}
	if strings.Count(errb, missing) != 1 {
		t.Errorf("cover path should appear once: %q", errb)
	}
	if strings.Contains(errb, "open "+missing) {
		t.Errorf("stderr leaked Go's \"open\" verb: %q", errb)
	}
}

// Non-image cover is usage error; --force embeds as octet-stream.
func TestAddCoverRejectsNonImage(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, notagsFLAC)
	notImage := filepath.Join(t.TempDir(), "cover.png")
	if err := os.WriteFile(notImage, []byte("this is plainly not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, errb, code := runCLI(t, "set", file, "--add-cover", notImage)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "not a recognized image") {
		t.Errorf("stderr should explain the rejection: %q", errb)
	}

	if _, _, code := runCLI(t, "set", file, "--add-cover", notImage, "--force"); code != 0 {
		t.Fatalf("--force exit = %d, want 0", code)
	}
	out, _, _ := runCLI(t, "--json", "dump", file)
	jd := decodeJSONOne[jsonDocument](t, out)
	if len(jd.Pictures) != 1 || jd.Pictures[0].MIME != "application/octet-stream" {
		t.Fatalf("pictures = %+v, want one forced octet-stream cover", jd.Pictures)
	}
}

func TestAddCoverAcceptsRecognizedImage(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, notagsFLAC)
	bmp := []byte{
		'B', 'M', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		40, 0, 0, 0, 3, 0, 0, 0, 5, 0, 0, 0, 1, 0, 24, 0,
	}
	cover := filepath.Join(t.TempDir(), "cover.bmp")
	if err := os.WriteFile(cover, bmp, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCLI(t, "set", file, "--add-cover", cover); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	out, _, _ := runCLI(t, "--json", "dump", file)
	jd := decodeJSONOne[jsonDocument](t, out)
	if len(jd.Pictures) != 1 || jd.Pictures[0].MIME != "image/bmp" {
		t.Fatalf("pictures = %+v, want one image/bmp cover", jd.Pictures)
	}
}

// Extension mismatch warns (no transcode) but still writes.
func TestSetExtensionMismatchWarns(t *testing.T) {
	t.Parallel()
	src := copyFixture(t, sampleFLAC)
	dst := filepath.Join(t.TempDir(), "out.mp3")
	_, errb, code := runCLI(t, "set", src, "--set", "TITLE=X", "-o", dst)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (the warning is non-fatal)", code)
	}
	if !strings.Contains(errb, "does not transcode") {
		t.Errorf("stderr should warn about the extension mismatch: %q", errb)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("output should still be written: %v", err)
	}
}

func TestSetExtensionMatchNoWarn(t *testing.T) {
	t.Parallel()
	src := copyFixture(t, sampleFLAC)
	dst := filepath.Join(t.TempDir(), "out.flac")
	_, errb, code := runCLI(t, "set", src, "--set", "TITLE=X", "-o", dst)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(errb, "transcode") {
		t.Errorf("a matching extension should not warn: %q", errb)
	}
}

func TestSetBulkInPlace(t *testing.T) {
	t.Parallel()
	a := copyFixture(t, sampleFLAC)
	b := copyFixture(t, sampleFLAC)
	out, _, code := runCLI(t, "set", a, b, "--set", "TITLE=Bulk")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "2 changed, 0 unchanged, 0 failed") {
		t.Errorf("missing/incorrect summary:\n%s", out)
	}
	for _, f := range []string{a, b} {
		j, _, _ := runCLI(t, "--json", "dump", f)
		jd := decodeJSONOne[jsonDocument](t, j)
		if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Bulk" {
			t.Errorf("%s TITLE = %v, want [Bulk]", f, got)
		}
	}
}

func TestSetRecursive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.flac", "sub/two.flac"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, code := runCLI(t, "set", "--recursive", dir, "--set", "ALBUM=Rec")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "2 changed, 0 unchanged, 0 failed") {
		t.Errorf("expected two audio files edited:\n%s", out)
	}
}

// Bulk set continues past failure; summary reflects partial success.
func TestSetBulkContinuesPastFailure(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	good := copyFixture(t, sampleFLAC)
	out, _, code := runCLI(t, "set", missing, good, "--set", "TITLE=Bulk")
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	if !strings.Contains(out, "1 changed, 0 unchanged, 1 failed") {
		t.Errorf("summary should report one success and one failure:\n%s", out)
	}
	j, _, _ := runCLI(t, "--json", "dump", good)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Bulk" {
		t.Errorf("the good file should still be edited: TITLE = %v", got)
	}
}

func TestSetOutputRejectsMultipleInputs(t *testing.T) {
	t.Parallel()
	a := copyFixture(t, sampleFLAC)
	b := copyFixture(t, sampleFLAC)
	dst := filepath.Join(t.TempDir(), "out.flac")
	_, errb, code := runCLI(t, "set", a, b, "-o", dst, "--set", "TITLE=X")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "single file") {
		t.Errorf("want a clear -o-with-many-inputs rejection: %q", errb)
	}
}

func TestPlanBulkJSONArray(t *testing.T) {
	t.Parallel()
	a := copyFixture(t, sampleFLAC)
	b := copyFixture(t, sampleFLAC)
	out, _, code := runCLI(t, "--json", "plan", a, b, "--set", "TITLE=X")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	arr := decodeJSONList[jsonReport](t, out)
	if len(arr) != 2 {
		t.Fatalf("got %d plan reports, want 2", len(arr))
	}
}

func TestDumpStdin(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLIStdin(t, string(data), "dump", "-")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "format:  FLAC") {
		t.Errorf("dump - missing format:\n%s", out)
	}
	if strings.Contains(out, "waxlabel-stdin") {
		t.Errorf("the buffered-stdin temp path leaked into output:\n%s", out)
	}
	if !strings.Contains(out, "<stdin>") {
		t.Errorf("dump - header should read <stdin>:\n%s", out)
	}
}

func TestLintStdin(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(emptyMP3)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLIStdin(t, string(data), "lint", "-")
	if code != 4 { // no-audio -> invalid-data
		t.Fatalf("exit = %d, want 4\n%s", code, out)
	}
	if !strings.Contains(out, "no-audio") {
		t.Errorf("lint - missing no-audio finding:\n%s", out)
	}
}

func TestVerifyStdinKeepsDisplayName(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLIStdin(t, string(data), "verify", "-")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "essence:") {
		t.Errorf("verify - missing essence:\n%s", out)
	}
	if strings.Contains(out, "waxlabel-stdin") {
		t.Errorf("the buffered-stdin temp path leaked into output:\n%s", out)
	}
}

func TestDiffStdinAgainstFile(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	_, _, code := runCLIStdin(t, string(data), "diff", "-", sampleFLAC)
	if code != 0 {
		t.Errorf("exit = %d, want 0 (identical metadata)", code)
	}
}

func TestDiffRejectsTwoStdin(t *testing.T) {
	t.Parallel()
	_, errb, code := runCLIStdin(t, "x", "diff", "-", "-")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "one operand") {
		t.Errorf("want a clear two-stdin rejection: %q", errb)
	}
}

func TestSetStdinRequiresOutput(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLIStdin(t, string(data), "set", "-", "--set", "TITLE=X")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "standard input") {
		t.Errorf("want a clear in-place-stdin rejection: %q", errb)
	}
}

// set --json failure: one-element array with file+error, not bare envelope.
func TestSetJSONErrorIsPerFileObject(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, _, code := runCLI(t, "--json", "set", missing, "--set", "TITLE=X")
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	res := decodeJSONOne[jsonSetResult](t, out)
	if res.File != missing {
		t.Errorf("file = %q, want %q", res.File, missing)
	}
	if res.Error == nil || res.Error.Code != "not-found" {
		t.Errorf("error = %+v, want code not-found", res.Error)
	}
}

// Empty recursive set/plan: note on stderr, exit 0, JSON [] not null.
func TestSetRecursiveNoFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no audio here"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLI(t, "set", "--recursive", dir, "--set", "TITLE=X")
	if code != 0 {
		t.Fatalf("set empty-walk exit = %d, want 0 (aligned with plan)", code)
	}
	if n := strings.Count(errb, "no audio files found"); n != 1 {
		t.Errorf("expected the no-files note exactly once, got %d: %q", n, errb)
	}
	for _, sub := range []string{"set", "plan"} {
		out, _, c := runCLI(t, "--json", sub, "--recursive", dir, "--set", "TITLE=X")
		if c != 0 {
			t.Fatalf("%s --json empty-walk exit = %d, want 0", sub, c)
		}
		if strings.TrimSpace(out) != "[]" {
			t.Errorf("%s --json output = %q, want [] (not null)", sub, strings.TrimSpace(out))
		}
	}
}

// lint --fix empty walk: exit 2; read-only lint exit 0 (set/plan align at 0).
func TestLintFixRecursiveNoFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no audio here"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLI(t, "lint", "--fix", "--recursive", dir)
	if code != 2 {
		t.Fatalf("lint --fix exit = %d, want 2 (usage)", code)
	}
	if n := strings.Count(errb, "no audio files found"); n != 1 {
		t.Errorf("expected the no-files message exactly once, got %d: %q", n, errb)
	}
	if _, _, readCode := runCLI(t, "lint", "--recursive", dir); readCode != 0 {
		t.Errorf("read-only lint exit = %d, want 0", readCode)
	}
}

func TestResolvePaddingFlag(t *testing.T) {
	t.Parallel()
	if opt, given, err := resolvePaddingFlag("", false); opt != nil || given || err != nil {
		t.Errorf("no flags: opt=%v given=%v err=%v, want nil,false,nil", opt, given, err)
	}
	for _, c := range []struct {
		padding   string
		noPadding bool
		desc      string
	}{
		{"16384", false, "--padding 16384"},
		{"", true, "--no-padding"},
		{"0", false, "--padding 0"}, // synonym for no-padding; behavior in TestPaddingZeroShrinksLikeNoPadding
		{"200000", false, "--padding 200000 (floor sets Min=Target)"},
		{"0", true, "--padding 0 --no-padding"},
		{"00", true, "--padding 00 --no-padding"},
		{" 0 ", true, "--padding ' 0 ' --no-padding"},
	} {
		if opt, given, err := resolvePaddingFlag(c.padding, c.noPadding); opt == nil || !given || err != nil {
			t.Errorf("%s: opt=%v given=%v err=%v, want option,true,nil", c.desc, opt, given, err)
		}
	}
	for _, c := range []struct {
		padding   string
		noPadding bool
	}{{"16384", true}, {"-1", false}, {"abc", false}, {"99999999999", false}, {"   ", false}} {
		if _, _, err := resolvePaddingFlag(c.padding, c.noPadding); err == nil || !isUsageError(err) {
			t.Errorf("resolvePaddingFlag(%q, %v) err = %v, want usage error", c.padding, c.noPadding, err)
		}
	}
}

func TestPaddingNoPadding(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	def, _, code := runCLI(t, "plan", file, "--set", "TITLE=Pad")
	if code != 0 {
		t.Fatalf("plan exit = %d", code)
	}
	if !strings.Contains(def, "padding:") {
		t.Fatalf("default plan should show a padding line; got:\n%s", def)
	}
	if !strings.Contains(def, "--padding") || !strings.Contains(def, "--no-padding") {
		t.Errorf("padding line should advertise its controls; got:\n%s", def)
	}
	out, _, code := runCLI(t, "plan", file, "--set", "TITLE=Pad", "--no-padding")
	if code != 0 {
		t.Fatalf("plan --no-padding exit = %d", code)
	}
	if !strings.Contains(out, "padding: none") {
		t.Errorf("--no-padding should confirm 'padding: none'; got:\n%s", out)
	}
}

// --padding 0 shrinks like --no-padding, not like a positive floor.
func TestPaddingZeroShrinksLikeNoPadding(t *testing.T) {
	t.Parallel()
	sizeAfter := func(extra ...string) int64 {
		file := copyFixture(t, sampleFLAC)
		args := append([]string{"set", file, "--set", "TITLE=Zero"}, extra...)
		if _, errb, code := runCLI(t, args...); code != 0 {
			t.Fatalf("set %v exit = %d: %s", extra, code, errb)
		}
		fi, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	def := sizeAfter()
	none := sizeAfter("--no-padding")
	zero := sizeAfter("--padding", "0")
	if zero >= def {
		t.Errorf("--padding 0 size %d should be smaller than the default-padded %d", zero, def)
	}
	if zero != none {
		t.Errorf("--padding 0 size %d should equal the --no-padding size %d", zero, none)
	}
}

// Explicit --padding overrides preset minimal (which alone writes none).
func TestPaddingPresetPrecedence(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	bare, _, _ := runCLI(t, "plan", file, "--set", "TITLE=Pad", "--preset", "minimal")
	if !strings.Contains(bare, "padding: none") {
		t.Errorf("--preset minimal alone should confirm 'padding: none'; got:\n%s", bare)
	}
	over, _, code := runCLI(t, "plan", file, "--set", "TITLE=Pad", "--preset", "minimal", "--padding", "16384")
	if code != 0 {
		t.Fatalf("plan exit = %d", code)
	}
	if !strings.Contains(over, "padding:") || strings.Contains(over, "padding: none") {
		t.Errorf("--padding should override the preset's zero padding; got:\n%s", over)
	}
}

func TestPaddingFlagValidation(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	for _, args := range [][]string{
		{"plan", file, "--padding", "1024", "--no-padding"},
		{"plan", file, "--padding", "-1"},
		{"plan", file, "--padding", "abc"},
		{"plan", file, "--padding", "99999999999"}, // above 64 MiB cap
		{"plan", file, "--padding", "1QiB"},
		{"plan", file, "--padding", "0.4"},    // fraction truncates to 0
		{"plan", file, "--padding", "1.9KiB"}, // truncates to 1945
		{"plan", file, "--padding", "1GiB"},   // valid suffix, above cap
	} {
		if _, _, code := runCLI(t, args...); code != 2 {
			t.Errorf("args %v exit = %d, want 2 (usage)", args, code)
		}
	}
	if _, errb, _ := runCLI(t, "plan", file, "--padding", "1QiB"); !strings.Contains(errb, "unknown unit") {
		t.Errorf("--padding 1QiB error lost the unit detail: %s", errb)
	}
}

// --padding accepts same size suffixes as --max-size.
func TestPaddingAcceptsSizeSuffixes(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	planPadding := func(args ...string) int64 {
		out, errb, code := runCLI(t, append([]string{"--json", "plan", file}, args...)...)
		if code != 0 {
			t.Fatalf("plan exit = %d for %v: %s", code, args, errb)
		}
		return decodeJSONList[jsonReport](t, out)[0].PaddingAfter
	}
	cases := map[string]int64{
		"32768":   32768,
		"32KiB":   32768,
		"32 KiB":  32768,
		"32k":     32768, // bare k is binary
		"33KB":    33000,
		"32.0KiB": 32768,
		"31.5KiB": 32256,
	}
	for spelling, want := range cases {
		if got := planPadding("--set", "TITLE=X", "--padding", spelling); got < want {
			t.Errorf("--padding %q PaddingAfter = %d, want >= %d", spelling, got, want)
		}
	}
}

// --padding N is a floor; must grow region even when edit fits existing padding.
func TestPaddingFloorGrowsRegion(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	planPadding := func(args ...string) int64 {
		out, _, code := runCLI(t, append([]string{"--json", "plan", file}, args...)...)
		if code != 0 {
			t.Fatalf("plan exit = %d for %v", code, args)
		}
		return decodeJSONList[jsonReport](t, out)[0].PaddingAfter
	}
	if def := planPadding("--set", "TITLE=X"); def > 100000 {
		t.Fatalf("fixture default padding = %d, expected the small reused region", def)
	}
	if floor := planPadding("--set", "TITLE=X", "--padding", "200000"); floor < 200000 {
		t.Errorf("--padding 200000 PaddingAfter = %d, want >= 200000 (floor)", floor)
	}
}

// Malformed numeric/date noted on stderr; write succeeds; --json suppresses note.
func TestMalformedValueNotes(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", file, "--set", "TRACKNUMBER=abc", "--set", "RECORDINGDATE=banana")
	if code != 0 {
		t.Fatalf("set exit = %d (a note must not fail the write); stderr:\n%s", code, errb)
	}
	if !strings.Contains(errb, "TRACKNUMBER=abc does not look like a number") {
		t.Errorf("expected a numeric note; stderr:\n%s", errb)
	}
	if !strings.Contains(errb, "RECORDINGDATE=banana is not YYYY") {
		t.Errorf("expected a date note; stderr:\n%s", errb)
	}
	out, jerr, _ := runCLI(t, "--json", "plan", file, "--set", "TRACKNUMBER=abc")
	if strings.Contains(jerr, "does not look like a number") {
		t.Errorf("note should be suppressed under --json; stderr:\n%s", jerr)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("--json output should be a JSON array; got:\n%s", out)
	}
}

func TestMalformedValueNotesTolerant(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", file,
		"--set", "TRACKNUMBER= 3 ",
		"--set", "DISCNUMBER=1/2",
		"--set", "PLAYCOUNT=-1",
		"--set", "RECORDINGDATE=2021-06")
	if code != 0 {
		t.Fatalf("set exit = %d; stderr:\n%s", code, errb)
	}
	if strings.Contains(errb, "does not look like a number") || strings.Contains(errb, "is not YYYY") {
		t.Errorf("ParseNumPair-tolerant values should not be flagged; stderr:\n%s", errb)
	}
}

// Value notes deferred until a real file is acted on.
func TestValueNotesDeferredUntilFiles(t *testing.T) {
	t.Parallel()
	_, errb, code := runCLI(t, "set", t.TempDir(), "--set", "TRACKNUMBER=abc")
	if code != 2 {
		t.Fatalf("directory exit = %d, want 2", code)
	}
	if strings.Contains(errb, "does not look like a number") {
		t.Errorf("value note must not print on a directory-only run:\n%s", errb)
	}
	_, errb, code = runCLI(t, "set", t.TempDir(), "--recursive", "--set", "TRACKNUMBER=abc")
	if code != 0 {
		t.Fatalf("empty-walk exit = %d, want 0", code)
	}
	if strings.Contains(errb, "does not look like a number") {
		t.Errorf("value note must not print on an empty walk:\n%s", errb)
	}
}

// --set KEY=: empty-value note, not malformed-value warning.
func TestEmptyValueNote(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", file, "--set", "TITLE=")
	if code != 0 {
		t.Fatalf("set exit = %d; stderr:\n%s", code, errb)
	}
	if !strings.Contains(errb, "TITLE= writes an empty value") || !strings.Contains(errb, "--clear TITLE") {
		t.Errorf("expected an empty-value note suggesting --clear; stderr:\n%s", errb)
	}
	if !strings.Contains(errb, "drop") {
		t.Errorf("empty-value note should state the drop possibility, not assert retention; stderr:\n%s", errb)
	}
}

// Whitespace-only numeric trims to empty-value note, not malformed-value.
func TestWhitespaceNumericNote(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", file, "--set", "TRACKNUMBER=   ")
	if code != 0 {
		t.Fatalf("set exit = %d; stderr:\n%s", code, errb)
	}
	if strings.Contains(errb, "kept as text") {
		t.Errorf("whitespace-only numeric must not take the malformed-value note (it is trimmed to empty); stderr:\n%s", errb)
	}
	if !strings.Contains(errb, "TRACKNUMBER= writes an empty value") {
		t.Errorf("whitespace-only numeric should take the empty-value note; stderr:\n%s", errb)
	}
}

func TestDumpSanitizesEndToEnd(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", file, "--set", "TITLE=x\x1b[31my\rz"); code != 0 {
		t.Fatalf("set exit = %d", code)
	}
	out, _, code := runCLI(t, "dump", file)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if strings.ContainsAny(out, "\x1b\r") {
		t.Errorf("dump leaked a raw control byte:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf("dump should show the escaped form:\n%s", out)
	}
}

func TestDiffSanitized(t *testing.T) {
	t.Parallel()
	a := copyFixture(t, sampleFLAC)
	b := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", b, "--set", "TITLE=clean\x1bX"); code != 0 {
		t.Fatalf("set exit = %d", code)
	}
	out, _, _ := runCLI(t, "diff", a, b)
	if strings.Contains(out, "\x1b") {
		t.Errorf("diff leaked a raw ESC:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf("diff should show the escaped title change:\n%s", out)
	}
}

func makeAudioTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.flac", "sub/two.flac"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDumpRecursive(t *testing.T) {
	t.Parallel()
	dir := makeAudioTree(t)
	out, _, code := runCLI(t, "--json", "dump", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	docs := decodeJSONList[jsonDocument](t, out)
	if len(docs) != 2 {
		t.Fatalf("dumped %d files, want 2 (two FLACs; notes.txt skipped)", len(docs))
	}
}

func TestVerifyRecursive(t *testing.T) {
	t.Parallel()
	dir := makeAudioTree(t)
	out, _, code := runCLI(t, "--json", "verify", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	vs := decodeJSONList[jsonVerify](t, out)
	if len(vs) != 2 {
		t.Fatalf("verified %d files, want 2", len(vs))
	}
	for _, v := range vs {
		if v.Essence == "" {
			t.Errorf("missing essence digest for %s", v.File)
		}
	}
}

// Exit code unchecked; fixtures may have warnings unrelated to recursion.
func TestLintRecursive(t *testing.T) {
	t.Parallel()
	dir := makeAudioTree(t)
	out, _, _ := runCLI(t, "--json", "lint", "--recursive", dir)
	ls := decodeJSONList[jsonLint](t, out)
	if len(ls) != 2 {
		t.Fatalf("linted %d files, want 2", len(ls))
	}
}

// Empty recursive dump --json: [], stderr clean (text note in TestNoFilesNoteSuppressedUnderJSON).
func TestDumpRecursiveNoFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runCLI(t, "--json", "dump", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if errb != "" {
		t.Errorf("expected no advisory on stderr under --json, got: %q", errb)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("JSON output = %q, want [] (not null)", strings.TrimSpace(out))
	}
}

// Unknown keys: per-key note, exit 0, one keys hint even for multiple unknowns.
func TestSetUnknownKeyNote(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", f, "--set", "TITEL=typo", "--set", "ARTST=who")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errb)
	}
	if !strings.Contains(errb, "TITEL is not a known key") || !strings.Contains(errb, "ARTST is not a known key") {
		t.Errorf("expected per-key unknown-key notes on stderr, got: %q", errb)
	}
	if n := strings.Count(errb, "waxlabel keys"); n != 1 {
		t.Errorf("keys hint should appear exactly once for multiple unknown keys, got %d:\n%s", n, errb)
	}
	j, _, _ := runCLI(t, "--json", "dump", f)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "TITEL"); len(got) != 1 || got[0] != "typo" {
		t.Errorf("the custom field should still be written: TITEL = %v", got)
	}
}

func TestSetStrictUnknownKeyFails(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	_, _, code := runCLI(t, "set", f, "--set", "TITEL=typo", "--strict")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	j, _, _ := runCLI(t, "--json", "dump", f)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "TITEL"); got != nil {
		t.Errorf("strict run touched the file: TITEL = %v, want nothing", got)
	}
}

// Unknown-key notes never pollute --json stdout or stderr.
func TestSetUnknownKeyJSONClean(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	out, errb, code := runCLI(t, "--json", "set", f, "--set", "TITEL=x")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(errb, "note:") {
		t.Errorf("a note leaked to stderr under --json: %q", errb)
	}
	if n := len(decodeJSONList[jsonSetResult](t, out)); n != 1 {
		t.Errorf("JSON array len = %d, want 1", n)
	}
}

// single-valued-multi on stdout report, not stderr note.
func TestPlanSingleValuedMultiNote(t *testing.T) {
	t.Parallel()
	out, errb, code := runCLI(t, "plan", sampleFLAC, "--add", "ENCODER=a", "--add", "ENCODER=b")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errb)
	}
	if !strings.Contains(out, "single-valued-multi") || !strings.Contains(out, "ENCODER is single-valued") {
		t.Errorf("expected single-valued-multi warning in the report, got stdout: %q", out)
	}
	if strings.Contains(errb, "note: ENCODER is single-valued") {
		t.Errorf("single-valued signal should not also be a stderr note: %q", errb)
	}
}

// Custom multi-value: unknown-key note only, not single-valued-multi.
func TestSetCustomMultiValueNoSingleValuedNote(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, notagsFLAC)
	_, errb, code := runCLI(t, "set", f, "--add", "MY_CUSTOM=a", "--add", "MY_CUSTOM=b")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errb)
	}
	if !strings.Contains(errb, "MY_CUSTOM is not a known key") {
		t.Errorf("expected the unknown-key note, got: %q", errb)
	}
	if strings.Contains(errb, "single-valued") {
		t.Errorf("a custom key should not trigger the single-valued note: %q", errb)
	}
	j, _, _ := runCLI(t, "--json", "dump", f)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "MY_CUSTOM"); len(got) != 2 {
		t.Errorf("custom field should hold both values: MY_CUSTOM = %v", got)
	}
}

func TestSetStrictSingleValuedMultiFails(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	_, _, code := runCLI(t, "set", f, "--add", "ENCODER=a", "--add", "ENCODER=b", "--strict")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
}

// Recursive walk: single-valued-multi per file on stdout, not stderr.
func TestSetSingleValuedMultiPerFileWarning(t *testing.T) {
	t.Parallel()
	dir := makeAudioTree(t)
	out, errb, _ := runCLI(t, "set", "--recursive", dir, "--add", "ENCODER=a", "--add", "ENCODER=b")
	if n := strings.Count(out, "single-valued-multi"); n != 2 {
		t.Errorf("single-valued-multi report warning appeared %d times, want 1 per file (2)", n)
	}
	if strings.Contains(errb, "note: ENCODER is single-valued") {
		t.Errorf("single-valued signal should not be a stderr note: %q", errb)
	}
}

// stdin-in-place usage error before --add-cover read (exit 2 beats exit 6).
func TestSetStdinUsageBeatsCoverRead(t *testing.T) {
	t.Parallel()
	missingCover := filepath.Join(t.TempDir(), "cover.jpg")
	_, errb, code := runCLIStdin(t, "ignored", "set", "-", "--add-cover", missingCover, "--set", "TITLE=X")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage), stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "standard input") {
		t.Errorf("want the stdin usage error, got: %q", errb)
	}
	if strings.Contains(errb, "cover image") {
		t.Errorf("the cover file should not have been read: %q", errb)
	}
}

func TestSetStdinToOutput(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out.flac")
	_, _, code := runCLIStdin(t, string(data), "set", "-", "-o", dst, "--set", "TITLE=FromStdin")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	j, _, _ := runCLI(t, "--json", "dump", dst)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "FromStdin" {
		t.Errorf("TITLE = %v, want [FromStdin]", got)
	}
}

// Ogg chapter copy is lossy (CHAPTERxxx comments drop language/flags).
func TestCopyChaptersIntoOggIsLossy(t *testing.T) {
	t.Parallel()
	src := filepath.Join("..", "..", "testdata", "chapters.mka")
	dst := copyFixture(t, filepath.Join("..", "..", "testdata", "sample.ogg"))
	out, errb, code := runCLI(t, "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy exit = %d: %s", code, errb)
	}
	if !strings.Contains(out, "lossy") || !strings.Contains(out, "chapters") {
		t.Errorf("ogg chapter copy should report a lossy chapter carry:\n%s", out)
	}
	if strings.Contains(out, "unsupported") {
		t.Errorf("unsupported marker leaked into the report:\n%s", out)
	}
	dump, _, _ := runCLI(t, "dump", dst)
	if !strings.Contains(dump, "chapters (") {
		t.Errorf("chapters did not transfer into the Ogg destination:\n%s", dump)
	}
}

// Split chapter set: both carried and lossy lines; carried line has no trailing colon.
func TestCopySplitChapterSetShowsCarriedSibling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Ch1 carries; ch2 ends early -> lossy. Ends below duration (run-to-EOF ends fold away).
	src := copyFixture(t, notagsMP3)
	doc, err := wl.ParseFile(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := doc.Edit().SetChapters(
		wl.Chapter{Start: 0, End: 400 * time.Millisecond, Title: "One"},
		wl.Chapter{Start: 400 * time.Millisecond, End: 700 * time.Millisecond, Title: "Two"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	re, err := wl.ParseFile(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if chs := re.Chapters(); len(chs) != 2 || chs[1].End == 0 {
		t.Fatalf("setup: chapters did not persist with an explicit trailing end: %+v", chs)
	}

	dst := copyFixture(t, notagsFLAC)
	stdout, stderr, code := runCLI(t, "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy failed: %s", stderr)
	}
	if !strings.Contains(stdout, "carried chapters (1)") {
		t.Errorf("missing carried sibling line:\n%s", stdout)
	}
	if !regexp.MustCompile(`(?m)^  lossy\s+chapters \(1\)`).MatchString(stdout) {
		t.Errorf("missing lossy chapters detail line:\n%s", stdout)
	}
	if regexp.MustCompile(`carried chapters \(1\):`).MatchString(stdout) {
		t.Errorf("carried line must not end in a colon:\n%s", stdout)
	}
}

func TestNotagsFixtureHasNoTags(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", notagsFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "tags:    (none)") {
		t.Errorf("expected no tags, got:\n%s", out)
	}
}

func TestExitCodes(t *testing.T) {
	t.Parallel()
	junk := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(junk, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "nope.flac")

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"missing-file", []string{"dump", missing}, 6},
		{"unsupported-format", []string{"dump", junk}, 3},
		{"bad-key", []string{"plan", sampleFLAC, "--clear", "A=B"}, 2},
		{"missing-assign", []string{"plan", sampleFLAC, "--set", "TITLE"}, 2},
		{"unknown-preset", []string{"plan", sampleFLAC, "--preset", "bogus"}, 2},
		{"unknown-command", []string{"frobnicate", "x"}, 2},
		{"unknown-flag", []string{"dump", "--nope", sampleFLAC}, 2},
		{"missing-arg", []string{"plan"}, 2},
		{"success", []string{"dump", sampleFLAC}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, code := runCLI(t, tc.args...)
			if code != tc.want {
				t.Errorf("exit = %d, want %d", code, tc.want)
			}
		})
	}
}

// Empty file: exit 3 unsupported-format regardless of extension (.flac must not steer parser).
func TestEmptyFileExitClass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"zero.flac", "zero.bin"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		out, _, code := runCLI(t, "--json", "dump", path)
		if code != 3 {
			t.Errorf("%s exit = %d, want 3 (unsupported-format)", name, code)
		}
		jr := decodeJSONOne[jsonDocument](t, out)
		if jr.Error == nil || jr.Error.Code != "unsupported-format" {
			t.Errorf("%s error = %+v, want code unsupported-format", name, jr.Error)
		}
		if jr.Error != nil && !strings.Contains(jr.Error.Message, "empty file") {
			t.Errorf("%s message = %q, want it to mention the empty file", name, jr.Error.Message)
		}
	}
}

// Junk exit 3 regardless of extension; recognized container + corrupt contents exit 4.
func TestContentFaithfulDetection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	junk := []byte("this is plainly not audio, just text\n")
	for _, ext := range []string{".txt", ".flac", ".wav"} {
		path := filepath.Join(dir, "x"+ext)
		if err := os.WriteFile(path, junk, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, code := runCLI(t, "dump", path); code != 3 {
			t.Errorf("byte-identical junk%s exit = %d, want 3 (unsupported regardless of extension)", ext, code)
		}
	}
	corrupt := append([]byte("fLaC"), bytes.Repeat([]byte{0xFF}, 64)...)
	path := filepath.Join(dir, "corrupt.flac")
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCLI(t, "dump", path); code != 4 {
		t.Errorf("recognized-but-corrupt FLAC exit = %d, want 4 (invalid-data)", code)
	}
}

// plan --json failure: one-element array with per-file error, not bare envelope.
func TestPlanJSONErrorIsPerFileObject(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, _, code := runCLI(t, "--json", "plan", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	jr := decodeJSONOne[jsonReport](t, out)
	if jr.File != missing {
		t.Errorf("file = %q, want %q", jr.File, missing)
	}
	if jr.Error == nil || jr.Error.Code != "not-found" {
		t.Errorf("error = %+v, want code not-found", jr.Error)
	}
}

func TestDumpJSONPerFileError(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, _, code := runCLI(t, "--json", "dump", sampleFLAC, missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6 (a file failed)", code)
	}
	docs := decodeJSONList[jsonDocument](t, out)
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
	if docs[0].Error != nil {
		t.Errorf("first doc should have parsed: %+v", docs[0].Error)
	}
	if docs[1].Error == nil || docs[1].Error.Code != "not-found" {
		t.Errorf("second doc should carry a not-found error: %+v", docs[1].Error)
	}
}

// Early cobra abort: error on stdout; list commands get array, unknown command gets object.
func TestJSONErrorRoutingOnEarlyAbort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args []string
		list bool // a list command wraps its pre-flight error in a one-element array
	}{
		{[]string{"--json", "frobnicate", "x"}, false},
		{[]string{"dump", "--nope", "--json", sampleFLAC}, true},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			out, errb, code := runCLI(t, tc.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			var body jsonErrBody
			if tc.list {
				body = decodeJSONOne[jsonError](t, out).Error
			} else {
				var je jsonError
				if err := json.Unmarshal([]byte(out), &je); err != nil {
					t.Fatalf("stdout is not a JSON object envelope: %v\nstdout=%q stderr=%q", err, out, errb)
				}
				body = je.Error
			}
			if body.Code != "usage" {
				t.Errorf("error code = %q, want usage", body.Code)
			}
		})
	}
}

// Plan prints before failed write. RO dir: file readable, temp create fails.
func TestSetShowsPlanBeforeFailedWrite(t *testing.T) {
	t.Parallel()
	requireUnwritableDir(t)
	roDir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(roDir, "in.flac")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) })
	out, _, code := runCLI(t, "set", file, "--set", "TITLE=X")
	if code != 6 {
		t.Fatalf("exit = %d, want 6 (io)", code)
	}
	if !strings.Contains(out, "plan") {
		t.Errorf("plan should be printed before the failed write:\n%s", out)
	}
}

func TestHumanDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00"},
		{500 * time.Millisecond, "0.50s"},
		{time.Second, "1.00s"},
		{59500 * time.Millisecond, "59.50s"},
		{59999 * time.Millisecond, "1:00"}, // not "60.00s"
		{60 * time.Second, "1:00"},
		{90 * time.Second, "1:30"},
		{3661 * time.Second, "1:01:01"},
	}
	for _, tc := range cases {
		if got := humanDuration(tc.d); got != tc.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestWriteWrapped(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	writeWrapped(&b, 4, "a\nb\n")
	if got, want := b.String(), "a\n    b\n"; got != want {
		t.Errorf("trailing newline: got %q, want %q", got, want)
	}
	b.Reset()
	writeWrapped(&b, 2, "x\n\ny")
	if got, want := b.String(), "x\n  \n  y\n"; got != want {
		t.Errorf("internal blank: got %q, want %q", got, want)
	}
}

// Every classifyError code has errClassRank entry; bidirectional with samples.
func TestErrClassRankCoversEveryErrorClass(t *testing.T) {
	t.Parallel()
	samples := []error{
		&usageError{msg: "bad usage"},
		waxerr.ErrInvalidKey,
		waxerr.ErrNeedsFile,
		waxerr.ErrUnsupportedFormat,
		waxerr.ErrUnsupportedTag,
		waxerr.ErrChainedStream,
		waxerr.ErrUnalignedStream,
		waxerr.ErrFragmented,
		waxerr.ErrPictureTooLarge,
		waxerr.ErrSourceChanged,
		waxerr.ErrInvalidData,
		waxerr.ErrInputTooLarge,
		&fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist},
		&fs.PathError{Op: "open", Path: "x", Err: errors.New("disk failure")},
		context.Canceled,
		context.DeadlineExceeded,
		errBrokenPipe,
		errors.New("some unclassified failure"),
	}
	seen := map[string]bool{}
	for _, err := range samples {
		code := classifyError(err).code
		seen[code] = true
		if _, ranked := errClassRank[code]; !ranked {
			t.Errorf("classifyError(%v) code %q has no errClassRank entry; worseError would sink it to 0", err, code)
		}
	}
	for code := range errClassRank {
		if !seen[code] {
			t.Errorf("errClassRank has %q, which no sampled error produces; add a sample or remove the rank", code)
		}
	}
	if !(errClassRank["invalid-data"] > errClassRank["not-found"] && errClassRank["not-found"] > errClassRank["usage"]) {
		t.Errorf("precedence broken: want invalid-data(%d) > not-found(%d) > usage(%d)",
			errClassRank["invalid-data"], errClassRank["not-found"], errClassRank["usage"])
	}
}

// input-too-large aggregate rank: below corrupt, above unsupported. Tested via worseError (stdin aborts early).
func TestInputTooLargeAggregateRank(t *testing.T) {
	t.Parallel()
	inputTooLarge := fmt.Errorf("w: %w", waxerr.ErrInputTooLarge)
	corrupt := fmt.Errorf("w: %w", waxerr.ErrInvalidData)
	unsupported := fmt.Errorf("w: %w", waxerr.ErrUnsupportedFormat)

	fold := func(errs ...error) int {
		var worst error
		for _, e := range errs {
			if worseError(worst, e) {
				worst = e
			}
		}
		return classifyError(worst).exitCode
	}
	for _, tc := range []struct {
		name string
		errs []error
		want int
	}{
		{"over-cap then corrupt", []error{inputTooLarge, corrupt}, 4},
		{"corrupt then over-cap", []error{corrupt, inputTooLarge}, 4},
		{"over-cap then unsupported", []error{inputTooLarge, unsupported}, 7},
		{"unsupported then over-cap", []error{unsupported, inputTooLarge}, 7},
	} {
		if got := fold(tc.errs...); got != tc.want {
			t.Errorf("%s: aggregate exit = %d, want %d", tc.name, got, tc.want)
		}
	}
}
