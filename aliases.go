package waxlabel

import (
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// Type aliases re-export internal/core value types under the public package
// (avoids an import cycle for codecs). Callers see waxlabel.Picture, etc.
type (
	// Format identifies a container/codec.
	Format = core.Format
	// Picture is an embedded image.
	Picture = core.Picture
	// PictureType is a cover-art role.
	PictureType = core.PictureType
	// Chapter is a navigation point (Start, End, Title) in a timed file.
	Chapter = core.Chapter
	// SyncedLyrics is one timed-lyrics set (Language, Description, Lines).
	SyncedLyrics = core.SyncedLyrics
	// SyncedLine is one timed lyric line (Time, Text) within a SyncedLyrics set.
	SyncedLine = core.SyncedLine
	// Properties describes the audio stream(s).
	Properties = core.Properties
	// AudioTrack is one stream's technical properties.
	AudioTrack = core.AudioTrack
	// Capabilities reports what a format can do, per dimension.
	Capabilities = core.Capabilities
	// Capability is one field's multidimensional support.
	Capability = core.Capability
	// AccessLevel grades a support dimension.
	AccessLevel = core.AccessLevel
	// Warning is a coded non-fatal note from parse or planning.
	Warning = core.Warning
	// WarningCode categorizes a Warning.
	WarningCode = core.WarningCode
	// Family identifies which tag container supplied a value.
	Family = core.Family
	// FamilyValue is one family's contribution to a key.
	FamilyValue = core.FamilyValue
	// Scope annotates the target a value applies to.
	Scope = core.Scope
	// NativeEntry summarizes one native metadata block.
	NativeEntry = core.NativeEntry
	// NativeDoc is a codec's editable native document.
	NativeDoc = core.NativeDoc
	// Identity is a strong source fingerprint for change detection.
	Identity = core.Identity
	// ReaderAtSized is the internal source contract: random access plus size.
	ReaderAtSized = core.ReaderAtSized
	// LegacyPolicy controls handling of legacy/foreign tag containers.
	LegacyPolicy = core.LegacyPolicy
	// ID3MultiValuePolicy controls the ID3v2.3 multi-value representation.
	ID3MultiValuePolicy = core.ID3MultiValuePolicy
	// PaddingPolicy controls post-metadata free space.
	PaddingPolicy = core.PaddingPolicy
	// Limits bounds resource use when parsing untrusted input.
	Limits = bits.Limits
	// WriteReport describes a planned write.
	WriteReport = core.WriteReport
	// TransferReport describes a cross-format metadata copy.
	TransferReport = core.TransferReport
	// TransferItem is one metadata item's fate in a transfer.
	TransferItem = core.TransferItem
	// TransferKind is field, picture, chapter, or synced lyric.
	TransferKind = core.TransferKind
	// Disposition is carried, lossy, dropped, or excluded.
	Disposition = core.Disposition
)

// TransferKind values.
const (
	TransferField       = core.TransferField
	TransferPicture     = core.TransferPicture
	TransferChapter     = core.TransferChapter
	TransferSyncedLyric = core.TransferSyncedLyric
)

// Picture MIME constants.
const (
	// UnrecognizedMIME is set when bytes are not a recognized image header
	// (unknown format, junk, or empty). Used by [Picture.Unrecognized] and lint.
	UnrecognizedMIME = core.UnrecognizedMIME
	// LinkMIME means the payload is a URL, not image bytes (ID3v2 and FLAC PICTURE).
	LinkMIME = core.LinkMIME
	// RecognizedImageFormats names formats [IsRecognizedImage] accepts.
	RecognizedImageFormats = bits.RecognizedFormats
)

// Disposition values.
const (
	Carried  = core.Carried
	Lossy    = core.Lossy
	Dropped  = core.Dropped
	Excluded = core.Excluded
)

// Format values.
const (
	FormatUnknown      = core.FormatUnknown
	FormatFLAC         = core.FormatFLAC
	FormatOggVorbis    = core.FormatOggVorbis
	FormatOggOpus      = core.FormatOggOpus
	FormatMP3          = core.FormatMP3
	FormatWAV          = core.FormatWAV
	FormatMP4          = core.FormatMP4
	FormatAAC          = core.FormatAAC
	FormatMatroska     = core.FormatMatroska
	FormatAIFF         = core.FormatAIFF
	FormatOggFLAC      = core.FormatOggFLAC
	FormatWavPack      = core.FormatWavPack
	FormatMonkeysAudio = core.FormatMonkeysAudio
	FormatWMA          = core.FormatWMA
	FormatMusepack     = core.FormatMusepack
)

// PictureType values (matching ID3 APIC / FLAC PICTURE type IDs).
const (
	PicOther              = core.PicOther
	PicFileIcon           = core.PicFileIcon
	PicOtherFileIcon      = core.PicOtherFileIcon
	PicFrontCover         = core.PicFrontCover
	PicBackCover          = core.PicBackCover
	PicLeaflet            = core.PicLeaflet
	PicMedia              = core.PicMedia
	PicLeadArtist         = core.PicLeadArtist
	PicArtist             = core.PicArtist
	PicConductor          = core.PicConductor
	PicBand               = core.PicBand
	PicComposer           = core.PicComposer
	PicLyricist           = core.PicLyricist
	PicRecordingLocation  = core.PicRecordingLocation
	PicDuringRecording    = core.PicDuringRecording
	PicDuringPerformance  = core.PicDuringPerformance
	PicVideoScreenCapture = core.PicVideoScreenCapture
	PicBrightFish         = core.PicBrightFish
	PicIllustration       = core.PicIllustration
	PicBandLogo           = core.PicBandLogo
	PicPublisherLogo      = core.PicPublisherLogo
)

// AccessLevel values.
const (
	AccessNone    = core.AccessNone
	AccessPartial = core.AccessPartial
	AccessFull    = core.AccessFull
)

// LegacyPolicy values.
const (
	LegacyPreserve = core.LegacyPreserve
	LegacyStrip    = core.LegacyStrip
)

// ID3MultiValuePolicy values.
const (
	ID3MultiNullSep     = core.ID3MultiNullSep
	ID3MultiRepeatFrame = core.ID3MultiRepeatFrame
	ID3MultiSlash       = core.ID3MultiSlash
)

// Family values.
const (
	FamilyVorbis   = core.FamilyVorbis
	FamilyID3v2    = core.FamilyID3v2
	FamilyID3v1    = core.FamilyID3v1
	FamilyAPEv2    = core.FamilyAPEv2
	FamilyMP4      = core.FamilyMP4
	FamilyRIFF     = core.FamilyRIFF
	FamilyMatroska = core.FamilyMatroska
	FamilyAIFF     = core.FamilyAIFF
	FamilyASF      = core.FamilyASF
)

// Scope values. Most formats are track-scoped; Matroska also uses album,
// edition, and chapter targets.
const (
	ScopeTrack   = core.ScopeTrack
	ScopeAlbum   = core.ScopeAlbum
	ScopeEdition = core.ScopeEdition
	ScopeChapter = core.ScopeChapter
)

// WarningCode values.
const (
	WarnStrayLeadingID3        = core.WarnStrayLeadingID3
	WarnTrailingID3v1          = core.WarnTrailingID3v1
	WarnLegacyAPE              = core.WarnLegacyAPE
	WarnMultipleVorbisComment  = core.WarnMultipleVorbisComment
	WarnInheritedEncoder       = core.WarnInheritedEncoder
	WarnDistrustedBlockSize    = core.WarnDistrustedBlockSize
	WarnUnknownBlock           = core.WarnUnknownBlock
	WarnInvalidPicture         = core.WarnInvalidPicture
	WarnConflictingFamilies    = core.WarnConflictingFamilies
	WarnNumericGenre           = core.WarnNumericGenre
	WarnChainedStream          = core.WarnChainedStream
	WarnID3MultiValue          = core.WarnID3MultiValue
	WarnDuplicateTagBlock      = core.WarnDuplicateTagBlock
	WarnChapterSourceConflict  = core.WarnChapterSourceConflict
	WarnChaptersStale          = core.WarnChaptersStale
	WarnChapterTitleTruncated  = core.WarnChapterTitleTruncated
	WarnChaptersFlattened      = core.WarnChaptersFlattened
	WarnNoAudioFrames          = core.WarnNoAudioFrames
	WarnTruncatedAudio         = core.WarnTruncatedAudio
	WarnChapterPastDuration    = core.WarnChapterPastDuration
	WarnDuplicateChapter       = core.WarnDuplicateChapter
	WarnSingleValuedMulti      = core.WarnSingleValuedMulti
	WarnDuplicatePicture       = core.WarnDuplicatePicture
	WarnMultipleFrontCovers    = core.WarnMultipleFrontCovers
	WarnPictureMetadataDropped = core.WarnPictureMetadataDropped
	WarnLegacyConflict         = core.WarnLegacyConflict
	WarnValueDropped           = core.WarnValueDropped
	WarnNativeValueReduced     = core.WarnNativeValueReduced
	WarnValueReduced           = core.WarnValueReduced
	WarnChapterEndsDropped     = core.WarnChapterEndsDropped
	WarnPaddingClamped         = core.WarnPaddingClamped
	WarnTagStructureDropped    = core.WarnTagStructureDropped
	WarnChapterStartOverflow   = core.WarnChapterStartOverflow
	WarnChapterMetadataDropped = core.WarnChapterMetadataDropped
	WarnOversizedChunk         = core.WarnOversizedChunk

	WarnSyncedLyricsTimestampFormat  = core.WarnSyncedLyricsTimestampFormat
	WarnSyncedLyricsContentType      = core.WarnSyncedLyricsContentType
	WarnSyncedLyricsMetadataDropped  = core.WarnSyncedLyricsMetadataDropped
	WarnSyncedLyricsTimestampClamped = core.WarnSyncedLyricsTimestampClamped
	WarnSyncedLyricsTruncated        = core.WarnSyncedLyricsTruncated
	WarnSyncedLyricsUnsupported      = core.WarnSyncedLyricsUnsupported

	WarnPictureUnsupported  = core.WarnPictureUnsupported
	WarnChaptersUnsupported = core.WarnChaptersUnsupported

	WarnMP4MultiValue = core.WarnMP4MultiValue

	WarnInvalidTagKey = core.WarnInvalidTagKey

	WarnNumberTotalConflict = core.WarnNumberTotalConflict

	WarnValueCoerced = core.WarnValueCoerced

	WarnChapterOverlapReconciled = core.WarnChapterOverlapReconciled

	WarnSyncedLyricsLineDropped = core.WarnSyncedLyricsLineDropped

	WarnPictureSelectorMiss = core.WarnPictureSelectorMiss

	WarnFragmented  = core.WarnFragmented
	WarnInvalidText = core.WarnInvalidText
	WarnElementCap  = core.WarnElementCap

	WarnTrailingBytes             = core.WarnTrailingBytes
	WarnLegacyStripDropped        = core.WarnLegacyStripDropped
	WarnCommentDescriptionDropped = core.WarnCommentDescriptionDropped
	WarnNonConformingIcon         = core.WarnNonConformingIcon
	WarnDuplicateTagBlockDropped  = core.WarnDuplicateTagBlockDropped

	WarnMalformedTagEntry        = core.WarnMalformedTagEntry
	WarnMalformedTagEntryDropped = core.WarnMalformedTagEntryDropped
	WarnUnknownChunkSize         = core.WarnUnknownChunkSize

	WarnOutputGainUnsupported = core.WarnOutputGainUnsupported
	WarnOutputGainR128Tags    = core.WarnOutputGainR128Tags
)

// IsDiscardWarning reports whether the edit's content was thrown away rather
// than stored in altered form. Distinguishes a no-byte plan whose edit was
// discarded from one that was already up to date.
func IsDiscardWarning(c WarningCode) bool { return core.IsDiscardWarning(c) }

// HasDiscardWarning reports whether any warning in ws is a discard ([IsDiscardWarning]).
func HasDiscardWarning(ws []Warning) bool { return core.HasDiscardWarning(ws) }

// NoChangesLine is the shared one-line summary for a plan that writes no bytes
// (already up to date, or edit discarded).
func NoChangesLine(discarded bool) string { return core.NoChangesLine(discarded) }

// BytesSource returns a ReaderAtSized backed by b. Do not mutate b while in use.
func BytesSource(b []byte) ReaderAtSized { return core.BytesSource(b) }

// EqualPictures reports whether two picture slices match by content, in order.
// Same equality codecs use to detect picture edits.
func EqualPictures(a, b []Picture) bool { return core.EqualPictures(a, b) }

// EqualChapters reports whether two chapter slices match by content, in order.
func EqualChapters(a, b []Chapter) bool { return core.EqualChapters(a, b) }

// EqualChaptersModuloEnds reports equality after normalizing ends a codec would
// reconstruct (gapless interior end, or trailing end to EOF). Byte-identical
// lists are always equal. durA/durB are media durations for the trailing rule.
// Diff uses this (interior rule matches copy; trailing run-to-EOF is
// diff-specific). [EqualChapters] backs codec change-detection.
func EqualChaptersModuloEnds(a, b []Chapter, durA, durB time.Duration) bool {
	return core.EqualChaptersModuloEnds(a, b, durA, durB)
}

// EqualSyncedLyrics reports whether two synced-lyrics slices match by content,
// in order. SyncedLyrics holds a slice, so it is not comparable with ==.
func EqualSyncedLyrics(a, b []SyncedLyrics) bool { return core.EqualSyncedLyrics(a, b) }

// ParseLRC parses LRC into timed lines. Applies foobar2000 [offset:]
// (effective = timestamp - offset); skips [ar:]/[ti:]/[al:]/[length:]. Multiple
// leading time tags yield one SyncedLine each; results are sorted by time.
// Used by FLAC/Ogg SYNCEDLYRICS. LRC has no language field; set it on
// [SyncedLyrics] when needed.
func ParseLRC(text string) []SyncedLine { return core.ParseLRC(text) }

// ParseLRCFull is [ParseLRC] without the per-set line cap, for trusted in-memory
// input (e.g. a user LRC file). Untrusted media should use capped [ParseLRC].
func ParseLRCFull(text string) []SyncedLine { return core.ParseLRCFull(text) }

// ParseLRCReportFull is [ParseLRCFull] plus 1-based line numbers of dropped
// non-blank, non-structure lines (malformed timestamps, untimed text, bare
// [section] headers). Blank lines and ID/[offset:]/[length:] tags are not
// reported. CLI uses this for --synced-lyrics-file warnings and --strict.
func ParseLRCReportFull(text string) (lines []SyncedLine, droppedLines []int) {
	return core.ParseLRCReportFull(text)
}

// FormatLRC renders timed lines as LRC ("[mm:ss.mmm]text"). Round-trips through
// [ParseLRC]; language and descriptor are not in LRC and are omitted.
func FormatLRC(lines []SyncedLine) string { return core.FormatLRC(lines) }

// OutputGainDB renders Opus output gain (signed Q7.8 dB, 256 = +1 dB) as text.
func OutputGainDB(gain int) string { return core.OutputGainDB(gain) }

// OutputGainDecibels converts Opus output gain (signed Q7.8 dB) to float dB.
func OutputGainDecibels(gain int) float64 { return core.OutputGainDecibels(gain) }

// IsRecognizedImage reports whether data starts with a known image header
// ([RecognizedImageFormats]). Header sniff only, not a full decode.
func IsRecognizedImage(data []byte) bool {
	_, ok := bits.SniffImage(data)
	return ok
}

// ExtensionsFor returns lowercase extensions for format f (each with a leading
// dot), or nil if unknown. WaxLabel never transcodes.
func ExtensionsFor(f Format) []string {
	codec, ok := core.ForFormat(f)
	if !ok {
		return nil
	}
	return codec.Extensions()
}

// Formats returns every implemented format in registration order.
func Formats() []Format {
	codecs := core.Codecs()
	out := make([]Format, 0, len(codecs))
	for _, c := range codecs {
		out = append(out, c.Format())
	}
	return out
}

// CapabilitiesFor reports what format f can do under opts, without a parsed
// file. Counterpart to [Document.Capabilities]. Unknown formats report
// read-only.
func CapabilitiesFor(f Format, opts ...WriteOption) Capabilities {
	codec, ok := core.ForFormat(f)
	if !ok {
		return Capabilities{Format: f, ReadOnly: true}
	}
	return codec.Capabilities(nil, resolveWriteOptions(opts))
}
