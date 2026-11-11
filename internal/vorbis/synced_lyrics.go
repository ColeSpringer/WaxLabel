package vorbis

import (
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
)

// Synced lyrics: one SYNCEDLYRICS comment with an LRC document (foobar2000;
// FLAC and Ogg). Structured, not a custom tag. Replaced only by a synced-lyrics
// edit; otherwise preserved, a malformed one too until a clear of the synced lyrics
// or of its name removes it (owned.go). LRC has no language/descriptor.

// syncedLyricsName is the comment name owning the LRC document.
const syncedLyricsName = "SYNCEDLYRICS"

// isSyncedLyricsComment reports whether a comment name is the owned synced-lyrics comment,
// so [Project] can exclude it from the generic tag view and [Rebuild] can drop it on a
// synced-lyrics edit. The match takes any ASCII case, like the chapter-comment check.
func isSyncedLyricsComment(name string) bool {
	return equalFoldASCII(name, syncedLyricsName)
}

// equalFoldASCII reports whether name is upper, an uppercase ASCII name, in any ASCII case.
// Unicode case folding would also match a long s or a Kelvin sign, which no other reader of the
// file takes for the name.
func equalFoldASCII(name, upper string) bool {
	if len(name) != len(upper) {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c != upper[i] {
			return false
		}
	}
	return true
}

// readSyncedLyrics finds the SYNCEDLYRICS comment that is read, the first holding a timed line
// (the LRC store holds one set): its index, -1 for none, its lines, and whether the document
// held more lines than the cap. A value with no timed line is passed over so that a later one
// can be read. With owned given it marks every SYNCEDLYRICS comment but the one read.
func readSyncedLyrics(comments []Comment, owned []unreadKind) (at int, lines []core.SyncedLine, truncated bool) {
	at = -1
	for i, cm := range comments {
		if cm.Unseparated || !isSyncedLyricsComment(cm.Name) {
			continue
		}
		if at < 0 {
			// LRC carries no language or descriptor; only the timed lines survive.
			if l, cut := core.ParseLRCReport(core.SanitizeUTF8(cm.Value)); len(l) > 0 {
				at, lines, truncated = i, l, cut
				continue
			}
		}
		if owned != nil {
			owned[i] = unreadLyrics
		}
	}
	return at, lines, truncated
}

// ProjectSyncedLyrics decodes the first SYNCEDLYRICS comment holding a parseable LRC
// document into one synced-lyrics set (the LRC store holds one). Every SYNCEDLYRICS comment
// is owned: unrelated edits preserve them, a synced-lyrics edit replaces them. Returns nil
// when none carries timed lines. The parse sites use [ProjectOwned], which also reports.
func ProjectSyncedLyrics(comments []Comment) []core.SyncedLyrics {
	_, lines, _ := readSyncedLyrics(comments, nil)
	return syncedLyricsOf(lines)
}

func syncedLyricsOf(lines []core.SyncedLine) []core.SyncedLyrics {
	if len(lines) == 0 {
		return nil
	}
	return []core.SyncedLyrics{{Lines: lines}}
}

// syncedLyricsComments renders the first set's lines as one SYNCEDLYRICS comment in LRC
// (the store holds one set). A set with no lines emits no comment, so it round-trips to
// no synced lyrics. A timestamp past the LRC ceiling is clamped to it (reported via the
// returned bool) so the next parse does not drop the line.
func syncedLyricsComments(sls []core.SyncedLyrics) ([]Comment, bool) {
	if len(sls) == 0 || len(sls[0].Lines) == 0 {
		return nil, false
	}
	lines := sls[0].Lines
	// Common case: nothing overflows, so render the lines directly with no copy.
	overflow := false
	for _, ln := range lines {
		if ln.Time > core.MaxLRCTime {
			overflow = true
			break
		}
	}
	if !overflow {
		return []Comment{{Name: syncedLyricsName, Value: core.FormatLRC(lines)}}, false
	}
	// Clamp into a copy so the caller's input is not mutated.
	clamped := make([]core.SyncedLine, len(lines))
	for i, ln := range lines {
		ln.Time, _ = core.ClampLRCTime(ln.Time)
		clamped[i] = ln
	}
	return []Comment{{Name: syncedLyricsName, Value: core.FormatLRC(clamped)}}, true
}

// SyncedLyricsCapability is the synced-lyrics capability shared by FLAC and Ogg. The LRC
// store holds one set (MaxItems 1) and no per-set language or descriptor
// (SyncedLyricsLossLanguage), so a transfer of a SYLT set carrying either is Lossy.
func SyncedLyricsCapability() core.Capability {
	return core.Capability{
		Read:             core.AccessFull,
		Write:            core.AccessFull,
		Representation:   "SYNCEDLYRICS comment (LRC)",
		Fidelity:         "timed text stored; per-set language and descriptor dropped",
		Constraints:      []string{fmt.Sprintf("at most %d synced-lyric lines (lines past the cap are dropped on read)", core.MaxSyncedLines)},
		MaxItems:         1,
		SyncedLyricsLoss: core.SyncedLyricsLossLanguage,
		// Write clamps a line past this ceiling (see ClampLRCTime above); expose it so a
		// transfer grades a clamping copy Lossy.
		SyncedLyricsTimeMax: core.MaxLRCTime,
	}
}
