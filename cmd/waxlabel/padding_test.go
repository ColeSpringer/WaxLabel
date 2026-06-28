package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sizeOf(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}

// TestPaddingFlagsApplyAlone checks that padding flags still apply when no metadata edit
// is pending. --no-padding strips existing padding, an already satisfied --padding N stays
// a no-op, and a larger --padding N grows the region.
func TestPaddingFlagsApplyAlone(t *testing.T) {
	td := func(name string) string { return filepath.Join("..", "..", "testdata", name) }

	for _, fix := range []string{"sample.flac", "sample.mp3", "sample.aac"} {
		t.Run("no-padding strips "+fix, func(t *testing.T) {
			f := copyFixture(t, td(fix))
			// Reserve a known padding region first so there is something to strip.
			if _, errb, code := runCLI(t, "set", f, "--padding", "8192"); code != 0 {
				t.Fatalf("--padding 8192: code %d, %s", code, errb)
			}
			padded := sizeOf(t, f)
			if _, errb, code := runCLI(t, "set", f, "--no-padding"); code != 0 {
				t.Fatalf("--no-padding: code %d, %s", code, errb)
			}
			if stripped := sizeOf(t, f); stripped >= padded {
				t.Errorf("--no-padding did not shrink %s: %d -> %d", fix, padded, stripped)
			}
		})
	}

	t.Run("padding already satisfied is a no-op", func(t *testing.T) {
		f := copyFixture(t, td("sample.flac"))
		if _, _, code := runCLI(t, "set", f, "--padding", "8192"); code != 0 {
			t.Fatalf("first --padding 8192: code %d", code)
		}
		before := sizeOf(t, f)
		out, errb, code := runCLI(t, "set", f, "--padding", "8192")
		if code != 0 {
			t.Fatalf("repeat --padding 8192: code %d, %s", code, errb)
		}
		if !strings.Contains(out+errb, "no changes") {
			t.Errorf("repeat --padding 8192 should be a no-op:\n%s%s", out, errb)
		}
		if after := sizeOf(t, f); after != before {
			t.Errorf("satisfied --padding changed size: %d -> %d", before, after)
		}
	})

	t.Run("padding above current grows", func(t *testing.T) {
		f := copyFixture(t, td("sample.flac"))
		if _, _, code := runCLI(t, "set", f, "--padding", "8192"); code != 0 {
			t.Fatalf("--padding 8192: code %d", code)
		}
		before := sizeOf(t, f)
		if _, _, code := runCLI(t, "set", f, "--padding", "32768"); code != 0 {
			t.Fatalf("--padding 32768: code %d", code)
		}
		if after := sizeOf(t, f); after <= before {
			t.Errorf("--padding 32768 should grow the region: %d -> %d", before, after)
		}
	})
}

// TestPaddingPresetsApplyAlone checks padding presets with no metadata edit. minimal
// strips padding, while preserve leaves both padded and zero-padding files unchanged.
func TestPaddingPresetsApplyAlone(t *testing.T) {
	td := func(name string) string { return filepath.Join("..", "..", "testdata", name) }

	t.Run("minimal strips padding", func(t *testing.T) {
		f := copyFixture(t, td("sample.flac"))
		if _, _, code := runCLI(t, "set", f, "--padding", "8192"); code != 0 {
			t.Fatalf("--padding 8192: code %d", code)
		}
		padded := sizeOf(t, f)
		if _, errb, code := runCLI(t, "set", f, "--preset", "minimal"); code != 0 {
			t.Fatalf("--preset minimal: code %d, %s", code, errb)
		}
		if after := sizeOf(t, f); after >= padded {
			t.Errorf("--preset minimal did not strip padding: %d -> %d", padded, after)
		}
	})

	for _, state := range []struct {
		name     string
		preStrip bool
	}{
		{"padded file", false},
		{"zero-padding file", true},
	} {
		t.Run("preserve is a no-op on a "+state.name, func(t *testing.T) {
			f := copyFixture(t, td("sample.flac"))
			if state.preStrip {
				if _, _, code := runCLI(t, "set", f, "--no-padding"); code != 0 {
					t.Fatalf("pre-strip: code %d", code)
				}
			}
			before := sizeOf(t, f)
			out, errb, code := runCLI(t, "set", f, "--preset", "preserve")
			if code != 0 {
				t.Fatalf("--preset preserve: code %d, %s", code, errb)
			}
			if !strings.Contains(out+errb, "no changes") {
				t.Errorf("--preset preserve must not churn a %s:\n%s%s", state.name, out, errb)
			}
			if after := sizeOf(t, f); after != before {
				t.Errorf("--preset preserve changed size on a %s: %d -> %d", state.name, before, after)
			}
		})
	}
}
