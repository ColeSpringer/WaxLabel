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

// chainedOgg writes two Ogg streams back to back into a temp file.
func chainedOgg(t *testing.T) string {
	t.Helper()
	var data []byte
	for _, name := range []string{"sample.ogg", "notags.ogg"} {
		b, err := os.ReadFile(td(name))
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, b...)
	}
	path := filepath.Join(t.TempDir(), "chained.ogg")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestVerifyRefusesChainedOgg: only a chained file's first link is read, so verify gives it no
// essence digest: exit 3 with the read warning that says why, and the other files still verified.
func TestVerifyRefusesChainedOgg(t *testing.T) {
	chained := chainedOgg(t)
	for _, order := range [][]string{{chained, td("sample.ogg")}, {td("sample.ogg"), chained}} {
		out, errOut, code := runCLI(t, append([]string{"verify"}, order...)...)
		if code != 3 {
			t.Errorf("exit = %d, want 3\n%s\n%s", code, out, errOut)
		}
		if !strings.Contains(errOut, "[chained-stream]") || !strings.Contains(errOut, "chained.ogg") {
			t.Errorf("stderr does not name the chained file with its read warning:\n%s", errOut)
		}
		// Only the warning that explains the refusal, not everything the read warned about.
		if strings.Count(errOut, "\n") != 2 || strings.Contains(errOut, "[inherited-encoder]") {
			t.Errorf("stderr should hold the chained-stream warning and the error alone:\n%s", errOut)
		}
		if strings.Count(out, "essence:") != 1 || !strings.Contains(out, "ogg-vorbis-packets-v1") {
			t.Errorf("stdout should hold the single stream's digest alone:\n%s", out)
		}
	}

	out, _, code := runCLI(t, "--json", "verify", chained, td("sample.ogg"))
	if code != 3 {
		t.Errorf("--json exit = %d, want 3", code)
	}
	var items []jsonVerify
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 2 {
		t.Fatalf("--json output: %v\n%s", err, out)
	}
	if items[0].Error == nil || items[0].Error.Code != "unsupported-stream" || items[0].Essence != "" {
		t.Errorf("chained entry = %+v, want the unsupported-stream error and no digest", items[0])
	}
	if items[1].Essence == "" || !slices.ContainsFunc(items[1].Warnings, func(w jsonWarning) bool { return w.Code == "inherited-encoder" }) {
		t.Errorf("single-stream entry = %+v, want a digest and its read warnings", items[1])
	}
}

// TestVerifyShowsReadWarnings: verify prints what the parse warned about under the digest, in
// JSON as an array that is empty for a clean file, and leaves it out of the one-line --quiet form.
func TestVerifyShowsReadWarnings(t *testing.T) {
	data, err := os.ReadFile(td("notags.ogg"))
	if err != nil {
		t.Fatal(err)
	}
	junked := filepath.Join(t.TempDir(), "junked.ogg")
	if err := os.WriteFile(junked, append(data, "TRAILING JUNK"...), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLI(t, "verify", junked)
	if code != 0 || !strings.Contains(out, "  warnings (3):\n") || !strings.Contains(out, "    [trailing-bytes] 13 byte(s)") {
		t.Errorf("exit %d, output:\n%s", code, out)
	}
	if out, _, _ := runCLI(t, "verify", "--quiet", junked); strings.Count(out, "\n") != 1 || strings.Contains(out, "trailing-bytes") {
		t.Errorf("--quiet output is not one digest line:\n%s", out)
	}
	if out, _, _ := runCLI(t, "verify", td("notags.flac")); strings.Contains(out, "warnings") {
		t.Errorf("a file with no read warning prints a warnings block:\n%s", out)
	}
	out, _, _ = runCLI(t, "--json", "verify", td("notags.flac"), junked)
	if !strings.Contains(out, `"warnings": []`) || !strings.Contains(out, `"code": "trailing-bytes"`) {
		t.Errorf("--json output:\n%s", out)
	}
}

// TestCapsNamesWhyAFileIsReadOnly: a chained Ogg file is read-only, and caps says why.
func TestCapsNamesWhyAFileIsReadOnly(t *testing.T) {
	chained := chainedOgg(t)
	out, _, code := runCLI(t, "caps", chained)
	if code != 0 || !strings.Contains(out, "read-only: ") || !strings.Contains(out, "chained") {
		t.Errorf("exit %d, output:\n%s", code, out)
	}
	out, _, _ = runCLI(t, "--json", "caps", chained)
	var items []jsonCaps
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 1 {
		t.Fatalf("--json output: %v\n%s", err, out)
	}
	if !items[0].ReadOnly || !strings.Contains(items[0].ReadOnlyReason, "chained") {
		t.Errorf("readOnly = %v, readOnlyReason = %q", items[0].ReadOnly, items[0].ReadOnlyReason)
	}
	out, _, _ = runCLI(t, "--json", "caps", td("sample.ogg"))
	if strings.Contains(out, "readOnly") {
		t.Errorf("a writable file reports read-only fields:\n%s", out)
	}
	// A format that is never written has no reason of the file's own to give.
	for args, want := range map[string]string{
		"--format wma":   "(read-only: this format cannot be written)",
		td("sample.wma"): "(read-only: this file cannot be written; the levels below describe the format)",
	} {
		if out, _, _ := runCLI(t, append([]string{"caps"}, strings.Fields(args)...)...); !strings.Contains(out, want) {
			t.Errorf("caps %s does not print %q:\n%s", args, want, out)
		}
		if out, _, _ := runCLI(t, append([]string{"--json", "caps"}, strings.Fields(args)...)...); strings.Contains(out, "readOnlyReason") {
			t.Errorf("caps %s gives a reason in JSON:\n%s", args, out)
		}
	}
}

// TestHeaderJunkPastTheWindowIsReadOnly: an Ogg file with more junk among its header pages than a
// reader skips cannot have its header rewritten, and says so before an edit is planned: caps
// reports it read-only, and set and copy refuse it the same way.
func TestHeaderJunkPastTheWindowIsReadOnly(t *testing.T) {
	// A comment header of over a page gives two places among the header pages.
	path := filepath.Join(t.TempDir(), "junk.ogg")
	if _, errOut, code := runCLI(t, "set", td("sample.ogg"), "--set", "COMMENT="+strings.Repeat("c", 70000), "-o", path); code != 0 {
		t.Fatalf("setup: exit %d: %s", code, errOut)
	}
	clean, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second := bytes.Index(clean[4:], []byte("OggS")) + 4
	third := second + 4 + bytes.Index(clean[second+4:], []byte("OggS"))
	junked := func(n int) {
		junk := make([]byte, n)
		if err := os.WriteFile(path, slices.Concat(clean[:second], junk, clean[second:third], junk, clean[third:]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	junked(30000)
	if out, _, code := runCLI(t, "caps", path); code != 0 || strings.Contains(out, "read-only") {
		t.Fatalf("setup: 60,000 bytes of junk among the header pages should leave the file writable (exit %d):\n%s", code, out)
	}
	junked(40000)
	out, _, code := runCLI(t, "caps", path)
	if code != 0 || !strings.Contains(out, "read-only: ") || !strings.Contains(out, "editable keys (0)") {
		t.Errorf("caps exit %d:\n%s", code, out)
	}
	for _, args := range [][]string{
		{"set", path, "--set", "TITLE=x"},
		{"copy", td("sample.flac"), path},
	} {
		if _, errOut, code := runCLI(t, args...); code != 3 || !strings.Contains(errOut, "between") {
			t.Errorf("%v: exit %d, want 3 naming the bytes between the header pages:\n%s", args[0], code, errOut)
		}
	}
}
