package main

import (
	"encoding/json"
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
}

func TestKeysRejectsArguments(t *testing.T) {
	_, _, code := runCLI(t, "keys", "extra")
	if code != 2 {
		t.Fatalf("keys with an argument exit = %d, want 2 (usage)", code)
	}
}
