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
	// Spot-check that cardinality and description are populated and correct: ARTIST
	// is multi-valued, TITLE single-valued, each carrying its description.
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
	// Aliases are surfaced so the common alternative spellings are discoverable: RECORDINGDATE
	// carries DATE and YEAR. A key with no aliases (TITLE) omits the field entirely.
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
	// The header reports the count; every known key appears on its own line.
	for _, k := range tag.KnownKeys() {
		if !strings.Contains(out, string(k)) {
			t.Errorf("keys output missing %q", k)
		}
	}
	// Aliases are shown inline on the RECORDINGDATE row so the common Vorbis spellings are
	// discoverable in the human listing, not just JSON.
	if !strings.Contains(out, "aliases: DATE") {
		t.Errorf("keys text output missing the RECORDINGDATE aliases annotation:\n%s", out)
	}
}

func TestKeysRejectsArguments(t *testing.T) {
	_, _, code := runCLI(t, "keys", "extra")
	if code != 2 {
		t.Fatalf("keys with an argument exit = %d, want 2 (usage)", code)
	}
}
