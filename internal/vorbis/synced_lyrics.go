package vorbis

import (
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
)

// Synchronized lyrics in Vorbis comments follow the de facto convention of a single
// SYNCEDLYRICS comment holding an LRC document (foobar2000, shared by FLAC and Ogg).
// WaxLabel treats it as structured synced lyrics, not an editable custom tag field: it is
// replaced only by a synced-lyrics edit and otherwise preserved byte-for-byte, including a
// malformed value. LRC has no language or descriptor field, so those are dropped (see
// [core.SyncedLyricsLossLanguage]); the timed lines round-trip losslessly through the
// shared [core.ParseLRC]/[core.FormatLRC].

// syncedLyricsName is the comment name owning the LRC document.
const syncedLyricsName = "SYNCEDLYRICS"

// isSyncedLyricsComment reports whether a comment name is the owned synced-lyrics comment,
// so [Project] can exclude it from the generic tag view and [Rebuild] can drop it on a
// synced-lyrics edit. The match is case-insensitive, like the chapter-comment check.
func isSyncedLyricsComment(name string) bool {
	return strings.EqualFold(name, syncedLyricsName)
}

// ProjectSyncedLyrics decodes the first SYNCEDLYRICS comment that holds a parseable LRC
// document into one synced-lyrics set. The LRC store holds one set. A SYNCEDLYRICS value
// with no timed line is skipped so a later valid one can still project, but every
// SYNCEDLYRICS comment is owned by this model: unrelated edits preserve them, and a
// synced-lyrics edit replaces them. Returns nil when none carries timed lines.
func ProjectSyncedLyrics(comments []Comment) []core.SyncedLyrics {
	for _, cm := range comments {
		if !isSyncedLyricsComment(cm.Name) {
			continue
		}
		if lines := core.ParseLRC(core.SanitizeUTF8(cm.Value)); len(lines) > 0 {
			// LRC carries no language or descriptor; only the timed lines survive.
			return []core.SyncedLyrics{{Lines: lines}}
		}
	}
	return nil
}

// syncedLyricsComments renders synced-lyrics sets as a single SYNCEDLYRICS comment holding
// the first set's lines as LRC (the store holds one set). A set with no lines emits no
// comment, so it round-trips to no synced lyrics rather than an empty comment. A line
// timestamp past the LRC ceiling is clamped to it (reported via the returned bool) so an
// over-range edited line is stored at the ceiling rather than written unreadably and
// silently dropped on the next parse.
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
	// At least one line is over the ceiling: clamp into a copy so the caller's input is not
	// mutated, then render.
	clamped := make([]core.SyncedLine, len(lines))
	for i, ln := range lines {
		ln.Time, _ = core.ClampLRCTime(ln.Time)
		clamped[i] = ln
	}
	return []Comment{{Name: syncedLyricsName, Value: core.FormatLRC(clamped)}}, true
}

// SyncedLyricsCapability is the synced-lyrics capability shared by FLAC and Ogg. The LRC
// store holds one set (MaxItems 1) and cannot store the per-set language or descriptor
// (SyncedLyricsLossLanguage), so a transfer of a SYLT set carrying either field is graded
// Lossy. The shared helper keeps FLAC and Ogg identical by construction.
func SyncedLyricsCapability() core.Capability {
	return core.Capability{
		Read:             core.AccessFull,
		Write:            core.AccessFull,
		Representation:   "SYNCEDLYRICS comment (LRC)",
		Fidelity:         "timed text stored; per-set language and descriptor dropped",
		MaxItems:         1,
		SyncedLyricsLoss: core.SyncedLyricsLossLanguage,
	}
}
