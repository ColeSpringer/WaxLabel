package waxlabel

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestRenameErrorHidesTempName: a failed commit must report the target the user named and the
// reason, never the temp file it renamed from. os.Rename's *os.LinkError carries both paths,
// so the raw error leaked ".waxlabel-*.tmp" into the CLI's per-file line for exactly the
// Windows sharing violation this write path exists to handle.
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
	// Unwrapping to the LinkError is what keeps the CLI's local-I/O class (exit 6).
	if _, ok := errors.AsType[*os.LinkError](e); !ok {
		t.Error("renameError must unwrap to the *os.LinkError")
	}
}

// TestRenameErrorNonLinkError: a rename failure that is not a *os.LinkError has no temp
// name to drop, so its message is kept whole rather than being reduced to nothing.
func TestRenameErrorNonLinkError(t *testing.T) {
	t.Parallel()
	e := &renameError{target: "/music/track.flac", err: errors.New("disk on fire")}
	if want, got := "replace /music/track.flac: disk on fire", e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
