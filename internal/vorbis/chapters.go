package vorbis

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
)

// Chapters use the CHAPTERxxx convention (foobar2000; FLAC and Ogg). Structured
// chapters (start+title), not custom tags. Replaced only by a chapter edit;
// otherwise preserved including malformed. CUESHEET preserved but not projected.
//
// Write: 1-based 3-digit (CHAPTER001). Read: any digit count, 0- or 1-based.

// chapterNamePrefix is the comment-name prefix for both the timestamp (CHAPTERxxx) and
// the title (CHAPTERxxxNAME) comments.
const chapterNamePrefix = "CHAPTER"

// MaxChapters is the most chapters the 3-digit CHAPTERxxx namespace holds: a
// 1000-entry list numbers CHAPTER000..CHAPTER999, and one more would need a 4-digit
// key no other reader recognizes. VorbisComment formats enforce it through
// Capabilities.Chapters.MaxItems.
const MaxChapters = 1000

// maxChapterSec is the largest whole-second chapter offset parseChapterTime accepts
// (1,000,000 hours). Past it a comment reads as malformed, so the writer clamps an
// over-range chapter start to it (and warns) instead of writing a value the next
// parse would drop.
const maxChapterSec = int64(1_000_000) * 3600

// maxChapterDuration is that ceiling as a Duration, the value chapterComments clamps to.
const maxChapterDuration = time.Duration(maxChapterSec) * time.Second

// parseChapterName splits a CHAPTERxxx / CHAPTERxxxNAME comment name into its numeric
// index and whether it is the NAME (title) half. ok is false for any other name,
// including a CHAPTER prefix with no digits ("CHAPTERS"), so a custom tag is never
// mistaken for a chapter comment.
func parseChapterName(name string) (index int, isTitle bool, ok bool) {
	up := strings.ToUpper(name)
	rest, found := strings.CutPrefix(up, chapterNamePrefix)
	if !found {
		return 0, false, false
	}
	if r, isName := strings.CutSuffix(rest, "NAME"); isName {
		rest, isTitle = r, true
	}
	if rest == "" || !core.AllASCIIDigits(rest) {
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

// ProjectChapters decodes the CHAPTERxxx/CHAPTERxxxNAME comments into a chapter list
// sorted by start. A comment with no parseable timestamp contributes no chapter. A
// stray CHAPTERxxxNAME with no CHAPTERxxx is not a chapter but is still owned:
// unrelated edits preserve it, chapter edits replace it. Returns nil when none.
func ProjectChapters(comments []Comment) []core.Chapter {
	type entry struct {
		start    time.Duration
		title    string
		hasStart bool
	}
	byIndex := map[int]*entry{}
	var order []int
	for _, cm := range comments {
		if cm.Unseparated {
			continue // no name to match the CHAPTERxxx convention against
		}
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
	slices.Sort(order)
	var chs []core.Chapter
	for _, idx := range order {
		if e := byIndex[idx]; e.hasStart {
			chs = append(chs, core.Chapter{Start: e.start, Title: e.title})
		}
	}
	// Sort by start so an out-of-order source (CHAPTER001 later than CHAPTER002)
	// projects in time order and a load->store round-trip is a no-op; the index order
	// above breaks ties for equal-start chapters.
	core.SortChaptersByStart(chs)
	return chs
}

// chapterComments renders a chapter list as CHAPTERxxx (+ optional CHAPTERxxxNAME)
// comments: 3-digit numbers, 1-based below 1000 chapters. A chapter with an empty title
// emits no CHAPTERxxxNAME, so it round-trips to a titleless chapter.
//
// At 1000 or more chapters numbering starts from 0. ffmpeg and ffprobe parse the
// convention with a fixed 3-digit key (CHAPTER%03d), so a 1-based CHAPTER1000 would be
// unreadable there, while CHAPTER000..CHAPTER999 reads in full. Past MaxChapters no
// 3-digit scheme fits; the editor refuses such a list first, so the 4-digit tail this
// loop would emit is defensive only. WaxLabel's own reader accepts any digit count.
func chapterComments(chs []core.Chapter) ([]Comment, bool) {
	out := make([]Comment, 0, len(chs))
	overflow := false
	base := 1
	if len(chs) >= MaxChapters {
		base = 0
	}
	for i, ch := range chs {
		start := ch.Start
		if start > maxChapterDuration {
			start = maxChapterDuration // clamp to the reader's ceiling so it round-trips
			overflow = true
		}
		num := fmt.Sprintf("%s%03d", chapterNamePrefix, base+i)
		out = append(out, Comment{Name: num, Value: formatChapterTime(start)})
		if ch.Title != "" {
			out = append(out, Comment{Name: num + "NAME", Value: ch.Title})
		}
	}
	return out, overflow
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

// parseChapterTime parses a CHAPTERxxx timestamp leniently: [[HH:]MM:]SS[.fff]. Hour
// and minute fields are all-digit; the fraction scales by its digit count (".5" is
// 500 ms, ".05" and ".050" are 50 ms) and truncates to milliseconds. It returns false
// for a malformed value so the caller preserves the comment verbatim.
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
		if !core.AllASCIIDigits(fracStr) {
			return 0, false
		}
		for len(fracStr) < 3 {
			fracStr += "0"
		}
		ms, _ = strconv.Atoi(fracStr[:3])
	}
	// Reject absurd values before they can overflow time.Duration. Past the final
	// ceiling the comment is treated as malformed and preserved through unrelated edits.
	const maxField = 1 << 32 // keeps h*3600 + m*60 + sec inside int64
	if int64(h) > maxField || int64(m) > maxField || int64(sec) > maxField {
		return 0, false
	}
	totalSec := int64(h)*3600 + int64(m)*60 + int64(sec)
	if totalSec > maxChapterSec {
		return 0, false
	}
	d := time.Duration(totalSec)*time.Second + time.Duration(ms)*time.Millisecond
	return d, true
}

// parseUint parses an all-digit string as a non-negative int. It rejects empty input and
// any sign or non-digit, unlike strconv.Atoi which accepts a leading "+"/"-".
func parseUint(s string) (int, bool) {
	if !core.AllASCIIDigits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
