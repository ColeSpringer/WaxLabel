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

// TestStrictEscalatesWriteLossFamily pins the Finding 2 boundary directly on the escalation
// set the gate reads: --strict escalates the whole family of edit-caused write losses, and
// deliberately does NOT escalate codes that are not an edit loss. A code silently added to or
// dropped from the family surfaces here rather than in the field.
func TestStrictEscalatesWriteLossFamily(t *testing.T) {
	escalating := []wl.WarningCode{
		// The original four value/structure losses.
		wl.WarnValueDropped, wl.WarnValueCoerced, wl.WarnSingleValuedMulti, wl.WarnTagStructureDropped,
		// The Finding 2 additions: the rest of the edit-caused write-loss family.
		wl.WarnValueReduced,
		wl.WarnPictureMetadataDropped,
		wl.WarnChapterEndsDropped,
		wl.WarnChapterTitleTruncated,
		wl.WarnChapterStartOverflow,
		wl.WarnChapterMetadataDropped,
		wl.WarnSyncedLyricsMetadataDropped,
		wl.WarnSyncedLyricsTimestampClamped,
		wl.WarnNumericGenre,
		// The write-path half of the duplicate-tag-block pair: this rewrite discarded a
		// duplicate container that held a value the survivor does not.
		wl.WarnDuplicateTagBlockDropped,
	}
	for _, c := range escalating {
		if !strictEscalatingCodes[c] {
			t.Errorf("--strict must escalate %v (an edit-caused write loss)", c)
		}
	}
	// Deliberately excluded: these are not an edit loss, so escalating them would fail
	// --strict on ordinary edits or on pre-existing file state.
	excluded := []wl.WarningCode{
		wl.WarnID3MultiValue,      // the value is fully stored, NUL-separated
		wl.WarnNativeValueReduced, // the full set is kept in the winning container
		wl.WarnChaptersFlattened,  // can describe pre-existing on-read state, not this edit
		wl.WarnPaddingClamped,     // about padding size, not tag content
		// The read-path half: the file holds two containers, which is true before any edit.
		// Its write-path sibling above is what escalates.
		wl.WarnDuplicateTagBlock,
	}
	for _, c := range excluded {
		if strictEscalatingCodes[c] {
			t.Errorf("--strict must NOT escalate %v (not an edit-caused loss)", c)
		}
	}
}

// TestStrictEscalatesNumericGenreCoercion: a bare numeric genre reference stored
// on an ID3-backed format reads back as its genre name; --strict must fail it
// whether or not --numeric-genre is involved, including when the write no-ops
// because the file already projects the coerced name. WAV without an id3 chunk
// keeps the literal text and passes.
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
			// Write it without --strict, then re-apply: the identical loss on a
			// no-op plan must still escalate.
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
	// With --numeric-genre the same loss also trips the capability reduction; the
	// strict error must report it once, through the numeric-genre wording that
	// names the value, not twice.
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

// TestStrictEscalatesNewWriteLossesEndToEnd drives the broadened --strict through the CLI: a
// newly-escalated keyed loss (a value reduced to lower precision) and a newly-escalated keyless
// loss (a chapter title truncated) each fail at exit 2, and the error echoes the plan-body
// message the user also sees.
func TestStrictEscalatesNewWriteLossesEndToEnd(t *testing.T) {
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// value-reduced (keyed): RECORDINGDATE=2021-03 on a fresh MP3 tag (written ID3v2.3) loses
	// the month, since v2.3 date frames need a full day. --strict names the key and the reason.
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

	// value-coerced (keyed): COMPILATION=maybe on an M4A is not a valid boolean, so cpil stores it
	// as 0 (false) rather than dropping it. That is a coercion --strict must catch; the error names
	// the key and the coercion.
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

	// A trkn/disk leading zero or sign is a numerically-lossless canonicalization, not a coercion,
	// so TRACKNUMBER=03 stores 3 without warning and does NOT trip --strict.
	t.Run("number canonicalization is not a strict loss", func(t *testing.T) {
		m4a := copyFixture(t, notagsM4A)
		if _, stderr, code := runCLI(t, "set", m4a, "--set", "TRACKNUMBER=03", "--strict"); code != 0 {
			t.Errorf("TRACKNUMBER=03 --strict exit = %d, want 0 (a leading zero is not a loss): %s", code, stderr)
		}
	})

	// chapter-title-truncated (keyless): a >255-byte chapter title cannot fit MP4's chpl
	// single-byte length prefix, so it is trimmed - a loss --strict must now catch.
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

// TestStrictExcludedAndCarryUnaffected guards the escalation boundary from the CLI side: an
// excluded code (id3-multi-value, a value stored in full) still exits 0 under --strict, and a
// carry the destination stores in full stays clean on both commands.
func TestStrictExcludedAndCarryUnaffected(t *testing.T) {
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// id3-multi-value is not a loss (stored NUL-separated), so an ordinary multi-value MP3 edit
	// must still succeed under --strict.
	t.Run("id3-multi-value still exits 0", func(t *testing.T) {
		mp3 := copyFixture(t, notagsMP3)
		if _, _, code := runCLI(t, "set", mp3, "--set", "ARTIST=A", "--add", "ARTIST=B", "--strict"); code != 0 {
			t.Errorf("multi-value MP3 edit under --strict exit = %d, want 0 (id3-multi-value is not a loss)", code)
		}
	})

	// An M4B's chapters carry to FLAC in full (their run-to-EOF ends are reconstructable, so
	// the transfer grades them Carried), which is the case copy --strict must not fail: the
	// gate is "this transfer lost something", not "this transfer crossed a format boundary".
	t.Run("a full carry succeeds with and without --strict", func(t *testing.T) {
		if _, _, code := runCLI(t, "copy", sampleM4B, copyFixture(t, notagsFLAC)); code != 0 {
			t.Errorf("m4b->flac carry exit = %d, want 0", code)
		}
		if _, _, code := runCLI(t, "copy", "--strict", sampleM4B, copyFixture(t, notagsFLAC)); code != 0 {
			t.Errorf("m4b->flac carry under --strict exit = %d, want 0 (nothing was lost)", code)
		}
	})
}

// TestStrictCatchesDroppedDuplicateTagBlock: a WAV with two LIST/INFO chunks keeps only the
// first on rewrite. When the second holds a value the first does not, that rewrite destroys
// it, so --strict must refuse the file at exit 2; when the second is fully redundant the
// write is silent and exits 0. The read-path duplicate-tag-block warning fires in every case
// and never escalates on its own.
func TestStrictCatchesDroppedDuplicateTagBlock(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		dup      [][2]string
		wantExit int
	}{
		{"different value for the same key", [][2]string{{"INAM", "OddTitle"}}, 2},
		// The write itself now stores the duplicate's value, so nothing dies with it.
		{"value the edit itself writes", [][2]string{{"INAM", "Written"}}, 0},
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

// TestDuplicateTagBlockDropWarnsWithoutStrict: without --strict the same write proceeds and
// reports the loss, so the warning and the strict decision read the same signal.
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

// wavTwoInfoLists builds a WAV holding two LIST/INFO chunks: an authoritative first one with
// INAM=First, and a duplicate carrying the given 4CC/value pairs. Only the first survives a
// rewrite, so what the second holds decides whether that rewrite destroys anything.
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

// writeTempFile writes data to a fresh temp directory and returns its path.
func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDiscardSetIsStrictSubset enumerates every warning code rather than restating the
// discard list, so a code added to either set is checked without editing this test. A discard
// that --strict ignored would let a plan say "the edit was discarded" and still exit 0.
func TestDiscardSetIsStrictSubset(t *testing.T) {
	// Walk the whole code space rather than stopping at a named code or at the first
	// unnamed one: either bound would silently narrow the invariant when a code is appended
	// to the block (or added without a String() case), which is the opposite of what this
	// test is for. WarningCode is a uint8, so c wraps to 0 and ends the loop.
	for c := wl.WarningCode(1); c != 0; c++ {
		if wl.IsDiscardWarning(c) && !strictEscalatingCodes[c] {
			t.Errorf("%v is a discard but --strict does not escalate it", c)
		}
	}
	// The whole-item losses: nothing the user asked for was stored.
	for _, c := range []wl.WarningCode{
		wl.WarnValueDropped, wl.WarnLegacyStripDropped, wl.WarnDuplicateTagBlockDropped,
		wl.WarnSyncedLyricsUnsupported, wl.WarnPictureUnsupported, wl.WarnChaptersUnsupported,
		wl.WarnPictureSelectorMiss,
	} {
		if !wl.IsDiscardWarning(c) {
			t.Errorf("%v means the item was not stored at all; it must count as a discard", c)
		}
	}
	// Partial losses keep the item, so "the edit was discarded" would overstate them - even
	// for the ones named "*Dropped".
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

// TestDiscardedEditNotReportedAsUpToDate is the report's repro: adding cover art to a WebM
// leaves the bytes unchanged because the format cannot store it, so the plan is a no-op - but
// "already up to date" says the file holds what was asked for, which is the opposite of what
// happened. Both the plan line and the save outcome must say the edit was discarded.
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

// TestCleanNoOpStillReportsUpToDate is the other side: a genuine no-op carries no warning and
// must keep its original wording.
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

// TestEmptyGenreDroppedOnID3: --set GENRE= wrote a stub TCON the genre read path then drops,
// so the file grew a frame no reader reports and the loss was silent. The plain text frames
// keep storing a present-empty value, which is the cross-format contract.
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
