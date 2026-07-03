package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// TestSplitSyncedLyric checks the --add-synced-lyric assignment parser: a timestamp before
// '=' and verbatim text after (empty and '='-bearing text allowed).
func TestSplitSyncedLyric(t *testing.T) {
	cases := []struct {
		in   string
		time time.Duration
		text string
	}{
		{"1:30=Verse", 90 * time.Second, "Verse"},
		{"0:00.500=", 500 * time.Millisecond, ""},  // empty text (clear marker)
		{"12=a=b", 12 * time.Second, "a=b"},        // '=' in text
		{"0:12.00=café", 12 * time.Second, "café"}, // unicode
	}
	for _, c := range cases {
		ln, err := splitSyncedLyric(c.in)
		if err != nil {
			t.Errorf("splitSyncedLyric(%q) error = %v", c.in, err)
			continue
		}
		if ln.Time != c.time || ln.Text != c.text {
			t.Errorf("splitSyncedLyric(%q) = {%v %q}, want {%v %q}", c.in, ln.Time, ln.Text, c.time, c.text)
		}
	}
	if _, err := splitSyncedLyric("no-equals"); err == nil {
		t.Error("expected error for a missing '='")
	}
	if _, err := splitSyncedLyric("bogus=text"); err == nil {
		t.Error("expected error for a malformed timestamp")
	}
}

// TestCLISyncedLyricsFile exercises the end-to-end CLI path: author synced lyrics from an
// LRC file, then read them back through dump --json.
func TestCLISyncedLyricsFile(t *testing.T) {
	lrc := filepath.Join(t.TempDir(), "song.lrc")
	if err := os.WriteFile(lrc, []byte("[ti:Song]\n[00:01.00]One\n[00:12.50]Two\n[00:30.00]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := copyFixture(t, "../../testdata/notags.flac")

	_, errb, code := runCLI(t, "set", file, "--synced-lyrics-file", lrc, "--synced-lyrics-lang", "eng")
	if code != 0 {
		t.Fatalf("set exit %d: %s", code, errb)
	}

	out, _, code := runCLI(t, "dump", "--json", file)
	if code != 0 {
		t.Fatalf("dump exit %d", code)
	}
	jd := decodeJSONOne[jsonDocument](t, out)
	if len(jd.SyncedLyrics) != 1 {
		t.Fatalf("syncedLyrics sets = %d, want 1: %s", len(jd.SyncedLyrics), out)
	}
	lines := jd.SyncedLyrics[0].Lines
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3 (the [ti:] metadata tag is skipped): %+v", len(lines), lines)
	}
	if lines[0].TimeMs != 1000 || lines[0].Text != "One" {
		t.Errorf("line0 = {%d %q}, want {1000 One}", lines[0].TimeMs, lines[0].Text)
	}
	if lines[2].TimeMs != 30000 || lines[2].Text != "" {
		t.Errorf("line2 (clear marker) = {%d %q}, want {30000 \"\"}", lines[2].TimeMs, lines[2].Text)
	}

	// Clearing removes them.
	if _, errb, code := runCLI(t, "set", file, "--clear-synced-lyrics"); code != 0 {
		t.Fatalf("clear exit %d: %s", code, errb)
	}
	out, _, _ = runCLI(t, "dump", "--json", file)
	if jd := decodeJSONOne[jsonDocument](t, out); len(jd.SyncedLyrics) != 0 {
		t.Errorf("after clear: %d sets, want 0", len(jd.SyncedLyrics))
	}
}

// TestCLIAddSyncedLyric exercises --add-synced-lyric building one set from individual lines.
func TestCLIAddSyncedLyric(t *testing.T) {
	file := copyFixture(t, "../../testdata/notags.mp3")
	_, errb, code := runCLI(t, "set", file,
		"--add-synced-lyric", "0:00=Intro",
		"--add-synced-lyric", "0:05.5=Verse",
		"--synced-lyrics-lang", "eng")
	if code != 0 {
		t.Fatalf("set exit %d: %s", code, errb)
	}
	out, _, _ := runCLI(t, "dump", "--json", file)
	jd := decodeJSONOne[jsonDocument](t, out)
	if len(jd.SyncedLyrics) != 1 || len(jd.SyncedLyrics[0].Lines) != 2 {
		t.Fatalf("syncedLyrics = %+v, want one set of two lines", jd.SyncedLyrics)
	}
	if jd.SyncedLyrics[0].Language != "eng" {
		t.Errorf("language = %q, want eng (ID3 SYLT keeps it)", jd.SyncedLyrics[0].Language)
	}
}

// TestCLICapsSyncedLyrics checks caps reports the synced-lyrics dimension as writable for
// the SYLT and LRC formats.
func TestCLICapsSyncedLyrics(t *testing.T) {
	for _, format := range []string{"mp3", "flac", "ogg", "wav", "m4a"} {
		out, _, code := runCLI(t, "caps", "--format", format, "--json")
		if code != 0 {
			t.Fatalf("caps %s exit %d", format, code)
		}
		var jc jsonCaps
		if err := json.Unmarshal([]byte(out), &jc); err != nil {
			t.Fatalf("caps %s: %v", format, err)
		}
		if jc.SyncedLyrics == nil {
			t.Fatalf("caps %s: no syncedLyrics dimension", format)
		}
		// MP4 (m4a) carries synced lyrics in a timed-text track, not metadata, so its
		// write level is none; the SYLT/LRC formats are full.
		wantFull := format != "m4a"
		if got := jc.SyncedLyrics.Write == "full"; got != wantFull {
			t.Errorf("caps %s: syncedLyrics write = %q, wantFull=%v", format, jc.SyncedLyrics.Write, wantFull)
		}
	}
}

// TestTransferLabelSyncedLyrics checks the copy report names a synced-lyrics transfer item
// rather than printing a blank label (it has no key, so a missing switch arm would fall
// through to the empty default).
func TestTransferLabelSyncedLyrics(t *testing.T) {
	got := transferLabel(wl.TransferItem{Kind: wl.TransferSyncedLyric, Count: 2})
	if got != "synced lyrics (2)" {
		t.Errorf("transferLabel(synced lyrics) = %q, want %q", got, "synced lyrics (2)")
	}
}

// TestCLISyncedLyricsLangValidation checks a malformed --synced-lyrics-lang is rejected
// once up front (a usage error, exit 2) rather than silently padded into the SYLT field.
// The validation should not depend on parsing a target file.
func TestCLISyncedLyricsLangValidation(t *testing.T) {
	file := copyFixture(t, "../../testdata/notags.mp3")
	for _, bad := range []string{"en", "zz", "english", "e1g"} {
		_, errb, code := runCLI(t, "set", file, "--add-synced-lyric", "0:00=Hi", "--synced-lyrics-lang", bad)
		if code != 2 {
			t.Errorf("--synced-lyrics-lang %q exit = %d, want 2 (usage error)", bad, code)
		}
		// The message promises 3 ASCII letters, not a full ISO-639-2 registry lookup (the
		// validator accepts any 3 letters), so it must not overpromise a code check it never does.
		if !strings.Contains(errb, "3 ASCII letters") {
			t.Errorf("--synced-lyrics-lang %q message = %q, want it to mention \"3 ASCII letters\"", bad, errb)
		}
	}
	// Any 3 ASCII letters are accepted, including one that is not a registered ISO-639-2 code.
	for _, ok := range []string{"eng", "zzz"} {
		if _, errb, code := runCLI(t, "set", file, "--add-synced-lyric", "0:00=Hi", "--synced-lyrics-lang", ok); code != 0 {
			t.Errorf("--synced-lyrics-lang %q exit = %d: %s", ok, code, errb)
		}
	}
}

// TestCLISyncedLyricsLangUppercaseCanonicalized checks that an uppercase
// --synced-lyrics-lang is stored and dumped as canonical lowercase ISO-639-2.
func TestCLISyncedLyricsLangUppercaseCanonicalized(t *testing.T) {
	file := copyFixture(t, "../../testdata/notags.mp3")
	if _, errb, code := runCLI(t, "set", file, "--add-synced-lyric", "0:00=Hi", "--synced-lyrics-lang", "ENG"); code != 0 {
		t.Fatalf("set exit %d: %s", code, errb)
	}
	out, _, _ := runCLI(t, "dump", "--json", file)
	jd := decodeJSONOne[jsonDocument](t, out)
	if len(jd.SyncedLyrics) != 1 || jd.SyncedLyrics[0].Language != "eng" {
		t.Errorf("synced lyrics = %+v, want one set with language eng", jd.SyncedLyrics)
	}
}

// TestCLISyncedLyricsUnsupported checks authoring synced lyrics on a format that cannot
// store them (MP4) fails cleanly rather than silently dropping.
func TestCLISyncedLyricsUnsupported(t *testing.T) {
	file := copyFixture(t, "../../testdata/notags.m4a")
	_, errb, code := runCLI(t, "set", file, "--add-synced-lyric", "0:00=Hi")
	if code == 0 {
		t.Fatal("expected a non-zero exit setting synced lyrics on MP4")
	}
	if errb == "" {
		t.Error("expected an error message on stderr")
	}
}
