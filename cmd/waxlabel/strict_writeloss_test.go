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

// TestStrictExcludedAndCarryUnaffected guards the Finding 2 boundary from the CLI side: an
// excluded code (id3-multi-value, a value stored in full) still exits 0 under --strict, and a
// lossy copy is unaffected because copy exposes no --strict flag.
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

	// copy exposes no --strict flag, so the broadened family adds no escalation surface there; a
	// genuinely lossy carry (an M4B's chapters -> FLAC drops the ends) still succeeds, and
	// passing --strict to copy is an unknown-flag usage error rather than a silently-honored gate.
	t.Run("copy has no strict flag and a lossy carry succeeds", func(t *testing.T) {
		flac := copyFixture(t, notagsFLAC)
		if _, _, code := runCLI(t, "copy", sampleM4B, flac); code != 0 {
			t.Errorf("lossy m4b->flac carry exit = %d, want 0", code)
		}
		if _, _, code := runCLI(t, "copy", "--strict", sampleM4B, copyFixture(t, notagsFLAC)); code == 0 {
			t.Error("copy --strict should be an unknown-flag usage error, not accepted")
		}
	})
}
