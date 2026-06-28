package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestDumpJSONCardinalityParity checks that dump --json reports the same duplicate or
// conflict state the human dump shows for single-valued keys.
func TestDumpJSONCardinalityParity(t *testing.T) {
	td := func(n string) string { return filepath.Join("..", "..", "testdata", n) }

	cardinalityOf := func(t *testing.T, file, key string) (string, bool) {
		t.Helper()
		out, _, code := runCLI(t, "--json", "dump", file)
		if code != 0 {
			t.Fatalf("dump: code %d", code)
		}
		var docs []struct {
			Tags []struct {
				Key         string `json:"key"`
				Cardinality string `json:"cardinality"`
			} `json:"tags"`
		}
		if err := json.Unmarshal([]byte(out), &docs); err != nil {
			t.Fatalf("unmarshal dump json: %v\n%s", err, out)
		}
		if len(docs) != 1 {
			t.Fatalf("got %d docs, want 1", len(docs))
		}
		for _, tg := range docs[0].Tags {
			if tg.Key == key {
				return tg.Cardinality, true
			}
		}
		return "", false
	}

	for _, c := range []struct {
		name    string
		setArgs []string
		key     string
		want    string
	}{
		{"conflict", []string{"--set", "TITLE=A", "--add", "TITLE=B"}, "TITLE", "conflict"},
		{"duplicate", []string{"--set", "TITLE=Same", "--add", "TITLE=Same"}, "TITLE", "duplicate"},
		{"normal multivalued", []string{"--set", "GENRE=Rock", "--add", "GENRE=Jazz"}, "GENRE", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := copyFixture(t, td("notags.flac"))
			if _, errb, code := runCLI(t, append([]string{"set", f}, c.setArgs...)...); code != 0 {
				t.Fatalf("set: code %d, %s", code, errb)
			}
			got, ok := cardinalityOf(t, f, c.key)
			if !ok {
				t.Fatalf("no %s tag in dump", c.key)
			}
			if got != c.want {
				t.Errorf("cardinality = %q, want %q (must match the human dump's three-way marker)", got, c.want)
			}
		})
	}
}
