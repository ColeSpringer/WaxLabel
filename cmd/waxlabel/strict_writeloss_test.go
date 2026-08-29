package main

import (
	"path/filepath"
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
