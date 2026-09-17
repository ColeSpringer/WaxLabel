package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCLIWebMCoverDropped: WebM drops cover with warning; mixed tag still applies; --strict fails.
func TestCLIWebMCoverDropped(t *testing.T) {
	const code = "picture-unsupported"
	cover := writeTempImage(t, "cover.png", minimalPNG())

	// ARTIST applies; cover drops.
	file := copyFixture(t, "../../testdata/sample.webm")
	out, errb, exit := runCLI(t, "--json", "set", file, "--set", "ARTIST=Keep", "--add-cover", cover)
	if exit != 0 {
		t.Fatalf("set with an unsupported cover exit = %d, want 0: %s", exit, errb)
	}
	if !reportHasWarning(t, out, code) {
		t.Errorf("dropping cover art on WebM must warn %q\n%s", code, out)
	}
	dump, _, _ := runCLI(t, "dump", "--json", file)
	jd := decodeJSONOne[jsonDocument](t, dump)
	if got := tagValues(jd, "ARTIST"); len(got) != 1 || got[0] != "Keep" {
		t.Errorf("ARTIST = %v, want [Keep] (the storable edit still applies)", got)
	}
	if len(jd.Pictures) != 0 {
		t.Errorf("pictures = %+v, want none (dropped on WebM)", jd.Pictures)
	}

	// --strict escalates drop to failure.
	file2 := copyFixture(t, "../../testdata/sample.webm")
	_, _, exit2 := runCLI(t, "set", file2, "--strict", "--add-cover", cover)
	if exit2 == 0 {
		t.Error("--strict must fail when an added cover is dropped as unsupported")
	}

	// Dropped cover skips picture sanity checks; one unsupported-drop warning only.
	file3 := copyFixture(t, "../../testdata/sample.webm")
	out3, _, exit3 := runCLI(t, "--json", "set", file3,
		"--add-picture", "front-cover="+cover, "--add-picture", "front-cover="+cover)
	if exit3 != 0 {
		t.Fatalf("two-cover WebM set exit = %d, want 0", exit3)
	}
	if reportHasWarning(t, out3, "multiple-front-covers") {
		t.Errorf("a dropped WebM cover set must not emit a multiple-front-covers sanity note\n%s", out3)
	}
	if !reportHasWarning(t, out3, code) {
		t.Errorf("the drop must still surface %q\n%s", code, out3)
	}
}

// TestCLIClearSyncedLyricsClearsLanguage: clear+author resets language; explicit lang wins.
func TestCLIClearSyncedLyricsClearsLanguage(t *testing.T) {
	syncedLang := func(t *testing.T, file string) string {
		t.Helper()
		out, _, _ := runCLI(t, "dump", "--json", file)
		jd := decodeJSONOne[jsonDocument](t, out)
		if len(jd.SyncedLyrics) != 1 {
			t.Fatalf("synced-lyrics sets = %d, want 1", len(jd.SyncedLyrics))
		}
		return jd.SyncedLyrics[0].Language
	}

	// Seed SYLT with language deu.
	file := copyFixture(t, "../../testdata/notags.mp3")
	if _, errb, code := runCLI(t, "set", file, "--add-synced-lyric", "0:05=Old", "--synced-lyrics-lang", "deu"); code != 0 {
		t.Fatalf("author deu exit %d: %s", code, errb)
	}
	if got := syncedLang(t, file); got != "deu" {
		t.Fatalf("language after author = %q, want deu", got)
	}

	// Clear + author: no inherited language.
	if _, errb, code := runCLI(t, "set", file, "--clear-synced-lyrics", "--add-synced-lyric", "5=New"); code != 0 {
		t.Fatalf("clear+author exit %d: %s", code, errb)
	}
	if got := syncedLang(t, file); got != "" {
		t.Errorf("language after clear+author = %q, want empty (cleared, not inherited)", got)
	}

	// Clear + explicit lang: explicit wins.
	file2 := copyFixture(t, "../../testdata/notags.mp3")
	if _, _, code := runCLI(t, "set", file2, "--add-synced-lyric", "0:05=Old", "--synced-lyrics-lang", "eng"); code != 0 {
		t.Fatalf("author eng failed")
	}
	if _, _, code := runCLI(t, "set", file2, "--clear-synced-lyrics", "--add-synced-lyric", "5=New", "--synced-lyrics-lang", "deu"); code != 0 {
		t.Fatalf("clear+author+deu failed")
	}
	if got := syncedLang(t, file2); got != "deu" {
		t.Errorf("language after clear+author+deu = %q, want deu (explicit wins)", got)
	}

	// Plain author without clear keeps existing language.
	file3 := copyFixture(t, "../../testdata/notags.mp3")
	if _, _, code := runCLI(t, "set", file3, "--add-synced-lyric", "0:05=Old", "--synced-lyrics-lang", "eng"); code != 0 {
		t.Fatalf("author eng failed")
	}
	if _, _, code := runCLI(t, "set", file3, "--add-synced-lyric", "5=New"); code != 0 {
		t.Fatalf("author-only failed")
	}
	if got := syncedLang(t, file3); got != "eng" {
		t.Errorf("language after author-only = %q, want eng (inherited, no clear)", got)
	}
}

// TestCLIClearSyncedLyricsFileClearsLanguage: same clear behavior via --synced-lyrics-file.
func TestCLIClearSyncedLyricsFileClearsLanguage(t *testing.T) {
	lrc := filepath.Join(t.TempDir(), "new.lrc")
	if err := os.WriteFile(lrc, []byte("[00:05.000]fresh line"), 0o644); err != nil {
		t.Fatal(err)
	}

	file := copyFixture(t, "../../testdata/notags.mp3")
	if _, _, code := runCLI(t, "set", file, "--add-synced-lyric", "0:05=Old", "--synced-lyrics-lang", "fra"); code != 0 {
		t.Fatalf("author fra failed")
	}
	if _, errb, code := runCLI(t, "set", file, "--clear-synced-lyrics", "--synced-lyrics-file", lrc); code != 0 {
		t.Fatalf("clear + --synced-lyrics-file exit %d: %s", code, errb)
	}
	out, _, _ := runCLI(t, "dump", "--json", file)
	jd := decodeJSONOne[jsonDocument](t, out)
	if len(jd.SyncedLyrics) != 1 {
		t.Fatalf("synced-lyrics sets = %d, want 1", len(jd.SyncedLyrics))
	}
	if lang := jd.SyncedLyrics[0].Language; lang != "" {
		t.Errorf("language after clear + file = %q, want empty (cleared, not inherited)", lang)
	}
}

// TestCLISyncedLyricsWriteTruncationStrict: over-cap truncates with warning; --strict fails.
func TestCLISyncedLyricsWriteTruncationStrict(t *testing.T) {
	const cap = 1 << 16
	const code = "synced-lyrics-truncated"

	var b strings.Builder
	for i := 0; i < cap+1; i++ {
		// Distinct MM:SS lines, one per minute.
		b.WriteString("[")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(":00.000]x\n")
	}
	lrc := filepath.Join(t.TempDir(), "over.lrc")
	if err := os.WriteFile(lrc, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without --strict: exit 0, truncation warning, cap stored.
	file := copyFixture(t, "../../testdata/notags.flac")
	out, errb, exit := runCLI(t, "--json", "set", file, "--synced-lyrics-file", lrc)
	if exit != 0 {
		t.Fatalf("over-cap author exit = %d, want 0: %s", exit, errb)
	}
	if !reportHasWarning(t, out, code) {
		t.Errorf("an over-cap synced-lyrics author must warn %q\n%s", code, out)
	}
	dump, _, _ := runCLI(t, "dump", "--json", file)
	jd := decodeJSONOne[jsonDocument](t, dump)
	if len(jd.SyncedLyrics) != 1 || len(jd.SyncedLyrics[0].Lines) != cap {
		t.Errorf("stored lines = %v, want one set of exactly %d", jd.SyncedLyrics, cap)
	}

	// With --strict: truncation fails.
	file2 := copyFixture(t, "../../testdata/notags.flac")
	_, _, exit2 := runCLI(t, "set", file2, "--strict", "--synced-lyrics-file", lrc)
	if exit2 == 0 {
		t.Error("--strict must fail when an over-cap synced-lyrics set is truncated on write")
	}
}
