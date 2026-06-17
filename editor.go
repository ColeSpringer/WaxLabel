package waxlabel

import (
	"context"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Editor records mutations against a [Document] without changing it. Mutations
// accumulate as a presence-aware [tag.TagPatch] (for canonical fields) plus a
// working picture list; [Editor.Prepare] resolves them into a [Plan]. The
// editor methods return the editor for chaining.
type Editor struct {
	doc             *Document
	base            *core.Media
	patch           tag.TagPatch
	pictures        []core.Picture
	picsTouched     bool
	chapters        []core.Chapter
	chaptersTouched bool
}

// Apply records an explicit patch (set/clear/add operations) after any already
// recorded, so later edits win on conflicts.
func (e *Editor) Apply(p tag.TagPatch) *Editor {
	e.patch.Append(p)
	return e
}

// Set replaces a key's values (present, possibly empty).
func (e *Editor) Set(key tag.Key, vals ...string) *Editor {
	e.patch.Set(key, vals...)
	return e
}

// Clear removes a key (makes it absent).
func (e *Editor) Clear(key tag.Key) *Editor {
	e.patch.Clear(key)
	return e
}

// Add appends values to a key.
func (e *Editor) Add(key tag.Key, vals ...string) *Editor {
	e.patch.Add(key, vals...)
	return e
}

// SetTags applies the non-empty fields of a typed [tag.Tags] as sugar (it
// compiles to a patch of Set operations; it cannot clear fields).
func (e *Editor) SetTags(t tag.Tags) *Editor { return e.Apply(t.Patch()) }

// AddPicture appends a picture. MIME and dimensions are sniffed from the data
// when not already set.
func (e *Editor) AddPicture(p Picture) *Editor {
	p.SniffInto()
	e.pictures = append(e.pictures, p)
	e.picsTouched = true
	return e
}

// RemovePictures drops every picture for which match returns true.
func (e *Editor) RemovePictures(match func(Picture) bool) *Editor {
	e.pictures = slices.DeleteFunc(e.pictures, match)
	e.picsTouched = true
	return e
}

// ClearPictures removes all pictures.
func (e *Editor) ClearPictures() *Editor {
	e.pictures = nil
	e.picsTouched = true
	return e
}

// SetChapters replaces the whole chapter list. Chapters are stored in the order
// given. A format that cannot write chapters reports it via [Capabilities]; only
// MP4 (Nero chpl) is writable in this version, and it caps a chapter list at 255
// — a larger list is rejected at [Editor.Prepare].
func (e *Editor) SetChapters(chs ...Chapter) *Editor {
	e.chapters = slices.Clone(chs)
	e.chaptersTouched = true
	return e
}

// ClearChapters removes all chapters.
func (e *Editor) ClearChapters() *Editor {
	e.chapters = nil
	e.chaptersTouched = true
	return e
}

// Native returns the native editing hatch for inspection. It reflects the
// original parsed document, not this editor's pending edits — so pictures or
// tags added on the editor are not visible here until after a save and reparse.
// Structural native mutation (arbitrary block add/remove, multiple comment
// blocks, the vendor string) lands with the public codec packages at v1.0; in
// M0 the canonical path (tags and pictures) plus this read view cover the
// common cases.
func (e *Editor) Native() NativeEditor {
	return NativeEditor{base: e.base}
}

// Prepare resolves the recorded mutations into a [Plan] under the given write
// options. The plan's [Plan.Report] describes exactly what executing it will
// do; nothing is written yet, and Prepare performs no I/O (the parsed document
// holds everything the planner needs).
func (e *Editor) Prepare(opts ...WriteOption) (*Plan, error) {
	wo := resolveWriteOptions(opts)

	// Validate every key the edit touches before it can reach the native writer
	// and corrupt on round-trip (e.g. a key containing '=').
	for _, k := range e.patch.Keys() {
		if !k.Valid() {
			return nil, fmt.Errorf("%w: %q", waxerr.ErrInvalidKey, k)
		}
	}

	// Share the native document and properties rather than deep-copying them:
	// planning only reads the native (re-cloning the blocks it keeps), so a full
	// copy here — which would duplicate every block body, including embedded
	// cover art — is pure waste. Only the canonical tags (cloned by the patch)
	// and the picture set are replaced.
	edited := &core.Media{
		Format:      e.base.Format,
		Properties:  e.base.Properties,
		Tags:        e.patch.Apply(e.base.Tags),
		Pictures:    e.base.Pictures,
		Chapters:    e.base.Chapters,
		Families:    e.base.Families,
		Warnings:    e.base.Warnings,
		Native:      e.base.Native,
		Identity:    e.base.Identity,
		AudioStart:  e.base.AudioStart,
		AudioEnd:    e.base.AudioEnd,
		AudioRanges: e.base.AudioRanges,
	}
	if e.picsTouched {
		edited.Pictures = e.pictures
	}
	if e.chaptersTouched {
		edited.Chapters = e.chapters
	}
	if err := validatePictures(edited.Pictures); err != nil {
		return nil, err
	}

	codec, ok := core.ForFormat(e.base.Format)
	if !ok {
		return nil, fmt.Errorf("%w: no writer for %s", waxerr.ErrUnsupportedFormat, e.base.Format)
	}
	wp, err := codec.Plan(context.Background(), e.base, edited, wo)
	if err != nil {
		return nil, err
	}
	return &Plan{doc: e.doc, plan: wp, opts: wo}, nil
}

// validatePictures enforces the single-icon rule: picture types 1 and 2 must
// each appear at most once.
func validatePictures(pics []core.Picture) error {
	icon, otherIcon := core.CountIcons(pics)
	if icon > 1 {
		return fmt.Errorf("%w: more than one 32x32 file-icon picture (type 1)", waxerr.ErrInvalidData)
	}
	if otherIcon > 1 {
		return fmt.Errorf("%w: more than one other-file-icon picture (type 2)", waxerr.ErrInvalidData)
	}
	return nil
}

// NativeEditor is the (currently read-only) native hatch. It exposes the
// native document's structure so a caller can see exactly what is preserved.
type NativeEditor struct {
	base *core.Media
}

// Entries summarizes the native metadata blocks.
func (n NativeEditor) Entries() []NativeEntry {
	if n.base == nil || n.base.Native == nil {
		return nil
	}
	return n.base.Native.Describe()
}
