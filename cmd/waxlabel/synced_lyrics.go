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
		return wl.SyncedLine{}, err
	}
	return wl.SyncedLine{Time: start, Text: s[i+1:]}, nil
}

// syncedLyricsAdds resolves --synced-lyrics-file and --add-synced-lyric into the single
// synced-lyrics set requested by the CLI, tagged with --synced-lyrics-lang. Authoring
// synced lyrics replaces the destination's existing synced lyrics with this one set,
// unlike --add-chapter, which appends. Merging individual lines into one of several
// existing sets would be ambiguous because synced-lyrics sets are keyed differently by
// native stores. The LRC file is read and timestamps are validated once, before any target
// file is parsed, so bad input is reported once for the whole invocation. Returns nil when
// neither authoring flag was given.
func (e *editFlags) syncedLyricsAdds() ([]wl.SyncedLyrics, error) {
	// Validate the author-provided language once for the whole run, alongside the
	// timestamps. A typo such as ISO-639-1 "en" or "english" should be one usage error, not
	// a per-file failure. ISO-639-2 codes are three letters; the SYLT field is a fixed three
	// bytes, so a shorter or longer value would be padded or truncated.
	if e.syncedLyricsLang != "" && !validLanguageCode(e.syncedLyricsLang) {
		return nil, usagef("--synced-lyrics-lang %q must be a 3-letter ISO-639-2 code (e.g. eng)", e.syncedLyricsLang)
	}
	var lines []wl.SyncedLine
	if e.syncedLyricsFile != "" {
		if err := checkRegularFile(e.syncedLyricsFile, false); err != nil {
			return nil, fmt.Errorf("--synced-lyrics-file: %w", err)
		}
		data, err := os.ReadFile(e.syncedLyricsFile)
		if err != nil {
			return nil, fmt.Errorf("--synced-lyrics-file: %s: %w", e.syncedLyricsFile, err)
		}
		fileLines := wl.ParseLRC(string(data))
		if len(fileLines) == 0 {
			return nil, usagef("--synced-lyrics-file: %s: no timed lyric lines found (want LRC lines like [00:12.00]Text)", e.syncedLyricsFile)
		}
		lines = append(lines, fileLines...)
	}
	for _, s := range e.addSyncedLyric {
		ln, err := splitSyncedLyric(s)
		if err != nil {
			return nil, err
		}
		lines = append(lines, ln)
	}
	if len(lines) == 0 {
		return nil, nil
	}
	// Store one set containing every authored line. The library sorts by timestamp.
	// Lowercase the language here so the model, JSON dump, and encoded ISO-639-2
	// bytes agree; validation has already limited the input to ASCII letters.
	return []wl.SyncedLyrics{{Language: strings.ToLower(e.syncedLyricsLang), Lines: lines}}, nil
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
