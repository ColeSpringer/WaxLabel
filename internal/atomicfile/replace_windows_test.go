//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRetryableRenameError: only transient handle failures retry.
func TestRetryableRenameError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"ERROR_ACCESS_DENIED", syscall.ERROR_ACCESS_DENIED, true},
		{"ERROR_SHARING_VIOLATION", errnoSharingViolation, true},
		{"ERROR_FILE_NOT_FOUND", syscall.ERROR_FILE_NOT_FOUND, false},
		{"ERROR_UNABLE_TO_REMOVE_REPLACED", errUnableToRemoveReplaced, true},
		{"ERROR_UNABLE_TO_MOVE_REPLACEMENT", errUnableToMoveReplacement, true},
		// os.Rename wraps in a *os.LinkError.
		{"wrapped ERROR_ACCESS_DENIED", &os.LinkError{Err: syscall.ERROR_ACCESS_DENIED}, true},
		{"wrapped ERROR_FILE_NOT_FOUND", &os.LinkError{Err: syscall.ERROR_FILE_NOT_FOUND}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableRenameError(tc.err); got != tc.want {
				t.Errorf("retryableRenameError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestReplaceSurfacesRealFailures: non-retryable rename still errors.
func TestReplaceSurfacesRealFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Replace(filepath.Join(dir, "missing.tmp"), filepath.Join(dir, "out"), false); err == nil {
		t.Error("Replace of a missing source = nil, want an error")
	}
}

// TestClearTargetReadOnlyRoundTrips: drop readonly for rename, restore after;
// writable/missing targets untouched.
func TestClearTargetReadOnlyRoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	ro := filepath.Join(dir, "ro.flac")
	if err := os.WriteFile(ro, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o666) })

	restore := clearTargetReadOnly(ro, mustStat(t, ro))
	if perm := mustPerm(t, ro); perm&0o200 == 0 {
		t.Errorf("target still read-only after clear: mode = %o", perm)
	}
	restore()
	if perm := mustPerm(t, ro); perm&0o200 != 0 {
		t.Errorf("restore did not put the attribute back: mode = %o", perm)
	}

	// Writable left alone; missing must not panic.
	rw := filepath.Join(dir, "rw.flac")
	if err := os.WriteFile(rw, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	clearTargetReadOnly(rw, mustStat(t, rw))()
	if perm := mustPerm(t, rw); perm&0o200 == 0 {
		t.Errorf("a writable target was made read-only: mode = %o", perm)
	}
	clearTargetReadOnly(filepath.Join(dir, "gone.flac"), nil)() // Write passes nil for a new target
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func mustPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	return mustStat(t, path).Mode().Perm()
}

// TestReplaceSwapsExistingTargets: an existing target goes through ReplaceFileW and leaves no
// backup behind; a new target is a plain MoveFileEx rename.
func TestReplaceSwapsExistingTargets(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	replaceCall = func(api string) { calls = append(calls, api) }
	t.Cleanup(func() { replaceCall = func(string) {} })
	put := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	target := put("out", "old")
	if err := Replace(put(TempPrefix+"a"+TempSuffix, "new"), target, false); err != nil {
		t.Fatal(err)
	}
	assertContent(t, target, "new")
	if len(calls) != 1 || calls[0] != "ReplaceFileW" {
		t.Errorf("existing target: calls = %v, want [ReplaceFileW]", calls)
	}
	assertNoTemps(t, dir)

	calls = nil
	fresh := filepath.Join(dir, "fresh")
	if err := Replace(put(TempPrefix+"b"+TempSuffix, "first"), fresh, false); err != nil {
		t.Fatal(err)
	}
	assertContent(t, fresh, "first")
	if len(calls) != 1 || calls[0] != "MoveFileEx" {
		t.Errorf("new target: calls = %v, want [MoveFileEx]", calls)
	}
}

// explicitACEs lists path's non-inherited ACEs as icacls prints them.
func explicitACEs(t *testing.T, icacls, path string) []string {
	t.Helper()
	out, err := exec.Command(icacls, path).CombinedOutput()
	if err != nil {
		t.Fatalf("icacls %s: %v\n%s", path, err, out)
	}
	var aces []string
	for line := range strings.Lines(string(out)) {
		entry := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), path))
		if strings.Contains(entry, ":(") && !strings.Contains(entry, "(I)") {
			aces = append(aces, entry)
		}
	}
	return aces
}

// TestReplaceKeepsExplicitACE: a permission granted on the replaced file itself survives the swap.
func TestReplaceKeepsExplicitACE(t *testing.T) {
	icacls, err := exec.LookPath("icacls")
	if err != nil {
		t.Skip("icacls not available")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "out")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// S-1-1-0 is Everyone; the SID form does not depend on the system language.
	if out, err := exec.Command(icacls, target, "/grant", "*S-1-1-0:(R)").CombinedOutput(); err != nil {
		t.Fatalf("icacls /grant: %v\n%s", err, out)
	}
	want := explicitACEs(t, icacls, target)
	if len(want) == 0 {
		t.Fatal("setup: the grant left no explicit ACE")
	}
	tmp := filepath.Join(dir, TempPrefix+"c"+TempSuffix)
	if err := os.WriteFile(tmp, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Replace(tmp, target, false); err != nil {
		t.Fatal(err)
	}
	if got := explicitACEs(t, icacls, target); !slices.Equal(got, want) {
		t.Errorf("explicit ACEs after Replace = %v, want %v", got, want)
	}
}

func putFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeReplaceFileW installs fn as the swap for the rest of the test.
func fakeReplaceFileW(t *testing.T, fn func(replaced, replacement, backup string) error) {
	replaceFileW = fn
	t.Cleanup(func() { replaceFileW = callReplaceFile })
}

// TestReplaceRestoresAfterAFailedSwap: when ReplaceFileW has moved the target aside but cannot
// finish (1177), the target is put back and the swap retried.
func TestReplaceRestoresAfterAFailedSwap(t *testing.T) {
	dir := t.TempDir()
	target := putFile(t, dir, "out", "old")
	tmp := putFile(t, dir, TempPrefix+"r"+TempSuffix, "new")
	calls := 0
	fakeReplaceFileW(t, func(replaced, replacement, backup string) error {
		if calls++; calls == 1 {
			if err := os.Rename(replaced, backup); err != nil {
				t.Fatal(err)
			}
			return errUnableToMoveReplacement2
		}
		return callReplaceFile(replaced, replacement, backup)
	})
	if err := Replace(tmp, target, false); err != nil {
		t.Fatal(err)
	}
	assertContent(t, target, "new")
	if calls != 2 {
		t.Errorf("swap calls = %d, want 2", calls)
	}
	assertNoTemps(t, dir)
}

// TestReplaceStopsWhenTheRestoreFails: if the original cannot be put back, nothing retries over
// the empty path, the error says where the original is, and that name is not one clean removes.
func TestReplaceStopsWhenTheRestoreFails(t *testing.T) {
	dir := t.TempDir()
	target := putFile(t, dir, "out", "old")
	tmp := putFile(t, dir, TempPrefix+"s"+TempSuffix, "new")
	calls := 0
	var backupName string
	fakeReplaceFileW(t, func(replaced, replacement, backup string) error {
		calls++
		backupName = backup
		if err := os.Rename(replaced, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(replaced, 0o755); err != nil { // the restore cannot rename over it
			t.Fatal(err)
		}
		return errUnableToMoveReplacement2
	})
	err := Replace(tmp, target, false)
	if err == nil || calls != 1 || !strings.Contains(err.Error(), backupName) {
		t.Fatalf("Replace = %v after %d swaps, want one terminal error naming %s", err, calls, backupName)
	}
	assertContent(t, backupName, "old")
	if base := filepath.Base(backupName); strings.HasPrefix(base, TempPrefix) && strings.HasSuffix(base, TempSuffix) {
		t.Errorf("backup %s has a temp name that clean would remove", base)
	}
}

// TestCommitRechecksBeforeEveryAttempt: each retried swap re-runs the pre-commit check first, and a
// check that fails on a retry stops the write.
func TestCommitRechecksBeforeEveryAttempt(t *testing.T) {
	dir := t.TempDir()
	target := putFile(t, dir, "out", "old")
	calls := 0
	fakeReplaceFileW(t, func(replaced, replacement, backup string) error {
		if calls++; calls%2 == 1 {
			return errnoSharingViolation
		}
		return callReplaceFile(replaced, replacement, backup)
	})

	checks := 0
	committed, _, err := Write(target, writeString("new"), Options{PreCommit: func() error { checks++; return nil }})
	if err != nil || !committed || checks != 2 {
		t.Fatalf("Write = (committed %v, %v) after %d checks, want a commit after 2", committed, err, checks)
	}
	assertContent(t, target, "new")

	changed := errors.New("source changed")
	checks = 0
	committed, _, err = Write(target, writeString("newer"), Options{PreCommit: func() error {
		if checks++; checks == 2 {
			return changed
		}
		return nil
	}})
	if committed || !errors.Is(err, changed) {
		t.Fatalf("Write = (committed %v, %v), want the retry's check failure", committed, err)
	}
	assertContent(t, target, "new")
	assertNoTemps(t, dir)
}

// TestReplaceFallsBackWhenReplaceFileIsRefused: a share that refuses ReplaceFileW outright still
// gets the save, through a plain rename.
func TestReplaceFallsBackWhenReplaceFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	target := putFile(t, dir, "out", "old")
	var calls []string
	replaceCall = func(api string) { calls = append(calls, api) }
	t.Cleanup(func() { replaceCall = func(string) {} })
	fakeReplaceFileW(t, func(string, string, string) error { return syscall.Errno(1) }) // ERROR_INVALID_FUNCTION
	if err := Replace(putFile(t, dir, TempPrefix+"f"+TempSuffix, "new"), target, false); err != nil {
		t.Fatal(err)
	}
	assertContent(t, target, "new")
	if !slices.Equal(calls, []string{"ReplaceFileW", "MoveFileEx"}) {
		t.Errorf("calls = %v, want ReplaceFileW then MoveFileEx", calls)
	}
}

// TestCommitReportsALeftoverBackup: a swap that commits but cannot remove the replaced file's old
// copy says where it is, after the commit.
func TestCommitReportsALeftoverBackup(t *testing.T) {
	dir := t.TempDir()
	target := putFile(t, dir, "out", "old")
	var hold *os.File
	t.Cleanup(func() {
		if hold != nil {
			hold.Close()
		}
	})
	fakeReplaceFileW(t, func(replaced, replacement, backup string) error {
		if err := callReplaceFile(replaced, replacement, backup); err != nil {
			return err
		}
		var err error
		hold, err = os.Open(backup) // no delete sharing: the removal fails while this is open
		return err
	})
	committed, _, err := Write(target, writeString("new"), Options{})
	if !committed || err == nil || !strings.Contains(err.Error(), ".replaced") {
		t.Fatalf("Write = (committed %v, %v), want a commit reporting the left-over copy", committed, err)
	}
	assertContent(t, target, "new")
}

// TestRefusedCommitLeavesTheReadOnlyFlag: the target's read-only attribute is cleared only for the
// move itself, so a refused commit leaves whatever file is at the path as it found it.
func TestRefusedCommitLeavesTheReadOnlyFlag(t *testing.T) {
	dir := t.TempDir()
	target := putFile(t, dir, "out", "old")
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o666) })
	changed := errors.New("source changed")
	// A competitor's writable file takes the path; the check sees it and refuses.
	check := func() error {
		if err := os.Chmod(target, 0o666); err != nil {
			return err
		}
		if err := os.Rename(putFile(t, dir, "competitor", "theirs"), target); err != nil {
			return err
		}
		return changed
	}
	if committed, _, err := Write(target, writeString("new"), Options{PreCommit: check}); committed || !errors.Is(err, changed) {
		t.Fatalf("Write = (committed %v, %v), want the check's refusal", committed, err)
	}
	assertContent(t, target, "theirs")
	if info, err := os.Stat(target); err != nil || info.Mode().Perm()&0o200 == 0 {
		t.Errorf("the competitor's file was made read-only (%v)", err)
	}
}

// TestReplaceLongPaths: targets past MAX_PATH are replaced, moved and stamped like short ones.
func TestReplaceLongPaths(t *testing.T) {
	dir := t.TempDir()
	for len(dir) < 300 {
		dir = filepath.Join(dir, strings.Repeat("d", 40))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "out")
	mt := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, o := range []Options{{NoReplace: true}, {ModTime: &mt}} {
		if _, _, err := Write(target, writeString("x"), o); err != nil {
			t.Fatalf("Write(%+v) under a long path: %v", o, err)
		}
	}
	assertContent(t, target, "x")
}
