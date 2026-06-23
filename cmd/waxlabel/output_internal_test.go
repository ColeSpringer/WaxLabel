package main

import (
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestHasFlagHonorsDoubleDash (review #1): hasFlag detects a long flag before the POSIX
// "--" terminator but treats an identical-looking token after "--" as a positional, so
// a file literally named --format does not masquerade as the caps format query and flip
// a list command's pre-flight error from an array to an object.
func TestHasFlagHonorsDoubleDash(t *testing.T) {
	if !hasFlag([]string{"caps", "--format", "flac"}, "format") {
		t.Error("--format before -- should be detected as the flag")
	}
	if !hasFlag([]string{"caps", "--format=flac"}, "format") {
		t.Error("--format=value should be detected")
	}
	if hasFlag([]string{"caps", "--", "--format"}, "format") {
		t.Error("--format after -- is a positional, not the flag")
	}
	if hasFlag([]string{"caps", "song.flac"}, "format") {
		t.Error("no --format present, should be false")
	}
}

// TestStrictWarningReasonKeyless (review #2): the --strict reason names the offending
// key when a warning carries one, and degrades to the warning's own prose (rather than
// a message with a leading bare colon) for a defensive keyless warning.
func TestStrictWarningReasonKeyless(t *testing.T) {
	keyed := wl.Warning{Code: wl.WarnValueDropped, Message: "TITLE value dropped", Keys: []tag.Key{tag.Title}}
	if got := strictWarningReason(keyed); !strings.HasPrefix(got, "TITLE:") {
		t.Errorf("keyed reason = %q, want it to name TITLE", got)
	}
	keyless := wl.Warning{Code: wl.WarnValueDropped, Message: "something was dropped"}
	if got := strictWarningReason(keyless); got != "something was dropped" {
		t.Errorf("keyless reason = %q, want the warning's own message (no leading colon)", got)
	}
}
