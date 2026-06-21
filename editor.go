package waxlabel

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Editor records mutations against a [Document] without changing it. Mutations
// accumulate as a presence-aware [tag.TagPatch] (for canonical fields) plus a
// working picture list; [Editor.Prepare] resolves them into a [Plan]. The
// editor methods return the editor for chaining.
type Editor struct {
	doc         *Document
	base        *core.Media
	patch       tag.TagPatch
	pictures    []core.Picture
	picsTouched bool
	// addedMask is parallel to pictures: addedMask[i] is true when pictures[i] was
	// added on this editor via AddPicture (so Prepare validates it), false for a
	// picture Edit seeded from the file. A mask rather than a second slice lets
	// RemovePictures filter both in lockstep with a single evaluation of the caller's
	// match predicate, so a side-effecting or non-deterministic matcher cannot be
	// called twice or desync the added set from what will be written.
	addedMask       []bool
	chapters        []core.Chapter
	chaptersTouched bool
	// carried marks this editor as a faithful carry from a source (the transfer
	// engine), not a user-authored edit, so [Editor.Prepare] suppresses the edit-time
	// sanity warnings that flag authoring mistakes - the chapter past-duration /
	// duplicate-start checks and the single-valued-multi note. Copying a file must not
	// lecture about metadata the user authored none of (a source's own conflicting
	// single-valued key, or its chapter timings).
	carried bool
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
	// Pad the mask for any Edit-seeded pictures not yet covered, then mark this one
	// added, keeping addedMask parallel to pictures.
	for len(e.addedMask) < len(e.pictures) {
		e.addedMask = append(e.addedMask, false)
	}
	e.pictures = append(e.pictures, p)
	e.addedMask = append(e.addedMask, true)
	e.picsTouched = true
	return e
}

// RemovePictures drops every picture for which match returns true. match is
// evaluated exactly once per picture, and the parallel added-mask is filtered with
// the same verdicts, so an added-then-removed picture is not validated by Prepare
// and a side-effecting/non-deterministic matcher cannot double-fire or desync.
func (e *Editor) RemovePictures(match func(Picture) bool) *Editor {
	pics := make([]core.Picture, 0, len(e.pictures))
	mask := make([]bool, 0, len(e.pictures))
	for i, p := range e.pictures {
		if match(p) {
			continue
		}
		pics = append(pics, p)
		mask = append(mask, i < len(e.addedMask) && e.addedMask[i])
	}
	e.pictures, e.addedMask = pics, mask
	e.picsTouched = true
	return e
}

// ClearPictures removes all pictures.
func (e *Editor) ClearPictures() *Editor {
	e.pictures = nil
	e.addedMask = nil
	e.picsTouched = true
	return e
}

// SetChapters replaces the whole chapter list. Chapters are a timeline, so the
// list is sorted by start time (stably, preserving the order of chapters that
// share a start) - an out-of-order argument would otherwise lose a start when a
// container encodes spans relative to the previous chapter. A format that cannot
// write chapters reports it via [Capabilities]; MP4/M4B and Matroska are writable.
// MP4 caps a chapter list at 255 (the Nero chpl limit) - a larger list is rejected
// at [Editor.Prepare]; Matroska has no such cap.
func (e *Editor) SetChapters(chs ...Chapter) *Editor {
	e.chapters = slices.Clone(chs)
	slices.SortStableFunc(e.chapters, func(a, b Chapter) int { return cmp.Compare(a.Start, b.Start) })
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
// original parsed document, not this editor's pending edits - so pictures or
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

	// An editor from a zero-value Document (Document.Edit guards that case) has no
	// base media to plan against; report it cleanly rather than deref a nil base
	// below.
	if e.base == nil {
		return nil, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}

	// Validate every key the edit touches before it can reach the native writer
	// and corrupt on round-trip (e.g. a key containing '=').
	for _, k := range e.patch.Keys() {
		if !k.Valid() {
			return nil, fmt.Errorf("%w: %q (keys are uppercase ASCII without '='; build them with tag.ParseKey or tag.MustKey)", waxerr.ErrInvalidKey, k)
		}
	}

	// Share the native document and properties rather than deep-copying them:
	// planning only reads the native (re-cloning the blocks it keeps), so a full
	// copy here - which would duplicate every block body, including embedded
	// cover art - is pure waste. Only the canonical tags (cloned by the patch)
	// and the picture set are replaced.
	editedTags := e.patch.Apply(e.base.Tags)
	// Collapse any key left present with a zero-length value slice to absent before
	// the codec plans or Changes() diffs: a Set/Add of no values on an absent key
	// (or a clear-then-empty-add) leaves the key present-but-empty, which no codec
	// persists - so without this the plan would diff a phantom add against an
	// identical file, reporting a change and bumping mtime over bytes that never
	// moved (#3). The scope is strictly zero-length: a present [""] (what `set KEY=`
	// produces) is a distinct, CLI-reachable empty value and is left untouched.
	dropEmptyValuedKeys(&editedTags)
	edited := &core.Media{
		Format:      e.base.Format,
		Properties:  e.base.Properties,
		Tags:        editedTags,
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
	// Validate only the pictures added on this editor (not the file's pre-existing
	// ones, which Edit seeded): a direct caller handing AddPicture empty or junk
	// bytes would otherwise have them embedded as application/octet-stream. The CLI
	// guards the common mistake earlier in loadCovers; this is the library-side
	// safety net. WithUnrecognizedPictures opts a deliberately exotic cover back in
	// (and the transfer engine opts out wholesale, carrying already-embedded art).
	if !wo.AllowUnrecognizedPictures {
		if err := validateAddedPictures(e.pictures, e.addedMask); err != nil {
			return nil, err
		}
	}

	codec, ok := core.ForFormat(e.base.Format)
	if !ok {
		return nil, fmt.Errorf("%w: no writer for %s", waxerr.ErrUnsupportedFormat, e.base.Format)
	}
	// Refuse to silently drop chapters on a format that cannot store them. This is a
	// format-agnostic capability gate (it reads the destination's Chapters write
	// level); the analogous picture refusal - a cover onto WebM - is enforced
	// separately as a WebM-specific check inside the Matroska writer, not here, so the
	// two share intent but neither site nor mechanism. Guard on a non-empty list so
	// ClearChapters() on a chapterless format stays a harmless no-op. A format-incapable
	// destination in a transfer is handled earlier (ProjectTransfer marks chapters
	// Dropped before SetChapters runs), so chaptersTouched is false there and this
	// never fires.
	if e.chaptersTouched && len(e.chapters) > 0 &&
		codec.Capabilities(e.base, wo).Chapters.Write < core.AccessPartial {
		return nil, fmt.Errorf("%w: chapters cannot be written to %s %s file",
			waxerr.ErrUnsupportedTag, core.IndefiniteArticle(e.base.Format.String()), e.base.Format)
	}
	wp, err := codec.Plan(context.Background(), e.base, edited, wo)
	if err != nil {
		return nil, err
	}
	// Surface edit-time chapter sanity warnings (a start past the file end, or two
	// chapters sharing a start) on the plan report so they flow through the same
	// Warnings path the CLI and JSON already render. Only chapters this edit
	// introduces are checked - not the file's pre-existing chapters, which the CLI's
	// --add-chapter merges into the SetChapters list (so warning about them would
	// flag chapters the user never touched). A faithful carry (the transfer engine)
	// authors nothing, so it suppresses these entirely via the carried flag.
	if e.chaptersTouched && !e.carried {
		wp.Report.Warnings = appendChapterWarnings(wp.Report.Warnings, e.chapters, e.base.Chapters, e.base.Properties.Duration())
	}
	// Surface a known single-valued key the edit leaves holding multiple values as a
	// non-fatal plan warning, so a library caller sees the cardinality the typed
	// projection would silently collapse to its first value (#17). It is computed off
	// the same base->result diff Plan.Changes() exposes, so it names exactly the keys
	// the CLI's --strict guardrail acts on - and lets the CLI drop its separate
	// stderr note, reporting the signal once (now also in --json warnings). A faithful
	// carry suppresses it (like the chapter checks): a copy must not flag the source's
	// own conflicting single-valued key as if the user authored it.
	if !e.carried {
		wp.Report.Warnings = appendSingleValuedWarnings(wp.Report.Warnings, e.base.Tags, planResultTags(wp, edited))
	}
	return &Plan{doc: e.doc, plan: wp, opts: wo}, nil
}

// planResultTags returns the tag set the plan will write: the codec's computed
// result when present, else the edited set (a NoOp plan may carry no result). It
// is the same source [Plan.Changes] diffs against, so a warning derived from it
// matches the plan's reported changes.
func planResultTags(wp *core.WritePlan, edited *core.Media) tag.TagSet {
	if wp.Result != nil {
		return wp.Result.Tags
	}
	return edited.Tags
}

// appendSingleValuedWarnings adds a WarnSingleValuedMulti for every known
// single-valued key the edit changes into holding more than one value. It diffs
// base against the plan's result (so an unchanged pre-existing multi - already
// reported by Lint - is not re-flagged here) and uses [tag.Key.SingleValuedMulti],
// the shared cardinality predicate, so the library warning, the linter's finding,
// and the CLI's --strict guardrail cannot disagree on the rule.
func appendSingleValuedWarnings(ws []core.Warning, base, result tag.TagSet) []core.Warning {
	for _, c := range tag.Diff(base, result) {
		if c.Key.SingleValuedMulti(len(c.New)) {
			ws = core.Warn(ws, core.WarnSingleValuedMulti, fmt.Sprintf(
				"%s is single-valued but is being given %d values; the typed projection reads only the first",
				c.Key, len(c.New)))
		}
	}
	return ws
}

// appendChapterWarnings adds the non-fatal chapter sanity warnings for the
// chapters this edit introduces (those in chapters but not in base):
// WarnChapterPastDuration for a newly-added chapter starting beyond the file's
// playable length, and WarnDuplicateChapter for a start a newly-added chapter
// shares with another. Scoping to the new chapters means a pre-existing on-disk
// chapter merged into the list (the CLI's --add-chapter appends to the file's
// chapters) is not flagged, while a collision the new chapter causes still is.
// chapters is sorted by Start (SetChapters), so equal starts are adjacent and each
// distinct collision is reported once. The past-duration check is gated on a known,
// non-zero duration: a truncated or header-only file reports duration 0 (and
// already warns no-audio), which would otherwise flag every chapter as beyond 0:00.
func appendChapterWarnings(ws []core.Warning, chapters, base []core.Chapter, duration time.Duration) []core.Warning {
	baseSet := make(map[core.Chapter]bool, len(base))
	for _, c := range base {
		baseSet[c] = true
	}
	isNew := func(c core.Chapter) bool { return !baseSet[c] }

	if duration > 0 {
		for _, c := range chapters {
			if isNew(c) && c.Start > duration {
				ws = core.Warn(ws, core.WarnChapterPastDuration, fmt.Sprintf(
					"chapter at %s starts past the file duration (%s); check the timestamp",
					core.FormatChapterTime(c.Start), core.FormatChapterTime(duration)))
			}
		}
	}
	// Walk each run of equal starts once; warn only when the run contains a
	// newly-added chapter, so a collision among untouched pre-existing chapters stays
	// quiet while one the edit introduces is surfaced.
	for i := 0; i < len(chapters); {
		j := i
		for j+1 < len(chapters) && chapters[j+1].Start == chapters[i].Start {
			j++
		}
		if j > i {
			runHasNew := false
			for k := i; k <= j; k++ {
				if isNew(chapters[k]) {
					runHasNew = true
					break
				}
			}
			if runHasNew {
				ws = core.Warn(ws, core.WarnDuplicateChapter, fmt.Sprintf(
					"two or more chapters share the start %s", core.FormatChapterTime(chapters[i].Start)))
			}
		}
		i = j + 1
	}
	return ws
}

// validateAddedPictures rejects an added picture (one with addedMask[i] true) whose
// bytes are not a recognized image - an empty payload or a header [IsRecognizedImage]
// does not know. It re-sniffs Data and ignores any caller-declared MIME, so
// MIME:"image/png" on junk bytes is still rejected. The message names the picture's
// type and the opt-out. It is the library counterpart to the CLI's loadCovers
// pre-check, run at Prepare so a direct API user cannot embed an
// application/octet-stream picture by mistake; WithUnrecognizedPictures (and the
// CLI's --force) skip it for a deliberately exotic cover. Pre-existing pictures
// Edit seeded (addedMask false) are never re-judged.
func validateAddedPictures(pics []core.Picture, addedMask []bool) error {
	for i, p := range pics {
		if i < len(addedMask) && addedMask[i] && !IsRecognizedImage(p.Data) {
			return fmt.Errorf("%w: added %q picture is not a recognized image "+
				"(PNG/JPEG/GIF/WebP/BMP/TIFF); pass WithUnrecognizedPictures to embed it anyway",
				waxerr.ErrInvalidData, p.Type)
		}
	}
	return nil
}

// dropEmptyValuedKeys removes every key that is present with a zero-length value
// slice, making it absent. It is the [Editor.Prepare] normalization that keeps a
// plan honest (see the call site): such a key is what an Add/Set of no values
// against an absent key produces, and no codec stores it, so leaving it present
// would make IsNoOp/Changes disagree with the bytes actually written. It is
// scoped strictly to a zero-length slice - a present [""] (one empty string) is a
// distinct, intentional state and is preserved.
func dropEmptyValuedKeys(ts *tag.TagSet) {
	for _, k := range ts.Keys() {
		if vs, ok := ts.Get(k); ok && len(vs) == 0 {
			ts.Delete(k)
		}
	}
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
