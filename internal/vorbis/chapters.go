package vorbis

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
)

// Chapters in Vorbis comments follow the de-facto CHAPTERxxx convention (foobar2000,
// shared by FLAC and Ogg): a CHAPTERxxx comment holds a chapter's start as HH:MM:SS.mmm
// and an optional CHAPTERxxxNAME comment holds its title. WaxLabel treats these comments
// as structured chapters, not editable custom tag fields. They are replaced only by a
// chapter edit and otherwise preserved byte-for-byte, including malformed entries. The
// model is start+title only; FLAC CUESHEET blocks are preserved but not projected.
//
// On write WaxLabel emits the common-writer form: 1-based, 3-digit numbers (CHAPTER001).
// On read it accepts any digit count and 0- or 1-based numbering.

// chapterNamePrefix is the comment-name prefix for both the timestamp (CHAPTERxxx) and
// the title (CHAPTERxxxNAME) comments.
const chapterNamePrefix = "CHAPTER"

// parseChapterName splits a CHAPTERxxx / CHAPTERxxxNAME comment name into its numeric
// index and whether it is the NAME (title) half. ok is false for any other name, including
// a CHAPTER prefix with no digits ("CHAPTERS") or a different field, so a genuine custom
// tag is never mistaken for a chapter comment.
func parseChapterName(name string) (index int, isTitle bool, ok bool) {
	up := strings.ToUpper(name)
	rest, found := strings.CutPrefix(up, chapterNamePrefix)
	if !found {
		return 0, false, false
	}
	if r, isName := strings.CutSuffix(rest, "NAME"); isName {
		rest, isTitle = r, true
	}
	if rest == "" || !isAllDigits(rest) {
		return 0, false, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false, false
	}
	return n, isTitle, true
}

// isChapterComment reports whether a comment name is an owned chapter comment
// (CHAPTERxxx or CHAPTERxxxNAME), so [Project] can exclude it from the generic tag view
// and [Rebuild] can drop it on a chapter edit.
func isChapterComment(name string) bool {
	_, _, ok := parseChapterName(name)
	return ok
}

// ProjectChapters decodes the CHAPTERxxx/CHAPTERxxxNAME comments into an ordered chapter
// list ordered by chapter number. A comment with no parseable timestamp contributes no
// chapter. A stray CHAPTERxxxNAME with no CHAPTERxxx is not a chapter, but it is still
// owned: unrelated edits preserve it, and chapter edits replace it with the edited set.
// Returns nil when none.
func ProjectChapters(comments []Comment) []core.Chapter {
	type entry struct {
		start    time.Duration
		title    string
		hasStart bool
	}
	byIndex := map[int]*entry{}
	var order []int
	for _, cm := range comments {
		idx, isTitle, ok := parseChapterName(cm.Name)
		if !ok {
			continue
		}
		e := byIndex[idx]
		if e == nil {
			e = &entry{}
			byIndex[idx] = e
			order = append(order, idx)
		}
		if isTitle {
			e.title = core.SanitizeUTF8(cm.Value)
		} else if d, ok := parseChapterTime(cm.Value); ok {
			e.start, e.hasStart = d, true
		}
	}
	sort.Ints(order)
	var chs []core.Chapter
	for _, idx := range order {
		if e := byIndex[idx]; e.hasStart {
			chs = append(chs, core.Chapter{Start: e.start, Title: e.title})
		}
	}
	return chs
}

// chapterComments renders a chapter list as CHAPTERxxx (+ optional CHAPTERxxxNAME)
// comments in the common-writer form: 1-based, 3-digit numbers. A chapter with an empty
// title emits no CHAPTERxxxNAME, so it round-trips to a titleless chapter rather than an
// empty-string title.
func chapterComments(chs []core.Chapter) []Comment {
	out := make([]Comment, 0, len(chs))
	for i, ch := range chs {
		num := fmt.Sprintf("%s%03d", chapterNamePrefix, i+1)
		out = append(out, Comment{Name: num, Value: formatChapterTime(ch.Start)})
		if ch.Title != "" {
			out = append(out, Comment{Name: num + "NAME", Value: ch.Title})
		}
	}
	return out
}

// formatChapterTime renders a chapter offset as HH:MM:SS.mmm (millisecond precision, the
// CHAPTERxxx convention). A negative offset clamps to zero.
func formatChapterTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	ms := d / time.Millisecond
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}

// parseChapterTime parses a CHAPTERxxx timestamp leniently: [[HH:]MM:]SS[.fff], where the
// hour and minute fields, when present, are all-digit and the fractional second is scaled
// by its digit count (".5" is 500 ms, ".05" is 50 ms, ".050" is 50 ms) and truncated to
// millisecond precision. It returns false for a malformed value so the caller preserves
// the comment verbatim rather than minting a bogus chapter.
func parseChapterTime(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0, false
	}
	var h, m int
	secPart := parts[len(parts)-1]
	if len(parts) >= 2 {
		v, ok := parseUint(parts[len(parts)-2])
		if !ok {
			return 0, false
		}
		m = v
	}
	if len(parts) == 3 {
		v, ok := parseUint(parts[0])
		if !ok {
			return 0, false
		}
		h = v
	}
	secStr, fracStr, _ := strings.Cut(secPart, ".")
	sec, ok := parseUint(secStr)
	if !ok {
		return 0, false
	}
	ms := 0
	if fracStr != "" {
		if !isAllDigits(fracStr) {
			return 0, false
		}
		for len(fracStr) < 3 {
			fracStr += "0"
		}
		ms, _ = strconv.Atoi(fracStr[:3])
	}
	// Reject absurd values before they can overflow time.Duration. The generous final
	// ceiling is far past any real chapter; beyond it the comment is treated as malformed
	// and preserved through unrelated edits.
	const maxField = 1 << 32 // keeps h*3600 + m*60 + sec inside int64
	if int64(h) > maxField || int64(m) > maxField || int64(sec) > maxField {
		return 0, false
	}
	totalSec := int64(h)*3600 + int64(m)*60 + int64(sec)
	const maxChapterSec = int64(1_000_000) * 3600
	if totalSec > maxChapterSec {
		return 0, false
	}
	d := time.Duration(totalSec)*time.Second + time.Duration(ms)*time.Millisecond
	return d, true
}

// parseUint parses an all-digit string as a non-negative int. It rejects empty input and
// any sign or non-digit, unlike strconv.Atoi which accepts a leading "+"/"-".
func parseUint(s string) (int, bool) {
	if !isAllDigits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// isAllDigits reports whether s is non-empty and entirely ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
