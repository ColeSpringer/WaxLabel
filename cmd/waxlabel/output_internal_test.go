package main

import (
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestHasFlagHonorsDoubleDash: hasFlag detects a long flag before the POSIX
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

// TestWantsJSONParsesBoolForms checks that raw-argument routing follows pflag's
// boolean forms: ParseBool spellings update the flag, invalid values leave the
// previous state unchanged, and tokens after "--" are positionals.
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

// TestStrictWarningReasonKeyless: the --strict reason names the offending
// key when a warning carries one, and degrades to the warning's own prose (rather than
// a message with a leading bare colon) for a defensive keyless warning. Uses a code that keeps
// the compact "KEY: reason" format (WarnValueDropped now echoes its own message instead - see
// TestStrictWarningReasonValueDroppedEchoesMessage).
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
// plan-body warning verbatim rather than a fixed phrase, so the --strict error and the plan
// report never contradict each other on the drop reason - notably the MP4 trkn/disk 0 case, which
// reads "treated as unset ... reads back as absent", not "cannot be represented".
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

// TestStrictEscalatesTagStructureDropped is a regression guard: WarnTagStructureDropped is
// keyed for a lossy edited field, so --strict must escalate it (the finding emitted it
// "so --strict can act on it"). This checks both halves of the gate: the code is in the
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
