package main

import (
	"fmt"
	"os"
	"strings"

	wl "github.com/colespringer/waxlabel"
)

// splitSyncedLyric parses an --add-synced-lyric "TIMESTAMP=Text" assignment. The
// timestamp is read from the text before the first '=', and everything after it is the
// lyric text, including an empty string or additional '=' characters. It mirrors
// splitChapter and reuses the shared [H:]MM:SS[.mmm] timestamp grammar, so a timestamp
// copied from dump output parses back to the same instant.
func splitSyncedLyric(s string) (wl.SyncedLine, error) {
	i := strings.IndexByte(s, '=')
	if i < 0 {
		return wl.SyncedLine{}, usagef("missing '=' in %q (want TIMESTAMP=Text, e.g. 1:30=Verse)", s)
	}
	start, err := parseChapterTimestamp(s[:i])
	if err != nil {
		// Reword the shared timestamp parser's "chapter" noun for the synced-lyric path, so a
		// bad --add-synced-lyric timestamp does not report an "invalid chapter timestamp".
		return wl.SyncedLine{}, usagef("invalid synced-lyric timestamp %q (want [H:]MM:SS[.mmm] or seconds, e.g. 1:30 or 90)", s[:i])
	}
	text := s[i+1:]
	if err := checkArgText(text, "synced-lyric text"); err != nil {
		return wl.SyncedLine{}, err
	}
	return wl.SyncedLine{Time: start, Text: text}, nil
}

// syncedLyricsAdds resolves --synced-lyrics-file and --add-synced-lyric into the single
// synced-lyrics set requested by the CLI, tagged with --synced-lyrics-lang. Authoring
// synced lyrics replaces the destination's existing synced lyrics with this one set,
// unlike --add-chapter, which appends. Merging individual lines into one of several
// existing sets would be ambiguous because synced-lyrics sets are keyed differently by
// native stores. The LRC file is read and timestamps are validated once, before any target
// file is parsed, so bad input is reported once for the whole invocation. Returns nil when
// neither authoring flag was given. droppedLines carries the 1-based --synced-lyrics-file line
// numbers the parser dropped (no timed lyric, not recognized LRC structure), so a per-file
// warning can surface the partial input loss rather than let it pass silently at exit 0.
func (e *editFlags) syncedLyricsAdds() (sets []wl.SyncedLyrics, droppedLines []int, err error) {
	// Validate the author-provided language once for the whole run, alongside the timestamps,
	// but only when lyrics are actually being authored: a bare --synced-lyrics-lang alongside
	// --clear-synced-lyrics tags nothing, so its value is unused and must not fail the run. A
	// typo such as ISO-639-1 "en" or "english" is then one usage error, not a per-file failure.
	// ISO-639-2 codes are three letters; the SYLT field is a fixed three bytes, so a shorter or
	// longer value would be padded or truncated.
	authoring := e.syncedLyricsFile != "" || len(e.addSyncedLyric) > 0
	if authoring && e.syncedLyricsLang != "" && !validLanguageCode(e.syncedLyricsLang) {
		return nil, nil, usagef("--synced-lyrics-lang %q must be 3 ASCII letters (e.g. eng)", e.syncedLyricsLang)
	}
	var lines []wl.SyncedLine
	if e.syncedLyricsFile != "" {
		if err := checkRegularFile(e.syncedLyricsFile, false); err != nil {
			return nil, nil, fmt.Errorf("--synced-lyrics-file: %w", err)
		}
		data, err := os.ReadFile(e.syncedLyricsFile)
		if err != nil {
			return nil, nil, fmt.Errorf("--synced-lyrics-file: %s: %w", e.syncedLyricsFile, err)
		}
		content := string(data)
		// Validate the file content at the boundary, like the argv author-text inputs, so a NUL
		// byte (valid UTF-8, missed by a UTF-8-only check) or invalid UTF-8 in the LRC is a usage
		// error (exit 2) rather than deferring to the library's exit-4 corrupt-media backstop.
		if err := checkArgText(content, "--synced-lyrics-file: "+e.syncedLyricsFile); err != nil {
			return nil, nil, err
		}
		// Parse the file uncapped: the content is already fully in memory, so the line count
		// is bounded by the file size. Delivering every line lets the write-time cap in the
		// library truncate and warn once (visible to --json and --strict), rather than this
		// read silently dropping lines past the cap. The reporting variant also returns the line
		// numbers dropped for having no timed lyric, so a partial-LRC drop is surfaced rather than silent.
		fileLines, dropped := wl.ParseLRCReportFull(content)
		if len(fileLines) == 0 {
			return nil, nil, usagef("--synced-lyrics-file: %s: no timed lyric lines found (want LRC lines like [00:12.00]Text)", e.syncedLyricsFile)
		}
		lines = append(lines, fileLines...)
		droppedLines = dropped
	}
	for _, s := range e.addSyncedLyric {
		ln, err := splitSyncedLyric(s)
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, ln)
	}
	if len(lines) == 0 {
		return nil, nil, nil
	}
	// Store one set containing every authored line. The library sorts by timestamp.
	// Lowercase the language here so the model, JSON dump, and encoded ISO-639-2
	// bytes agree; validation has already limited the input to ASCII letters.
	return []wl.SyncedLyrics{{Language: strings.ToLower(e.syncedLyricsLang), Lines: lines}}, droppedLines, nil
}

// validLanguageCode reports whether s is exactly three ASCII letters, the shape of an
// ISO-639-2 language code and the only form the ID3 SYLT 3-byte field stores without
// padding or truncation. Input is case-insensitive and is stored in lowercase. A full
// ISO registry lookup is intentionally out of scope; the storage field can only preserve
// three single-byte letters.
func validLanguageCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}
