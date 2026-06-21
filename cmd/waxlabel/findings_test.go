package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnknownCommandSuggestionNotMangled (H1): cobra's multi-line "Did you mean
// this?" suggestion is trusted CLI text, so it must render with real newlines and a
// real tab - never the literal \x0a/\x09 the single-line path would escape.
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
	// A real newline and a real (tab-indented) "dump" suggestion survive.
	if !strings.Contains(stderr, "\n\tdump") {
		t.Errorf("expected a real tab-indented suggestion line; got:\n%q", stderr)
	}
}

// TestUsageHintOnDeadEnds (M5): a cobra dead-end with no built-in guidance - an
// arg-count failure or an unknown flag - gains a "run '<cmd> --help' for usage"
// pointer, with the resolved command path. A self-documenting usagef message does
// not (it would be redundant).
func TestUsageHintOnDeadEnds(t *testing.T) {
	_, stderr, code := runCLI(t, "set")
	if code != 2 || !strings.Contains(stderr, "run 'waxlabel set --help' for usage") {
		t.Errorf("set with no args: code %d, stderr %q; want exit 2 + set help hint", code, stderr)
	}

	_, stderr, _ = runCLI(t, "dump", "--bogus")
	if !strings.Contains(stderr, "run 'waxlabel dump --help' for usage") {
		t.Errorf("unknown flag: want dump help hint; got %q", stderr)
	}

	// Unknown command falls back to the bare "waxlabel" hint (list the commands).
	_, stderr, _ = runCLI(t, "dumps")
	if !strings.Contains(stderr, "run 'waxlabel --help' for usage") {
		t.Errorf("unknown command: want bare waxlabel hint; got %q", stderr)
	}

	// A self-documenting message (unknown preset) carries no redundant hint.
	_, stderr, code = runCLI(t, "set", sampleFLAC, "--preset", "bogus")
	if code != 2 {
		t.Fatalf("unknown preset exit = %d, want 2", code)
	}
	if strings.Contains(stderr, "for usage") {
		t.Errorf("self-documenting message should carry no hint; got %q", stderr)
	}
}

// TestCopyNotFoundMatchesOtherCommands (M4): copy's per-input parse failure reads
// as the "waxlabel: <path>: <reason>" line dump/verify/set print, not the
// classifier's bare "no such file: <path>". JSON output is unchanged.
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

	// JSON path is the machine contract: still the not-found code + "no such file".
	stdout, _, code := runCLI(t, "copy", missing, dst, "--json")
	if code != 6 || !strings.Contains(stdout, `"not-found"`) || !strings.Contains(stdout, "no such file: "+missing) {
		t.Errorf("copy --json not-found = %q (code %d); want not-found envelope unchanged", stdout, code)
	}
}

// TestSetOutputOverwriteGuard (M3): -o refuses to clobber an existing, unrelated
// file unless --overwrite is given; the input-as-output case is exempt.
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
	// The existing file is untouched (refused before any write).
	if b, _ := os.ReadFile(existing); string(b) != "keep me" {
		t.Errorf("refused -o target was modified: %q", b)
	}

	_, _, code = runCLI(t, "set", in, "--set", "TITLE=X", "-o", existing, "--overwrite")
	if code != 0 {
		t.Errorf("overwrite with --overwrite: code %d, want 0", code)
	}

	// Output == input is effectively in-place: allowed without --overwrite.
	_, _, code = runCLI(t, "set", in, "--set", "TITLE=Y", "-o", in)
	if code != 0 {
		t.Errorf("set f -o f (same file): code %d, want 0", code)
	}

	// A dangling symlink at the target is still an existing entry the atomic rename
	// would destroy, so it must be refused too - os.Stat follows the link and would
	// miss it, so the guard uses Lstat.
	dir := t.TempDir()
	dangling := filepath.Join(dir, "dangling.flac")
	if err := os.Symlink(filepath.Join(dir, "no-such-target"), dangling); err != nil {
		t.Logf("skipping dangling-symlink case (symlinks unsupported here): %v", err)
	} else {
		_, _, code = runCLI(t, "set", in, "--set", "TITLE=X", "-o", dangling)
		if code != 2 {
			t.Errorf("dangling -o symlink should be refused without --overwrite: code %d, want 2", code)
		}
		// The symlink is untouched (refused before any write).
		if fi, err := os.Lstat(dangling); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("dangling -o symlink was clobbered despite refusal (mode %v, err %v)", fi.Mode(), err)
		}
		// With --overwrite it is replaced.
		if _, _, code = runCLI(t, "set", in, "--set", "TITLE=X", "-o", dangling, "--overwrite"); code != 0 {
			t.Errorf("dangling -o symlink with --overwrite: code %d, want 0", code)
		}
	}

	// A missing input plus an existing -o target reports the input's not-found
	// (exit 6), not "already exists" - the parse fails and writes nothing, so the
	// target is safe and the more-relevant error surfaces (#4).
	missing := filepath.Join(t.TempDir(), "missing.flac")
	if _, _, code = runCLI(t, "set", missing, "--set", "TITLE=X", "-o", existing); code != 6 {
		t.Errorf("missing input + existing -o: code %d, want 6 (not-found), not 2", code)
	}

	// A directory -o target is rejected with a clear message, even with --overwrite
	// (the rename could never succeed) (#5).
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

// TestSetOutputNoOpVerbatim (L1): -o on an unchanged file prints one honest line,
// not a "no changes" preview followed by a contradictory "Wrote" line.
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
	// The contradictory preview is gone.
	if strings.Contains(stdout, "no changes (already up to date)") || strings.Contains(stdout, ": plan") {
		t.Errorf("no-op -o should not print the change preview; got:\n%q", stdout)
	}
}

// TestStrictBeforeNotFound (L7): a strict-key misuse is checked upfront, so it
// stays exit 2 even when the file is missing; the non-strict note waits for a real
// file, so a missing file is reported as not-found without a premature key lecture.
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

// TestHelpTopicExitCode (L9): an unknown help topic exits non-zero like an unknown
// command; a valid topic and bare help exit 0. A valid command followed by a stray
// token that resolves to nothing (e.g. "help set bogus") is rejected too, but a
// flag after a valid command (stripped before RunE) is not.
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

// TestBareInvocationExitsUsage (#24, B3): a bare `waxlabel` with no subcommand is a
// usage error (exit 2) so a script can tell "no command" from success, with the
// help printed to stderr - while --help/-h stay exit 0 with help on stdout, and a
// --json bare run still gets the machine-readable error envelope.
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

// TestMultiFileExitMostSevere (#14, B1): in a multi-file run the exit code is the
// most-severe failure's class, not the first file's - and is independent of
// argument order. A corrupt file (invalid-data, exit 4) outranks a missing path
// (not-found, exit 6), where first-error capture would yield 4 one way and 6 the
// other.
func TestMultiFileExitMostSevere(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A file the FLAC detector claims by its "fLaC" magic but cannot parse: invalid-data.
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

// TestAddCoverNonRegularIsUsageError (#22, B2): an --add-cover pointed at a
// directory (or other non-regular file) is a usage error (exit 2), consistent with
// every other non-regular input, rather than the exit-6 io error os.ReadFile would
// raise. A genuinely missing cover still falls through to the read and stays io.
func TestAddCoverNonRegularIsUsageError(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	coverDir := t.TempDir() // a directory, not an image
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

// TestPlanJSONErrorEntryMinimal (#4, C1): a per-file error element is exactly
// {schemaVersion,file,error} - no null "operations" array and none of the other
// zeroed plan fields leaking through.
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
	errEl := raw[1] // the missing file's element
	if _, ok := errEl["error"]; !ok {
		t.Fatalf("second element should carry an error: %v", errEl)
	}
	if len(errEl) != 3 {
		t.Errorf("error element has %d keys, want exactly schemaVersion/file/error: %v", len(errEl), errEl)
	}
	for _, forbidden := range []string{"operations", "noOp", "changes", "bytesBefore", "bytesAfter", "paddingAfter"} {
		if _, present := errEl[forbidden]; present {
			t.Errorf("error element leaks %q (#4); want only schemaVersion/file/error", forbidden)
		}
	}
}

// TestSetJSONErrorNoPhantomOutput (#15, C1): a failed set's per-file error element
// does not echo the (unwritten) output path or carry committed/size, and the output
// file is never created.
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
			t.Errorf("set error element leaks %q (#15)", forbidden)
		}
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("the output file must not be created on a failed set")
	}
}

// TestStrictGuardrailShapes pins how the two --strict guardrails surface (#5, B1).
// The file-independent unknown-key guardrail aborts up front as the single object
// envelope. The per-file single-valued-multi guardrail is a per-file array element,
// so it participates in the most-severe-wins aggregate exit code rather than aborting
// the run: pairing it with a missing file yields exit 6 (not-found outranks the usage
// error) with BOTH elements present, independent of argument order. (An earlier
// abort-on-first design discarded the not-found element and flipped the exit to 2 -
// order-dependently - which this guards against.)
func TestStrictGuardrailShapes(t *testing.T) {
	t.Parallel()

	// Unknown key: invocation-level, one object envelope, exit 2.
	t.Run("unknown-key-is-object", func(t *testing.T) {
		out, _, code := runCLI(t, "--json", "plan", sampleFLAC, "--strict", "--set", "BOGUS=1")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		var je jsonError
		if err := json.Unmarshal([]byte(out), &je); err != nil {
			t.Fatalf("stdout is not the object envelope: %v\n%s", err, out)
		}
		if je.Error.Code != "usage" {
			t.Errorf("error code = %q, want usage", je.Error.Code)
		}
	})

	// Single-valued-multi on a lone file: a one-element array, exit 2.
	t.Run("single-valued-is-array-element", func(t *testing.T) {
		out, _, code := runCLI(t, "--json", "plan", sampleFLAC, "--strict", "--add", "ENCODER=a", "--add", "ENCODER=b")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		jr := decodeJSONOne[jsonReport](t, out) // also asserts a single-element array
		if jr.Error == nil || jr.Error.Code != "usage" {
			t.Errorf("error = %+v, want a usage element", jr.Error)
		}
	})

	// Multi-file: the strict element does not discard a more-severe not-found, and the
	// aggregate exit is order-independent.
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

// TestDiffQuietJSONEmitsObject (#6, C3): --json overrides --quiet, so a quiet JSON
// diff still emits the documented object (the exit code carries the verdict either
// way), while a plain --quiet diff still prints nothing.
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

// TestRecursiveWalkFollowsSymlinkedAudio (A1): the no-hang hardening must not break
// the documented "symlinks are followed" behavior - a recursive walk still picks up
// a symlink that points at a real audio file (resolved via os.Stat), even though
// filepath.WalkDir does not follow symlinks itself.
func TestRecursiveWalkFollowsSymlinkedAudio(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target.flac") // the real file, outside the walked dir
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

// TestRecursiveWalkReportsDanglingSymlink (A1): a dangling symlink with an audio
// extension is surfaced as a per-file not-found by a recursive walk, not silently
// dropped - so a library scan does not read "clean" over a broken link. (The
// non-regular skip applies to FIFOs/sockets, which can wedge a parse; a dangling
// link cannot - os.Stat fails fast - so it is passed through to be reported.)
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

// TestNonExpandingCommandsRejectNonRegular (#3): caps, diff, and copy parse their
// operands directly (no directory expansion), but still reject a non-regular input -
// here a directory - as an exit-2 usage error, matching dump/verify/plan/set/lint
// rather than falling through to the library's exit-4 backstop.
func TestNonExpandingCommandsRejectNonRegular(t *testing.T) {
	t.Parallel()
	d := t.TempDir() // a directory, not a file
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
