package main

import (
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
