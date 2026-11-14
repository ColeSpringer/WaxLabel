package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

func TestKeysJSONListsWholeVocabulary(t *testing.T) {
	out, _, code := runCLI(t, "--json", "keys")
	if code != 0 {
		t.Fatalf("keys --json exit = %d, want 0", code)
	}
	var jk jsonKeys
	if err := json.Unmarshal([]byte(out), &jk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if want := len(tag.KnownKeys()); len(jk.Keys) != want {
		t.Fatalf("keys listed %d, want the whole vocabulary (%d)", len(jk.Keys), want)
	}
	// ARTIST multi, TITLE single, both with descriptions.
	got := map[string]jsonKey{}
	for _, k := range jk.Keys {
		got[k.Key] = k
	}
	if k := got["ARTIST"]; k.Cardinality != "multi" || k.Description == "" {
		t.Errorf("ARTIST = %+v, want multi with a description", k)
	}
	if k := got["TITLE"]; k.Cardinality != "single" || k.Description == "" {
		t.Errorf("TITLE = %+v, want single with a description", k)
	}
	// RECORDINGDATE lists DATE/YEAR aliases; TITLE omits aliases field.
	if k := got["RECORDINGDATE"]; !slices.Contains(k.Aliases, "DATE") || !slices.Contains(k.Aliases, "YEAR") {
		t.Errorf("RECORDINGDATE aliases = %v, want them to contain DATE and YEAR", k.Aliases)
	}
	if k := got["TITLE"]; len(k.Aliases) != 0 {
		t.Errorf("TITLE aliases = %v, want none", k.Aliases)
	}
}

func TestKeysTextListsWholeVocabulary(t *testing.T) {
	out, _, code := runCLI(t, "keys")
	if code != 0 {
		t.Fatalf("keys exit = %d, want 0", code)
	}
	// Header shows count; one line per key.
	for _, k := range tag.KnownKeys() {
		if !strings.Contains(out, string(k)) {
			t.Errorf("keys output missing %q", k)
		}
	}
	// Text listing shows aliases inline on RECORDINGDATE.
	if !strings.Contains(out, "aliases: DATE") {
		t.Errorf("keys text output missing the RECORDINGDATE aliases annotation:\n%s", out)
	}
}

// The TV keys are listed, single-valued and described, in both renderings.
func TestKeysListsTVKeys(t *testing.T) {
	tv := []string{"SHOW", "SHOWSORT", "EPISODEID", "EPISODENUMBER", "SEASONNUMBER", "NETWORK"}
	out, _, code := runCLI(t, "--json", "keys")
	if code != 0 {
		t.Fatalf("keys --json exit = %d, want 0", code)
	}
	var jk jsonKeys
	if err := json.Unmarshal([]byte(out), &jk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]jsonKey{}
	for _, k := range jk.Keys {
		got[k.Key] = k
	}
	text, _, _ := runCLI(t, "keys")
	for _, k := range tv {
		if e, ok := got[k]; !ok || e.Cardinality != "single" || e.Description == "" {
			t.Errorf("keys --json %s = %+v (listed %v), want single with a description", k, e, ok)
		}
		if !strings.Contains(text, "\n  "+k+" ") {
			t.Errorf("keys text output has no %s line:\n%s", k, text)
		}
	}
}

func TestKeysRejectsArguments(t *testing.T) {
	_, _, code := runCLI(t, "keys", "extra")
	if code != 2 {
		t.Fatalf("keys with an argument exit = %d, want 2 (usage)", code)
	}
}

// TestInvalidKeyCode: a key the tag model refuses is the invalid-key class (exit 2), not a
// generic usage error.
func TestInvalidKeyCode(t *testing.T) {
	t.Parallel()
	for _, edit := range [][]string{
		{"--set", "Ä=1"}, {"--add", "Ä=1"}, {"--clear", "a=b"}, {"--clear", ""}, {"--set", "=1"},
	} {
		for _, cmd := range []string{"plan", "set"} {
			args := append([]string{cmd, sampleFLAC}, edit...)
			out, _, code := runCLI(t, append([]string{"--json"}, args...)...)
			env := decodeJSONOne[jsonError](t, out)
			if code != 2 || env.Error.Code != "invalid-key" || !strings.Contains(env.Error.Message, "invalid tag key") {
				t.Errorf("%v --json: exit %d, error %+v; want invalid-key", args, code, env.Error)
			}
			if _, errb, code := runCLI(t, args...); code != 2 || !strings.HasPrefix(errb, "waxlabel: invalid tag key") {
				t.Errorf("%v: exit %d, stderr %q", args, code, errb)
			}
		}
	}
	// A malformed assignment is still a usage error.
	out, _, _ := runCLI(t, "--json", "plan", sampleFLAC, "--set", "TITLE")
	if env := decodeJSONOne[jsonError](t, out); env.Error.Code != "usage" {
		t.Errorf("--set without '=': code %q, want usage", env.Error.Code)
	}
}
