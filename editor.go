package waxlabel

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
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

// AddPicture appends a picture. Its MIME and dimensions are reconciled with the
// image bytes via an authoritative header sniff ([Picture.SniffAuthoritative]):
// when the bytes are a recognized image the sniffed MIME and dimensions win over
// any the caller set, so a mislabeled cover cannot be embedded under a MIME that
// contradicts it. (A file's stored picture, read by the decoders, keeps its own
// MIME - that path fills only, via [Picture.SniffInto].)
func (e *Editor) AddPicture(p Picture) *Editor {
	p.SniffAuthoritative()
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
	// and corrupt on round-trip (e.g. a key containing '='). The key list is reused by
	// the NUL scan below, so it is computed once.
	patchKeys := e.patch.Keys()
	for _, k := range patchKeys {
		if !k.Valid() {
			return nil, fmt.Errorf("%w: %q (keys are uppercase printable ASCII without '=' (spaces and punctuation are allowed); build them with tag.ParseKey or tag.MustKey, which accept any case)", waxerr.ErrInvalidKey, k)
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
	// Reject a NUL byte in any value, chapter title, or picture description this edit
	// introduces: a NUL silently truncates the field on the C-string formats and would
	// otherwise corrupt the write. (D1)
	if err := e.rejectNULValues(editedTags, patchKeys); err != nil {
		return nil, err
	}
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
	// Surface edit-time picture sanity warnings for the pictures this edit authored
	// (added via AddPicture, tracked by addedMask) - an unrecognized image embedded
	// under WithUnrecognizedPictures, an added duplicate, or an added front cover that
	// makes a second - so the user sees what a picture edit introduced without being
	// lectured about a file's pre-existing art (which stays the linter's whole-set
	// concern, mirroring how the chapter checks scope to newly-authored chapters). A
	// faithful carry authors nothing, so it suppresses these via the carried flag.
	if e.picsTouched && !e.carried {
		wp.Report.Warnings = appendPictureWarnings(wp.Report.Warnings, e.pictures, e.addedMask)
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

// rejectNULValues refuses a NUL byte in any value, chapter title, or picture
// description this edit introduces. A NUL silently truncates the field when it is
// written to a C-string format (the ID3 frames MP3/WAV/AIFF store, MP4 atoms), so
// it is refused at the source for every format - even those that round-trip it
// today - rather than written and cut on the formats that cannot hold it. This is
// the library and transfer counterpart to the CLI's OS-level argument guard. The
// tag scan covers only the keys the patch touches (their resolved values are in
// editedTags); the file's untouched pre-existing tags are not re-judged. Pictures
// are scoped to those added on this editor (addedMask), like the rest of the
// added-picture validation; chapters cover the full edited list, which SetChapters
// replaces wholesale. (D1)
func (e *Editor) rejectNULValues(editedTags tag.TagSet, keys []tag.Key) error {
	for _, k := range keys {
		vals, ok := editedTags.Get(k)
		if !ok {
			continue
		}
		for _, v := range vals {
			if containsNUL(v) {
				return nulErr(fmt.Sprintf("tag value for %q", k))
			}
		}
	}
	for i, p := range e.pictures {
		if i < len(e.addedMask) && e.addedMask[i] && containsNUL(p.Description) {
			return nulErr("picture description")
		}
	}
	for _, c := range e.chapters {
		if containsNUL(c.Title) {
			return nulErr("chapter title")
		}
	}
	return nil
}

// containsNUL reports whether s holds a NUL byte, which truncates the field on a
// C-string format. nulErr builds the shared rejection for [Editor.rejectNULValues].
func containsNUL(s string) bool { return strings.IndexByte(s, 0) >= 0 }

func nulErr(what string) error {
	return fmt.Errorf("%w: %s contains a NUL byte", waxerr.ErrInvalidData, what)
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

// appendPictureWarnings adds the non-fatal picture sanity warnings for the
// pictures this edit authored - those with addedMask[i] true (added via
// AddPicture), not the ones Edit seeded from the file. Scoping to the added set is
// the picture counterpart to appendChapterWarnings' new-chapter scope: a copy or a
// tags-only edit must not be lectured about a file's pre-existing art, which the
// linter already covers whole-set. Three checks, each off a predicate the linter
// shares so the rule cannot drift:
//   - invalid-picture: an added picture stored under [core.UnrecognizedMIME]. This
//     reaches here only under WithUnrecognizedPictures (the CLI's --force); without
//     it validateAddedPictures has already rejected the picture.
//   - duplicate-picture: an added picture whose image bytes ([core.Picture.Hash])
//     match another in the set. Reported once per duplicate group an added picture
//     belongs to, whether the twin is another added picture or a pre-existing one.
//   - multiple-front-covers: an added front cover that leaves the set holding more
//     than one front cover (a pair the user did not touch stays the linter's job).
func appendPictureWarnings(ws []core.Warning, pics []core.Picture, addedMask []bool) []core.Warning {
	added := func(i int) bool { return i < len(addedMask) && addedMask[i] }

	// One cheap pass over the set (no hashing): flag each added unrecognized image,
	// tally front covers, record the byte lengths of added pictures (for the duplicate
	// scan below), and note whether anything was added at all.
	var anyAdded, frontAdded bool
	fronts := 0
	addedLens := map[int]bool{}
	for i, p := range pics {
		if added(i) {
			anyAdded = true
			addedLens[len(p.Data)] = true
			if p.Unrecognized() {
				ws = core.Warn(ws, core.WarnInvalidPicture, fmt.Sprintf(
					"added %s picture is not a recognized image type (%s)", p.Type, p.MIME))
			}
		}
		if p.Type == core.PicFrontCover {
			fronts++
			if added(i) {
				frontAdded = true
			}
		}
	}
	// Nothing added (e.g. a removal-only edit, where addedMask is all-false): there is
	// nothing to warn about, and - importantly - no picture has been hashed.
	if !anyAdded {
		return ws
	}

	// Duplicate detection. Two images of different byte length can never be equal, so
	// hash only pictures whose length some added picture shares - a large pre-existing
	// cover of a different size is never SHA-256'd. Then warn once per duplicate group an
	// added picture belongs to (whether its twin is another added or a carried picture).
	hashes := map[int][32]byte{}
	counts := map[[32]byte]int{}
	for i, p := range pics {
		if !addedLens[len(p.Data)] {
			continue
		}
		h := p.Hash()
		hashes[i] = h
		counts[h]++
	}
	warned := map[[32]byte]bool{}
	for i := range pics { // pic order, so the warnings are deterministic
		if h, ok := hashes[i]; ok && added(i) && counts[h] > 1 && !warned[h] {
			warned[h] = true
			ws = core.Warn(ws, core.WarnDuplicatePicture, duplicatePictureMessage(pics[i].Type))
		}
	}

	if frontAdded && fronts > 1 {
		ws = core.Warn(ws, core.WarnMultipleFrontCovers, multipleFrontCoversMessage(fronts))
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
