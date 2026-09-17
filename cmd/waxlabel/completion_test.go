package main

import (
	"strings"
	"testing"
)

// TestCompletionUnknownShellExits2: unknown shell is usage error (exit 2), not cobra's silent 0.
func TestCompletionUnknownShellExits2(t *testing.T) {
	if _, _, code := runCLI(t, "completion", "zzz"); code != 2 {
		t.Errorf("completion zzz exit = %d, want 2 (usage)", code)
	}
	// Extra arg after a valid shell is also rejected (NoArgs subcommands).
	if _, _, code := runCLI(t, "completion", "bash", "extra"); code != 2 {
		t.Errorf("completion bash extra exit = %d, want 2 (usage)", code)
	}
}

// TestCompletionBareExits0: bare "completion" prints help at exit 0.
func TestCompletionBareExits0(t *testing.T) {
	if _, _, code := runCLI(t, "completion"); code != 0 {
		t.Errorf("bare completion exit = %d, want 0", code)
	}
}

// TestCompletionShellsGenerate: each shell emits a non-empty script at exit 0.
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
	// Bash body must name the program, not look like generic help.
	out, _, _ := runCLI(t, "completion", "bash")
	if !strings.Contains(out, "waxlabel") {
		t.Errorf("bash completion body does not mention waxlabel:\n%s", out)
	}
}
