package waxlabel

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestRenameErrorHidesTempName: commit failure names the target, not .waxlabel-*.tmp.
func TestRenameErrorHidesTempName(t *testing.T) {
	t.Parallel()
	inner := &os.LinkError{
		Op:  "rename",
		Old: "/music/.waxlabel-2417.tmp",
		New: "/music/track.flac",
		Err: errors.New("the process cannot access the file"),
	}
	e := &renameError{target: "/music/track.flac", err: inner}

	if got := e.Error(); strings.Contains(got, ".waxlabel-") {
		t.Errorf("the internal temp name leaked: %q", got)
	}
	want := "replace /music/track.flac: the process cannot access the file"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	// Unwrap LinkError so CLI keeps local-I/O class (exit 6).
	if _, ok := errors.AsType[*os.LinkError](e); !ok {
		t.Error("renameError must unwrap to the *os.LinkError")
	}
}

// TestRenameErrorNonLinkError: non-LinkError message kept whole.
func TestRenameErrorNonLinkError(t *testing.T) {
	t.Parallel()
	e := &renameError{target: "/music/track.flac", err: errors.New("disk on fire")}
	if want, got := "replace /music/track.flac: disk on fire", e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
