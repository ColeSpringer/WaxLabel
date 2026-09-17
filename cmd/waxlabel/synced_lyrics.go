package main

import (
	"fmt"
	"os"
	"strings"

	wl "github.com/colespringer/waxlabel"
)

// splitSyncedLyric parses --add-synced-lyric "TIMESTAMP=Text": timestamp before
// the first '=', text (possibly empty or containing '=') after. Mirrors
// splitChapter; same [H:]MM:SS[.mmm] grammar so dump timestamps round-trip.
func splitSyncedLyric(s string) (wl.SyncedLine, error) {
	i := strings.IndexByte(s, '=')
	if i < 0 {
		return wl.SyncedLine{}, usagef("missing '=' in %q (want TIMESTAMP=Text, e.g. 1:30=Verse)", s)
	}
	start, err := parseChapterTimestamp(s[:i])
	if err != nil {
		// Reword the shared parser's "chapter" noun for this flag.
		return wl.SyncedLine{}, usagef("invalid synced-lyric timestamp %q (want [H:]MM:SS[.mmm] or seconds, e.g. 1:30 or 90)", s[:i])
	}
	text := s[i+1:]
	if err := checkArgText(text, "synced-lyric text"); err != nil {
		return wl.SyncedLine{}, err
	}
	return wl.SyncedLine{Time: start, Text: text}, nil
}

// syncedLyricsAdds resolves --synced-lyrics-file and --add-synced-lyric into one
// set tagged with --synced-lyrics-lang. Authoring replaces existing synced lyrics
// (unlike --add-chapter, which appends): merging into one of several native sets
// would be ambiguous. LRC and timestamps are validated once before any target
// parse. Nil when neither authoring flag is set. droppedLines are 1-based LRC
// lines dropped (no timed lyric / unrecognized), for a per-file warning.
func (e *editFlags) syncedLyricsAdds() (sets []wl.SyncedLyrics, droppedLines []int, err error) {
	// Validate language once for the run, only when authoring: bare
	// --synced-lyrics-lang with --clear-synced-lyrics tags nothing. ISO-639-2 is
	// three letters; SYLT is three bytes, so other lengths would pad/truncate.
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
		// Boundary check like argv text: NUL (valid UTF-8) or invalid UTF-8 is
		// usage (exit 2), not the library's exit-4 corrupt-media backstop.
		if err := checkArgText(content, "--synced-lyrics-file: "+e.syncedLyricsFile); err != nil {
			return nil, nil, err
		}
		// Uncapped parse: content is already in memory. Library write-time cap
		// truncates and warns once (--json/--strict) instead of silently dropping.
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
	// One set of every authored line; library sorts by timestamp. Lowercase lang
	// so model, JSON, and ISO-639-2 bytes agree (already ASCII-letter validated).
	return []wl.SyncedLyrics{{Language: strings.ToLower(e.syncedLyricsLang), Lines: lines}}, droppedLines, nil
}

// validLanguageCode reports exactly three ASCII letters (ISO-639-2 / SYLT shape).
// Case-insensitive; stored lowercase. No registry lookup: the field holds three
// single-byte letters only.
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
