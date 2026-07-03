package waxlabel

import (
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// Document is an immutable, detached, serializable view of a parsed file. It
// holds no file descriptor and has no Close method, so a caller may scan it,
// cache it, and discard it freely. Accessors return detached deep copies,
// including each [Picture]'s payload bytes ([Document.Pictures]), so a caller may
// mutate anything an accessor returns without affecting the Document or a later
// call. For bulk scans that do not need payloads, [Document.Inspect] skips them
// (and the native document) to stay cheap.
//
// A Document is safe for concurrent reads.
type Document struct {
	media *core.Media

	// Write-source resolution (never an OS resource the Document owns):
	// path is set by ParseFile; src is an in-memory source set by OpenSource.
	path string
	src  core.ReaderAtSized

	// limits records the allocation/recursion limits this document was parsed under, so a
	// later write with WithVerifyEssence can re-parse the output under the same ceilings the
	// original parse cleared. Without it, a document parsed with an elevated WithLimits (a deep
	// tree, many elements, or a cover past the default cap) would fail its own structural
	// re-verification under the default limits and abort a valid write. Zero value = the library
	// defaults, which parseSource resolves the same way parse does.
	limits Limits
}

// zero reports whether d is a nil or uninitialized Document (no parsed media).
// Every accessor guards on it so a zero-value *Document - or one a caller built
// directly - returns the safe empty value for its type instead of panicking on a
// nil d.media deref, matching the graceful errors the ctx-taking entry points and
// HashAudioEssence already return. It is safe to call on a nil receiver.
func (d *Document) zero() bool { return d == nil || d.media == nil }

// Format returns the detected container/codec.
func (d *Document) Format() Format {
	if d.zero() {
		return FormatUnknown
	}
	return d.media.Format
}

// Tags returns the authoritative, presence-aware canonical tag set as a deep
// copy. This is the source of truth; the typed [Document.Fields] is derived
// from it.
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

// Fields returns the typed convenience projection of the canonical tags. It is
// lossy (a struct cannot express presence); use [Document.Tags] when that
// distinction matters.
func (d *Document) Fields() tag.Tags {
	if d.zero() {
		return tag.Tags{}
	}
	return tag.Project(d.media.Tags)
}

// Properties returns the audio stream properties (a copy).
func (d *Document) Properties() Properties {
	if d.zero() {
		return Properties{}
	}
	return d.media.Properties.Clone()
}

// Pictures returns the embedded pictures as a fully detached deep copy: each
// returned Picture's Data is independent, so a caller may mutate it without
// affecting a later Pictures() call or any internal state.
func (d *Document) Pictures() []Picture {
	if d.zero() {
		return nil
	}
	// Display projection: reconcile each returned cover's MIME/dimensions with its own bytes, so a
	// mislabeled cover shows its real type and a junk cover degrades to the unrecognized MIME that
	// lint flags. media.Pictures itself stays stored (it is the edit/write source), so this sniff is
	// confined to the detached copy the caller sees. Idempotent for the id3/mp4/matroska codecs,
	// whose decoders already store the sniffed type.
	pics := clonePicturesDeep(d.media.Pictures)
	for i := range pics {
		pics[i].SniffAuthoritative()
	}
	return pics
}

// Chapters returns the navigation chapters as a detached copy, in file order.
// Chapters live beside the canonical tags (not inside the TagSet); a file with no
// chapters returns none. [Document.Inspect] deliberately omits them.
func (d *Document) Chapters() []Chapter {
	if d.zero() {
		return nil
	}
	return core.CloneChapters(d.media.Chapters)
}

// SyncedLyrics returns the timed lyric sets as a detached deep copy, in file order. Like
// chapters, they live beside the canonical tags rather than inside the TagSet. A file with
// none returns nil. Unsynchronized lyrics remain in [Document.Tags] as the LYRICS field.
func (d *Document) SyncedLyrics() []SyncedLyrics {
	if d.zero() {
		return nil
	}
	return core.CloneSyncedLyrics(d.media.SyncedLyrics)
}

// Families returns the tag-family/source view: which family supplied each
// canonical value, its scope, and whether it won the projection (unselected
// entries for a key signal a conflict).
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

// Warnings returns the non-fatal conditions found during parse (preserved
// legacy tags, inherited encoder stamps, unknown blocks, and so on).
func (d *Document) Warnings() []Warning {
	if d.zero() {
		return nil
	}
	return core.CloneWarnings(d.media.Warnings)
}

// Native returns a deep copy of the format's native document for inspection.
// To edit it, use [Editor.Native].
func (d *Document) Native() NativeDoc {
	if d.zero() || d.media.Native == nil {
		return nil
	}
	return d.media.Native.Clone()
}

// Identity returns the recorded source identity (path, size, timestamps, and
// structural fingerprint) used for save-back change detection.
func (d *Document) Identity() Identity {
	if d.zero() {
		return Identity{}
	}
	return d.media.Identity
}

// Capabilities reports what the format can do under the given write options
// (capabilities are option-dependent).
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

// Snapshot is the lightweight result of [Document.Inspect]: typed fields and
// properties for bulk scanning, deliberately omitting picture bytes and the
// native document.
type Snapshot struct {
	Format       Format
	Fields       tag.Tags
	Properties   Properties
	PictureCount int
	Warnings     []Warning
}

// Inspect returns a Snapshot suitable for scanning large libraries: it skips
// picture payloads and the native document entirely, so it stays cheap.
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

// Edit returns an Editor that records mutations against this Document without
// altering it. The Document remains immutable.
func (d *Document) Edit() *Editor {
	// A zero-value Document has no base media to seed the editor; return an editor
	// with a nil base (and no pictures) so the chaining methods stay usable and
	// [Editor.Prepare] reports the uninitialized state cleanly rather than panicking
	// here on d.media.Pictures.
	if d.zero() {
		return &Editor{doc: d}
	}
	return &Editor{
		doc:      d,
		base:     d.media,
		pictures: core.ClonePictures(d.media.Pictures),
	}
}

// clonePicturesDeep returns a fully detached copy of ps: each Picture's
// structural fields and its Data bytes are independent of the source, so a caller
// may mutate the returned Data freely. It backs the public [Document.Pictures]
// accessor; the internal hot paths that re-stream Data from source use the
// shallow [core.ClonePictures] instead (they never hand Data out for mutation, so
// paying a deep copy there - per cover, per call - would be waste).
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
