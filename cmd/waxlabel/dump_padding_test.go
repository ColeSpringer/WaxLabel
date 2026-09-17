package main

import (
	"strings"
	"testing"
)

// TestDumpPaddingMatchesPlan: dump paddingBytes must match plan padding for same-length edit.
func TestDumpPaddingMatchesPlan(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sample.flac", "sample.mp3", "sample.aac", "sample.m4a"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := copyFixture(t, td(name))
			// Seed via waxlabel so padding reflects this codec, not the original muxer.
			if _, errb, code := runCLI(t, "set", f, "--set", "ARTIST=AAAAAA"); code != 0 {
				t.Fatalf("seeding write: exit = %d\n%s", code, errb)
			}
			jd := dumpJSON(t, f)
			if jd.Properties == nil || jd.Properties.PaddingBytes <= 0 {
				t.Fatalf("dump reported no paddingBytes for %s: %+v", name, jd.Properties)
			}
			// Same-length edit reuses metadata region; leftover padding matches dump.
			out, _, code := runCLI(t, "--json", "plan", f, "--set", "ARTIST=BBBBBB")
			if code != 0 {
				t.Fatalf("plan exit = %d\n%s", code, out)
			}
			jr := decodeJSONList[jsonReport](t, out)
			if len(jr) != 1 {
				t.Fatalf("want one plan report, got %d: %s", len(jr), out)
			}
			if jd.Properties.PaddingBytes != jr[0].PaddingAfter {
				t.Errorf("dump paddingBytes = %d, plan padding = %d; they describe the same region",
					jd.Properties.PaddingBytes, jr[0].PaddingAfter)
			}
		})
	}
}

// TestDumpPaddingOgg: dump and plan agree on Ogg comment padding (not Ogg FLAC PADDING block).
func TestDumpPaddingOgg(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sample.opus", "sample.ogg", "sample.oga"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := copyFixture(t, td(name))
			jd := dumpJSON(t, f)
			out, _, code := runCLI(t, "--json", "plan", f, "--set", "ARTIST=Padded")
			if code != 0 {
				t.Fatalf("plan exit = %d\n%s", code, out)
			}
			jr := decodeJSONList[jsonReport](t, out)
			if len(jr) != 1 {
				t.Fatalf("want one plan report, got %d: %s", len(jr), out)
			}
			var got int64
			if jd.Properties != nil {
				got = jd.Properties.PaddingBytes
			}
			if got != jr[0].PaddingAfter {
				t.Errorf("dump paddingBytes = %d, plan padding = %d", got, jr[0].PaddingAfter)
			}
		})
	}
}

// TestDumpPaddingAbsentWithoutARegion: formats with no padding region omit paddingBytes.
func TestDumpPaddingAbsentWithoutARegion(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sample.wav", "sample.aiff", "sample.mka", "sample.wv", "sample.ape"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, _, code := runCLI(t, "--json", "dump", td(name))
			if code != 0 {
				t.Fatalf("dump exit = %d\n%s", code, out)
			}
			if strings.Contains(out, "paddingBytes") {
				t.Errorf("%s has no padding region; dump should omit paddingBytes:\n%s", name, out)
			}
		})
	}
}
