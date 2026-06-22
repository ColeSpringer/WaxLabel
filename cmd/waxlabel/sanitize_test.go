package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// hostilePayload is an OSC "set terminal title" sequence (ESC ] 0 ; ... BEL) - the
// exact terminal-hijack class the sanitizing output boundary exists to neutralize.
// Both ESC (0x1b) and BEL (0x07) are control bytes that must never reach the
// terminal raw.
const hostilePayload = "evil\x1b]0;pwned\x07end"

// assertSafe asserts s carries no raw terminal-control byte and that the hostile
// ESC survived as a visible \x1b escape - proving the field was actually rendered,
// not silently dropped. ESC is representation-stable (it is \x1b under %q and under
// SanitizeText/SanitizeLine alike), so keying on it does not assume which escaper
// ran.
func assertSafe(t *testing.T, label, s string) {
	t.Helper()
	if !strings.Contains(s, `\x1b`) {
		t.Errorf("%s: want a visible \\x1b escape (hostile field rendered); got:\n%q", label, s)
	}
	assertNoRawControl(t, label, s)
}

// assertNoRawControl fails on any byte the sanitizer is contracted to escape: a C0
// control other than tab/newline, DEL, a C1 control, or an invalid UTF-8 byte. It
// restates the bar of the unexported tag.controlRune independently - a security
// backstop wants its own statement of the threshold - and scans with a manual
// decode loop so an invalid byte surfaces as size==1 (a plain range would hide it).
// Tab and newline are allowed: the boundary keeps them deliberately (the newline
// separates output lines and carries multi-line tag values).
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

// forbiddenRaw is the independent restatement of tag.controlRune's bar (that
// predicate is unexported): a control rune the sanitizer must escape from human
// output. Tab and newline are intentionally exempt.
func forbiddenRaw(r rune) bool {
	if r == '\t' || r == '\n' {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// seedValue copies src's fixture to a fresh temp file carrying val under key,
// returning the new path. It writes through the public API (proven to preserve
// arbitrary value bytes), so the renderers see genuinely file-derived content end
// to end rather than a value the test injected past the parser.
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

// hostileNamedCopy copies src into a temp file whose *name* embeds hostilePayload.
// A Linux filename may hold any byte but '/' and NUL, so such a name - handed over
// by a shell glob or the --recursive walk - reaches a record header or an error
// line before a byte of the file is parsed. It skips the test if the filesystem
// refuses the name.
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

// TestBoundarySanitizesHostileContent drives each human command end to end through
// dispatch (the real boundary, via runCLI) with a file whose *content* carries a
// terminal-hijack sequence, asserting stdout renders it as a visible escape and
// that neither stream leaks a raw control byte. This covers the renderer classes
// the boundary stands behind: dump tags, the dump --native blocks (Matroska, where
// the segment title is file-controlled - the native leak the plan names), the
// plan/set change preview, the diff change line, and the lint finding line.
func TestBoundarySanitizesHostileContent(t *testing.T) {
	t.Parallel()
	hostileFLAC := seedValue(t, sampleFLAC, tag.Title, hostilePayload)
	hostileMKA := seedValue(t, sampleMKA, tag.Title, hostilePayload)
	// A malformed date drives a lint finding whose message embeds the hostile bytes,
	// so the lint/lint --fix lines (Finding.String) are exercised with file-derived
	// content; the date is not auto-fixable, so it also appears under lint --fix.
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

// TestBoundarySanitizesHostileFilename covers the larger class: the file *path* is
// printed - in a record header on stdout, and in an error line on stderr - before
// or without any content being parsed. It exercises a hostile name through the
// per-file commands' headers and through both error-line shapes (the per-file
// "waxlabel: <path>: ..." that dump writes, and the terminal "no such file:
// <path>" that diff routes through renderError).
func TestBoundarySanitizesHostileFilename(t *testing.T) {
	t.Parallel()
	named := hostileNamedCopy(t, sampleFLAC)
	// A path that does not exist: its bytes still reach stderr through the not-found
	// reporting, before any file is opened.
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

// TestBoundaryJSONStaysRaw pins the deliberate exemption: --json is the machine
// contract, so the boundary unwraps to the raw stream for it. A value carrying DEL
// (0x7f) and a C1 control (U+009B) - both of which json.Encoder emits raw - must
// survive byte-for-byte, and the output must stay valid JSON. (Sanitizing it would
// rewrite those bytes as \x7f/\x9b, which are not valid JSON string escapes.)
func TestBoundaryJSONStaysRaw(t *testing.T) {
	t.Parallel()
	// The value carries DEL (0x7f) and a C1 control (U+009B); json.Encoder emits
	// both raw, so they must survive byte-for-byte through the exempted JSON path.
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

// TestRenderPicturesSanitizesHostileMIME: a picture's file-derived MIME with a
// control byte is escaped on its single-line row (SanitizeLine), never leaked raw.
// The picture Type is an enum, so the only hostile bytes come from the MIME.
func TestRenderPicturesSanitizesHostileMIME(t *testing.T) {
	var buf bytes.Buffer
	renderPictures(&buf, []wl.Picture{{
		Type: wl.PicFrontCover,
		MIME: "image/\x1b]0;pwned\x07png",
		Data: []byte("xx"),
	}})
	assertSafe(t, "renderPictures MIME", buf.String())
}

// TestRenderChaptersSanitizesHostileTitle: a file-derived chapter title with a
// control byte is escaped on the single-line chapter row.
func TestRenderChaptersSanitizesHostileTitle(t *testing.T) {
	var buf bytes.Buffer
	renderChapters(&buf, []wl.Chapter{{Title: "chapter\x1b]0;pwned\x07one"}})
	assertSafe(t, "renderChapters title", buf.String())
}

// TestWarningStringSanitizes / TestFindingStringSanitizes / TestWriteReportStringSanitizes
// pin the Layer-2 invariant: the library String() methods self-sanitize their
// file-derived parts, so a consumer that prints them without the CLI boundary is
// safe too (and the CLI's now-dropped per-line wraps were redundant).
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

// TestWarningAndFindingStringEscapeNewline: Warning and Finding each print as one
// list item, so a newline in a file-derived message must be escaped (output
// spoofing), never emitted as a raw line break that forges an extra item.
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

// TestSanitizingWriterRuneSplit exercises the boundary's rune-boundary hardening,
// which the fmt-based renderers never trigger (each emits a complete UTF-8 unit in
// one Write): a multi-byte rune split across two Writes must be reassembled, not
// escaped as a stray invalid lead byte, while a control byte in the same stream is
// still escaped. "€" is 0xE2 0x82 0xAC.
func TestSanitizingWriterRuneSplit(t *testing.T) {
	var under bytes.Buffer
	sw := newSanitizingWriter(&under)
	if _, err := sw.Write([]byte{0xE2, 0x82}); err != nil { // incomplete: held back
		t.Fatal(err)
	}
	if under.Len() != 0 {
		t.Errorf("incomplete lead bytes should be held, got %q", under.String())
	}
	if _, err := sw.Write([]byte{0xAC, 0x1b, 'X'}); err != nil { // completes €, then ESC X
		t.Fatal(err)
	}
	if err := sw.Close(); err != nil {
		t.Fatal(err)
	}
	got := under.String()
	if !strings.Contains(got, "€") {
		t.Errorf("split rune not reassembled: %q", got)
	}
	assertSafe(t, "rune-split writer", got) // ESC escaped, no raw control byte
}

// failOnceWriter writes through to a buffer, but fails the one write for which
// failNext is set - so a test can make the underlying stream error on a chosen
// Write and then recover.
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

// TestSanitizingWriterPreservesBufferOnError: an underlying write error must not
// drop the previously held partial-rune tail. After the error, the held bytes are
// still buffered, so a retry (with the stream recovered) emits the complete rune -
// the io.Writer-contract / no-data-loss fix.
func TestSanitizingWriterPreservesBufferOnError(t *testing.T) {
	fw := &failOnceWriter{}
	sw := newSanitizingWriter(fw)
	// "a" is emitted; the 2-byte head of "€" (0xE2 0x82) is held back.
	if _, err := sw.Write([]byte{'a', 0xE2, 0x82}); err != nil {
		t.Fatal(err)
	}
	// The completing byte arrives, but the underlying stream fails this write.
	fw.failNext = true
	n, err := sw.Write([]byte{0xAC})
	if err == nil {
		t.Fatal("expected the underlying write error to surface")
	}
	if n != 0 {
		t.Errorf("on error want n=0 (nothing of p consumed), got %d", n)
	}
	// Recover and retry: the held 0xE2 0x82 was not lost, so "€" completes.
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

// TestSanitizingWriterCloseFlushesPartial: a trailing incomplete sequence that
// never completes is flushed on Close as a visible escape, never as a raw byte -
// so even the flush path cannot leak one.
func TestSanitizingWriterCloseFlushesPartial(t *testing.T) {
	var under bytes.Buffer
	sw := newSanitizingWriter(&under)
	if _, err := sw.Write([]byte{'a', 0xE2, 0x82}); err != nil { // 'a' emitted, 0xE2 0x82 held
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
