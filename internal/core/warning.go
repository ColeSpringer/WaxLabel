package core

import (
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
	// preserved verbatim and now disagreed. A chapter edit now rebuilds both
	// representations, so this is no longer emitted; the code is retained (a stable
	// part of the warning surface) for a future write that can only update one.
	WarnChaptersStale
	// WarnChapterTitleTruncated means one or more chapter titles were trimmed to fit
	// a container limit on write (the Nero chpl's single-byte, 255-byte-max length
	// prefix). It is a plan-time warning, surfaced rather than silently truncating.
	WarnChapterTitleTruncated
	// WarnChaptersFlattened means a chapter edit re-rendered a default edition that
	// carried structure the flat chapter model cannot hold - nested sub-chapters or
	// secondary-language titles (Matroska ChapterDisplay) - so that structure was
	// dropped. A plan-time warning, surfaced rather than silently flattening.
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
	case WarnChapterPastDuration:
		return "chapter-past-duration"
	case WarnDuplicateChapter:
		return "duplicate-chapter"
	case WarnSingleValuedMulti:
		return "single-valued-multi"
	default:
		return "unknown"
	}
}

// Warning is a coded, human-readable note.
type Warning struct {
	Code    WarningCode
	Message string
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

// WarnTruncated appends a WarnTruncatedAudio warning naming the essence container
// that overran its file (e.g. "the data chunk", "the SSND chunk", "an mdat atom").
// The container walkers each detect the overrun at their own clamp but share this so
// the code and phrasing cannot drift between formats.
func WarnTruncated(ws []Warning, subject string) []Warning {
	return Warn(ws, WarnTruncatedAudio, subject+" declares more audio than the file holds; file may be truncated")
}

// CloneWarnings copies a warning slice.
func CloneWarnings(ws []Warning) []Warning { return slices.Clone(ws) }
