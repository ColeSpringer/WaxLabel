package core

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SyncedLyrics is a single timed-lyrics set: a sequence of text lines anchored to playback
// offsets, plus descriptor metadata when the native store carries it. It is the
// synchronized counterpart to the unsynchronized [tag.Lyrics] field and is format-neutral:
// an ID3v2 SYLT frame (MP3/AAC/AIFF/WAV) and a SYNCEDLYRICS Vorbis comment containing LRC
// text (FLAC/Ogg) both project into this type.
//
// A file may carry more than one set (a SYLT per language), so [Media.SyncedLyrics]
// is a slice. Callers outside core must use keyed fields, such as
// SyncedLyrics{Language: "eng", Lines: ls}, so new fields can be added without
// breaking positional literals.
type SyncedLyrics struct {
	// Language is the lyrics' language as an ISO-639-2 code (the SYLT 3-byte language), for
	// example "eng". Empty means unspecified. The VorbisComment LRC store has no language
	// field, so it drops this (see [SyncedLyricsLossLanguage]).
	Language string
	// Description is the SYLT content descriptor, a short label distinguishing one set from
	// another. It round-trips through SYLT; the LRC store drops it.
	Description string
	// Lines are the timed text lines, in playback order (sorted by Time on edit and on
	// projection). A line may carry empty Text: an LRC clear marker that blanks the
	// display at its timestamp.
	Lines []SyncedLine

	// _ makes positional construction a compile error in other packages, enforcing the
	// keyed-field contract so a later field can be added without breaking literals.
	// SyncedLyrics also contains a slice, so use [EqualSyncedLyrics] for equality.
	_ struct{}
}

// SyncedLine is one timed lyric line: its text and the playback offset it appears at.
// Both fields are comparable, so a SyncedLine compares with == and a line slice with
// slices.Equal, which [EqualSyncedLyrics] relies on.
type SyncedLine struct {
	// Time is the line's offset from the start of the media.
	Time time.Duration
	// Text is the line's lyric text. Empty is meaningful: a clear marker.
	Text string
}

// SyncedLyricsLoss names synced-lyrics metadata a destination format cannot preserve.
// It is recorded on the synced-lyrics [Capability] so transfer reports and direct-edit
// warnings share the [SyncedLyricsLoseMetadata] predicate, matching [ChapterLoss] for
// chapters.
type SyncedLyricsLoss uint8

const (
	// SyncedLyricsLossNone means the format preserves the per-set language and descriptor
	// along with the timed text (ID3v2 SYLT).
	SyncedLyricsLossNone SyncedLyricsLoss = iota
	// SyncedLyricsLossLanguage means the format stores the timed text only, dropping the
	// per-set language and descriptor. The VorbisComment LRC convention (FLAC/Ogg) uses
	// this model: an LRC document has timestamps and text but no language or descriptor.
	SyncedLyricsLossLanguage
)

// SyncedLyricsLoseMetadata reports whether writing sls to a destination with loss would
// discard metadata present in sls. Transfers and direct-edit warnings share this
// predicate, so they classify the same sets as lossy.
//
// For [SyncedLyricsLossLanguage] (VorbisComment LRC), the timed lines survive, but a
// non-empty per-set language or descriptor is lost because LRC has no field for it - and an
// embedded line break in a line's text is flattened to a space by the LRC store (FormatLRC), a
// silent content change, so a set carrying one is a lossy carry too.
func SyncedLyricsLoseMetadata(sls []SyncedLyrics, loss SyncedLyricsLoss) bool {
	for _, sl := range sls {
		if SyncedLyricsSetLosesMetadata(sl, loss) {
			return true
		}
	}
	return false
}

// SyncedLyricsSetLosesMetadata reports whether writing one set sl to a destination with loss would
// discard its metadata. SyncedLyricsLoseMetadata is the fold of this over all sets, so the whole-set
// and per-set graders cannot drift; the per-set form lets a transfer report grade each set carried
// vs lossy, mirroring the per-picture ([pictureLosesMetadata]) and per-chapter ([ChapterLosesMetadata])
// splits.
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

// SyncedLyricsMetadataDroppedMessage returns the edit-time warning text for the fields the
// VorbisComment LRC convention cannot preserve. There is only one lossy synced-lyrics
// variant, so the helper takes no loss argument.
func SyncedLyricsMetadataDroppedMessage() string {
	return "LRC synced lyrics store timed text only; the per-set language, descriptor, and embedded line breaks are dropped"
}

// SyncedLyricsClampOverflows reports whether any line in sls carries a timestamp past max, so
// the destination's writer would clamp it (SYLT's 32-bit ms field, or the LRC store's
// MaxLRCTime). max == 0 means no limit (nothing overflows). The comparison is strictly
// greater, matching the writers' clamp predicates (a line exactly at max round-trips). It is
// the synced-lyrics analogue of the chapter-title byte-cap check, letting a transfer grade a
// clamping copy Lossy instead of a clean carry.
func SyncedLyricsClampOverflows(sls []SyncedLyrics, max time.Duration) bool {
	for _, sl := range sls {
		if SyncedLyricsSetClampOverflows(sl, max) {
			return true
		}
	}
	return false
}

// SyncedLyricsSetClampOverflows reports whether one set sl carries a line timestamp past max.
// SyncedLyricsClampOverflows is the fold of this over all sets, letting a transfer grade each set's
// clamp loss independently. max == 0 means no limit (nothing overflows).
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

// EqualSyncedLyrics reports whether two synced-lyrics slices are identical by content,
// including order. SyncedLyrics contains a slice, so it is not comparable with ==; this
// compares each set's Language, Description, and Lines element-wise. It is the
// synced-lyrics analogue of [EqualChapters], so codecs can detect synced-lyrics edits the
// same way they detect chapter edits.
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

// CloneSyncedLyrics returns an independent deep copy. SyncedLyrics contains a Lines slice,
// so each set's lines are copied to detach it fully, unlike [CloneChapters], whose element
// is value-only. It returns nil for nil input so a document with no synced lyrics keeps
// that shape on round-trip.
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

// MaxSyncedLines caps how many timed lines one [ParseLRC] call accumulates, a
// defense-in-depth bound against a hostile SYNCEDLYRICS comment packed with minimal
// timestamp tags. The cap is far past any real song's line count. The ID3 SYLT decoder
// caps its own line count separately through the element-cap machinery.
const MaxSyncedLines = 1 << 16

// maxLRCField bounds an LRC hour, minute, or second field before it can overflow a
// time.Duration. It is set well below the point where the assembled hour+minute+second
// nanosecond product could overflow int64 (MaxInt64/(time.Hour+time.Minute+time.Second) is
// ~2.5e6), so even maxLRCField in every field stays in range. The ceiling is still far past
// any real timestamp (the hours field alone reaches ~239 years), so a genuine timestamp
// parses while an absurd one is treated as a non-time tag and skipped rather than wrapping
// to an invalid, often negative, duration.
const maxLRCField = 1 << 21

// MaxLRCTime is the largest line offset [FormatLRC] can emit and [ParseLRC] read back: past
// it the minute-normalized value fails to re-parse (parseLRCTime rejects it), so the
// VorbisComment synced-lyrics writer clamps to it and warns rather than writing a timestamp
// that is silently dropped on the next read. It is the same ceiling applyLRCOffset enforces.
const MaxLRCTime = time.Duration(maxLRCField) * time.Minute

// ClampLRCTime clamps a synced-lyric line offset to the round-trippable range, reporting
// whether it changed the value. The VorbisComment writer uses it so an over-range edited
// timestamp is stored at the ceiling (and warned) instead of written past what ParseLRC
// accepts. It clamps only the upper bound, which is the sole case that fails to re-parse and
// so warrants the overflow warning. A negative offset is not an overflow: formatLRCTime
// already renders it as zero and ParseLRC reads zero back, so it round-trips faithfully with
// no clamp reported - flagging it here would emit a "timestamp exceeded the limit" warning
// that does not describe a below-zero value (matching chapterComments, which likewise leaves
// negatives to its formatter).
func ClampLRCTime(d time.Duration) (time.Duration, bool) {
	if d > MaxLRCTime {
		return MaxLRCTime, true
	}
	return d, false
}

// maxLRCOffsetMs bounds an LRC [offset:] magnitude (in milliseconds) so applying it cannot
// overflow a time.Duration. It is far past any real offset (a few seconds), so a legitimate
// offset always applies; an absurd one is clamped rather than wrapped.
const maxLRCOffsetMs = 1 << 40

// ParseLRC parses an LRC document into timed lyric lines, applying the foobar2000
// [offset:] convention and skipping the metadata tags ([ar:], [ti:], [al:], [length:],
// and any other non-timestamp tag). It is the shared reader behind the VorbisComment
// SYNCEDLYRICS store and the CLI's --synced-lyrics-file input, so both interpret an LRC
// file identically.
//
// Each leading [mm:ss.xx] or [mm:ss.mmm] tag on a line yields one line at that
// timestamp carrying the line's remaining text; a line with several leading time tags
// (a repeated chorus) yields one line per tag. Collection of leading tags stops at the
// first bracket group that is not a timestamp, such as a metadata tag or a section marker
// like "[Chorus]" that belongs to the lyric text. This preserves a lyric line whose text
// begins with a non-timestamp "[...]". The fractional second is scaled by its digit
// count (".5" is 500 ms, ".05" is 50 ms) and truncated to milliseconds, so the centisecond
// LRC convention and the millisecond form WaxLabel emits both parse. A line with a
// timestamp but no text is kept as an empty-text clear marker. Lines are returned sorted by
// timestamp (stably, preserving file order among equal times) and capped at
// [MaxSyncedLines]. A document with no timestamped line yields nil.
//
// WaxLabel separates a line's timestamp from its text with one space (see [FormatLRC]) and
// strips exactly that space here, so its own output round-trips even when the text is itself a
// literal [mm:ss.xx]-shaped string: the space stops timestamp collection, so the text is not
// re-read as a second time tag. LRC has no escape mechanism, so the residual ambiguity is an
// externally-authored line that abuts a [mm:ss.xx]-shaped text directly against its timestamp
// with no separator ("[00:01.00][00:02.00]x"); that reads as two time tags, as it would in any
// LRC player. Every other text, including section markers, round-trips. A file already corrupted
// by WaxLabel's old no-separator writer stays corrupted - this prevents new corruption, it cannot
// recover a phantom line already baked into existing bytes.
//
// [offset:N] (milliseconds, optionally signed) shifts every timestamp by the
// foobar2000 rule, effective timestamp = timestamp - offset, clamped at zero. The first
// offset tag wins, whether on its own line or inline before a stamp, and is applied to
// every line.
// The sign is implementation-specific across LRC players; WaxLabel pins it to foobar2000
// and round-trips its own output losslessly (it emits no offset). A leading UTF-8 BOM (from
// a Windows editor) is stripped so the first line is not lost.
func ParseLRC(text string) []SyncedLine {
	lines, _ := ParseLRCReport(text)
	return lines
}

// ParseLRCReport is [ParseLRC] plus a truncation flag: truncated is true when the input
// carried more than the modeled per-set line cap (MaxSyncedLines) and a timed line past it
// was dropped. The public [ParseLRC] wraps it and discards the flag to keep its
// func(string) []SyncedLine signature; the VorbisComment read path uses this variant to
// surface a [WarnSyncedLyricsTruncated] instead of capping silently. truncated is false
// for a set that fits (trailing blank lines past a full set do not count, since none was
// dropped).
func ParseLRCReport(text string) (lines []SyncedLine, truncated bool) {
	text = strings.TrimPrefix(text, "\ufeff")
	// Normalize CRLF and lone-CR (classic-Mac) line endings to LF before splitting, so a
	// pure-CR file is not read as one giant line with every lyric concatenated.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var out []SyncedLine
	var offsetMs int64
	hasOffset := false
	for _, raw := range strings.Split(text, "\n") {
		times, lineOffset, lineHasOffset, body := leadingTimestamps(raw)
		if lineHasOffset && !hasOffset {
			offsetMs, hasOffset = lineOffset, true
		}
		for _, d := range times {
			if len(out) >= MaxSyncedLines {
				// The set is full but a timed line remains: it is dropped, so flag the
				// truncation rather than returning a short set silently. Breaking here (and out
				// of the outer loop below) also stops scanning a pathological over-cap file.
				truncated = true
				break
			}
			out = append(out, SyncedLine{Time: d, Text: body}) // raw time; the offset is applied below
		}
		if truncated {
			break
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	// Apply the document offset after collection so a tag found on a later line still shifts
	// the earlier lines, then sort.
	if hasOffset {
		for i := range out {
			out[i].Time = applyLRCOffset(out[i].Time, offsetMs)
		}
	}
	slices.SortStableFunc(out, func(a, b SyncedLine) int { return cmp.Compare(a.Time, b.Time) })
	return out, truncated
}

// FormatLRC renders timed lyric lines as an LRC document: one "[mm:ss.mmm] text" line per
// [SyncedLine], in the given order, joined by newlines. A single space separates the timestamp
// from non-empty text and [ParseLRC] strips it back, so the millisecond timestamp makes a
// round-trip through ParseLRC lossless for every line - including one whose text is itself a
// literal [mm:ss.xx]-shaped string, which the separator disambiguates (see ParseLRC). The minute
// field widens past two digits for a long track, and an empty-text line emits a bare timestamp
// (a clear marker). The per-set language and descriptor are not representable in LRC and are not
// emitted (see [SyncedLyricsLossLanguage]).
func FormatLRC(lines []SyncedLine) string {
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(formatLRCTime(ln.Time))
		// Separate the timestamp from the text with exactly one space, and only for non-empty
		// text (an empty-text clear marker stays a bare timestamp). leadingTimestamps strips this
		// one space back off on read, so "[00:05.000] [x]" round-trips even when the text is
		// itself a literal [..]-shaped string: the space stops timestamp collection, so the text
		// is no longer re-read as a second time tag. Without it the two abut and corrupt.
		if text := flattenLRCText(ln.Text); text != "" {
			b.WriteByte(' ')
			b.WriteString(text)
		}
	}
	return b.String()
}

// lrcLineBreaks replaces an embedded line break in a line's text with a single space.
var lrcLineBreaks = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")

// flattenLRCText neutralizes an embedded CR/LF in a line's text. The LRC store is
// line-based: a literal newline would be read back as a record separator, splitting the
// line and silently dropping everything after the break (the timestamp anchors only the
// first physical line). A SyncedLine is one line by definition, so a stray break is
// flattened to a space rather than allowed to corrupt the document. The ID3 SYLT store,
// whose entries are NUL-terminated, preserves embedded newlines and needs no flattening.
func flattenLRCText(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return lrcLineBreaks.Replace(s)
}

// formatLRCTime renders a line offset as [mm:ss.mmm]. A negative offset clamps to zero;
// the minute field is not bounded to two digits, so a long track renders correctly.
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

// leadingTimestamps scans the bracket groups at the start of a line. It collects
// [mm:ss.xx] timestamps, records the first inline [offset:N], and skips that offset so a
// following timestamp on the same line is still read. Collection stops at the first group
// that is not a timestamp or offset, such as [ar:] metadata or a "[Chorus]" marker that
// belongs to the lyric text. It also stops at a non-bracket character or an unclosed
// bracket, preserving lyric text instead of swallowing it as a tag.
func leadingTimestamps(line string) (times []time.Duration, offsetMs int64, hasOffset bool, rest string) {
	s := line
	for len(s) > 0 && s[0] == '[' {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			break // unclosed bracket: the remainder is text
		}
		inner := s[1:end]
		if d, ok := parseLRCTime(inner); ok {
			times = append(times, d)
		} else if o, ok := parseLRCOffsetTag(inner); ok {
			if !hasOffset {
				offsetMs, hasOffset = o, true
			}
			// an inline [offset:N] is skipped so a following timestamp is still collected
		} else {
			break // a metadata tag or a section marker in the lyric text; the text starts here
		}
		s = s[end+1:]
	}
	// Strip exactly one space between the consumed timestamp group(s) and the text - the
	// separator FormatLRC now emits - so "[00:05.000] hi" reads back "hi", not " hi". Scoped to a
	// line that actually yielded a timestamp: a plain-text or metadata line (no timestamp
	// consumed) keeps its leading whitespace untouched. TrimPrefix removes at most one space, so
	// a lyric whose text legitimately begins with a space (written as two spaces) keeps the rest,
	// and an external no-space "[00:05.000]hi" is unaffected.
	if len(times) > 0 {
		s = strings.TrimPrefix(s, " ")
	}
	return times, offsetMs, hasOffset, s
}

// parseLRCOffsetTag parses an [offset:N] tag's inner content, returning the millisecond
// offset (clamped so applying it cannot overflow a time.Duration) and whether it matched.
// The clamp uses int64 so the maxLRCOffsetMs constant does not overflow a 32-bit int on a
// 386 build. Surrounding whitespace and case are tolerated.
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

// applyLRCOffset shifts a timestamp by the foobar2000 rule: effective timestamp =
// timestamp - offsetMs. The result is clamped to the round-trippable range. A negative
// result clamps to zero; a result past maxLRCField minutes clamps to it, so a large
// negative offset cannot push a line past the minute bound parseLRCTime enforces. Without
// that cap, FormatLRC could emit a value ParseLRC then rejects.
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

// parseLRCTime parses an LRC timestamp tag's inner content: "mm:ss[.fff]" or the optional
// three-part "hh:mm:ss[.fff]" some long files use. Surrounding whitespace is tolerated
// ("[ 00:12.00 ]"), the whole-number fields are all-digit, and the fractional second is
// scaled by its digit count and truncated to milliseconds. It returns false for any other
// content (a metadata tag such as "ar:Artist", or a malformed timestamp) so the caller
// treats that group as metadata or lyric text rather than a line anchor. The minute field
// may exceed 59 (the standard form for a long track is a large minute count, not hours).
func parseLRCTime(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false // need at least MM:SS; at most HH:MM:SS
	}
	// The last component is the (optionally fractional) seconds; the components before it
	// are whole minutes and, for a three-part value, whole hours.
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
	// Seconds are a real seconds value (< 60) in every form: "[00:99.00]" is malformed, not 99 s.
	if secs >= 60 {
		return 0, false
	}
	// Minutes are bounded < 60 only in the three-part HH:MM:SS form; the two-part MM:SS form
	// legitimately uses a large minute count for a long track ("[100:00.000]"), which FormatLRC
	// emits and the round-trip guard below re-accepts - so it must not be capped at 59 here.
	if len(parts) == 3 && mins >= 60 {
		return 0, false
	}
	// Bound the remaining unbounded fields below the point where the assembled nanosecond product
	// could overflow a time.Duration (see maxLRCField): the two-part minute count and the hours.
	if hours > maxLRCField || mins > maxLRCField || secs > maxLRCField {
		return 0, false
	}
	d := time.Duration(hours)*time.Hour + time.Duration(mins)*time.Minute +
		time.Duration(secs)*time.Second + time.Duration(ms)*time.Millisecond
	// FormatLRC re-emits the total as a single minute count, so reject a value whose minute
	// form would itself exceed maxLRCField and fail to re-parse. This keeps ParseLRC and
	// FormatLRC a true inverse pair (an hours value normalizes to minutes on round-trip).
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

// AllASCIIDigits reports whether s is non-empty and entirely ASCII digits. It is the one shared
// digit check for the codecs (the LRC timestamp fields here, the Vorbis chapter/LRC parser, and
// the Matroska cover-name suffix), so the same rule cannot drift between packages.
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
