package core

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/tag"
)

// WarningCode categorizes a non-fatal condition surfaced during parse or
// planning. Preservation-first means WaxLabel warns rather than silently
// dropping or rewriting; callers can inspect or act on these.
type WarningCode uint8

const (
	WarnUnknown WarningCode = iota
	// WarnStrayLeadingID3 means an ID3v2 tag precedes the "fLaC" marker. It is
	// preserved by default.
	WarnStrayLeadingID3
	// WarnTrailingID3v1 means a 128-byte ID3v1 tag trails the audio. Preserved.
	WarnTrailingID3v1
	// WarnLegacyAPE means an APEv2 tag is present alongside the native tags.
	WarnLegacyAPE
	// WarnMultipleVorbisComment means more than one Vorbis comment block was
	// found; the first is authoritative, the rest preserved.
	WarnMultipleVorbisComment
	// WarnInheritedEncoder means an "encoder=Lavf..." style comment from a
	// transcoder was found - typical of acquired files.
	WarnInheritedEncoder
	// WarnDistrustedBlockSize means a block's declared length disagreed with
	// its real content length (a known broken-encoder case).
	WarnDistrustedBlockSize
	// WarnUnknownBlock means a metadata block of an unrecognized type was
	// preserved verbatim.
	WarnUnknownBlock
	// WarnInvalidPicture means a picture block could not be fully interpreted.
	WarnInvalidPicture
	// WarnConflictingFamilies means multiple tag families supplied different
	// values for the same canonical field.
	WarnConflictingFamilies
	// WarnNumericGenre means a numeric/"(17)" genre reference was mapped to a
	// name on read.
	WarnNumericGenre
	// WarnChainedStream means a chained/multiplexed Ogg stream was read
	// best-effort.
	WarnChainedStream
	// WarnID3MultiValue means a multi-value field was written NUL-separated in an
	// ID3v2.3 tag - a de-facto extension some readers do not split.
	WarnID3MultiValue
	// WarnDuplicateTagBlock means more than one tag container of the same kind was
	// found (e.g. two RIFF LIST/INFO chunks or two WAV id3 chunks); the first is
	// authoritative and the rest are dropped if the file is rewritten.
	WarnDuplicateTagBlock
	// WarnChapterSourceConflict means a file carried chapters in two
	// representations (an MP4 Nero chpl list and a QuickTime chapter text track)
	// that disagree. The file was already inconsistent on parse; the richer
	// representation is projected and this records the disagreement.
	WarnChapterSourceConflict
	// WarnChaptersStale meant a chapter edit was written to one representation only
	// (the MP4 Nero chpl) while a second (a QuickTime chapter text track) was
	// preserved verbatim and now disagreed. Chapter edits now rebuild both
	// representations, so this is no longer emitted; the code remains part of the
	// stable warning surface.
	WarnChaptersStale
	// WarnChapterTitleTruncated means one or more chapter titles were trimmed to fit
	// a container limit on write (the Nero chpl's single-byte, 255-byte-max length
	// prefix). It is a plan-time warning, surfaced rather than silently truncating.
	WarnChapterTitleTruncated
	// WarnChaptersFlattened means a file's chapters carried structure the flat chapter
	// model cannot hold, so that structure was dropped during projection. Matroska nested
	// sub-chapters, secondary ChapterDisplay titles, and nested ID3v2 CTOC hierarchies
	// all flatten to a single ordered chapter list.
	WarnChaptersFlattened
	// WarnNoAudioFrames means no decodable audio frame was found: the file may be
	// tag-only or truncated. The audio-essence digest refuses to hash zero essence
	// (see HashAudioEssence) rather than mint a fake-stable hash over nothing.
	WarnNoAudioFrames
	// WarnTruncatedAudio means the container declares more audio than the file
	// actually holds: a positive declared essence size whose end runs past the file
	// (WAV data / AIFF SSND / MP4 mdat), or a VBR MP3 whose Xing/Info frame count
	// implies far more audio than the bytes present. It is the "some-but-not-all"
	// counterpart to WarnNoAudioFrames (zero essence); only the reliable per-format
	// signals are emitted, so a clean file is never flagged.
	WarnTruncatedAudio
	// WarnChapterPastDuration means an edited chapter starts beyond the file's
	// playable length - usually a mistyped timestamp. It is an edit-time sanity
	// warning on the user's chapter input (gated on a known, non-zero duration), not
	// a lint of pre-existing on-disk chapters; the chapter is still written.
	WarnChapterPastDuration
	// WarnDuplicateChapter means an edited chapter list has two chapters sharing a
	// start time - navigation will land on only one. An edit-time sanity warning;
	// the chapters are still written faithfully.
	WarnDuplicateChapter
	// WarnSingleValuedMulti means an edit leaves a known single-valued key holding
	// more than one value. The writer stores them faithfully, but a reader using the
	// typed projection sees only the first - so it is surfaced as a plan-time warning
	// rather than written silently.
	WarnSingleValuedMulti
	// WarnDuplicatePicture means an edit added a picture whose image bytes are
	// identical (same [Picture.Hash]) to another in the set. It is an edit-time sanity
	// warning scoped to pictures this edit authored (the linter reports the whole-set
	// case separately); the picture is still written. Its String() is "duplicate-picture"
	// to match the linter's finding code, so the two never drift.
	WarnDuplicatePicture
	// WarnMultipleFrontCovers means an edit added a front-cover picture to a set that
	// now holds more than one. An edit-time sanity warning scoped to this edit's
	// additions; both covers are still written. Its String() is "multiple-front-covers"
	// to match the linter's finding code.
	WarnMultipleFrontCovers
	// WarnPictureMetadataDropped means the destination format does not fully preserve a
	// picture's role (type) and/or description an edit set. MP4 covr atoms store image
	// data only, so every cover reads back as a front cover with no description. Matroska
	// preserves only the front-cover role; other roles read back as Other, though
	// descriptions survive. The warning makes that loss visible before the write.
	WarnPictureMetadataDropped
	// WarnLegacyConflict means an edit changed a canonical key whose value is also held
	// in a preserved legacy container the family view carries (an ID3v1 or APEv2 tag on
	// the ID3-based formats) under the default LegacyPreserve policy, so the legacy copy
	// now disagrees with the native tags. It is an edit-time sanity warning (the value is
	// still written; the legacy container is preserved verbatim as promised), surfaced so
	// the divergence is visible and the remedy (--legacy strip, or lint --fix) is offered.
	WarnLegacyConflict
	// WarnValueDropped means an edit set a canonical value the destination format's
	// encoder cannot represent, so the value is silently lost on write - today the MP4
	// iTunes atoms: a trkn/disk number/total outside the uint16 the atom holds (a
	// non-numeric value, a negative, or one past 65535) or a non-numeric stik media
	// kind. It is a plan-time warning carrying the offending key (Warning.Keys), surfaced
	// before the write rather than vanishing with exit 0, so the user (and the CLI's
	// --strict gate) sees the loss.
	WarnValueDropped
	// WarnNativeValueReduced means a legitimately multi-valued key was reduced to its
	// first value in a secondary single-valued native container (the WAV LIST/INFO chunk
	// or an AIFF text chunk) while the full set is kept in the embedded ID3 chunk. The
	// canonical projection is unaffected because ID3 wins, but a non-WaxLabel reader that
	// consults only the native container will see only the first value. This is the
	// opposite of WarnSingleValuedMulti: here the key is genuinely multi-valued and the
	// reduction is a faithful format limit.
	WarnNativeValueReduced
	// WarnValueReduced means an edit set a value the destination stores with reduced
	// fidelity under a field-level partial-write capability. The warning carries the
	// affected key and is emitted only when the codec's projected result differs from the
	// edited value. For example, ID3v2.3 stores ORIGINALDATE as a year-only TORY frame.
	WarnValueReduced
	// WarnChapterEndsDropped means a chapter rewrite replaced chapters that carried
	// explicit end times with a list that has none. It is currently Matroska/WebM-only:
	// that format reads ends from ChapterTimeEnd, while MP4 infers them from the next
	// chapter start. The warning is keyless because it describes the chapter set, not a
	// tag field.
	WarnChapterEndsDropped
	// WarnPaddingClamped means a requested post-metadata padding exceeded the format's
	// per-block hard cap (FLAC's ~16 MiB metadata-block body) and was reduced to it, so
	// the written padding is smaller than asked. Keyless: it describes the write, not a
	// tag field.
	WarnPaddingClamped
	// WarnTagStructureDropped means an edit changed the value of an album-scope Matroska
	// SimpleTag that carried structure the flat canonical model cannot hold - a secondary
	// language (TagLanguage), a binary value (TagBinary), or nested sub-tags - so the new
	// value is re-emitted flat and that structure is lost. An unchanged structured tag is
	// preserved verbatim; this fires only when the key was edited, so the old bytes cannot
	// be kept. It is a plan-time warning carrying the affected key (Warning.Keys), so the
	// CLI's --strict gate can act on it.
	WarnTagStructureDropped
	// WarnChapterStartOverflow means a chapter start or end exceeded a format's 32-bit
	// timestamp field and was clamped on write. MP4 QuickTime chapter tracks and ID3v2
	// CHAP frames both have this limit. It is keyless because it describes the chapter
	// set, not a tag field.
	WarnChapterStartOverflow
	// WarnChapterMetadataDropped means a direct chapter edit carried fields the destination
	// cannot store. The exact loss is driven by [ChapterLoss]: start+title formats drop
	// gapped ends, language, and hidden/disabled flags; ID3v2 CHAP keeps ends but drops
	// language and flags. It is scoped to authored chapters and is keyless because it
	// describes the chapter set, not a tag field.
	WarnChapterMetadataDropped
	// WarnOversizedChunk means a non-audio RIFF/IFF chunk declared a body past EOF and was
	// clamped to fit. It is separate from WarnTruncatedAudio because it describes a
	// container chunk, not audio essence or a tag field.
	WarnOversizedChunk
	// WarnSyncedLyricsTimestampFormat means an ID3v2 SYLT frame used the MPEG-frames
	// timestamp format (format 1) instead of milliseconds (format 2). WaxLabel reads and
	// writes only the millisecond format. Mapping MPEG frames to a time needs the full
	// frame index, so the frame is skipped on projection rather than placed at the wrong
	// offset. Keyless: it describes a synced-lyrics set, not a tag field.
	WarnSyncedLyricsTimestampFormat
	// WarnSyncedLyricsContentType means an ID3v2 SYLT frame carried a non-lyric content
	// type, such as chord, trivia, or image URL. Only the lyrics content type projects into
	// the synced-lyrics model, so the frame is skipped rather than misrepresented as lyrics.
	// It is preserved verbatim through an unrelated edit. Keyless.
	WarnSyncedLyricsContentType
	// WarnSyncedLyricsMetadataDropped means a direct synced-lyrics edit carried a per-set
	// language or descriptor the destination cannot store. The VorbisComment LRC store
	// keeps the timed text only (see [SyncedLyricsLoss]). It is scoped to authored sets and
	// is keyless because it describes the synced-lyrics set, not a tag field.
	WarnSyncedLyricsMetadataDropped
	// WarnSyncedLyricsTimestampClamped means a synced-lyric line's timestamp exceeded the
	// ID3v2 SYLT frame's 32-bit millisecond field (~49.7 days) and was clamped on write,
	// the synced-lyrics analogue of [WarnChapterStartOverflow]. Keyless: it describes the
	// synced-lyrics set, not a tag field.
	WarnSyncedLyricsTimestampClamped
	// WarnInvalidTagKey means a Vorbis comment used a native name that does not map to a valid
	// canonical tag key - an empty name, or one with characters the writer's Key.Valid() gate
	// rejects. The raw comment is preserved verbatim on write (preservation-first Rebuild), so
	// the key is absent from the canonical tag model and a copy does not carry it, but it is
	// not removed from the file. Emitted on the read path so dump and lint surface it. Keyless:
	// the offending native name is in the prose Message, since there is no valid canonical key
	// to name.
	WarnInvalidTagKey
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
	case WarnInvalidTagKey:
		return "invalid-tag-key"
	default:
		return "unknown"
	}
}

// Warning is a coded, human-readable note.
type Warning struct {
	Code    WarningCode
	Message string
	// Keys names the canonical key(s) a key-specific warning concerns (a value-dropped
	// or single-valued-multi warning), so a consumer can act on the key without parsing
	// the prose Message - the CLI's --strict gate renders the offending key from it.
	// It is metadata on top of Message (which already names the key in prose), not part
	// of String(); it is empty for warnings that are not about a specific key.
	Keys []tag.Key
}

// String renders the warning as "[code] message". The code is a fixed vocabulary
// word, but the message can embed a file-derived snippet (an inherited encoder
// stamp, a conflicting family value), so it is run through [tag.SanitizeLine] -
// the warning prints as one list item, so an embedded newline or tab is escaped
// too (it cannot forge a line), not just the terminal-hijack class. A library
// consumer that prints this without the CLI's output boundary is then safe, and
// the boundary is a no-op over the already-escaped result.
func (w Warning) String() string { return "[" + w.Code.String() + "] " + tag.SanitizeLine(w.Message) }

// Warn appends a warning to a slice, returning the new slice.
func Warn(ws []Warning, code WarningCode, msg string) []Warning {
	return append(ws, Warning{Code: code, Message: msg})
}

// WarnKeyed appends a warning carrying the canonical key(s) it concerns, so a
// consumer (the CLI's --strict gate) can name the offending key without parsing the
// message. It is the keyed counterpart to [Warn]; the keys are metadata on top of
// the prose Message, which still names the key itself.
func WarnKeyed(ws []Warning, code WarningCode, msg string, keys ...tag.Key) []Warning {
	return append(ws, Warning{Code: code, Message: msg, Keys: keys})
}

// WarningsWithCode returns the warnings in ws whose code is one of codes, in order.
// [DowngradeNoOp] uses it to carry the input-rejection warnings (value-dropped,
// picture-metadata-dropped) through a no-op downgrade, which builds a fresh report:
// when an edit produces no byte change precisely because an input could not be stored,
// that silent drop must still surface (and --strict still escalate).
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

// WarnNativeReduced appends a [WarnNativeValueReduced] warning naming a multi-valued
// key whose secondary single-valued native container stores only its first value while
// the full set is kept in the embedded ID3 chunk. container names the native slot for
// the message ("LIST/INFO" for WAV, "text chunk" for AIFF).
func WarnNativeReduced(ws []Warning, key tag.Key, n int, container string) []Warning {
	return WarnKeyed(ws, WarnNativeValueReduced,
		fmt.Sprintf("%s: native %s stores only the first of %d values (full set kept in the ID3 chunk)", key, container, n),
		key)
}

// NativeReducedWarnings notes each key whose multi-valued set is reduced to its first
// value in a single-valued native slot while the full set is written alongside in ID3.
// reduces reports whether a key maps to such a native slot; container names that slot in
// the message ("LIST/INFO", "text chunk"). Only a key whose first value is present and
// non-empty is reported, since a slot that stores nothing reduces nothing.
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

// WarnTruncated appends a WarnTruncatedAudio warning naming the essence container
// that overran its file (e.g. "the data chunk", "the SSND chunk", "an mdat atom").
// The container walkers each detect the overrun at their own clamp but share this so
// the code and phrasing cannot drift between formats.
func WarnTruncated(ws []Warning, subject string) []Warning {
	return Warn(ws, WarnTruncatedAudio, subject+" declares more audio than the file holds; file may be truncated")
}

// ConflictingFamiliesMessage is the shared keyless wording for the conflicting-families
// condition: more than one native field supplied a different value for a key, so no
// value could be selected. Both surfaces attach the key the same way - the dump warning
// (which has no key field) appends it inline as " (KEY)", and the linter's lintFamilies
// finding carries it in its Key field, which Finding.String renders the same " (KEY)"
// way - so dump and lint read identically while lint keeps the key structured (in its
// JSON, like the other key-specific findings). Sharing the wording here is what keeps
// the two from drifting, mirroring the duplicatePictureMessage/multipleFrontCoversMessage
// pattern.
func ConflictingFamiliesMessage() string {
	return "multiple source fields supplied conflicting values"
}

// CloneWarnings deep-copies a warning slice, detaching each Warning.Keys so a
// caller mutating a returned warning's Keys cannot reach back into the shared
// source slice. A shallow slices.Clone leaves every element's Keys aliasing the
// original; [WarnKeyed] populates Keys, so a keyed warning surfaced through a
// defensive copy (e.g. Plan.Report) needs the per-element clone too. Returns nil
// for a nil/empty input.
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
