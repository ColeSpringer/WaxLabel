package core

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SyncedLyrics is one timed-lyrics set (lines plus optional descriptor metadata).
// Format-neutral: ID3 SYLT and Vorbis SYNCEDLYRICS/LRC both project here.
// Files may hold multiple sets; callers outside core must use keyed fields.
type SyncedLyrics struct {
	// Language is ISO-639-2 (SYLT). Empty means unspecified. LRC drops it ([SyncedLyricsLossLanguage]).
	Language string
	// Description is the SYLT content descriptor. LRC drops it.
	Description string
	// Lines in playback order. Empty Text is an LRC clear marker.
	Lines []SyncedLine

	// _ blocks positional literals outside core. Compare with [EqualSyncedLyrics].
	_ struct{}
}

// SyncedLine is one timed lyric line. Comparable with ==.
type SyncedLine struct {
	// Time is the line's offset from the start of the media.
	Time time.Duration
	// Text is the line's lyric text. Empty is meaningful: a clear marker.
	Text string
}

// SyncedLyricsLoss names metadata a destination cannot preserve. On the synced-lyrics
// [Capability]; [SyncedLyricsLoseMetadata] is shared by transfers and edit warnings.
type SyncedLyricsLoss uint8

const (
	// SyncedLyricsLossNone: language, descriptor, and lines preserved (ID3 SYLT).
	SyncedLyricsLossNone SyncedLyricsLoss = iota
	// SyncedLyricsLossLanguage: timed text only (Vorbis LRC).
	SyncedLyricsLossLanguage
)

// SyncedLyricsLoseMetadata reports whether writing sls with loss drops metadata they carry.
//
// [SyncedLyricsLossLanguage]: language, descriptor, and embedded line breaks in text are lost
// (LRC flattens breaks to spaces).
func SyncedLyricsLoseMetadata(sls []SyncedLyrics, loss SyncedLyricsLoss) bool {
	for _, sl := range sls {
		if SyncedLyricsSetLosesMetadata(sl, loss) {
			return true
		}
	}
	return false
}

// SyncedLyricsSetLosesMetadata is the per-set form; [SyncedLyricsLoseMetadata] folds it.
func SyncedLyricsSetLosesMetadata(sl SyncedLyrics, loss SyncedLyricsLoss) bool {
	if loss != SyncedLyricsLossLanguage {
		return false
	}
	if sl.Language != "" || sl.Description != "" {
		return true
	}
	for _, ln := range sl.Lines {
		if strings.ContainsAny(ln.Text, "\r\n") {
			return true
		}
	}
	return false
}

// SyncedLyricsMetadataDroppedMessage is edit-time text for LRC metadata loss.
func SyncedLyricsMetadataDroppedMessage() string {
	return "LRC synced lyrics store timed text only; the per-set language, descriptor, and embedded line breaks are dropped"
}

// SyncedLyricsUnsupportedMessage is for formats with no synced-lyrics store.
func SyncedLyricsUnsupportedMessage(f Format) string {
	return fmt.Sprintf("%s %s file cannot store synced lyrics; the set was dropped", IndefiniteArticle(f.String()), f)
}

// SyncedLyricsTruncatedMessage is edit-time text when a set exceeds [MaxSyncedLines] on write.
func SyncedLyricsTruncatedMessage() string {
	return fmt.Sprintf("the authored synced-lyrics set carried more than %d lines; the lines past the limit were dropped", MaxSyncedLines)
}

// TruncateSyncedLyrics caps each set at [MaxSyncedLines]. Does not mutate input.
// Over-cap sets truncate with a warning; in-cap sets share their Lines slice unchanged.
func TruncateSyncedLyrics(sls []SyncedLyrics) (out []SyncedLyrics, truncated bool) {
	out = make([]SyncedLyrics, 0, len(sls))
	for _, sl := range sls {
		if len(sl.Lines) > MaxSyncedLines {
			sl.Lines = slices.Clone(sl.Lines[:MaxSyncedLines])
			truncated = true
		}
		out = append(out, sl)
	}
	return out, truncated
}

// SyncedLyricsClampOverflows reports whether any line time exceeds max (writers clamp).
// max==0 means no limit. Strictly greater than max, matching writer predicates.
func SyncedLyricsClampOverflows(sls []SyncedLyrics, max time.Duration) bool {
	for _, sl := range sls {
		if SyncedLyricsSetClampOverflows(sl, max) {
			return true
		}
	}
	return false
}

// SyncedLyricsSetClampOverflows is the per-set form; max==0 means no limit.
func SyncedLyricsSetClampOverflows(sl SyncedLyrics, max time.Duration) bool {
	if max <= 0 {
		return false
	}
	for _, ln := range sl.Lines {
		if ln.Time > max {
			return true
		}
	}
	return false
}

// EqualSyncedLyrics compares synced-lyrics slices by content and order.
func EqualSyncedLyrics(a, b []SyncedLyrics) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Language != b[i].Language || a[i].Description != b[i].Description {
			return false
		}
		if !slices.Equal(a[i].Lines, b[i].Lines) {
			return false
		}
	}
	return true
}

// CloneSyncedLyrics deep-copies sets and their Lines. Nil in, nil out.
func CloneSyncedLyrics(sls []SyncedLyrics) []SyncedLyrics {
	if sls == nil {
		return nil
	}
	out := make([]SyncedLyrics, len(sls))
	for i, sl := range sls {
		sl.Lines = slices.Clone(sl.Lines)
		out[i] = sl
	}
	return out
}

// MaxSyncedLines caps lines per [ParseLRC] call (hostile-input bound). SYLT has a separate cap.
const MaxSyncedLines = 1 << 16

// maxLRCField bounds LRC time fields below int64 overflow in duration assembly.
const maxLRCField = 1 << 21

// MaxLRCTime is the largest round-trippable LRC offset ([FormatLRC]/[ParseLRC]).
const MaxLRCTime = time.Duration(maxLRCField) * time.Minute

// ClampLRCTime clamps d to [MaxLRCTime], upper bound only. Negatives round-trip as zero.
func ClampLRCTime(d time.Duration) (time.Duration, bool) {
	if d > MaxLRCTime {
		return MaxLRCTime, true
	}
	return d, false
}

// maxLRCOffsetMs bounds [offset:] magnitude so applyLRCOffset cannot overflow.
const maxLRCOffsetMs = 1 << 40

// ParseLRC parses LRC into timed lines. Skips metadata tags; applies foobar2000 [offset:].
// Leading [mm:ss.xx] or [mm:ss.mmm] tags yield one line each; stops at non-timestamp brackets.
// Fractional seconds scale by digit count, truncated to ms. Empty text after a stamp is a clear marker.
// Sorted stably by time, capped at [MaxSyncedLines]. No timestamps yields nil.
//
// [FormatLRC] emits one space after the stamp; ParseLRC strips it for round-trip.
// Abutted stamp+text with no separator reads as multiple tags (LRC has no escape).
//
// First [offset:N] wins (ms, signed). effective = timestamp - offset, clamped at zero.
// Strips UTF-8 BOM.
func ParseLRC(text string) []SyncedLine {
	lines, _ := ParseLRCReport(text)
	return lines
}

// ParseLRCReport is [ParseLRC] plus truncated when input exceeded [MaxSyncedLines].
func ParseLRCReport(text string) (lines []SyncedLine, truncated bool) {
	lines, truncated, _ = parseLRC(text, MaxSyncedLines)
	return lines, truncated
}

// ParseLRCFull is uncapped [ParseLRC] for trusted whole-file input (CLI). Media uses capped parse.
func ParseLRCFull(text string) []SyncedLine {
	lines, _, _ := parseLRC(text, 0)
	return lines
}

// ParseLRCReportFull is uncapped parse plus 1-based droppedLines for unrecognized content.
// CLI warns; --strict fails.
func ParseLRCReportFull(text string) (lines []SyncedLine, droppedLines []int) {
	lines, _, droppedLines = parseLRC(text, 0)
	return lines, droppedLines
}

// parseLRC is the shared reader. lineCap<=0 disables cap. dropped lists unrecognized lines.
func parseLRC(text string, lineCap int) (lines []SyncedLine, truncated bool, dropped []int) {
	text = strings.TrimPrefix(text, "\ufeff")
	// Normalize CRLF and lone CR to LF before splitting.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var out []SyncedLine
	var offsetMs int64
	hasOffset := false
	for i, raw := range strings.Split(text, "\n") {
		times, lineOffset, lineHasOffset, body := leadingTimestamps(raw)
		if lineHasOffset && !hasOffset {
			offsetMs, hasOffset = lineOffset, true
		}
		// Unrecognized non-structure line: count as dropped.
		if len(times) == 0 && countsAsDroppedLRCLine(raw) {
			dropped = append(dropped, i+1)
		}
		for _, d := range times {
			if lineCap > 0 && len(out) >= lineCap {
				// Cap reached; flag truncation and stop scanning.
				truncated = true
				break
			}
			out = append(out, SyncedLine{Time: d, Text: body})
		}
		if truncated {
			break
		}
	}
	if len(out) == 0 {
		return nil, false, dropped
	}
	// Apply document offset after collecting all lines, then sort.
	if hasOffset {
		for i := range out {
			out[i].Time = applyLRCOffset(out[i].Time, offsetMs)
		}
	}
	slices.SortStableFunc(out, func(a, b SyncedLine) int { return cmp.Compare(a.Time, b.Time) })
	return out, truncated, dropped
}

// lrcIDTagPrefixes are ID metadata tags whose lines carry no timed lyric.
var lrcIDTagPrefixes = []string{"ar:", "ti:", "al:", "au:", "by:", "re:", "ve:", "length:"}

// countsAsDroppedLRCLine reports unrecognized content on a line with no timed lyric.
// Blank lines, ID tags, [offset:], and [length:] are not dropped.
func countsAsDroppedLRCLine(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if trimmed[0] != '[' {
		return true
	}
	end := strings.IndexByte(trimmed, ']')
	if end < 0 {
		return true
	}
	// Whole-line bracket only; "[Chorus] text" is dropped content.
	if end != len(trimmed)-1 {
		return true
	}
	inner := trimmed[1:end]
	if _, ok := parseLRCTime(inner); ok {
		return false
	}
	if _, ok := parseLRCOffsetTag(inner); ok {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(inner))
	for _, p := range lrcIDTagPrefixes {
		if strings.HasPrefix(lower, p) {
			return false
		}
	}
	return true
}

// FormatLRC renders lines as "[mm:ss.mmm] text", one space before non-empty text.
// Empty text emits a bare timestamp (clear marker). Language and descriptor are omitted (LRC).
func FormatLRC(lines []SyncedLine) string {
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(formatLRCTime(ln.Time))
		// One space before non-empty text so ParseLRC does not re-read text as a stamp.
		if text := flattenLRCText(ln.Text); text != "" {
			b.WriteByte(' ')
			b.WriteString(text)
		}
	}
	return b.String()
}

// lrcLineBreaks replaces an embedded line break in a line's text with a single space.
var lrcLineBreaks = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")

// flattenLRCText replaces embedded CR/LF with spaces (LRC is line-based). SYLT keeps newlines.
func flattenLRCText(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return lrcLineBreaks.Replace(s)
}

// formatLRCTime renders [mm:ss.mmm]. Negative clamps to zero; minutes may exceed 59.
func formatLRCTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	ms := d / time.Millisecond
	return fmt.Sprintf("[%02d:%02d.%03d]", m, s, ms)
}

// leadingTimestamps collects leading [mm:ss] stamps and the first [offset:N] on a line.
// Stops at metadata, section markers, or non-bracket text.
func leadingTimestamps(line string) (times []time.Duration, offsetMs int64, hasOffset bool, rest string) {
	s := line
	for len(s) > 0 && s[0] == '[' {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			break
		}
		inner := s[1:end]
		if d, ok := parseLRCTime(inner); ok {
			times = append(times, d)
		} else if o, ok := parseLRCOffsetTag(inner); ok {
			if !hasOffset {
				offsetMs, hasOffset = o, true
			}
		} else {
			break
		}
		s = s[end+1:]
	}
	// Strip one space after stamps (FormatLRC separator). Only when timestamps were consumed.
	if len(times) > 0 {
		s = strings.TrimPrefix(s, " ")
	}
	return times, offsetMs, hasOffset, s
}

// parseLRCOffsetTag parses [offset:N] inner content (ms, clamped). Case and space tolerant.
func parseLRCOffsetTag(inner string) (int64, bool) {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(inner)), "offset:")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	if err != nil {
		return 0, false
	}
	return min(max(n, -maxLRCOffsetMs), maxLRCOffsetMs), true
}

// applyLRCOffset applies foobar2000: timestamp - offsetMs, clamped to [0, MaxLRCTime].
func applyLRCOffset(d time.Duration, offsetMs int64) time.Duration {
	d -= time.Duration(offsetMs) * time.Millisecond
	if d < 0 {
		return 0
	}
	if d > MaxLRCTime {
		return MaxLRCTime
	}
	return d
}

// parseLRCTime parses "mm:ss[.fff]" or "hh:mm:ss[.fff]". Fraction scaled by digit count to ms.
// Two-part form allows minutes >59 for long tracks.
func parseLRCTime(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	secStr, fracStr, _ := strings.Cut(parts[len(parts)-1], ".")
	secs, ok := lrcUint(secStr)
	if !ok {
		return 0, false
	}
	mins, ok := lrcUint(parts[len(parts)-2])
	if !ok {
		return 0, false
	}
	hours := 0
	if len(parts) == 3 {
		if hours, ok = lrcUint(parts[0]); !ok {
			return 0, false
		}
	}
	ms := 0
	if fracStr != "" {
		if !AllASCIIDigits(fracStr) {
			return 0, false
		}
		for len(fracStr) < 3 {
			fracStr += "0"
		}
		ms, _ = strconv.Atoi(fracStr[:3])
	}
	// secs must be <60 in all forms.
	if secs >= 60 {
		return 0, false
	}
	// mins capped at 59 only in three-part form.
	if len(parts) == 3 && mins >= 60 {
		return 0, false
	}
	// Bound fields below duration overflow (maxLRCField).
	if hours > maxLRCField || mins > maxLRCField || secs > maxLRCField {
		return 0, false
	}
	d := time.Duration(hours)*time.Hour + time.Duration(mins)*time.Minute +
		time.Duration(secs)*time.Second + time.Duration(ms)*time.Millisecond
	// Reject values whose minute form exceeds maxLRCField (FormatLRC round-trip).
	if d/time.Minute > time.Duration(maxLRCField) {
		return 0, false
	}
	return d, true
}

// lrcUint parses an all-digit string as a non-negative int, rejecting empty input, a
// sign, or any non-digit.
func lrcUint(s string) (int, bool) {
	if !AllASCIIDigits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// AllASCIIDigits reports non-empty all-ASCII-digit s. Shared digit check for codecs.
func AllASCIIDigits(s string) bool {
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
