package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// forgeMark is newline + sentinel. Unescaped it forges a second line; SanitizeLine emits \x0a.
// Unique sentinel detects forgery; bare \n in boundaries is legitimate.
const forgeMark = "\n!INJECTED!"

// assertNoForgedLine fails if forgeMark appears as a raw line break.
func assertNoForgedLine(t *testing.T, label, output string) {
	t.Helper()
	if strings.Contains(output, forgeMark) {
		t.Errorf("%s: a hostile newline forged a line (field not SanitizeLine'd); %q present in:\n%q", label, forgeMark, output)
	}
}

// forgeNamedCopy copies src to a temp path whose name embeds forgeMark (newline on Linux).
func forgeNamedCopy(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), "forge"+forgeMark+filepath.Ext(src))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Skipf("filesystem refused a newline filename: %v", err)
	}
	return dst
}

// TestNoLineForgeryFromFilename: a path in a header, an outcome line, a note or an error cannot
// forge a line.
func TestNoLineForgeryFromFilename(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "gone"+forgeMark+".flac")
	forgedOut := func(ext string) string { return filepath.Join(t.TempDir(), "out"+forgeMark+ext) }

	for _, c := range []struct {
		name string
		args func(named string) []string
	}{
		{"dump header", func(n string) []string { return []string{"dump", n} }},
		{"lint header", func(n string) []string { return []string{"lint", n} }},
		{"lint --fix header", func(n string) []string { return []string{"lint", "--fix", n} }},
		{"diff header", func(n string) []string { return []string{"diff", n, sampleFLAC} }},
		{"copy header", func(n string) []string { return []string{"copy", n, copyFixture(t, sampleFLAC)} }},
		{"copy --dry-run", func(n string) []string { return []string{"copy", "--dry-run", sampleM4B, n} }},
		{"dump not-found", func(string) []string { return []string{"dump", missing} }},
		{"diff not-found", func(string) []string { return []string{"diff", missing, sampleFLAC} }},
		{"set in place", func(n string) []string { return []string{"set", n, "--set", "TITLE=x"} }},
		{"set no-op", func(n string) []string { return []string{"set", n, "--set", "TITLE=Original Title"} }},
		{"set discarded", func(n string) []string { return []string{"set", n, "--output-gain", "3"} }},
		{"set -o", func(string) []string {
			return []string{"set", sampleFLAC, "--set", "TITLE=x", "-o", forgedOut(".flac")}
		}},
		{"set -o verbatim", func(string) []string { return []string{"set", sampleFLAC, "-o", forgedOut(".flac")} }},
		{"set -o discarded", func(string) []string {
			return []string{"set", sampleFLAC, "--output-gain", "3", "-o", forgedOut(".flac")}
		}},
		{"set -o extension mismatch", func(string) []string {
			return []string{"set", sampleFLAC, "--set", "TITLE=x", "-o", forgedOut(".mp3")}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, _ := runCLI(t, c.args(forgeNamedCopy(t, sampleFLAC))...)
			if !strings.Contains(stdout+stderr, `\x0a!INJECTED!`) {
				t.Fatalf("the path is not in the output, so the case tests nothing:\n%s%s", stdout, stderr)
			}
			assertNoForgedLine(t, c.name+" stdout", stdout)
			assertNoForgedLine(t, c.name+" stderr", stderr)
		})
	}
}

// TestNoLineForgeryFromErrorReason: a per-file reason names paths of its own (a save names the
// directory it could not write to), and those cannot forge a line either.
func TestNoLineForgeryFromErrorReason(t *testing.T) {
	t.Parallel()
	requireUnwritableDir(t)
	roDir := filepath.Join(t.TempDir(), "ro"+forgeMark)
	if err := os.Mkdir(roDir, 0o755); err != nil {
		t.Skipf("filesystem refused a newline directory name: %v", err)
	}
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
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
		_, stderr, code := runCLI(t, args...)
		if code != 6 || !strings.Contains(stderr, "create temp file in ") {
			t.Fatalf("%v: exit %d, stderr %q; want the temp-create failure", args, code, stderr)
		}
		assertNoForgedLine(t, args[0], stderr)
	}

	var note strings.Builder
	warnPostCommit(&note, false, "a.flac", errors.New("sync directory "+roDir+": input/output error"))
	assertNoForgedLine(t, "post-write note", note.String())
}

// TestNoLineForgeryFromCodec: newline in codec name cannot forge audio summary line.
func TestNoLineForgeryFromCodec(t *testing.T) {
	line := audioLine(trackProps("Matroska", wl.AudioTrack{Codec: "X" + forgeMark, SampleRate: 48000, Channels: 2}))
	assertNoForgedLine(t, "audioLine codec", line)
}

// TestNoLineForgeryFromTransferKey: newline in transfer key is escaped on one line.
func TestNoLineForgeryFromTransferKey(t *testing.T) {
	got := transferLabel(wl.TransferItem{Key: tag.Key("k" + forgeMark)})
	assertNoForgedLine(t, "transferLabel key", got)
}

// TestNoLineForgeryFromTransferReason: newline in drop reason cannot forge copy report line.
func TestNoLineForgeryFromTransferReason(t *testing.T) {
	var buf strings.Builder
	r := wl.TransferReport{Items: []wl.TransferItem{
		{Kind: wl.TransferPicture, Count: 1, Disposition: wl.Dropped, Reason: "MP4 cannot store image/gif" + forgeMark},
	}}
	renderTransfer(&buf, "a.flac", "b.m4a", r, "FLAC", "MP4")
	assertNoForgedLine(t, "renderTransfer reason", buf.String())
}

// TestNoLineForgeryFromSetNote: stderr note and stdout preview escape newline in --set value.
func TestNoLineForgeryFromSetNote(t *testing.T) {
	t.Parallel()
	target := copyFixture(t, sampleFLAC)
	stdout, stderr, _ := runCLI(t, "set", "--set", "RECORDINGDATE=20"+forgeMark, target)
	assertNoForgedLine(t, "set note stderr", stderr)
	assertNoForgedLine(t, "set change stdout", stdout)
}
