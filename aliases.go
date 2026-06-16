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
	LegacyPreserve       = core.LegacyPreserve
	LegacyStrip          = core.LegacyStrip
	LegacyReconcile      = core.LegacyReconcile
	LegacyUpdateExisting = core.LegacyUpdateExisting
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
	WarnStrayLeadingID3       = core.WarnStrayLeadingID3
	WarnTrailingID3v1         = core.WarnTrailingID3v1
	WarnLegacyAPE             = core.WarnLegacyAPE
	WarnMultipleVorbisComment = core.WarnMultipleVorbisComment
	WarnInheritedEncoder      = core.WarnInheritedEncoder
	WarnDistrustedBlockSize   = core.WarnDistrustedBlockSize
	WarnUnknownBlock          = core.WarnUnknownBlock
	WarnInvalidPicture        = core.WarnInvalidPicture
	WarnConflictingFamilies   = core.WarnConflictingFamilies
	WarnNumericGenre          = core.WarnNumericGenre
	WarnChainedStream         = core.WarnChainedStream
	WarnID3MultiValue         = core.WarnID3MultiValue
	WarnDuplicateTagBlock     = core.WarnDuplicateTagBlock
)

// BytesSource returns a ReaderAtSized backed by b (which must not be mutated
// while in use). It is handy for parsing or writing in-memory data.
func BytesSource(b []byte) ReaderAtSized { return core.BytesSource(b) }
