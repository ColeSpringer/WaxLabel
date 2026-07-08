package main

import (
	"strings"
	"testing"
)

// TestCompletionUnknownShellExits2 checks that an unknown shell name is a usage error (exit 2),
// matching every other unknown-topic path (help, version, unknown command) - not cobra's
// default non-runnable-parent behavior, which skipped arg validation and exited 0 on a typo.
func TestCompletionUnknownShellExits2(t *testing.T) {
	if _, _, code := runCLI(t, "completion", "zzz"); code != 2 {
		t.Errorf("completion zzz exit = %d, want 2 (usage)", code)
	}
	// An extra argument after a valid shell is also rejected (each subcommand is NoArgs).
	if _, _, code := runCLI(t, "completion", "bash", "extra"); code != 2 {
		t.Errorf("completion bash extra exit = %d, want 2 (usage)", code)
	}
}

// TestCompletionBareExits0 checks the runnable parent: a bare "completion" prints help and
// exits 0 rather than erroring.
func TestCompletionBareExits0(t *testing.T) {
	if _, _, code := runCLI(t, "completion"); code != 0 {
		t.Errorf("bare completion exit = %d, want 0", code)
	}
}

// TestCompletionShellsGenerate checks each supported shell generates a script at exit 0, and
// that the bash script has a non-empty body (proving the generator actually ran and wrote to
// the redirected output the harness captures).
func TestCompletionShellsGenerate(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		out, _, code := runCLI(t, "completion", shell)
		if code != 0 {
			t.Errorf("completion %s exit = %d, want 0", shell, code)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("completion %s produced an empty script", shell)
		}
	}
	// The bash script names the program, confirming it is a real completion body and not an
	// unrelated help dump.
	out, _, _ := runCLI(t, "completion", "bash")
	if !strings.Contains(out, "waxlabel") {
		t.Errorf("bash completion body does not mention waxlabel:\n%s", out)
	}
}
