package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// Pins strictEscalatingCodes: edit-caused write losses escalate; pre-existing/read-path codes do not.
func TestStrictEscalatesWriteLossFamily(t *testing.T) {
	escalating := []wl.WarningCode{
		wl.WarnValueDropped, wl.WarnValueCoerced, wl.WarnSingleValuedMulti, wl.WarnTagStructureDropped,
		wl.WarnValueReduced,
		wl.WarnPictureMetadataDropped,
		wl.WarnChapterEndsDropped,
		wl.WarnChapterTitleTruncated,
		wl.WarnChapterStartOverflow,
		wl.WarnChapterMetadataDropped,
		wl.WarnSyncedLyricsMetadataDropped,
		wl.WarnSyncedLyricsTimestampClamped,
		wl.WarnNumericGenre,
		wl.WarnDuplicateTagBlockDropped, // write-path: duplicate held value survivor lacks
	}
	for _, c := range escalating {
		if !strictEscalatingCodes[c] {
			t.Errorf("--strict must escalate %v (an edit-caused write loss)", c)
		}
	}
	excluded := []wl.WarningCode{
		wl.WarnID3MultiValue,      // stored NUL-separated in full
		wl.WarnNativeValueReduced, // full set kept in winning container
		wl.WarnChaptersFlattened,  // can describe pre-existing state
		wl.WarnPaddingClamped,     // padding size, not tag content
		wl.WarnDuplicateTagBlock,  // read-path; write-path sibling escalates above
	}
	for _, c := range excluded {
		if strictEscalatingCodes[c] {
			t.Errorf("--strict must NOT escalate %v (not an edit-caused loss)", c)
		}
	}
}

// Bare numeric GENRE on ID3 formats coerces to name; --strict fails even on no-op re-apply. WAV keeps literal.
func TestStrictEscalatesNumericGenreCoercion(t *testing.T) {
	t.Parallel()
	aacFixture := filepath.Join("..", "..", "testdata", "notags.aac")
	for _, tc := range []struct{ name, fixture string }{
		{"mp3", notagsMP3},
		{"aac", aacFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := copyFixture(t, tc.fixture)
			_, stderr, code := runCLI(t, "set", f, "--set", "GENRE=17", "--strict")
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr: %s", code, stderr)
			}
			if !strings.Contains(stderr, "GENRE") || !strings.Contains(stderr, "numeric") {
				t.Errorf("stderr must name GENRE and the numeric coercion: %s", stderr)
			}
			// No-op re-apply of identical loss must still escalate.
			if _, _, code := runCLI(t, "set", f, "--set", "GENRE=17"); code != 0 {
				t.Fatalf("non-strict write failed")
			}
			if _, _, code := runCLI(t, "set", f, "--set", "GENRE=17", "--strict"); code != 2 {
				t.Errorf("re-applied identical loss: exit = %d, want 2", code)
			}
		})
	}
	t.Run("wav keeps the literal value", func(t *testing.T) {
		f := copyFixture(t, filepath.Join("..", "..", "testdata", "notags.wav"))
		if _, stderr, code := runCLI(t, "set", f, "--set", "GENRE=17", "--strict"); code != 0 {
			t.Errorf("exit = %d, want 0 (LIST/INFO IGNR keeps the literal); stderr: %s", code, stderr)
		}
	})
	// With --numeric-genre: one strict error, not double-report via capability wording.
	t.Run("numeric-genre flag does not double-report", func(t *testing.T) {
		f := copyFixture(t, notagsMP3)
		_, stderr, code := runCLI(t, "set", f, "--set", "GENRE=17", "--strict", "--numeric-genre")
		if code != 2 {
			t.Fatalf("exit = %d, want 2; stderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "numeric reference") {
			t.Errorf("strict error must carry the numeric-genre wording: %s", stderr)
		}
		if strings.Contains(stderr, "re-read as its canonical name") {
			t.Errorf("strict error restates the same loss as a capability reduction: %s", stderr)
		}
	})
}

// End-to-end: newly escalated keyed and keyless losses fail at exit 2 with plan-body wording.
func TestStrictEscalatesNewWriteLossesEndToEnd(t *testing.T) {
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// ID3v2.3 date frames need full day; 2021-03 loses month.
	t.Run("value-reduced keyed", func(t *testing.T) {
		mp3 := copyFixture(t, notagsMP3)
		_, stderr, code := runCLI(t, "set", mp3, "--set", "RECORDINGDATE=2021-03", "--strict")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if !strings.Contains(stderr, "RECORDINGDATE") || !strings.Contains(stderr, "reduced") {
			t.Errorf("strict error = %q, want it to name RECORDINGDATE and the reduction", stderr)
		}
	})

	// COMPILATION=maybe coerces to false, not dropped.
	t.Run("value-coerced boolean keyed", func(t *testing.T) {
		m4a := copyFixture(t, notagsM4A)
		_, stderr, code := runCLI(t, "set", m4a, "--set", "COMPILATION=maybe", "--strict")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if !strings.Contains(stderr, "COMPILATION") || !strings.Contains(stderr, "coerced") {
			t.Errorf("strict error = %q, want it to name COMPILATION and the coercion", stderr)
		}
	})

	// Leading zero is canonicalization, not a loss.
	t.Run("number canonicalization is not a strict loss", func(t *testing.T) {
		m4a := copyFixture(t, notagsM4A)
		if _, stderr, code := runCLI(t, "set", m4a, "--set", "TRACKNUMBER=03", "--strict"); code != 0 {
			t.Errorf("TRACKNUMBER=03 --strict exit = %d, want 0 (a leading zero is not a loss): %s", code, stderr)
		}
	})

	// MP4 chpl title length is 1 byte; >255 bytes truncates.
	t.Run("chapter-title-truncated keyless", func(t *testing.T) {
		m4a := copyFixture(t, notagsM4A)
		_, stderr, code := runCLI(t, "set", m4a, "--add-chapter", "0="+strings.Repeat("x", 300), "--strict")
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if !strings.Contains(stderr, "255") && !strings.Contains(stderr, "trimmed") {
			t.Errorf("strict error = %q, want it to mention the title truncation", stderr)
		}
	})
}

// Excluded codes and full carries stay exit 0 under --strict.
func TestStrictExcludedAndCarryUnaffected(t *testing.T) {
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	t.Run("id3-multi-value still exits 0", func(t *testing.T) {
		mp3 := copyFixture(t, notagsMP3)
		if _, _, code := runCLI(t, "set", mp3, "--set", "ARTIST=A", "--add", "ARTIST=B", "--strict"); code != 0 {
			t.Errorf("multi-value MP3 edit under --strict exit = %d, want 0 (id3-multi-value is not a loss)", code)
		}
	})

	// M4B chapters carry to FLAC in full; --strict gate is loss, not format change.
	t.Run("a full carry succeeds with and without --strict", func(t *testing.T) {
		if _, _, code := runCLI(t, "copy", sampleM4B, copyFixture(t, notagsFLAC)); code != 0 {
			t.Errorf("m4b->flac carry exit = %d, want 0", code)
		}
		if _, _, code := runCLI(t, "copy", "--strict", sampleM4B, copyFixture(t, notagsFLAC)); code != 0 {
			t.Errorf("m4b->flac carry under --strict exit = %d, want 0 (nothing was lost)", code)
		}
	})
}

// WAV rewrite keeps first LIST/INFO; --strict refuses when second held unique content.
func TestStrictCatchesDroppedDuplicateTagBlock(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		dup      [][2]string
		wantExit int
	}{
		{"different value for the same key", [][2]string{{"INAM", "OddTitle"}}, 2},
		{"value the edit itself writes", [][2]string{{"INAM", "Written"}}, 0}, // edit stores duplicate value
		{"key the survivor does not hold", [][2]string{{"IART", "Ghost Artist"}}, 2},
		{"redundant subset", [][2]string{{"INAM", "First"}}, 0},
		{"empty duplicate", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			file := writeTempFile(t, "dup.wav", wavTwoInfoLists(c.dup))
			args := []string{"set", "--strict", "--set", "ALBUM=Anything", file}
			if c.name == "value the edit itself writes" {
				args = []string{"set", "--strict", "--set", "TITLE=Written", file}
			}
			_, errb, code := runCLI(t, args...)
			if code != c.wantExit {
				t.Errorf("exit = %d, want %d: %s", code, c.wantExit, errb)
			}
			if c.wantExit == 2 && !strings.Contains(errb, "duplicate tag chunk held content no other container does") {
				t.Errorf("strict refusal did not say what the write destroys:\n%s", errb)
			}
		})
	}
}

func TestDuplicateTagBlockDropWarnsWithoutStrict(t *testing.T) {
	t.Parallel()
	file := writeTempFile(t, "dup.wav", wavTwoInfoLists([][2]string{{"INAM", "OddTitle"}}))
	out, errb, code := runCLI(t, "set", "--set", "ALBUM=Anything", file)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, errb)
	}
	if !strings.Contains(out, "duplicate-tag-block-dropped") {
		t.Errorf("the plan did not report the dropped duplicate:\n%s", out)
	}
}

// Two LIST/INFO chunks; only first survives rewrite.
func wavTwoInfoLists(dup [][2]string) []byte {
	info := func(pairs [][2]string) []byte {
		body := []byte("INFO")
		for _, p := range pairs {
			body = append(body, wavItem(p[0], p[1])...)
		}
		return wavChunk("LIST", body)
	}
	return wavWrap(slices.Concat(wavFmtChunk(),
		info([][2]string{{"INAM", "First"}}), info(dup), wavChunk("data", make([]byte, 4000))))
}

func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every discard warning must escalate under --strict; enumerates full WarningCode space.
func TestDiscardSetIsStrictSubset(t *testing.T) {
	// Walk full WarningCode space (uint8 wrap ends loop).
	for c := wl.WarningCode(1); c != 0; c++ {
		if wl.IsDiscardWarning(c) && !strictEscalatingCodes[c] {
			t.Errorf("%v is a discard but --strict does not escalate it", c)
		}
	}
	// Whole-item losses: nothing stored.
	for _, c := range []wl.WarningCode{
		wl.WarnValueDropped, wl.WarnLegacyStripDropped, wl.WarnDuplicateTagBlockDropped,
		wl.WarnSyncedLyricsUnsupported, wl.WarnPictureUnsupported, wl.WarnChaptersUnsupported,
		wl.WarnPictureSelectorMiss,
	} {
		if !wl.IsDiscardWarning(c) {
			t.Errorf("%v means the item was not stored at all; it must count as a discard", c)
		}
	}
	// Partial losses keep the item; not discards even if named *Dropped.
	for _, c := range []wl.WarningCode{
		wl.WarnPictureMetadataDropped, wl.WarnCommentDescriptionDropped, wl.WarnChapterEndsDropped,
		wl.WarnChapterMetadataDropped, wl.WarnSyncedLyricsMetadataDropped,
		wl.WarnSyncedLyricsLineDropped, wl.WarnTagStructureDropped,
		wl.WarnValueCoerced, wl.WarnValueReduced, wl.WarnSingleValuedMulti, wl.WarnNumericGenre,
		wl.WarnChapterTitleTruncated, wl.WarnChapterStartOverflow,
		wl.WarnSyncedLyricsTimestampClamped, wl.WarnSyncedLyricsTruncated,
		wl.WarnDuplicateTagBlock,
	} {
		if wl.IsDiscardWarning(c) {
			t.Errorf("%v keeps the item in an altered or partial form; it is not a discard", c)
		}
	}
}

// Discarded edit (WebM picture) must not say "already up to date".
func TestDiscardedEditNotReportedAsUpToDate(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, "../../testdata/sample.webm")
	png := writeTempImage(t, "cover.png", minimalPNG())

	out, errb, code := runCLI(t, "set", file, "--add-picture", "front-cover="+png)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, errb)
	}
	if strings.Contains(out, "already up to date") {
		t.Errorf("a discarded edit must not report the file as already up to date:\n%s", out)
	}
	if !strings.Contains(out, "the edit was discarded") {
		t.Errorf("the plan line did not say the edit was discarded:\n%s", out)
	}
	if !strings.Contains(out, "Edit discarded;") {
		t.Errorf("the save outcome did not say the edit was discarded:\n%s", out)
	}
	if !strings.Contains(out, "picture-unsupported") {
		t.Errorf("the warning that explains the discard is missing:\n%s", out)
	}
}

// Genuine no-op keeps "already up to date" wording.
func TestCleanNoOpStillReportsUpToDate(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	out, _, code := runCLI(t, "set", file, "--set", "TITLE=Original Title")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "already up to date") {
		t.Errorf("a clean no-op must still report the file as already up to date:\n%s", out)
	}
}

// Empty GENRE on ID3 drops stub TCON silently; other text fields keep present-empty.
func TestEmptyGenreDroppedOnID3(t *testing.T) {
	t.Parallel()
	t.Run("id3-backed formats drop and report it", func(t *testing.T) {
		t.Parallel()
		for _, fix := range []string{"notags.mp3", "notags.aac", "notags.aiff"} {
			file := copyFixture(t, filepath.Join("..", "..", "testdata", fix))
			out, _, code := runCLI(t, "set", file, "--set", "GENRE=")
			if code != 0 {
				t.Fatalf("%s: exit = %d, want 0", fix, code)
			}
			if !strings.Contains(out, "value-dropped") {
				t.Errorf("%s: the dropped empty genre was not reported:\n%s", fix, out)
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("TCON")) {
				t.Errorf("%s: a stub TCON frame was written for an empty genre", fix)
			}
			if _, _, code := runCLI(t, "set", copyFixture(t, filepath.Join("..", "..", "testdata", fix)),
				"--strict", "--set", "GENRE="); code != 2 {
				t.Errorf("%s: --strict exit = %d, want 2", fix, code)
			}
		}
	})
	t.Run("other text fields still store a present empty", func(t *testing.T) {
		t.Parallel()
		file := copyFixture(t, filepath.Join("..", "..", "testdata", "notags.mp3"))
		if _, _, code := runCLI(t, "set", file, "--set", "TITLE=", "-q"); code != 0 {
			t.Fatalf("set TITLE= exit %d", code)
		}
		if v := tagValues(decodeJSONOne[jsonDocument](t, mustDumpJSON(t, file)), "TITLE"); len(v) != 1 || v[0] != "" {
			t.Errorf("TITLE = %v, want the present-empty value every other format keeps", v)
		}
	})
}
