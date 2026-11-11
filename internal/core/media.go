package core

import (
	"slices"

	"github.com/colespringer/waxlabel/tag"
)

// Family identifies which tag container supplied a value.
type Family uint8

const (
	FamilyUnknown Family = iota
	FamilyVorbis
	FamilyID3v2
	FamilyID3v1
	FamilyAPEv2
	_ // retired: nothing projects Lyrics3; the slot keeps the later values stable
	FamilyMP4
	FamilyRIFF
	FamilyMatroska
	FamilyAIFF
	FamilyASF
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
	case FamilyMP4:
		return "mp4"
	case FamilyRIFF:
		return "riff"
	case FamilyMatroska:
		return "matroska"
	case FamilyAIFF:
		return "aiff"
	case FamilyASF:
		return "asf"
	default:
		return "unknown"
	}
}

// Scope is the target a value applies to (track, album, edition, chapter).
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

// FamilyValue is one family's values for a key. Selected won projection; unselected = conflict.
type FamilyValue struct {
	Key      tag.Key
	Family   Family
	Scope    Scope
	Values   []string
	Selected bool
	// Legacy: value from a non-authoritative container (ID3v1/APE, FLAC stray ID3).
	Legacy bool
}

// NativeEntry summarizes one native metadata block for dump.
type NativeEntry struct {
	Kind string
	// Size is bytes unless Unit is set (then a count: pages, tags, chapters).
	Size int
	// Unit names non-byte Size; empty means bytes.
	Unit string
	Note string
}

// LeadingBytesEntry describes the n bytes a rewrite keeps between a leading tag and the stream.
func LeadingBytesEntry(n int64) NativeEntry {
	return NativeEntry{Kind: "leading bytes", Size: int(n), Note: "preserved"}
}

// LeadingID3Entry describes an n-byte stray ID3v2 tag before a stream whose tags live elsewhere;
// unread is how many of its bytes could not be read.
func LeadingID3Entry(n int, unread int64) NativeEntry {
	return NativeEntry{Kind: "ID3v2", Size: n, Note: "leading, legacy, preserved" + UnparsedNote(unread)}
}

// NativeDoc is a codec's editable native document (clone + describe only).
type NativeDoc interface {
	Format() Format
	Clone() NativeDoc
	Describe() []NativeEntry
}

// PaddingReporter reports padding bytes (optional; ID3 padding is not a separate block).
type PaddingReporter interface {
	PaddingBytes() int64
}

// EssenceRefuser is a native document that may hold part of the file's audio alone (optional):
// RefuseEssence says why a digest of its essence would not stand for the file, or nil.
type EssenceRefuser interface {
	RefuseEssence() error
}

// Media is the neutral parse result: canonical projection plus native base for rewrites.
type Media struct {
	Format       Format
	Properties   Properties
	Tags         tag.TagSet
	Pictures     []Picture
	Chapters     []Chapter
	SyncedLyrics []SyncedLyrics
	Comments     []TextItem // qualifiers for the COMMENT values, one-to-one ([SyncTextItems])
	Lyrics       []TextItem // qualifiers for the LYRICS values, one-to-one
	Families     []FamilyValue
	Warnings     []Warning
	Native       NativeDoc
	Identity     Identity

	// LegacyOpaqueContent: legacy container holds content projection cannot fold (APE binary, etc.).
	LegacyOpaqueContent bool

	// AudioStart/AudioEnd bound contiguous essence bytes to copy and hash.
	AudioStart int64
	AudioEnd   int64

	// AudioRanges is multi-segment essence when non-nil (Ogg, split mdat). Else use AudioStart/End.
	AudioRanges [][2]int64
}

// EssenceRanges returns audio byte ranges to hash (AudioRanges or single extent).
func (m *Media) EssenceRanges() [][2]int64 {
	if len(m.AudioRanges) > 0 {
		return m.AudioRanges
	}
	return [][2]int64{{m.AudioStart, m.AudioEnd}}
}

// Clone deep-copies Media. Picture Data stays shared read-only.
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
		Comments:     slices.Clone(m.Comments),
		Lyrics:       slices.Clone(m.Lyrics),
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
