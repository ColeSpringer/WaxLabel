package vorbis

import (
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
)

// Synced lyrics: one SYNCEDLYRICS comment with an LRC document (foobar2000;
// FLAC and Ogg). Structured, not a custom tag. Replaced only by a synced-lyrics
// edit; otherwise preserved including malformed. LRC has no language/descriptor.

// syncedLyricsName is the comment name owning the LRC document.
const syncedLyricsName = "SYNCEDLYRICS"

// isSyncedLyricsComment reports whether a comment name is the owned synced-lyrics comment,
// so [Project] can exclude it from the generic tag view and [Rebuild] can drop it on a
// synced-lyrics edit. The match is case-insensitive, like the chapter-comment check.
func isSyncedLyricsComment(name string) bool {
	return strings.EqualFold(name, syncedLyricsName)
}

// ProjectSyncedLyrics decodes the first SYNCEDLYRICS comment holding a parseable LRC
// document into one synced-lyrics set (the LRC store holds one). A value with no timed
// line is skipped so a later valid one can project, but every SYNCEDLYRICS comment is
// owned: unrelated edits preserve them, a synced-lyrics edit replaces them. Returns nil
// when none carries timed lines.
func ProjectSyncedLyrics(comments []Comment) []core.SyncedLyrics {
	sets, _ := ProjectSyncedLyricsReport(comments)
	return sets
}

// ProjectSyncedLyricsReport is [ProjectSyncedLyrics] plus a [core.WarnSyncedLyricsTruncated]
// when the LRC document exceeded the line cap and lines past it were dropped on read. The
// FLAC and Ogg parse paths use it; the write re-projection uses [ProjectSyncedLyrics].
func ProjectSyncedLyricsReport(comments []Comment) ([]core.SyncedLyrics, []core.Warning) {
	for _, cm := range comments {
		if cm.Unseparated {
			continue // no name to match the SYNCEDLYRICS convention against
		}
		if !isSyncedLyricsComment(cm.Name) {
			continue
		}
		lines, truncated := core.ParseLRCReport(core.SanitizeUTF8(cm.Value))
		if len(lines) == 0 {
			continue
		}
		// LRC carries no language or descriptor; only the timed lines survive.
		var ws []core.Warning
		if truncated {
			ws = []core.Warning{{Code: core.WarnSyncedLyricsTruncated,
				Message: fmt.Sprintf("a SYNCEDLYRICS comment carried more than %d synced-lyric lines; the lines past the limit were dropped on read", core.MaxSyncedLines)}}
		}
		return []core.SyncedLyrics{{Lines: lines}}, ws
	}
	return nil, nil
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
