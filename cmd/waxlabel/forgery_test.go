package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// forgeMark is a newline followed by a distinctive sentinel. A single-line field
// that fails to escape it forges a second output line; SanitizeLine renders the
// newline as \x0a, so the raw mark never appears. The sentinel is unique to these
// test payloads, so a match in real output can only be a forged line - unlike a
// bare \n, which the boundary legitimately keeps as a line separator (so
// assertNoRawControl cannot catch this class).
const forgeMark = "\n!INJECTED!"

// assertNoForgedLine fails if a hostile newline survived as a real line break -
// i.e. a single-line field carrying forgeMark was not run through SanitizeLine.
func assertNoForgedLine(t *testing.T, label, output string) {
	t.Helper()
	if strings.Contains(output, forgeMark) {
		t.Errorf("%s: a hostile newline forged a line (field not SanitizeLine'd); %q present in:\n%q", label, forgeMark, output)
	}
}

// forgeNamedCopy copies src into a temp file whose name embeds forgeMark (a real
// newline; legal on Linux), so the record headers and error lines that print the
// path are tested for line-forgery. It skips if the filesystem refuses the name.
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

// TestNoLineForgeryFromFilename: a path printed in a record header (or an error
// line) must not let an embedded newline forge a line. Covers the commands the
// SanitizeLine sweep now reaches for paths - dump, lint, lint --fix, diff, copy -
// and the not-found error line.
func TestNoLineForgeryFromFilename(t *testing.T) {
	t.Parallel()
	named := forgeNamedCopy(t, sampleFLAC)
	dst := copyFixture(t, sampleFLAC)
	missing := filepath.Join(t.TempDir(), "gone"+forgeMark+".flac")

	cases := []struct {
		name       string
		args       []string
		wantStream func(stdout, stderr string) string // which stream carries the path
	}{
		{"dump header", []string{"dump", named}, func(o, e string) string { return o }},
		{"lint header", []string{"lint", named}, func(o, e string) string { return o }},
		{"lint --fix header", []string{"lint", "--fix", named}, func(o, e string) string { return o }},
		{"diff header", []string{"diff", named, sampleFLAC}, func(o, e string) string { return o }},
		{"copy header", []string{"copy", named, dst}, func(o, e string) string { return o }},
		{"dump not-found (stderr)", []string{"dump", missing}, func(o, e string) string { return e }},
		{"diff not-found (stderr)", []string{"diff", missing, sampleFLAC}, func(o, e string) string { return e }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, _ := runCLI(t, c.args...)
			assertNoForgedLine(t, c.name, c.wantStream(stdout, stderr))
		})
	}
}

// TestNoLineForgeryFromCodec (unit): a file-derived codec name with a newline (an
// unrecognized container codec ID) cannot forge a line in the single-line audio
// summary.
func TestNoLineForgeryFromCodec(t *testing.T) {
	line := audioLine(trackProps("Matroska", wl.AudioTrack{Codec: "X" + forgeMark, SampleRate: 48000, Channels: 2}))
	assertNoForgedLine(t, "audioLine codec", line)
}

// TestNoLineForgeryFromTransferKey (unit): a file-derived transfer key (an
// unvalidated field name from parse) with a newline is escaped on its single line.
func TestNoLineForgeryFromTransferKey(t *testing.T) {
	got := transferLabel(wl.TransferItem{Key: tag.Key("k" + forgeMark)})
	assertNoForgedLine(t, "transferLabel key", got)
}

// TestNoLineForgeryFromTransferReason covers a drop reason that includes a source cover
// MIME containing a newline. The reason is escaped on one report line, so it cannot forge
// a fake loss line in the copy report.
func TestNoLineForgeryFromTransferReason(t *testing.T) {
	var buf strings.Builder
	r := wl.TransferReport{Items: []wl.TransferItem{
		{Kind: wl.TransferPicture, Count: 1, Disposition: wl.Dropped, Reason: "MP4 cannot store image/gif" + forgeMark},
	}}
	renderTransfer(&buf, "a.flac", "b.m4a", r, "FLAC", "MP4")
	assertNoForgedLine(t, "renderTransfer reason", buf.String())
}

// TestNoLineForgeryFromSetNote: the malformed-value note (stderr) and the change
// preview (stdout) both escape a hostile newline in a --set value.
func TestNoLineForgeryFromSetNote(t *testing.T) {
	t.Parallel()
	target := copyFixture(t, sampleFLAC)
	stdout, stderr, _ := runCLI(t, "set", "--set", "RECORDINGDATE=20"+forgeMark, target)
	assertNoForgedLine(t, "set note stderr", stderr)
	assertNoForgedLine(t, "set change stdout", stdout)
}
