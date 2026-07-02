package waxlabel

import (
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// These aliases re-export the shared value types (which live in internal/core
// so codecs can use them without an import cycle) under the public package
// name. To a caller they are waxlabel.Picture, waxlabel.Format, and so on.
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
	// TransferReport describes a cross-format metadata copy: what each field,
	// picture set, and chapter set would carry, downgrade, or lose.
	TransferReport = core.TransferReport
	// TransferItem is one piece of metadata's fate in a transfer.
	TransferItem = core.TransferItem
	// TransferKind names a transferred item's category (field/picture/chapter).
	TransferKind = core.TransferKind
	// Disposition grades how a value survives a transfer
	// (carried/lossy/dropped/excluded).
	Disposition = core.Disposition
)

// TransferKind values.
const (
	TransferField       = core.TransferField
	TransferPicture     = core.TransferPicture
	TransferChapter     = core.TransferChapter
	TransferSyncedLyric = core.TransferSyncedLyric
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
	FormatUnknown   = core.FormatUnknown
	FormatFLAC      = core.FormatFLAC
	FormatOggVorbis = core.FormatOggVorbis
	FormatOggOpus   = core.FormatOggOpus
	FormatMP3       = core.FormatMP3
	FormatWAV       = core.FormatWAV
	FormatMP4       = core.FormatMP4
	FormatAAC       = core.FormatAAC
	FormatMatroska  = core.FormatMatroska
	FormatAIFF      = core.FormatAIFF
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
)

// Scope values annotate the target a family value applies to. Most formats are
// track-scoped; Matroska's targets make album/edition/chapter scopes meaningful.
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

	WarnInvalidTagKey = core.WarnInvalidTagKey
)

// BytesSource returns a ReaderAtSized backed by b (which must not be mutated
// while in use). It is handy for parsing or writing in-memory data.
func BytesSource(b []byte) ReaderAtSized { return core.BytesSource(b) }

// EqualPictures reports whether two picture slices are identical by content
// (type, MIME, description, dimensions, and bytes), in order. It is the same
// equality a codec uses to detect a picture edit, so a comparison and an edit
// cannot disagree on what "the same pictures" means.
func EqualPictures(a, b []Picture) bool { return core.EqualPictures(a, b) }

// EqualChapters reports whether two chapter slices are identical by content
// (start, end, and title), in order. This is the chapter analogue of [EqualPictures].
func EqualChapters(a, b []Chapter) bool { return core.EqualChapters(a, b) }

// EqualSyncedLyrics reports whether two synced-lyrics slices are identical by content
// (language, description, and timed lines), in order. SyncedLyrics contains a slice, so
// it is not comparable with ==; this is the equality codecs use to detect edits.
func EqualSyncedLyrics(a, b []SyncedLyrics) bool { return core.EqualSyncedLyrics(a, b) }

// ParseLRC parses an LRC document into timed lyric lines, applying the foobar2000
// [offset:] convention (effective timestamp = timestamp - offset) and skipping metadata
// tags such as [ar:], [ti:], [al:], and [length:]. A line with several leading time tags
// yields one SyncedLine per tag; lines are returned sorted by timestamp. This is the
// parser behind the FLAC/Ogg SYNCEDLYRICS store and a convenience for building a
// [SyncedLyrics] from an LRC file. LRC has no per-set language field; set it on
// SyncedLyrics yourself when the destination can store it.
func ParseLRC(text string) []SyncedLine { return core.ParseLRC(text) }

// FormatLRC renders timed lyric lines as an LRC document ("[mm:ss.mmm]text" per line, in
// order). It round-trips losslessly through [ParseLRC]; the per-set language and
// descriptor are not representable in LRC and are not emitted.
func FormatLRC(lines []SyncedLine) string { return core.FormatLRC(lines) }

// IsRecognizedImage reports whether data begins with the header of an image
// format WaxLabel can identify (PNG, JPEG, GIF, WebP, BMP, or TIFF). It is a
// header sniff, not a full decode, so it cannot recognize every valid image
// (AVIF/HEIC/JXL and the like return false); a caller embedding a deliberately
// exotic cover should offer an explicit override rather than treat a false
// negative as corruption. The CLI uses it to reject a non-image file passed as
// cover art before embedding it, without reaching into internal packages.
func IsRecognizedImage(data []byte) bool {
	_, ok := bits.SniffImage(data)
	return ok
}

// ExtensionsFor returns the lowercase file extensions (each with a leading dot)
// associated with format f, or nil for an unknown or unimplemented format. It
// lets a caller warn when an output path's extension does not match the data
// being written, since WaxLabel never transcodes.
func ExtensionsFor(f Format) []string {
	codec, ok := core.ForFormat(f)
	if !ok {
		return nil
	}
	return codec.Extensions()
}

// Formats returns every container/codec format this build implements, in
// registration order. It lets a caller enumerate the formats - for example to
// gather all recognized file extensions via [ExtensionsFor] when scanning a
// directory tree - without hard-coding the list.
func Formats() []Format {
	codecs := core.Codecs()
	out := make([]Format, 0, len(codecs))
	for _, c := range codecs {
		out = append(out, c.Format())
	}
	return out
}

// CapabilitiesFor reports what format f can do under the given write options,
// without a parsed file. It is the file-less, format-level query an edit form for
// a not-yet-created file of format f needs - the counterpart to
// [Document.Capabilities], which answers the same question for a file already in
// hand. Both route through the same codec call, so the file-aware and file-less
// reports cannot drift. An unknown or unimplemented format reports read-only
// (mirroring Document.Capabilities's no-codec fallback); pair it with the
// per-key [tag.Key.Multivalued] and the [Capabilities.Field] detail to enumerate
// what is editable.
func CapabilitiesFor(f Format, opts ...WriteOption) Capabilities {
	codec, ok := core.ForFormat(f)
	if !ok {
		return Capabilities{Format: f, ReadOnly: true}
	}
	return codec.Capabilities(nil, resolveWriteOptions(opts))
}
