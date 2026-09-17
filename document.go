package waxlabel

import (
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// Document is an immutable, detached view of a parsed file. No file descriptor,
// no Close. Accessors return deep copies (including [Picture] payloads).
// [Document.Inspect] skips payloads for bulk scans. Safe for concurrent reads.
type Document struct {
	media *core.Media

	// Write source: path from ParseFile; src from OpenSource. Not owned FDs.
	path string
	src  core.ReaderAtSized

	// Parse limits, reused for WithVerifyEssence re-parse. Zero = defaults.
	limits Limits
}

// zero reports whether d is nil or has no media. Safe on a nil receiver.
func (d *Document) zero() bool { return d == nil || d.media == nil }

// Format returns the detected container/codec.
func (d *Document) Format() Format {
	if d.zero() {
		return FormatUnknown
	}
	return d.media.Format
}

// Tags returns a deep copy of the presence-aware canonical tag set.
func (d *Document) Tags() tag.TagSet {
	if d.zero() {
		return tag.TagSet{}
	}
	return d.media.Tags.Clone()
}

// Get returns the values for a canonical key and whether it is present.
func (d *Document) Get(key tag.Key) ([]string, bool) {
	if d.zero() {
		return nil, false
	}
	return d.media.Tags.Get(key)
}

// Fields returns the typed projection of tags (lossy on presence). Prefer [Tags]
// when presence matters.
func (d *Document) Fields() tag.Tags {
	if d.zero() {
		return tag.Tags{}
	}
	return tag.Project(d.media.Tags)
}

// Properties returns a copy of audio stream properties.
func (d *Document) Properties() Properties {
	if d.zero() {
		return Properties{}
	}
	return d.media.Properties.Clone()
}

// Pictures returns a deep copy of embedded pictures. MIME/dimensions are
// reconciled with bytes on the copy; stored media.Pictures is unchanged.
func (d *Document) Pictures() []Picture {
	if d.zero() {
		return nil
	}
	pics := clonePicturesDeep(d.media.Pictures)
	for i := range pics {
		pics[i].SniffAuthoritative()
	}
	return pics
}

// Chapters returns a detached copy in file order. Not in TagSet.
// [Document.Inspect] omits them.
func (d *Document) Chapters() []Chapter {
	if d.zero() {
		return nil
	}
	return core.CloneChapters(d.media.Chapters)
}

// SyncedLyrics returns a deep copy of timed lyric sets. Unsynchronized lyrics
// stay in [Tags] as LYRICS.
func (d *Document) SyncedLyrics() []SyncedLyrics {
	if d.zero() {
		return nil
	}
	return core.CloneSyncedLyrics(d.media.SyncedLyrics)
}

// Families returns which family supplied each canonical value and whether it won.
func (d *Document) Families() []FamilyValue {
	if d.zero() {
		return nil
	}
	out := make([]FamilyValue, len(d.media.Families))
	copy(out, d.media.Families)
	for i := range out {
		out[i].Values = append([]string(nil), out[i].Values...)
	}
	return out
}

// LegacyOnlyKeys returns keys present only in a legacy container (e.g. ID3v1,
// APEv2, stray ID3). Safe auto-fix keeps those containers; dump surfaces them.
func (d *Document) LegacyOnlyKeys() []tag.Key {
	if d.zero() {
		return nil
	}
	return core.LegacyOnlyKeys(d.media.Families, d.media.Tags)
}

// HasOpaqueLegacyContent reports non-tag legacy content (APEv2 binary, leading
// ID3v2 pictures/chapters/lyrics, or unreadable container). Safe fix keeps it.
func (d *Document) HasOpaqueLegacyContent() bool {
	if d.zero() {
		return false
	}
	return d.media.LegacyOpaqueContent
}

// Warnings returns non-fatal parse conditions.
func (d *Document) Warnings() []Warning {
	if d.zero() {
		return nil
	}
	return core.CloneWarnings(d.media.Warnings)
}

// Native returns a deep copy of the format's native document for inspection.
// Edit via [Editor.Native].
func (d *Document) Native() NativeDoc {
	if d.zero() || d.media.Native == nil {
		return nil
	}
	return d.media.Native.Clone()
}

// Padding reports free bytes in the metadata region (FLAC/MP3/AAC/MP4). Formats
// without a reporter return 0 ("not reported"). A truncated ID3 frame walk also
// reports 0 (malformed remainder, not free space); a plan may still report
// padding it will write. Uses [core.PaddingReporter] without deep-cloning Native.
func (d *Document) Padding() int64 {
	if d.zero() || d.media.Native == nil {
		return 0
	}
	if p, ok := d.media.Native.(core.PaddingReporter); ok {
		return p.PaddingBytes()
	}
	return 0
}

// Identity returns source identity for save-back change detection.
func (d *Document) Identity() Identity {
	if d.zero() {
		return Identity{}
	}
	return d.media.Identity
}

// Capabilities reports what the format can do under the given write options.
func (d *Document) Capabilities(opts ...WriteOption) Capabilities {
	if d.zero() {
		return Capabilities{Format: FormatUnknown, ReadOnly: true}
	}
	codec, ok := core.ForFormat(d.media.Format)
	if !ok {
		return Capabilities{Format: d.media.Format, ReadOnly: true}
	}
	return codec.Capabilities(d.media, resolveWriteOptions(opts))
}

// Snapshot is [Document.Inspect] output: fields and properties without picture
// bytes or native document.
type Snapshot struct {
	Format       Format
	Fields       tag.Tags
	Properties   Properties
	PictureCount int
	Warnings     []Warning
}

// Inspect returns a cheap Snapshot for bulk library scans.
func (d *Document) Inspect() Snapshot {
	if d.zero() {
		return Snapshot{Format: FormatUnknown}
	}
	return Snapshot{
		Format:       d.media.Format,
		Fields:       tag.Project(d.media.Tags),
		Properties:   d.media.Properties.Clone(),
		PictureCount: len(d.media.Pictures),
		Warnings:     core.CloneWarnings(d.media.Warnings),
	}
}

// Edit returns an Editor that records mutations without altering the Document.
func (d *Document) Edit() *Editor {
	if d.zero() {
		return &Editor{doc: d}
	}
	return &Editor{
		doc:      d,
		base:     d.media,
		pictures: core.ClonePictures(d.media.Pictures),
	}
}

// clonePicturesDeep returns a fully detached copy (including Data). Public
// [Pictures] uses this; hot paths use shallow [core.ClonePictures].
func clonePicturesDeep(ps []core.Picture) []Picture {
	if ps == nil {
		return nil
	}
	out := make([]Picture, len(ps))
	for i, p := range ps {
		c := p.CloneMeta() // structural fields; Data still shared at this point
		c.Data = append([]byte(nil), p.Data...)
		out[i] = c
	}
	return out
}
