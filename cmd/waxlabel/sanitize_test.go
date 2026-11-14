package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// OSC set-title sequence plus bidi override and zero-width space.
const hostilePayload = "evil\x1b]0;pwned\x07end\u202e\u200b"

// No raw controls; hostile ESC visible as \x1b (proves render, not drop).
func assertSafe(t *testing.T, label, s string) {
	t.Helper()
	if !strings.Contains(s, `\x1b`) {
		t.Errorf("%s: want a visible \\x1b escape (hostile field rendered); got:\n%q", label, s)
	}
	assertNoRawControl(t, label, s)
}

// Independent restatement of tag.controlRune bar; tab/newline allowed.
func assertNoRawControl(t *testing.T, label, s string) {
	t.Helper()
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			t.Errorf("%s: raw invalid UTF-8 byte 0x%02x at offset %d in:\n%q", label, s[i], i, s)
			i++
			continue
		}
		if forbiddenRaw(r) {
			t.Errorf("%s: raw control byte 0x%02x at offset %d survived sanitizing in:\n%q", label, r, i, s)
		}
		i += size
	}
}

func forbiddenRaw(r rune) bool {
	if r == '\t' || r == '\n' {
		return false
	}
	if r == 0x202e || r == 0x200b {
		return true
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// Seed via public API so renderers see file-derived bytes, not injected past parser.
func seedValue(t *testing.T, src string, key tag.Key, val string) string {
	t.Helper()
	ctx := context.Background()
	doc, err := wl.ParseFile(ctx, src)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", src, err)
	}
	out := filepath.Join(t.TempDir(), "seeded"+filepath.Ext(src))
	plan, err := doc.Edit().Set(key, val).Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, _, err := plan.Execute(ctx, wl.SaveAsFile(out)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out
}

// Hostile bytes in filename (Linux allows any byte except / and NUL). Skips if FS refuses.
func hostileNamedCopy(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), hostilePayload+filepath.Ext(src))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Skipf("filesystem refused a control-byte filename: %v", err)
	}
	return dst
}

// Human commands: hostile tag content renders escaped, no raw controls on either stream.
func TestBoundarySanitizesHostileContent(t *testing.T) {
	t.Parallel()
	hostileFLAC := seedValue(t, sampleFLAC, tag.Title, hostilePayload)
	hostileMKA := seedValue(t, sampleMKA, tag.Title, hostilePayload)
	// Malformed date embeds hostile bytes in Finding.String; not auto-fixable, so lint --fix too.
	hostileDateFLAC := seedValue(t, sampleFLAC, tag.RecordingDate, "20\x1b]0;pwned\x0721")
	setTarget := copyFixture(t, sampleFLAC)

	cases := []struct {
		name string
		args []string
	}{
		{"dump", []string{"dump", hostileFLAC}},
		{"dump --native (matroska)", []string{"dump", "--native", hostileMKA}},
		{"plan", []string{"plan", "--set", "TITLE=" + hostilePayload, sampleFLAC}},
		{"set", []string{"set", "--set", "TITLE=" + hostilePayload, setTarget}},
		{"diff", []string{"diff", sampleFLAC, hostileFLAC}},
		{"lint", []string{"lint", hostileDateFLAC}},
		{"lint --fix", []string{"lint", "--fix", hostileDateFLAC}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, _ := runCLI(t, c.args...)
			assertSafe(t, c.name+" stdout", stdout)
			assertNoRawControl(t, c.name+" stderr", stderr)
		})
	}
}

// Hostile path bytes in headers and error lines, before or without parsing content.
func TestBoundarySanitizesHostileFilename(t *testing.T) {
	t.Parallel()
	named := hostileNamedCopy(t, sampleFLAC)
	// Missing path: name bytes still reach stderr via not-found.
	missing := filepath.Join(t.TempDir(), hostilePayload+".flac")

	t.Run("dump header", func(t *testing.T) {
		stdout, _, _ := runCLI(t, "dump", named)
		assertSafe(t, "dump header stdout", stdout)
	})
	t.Run("lint header", func(t *testing.T) {
		stdout, _, _ := runCLI(t, "lint", named)
		assertSafe(t, "lint header stdout", stdout)
	})
	t.Run("plan header", func(t *testing.T) {
		stdout, _, _ := runCLI(t, "plan", "--set", "TITLE=x", named)
		assertSafe(t, "plan header stdout", stdout)
	})
	t.Run("dump per-file error (stderr)", func(t *testing.T) {
		_, stderr, _ := runCLI(t, "dump", missing)
		assertSafe(t, "dump not-found stderr", stderr)
	})
	t.Run("diff terminal not-found (stderr)", func(t *testing.T) {
		_, stderr, code := runCLI(t, "diff", missing, sampleFLAC)
		if code != 6 {
			t.Errorf("diff missing-file exit = %d, want 6 (not-found)", code)
		}
		assertSafe(t, "diff not-found stderr", stderr)
	})
}

// Library %q escapes once; CLI must pass raw jsonFileName or tab double-escapes.
func TestUnidentifiedFilenameEscapedOnce(t *testing.T) {
	t.Parallel()
	// Windows cannot create control-byte filenames; value cases cover other platforms.
	if runtime.GOOS == "windows" {
		t.Skip("windows: a control byte is not a legal filename character")
	}
	path := filepath.Join(t.TempDir(), "na\tme.bin") // tab byte in name
	if err := os.WriteFile(path, []byte("not an audio file"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A double-escaped tab would show as \\x09 in the reason.
	_, stderr, code := runCLI(t, "dump", path)
	if code == 0 {
		t.Fatalf("dump of an unidentifiable file should fail")
	}
	if !strings.Contains(stderr, `na\tme.bin`) {
		t.Errorf("human error should escape the tab once as \\t in the reason:\n%s", stderr)
	}
	if strings.Contains(stderr, `na\\x09`) {
		t.Errorf("human error double-escaped the tab (\\\\x09 present):\n%s", stderr)
	}

	jout, _, _ := runCLI(t, "--json", "dump", path)
	docs := decodeJSONList[jsonDocument](t, jout)
	if len(docs) != 1 || docs[0].Error == nil {
		t.Fatalf("expected one document carrying an error:\n%s", jout)
	}
	if !strings.Contains(docs[0].Error.Message, `na\tme.bin`) {
		t.Errorf("JSON error.message should escape the tab once as \\t: %q", docs[0].Error.Message)
	}
	if strings.Contains(docs[0].Error.Message, "x09") {
		t.Errorf("JSON error.message double-escaped the tab: %q", docs[0].Error.Message)
	}
}

// --json is machine contract: raw DEL/C1 bytes survive (sanitizing would break JSON).
func TestBoundaryJSONStaysRaw(t *testing.T) {
	t.Parallel()
	src := seedValue(t, sampleFLAC, tag.Title, "raw\x7f\u009bbytes")
	stdout, _, code := runCLI(t, "dump", "--json", src)
	if code != 0 {
		t.Fatalf("dump --json exit = %d, want 0\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "\x7f") {
		t.Errorf("JSON output should carry the raw DEL byte; got:\n%q", stdout)
	}
	if !strings.Contains(stdout, "\u009b") {
		t.Errorf("JSON output should carry the raw C1 (U+009B) bytes; got:\n%q", stdout)
	}
	var v any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Errorf("dump --json output is not valid JSON: %v\n%q", err, stdout)
	}
}

func TestRenderPicturesSanitizesHostileMIME(t *testing.T) {
	var buf bytes.Buffer
	renderPictures(&buf, []wl.Picture{{
		Type: wl.PicFrontCover,
		MIME: "image/\x1b]0;pwned\x07png",
		Data: []byte("xx"),
	}})
	assertSafe(t, "renderPictures MIME", buf.String())
}

func TestRenderChaptersSanitizesHostileTitle(t *testing.T) {
	var buf bytes.Buffer
	renderChapters(&buf, []wl.Chapter{{Title: "chapter\x1b]0;pwned\x07one"}})
	assertSafe(t, "renderChapters title", buf.String())
}

// Library String() methods self-sanitize file-derived parts.
func TestWarningStringSanitizes(t *testing.T) {
	w := wl.Warning{Code: wl.WarnInheritedEncoder, Message: "Lavf\x1b]0;pwned\x07"}
	assertSafe(t, "Warning.String", w.String())
}

func TestFindingStringSanitizes(t *testing.T) {
	f := wl.Finding{Severity: wl.LintInfo, Code: "custom-key", Message: "custom field, not a known key", Key: tag.Key("BAD\x1b]0;pwned\x07KEY")}
	assertSafe(t, "Finding.String", f.String())
}

func TestWriteReportStringSanitizes(t *testing.T) {
	r := wl.WriteReport{
		Operations: []string{"rewrite metadata"},
		Warnings:   []wl.Warning{{Code: wl.WarnInheritedEncoder, Message: "stamp\x1b]0;pwned\x07"}},
	}
	assertSafe(t, "WriteReport.String", r.String())
}

// Single-line list items: newline in message must escape, not forge extra item.
func TestWarningAndFindingStringEscapeNewline(t *testing.T) {
	w := wl.Warning{Code: wl.WarnInheritedEncoder, Message: "a\nb"}
	if strings.Contains(w.String(), "\n") {
		t.Errorf("Warning.String should escape a newline (single-line item): %q", w.String())
	}
	f := wl.Finding{Severity: wl.LintWarning, Code: "inherited-encoder", Message: "a\nb"}
	if strings.Contains(f.String(), "\n") {
		t.Errorf("Finding.String should escape a newline (single-line item): %q", f.String())
	}
}

// Split UTF-8 rune across Writes must reassemble; fmt renderers never trigger this.
func TestSanitizingWriterRuneSplit(t *testing.T) {
	var under bytes.Buffer
	sw := newSanitizingWriter(&under)
	if _, err := sw.Write([]byte{0xE2, 0x82}); err != nil { // incomplete, held back
		t.Fatal(err)
	}
	if under.Len() != 0 {
		t.Errorf("incomplete lead bytes should be held, got %q", under.String())
	}
	if _, err := sw.Write([]byte{0xAC, 0x1b, 'X'}); err != nil { // completes euro, then ESC
		t.Fatal(err)
	}
	if err := sw.Close(); err != nil {
		t.Fatal(err)
	}
	got := under.String()
	if !strings.Contains(got, "€") {
		t.Errorf("split rune not reassembled: %q", got)
	}
	assertSafe(t, "rune-split writer", got)
}

type failOnceWriter struct {
	got      bytes.Buffer
	failNext bool
}

func (f *failOnceWriter) Write(p []byte) (int, error) {
	if f.failNext {
		return 0, errors.New("boom")
	}
	return f.got.Write(p)
}

// Write error must not drop held partial-rune tail (io.Writer contract).
func TestSanitizingWriterPreservesBufferOnError(t *testing.T) {
	fw := &failOnceWriter{}
	sw := newSanitizingWriter(fw)
	// "a" emitted; 0xE2 0x82 held back.
	if _, err := sw.Write([]byte{'a', 0xE2, 0x82}); err != nil {
		t.Fatal(err)
	}
	fw.failNext = true
	n, err := sw.Write([]byte{0xAC})
	if err == nil {
		t.Fatal("expected the underlying write error to surface")
	}
	if n != 0 {
		t.Errorf("on error want n=0 (nothing of p consumed), got %d", n)
	}
	fw.failNext = false
	if _, err := sw.Write([]byte{0xAC}); err != nil {
		t.Fatal(err)
	}
	if err := sw.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fw.got.String(); !strings.Contains(got, "€") {
		t.Errorf("held partial rune lost after error+retry; got %q", got)
	}
}

// Close flushes incomplete UTF-8 as escapes, never raw bytes.
func TestSanitizingWriterCloseFlushesPartial(t *testing.T) {
	var under bytes.Buffer
	sw := newSanitizingWriter(&under)
	if _, err := sw.Write([]byte{'a', 0xE2, 0x82}); err != nil {
		t.Fatal(err)
	}
	if under.String() != "a" {
		t.Errorf("incomplete tail should be held back, got %q", under.String())
	}
	if err := sw.Close(); err != nil {
		t.Fatal(err)
	}
	got := under.String()
	assertNoRawControl(t, "close-flush", got)
	if got != `a\xe2\x82` {
		t.Errorf("Close should flush the held partial as escapes, got %q", got)
	}
}

// TestJSONFileNamesEscapeInvalidBytes: a file name that is not UTF-8 reads in JSON as it does
// in text, each invalid byte as \xNN. Encoded as it is, it would come out as U+FFFD and name
// no file.
func TestJSONFileNamesEscapeInvalidBytes(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	named := filepath.Join(dir, "caf\xe9.flac")
	if err := os.WriteFile(named, data, 0o644); err != nil {
		t.Skipf("filesystem refused a non-UTF-8 name: %v", err)
	}
	want := filepath.Join(dir, `caf\xe9.flac`)
	outName := filepath.Join(dir, "out\xe9.flac")
	wantOut := filepath.Join(dir, `out\xe9.flac`)
	png := filepath.Join(dir, "p\xe9.png")

	// fields decodes one JSON object, or the one element of a list, into a string-valued map.
	fields := func(out string) map[string]any {
		t.Helper()
		var one map[string]any
		if json.Unmarshal([]byte(out), &one) != nil {
			one = decodeJSONOne[map[string]any](t, out)
		}
		return one
	}
	for _, c := range []struct {
		args []string
		want map[string]string
	}{
		{[]string{"dump", named}, map[string]string{"file": want}},
		{[]string{"lint", named}, map[string]string{"file": want}},
		{[]string{"verify", named}, map[string]string{"file": want}},
		{[]string{"caps", named}, map[string]string{"file": want}},
		{[]string{"plan", named, "--set", "TITLE=x"}, map[string]string{"file": want}},
		{[]string{"diff", named, named}, map[string]string{"a": want, "b": want}},
		{[]string{"copy", "--dry-run", named, named}, map[string]string{"source": want, "file": want}},
		{[]string{"set", named, "--set", "TITLE=x", "-o", outName}, map[string]string{"file": want, "output": wantOut}},
		{[]string{"set", named, "--add-cover", writeTempImage(t, "c.png", testfile.PNG())}, map[string]string{"file": want}},
		{[]string{"export-picture", named, "-o", png}, map[string]string{"file": want, "output": filepath.Join(dir, `p\xe9.png`)}},
		{[]string{"lint", "--fix", named}, map[string]string{"file": want}},
		{[]string{"dump", filepath.Join(dir, "gone\xe9.flac")}, map[string]string{"file": filepath.Join(dir, `gone\xe9.flac`)}},
	} {
		out, errb, _ := runCLI(t, append([]string{"--json"}, c.args...)...)
		got := fields(out)
		for field, want := range c.want {
			if got[field] != want {
				t.Errorf("%v: %s = %q, want %q (stderr %q)", c.args[:1], field, got[field], want, errb)
			}
		}
	}
	// The envelope of a failed operand names it the same way.
	out, _, _ := runCLI(t, "--json", "diff", filepath.Join(dir, "gone\xe9.flac"), named)
	var env jsonError
	if err := json.Unmarshal([]byte(out), &env); err != nil || !strings.HasPrefix(env.Error.Message, filepath.Join(dir, `gone\xe9.flac`)+": ") {
		t.Errorf("diff envelope message = %q (%v)", env.Error.Message, err)
	}
	// So does an error that names a path, whatever built its message.
	if err := os.MkdirAll(dir+"\xe9", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		code int
		args []string
	}{
		{2, []string{"clean", named}},
		{2, []string{"plan", named, "--add-cover", dir + "\xe9"}},
		{6, []string{"plan", named, "--add-cover", filepath.Join(dir, "no\xe9.png")}},
		{6, []string{"set", named, "--set", "TITLE=x", "-o", filepath.Join(dir, "no\xe9", "out.flac")}},
	} {
		args := c.args
		out, _, code := runCLI(t, append([]string{"--json"}, args...)...)
		var msg string
		if one := fields(out); one["error"] != nil {
			msg, _ = one["error"].(map[string]any)["message"].(string)
		}
		if code != c.code || !strings.Contains(msg, `\xe9`) || strings.Contains(msg, "\ufffd") {
			t.Errorf("%v: exit %d, message %q; want the byte escaped", args[:1], code, msg)
		}
	}
	// A name that is valid UTF-8 is not touched, whatever it holds.
	if got := jsonFileName("a\nb\u202e\\xe9.flac"); got != "a\nb\u202e\\xe9.flac" {
		t.Errorf("jsonFileName rewrote a valid name: %q", got)
	}
}

// TestJSONPerFileErrorsEscapeInvalidBytes: the reason of a failed save names its directory, and
// the JSON of set, copy and lint --fix carries a byte of it that is not UTF-8 as \xNN.
func TestJSONPerFileErrorsEscapeInvalidBytes(t *testing.T) {
	t.Parallel()
	requireUnwritableDir(t)
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	roDir := filepath.Join(t.TempDir(), "ro\xe9")
	if err := os.Mkdir(roDir, 0o755); err != nil {
		t.Skipf("filesystem refused a non-UTF-8 name: %v", err)
	}
	file := filepath.Join(roDir, "a.flac")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) })

	for _, args := range [][]string{
		{"set", file, "--set", "TITLE=x"},
		{"copy", sampleM4B, file},
		{"lint", "--fix", file},
	} {
		out, _, code := runCLI(t, append([]string{"--json"}, args...)...)
		var one map[string]any
		if json.Unmarshal([]byte(out), &one) != nil {
			one = decodeJSONOne[map[string]any](t, out)
		}
		body, _ := one["error"].(map[string]any)
		msg, _ := body["message"].(string)
		if code != 6 || !strings.Contains(msg, `create temp file in `) || !strings.Contains(msg, `ro\xe9`) {
			t.Errorf("%s: exit %d, message %q; want the directory named with its byte escaped", args[0], code, msg)
		}
	}
	var j jsonPostWrite
	j.setPostWrite(errors.New("sync directory " + roDir + ": input/output error"))
	if !strings.Contains(j.PostWriteWarning, `ro\xe9:`) {
		t.Errorf("postWriteWarning = %q", j.PostWriteWarning)
	}
}
