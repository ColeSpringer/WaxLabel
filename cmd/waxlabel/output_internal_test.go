package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestHasFlagHonorsDoubleDash: hasFlag detects a long flag before the POSIX "--" terminator
// but treats an identical-looking token after it as a positional, so a file literally named
// --format cannot masquerade as the caps format query and flip a list command's pre-flight
// error from an array to an object.
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

// TestWantsJSONParsesBoolForms: raw-argument routing follows pflag's boolean forms. ParseBool
// spellings update the flag, invalid values leave the previous state, and tokens after "--"
// are positionals.
func TestWantsJSONParsesBoolForms(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"caps", "--json"}, true},
		{[]string{"caps", "--json=true"}, true},
		{[]string{"caps", "--json=1"}, true},
		{[]string{"caps", "--json=t"}, true},
		{[]string{"caps", "--json=T"}, true},
		{[]string{"caps", "--json=TRUE"}, true},
		{[]string{"caps", "--json=True"}, true},
		{[]string{"caps", "--json=false"}, false},
		{[]string{"caps", "--json=0"}, false},
		{[]string{"caps", "--json=f"}, false},
		{[]string{"caps", "--json=bogus"}, false},         // unparseable: prior state (false) stands
		{[]string{"caps", "--json=yes"}, false},           // ParseBool rejects "yes" (not a bool spelling)
		{[]string{"caps", "--json=1", "--json=0"}, false}, // last wins
		{[]string{"caps", "--", "--json=1"}, false},       // after -- is a positional
		{[]string{"caps"}, false},
	} {
		if got := wantsJSON(c.args); got != c.want {
			t.Errorf("wantsJSON(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// TestStrictWarningReasonKeyless: the --strict reason names the offending key when a warning
// carries one, and degrades to the warning's own prose, not a leading bare colon, for a
// defensive keyless one. Uses a code that keeps the compact "KEY: reason" format;
// WarnValueDropped echoes its own message instead (see the test below).
func TestStrictWarningReasonKeyless(t *testing.T) {
	keyed := wl.Warning{Code: wl.WarnValueCoerced, Message: "TITLE coerced", Keys: []tag.Key{tag.Title}}
	if got := strictWarningReason(keyed); !strings.HasPrefix(got, "TITLE:") {
		t.Errorf("keyed reason = %q, want it to name TITLE with a colon", got)
	}
	keyless := wl.Warning{Code: wl.WarnValueCoerced, Message: "something was coerced"}
	if got := strictWarningReason(keyless); got != "something was coerced" {
		t.Errorf("keyless reason = %q, want the warning's own message (no leading colon)", got)
	}
}

// TestStrictWarningReasonValueDroppedEchoesMessage: a dropped-value strict reason mirrors the
// plan-body warning verbatim rather than a fixed phrase, so the two never contradict each
// other on the drop reason. Notably the MP4 trkn/disk 0 case, which reads "treated as unset
// ... reads back as absent", not "cannot be represented".
func TestStrictWarningReasonValueDroppedEchoesMessage(t *testing.T) {
	zero := wl.Warning{
		Code:    wl.WarnValueDropped,
		Message: `TRACKNUMBER value "0" is treated as unset in this format and reads back as absent`,
		Keys:    []tag.Key{tag.TrackNumber},
	}
	if got := strictWarningReason(zero); got != zero.Message {
		t.Errorf("value-dropped strict reason = %q, want the warning's own message verbatim", got)
	}
	if got := strictWarningReason(zero); strings.Contains(got, "cannot be represented") {
		t.Errorf("zero-slot strict reason must not use the 'cannot be represented' wording: %q", got)
	}
}

// TestStrictEscalatesTagStructureDropped: WarnTagStructureDropped is keyed for a lossy edited
// field, so --strict must escalate it. Checks both halves of the gate: the code is in the
// escalating set, and its reason names the offending key.
func TestStrictEscalatesTagStructureDropped(t *testing.T) {
	if !strictEscalatingCodes[wl.WarnTagStructureDropped] {
		t.Error("--strict must escalate tag-structure-dropped (a lossy keyed edit)")
	}
	w := wl.Warning{Code: wl.WarnTagStructureDropped, Message: "an edited album tag dropped its structure", Keys: []tag.Key{tag.Artist}}
	if got := strictWarningReason(w); !strings.HasPrefix(got, "ARTIST:") {
		t.Errorf("tag-structure-dropped strict reason = %q, want it to name ARTIST", got)
	}
}

// TestPerFileReasonAndEntryAgree covers the error shapes no portable CLI run produces, on
// both renderings at once: the human per-file line's reason and the --json element's message.
// Each is asserted against the same literal rather than against the other, which would only
// restate that errorEntry calls perFileReason.
func TestPerFileReasonAndEntryAgree(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{{
		name: "bare PathError drops the path its caller already printed",
		err:  &fs.PathError{Op: "open", Path: "f.flac", Err: fs.ErrPermission},
		want: "permission denied",
	}, {
		name: "not-found takes the canonical wording",
		err:  &fs.PathError{Op: "open", Path: "f.flac", Err: fs.ErrNotExist},
		want: notFoundReason,
	}, {
		name: "bare cancellation",
		err:  context.Canceled,
		want: canceledReason,
	}, {
		name: "wrapped deadline",
		err:  fmt.Errorf("computing: %w", context.DeadlineExceeded),
		want: timeoutReason,
	}, {
		// Not a bare PathError, so nothing is reduced and both paths survive. The write path
		// no longer emits a raw one, since a failed rename is wrapped to hide the temp name,
		// so this pins the fall-through any other LinkError still takes.
		name: "LinkError is not reduced",
		err:  &os.LinkError{Op: "rename", Old: "a.tmp", New: "f.flac", Err: errors.New("cross-device link")},
		want: "rename a.tmp f.flac: cross-device link",
	}, {
		// The boundary is load-bearing: pictureLoadError is the live wrapped case, and its
		// label around the inner path is the whole point of that wrapper.
		name: "wrapped PathError keeps the caller's framing and the inner path",
		err:  fmt.Errorf("reading cover art: %w", &fs.PathError{Op: "open", Path: "cover.jpg", Err: fs.ErrPermission}),
		want: "reading cover art: open cover.jpg: permission denied",
	}} {
		t.Run(c.name, func(t *testing.T) {
			if got := perFileReason(c.err); got != c.want {
				t.Errorf("perFileReason = %q, want %q", got, c.want)
			}
			if got := errorEntry("f.flac", c.err).Error.Message; got != c.want {
				t.Errorf("errorEntry message = %q, want %q", got, c.want)
			}
		})
	}
}
