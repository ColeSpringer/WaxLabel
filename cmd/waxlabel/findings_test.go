package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnknownCommandSuggestionNotMangled: cobra's multi-line "Did you mean
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

// TestUsageHintOnDeadEnds: a cobra dead-end with no built-in guidance - an
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

// TestRemovedWritePoliciesRejected verifies that removed write-policy names stay
// outside the CLI surface. They are rejected as unknown flag values (exit 2)
// listing only the supported options, while every supported value still parses.
func TestRemovedWritePoliciesRejected(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)

	// The removed legacy policies are now unknown values: a usage error (exit 2)
	// naming the survivors, not a stub write-time failure.
	for _, v := range []string{"reconcile", "update-existing"} {
		_, stderr, code := runCLI(t, "set", file, "--set", "TITLE=x", "--legacy", v)
		if code != 2 {
			t.Errorf("--legacy %s: exit = %d, want 2", v, code)
		}
		if !strings.Contains(stderr, "unknown legacy policy") || !strings.Contains(stderr, "preserve|strip") {
			t.Errorf("--legacy %s: stderr = %q, want unknown-legacy-policy naming preserve|strip", v, stderr)
		}
	}

	// The removed canonical preset is likewise an unknown value.
	if _, stderr, code := runCLI(t, "set", file, "--set", "TITLE=x", "--preset", "canonical"); code != 2 ||
		!strings.Contains(stderr, "unknown preset") || !strings.Contains(stderr, "preserve|compatible|minimal") {
		t.Errorf("--preset canonical: exit = %d, stderr = %q, want exit 2 unknown-preset naming preserve|compatible|minimal", code, stderr)
	}

	// Every surviving value still resolves (plan previews without a usage error).
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

// TestCopyNotFoundMatchesOtherCommands: copy's per-input parse failure reads
// as the "waxlabel: <path>: <reason>" line dump/verify/set print, and the --json
// not-found message uses the same "<path>: no such file or directory" phrasing, so
// the human and machine forms agree across every command.
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

	// JSON path is the machine contract: the not-found code plus the unified message.
	stdout, _, code := runCLI(t, "copy", missing, dst, "--json")
	if code != 6 || !strings.Contains(stdout, `"not-found"`) || !strings.Contains(stdout, want) {
		t.Errorf("copy --json not-found = %q (code %d); want not-found envelope with %q", stdout, code, want)
	}
}

// TestSetOutputOverwriteGuard: -o refuses to clobber an existing, unrelated
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
	// target is safe and the more-relevant error surfaces.
	missing := filepath.Join(t.TempDir(), "missing.flac")
	if _, _, code = runCLI(t, "set", missing, "--set", "TITLE=X", "-o", existing); code != 6 {
		t.Errorf("missing input + existing -o: code %d, want 6 (not-found), not 2", code)
	}

	// A directory -o target is rejected with a clear message, even with --overwrite
	// (the rename could never succeed).
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

// TestSetOutputNoOpVerbatim: -o on an unchanged file prints one honest line,
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

// TestStrictBeforeNotFound: a strict-key misuse is checked upfront, so it
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

// TestHelpTopicExitCode: an unknown help topic exits non-zero like an unknown
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

// TestBareInvocationExitsUsage: a bare `waxlabel` with no subcommand is a
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
	// An explicit failure line follows the help so the non-zero exit is obvious in a
	// log that captured stderr.
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

// TestMultiFileExitMostSevere: in a multi-file run the exit code is the
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

// TestAddCoverNonRegularIsUsageError: an --add-cover pointed at a
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

// TestPlanJSONErrorEntryMinimal: a per-file error element is exactly
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
			t.Errorf("error element leaks %q; want only schemaVersion/file/error", forbidden)
		}
	}
}

// TestSetJSONErrorNoPhantomOutput: a failed set's per-file error element
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
			t.Errorf("set error element leaks %q", forbidden)
		}
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("the output file must not be created on a failed set")
	}
}

// TestPreflightErrorEnvelopeShape pins that a list command wraps a pre-flight
// failure (missing args, a directory without --recursive, a bad flag) in the same
// one-element JSON array its successful --json output uses, so `jq '.[]'` works no
// matter how the run ends; a non-list command (diff/copy/keys), caps --format (a
// format query), and an unknown command keep the bare object envelope.
func TestPreflightErrorEnvelopeShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	for _, args := range [][]string{
		{"--json", "dump"},      // missing args
		{"--json", "dump", dir}, // directory without --recursive
		{"--json", "lint"},      // missing args
		{"--json", "verify"},    // missing args
		{"--json", "caps", dir}, // caps over files, directory rejected
	} {
		t.Run("array_"+strings.Join(args[1:], "_"), func(t *testing.T) {
			out, _, code := runCLI(t, args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if je := decodeJSONOne[jsonError](t, out); je.Error.Code != "usage" { // asserts a one-element array
				t.Errorf("error code = %q, want usage", je.Error.Code)
			}
		})
	}

	for _, args := range [][]string{
		{"--json", "diff", "a", "b", "c"},       // diff takes exactly two
		{"--json", "keys", "extra"},             // keys takes no args
		{"--json", "copy", "onlyone"},           // copy takes two
		{"--json", "caps", "--format", "bogus"}, // caps --format is a format query
		{"--json", "frobnicate"},                // unknown command
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

// TestStrictGuardrailShapes pins how the two --strict guardrails surface.
// The file-independent unknown-key guardrail aborts up front: a single pre-flight
// error, now wrapped in plan's documented one-element array like every other list-
// command pre-flight failure. The per-file single-valued-multi guardrail is a
// per-file array element, so it participates in the most-severe-wins aggregate exit
// code rather than aborting the run: pairing it with a missing file yields exit 6
// (not-found outranks the usage error) with both elements present, independent of
// argument order. (An earlier abort-on-first design discarded the not-found element
// and flipped the exit to 2 - order-dependently - which this guards against.) The
// abort-vs-per-file distinction now lives in the element count and aggregate exit,
// not in array-vs-object.
func TestStrictGuardrailShapes(t *testing.T) {
	t.Parallel()

	// Unknown key: invocation-level abort, a single-element array, exit 2.
	t.Run("unknown-key-aborts-as-one-element", func(t *testing.T) {
		out, _, code := runCLI(t, "--json", "plan", sampleFLAC, "--strict", "--set", "BOGUS=1")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		je := decodeJSONOne[jsonError](t, out) // asserts a single-element array
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

// TestDiffQuietJSONEmitsObject: --json overrides --quiet, so a quiet JSON
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

// TestRecursiveWalkFollowsSymlinkedAudio: the no-hang hardening must not break
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

// TestRecursiveWalkThroughSymlinkedDirRoot verifies that a symlink-to-directory
// used as the --recursive root is followed and its audio found. WalkDir lstats its
// root and would refuse to descend a symlink node, so walkAudioFiles resolves the
// named root; the matches are then listed under the original argument name, not the
// link's resolved target.
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
	// The audio is found and listed under the original arg name (linkdir), not the
	// resolved target (realdir).
	if !strings.Contains(out, filepath.Join("linkdir", "inside.flac")) {
		t.Errorf("walk did not list the audio under the original arg name 'linkdir':\n%s", out)
	}
	if strings.Contains(out, filepath.Join("realdir", "inside.flac")) {
		t.Errorf("walk leaked the resolved target path 'realdir' instead of the user's arg:\n%s", out)
	}
}

// TestRecursiveWalkReportsDanglingSymlink: a dangling symlink with an audio
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

// TestNonExpandingCommandsRejectNonRegular: caps, diff, and copy parse their
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

// TestCopyRejectsStdin: copy has no streaming model, so "-" as either operand
// is a usage error (exit 2), not an attempt to open a file literally named "-".
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

// TestReadCommandsRejectRepeatedStdin checks that read commands accept at most one
// "-". A second one would replay the buffered stdin bytes as a duplicate input, so it
// is a usage error.
func TestReadCommandsRejectRepeatedStdin(t *testing.T) {
	for _, cmd := range []string{"dump", "verify", "lint", "plan"} {
		_, stderr, code := runCLI(t, cmd, "-", "-")
		if code != 2 || !strings.Contains(stderr, "standard input") {
			t.Errorf("%s - -: code %d, stderr %q; want exit 2 mentioning standard input", cmd, code, stderr)
		}
	}
}

// TestRejectEmptyScalarFlags: an explicitly-empty --preset/--legacy/--padding is
// a usage error on both set and plan, matching the unknown-value rejection rather than
// being silently treated as unset (and keeping the scalar write-shaping flags
// consistent).
func TestRejectEmptyScalarFlags(t *testing.T) {
	file := copyFixture(t, sampleFLAC)
	for _, flag := range []string{"--preset", "--legacy", "--padding"} {
		_, stderr, code := runCLI(t, "set", file, "--set", "TITLE=X", flag, "")
		if code != 2 || !strings.Contains(stderr, "cannot be empty") {
			t.Errorf("set %s '': code %d, stderr %q; want exit 2 'cannot be empty'", flag, code, stderr)
		}
		if _, _, code := runCLI(t, "plan", file, flag, ""); code != 2 {
			t.Errorf("plan %s '': code %d, want exit 2", flag, code)
		}
	}
}

// TestCapsNoArgsHasHint: `caps` with neither a file nor --format dead-ends with
// the same "run '... --help' for usage" pointer the other commands print.
func TestCapsNoArgsHasHint(t *testing.T) {
	_, stderr, code := runCLI(t, "caps")
	if code != 2 {
		t.Fatalf("caps no-args exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "--help") {
		t.Errorf("caps no-args stderr = %q, want the --help hint", stderr)
	}
}

// TestEmptyWalkNoteNotAFailure: a --recursive walk that matches no audio files
// prints a "note:" line (not a "waxlabel:" failure line) and still exits 0.
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

// TestRecursiveSkippedFileNote verifies that a recursive walk that passes over files
// with unrecognized extensions prints a text-mode "N file(s) skipped" note, so a
// directory of mostly non-audio files is not a silent near-no-op. The note counts only
// regular files the extension filter rejected and is suppressed under --json.
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
	for _, name := range []string{"cover.jpg", "notes.txt"} { // two files the filter rejects
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

	// --json suppresses the note (stdout has a fixed shape; stderr stays clean of it).
	if _, jerrb, _ := runCLI(t, "--json", "dump", "--recursive", dir); strings.Contains(jerrb, "skipped") {
		t.Errorf("--json should suppress the skipped-file note; stderr:\n%s", jerrb)
	}
}

// TestRecursiveSkippedCountsSymlinks verifies that a symlinked non-audio file counts
// toward the skipped tally too, matching how the inclusion side treats symlinks as
// candidates so the count is not short by the symlinked entries.
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
	target := filepath.Join(dir, "real.jpg") // a regular non-audio file
	if err := os.WriteFile(target, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.png")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	// real.jpg (regular) and link.png (symlink) are both non-audio -> 2 skipped.
	_, errb, code := runCLI(t, "dump", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "note: 2 file(s) skipped") {
		t.Errorf("a symlinked non-audio file should count toward skipped (want 2):\n%s", errb)
	}
}

// TestSetVerifyConfirmation: a committed --verify save confirms the essence
// check - a human "Audio essence verified" line and a JSON "verified": true - while
// a run without --verify omits the field so a normal save does not read like a check.
func TestSetVerifyConfirmation(t *testing.T) {
	out, _, code := runCLI(t, "set", copyFixture(t, sampleFLAC), "--set", "TITLE=Verified", "--verify")
	if code != 0 {
		t.Fatalf("set --verify exit = %d, want 0", code)
	}
	if !strings.Contains(out, "Audio essence verified") {
		t.Errorf("human output missing the verified confirmation:\n%s", out)
	}

	jout, _, code := runCLI(t, "--json", "set", copyFixture(t, sampleFLAC), "--set", "TITLE=Y", "--verify")
	if code != 0 {
		t.Fatalf("set --json --verify exit = %d, want 0", code)
	}
	if !strings.Contains(jout, `"verified": true`) {
		t.Errorf("JSON output missing verified:true:\n%s", jout)
	}

	// A normal save (no --verify) omits the field entirely - never "verified": false.
	jplain, _, _ := runCLI(t, "--json", "set", copyFixture(t, sampleFLAC), "--set", "TITLE=Z")
	if strings.Contains(jplain, "verified") {
		t.Errorf("a non-verify save should not mention verified:\n%s", jplain)
	}
}

// TestUnquotedValueHint: an unquoted value with spaces (--set TITLE=Two
// Words) leaves a stray bare-word positional beside a real input. The writing set
// command refuses the whole run up front (exit 2, nothing written) so a script cannot
// misread a truncated tag as a partial success; the read-only plan keeps the same
// text as an advisory and previews as usual.
func TestUnquotedValueHint(t *testing.T) {
	file := copyFixture(t, sampleFLAC)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// The shell would split "Two Words" into "--set TITLE=Two" plus a stray "Words".
	_, stderr, code := runCLI(t, "set", file, "--set", "TITLE=Two", "Words")
	if code != 2 {
		t.Fatalf("unquoted-value set exit = %d, want 2 (refused before writing)", code)
	}
	if !strings.Contains(stderr, "must be quoted") || !strings.Contains(stderr, "nothing was written") {
		t.Errorf("stderr should carry the quoting hint and say nothing was written; got:\n%s", stderr)
	}
	// The refusal is up front: the named file is byte-for-byte unchanged.
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Error("set refused the unquoted-value run but still modified the named file")
	}

	// plan now refuses identically to set (its preview is meant to be authoritative),
	// but with the bare hint and no "nothing was written" suffix (plan never writes).
	if _, pstderr, pcode := runCLI(t, "plan", file, "--set", "TITLE=Two", "Words"); pcode != 2 {
		t.Fatalf("plan stray bare word exit = %d, want 2 (refused); stderr=%s", pcode, pstderr)
	} else if !strings.Contains(pstderr, "must be quoted") {
		t.Errorf("plan should carry the quoting hint; got:\n%s", pstderr)
	} else if strings.Contains(pstderr, "nothing was written") {
		t.Errorf("plan never writes, so it must not claim 'nothing was written'; got:\n%s", pstderr)
	}

	// No false positive: two real files with no stray bare word are not refused.
	_, stderr, code = runCLI(t, "set", file, copyFixture(t, sampleFLAC), "--set", "TITLE=One")
	if code != 0 {
		t.Fatalf("two real files exit = %d, want 0", code)
	}
	if strings.Contains(stderr, "must be quoted") {
		t.Errorf("no bare word, so no quoting hint expected; got:\n%s", stderr)
	}

	// No false positive: a legitimate extensionless audio file (which looks like a bare
	// word) given on its own resolves, so it is edited, not refused.
	extless := filepath.Join(t.TempDir(), "song")
	if err := os.WriteFile(extless, before, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, estderr, ecode := runCLI(t, "set", extless, "--set", "TITLE=X"); ecode != 0 {
		t.Fatalf("legitimate extensionless file exit = %d, want 0 (edited, not refused); stderr=%s", ecode, estderr)
	}
}

// TestEmptyFilenameUsage verifies that an empty operand is a usage error (exit 2) at
// the CLI boundary, not the library's ErrInvalidData (exit 4), so in a multi-file run
// it does not outrank a real not-found by masquerading as a corrupt file.
func TestEmptyFilenameUsage(t *testing.T) {
	t.Parallel()
	if _, _, code := runCLI(t, "dump", ""); code != 2 {
		t.Errorf(`dump "" exit = %d, want 2 (usage)`, code)
	}
	// Beside a missing file the empty operand is still caught up front (exit 2) and
	// never classifies as invalid-data (exit 4) over the not-found.
	out, _, code := runCLI(t, "--json", "dump", "", "missing.flac")
	if code != 2 {
		t.Errorf(`dump "" missing.flac exit = %d, want 2`, code)
	}
	if strings.Contains(out, "invalid-data") {
		t.Errorf("empty filename must not classify as invalid-data:\n%s", out)
	}
	// copy, diff, and caps parse operands directly (no expandPaths) but reject an empty
	// operand the same way via their own boundary checks - caps included, so a multi-file
	// caps run does not mis-class an empty name as invalid-data (exit 4).
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

// TestDiffPerFilePathPrefix verifies that a parse failure in diff is reported with
// the per-file "waxlabel: <path>: <reason>" prefix the other commands print, so the
// failing operand is named.
func TestDiffPerFilePathPrefix(t *testing.T) {
	t.Parallel()
	png := writeTempImage(t, "red.png", minimalPNG()) // a non-audio file diff cannot parse
	_, errb, code := runCLI(t, "diff", png, sampleFLAC)
	if code < 2 {
		t.Fatalf("diff of a non-audio file exit = %d, want a real error (>= 2); stderr=%s", code, errb)
	}
	if !strings.Contains(errb, "red.png:") {
		t.Errorf("diff parse error should carry the 'red.png:' per-file prefix:\n%s", errb)
	}
}

// TestJSONErrorCarriesHint verifies that a usage error whose human render shows a
// hint, such as the leading-dash "use --" pointer, carries that same hint in the JSON
// envelope. A per-file error entry can carry a hint too, such as source-changed's
// "re-run" pointer.
func TestJSONErrorCarriesHint(t *testing.T) {
	t.Parallel()
	// A leading-dash file path is read by cobra as an unknown flag; the usage envelope
	// then carries the "put -- before it" hint - now in JSON, not only the human line.
	out, _, code := runCLI(t, "--json", "dump", "-track.flac")
	if code != 2 {
		t.Fatalf("leading-dash arg exit = %d, want 2; out=%s", code, out)
	}
	// dump is a list command, so its pre-flight error is a single-element array,
	// and the hint rides on that element.
	je := decodeJSONOne[jsonError](t, out)
	if !strings.Contains(je.Error.Hint, "--") {
		t.Errorf("JSON usage envelope hint missing the '--' guidance; got %q\n%s", je.Error.Hint, out)
	}
	// The shared per-file error element single-sources the same hint from the classified
	// error, so a bulk-run entry can carry one too.
	entry := errorEntry("f.flac", classifiedError{code: "source-changed", message: "x", hint: "re-run to pick up the new contents"})
	if entry.Error.Hint != "re-run to pick up the new contents" {
		t.Errorf("per-file error entry dropped the hint: %q", entry.Error.Hint)
	}
}

// TestPlanJSONEmptyChangesArray verifies that a no-op plan's --json output emits
// "changes": [] (a non-null empty array), not an omitted field, so a scripting
// consumer can iterate changes unconditionally.
func TestPlanJSONEmptyChangesArray(t *testing.T) {
	t.Parallel()
	// plan with no edits previews a no-op: no field changes.
	out, _, code := runCLI(t, "--json", "plan", sampleFLAC)
	if code != 0 {
		t.Fatalf("no-op plan --json exit = %d, want 0", code)
	}
	if !strings.Contains(out, `"changes": []`) {
		t.Errorf(`no-op plan --json should emit "changes": [], got:\n%s`, out)
	}
}

// TestVorbisAliasCanonicalized verifies that a recognized alias (DATE, YEAR, TOTALTRACKS, ...) is
// resolved to its canonical key, so editing one targets the real field - replacing the
// value rather than appending a stray duplicate - is not flagged as a custom field, and
// is accepted under --strict. A genuinely unknown key is still flagged and rejected.
func TestVorbisAliasCanonicalized(t *testing.T) {
	// DATE replaces the existing RECORDINGDATE rather than creating a second value.
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

	// Under --strict, DATE is accepted (it resolves to a known key, so the guardrail does
	// not reject it) and is stored as RECORDINGDATE even on a non-Vorbis format.
	mp3 := copyFixture(t, sampleMP3)
	if _, _, code := runCLI(t, "set", "--strict", mp3, "--set", "DATE=2021"); code != 0 {
		t.Errorf("--strict --set DATE on MP3 exit = %d, want 0 (DATE resolves to a known key)", code)
	}
	if out, _, _ := runCLI(t, "dump", mp3); !strings.Contains(out, "RECORDINGDATE  2021") {
		t.Errorf("DATE on MP3 should be stored as RECORDINGDATE:\n%s", out)
	}

	// A genuinely unknown key is still flagged, and rejected under --strict.
	if _, stderr, code := runCLI(t, "set", copyFixture(t, sampleFLAC), "--set", "BOGUSKEY=x"); code != 0 || !strings.Contains(stderr, "custom field") {
		t.Errorf("unknown key: exit %d, want 0 with a custom-field note; stderr:\n%s", code, stderr)
	}
	if _, _, code := runCLI(t, "set", "--strict", copyFixture(t, sampleFLAC), "--set", "BOGUSKEY=x"); code != 2 {
		t.Errorf("--strict unknown key exit = %d, want 2 (rejected)", code)
	}
}

// TestJSONVersionFlag verifies that `--json --version` emits the JSON version object,
// not cobra's plain text template; plain `--version` stays text. Both exit 0.
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
