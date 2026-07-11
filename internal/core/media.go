package core

import (
	"slices"

	"github.com/colespringer/waxlabel/tag"
)

// Family identifies which tag container supplied a value. A single file can
// carry several (native plus legacy), so the projection records provenance and
// surfaces conflicts rather than hiding them.
type Family uint8

const (
	FamilyUnknown Family = iota
	FamilyVorbis
	FamilyID3v2
	FamilyID3v1
	FamilyAPEv2
	FamilyLyrics3
	FamilyMP4
	FamilyRIFF
	FamilyMatroska
	FamilyAIFF
)

func (f Family) String() string {
	switch f {
	case FamilyVorbis:
		return "vorbis"
	case FamilyID3v2:
		return "id3v2"
	case FamilyID3v1:
		return "id3v1"
	case FamilyAPEv2:
		return "apev2"
	case FamilyLyrics3:
		return "lyrics3"
	case FamilyMP4:
		return "mp4"
	case FamilyRIFF:
		return "riff"
	case FamilyMatroska:
		return "matroska"
	case FamilyAIFF:
		return "aiff"
	default:
		return "unknown"
	}
}

// Scope annotates the target a value applies to. Most formats are track-scoped;
// Matroska's targets make album/edition/chapter scopes meaningful.
type Scope uint8

const (
	ScopeTrack Scope = iota
	ScopeAlbum
	ScopeEdition
	ScopeChapter
)

func (s Scope) String() string {
	switch s {
	case ScopeAlbum:
		return "album"
	case ScopeEdition:
		return "edition"
	case ScopeChapter:
		return "chapter"
	default:
		return "track"
	}
}

// FamilyValue is one family's contribution to a canonical key. Selected marks
// the contribution that won the canonical projection; unselected entries for
// the same key indicate a conflict.
type FamilyValue struct {
	Key      tag.Key
	Family   Family
	Scope    Scope
	Values   []string
	Selected bool
	// Legacy marks a contribution from a non-authoritative, alternate container
	// (MP3's ID3v1/APEv2, FLAC's leading ID3v2 / trailing ID3v1) rather than the
	// format's canonical tag set. It distinguishes a value that lives only in such a
	// container - which dump would otherwise omit and a legacy strip would destroy -
	// from a native container's own scoped families, and disambiguates FamilyID3v2,
	// which is canonical for MP3 but legacy for FLAC.
	Legacy bool
}

// NativeEntry is a human-readable summary of one native metadata block, for
// the native/dump views.
type NativeEntry struct {
	Kind string
	// Size is a byte count by default, rendered with a binary unit (e.g. "57.2
	// KiB"). When Unit is non-empty, Size is instead that many of Unit (a count or
	// other non-byte quantity), rendered as "N <unit>" - so a count of pages,
	// tags, or chapters is never mislabeled as bytes. A zero Size with no Unit
	// renders blank (the block has no meaningful size, e.g. an EBML header).
	Size int
	// Unit names what Size counts when it is not bytes ("pages", "tags",
	// "chapters"); empty means Size is a byte count.
	Unit string
	Note string
}

// NativeDoc is a codec's editable native document - the base for
// preservation-first edits. It is opaque to the engine except for cloning (so
// Document accessors stay detached) and describing (for the native view).
type NativeDoc interface {
	Format() Format
	Clone() NativeDoc
	Describe() []NativeEntry
}

// Media is the neutral parsed representation a codec produces and the engine
// wraps in a Document. It carries both the canonical projection (Tags,
// Pictures, Properties) and the native base (Native) needed for
// preservation-first rewrites.
type Media struct {
	Format       Format
	Properties   Properties
	Tags         tag.TagSet
	Pictures     []Picture
	Chapters     []Chapter
	SyncedLyrics []SyncedLyrics
	Families     []FamilyValue
	Warnings     []Warning
	Native       NativeDoc
	Identity     Identity

	// LegacyOpaqueContent records that a legacy container holds non-tag content the
	// canonical projection does not fold in (an MP3 APEv2's binary items, a FLAC
	// leading ID3v2's pictures/chapters/synced lyrics, or an unreadable such
	// container). A legacy strip cannot prove such a container fully redundant, so
	// the safe auto-fix keeps it; dump surfaces it rather than hiding it.
	LegacyOpaqueContent bool

	// AudioStart and AudioEnd bound the audio essence within the source: the
	// bytes the rewrite must copy verbatim and the essence digest must hash.
	//
	// This single contiguous extent fits FLAC (metadata up front, one trailing
	// audio run). Codecs that interleave or split the essence set AudioRanges
	// instead (see below); for them AudioStart still marks where the audio region
	// begins (used for the save-back structural fingerprint).
	AudioStart int64
	AudioEnd   int64

	// AudioRanges is the codec-supplied multi-segment essence region for formats
	// whose audio is not one contiguous run - Ogg page bodies interleaved with
	// page headers, and later multiple/relocatable MP4 mdat. When non-nil it is
	// authoritative for essence hashing and verification (the ranges must be
	// ascending and disjoint, in source order); when nil the single
	// [AudioStart, AudioEnd) extent is used.
	AudioRanges [][2]int64
}

// EssenceRanges returns the audio-essence byte ranges to hash: the codec-supplied
// AudioRanges when present, else the single [AudioStart, AudioEnd) extent. The
// result is always non-nil for a parsed media with audio.
func (m *Media) EssenceRanges() [][2]int64 {
	if len(m.AudioRanges) > 0 {
		return m.AudioRanges
	}
	return [][2]int64{{m.AudioStart, m.AudioEnd}}
}

// Clone returns a deep copy. Native is cloned through its interface; picture
// Data stays shared (read-only by contract).
func (m *Media) Clone() *Media {
	if m == nil {
		return nil
	}
	c := &Media{
		Format:       m.Format,
		Properties:   m.Properties.Clone(),
		Tags:         m.Tags.Clone(),
		Pictures:     ClonePictures(m.Pictures),
		Chapters:     CloneChapters(m.Chapters),
		SyncedLyrics: CloneSyncedLyrics(m.SyncedLyrics),
		Families:     cloneFamilies(m.Families),
		Warnings:     CloneWarnings(m.Warnings),
		Identity:     m.Identity,
		AudioStart:   m.AudioStart,
		AudioEnd:     m.AudioEnd,

		LegacyOpaqueContent: m.LegacyOpaqueContent,
	}
	if m.AudioRanges != nil {
		c.AudioRanges = make([][2]int64, len(m.AudioRanges))
		copy(c.AudioRanges, m.AudioRanges)
	}
	if m.Native != nil {
		c.Native = m.Native.Clone()
	}
	return c
}

func cloneFamilies(fs []FamilyValue) []FamilyValue {
	if fs == nil {
		return nil
	}
	out := make([]FamilyValue, len(fs))
	for i, f := range fs {
		f.Values = slices.Clone(f.Values)
		out[i] = f
	}
	return out
}
