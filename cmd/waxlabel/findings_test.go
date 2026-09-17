package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/waxerr"
)

// Cobra suggestion must use real newlines/tabs, not literal \x0a/\x09 escapes.
func TestUnknownCommandSuggestionNotMangled(t *testing.T) {
	_, stderr, code := runCLI(t, "dumps")
	if code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	if strings.Contains(stderr, `\x0a`) || strings.Contains(stderr, `\x09`) {
		t.Errorf("suggestion was mangled with literal escapes:\n%q", stderr)
	}
	if !strings.Contains(stderr, "Did you mean this?") {
		t.Errorf("expected cobra suggestion block; got:\n%q", stderr)
	}
	if !strings.Contains(stderr, "\n\tdump") {
		t.Errorf("expected a real tab-indented suggestion line; got:\n%q", stderr)
	}
}

// Dead-ends get "run '<cmd> --help'"; self-documenting messages do not.
func TestUsageHintOnDeadEnds(t *testing.T) {
	_, stderr, code := runCLI(t, "set")
	if code != 2 || !strings.Contains(stderr, "run 'waxlabel set --help' for usage") {
		t.Errorf("set with no args: code %d, stderr %q; want exit 2 + set help hint", code, stderr)
	}

	_, stderr, _ = runCLI(t, "dump", "--bogus")
	if !strings.Contains(stderr, "run 'waxlabel dump --help' for usage") {
		t.Errorf("unknown flag: want dump help hint; got %q", stderr)
	}

	_, stderr, _ = runCLI(t, "dumps")
	if !strings.Contains(stderr, "run 'waxlabel --help' for usage") {
		t.Errorf("unknown command: want bare waxlabel hint; got %q", stderr)
	}

	_, stderr, code = runCLI(t, "set", sampleFLAC, "--preset", "bogus")
	if code != 2 {
		t.Fatalf("unknown preset exit = %d, want 2", code)
	}
	if strings.Contains(stderr, "for usage") {
		t.Errorf("self-documenting message should carry no hint; got %q", stderr)
	}
}

// Removed legacy/preset names rejected as unknown; surviving values still parse.
func TestRemovedWritePoliciesRejected(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)

	for _, v := range []string{"reconcile", "update-existing"} {
		_, stderr, code := runCLI(t, "set", file, "--set", "TITLE=x", "--legacy", v)
		if code != 2 {
			t.Errorf("--legacy %s: exit = %d, want 2", v, code)
		}
		if !strings.Contains(stderr, "unknown legacy policy") || !strings.Contains(stderr, "preserve|strip") {
			t.Errorf("--legacy %s: stderr = %q, want unknown-legacy-policy naming preserve|strip", v, stderr)
		}
	}

	if _, stderr, code := runCLI(t, "set", file, "--set", "TITLE=x", "--preset", "canonical"); code != 2 ||
		!strings.Contains(stderr, "unknown preset") || !strings.Contains(stderr, "preserve|compatible|minimal") {
		t.Errorf("--preset canonical: exit = %d, stderr = %q, want exit 2 unknown-preset naming preserve|compatible|minimal", code, stderr)
	}

	for _, extra := range [][]string{
		{"--legacy", "preserve"}, {"--legacy", "strip"},
		{"--preset", "preserve"}, {"--preset", "compatible"}, {"--preset", "minimal"},
	} {
		args := append([]string{"plan", file, "--set", "TITLE=x"}, extra...)
		if _, stderr, code := runCLI(t, args...); code != 0 {
			t.Errorf("%v: exit = %d, stderr = %q, want 0", extra, code, stderr)
		}
	}
}

// copy not-found wording matches other commands; JSON envelope shape differs (path in message).
func TestCopyNotFoundMatchesOtherCommands(t *testing.T) {
	dst := copyFixture(t, sampleM4B)
	missing := filepath.Join(t.TempDir(), "nope.flac")
	_, stderr, code := runCLI(t, "copy", missing, dst)
	if code != 6 {
		t.Fatalf("copy missing src exit = %d, want 6", code)
	}
	want := missing + ": no such file or directory"
	if !strings.Contains(stderr, want) {
		t.Errorf("copy not-found human message = %q, want it to contain %q", stderr, want)
	}

	// Decode JSON: Windows backslashes are escaped in output.
	stdout, _, code := runCLI(t, "copy", missing, dst, "--json")
	if code != 6 {
		t.Fatalf("copy --json missing src exit = %d, want 6\n%s", code, stdout)
	}
	var env jsonError
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("copy --json output is not an error envelope: %v\n%s", err, stdout)
	}
	if env.Error.Code != "not-found" || env.Error.Message != want {
		t.Errorf("copy --json not-found = {code %q, message %q}, want {\"not-found\", %q}",
			env.Error.Code, env.Error.Message, want)
	}
}

// -o refuses clobber unless --overwrite; same path exempt.
func TestSetOutputOverwriteGuard(t *testing.T) {
	in := copyFixture(t, sampleFLAC)
	existing := filepath.Join(t.TempDir(), "existing.flac")
	if err := os.WriteFile(existing, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runCLI(t, "set", in, "--set", "TITLE=X", "-o", existing)
	if code != 2 || !strings.Contains(stderr, "already exists") {
		t.Errorf("overwrite without flag: code %d, stderr %q; want exit 2 'already exists'", code, stderr)
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep me" {
		t.Errorf("refused -o target was modified: %q", b)
	}

	_, _, code = runCLI(t, "set", in, "--set", "TITLE=X", "-o", existing, "--overwrite")
	if code != 0 {
		t.Errorf("overwrite with --overwrite: code %d, want 0", code)
	}

	_, _, code = runCLI(t, "set", in, "--set", "TITLE=Y", "-o", in)
	if code != 0 {
		t.Errorf("set f -o f (same file): code %d, want 0", code)
	}

	// Hardlink is distinct path; rename replaces dir entry only, needs --overwrite.
	hardlink := filepath.Join(filepath.Dir(in), "hardlink.flac")
	if err := os.Link(in, hardlink); err != nil {
		t.Logf("skipping hardlink case (hardlinks unsupported here): %v", err)
	} else {
		_, stderr, code = runCLI(t, "set", in, "--set", "TITLE=X", "-o", hardlink)
		if code != 2 || !strings.Contains(stderr, "already exists") {
			t.Errorf("hardlink -o target without --overwrite: code %d, stderr %q; want exit 2 'already exists'", code, stderr)
		}
		if _, _, code = runCLI(t, "set", in, "--set", "TITLE=X", "-o", hardlink, "--overwrite"); code != 0 {
			t.Errorf("hardlink -o target with --overwrite: code %d, want 0", code)
		}
	}

	// Dangling symlink: Lstat guard (Stat would follow and miss it).
	dir := t.TempDir()
	dangling := filepath.Join(dir, "dangling.flac")
	if err := os.Symlink(filepath.Join(dir, "no-such-target"), dangling); err != nil {
		t.Logf("skipping dangling-symlink case (symlinks unsupported here): %v", err)
	} else {
		_, _, code = runCLI(t, "set", in, "--set", "TITLE=X", "-o", dangling)
		if code != 2 {
			t.Errorf("dangling -o symlink should be refused without --overwrite: code %d, want 2", code)
		}
		if fi, err := os.Lstat(dangling); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("dangling -o symlink was clobbered despite refusal (mode %v, err %v)", fi.Mode(), err)
		}
		// Still refused with --overwrite: broken link is not a replaceable file.
		if _, _, code = runCLI(t, "set", in, "--set", "TITLE=X", "-o", dangling, "--overwrite"); code != 2 {
			t.Errorf("dangling -o symlink with --overwrite should still be refused: code %d, want 2", code)
		}
		if fi, err := os.Lstat(dangling); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("dangling -o symlink clobbered under --overwrite (mode %v, err %v)", fi.Mode(), err)
		}
	}

	// Missing input wins over already-exists on -o target.
	missing := filepath.Join(t.TempDir(), "missing.flac")
	if _, _, code = runCLI(t, "set", missing, "--set", "TITLE=X", "-o", existing); code != 6 {
		t.Errorf("missing input + existing -o: code %d, want 6 (not-found), not 2", code)
	}

	subdir := filepath.Join(t.TempDir(), "outdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runCLI(t, "set", in, "-o", subdir)
	if code != 2 || !strings.Contains(stderr, "is a directory") {
		t.Errorf("-o directory: code %d, stderr %q; want exit 2 'is a directory'", code, stderr)
	}
	if _, _, code = runCLI(t, "set", in, "-o", subdir, "--overwrite"); code != 2 {
		t.Errorf("-o directory with --overwrite: code %d, want 2 (still rejected)", code)
	}
}

// --overwrite without -o is advisory on stderr, even under --json.
func TestSetOverwriteWithoutOutputWarns(t *testing.T) {
	in := copyFixture(t, sampleFLAC)

	_, stderr, code := runCLI(t, "set", in, "--set", "TITLE=X", "--overwrite")
	if code != 0 {
		t.Errorf("--overwrite without -o: exit = %d, want 0 (non-fatal advisory)", code)
	}
	if !strings.Contains(stderr, "--overwrite has no effect") {
		t.Errorf("--overwrite without -o should note it has no effect on stderr; got %q", stderr)
	}

	in2 := copyFixture(t, sampleFLAC)
	stdout, stderr, code := runCLI(t, "set", in2, "--set", "TITLE=X", "--overwrite", "--json")
	if code != 0 {
		t.Errorf("--overwrite without -o (--json): exit = %d, want 0", code)
	}
	if !strings.Contains(stderr, "--overwrite has no effect") {
		t.Errorf("--json run should still note --overwrite on stderr; got %q", stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Errorf("--json stdout must stay valid JSON, uncontaminated by the advisory; got %q", stdout)
	}
	if strings.Contains(stdout, "--overwrite has no effect") {
		t.Errorf("the advisory must not leak into --json stdout; got %q", stdout)
	}
}

// No-op -o: one verbatim-copy line, not contradictory plan + Wrote.
func TestSetOutputNoOpVerbatim(t *testing.T) {
	in := copyFixture(t, sampleFLAC)
	out := filepath.Join(t.TempDir(), "out.flac")
	stdout, _, code := runCLI(t, "set", in, "-o", out)
	if code != 0 {
		t.Fatalf("no-op -o exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "No metadata changes; wrote a verbatim copy to "+out) {
		t.Errorf("expected the single verbatim-copy line; got:\n%q", stdout)
	}
	if strings.Contains(stdout, "no changes (already up to date)") || strings.Contains(stdout, ": plan") {
		t.Errorf("no-op -o should not print the change preview; got:\n%q", stdout)
	}
}

// --strict key check is pre-flight (exit 2); non-strict defers unknown-key note until file exists.
func TestStrictBeforeNotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.flac")

	_, _, code := runCLI(t, "set", missing, "--strict", "--set", "BOGUS=1")
	if code != 2 {
		t.Errorf("strict + missing file: code %d, want 2 (strict), not 6", code)
	}

	_, stderr, code := runCLI(t, "set", missing, "--set", "BOGUS=1")
	if code != 6 {
		t.Errorf("non-strict + missing file: code %d, want 6 (not-found)", code)
	}
	if strings.Contains(stderr, "is not a known key") {
		t.Errorf("a missing file should not be lectured about its key before not-found:\n%q", stderr)
	}
}

// Unknown help topic exits 2; valid topic and bare help exit 0. Flag after valid command is stripped, not rejected.
func TestHelpTopicExitCode(t *testing.T) {
	if _, _, code := runCLI(t, "help", "bogus"); code != 2 {
		t.Errorf("help bogus exit = %d, want 2", code)
	}
	if _, _, code := runCLI(t, "help", "set", "bogus"); code != 2 {
		t.Errorf("help set bogus (stray topic token) exit = %d, want 2", code)
	}
	if _, _, code := runCLI(t, "help", "dump"); code != 0 {
		t.Errorf("help dump exit = %d, want 0", code)
	}
	if _, _, code := runCLI(t, "help", "set", "--json"); code != 0 {
		t.Errorf("help set --json (flag stripped, not a stray topic) exit = %d, want 0", code)
	}
	if _, _, code := runCLI(t, "help"); code != 0 {
		t.Errorf("bare help exit = %d, want 0", code)
	}
}

// Bare invocation exit 2 with help on stderr; --help exit 0; --json gets envelope.
func TestBareInvocationExitsUsage(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runCLI(t)
	if code != 2 {
		t.Fatalf("bare waxlabel exit = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("bare invocation should print help to stderr, not stdout; stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "Usage:") || !strings.Contains(stderr, "Available Commands:") {
		t.Errorf("stderr should carry the help text: %q", stderr)
	}
	if !strings.Contains(stderr, "waxlabel: no command given") {
		t.Errorf("stderr should carry the explicit 'no command given' line: %q", stderr)
	}

	hout, _, hcode := runCLI(t, "--help")
	if hcode != 0 {
		t.Errorf("--help exit = %d, want 0", hcode)
	}
	if !strings.Contains(hout, "Usage:") {
		t.Errorf("--help should print usage to stdout: %q", hout)
	}

	jout, _, jcode := runCLI(t, "--json")
	if jcode != 2 {
		t.Fatalf("waxlabel --json exit = %d, want 2", jcode)
	}
	var je jsonError
	if err := json.Unmarshal([]byte(jout), &je); err != nil {
		t.Fatalf("--json bare stdout is not the error envelope: %v\n%s", err, jout)
	}
	if je.Error.Code != "usage" {
		t.Errorf("error code = %q, want usage", je.Error.Code)
	}
}

// Aggregate exit is most-severe class, order-independent (corrupt outranks missing).
func TestMultiFileExitMostSevere(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// fLaC magic but unparseable: invalid-data.
	bad := filepath.Join(dir, "garbage.flac")
	if err := os.WriteFile(bad, append([]byte("fLaC"), make([]byte, 64)...), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.x")

	if _, _, code := runCLI(t, "dump", bad); code != 4 {
		t.Fatalf("garbage.flac alone exit = %d, want 4 (invalid-data)", code)
	}
	if _, _, code := runCLI(t, "dump", missing); code != 6 {
		t.Fatalf("missing alone exit = %d, want 6 (not-found)", code)
	}
	if _, _, code := runCLI(t, "dump", bad, missing); code != 4 {
		t.Errorf("dump bad missing exit = %d, want 4 (most-severe)", code)
	}
	if _, _, code := runCLI(t, "dump", missing, bad); code != 4 {
		t.Errorf("dump missing bad (swapped) exit = %d, want 4 (order-independent)", code)
	}
}

// --add-cover on directory is usage error (exit 2), not ReadFile io error. Missing cover stays exit 6.
func TestAddCoverNonRegularIsUsageError(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	coverDir := t.TempDir()
	_, errb, code := runCLI(t, "set", f, "--add-cover", coverDir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage); stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "is a directory") {
		t.Errorf("stderr should explain the directory cover: %q", errb)
	}

	missing := filepath.Join(t.TempDir(), "nope.jpg")
	if _, _, code := runCLI(t, "set", f, "--add-cover", missing); code != 6 {
		t.Errorf("missing cover exit = %d, want 6 (io); checkRegularFile must let does-not-exist through", code)
	}
}

// Plan error element is exactly {schemaVersion,file,error}; no zeroed plan fields.
func TestPlanJSONErrorEntryMinimal(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, _, code := runCLI(t, "--json", "plan", sampleFLAC, missing, "--set", "TITLE=X")
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, out)
	}
	if len(raw) != 2 {
		t.Fatalf("got %d elements, want 2", len(raw))
	}
	errEl := raw[1]
	if _, ok := errEl["error"]; !ok {
		t.Fatalf("second element should carry an error: %v", errEl)
	}
	if len(errEl) != 3 {
		t.Errorf("error element has %d keys, want exactly schemaVersion/file/error: %v", len(errEl), errEl)
	}
	for _, forbidden := range []string{"operations", "noOp", "changes", "bytesBefore", "bytesAfter", "paddingAfter"} {
		if _, present := errEl[forbidden]; present {
			t.Errorf("error element leaks %q; want only schemaVersion/file/error", forbidden)
		}
	}
}

// Failed set JSON: no phantom output/committed/size; output file not created.
func TestSetJSONErrorNoPhantomOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.flac")
	outPath := filepath.Join(dir, "out.flac")
	out, _, code := runCLI(t, "--json", "set", missing, "--set", "TITLE=X", "-o", outPath)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, out)
	}
	if len(raw) != 1 {
		t.Fatalf("got %d elements, want 1", len(raw))
	}
	for _, forbidden := range []string{"output", "committed", "size", "operations", "noOp"} {
		if _, present := raw[0][forbidden]; present {
			t.Errorf("set error element leaks %q", forbidden)
		}
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("the output file must not be created on a failed set")
	}
}

// List commands wrap pre-flight errors in one-element array; non-list keep bare object.
func TestPreflightErrorEnvelopeShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	for _, args := range [][]string{
		{"--json", "dump"},
		{"--json", "dump", dir},
		{"--json", "lint"},
		{"--json", "verify"},
		{"--json", "caps", dir},
	} {
		t.Run("array_"+strings.Join(args[1:], "_"), func(t *testing.T) {
			out, _, code := runCLI(t, args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if je := decodeJSONOne[jsonError](t, out); je.Error.Code != "usage" {
				t.Errorf("error code = %q, want usage", je.Error.Code)
			}
		})
	}

	for _, args := range [][]string{
		{"--json", "diff", "a", "b", "c"},
		{"--json", "keys", "extra"},
		{"--json", "copy", "onlyone"},
		{"--json", "caps", "--format", "bogus"},
		{"--json", "frobnicate"},
	} {
		t.Run("object_"+strings.Join(args[1:], "_"), func(t *testing.T) {
			out, _, _ := runCLI(t, args...)
			var je jsonError
			if err := json.Unmarshal([]byte(out), &je); err != nil {
				t.Fatalf("want a bare object envelope, got: %v\n%s", err, out)
			}
		})
	}
}

// --strict unknown-key aborts pre-flight; single-valued-multi is per-file element in aggregate.
func TestStrictGuardrailShapes(t *testing.T) {
	t.Parallel()

	t.Run("unknown-key-aborts-as-one-element", func(t *testing.T) {
		out, _, code := runCLI(t, "--json", "plan", sampleFLAC, "--strict", "--set", "BOGUS=1")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		je := decodeJSONOne[jsonError](t, out)
		if je.Error.Code != "usage" {
			t.Errorf("error code = %q, want usage", je.Error.Code)
		}
	})

	t.Run("single-valued-is-array-element", func(t *testing.T) {
		out, _, code := runCLI(t, "--json", "plan", sampleFLAC, "--strict", "--add", "ENCODER=a", "--add", "ENCODER=b")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		jr := decodeJSONOne[jsonReport](t, out)
		if jr.Error == nil || jr.Error.Code != "usage" {
			t.Errorf("error = %+v, want a usage element", jr.Error)
		}
	})

	missing := filepath.Join(t.TempDir(), "nope.flac")
	for _, order := range [][]string{{missing, sampleFLAC}, {sampleFLAC, missing}} {
		args := append([]string{"--json", "plan"}, order...)
		args = append(args, "--strict", "--add", "ENCODER=a", "--add", "ENCODER=b")
		out, _, code := runCLI(t, args...)
		if code != 6 {
			t.Errorf("order %v: exit = %d, want 6 (not-found outranks the strict usage error)", order, code)
		}
		if entries := decodeJSONList[jsonReport](t, out); len(entries) != 2 {
			t.Errorf("order %v: %d elements, want 2 (neither discarded)", order, len(entries))
		}
	}
}

// --json overrides --quiet on diff; plain --quiet prints nothing.
func TestDiffQuietJSONEmitsObject(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "diff", "--quiet", sampleFLAC, notagsFLAC)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (differ)", code)
	}
	var jd jsonDiff
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("stdout is not the diff object: %v\n%s", err, out)
	}
	if jd.Identical {
		t.Errorf("diff object should report the files differ")
	}

	qout, qerr, qcode := runCLI(t, "diff", "--quiet", sampleFLAC, notagsFLAC)
	if qcode != 1 || qout != "" || qerr != "" {
		t.Errorf("plain --quiet should print nothing; exit=%d stdout=%q stderr=%q", qcode, qout, qerr)
	}
}

// Recursive walk follows symlinks to audio (WalkDir does not; resolved via Stat).
func TestRecursiveWalkFollowsSymlinkedAudio(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target.flac")
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}
	walk := filepath.Join(base, "walk")
	if err := os.MkdirAll(walk, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(walk, "link.flac")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	out, errb, code := runCLI(t, "--json", "dump", "--recursive", walk)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, errb)
	}
	docs := decodeJSONList[jsonDocument](t, out)
	if len(docs) != 1 {
		t.Fatalf("walk found %d files, want 1 (the symlinked audio file)", len(docs))
	}
	if docs[0].Error != nil {
		t.Errorf("the symlinked audio file should parse: %+v", docs[0].Error)
	}
}

// Symlinked-dir root is resolved and walked; listings use original arg name.
func TestRecursiveWalkThroughSymlinkedDirRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(base, "realdir")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "inside.flac"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(base, "linkdir")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	out, errb, code := runCLI(t, "dump", "--recursive", linkDir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (symlinked-dir root should be walked); stderr=%q", code, errb)
	}
	if !strings.Contains(out, filepath.Join("linkdir", "inside.flac")) {
		t.Errorf("walk did not list the audio under the original arg name 'linkdir':\n%s", out)
	}
	if strings.Contains(out, filepath.Join("realdir", "inside.flac")) {
		t.Errorf("walk leaked the resolved target path 'realdir' instead of the user's arg:\n%s", out)
	}
}

// Dangling audio symlink reported as not-found, not dropped silently.
func TestRecursiveWalkReportsDanglingSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.flac"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nonexistent-target"), filepath.Join(dir, "broken.flac")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	out, _, code := runCLI(t, "--json", "dump", "--recursive", dir)
	if code != 6 {
		t.Fatalf("exit = %d, want 6 (the broken link is reported as not-found)", code)
	}
	docs := decodeJSONList[jsonDocument](t, out)
	if len(docs) != 2 {
		t.Fatalf("walk dumped %d entries, want 2 (real.flac + the broken link)", len(docs))
	}
	var sawNotFound, sawOK bool
	for _, d := range docs {
		switch {
		case d.Error != nil && d.Error.Code == "not-found":
			sawNotFound = true
		case d.Error == nil:
			sawOK = true
		}
	}
	if !sawNotFound {
		t.Error("the dangling symlink should be reported as not-found, not dropped")
	}
	if !sawOK {
		t.Error("the real audio file should still be dumped")
	}
}

// Directory without --recursive: path named once, not doubled in detail.
func TestDirectoryWithoutRecursiveNoDoublePath(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	for _, cmd := range []string{"dump", "verify", "plan", "lint"} {
		t.Run(cmd, func(t *testing.T) {
			args := []string{cmd, d}
			if cmd == "plan" {
				args = append(args, "--set", "TITLE=X")
			}
			_, errb, code := runCLI(t, args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (usage); stderr=%q", code, errb)
			}
			if !strings.Contains(errb, "is a directory; pass --recursive") {
				t.Fatalf("stderr should explain --recursive: %q", errb)
			}
			if strings.Contains(errb, ": "+d+" is a directory") {
				t.Errorf("path appears twice (doubled): %q", errb)
			}
		})
	}
}

// caps/diff/copy reject directory operands as usage error (exit 2), not library exit 4.
func TestNonExpandingCommandsRejectNonRegular(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"caps", []string{"caps", d}},
		{"diff-first", []string{"diff", d, sampleFLAC}},
		{"diff-second", []string{"diff", sampleFLAC, d}},
		{"copy-src", []string{"copy", d, sampleFLAC}},
		{"copy-dst", []string{"copy", sampleFLAC, d}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errb, code := runCLI(t, tc.args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (usage); stderr=%q", code, errb)
			}
			if !strings.Contains(errb, "is a directory") {
				t.Errorf("stderr should name the directory: %q", errb)
			}
		})
	}
}

// copy rejects "-" (no streaming model).
func TestCopyRejectsStdin(t *testing.T) {
	dst := copyFixture(t, sampleFLAC)
	_, stderr, code := runCLI(t, "copy", "-", dst)
	if code != 2 || !strings.Contains(stderr, "standard input") {
		t.Errorf("copy - dst: code %d, stderr %q; want exit 2 mentioning standard input", code, stderr)
	}
	if _, _, code := runCLI(t, "copy", dst, "-"); code != 2 {
		t.Errorf("copy dst -: code %d, want exit 2", code)
	}
}

// At most one "-" per read command; second would replay buffered stdin.
func TestReadCommandsRejectRepeatedStdin(t *testing.T) {
	for _, cmd := range []string{"dump", "verify", "lint", "plan"} {
		_, stderr, code := runCLI(t, cmd, "-", "-")
		if code != 2 || !strings.Contains(stderr, "standard input") {
			t.Errorf("%s - -: code %d, stderr %q; want exit 2 mentioning standard input", cmd, code, stderr)
		}
	}
}

// Empty scalar flags are usage errors, not treated as unset.
func TestRejectEmptyScalarFlags(t *testing.T) {
	file := copyFixture(t, sampleFLAC)
	for _, flag := range []string{"--preset", "--legacy", "--padding", "--synced-lyrics-file"} {
		_, stderr, code := runCLI(t, "set", file, "--set", "TITLE=X", flag, "")
		if code != 2 || !strings.Contains(stderr, "cannot be empty") {
			t.Errorf("set %s '': code %d, stderr %q; want exit 2 'cannot be empty'", flag, code, stderr)
		}
		if _, _, code := runCLI(t, "plan", file, flag, ""); code != 2 {
			t.Errorf("plan %s '': code %d, want exit 2", flag, code)
		}
	}
}

func TestCapsNoArgsHasHint(t *testing.T) {
	_, stderr, code := runCLI(t, "caps")
	if code != 2 {
		t.Fatalf("caps no-args exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "--help") {
		t.Errorf("caps no-args stderr = %q, want the --help hint", stderr)
	}
}

// Empty walk: "note:" advisory, exit 0, not waxlabel: failure prefix.
func TestEmptyWalkNoteNotAFailure(t *testing.T) {
	dir := t.TempDir()
	_, stderr, code := runCLI(t, "plan", dir, "--recursive")
	if code != 0 {
		t.Fatalf("plan empty --recursive exit = %d, want 0", code)
	}
	if !strings.Contains(stderr, "note: no audio files found") {
		t.Errorf("stderr = %q, want 'note: no audio files found'", stderr)
	}
	if strings.Contains(stderr, "waxlabel: no audio") {
		t.Errorf("the exit-0 note should not wear the 'waxlabel:' failure prefix; got %q", stderr)
	}
}

// Skipped non-audio files noted in text mode; --json suppresses.
func TestRecursiveSkippedFileNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "song.flac"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cover.jpg", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, errb, code := runCLI(t, "dump", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "note: 2 file(s) skipped (not recognized by extension)") {
		t.Errorf("expected a skipped-file note for the 2 non-audio files; stderr:\n%s", errb)
	}
	if !strings.Contains(out, "song.flac") {
		t.Errorf("the audio file should still be dumped:\n%s", out)
	}

	if _, jerrb, _ := runCLI(t, "--json", "dump", "--recursive", dir); strings.Contains(jerrb, "skipped") {
		t.Errorf("--json should suppress the skipped-file note; stderr:\n%s", jerrb)
	}
}

// Symlinked non-audio files count toward skipped tally.
func TestRecursiveSkippedCountsSymlinks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "song.flac"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "real.jpg")
	if err := os.WriteFile(target, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.png")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	_, errb, code := runCLI(t, "dump", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "note: 2 file(s) skipped") {
		t.Errorf("a symlinked non-audio file should count toward skipped (want 2):\n%s", errb)
	}
}

// --verify save confirms in text and JSON; normal save omits verified field.
func TestSetVerifyConfirmation(t *testing.T) {
	out, _, code := runCLI(t, "set", copyFixture(t, sampleFLAC), "--set", "TITLE=Verified", "--verify")
	if code != 0 {
		t.Fatalf("set --verify exit = %d, want 0", code)
	}
	if !strings.Contains(out, "Output verified (audio essence + structure)") {
		t.Errorf("human output missing the verified confirmation:\n%s", out)
	}

	jout, _, code := runCLI(t, "--json", "set", copyFixture(t, sampleFLAC), "--set", "TITLE=Y", "--verify")
	if code != 0 {
		t.Fatalf("set --json --verify exit = %d, want 0", code)
	}
	if !strings.Contains(jout, `"verified": true`) {
		t.Errorf("JSON output missing verified:true:\n%s", jout)
	}

	jplain, _, _ := runCLI(t, "--json", "set", copyFixture(t, sampleFLAC), "--set", "TITLE=Z")
	if strings.Contains(jplain, "verified") {
		t.Errorf("a non-verify save should not mention verified:\n%s", jplain)
	}
}

// Stray positional from unquoted value refused; set adds "nothing was written", plan does not.
func TestUnquotedValueHint(t *testing.T) {
	file := copyFixture(t, sampleFLAC)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, "set", file, "--set", "TITLE=Two", "Words")
	if code != 2 {
		t.Fatalf("unquoted-value set exit = %d, want 2 (refused before writing)", code)
	}
	if !strings.Contains(stderr, "must be quoted") || !strings.Contains(stderr, "nothing was written") {
		t.Errorf("stderr should carry the quoting hint and say nothing was written; got:\n%s", stderr)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Error("set refused the unquoted-value run but still modified the named file")
	}

	if _, pstderr, pcode := runCLI(t, "plan", file, "--set", "TITLE=Two", "Words"); pcode != 2 {
		t.Fatalf("plan stray bare word exit = %d, want 2 (refused); stderr=%s", pcode, pstderr)
	} else if !strings.Contains(pstderr, "must be quoted") {
		t.Errorf("plan should carry the quoting hint; got:\n%s", pstderr)
	} else if strings.Contains(pstderr, "nothing was written") {
		t.Errorf("plan never writes, so it must not claim 'nothing was written'; got:\n%s", pstderr)
	}

	_, stderr, code = runCLI(t, "set", file, copyFixture(t, sampleFLAC), "--set", "TITLE=One")
	if code != 0 {
		t.Fatalf("two real files exit = %d, want 0", code)
	}
	if strings.Contains(stderr, "must be quoted") {
		t.Errorf("no bare word, so no quoting hint expected; got:\n%s", stderr)
	}

	// Extensionless audio file resolves; not refused as stray word.
	extless := filepath.Join(t.TempDir(), "song")
	if err := os.WriteFile(extless, before, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, estderr, ecode := runCLI(t, "set", extless, "--set", "TITLE=X"); ecode != 0 {
		t.Fatalf("legitimate extensionless file exit = %d, want 0 (edited, not refused); stderr=%s", ecode, estderr)
	}
}

// Empty operand is usage error (exit 2), not invalid-data; cannot outrank not-found in batch.
func TestEmptyFilenameUsage(t *testing.T) {
	t.Parallel()
	if _, _, code := runCLI(t, "dump", ""); code != 2 {
		t.Errorf(`dump "" exit = %d, want 2 (usage)`, code)
	}
	out, _, code := runCLI(t, "--json", "dump", "", "missing.flac")
	if code != 2 {
		t.Errorf(`dump "" missing.flac exit = %d, want 2`, code)
	}
	if strings.Contains(out, "invalid-data") {
		t.Errorf("empty filename must not classify as invalid-data:\n%s", out)
	}
	if _, _, code := runCLI(t, "copy", "", filepath.Join(t.TempDir(), "x.flac")); code != 2 {
		t.Errorf(`copy "" dst exit = %d, want 2`, code)
	}
	if _, _, code := runCLI(t, "diff", "", sampleFLAC); code != 2 {
		t.Errorf(`diff "" b exit = %d, want 2`, code)
	}
	if _, _, code := runCLI(t, "caps", ""); code != 2 {
		t.Errorf(`caps "" exit = %d, want 2`, code)
	}
	if out, _, code := runCLI(t, "--json", "caps", "", "missing.flac"); code != 2 || strings.Contains(out, "invalid-data") {
		t.Errorf(`caps "" missing.flac exit = %d (want 2), invalid-data in output=%v`, code, strings.Contains(out, "invalid-data"))
	}
}

func TestDiffPerFilePathPrefix(t *testing.T) {
	t.Parallel()
	png := writeTempImage(t, "red.png", minimalPNG())
	_, errb, code := runCLI(t, "diff", png, sampleFLAC)
	if code < 2 {
		t.Fatalf("diff of a non-audio file exit = %d, want a real error (>= 2); stderr=%s", code, errb)
	}
	if !strings.Contains(errb, "red.png:") {
		t.Errorf("diff parse error should carry the 'red.png:' per-file prefix:\n%s", errb)
	}
}

// Usage and per-file JSON errors carry same hints as human render (e.g. "--", source-changed re-run).
func TestJSONErrorCarriesHint(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "dump", "-track.flac")
	if code != 2 {
		t.Fatalf("leading-dash arg exit = %d, want 2; out=%s", code, out)
	}
	je := decodeJSONOne[jsonError](t, out)
	if !strings.Contains(je.Error.Hint, "--") {
		t.Errorf("JSON usage envelope hint missing the '--' guidance; got %q\n%s", je.Error.Hint, out)
	}
	wantHint := "the file changed since it was read; re-run to pick up the new contents"
	if got := errorEntry("f.flac", fmt.Errorf("reading: %w", waxerr.ErrSourceChanged)).Error.Hint; got != wantHint {
		t.Errorf("per-file error entry hint = %q, want %q", got, wantHint)
	}
}

// No-op plan JSON emits "changes": [], not null or omitted.
func TestPlanJSONEmptyChangesArray(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "plan", sampleFLAC)
	if code != 0 {
		t.Fatalf("no-op plan --json exit = %d, want 0", code)
	}
	if !strings.Contains(out, `"changes": []`) {
		t.Errorf(`no-op plan --json should emit "changes": [], got:\n%s`, out)
	}
}

// Aliases (DATE etc.) resolve to canonical keys; unknown keys still flagged/rejected under --strict.
func TestVorbisAliasCanonicalized(t *testing.T) {
	f := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", f, "--set", "RECORDINGDATE=2019"); code != 0 {
		t.Fatalf("seed RECORDINGDATE exit = %d", code)
	}
	if _, stderr, code := runCLI(t, "set", f, "--set", "DATE=2021"); code != 0 {
		t.Fatalf("set --set DATE exit = %d, want 0; stderr: %s", code, stderr)
	} else if strings.Contains(stderr, "custom field") || strings.Contains(stderr, "not a known key") {
		t.Errorf("DATE must resolve to RECORDINGDATE, not be flagged a custom field; stderr:\n%s", stderr)
	}
	out, _, _ := runCLI(t, "dump", f)
	if strings.Count(out, "RECORDINGDATE") != 1 || !strings.Contains(out, "RECORDINGDATE  2021") {
		t.Errorf("DATE should replace RECORDINGDATE with one value, not duplicate it:\n%s", out)
	}

	mp3 := copyFixture(t, sampleMP3)
	if _, _, code := runCLI(t, "set", "--strict", mp3, "--set", "DATE=2021"); code != 0 {
		t.Errorf("--strict --set DATE on MP3 exit = %d, want 0 (DATE resolves to a known key)", code)
	}
	if out, _, _ := runCLI(t, "dump", mp3); !strings.Contains(out, "RECORDINGDATE  2021") {
		t.Errorf("DATE on MP3 should be stored as RECORDINGDATE:\n%s", out)
	}

	if _, stderr, code := runCLI(t, "set", copyFixture(t, sampleFLAC), "--set", "BOGUSKEY=x"); code != 0 || !strings.Contains(stderr, "custom field") {
		t.Errorf("unknown key: exit %d, want 0 with a custom-field note; stderr:\n%s", code, stderr)
	}
	if _, _, code := runCLI(t, "set", "--strict", copyFixture(t, sampleFLAC), "--set", "BOGUSKEY=x"); code != 2 {
		t.Errorf("--strict unknown key exit = %d, want 2 (rejected)", code)
	}
}

// --json --version emits JSON object; plain --version stays text.
func TestJSONVersionFlag(t *testing.T) {
	stdout, _, code := runCLI(t, "--json", "--version")
	if code != 0 {
		t.Fatalf("--json --version exit = %d, want 0", code)
	}
	var jv jsonVersion
	if err := json.Unmarshal([]byte(stdout), &jv); err != nil {
		t.Fatalf("--json --version did not emit JSON: %v\noutput: %s", err, stdout)
	}
	if jv.SchemaVersion != schemaVersion || jv.Version == "" {
		t.Errorf("version JSON = %+v, want schemaVersion %d and a non-empty version", jv, schemaVersion)
	}

	text, _, code := runCLI(t, "--version")
	if code != 0 || !strings.HasPrefix(text, "waxlabel version ") {
		t.Errorf("--version = %q (exit %d), want a 'waxlabel version...' line", text, code)
	}
}

// Leftover temps noted on recursive walk; in-flight temps (young) omitted. Suppressed under --json.
func TestRecursiveWalkNotesLeftoverTemps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.flac"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, dir, ".waxlabel-99.tmp", 2*time.Hour)

	_, errb, code := runCLI(t, "dump", "--recursive", dir)
	if code != 0 || !strings.Contains(errb, "1 leftover temp file") || !strings.Contains(errb, "waxlabel clean") {
		t.Errorf("exit %d stderr %q", code, errb)
	}
	out, _, _ := runCLI(t, "clean", dir)
	if !strings.Contains(out, ".waxlabel-99.tmp") {
		t.Errorf("clean should list the leftover the note counted:\n%s", out)
	}

	fresh := t.TempDir()
	if err := os.WriteFile(filepath.Join(fresh, "b.flac"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, fresh, ".waxlabel-1.tmp", time.Minute)
	if _, errb, _ := runCLI(t, "dump", "--recursive", fresh); strings.Contains(errb, "leftover") {
		t.Errorf("a write in flight must not be reported as a leftover: %q", errb)
	}

	outJSON, errJSON, _ := runCLI(t, "--json", "dump", "--recursive", dir)
	if strings.Contains(outJSON, "leftover") || strings.Contains(errJSON, "leftover") {
		t.Errorf("the note must stay out of --json output: stdout %q stderr %q", outJSON, errJSON)
	}
}
