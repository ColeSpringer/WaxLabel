package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// TestWriteFailedUsesCommittedNotError: writeFailed uses Committed, not err alone.
func TestWriteFailedUsesCommittedNotError(t *testing.T) {
	t.Parallel()
	boom := errors.New("directory fsync: no space left on device")
	for _, tc := range []struct {
		name      string
		committed bool
		err       error
		want      bool
	}{
		{"clean write", true, nil, false},
		{"no-op write", false, nil, false},
		{"write never landed", false, boom, true},
		{"committed, then a later step failed", true, boom, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := writeFailed(wl.SaveResult{Committed: tc.committed}, tc.err)
			if got != tc.want {
				t.Errorf("writeFailed(committed=%v, err=%v) = %v, want %v",
					tc.committed, tc.err, got, tc.want)
			}
		})
	}
}

// TestPostCommitWarningIsNotAnError: post-commit note on stderr or postWriteWarning in JSON, not error.
func TestPostCommitWarningIsNotAnError(t *testing.T) {
	t.Parallel()
	boom := errors.New("directory fsync: no space left on device")

	var errb bytes.Buffer
	warnPostCommit(&errb, false, "/music/a.flac", boom)
	note := errb.String()
	// Note: write done, names failed post-commit step.
	if !strings.Contains(note, "written, but") || !strings.Contains(note, "directory fsync") {
		t.Errorf("stderr note = %q, want it to report a completed write and name the failed step", note)
	}
	if !strings.Contains(note, "a.flac") {
		t.Errorf("stderr note should name the file: %q", note)
	}

	j := toJSONSetResult("/music/a.flac", "", nil, wl.SaveResult{Committed: true}, false, boom)
	if !j.Committed {
		t.Error("a post-commit failure must still report committed")
	}
	if j.PostWriteWarning != boom.Error() {
		t.Errorf("postWriteWarning = %q, want %q", j.PostWriteWarning, boom.Error())
	}
	if j.Error != nil {
		t.Errorf("a committed write must not carry an error envelope, got %+v", j.Error)
	}

	// JSON mode and nil error stay silent on stderr.
	var quiet bytes.Buffer
	warnPostCommit(&quiet, true, "/music/a.flac", boom)
	warnPostCommit(&quiet, false, "/music/a.flac", nil)
	if quiet.Len() != 0 {
		t.Errorf("expected no stderr note under --json or for a nil error, got %q", quiet.String())
	}
}

// TestPostCommitWarningNamesTheWrittenFile: set -o note names output, not input.
func TestPostCommitWarningNamesTheWrittenFile(t *testing.T) {
	t.Parallel()
	var errb bytes.Buffer
	warnPostCommit(&errb, false, "out.flac", errors.New("directory fsync: no space left on device"))
	note := errb.String()
	if !strings.Contains(note, "out.flac") {
		t.Errorf("note should name the written file: %q", note)
	}
	if strings.Contains(note, "in.flac") {
		t.Errorf("note must not name the untouched input: %q", note)
	}
}
