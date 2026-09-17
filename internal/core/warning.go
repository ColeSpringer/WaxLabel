package core

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

// WarningCode categorizes non-fatal parse/plan conditions. WaxLabel warns instead of silent loss.
type WarningCode uint8

const (
	WarnUnknown WarningCode = iota
	// WarnStrayLeadingID3: ID3v2 before FLAC marker. Preserved by default. Read path.
	WarnStrayLeadingID3
	// WarnTrailingID3v1: trailing 128-byte ID3v1. Preserved. Read path.
	WarnTrailingID3v1
	// WarnLegacyAPE: APEv2 alongside native tags. Read path.
	WarnLegacyAPE
	// WarnMultipleVorbisComment: extra comment blocks; first wins on rewrite. Read path.
	WarnMultipleVorbisComment
	// WarnInheritedEncoder: transcoder encoder stamp (Lavf/Lavc). Read path.
	WarnInheritedEncoder
	// WarnDistrustedBlockSize: declared block size disagreed with content. Read path.
	WarnDistrustedBlockSize
	// WarnUnknownBlock: unrecognized metadata block preserved verbatim. Read path.
	WarnUnknownBlock
	// WarnInvalidPicture: picture block not fully interpreted. Read path.
	WarnInvalidPicture
	// WarnConflictingFamilies: families disagree on one canonical key. Read path.
	WarnConflictingFamilies
	// WarnNumericGenre: numeric genre mapped to name on read. Read path.
	WarnNumericGenre
	// WarnChainedStream: chained Ogg read best-effort. Read path.
	WarnChainedStream
	// WarnID3MultiValue: multi-value written NUL-separated in ID3v2.3. Write path; not --strict.
	WarnID3MultiValue
	// WarnDuplicateTagBlock: duplicate tag container; first wins on rewrite. Read path.
	WarnDuplicateTagBlock
	// WarnChapterSourceConflict: MP4 chapter representations disagree. Read path.
	WarnChapterSourceConflict
	// WarnChaptersStale: obsolete; chapter edits now rebuild both MP4 stores. Stable surface only.
	WarnChaptersStale
	// WarnChapterTitleTruncated: title trimmed to container limit on write. Plan; keyed N/A.
	WarnChapterTitleTruncated
	// WarnChaptersFlattened: nested/extra chapter structure dropped on projection. Read path.
	WarnChaptersFlattened
	// WarnNoAudioFrames: no decodable audio (tag-only or truncated). Read path.
	WarnNoAudioFrames
	// WarnTruncatedAudio: declared audio extends past file. Read path.
	WarnTruncatedAudio
	// WarnChapterPastDuration: chapter start past duration. Edit or lint. Keyless.
	WarnChapterPastDuration
	// WarnDuplicateChapter: duplicate chapter start times. Edit or lint. Keyless.
	WarnDuplicateChapter
	// WarnSingleValuedMulti: single-valued key holds multiple values after edit. Plan; Warning.Keys.
	WarnSingleValuedMulti
	// WarnDuplicatePicture: edit added duplicate image bytes. Plan; matches linter code.
	WarnDuplicatePicture
	// WarnMultipleFrontCovers: edit added second front cover. Plan; matches linter code.
	WarnMultipleFrontCovers
	// WarnPictureMetadataDropped: destination drops picture role/description. Plan; Warning.Keys.
	WarnPictureMetadataDropped
	// WarnLegacyConflict: edit diverges from preserved legacy container. Plan; remedy --legacy strip.
	WarnLegacyConflict
	// WarnValueDropped: value cannot be encoded; lost on write. Plan; Warning.Keys; --strict.
	WarnValueDropped
	// WarnNativeValueReduced: multi-value reduced in native slot; full set in ID3. Write path.
	WarnNativeValueReduced
	// WarnValueReduced: partial-write fidelity loss when projection differs. Plan; Warning.Keys.
	WarnValueReduced
	// WarnChapterEndsDropped: rewrite dropped explicit chapter ends (Matroska). Plan; keyless.
	WarnChapterEndsDropped
	// WarnPaddingClamped: padding request exceeded format cap. Write path; keyless.
	WarnPaddingClamped
	// WarnTagStructureDropped: edited Matroska tag lost language/binary/nesting. Plan; Warning.Keys; --strict.
	WarnTagStructureDropped
	// WarnChapterStartOverflow: chapter time clamped to 32-bit field. Write path; keyless.
	WarnChapterStartOverflow
	// WarnChapterMetadataDropped: chapter edit loses fields per [ChapterLoss]. Plan; keyless.
	WarnChapterMetadataDropped
	// WarnOversizedChunk: RIFF/IFF chunk body clamped at EOF. Read path; not audio truncation.
	WarnOversizedChunk
	// WarnSyncedLyricsTimestampFormat: non-ms SYLT skipped on read. Read path; keyless.
	WarnSyncedLyricsTimestampFormat
	// WarnSyncedLyricsContentType: non-lyric SYLT skipped. Read path; keyless.
	WarnSyncedLyricsContentType
	// WarnSyncedLyricsMetadataDropped: LRC/SYLT per-set metadata loss on write. Plan; keyless.
	WarnSyncedLyricsMetadataDropped
	// WarnSyncedLyricsTimestampClamped: SYLT/LRC timestamp clamped. Write path; keyless.
	WarnSyncedLyricsTimestampClamped
	// WarnInvalidTagKey: Vorbis name not mappable to canonical key; preserved in file. Read; keyless.
	WarnInvalidTagKey
	// WarnNumberTotalConflict: slash number and explicit total disagree. Edit only; Warning.Keys; not --strict.
	WarnNumberTotalConflict
	// WarnValueCoerced: value normalized instead of dropped (vs WarnValueDropped). Plan; Warning.Keys; --strict.
	WarnValueCoerced
	// WarnChapterOverlapReconciled: edit overlap truncated stale end. Plan; keyless; not --strict.
	WarnChapterOverlapReconciled
	// WarnSyncedLyricsTruncated: per-set line cap exceeded on read (SYLT/LRC). Read; keyless.
	WarnSyncedLyricsTruncated
	// WarnSyncedLyricsUnsupported: no synced-lyrics store; whole set dropped. Plan discard; keyless; --strict.
	WarnSyncedLyricsUnsupported
	// WarnPictureUnsupported: format cannot store cover; picture dropped. Plan discard; keyless; --strict.
	WarnPictureUnsupported
	// WarnChaptersUnsupported: no chapter store; list dropped. Plan discard; keyless; --strict.
	WarnChaptersUnsupported
	// WarnMP4MultiValue: MP4 multi-value round-trips but many readers show first only. Plan; Warning.Keys; not --strict.
	WarnMP4MultiValue
	// WarnSyncedLyricsLineDropped: LRC input lines dropped as unrecognized. Plan; keyless; --strict.
	WarnSyncedLyricsLineDropped
	// WarnPictureSelectorMiss: remove-by-role matched nothing. Plan discard; keyless; --strict.
	WarnPictureSelectorMiss
	// WarnFragmented: MP4 moof; unwritable, duration/digest degraded. Read; keyless.
	WarnFragmented
	// WarnInvalidText: non-UTF-8 decoded via legacy code page. Read path.
	WarnInvalidText
	// WarnElementCap: parse element limit; partial model; write refused. Read path.
	WarnElementCap
	// WarnTrailingBytes: bytes outside container structure; preserved. Read; keyless.
	WarnTrailingBytes
	// WarnLegacyStripDropped: --legacy strip destroyed legacy-only data. Write; Warning.Keys if any; --strict.
	WarnLegacyStripDropped
	// WarnCommentDescriptionDropped: ID3 COMM description lost on merge/split. Plan; Warning.Keys.
	WarnCommentDescriptionDropped
	// WarnNonConformingIcon: type-1 icon not 32x32 PNG; still written. Plan; matches linter code.
	WarnNonConformingIcon
	// WarnDuplicateTagBlockDropped: rewrite dropped duplicate with extra content. Write; Warning.Keys; --strict.
	WarnDuplicateTagBlockDropped
	// WarnMalformedTagEntry: unparseable tag entry; bytes preserved. Read; not --strict.
	WarnMalformedTagEntry
	// WarnMalformedTagEntryDropped: rewrite omitted unreadable entry region. Write; --strict; not discard.
	WarnMalformedTagEntryDropped
	// WarnUnknownChunkSize: 0xFFFFFFFF chunk size; rest of file taken as body. Read; info severity.
	WarnUnknownChunkSize
	// WarnOutputGainUnsupported: output gain dropped (format stores none). Plan discard; --strict.
	WarnOutputGainUnsupported
	// WarnOutputGainR128Tags: R128 tags not rebased after gain edit ([WriteOptions.KeepR128Gains]). Advisory.
	WarnOutputGainR128Tags
)

func (c WarningCode) String() string {
	switch c {
	case WarnStrayLeadingID3:
		return "stray-leading-id3"
	case WarnTrailingID3v1:
		return "trailing-id3v1"
	case WarnLegacyAPE:
		return "legacy-ape"
	case WarnMultipleVorbisComment:
		return "multiple-vorbis-comment"
	case WarnInheritedEncoder:
		return "inherited-encoder"
	case WarnDistrustedBlockSize:
		return "distrusted-block-size"
	case WarnUnknownBlock:
		return "unknown-block"
	case WarnInvalidPicture:
		return "invalid-picture"
	case WarnConflictingFamilies:
		return "conflicting-families"
	case WarnNumericGenre:
		return "numeric-genre"
	case WarnChainedStream:
		return "chained-stream"
	case WarnID3MultiValue:
		return "id3-multi-value"
	case WarnDuplicateTagBlock:
		return "duplicate-tag-block"
	case WarnChapterSourceConflict:
		return "chapter-source-conflict"
	case WarnChaptersStale:
		return "chapters-stale"
	case WarnChapterTitleTruncated:
		return "chapter-title-truncated"
	case WarnChaptersFlattened:
		return "chapters-flattened"
	case WarnNoAudioFrames:
		return "no-audio"
	case WarnTruncatedAudio:
		return "truncated-audio"
	case WarnOversizedChunk:
		return "oversized-chunk"
	case WarnChapterPastDuration:
		return "chapter-past-duration"
	case WarnDuplicateChapter:
		return "duplicate-chapter"
	case WarnSingleValuedMulti:
		return "single-valued-multi"
	case WarnDuplicatePicture:
		return "duplicate-picture"
	case WarnMultipleFrontCovers:
		return "multiple-front-covers"
	case WarnPictureMetadataDropped:
		return "picture-metadata-dropped"
	case WarnLegacyConflict:
		return "legacy-conflict"
	case WarnValueDropped:
		return "value-dropped"
	case WarnNativeValueReduced:
		return "native-value-reduced"
	case WarnValueReduced:
		return "value-reduced"
	case WarnChapterEndsDropped:
		return "chapter-ends-dropped"
	case WarnPaddingClamped:
		return "padding-clamped"
	case WarnTagStructureDropped:
		return "tag-structure-dropped"
	case WarnChapterStartOverflow:
		return "chapter-start-overflow"
	case WarnChapterMetadataDropped:
		return "chapter-metadata-dropped"
	case WarnSyncedLyricsTimestampFormat:
		return "synced-lyrics-timestamp-format"
	case WarnSyncedLyricsContentType:
		return "synced-lyrics-content-type"
	case WarnSyncedLyricsMetadataDropped:
		return "synced-lyrics-metadata-dropped"
	case WarnSyncedLyricsTimestampClamped:
		return "synced-lyrics-timestamp-clamped"
	case WarnSyncedLyricsTruncated:
		return "synced-lyrics-truncated"
	case WarnSyncedLyricsUnsupported:
		return "synced-lyrics-unsupported"
	case WarnPictureUnsupported:
		return "picture-unsupported"
	case WarnChaptersUnsupported:
		return "chapters-unsupported"
	case WarnMP4MultiValue:
		return "mp4-multi-value"
	case WarnInvalidTagKey:
		return "invalid-tag-key"
	case WarnNumberTotalConflict:
		return "number-total-conflict"
	case WarnValueCoerced:
		return "value-coerced"
	case WarnChapterOverlapReconciled:
		return "chapter-overlap-reconciled"
	case WarnSyncedLyricsLineDropped:
		return "synced-lyrics-line-dropped"
	case WarnPictureSelectorMiss:
		return "picture-remove-role-miss"
	case WarnInvalidText:
		return "invalid-text"
	case WarnElementCap:
		return "element-cap"
	case WarnFragmented:
		return "fragmented"
	case WarnTrailingBytes:
		return "trailing-bytes"
	case WarnLegacyStripDropped:
		return "legacy-strip-dropped"
	case WarnCommentDescriptionDropped:
		return "comment-description-dropped"
	case WarnNonConformingIcon:
		return "non-conforming-icon"
	case WarnDuplicateTagBlockDropped:
		return "duplicate-tag-block-dropped"
	case WarnMalformedTagEntry:
		return "malformed-tag-entry"
	case WarnMalformedTagEntryDropped:
		return "malformed-tag-entry-dropped"
	case WarnUnknownChunkSize:
		return "unknown-chunk-size"
	case WarnOutputGainUnsupported:
		return "output-gain-unsupported"
	case WarnOutputGainR128Tags:
		return "output-gain-r128-tags"
	default:
		return "unknown"
	}
}

// Warning is a coded, human-readable note.
type Warning struct {
	Code    WarningCode
	Message string
	// Keys: canonical keys for keyed warnings (--strict). Empty when keyless.
	Keys []tag.Key
}

// String renders "[code] message". Message sanitized via [tag.SanitizeLine].
func (w Warning) String() string { return "[" + w.Code.String() + "] " + tag.SanitizeLine(w.Message) }

// Warn appends a warning to a slice, returning the new slice.
func Warn(ws []Warning, code WarningCode, msg string) []Warning {
	return append(ws, Warning{Code: code, Message: msg})
}

// WarnKeyed appends a warning with Warning.Keys set.
func WarnKeyed(ws []Warning, code WarningCode, msg string, keys ...tag.Key) []Warning {
	return append(ws, Warning{Code: code, Message: msg, Keys: keys})
}

// WarningsWithCode filters ws to listed codes, preserving order.
func WarningsWithCode(ws []Warning, codes ...WarningCode) []Warning {
	var out []Warning
	for _, w := range ws {
		if slices.Contains(codes, w.Code) {
			out = append(out, w)
		}
	}
	return out
}

// WarningsWithoutCode returns the warnings in ws whose code is not listed in codes,
// preserving order. It is the complement of [WarningsWithCode].
func WarningsWithoutCode(ws []Warning, codes ...WarningCode) []Warning {
	var out []Warning
	for _, w := range ws {
		if !slices.Contains(codes, w.Code) {
			out = append(out, w)
		}
	}
	return out
}

// WarnNativeReduced appends WarnNativeValueReduced for a multi-value native slot.
func WarnNativeReduced(ws []Warning, key tag.Key, n int, container string) []Warning {
	return WarnKeyed(ws, WarnNativeValueReduced,
		fmt.Sprintf("%s: native %s stores only the first of %d values (full set kept in the ID3 chunk)", key, container, n),
		key)
}

// NativeReducedWarnings collects WarnNativeValueReduced for keys reduced in a native slot.
func NativeReducedWarnings(ts tag.TagSet, container string, reduces func(tag.Key) bool) []Warning {
	var ws []Warning
	for _, k := range ts.Keys() {
		if !reduces(k) {
			continue
		}
		if n := ts.ValueCount(k); n > 1 {
			if v, ok := ts.First(k); ok && v != "" {
				ws = WarnNativeReduced(ws, k, n, container)
			}
		}
	}
	return ws
}

// WarnTruncated appends WarnTruncatedAudio with shared phrasing.
func WarnTruncated(ws []Warning, subject string) []Warning {
	return Warn(ws, WarnTruncatedAudio, subject+" declares more audio than the file holds; file may be truncated")
}

// WarnTrailing appends WarnTrailingBytes. what identifies region (see TrailingID3v1What).
func WarnTrailing(ws []Warning, n int64, subject, what string) []Warning {
	if n <= 0 {
		return ws
	}
	if what == "" {
		what = "belong to no chunk or page"
	}
	return Warn(ws, WarnTrailingBytes, fmt.Sprintf("%d byte(s) %s %s; preserved verbatim", n, subject, what))
}

// WarnInvalidKey appends WarnInvalidTagKey for an unmappable native name.
func WarnInvalidKey(ws []Warning, name string) []Warning {
	return Warn(ws, WarnInvalidTagKey,
		"tag key not represented in canonical tags (not carried): "+WarnSnippet(name))
}

// WarnUnseparatedEntry: one WarnMalformedTagEntry for unseparated comment entries (aggregated).
func WarnUnseparatedEntry(ws []Warning, first string, n int) []Warning {
	if n <= 0 {
		return ws
	}
	msg := "comment entry has no '=' separator (preserved in place, not carried): " + WarnSnippet(first)
	if n > 1 {
		msg += fmt.Sprintf(" (and %d more)", n-1)
	}
	return Warn(ws, WarnMalformedTagEntry, msg)
}

// WarnUnknownSize appends WarnUnknownChunkSize for 0xFFFFFFFF chunk sizes.
func WarnUnknownSize(ws []Warning, ids [][4]byte) []Warning {
	for _, id := range ids {
		ws = Warn(ws, WarnUnknownChunkSize,
			fmt.Sprintf("the %q chunk declares the 0xFFFFFFFF size-unknown value; its extent was taken as the rest of the file, so any chunk after it is not read", string(id[:])))
	}
	return ws
}

// warnSnippetBytes caps quoted file-derived text in warning messages.
const warnSnippetBytes = 96

// WarnSnippet sanitizes and elides file-derived warning text.
func WarnSnippet(s string) string { return tag.SanitizeLine(tag.ElideValueAt(s, warnSnippetBytes)) }

// UnparsedNote suffix for unread bytes in a native block summary. Empty when none.
func UnparsedNote(n int64) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(", %d unparsed byte(s)", n)
}

// TrailingID3v1What is WarnTrailing's what for a preserved ID3v1 trailer.
const TrailingID3v1What = "are an ID3v1 tag this format preserves but does not read"

// ConflictingFamiliesMessage is shared dump/lint wording for family conflicts.
func ConflictingFamiliesMessage() string {
	return "multiple source fields supplied conflicting values"
}

// ChapterPastDurationMessage is shared editor/lint wording.
func ChapterPastDurationMessage(start, duration time.Duration) string {
	return fmt.Sprintf("chapter at %s starts past the file duration (%s); check the timestamp",
		FormatChapterTime(start), FormatChapterTime(duration))
}

// DuplicateChapterMessage is shared editor/lint wording.
func DuplicateChapterMessage(start time.Duration) string {
	return fmt.Sprintf("two or more chapters share the start %s", FormatChapterTime(start))
}

// ChaptersPastDuration returns chapters with Start > duration. None when duration <= 0.
func ChaptersPastDuration(chapters []Chapter, duration time.Duration) []Chapter {
	if duration <= 0 {
		return nil
	}
	var out []Chapter
	for _, c := range chapters {
		if c.Start > duration {
			out = append(out, c)
		}
	}
	return out
}

// DuplicateChapterStarts returns duplicate start times, first-seen order.
func DuplicateChapterStarts(chapters []Chapter) []time.Duration {
	counts := make(map[time.Duration]int, len(chapters))
	var out []time.Duration
	for _, c := range chapters {
		if counts[c.Start]++; counts[c.Start] == 2 {
			out = append(out, c.Start)
		}
	}
	return out
}

// IsDiscardWarning: edit requested storage that did not happen (whole item dropped).
func IsDiscardWarning(c WarningCode) bool {
	switch c {
	case WarnValueDropped, WarnLegacyStripDropped, WarnDuplicateTagBlockDropped,
		WarnSyncedLyricsUnsupported, WarnPictureUnsupported, WarnChaptersUnsupported,
		WarnPictureSelectorMiss, WarnOutputGainUnsupported:
		return true
	}
	return false
}

// HasDiscardWarning reports whether any warning in ws is a discard (see [IsDiscardWarning]).
func HasDiscardWarning(ws []Warning) bool {
	for _, w := range ws {
		if IsDiscardWarning(w.Code) {
			return true
		}
	}
	return false
}

// NoChangesLine summarizes a no-op plan; distinguishes discard vs already up to date.
func NoChangesLine(discarded bool) string {
	if discarded {
		return "no changes written (the edit was discarded)"
	}
	return "no changes (already up to date)"
}

// AppendDuplicateBlockDropped warns when a dropped duplicate held content the write omits.
func AppendDuplicateBlockDropped(ws []Warning, container string, written tag.TagSet, dups []DuplicateContent) []Warning {
	seen := map[tag.Key]bool{}
	var keys []tag.Key
	var extra []string
	var pics, chaps, lyrics int
	for _, d := range dups {
		for _, k := range UnsubsumedKeys(written, d.Tags) {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		pics += d.Pictures
		chaps += d.Chapters
		lyrics += d.SyncedLyrics
	}
	names := make([]string, 0, len(keys)+3)
	for _, k := range keys {
		names = append(names, string(k))
	}
	for _, c := range []struct {
		n    int
		unit string
	}{{pics, "picture"}, {chaps, "chapter"}, {lyrics, "synced lyric set"}} {
		if c.n > 0 {
			extra = append(extra, fmt.Sprintf("%d %s(s)", c.n, c.unit))
		}
	}
	names = append(names, extra...)
	if len(names) == 0 {
		return ws
	}
	return WarnKeyed(ws, WarnDuplicateTagBlockDropped,
		fmt.Sprintf("a duplicate %s held content no other container does (%s); this rewrite drops it",
			container, strings.Join(names, ", ")), keys...)
}

// CloneWarnings deep-copies warnings and their Keys slices. Nil/empty in, nil out.
func CloneWarnings(ws []Warning) []Warning {
	if len(ws) == 0 {
		return nil
	}
	out := slices.Clone(ws)
	for i := range out {
		out[i].Keys = slices.Clone(out[i].Keys)
	}
	return out
}
