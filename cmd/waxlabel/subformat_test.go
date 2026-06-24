package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestSubformatJSON checks that dump and caps expose the exact subtype at the top
// level. format stays at the family level, while subformat distinguishes WebM from
// Matroska and AIFC from AIFF. Plain formats report the same value for both fields.
func TestSubformatJSON(t *testing.T) {
	sampleAIFC := filepath.Join("..", "..", "testdata", "sample.aifc")

	cases := []struct {
		name, path, wantFormat, wantSub string
	}{
		{"webm", sampleWebMF, "Matroska", "WebM"},
		{"aifc", sampleAIFC, "AIFF", "AIFC"},
		{"flac", sampleFLAC, "FLAC", "FLAC"}, // plain: subformat == format
	}
	for _, c := range cases {
		t.Run("dump/"+c.name, func(t *testing.T) {
			jd := dumpJSON(t, c.path)
			if jd.Format != c.wantFormat {
				t.Errorf("dump format = %q, want %q", jd.Format, c.wantFormat)
			}
			if jd.Subformat != c.wantSub {
				t.Errorf("dump subformat = %q, want %q", jd.Subformat, c.wantSub)
			}
		})
		t.Run("caps/"+c.name, func(t *testing.T) {
			out, _, code := runCLI(t, "--json", "caps", c.path)
			if code != 0 {
				t.Fatalf("caps %s exit = %d\n%s", c.path, code, out)
			}
			jc := decodeJSONOne[jsonCaps](t, out)
			if jc.Format != c.wantFormat {
				t.Errorf("caps format = %q, want %q", jc.Format, c.wantFormat)
			}
			if jc.Subformat != c.wantSub {
				t.Errorf("caps subformat = %q, want %q", jc.Subformat, c.wantSub)
			}
		})
	}

	// caps --format webm has no file, but the selected WebM variant still supplies the
	// subtype. Adding this field does not change schemaVersion.
	t.Run("caps/--format-webm", func(t *testing.T) {
		out, _, code := runCLI(t, "--json", "caps", "--format", "webm")
		if code != 0 {
			t.Fatalf("caps --format webm exit = %d\n%s", code, out)
		}
		var jc jsonCaps
		if err := json.Unmarshal([]byte(out), &jc); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if jc.SchemaVersion != schemaVersion {
			t.Errorf("schemaVersion = %d, want %d (no bump for an additive field)", jc.SchemaVersion, schemaVersion)
		}
		if jc.Format != "Matroska" || jc.Subformat != "WebM" {
			t.Errorf("caps --format webm: format=%q subformat=%q, want Matroska/WebM", jc.Format, jc.Subformat)
		}
	})
}
