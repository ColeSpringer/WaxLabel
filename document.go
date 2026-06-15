package waxlabel

import (
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// Document is an immutable, detached, serializable view of a parsed file. It
// holds no file descriptor and has no Close method, so a caller may scan it,
// cache it, and discard it freely. Accessors return detached deep copies of
// structural data; only [Picture] payload bytes are shared (read-only), since
// deep-copying megabytes per call would be wasteful.
//
// A Document is safe for concurrent reads.
type Document struct {
	media *core.Media

	// Write-source resolution (never an OS resource the Document owns):
	// path is set by ParseFile; src is an in-memory source set by OpenSource.
	path string
	src  core.ReaderAtSized
}

// Format returns the detected container/codec.
func (d *Document) Format() Format { return d.media.Format }

// Tags returns the authoritative, presence-aware canonical tag set as a deep
// copy. This is the source of truth; the typed [Document.Fields] is derived
// from it.
func (d *Document) Tags() tag.TagSet { return d.media.Tags.Clone() }

// Get returns the values for a canonical key and whether it is present.
func (d *Document) Get(key tag.Key) ([]string, bool) { return d.media.Tags.Get(key) }

// Fields returns the typed convenience projection of the canonical tags. It is
// lossy (a struct cannot express presence); use [Document.Tags] when that
// distinction matters.
func (d *Document) Fields() tag.Tags { return tag.Project(d.media.Tags) }

// Properties returns the audio stream properties (a copy).
func (d *Document) Properties() Properties { return d.media.Properties.Clone() }

// Pictures returns the embedded pictures. Structural fields are copied; each
// Picture's Data is shared read-only.
func (d *Document) Pictures() []Picture { return core.ClonePictures(d.media.Pictures) }

// Families returns the tag-family/source view: which family supplied each
// canonical value, its scope, and whether it won the projection (unselected
// entries for a key signal a conflict).
func (d *Document) Families() []FamilyValue {
	out := make([]FamilyValue, len(d.media.Families))
	copy(out, d.media.Families)
	for i := range out {
		out[i].Values = append([]string(nil), out[i].Values...)
	}
	return out
}

// Warnings returns the non-fatal conditions found during parse (preserved
// legacy tags, inherited encoder stamps, unknown blocks, and so on).
func (d *Document) Warnings() []Warning { return core.CloneWarnings(d.media.Warnings) }

// Native returns a deep copy of the format's native document for inspection.
// To edit it, use [Editor.Native].
func (d *Document) Native() NativeDoc {
	if d.media.Native == nil {
		return nil
	}
	return d.media.Native.Clone()
}

// Identity returns the recorded source identity (path, size, timestamps, and
// structural fingerprint) used for save-back change detection.
func (d *Document) Identity() Identity { return d.media.Identity }

// Capabilities reports what the format can do under the given write options
// (capabilities are option-dependent).
func (d *Document) Capabilities(opts ...WriteOption) Capabilities {
	codec, ok := core.ForFormat(d.media.Format)
	if !ok {
		return Capabilities{Format: d.media.Format, ReadOnly: true}
	}
	return codec.Capabilities(resolveWriteOptions(opts))
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
	return &Editor{
		doc:      d,
		base:     d.media,
		pictures: core.ClonePictures(d.media.Pictures),
	}
}
