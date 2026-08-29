package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestRepeatedSetNotesTheDiscardedValue: --set K=A --set K=B silently keeps B, exactly as
// two aliased spellings of one key do, and that collision is already noted. An identical
// repeat discards nothing and stays quiet.
func TestRepeatedSetNotesTheDiscardedValue(t *testing.T) {
	t.Parallel()
	t.Run("differing", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		// Three assignments, not two: the note has to name the value that survived the
		// whole run, and it must appear once. Emitting it at each collision would name
		// whichever value was current there, which is precisely the value that lost.
		_, note, code := runCLI(t, "set", f, "--set", "TITLE=A", "--set", "TITLE=B", "--set", "TITLE=C")
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if !strings.Contains(note, `--set TITLE was given more than once; last value "C" was used`) {
			t.Errorf("want a repeated-key note naming the surviving value, got:\n%s", note)
		}
		if got := strings.Count(note, "was given more than once"); got != 1 {
			t.Errorf("repeated-key notes = %d, want exactly 1:\n%s", got, note)
		}
		// The note is advisory: the write itself is unaffected and takes the last value.
		if got := tagValues(dumpJSON(t, f), "TITLE"); len(got) != 1 || got[0] != "C" {
			t.Errorf("stored TITLE = %q, want [C] (last wins)", got)
		}
	})
	t.Run("identical", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, "set", f, "--set", "TITLE=A", "--set", "TITLE=A")
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if strings.Contains(note, "more than once") {
			t.Errorf("an identical repeat discards nothing and should stay silent, got:\n%s", note)
		}
	})
	t.Run("alias", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, "set", f, "--set", "DATE=2001", "--set", "RECORDINGDATE=2002", "--set", "DATE=2003")
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		// Two spellings need the shared field named; one spelling twice does not. The
		// surviving value and the once-only rule hold here too.
		if !strings.Contains(note, `--set DATE and --set RECORDINGDATE refer to the same field (RECORDINGDATE); last value "2003" was used`) {
			t.Errorf("the alias-collision note should name both spellings and the survivor, got:\n%s", note)
		}
		if got := strings.Count(note, "refer to the same field"); got != 1 {
			t.Errorf("alias-collision notes = %d, want exactly 1:\n%s", got, note)
		}
	})
}

// TestUnknownKeyNotesAreCapped: an unknown key is a note per key, so a bulk --set of
// thousands of them buried the plan output under thousands of stderr lines. Cap the listing
// and count the rest, the way the trailing "run 'waxlabel keys'" hint is already aggregated.
func TestUnknownKeyNotesAreCapped(t *testing.T) {
	t.Parallel()
	const n = 200
	// n unknown keys under one flag, addressed at file f.
	mk := func(f, flag, format string) []string {
		args := []string{"set", f}
		for i := range n {
			args = append(args, flag, fmt.Sprintf(format, i))
		}
		return args
	}

	t.Run("set", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, mk(f, "--set", "ZZKEY%04d=v")...)
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if got := strings.Count(note, "is not a known key;"); got != noteListCap {
			t.Errorf("per-key notes = %d, want %d", got, noteListCap)
		}
		if want := fmt.Sprintf("note: %d more unknown key(s) not listed", n-noteListCap); !strings.Contains(note, want) {
			t.Errorf("want the aggregate line %q, got:\n%s", want, note)
		}
	})

	t.Run("clear", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, mk(f, "--clear", "ZZKEY%04d")...)
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if got := strings.Count(note, "is not a known key ("); got != noteListCap {
			t.Errorf("per-key notes = %d, want %d", got, noteListCap)
		}
		if want := fmt.Sprintf("note: %d more unknown key(s) not listed", n-noteListCap); !strings.Contains(note, want) {
			t.Errorf("want the aggregate line %q, got:\n%s", want, note)
		}
	})

	t.Run("value-notes", func(t *testing.T) {
		t.Parallel()
		// The per-value advisories flood the same way the per-key ones do: an empty --set
		// value notes once per assignment. Capping only the unknown-key path would leave
		// the same run buried under 200 lines.
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, mk(f, "--set", "ZZKEY%04d=")...)
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if got := strings.Count(note, "writes an empty value"); got != noteListCap {
			t.Errorf("empty-value notes = %d, want %d", got, noteListCap)
		}
		if want := fmt.Sprintf("note: %d more note(s) not listed", n-noteListCap); !strings.Contains(note, want) {
			t.Errorf("want the aggregate line %q, got:\n%s", want, note)
		}
	})

	t.Run("strict lists every key", func(t *testing.T) {
		t.Parallel()
		// The cap is on note lines, not on the failure report. A strict run writes nothing
		// and this message is the user's only account of what to fix - and in --json it is
		// the error envelope - so truncating it would let a batch script recover ten keys
		// per run.
		f := copyFixture(t, sampleFLAC)
		_, errb, code := runCLI(t, append(mk(f, "--set", "ZZKEY%04d=v"), "--strict")...)
		if code != 2 {
			t.Fatalf("strict exit = %d, want 2\n%s", code, errb)
		}
		for _, i := range []int{0, n / 2, n - 1} {
			if key := fmt.Sprintf("ZZKEY%04d", i); !strings.Contains(errb, key) {
				t.Errorf("the strict key list omits %s:\n%s", key, errb)
			}
		}
		if strings.Contains(errb, "more)") {
			t.Errorf("the strict key list should not be truncated:\n%s", errb)
		}
	})
}
