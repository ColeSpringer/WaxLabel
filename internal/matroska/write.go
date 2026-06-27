package matroska

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan computes the byte-level rewrite that turns the original Matroska/WebM into
// the edited media. It is preservation-first, mirroring the WAV/MP4 pattern: the
// cluster media is copied byte-for-byte and only the affected Segment children
// (Tags, Info.Title, Attachments) are re-rendered.
//
// The size change is typically absorbed into a reserved Void element so the clusters
// do not move - keeping every Cues/SeekHead position valid - which is the layout
// mkvmerge and ffmpeg both write (SeekHead, Void, ..., Clusters). Only the SeekHead
// entries for the header elements that shift within the rebuilt header are patched,
// in place at their original width, and the affected CRC-32s recomputed. Two cases
// force the tail to move instead (see planShift): the file has no usable Void, or a
// shift pushes an indexed SeekPosition across a VINT-width boundary so it no longer
// fits its original-width slot (patchSeekAbsorb fails). Seek targets and CRC-32s stay
// correct in every case, and the cluster media is always copied byte-for-byte.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("matroska: edited media has no Matroska native document")
	}
	if d.wb == nil {
		return nil, fmt.Errorf("%w: Matroska document must be re-parsed before editing", waxerr.ErrInvalidData)
	}

	ch := detectChanges(base, edited)
	report := core.WriteReport{Format: core.FormatMatroska, BytesBefore: edited.Identity.Size}

	if !ch.any() {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// The canonical keys whose values changed, computed once and threaded into the
	// preservation check and every group render: a changed key must reach (and be
	// dropped from) every scope that held it, not just album scope.
	ek := editedKeySet(base.Tags, edited.Tags)

	// A title edit normally rewrites only Info.Title (ch.title). But a TITLE
	// SimpleTag carried at any Tag scope also projects into the canonical Title, so
	// the cross-scope removal contract requires dropping it too - and that drop only
	// happens on the Tags re-render path (renderTags). Force ch.simple so a
	// title-only edit still reaches a stale scoped TITLE, the way every other key
	// does; otherwise the projection would read two titles after the edit.
	if ch.title && !ch.simple && hasManagedTitleTag(d.groups) {
		ch.simple = true
	}

	// A source can carry its canonical title only in a scoped TITLE SimpleTag, with no
	// Segment Info title. When Tags are re-rendered, renderTags drops managed TITLE tags
	// because Info.Title is the canonical home. If an Info element exists, force a title
	// render so that scoped-only title migrates there instead of disappearing on an
	// unrelated tag edit.
	if ch.simple && !ch.title && !d.hasSegTitle && d.wb.info != nil {
		if _, ok := edited.Tags.First(tag.Title); ok {
			ch.title = true
		}
	}

	// WebM does not include the Attachments element in its subset, so refuse to
	// write cover art into a webm file rather than emit something strict WebM
	// validators reject. Plain tag/Title writes to .webm remain fine. This Plan-level
	// refusal is the backstop for a direct Editor.AddPicture; the transfer path is
	// gated earlier by Capabilities (matroska.go reports pictures.Write=AccessNone
	// for a WebM file, so copy/PlanTransfer drops the cover). Both key on isWebM and
	// must stay in sync.
	if ch.pictures && isWebM(d.docType) {
		return nil, fmt.Errorf("%w: cover art cannot be written to %s WebM file (Attachments is not in the WebM subset)",
			waxerr.ErrUnsupportedTag, core.IndefiniteArticle("WebM"))
	}

	// The segment title lives in Info.Title; a file with no Info element (Info is
	// mandatory, so this is malformed) cannot receive one.
	if ch.title && d.wb.info == nil {
		return nil, fmt.Errorf("%w: cannot write a title to a Matroska file that has no Info element",
			waxerr.ErrUnsupportedTag)
	}

	// A SeekHead/Cues whose structure could not be captured (e.g. an over-limit
	// declared size) cannot be repositioned, and copying it verbatim while other
	// elements move would leave its offsets pointing at the wrong bytes - refuse
	// rather than silently corrupt the index.
	if err := checkIndexCaptured(d.wb); err != nil {
		return nil, err
	}
	if err := checkPreservable(d, ch, ek); err != nil {
		return nil, err
	}

	// An edit that changes the value of an album-scope SimpleTag carrying structure the
	// flat canonical model cannot hold (a secondary TagLanguage, a TagBinary value, or
	// nested sub-tags) re-emits that value flat and drops the structure. The unchanged-tag
	// case is preserved verbatim by buildAlbumGroup, so this fires only when the key was
	// edited and the old bytes cannot be kept. Emit it as a keyed plan-time warning here,
	// before planAbsorb, so an absorb-then-shift retry cannot render it twice.
	if ch.simple {
		if keys := tagStructureDropped(d, ek); len(keys) > 0 {
			report.Warnings = core.WarnKeyed(report.Warnings, core.WarnTagStructureDropped,
				"an edited album tag dropped its secondary language, binary value, or nested sub-tags", keys...)
		}
	}

	// Re-rendering a default edition that carried nested sub-chapters or
	// secondary-language titles drops that structure (the flat chapter model cannot
	// hold it). Surface it as a plan-time warning rather than flattening silently -
	// the established precedent for a lossy chapter write. A full clear is a removal,
	// not a flatten, so it does not warn.
	if ch.chapters && len(edited.Chapters) > 0 && d.chapters != nil && d.chapters.defLossy {
		report.Warnings = core.Warn(report.Warnings, core.WarnChaptersFlattened,
			"chapter edit dropped the default edition's nested sub-chapters or secondary-language titles")
	}

	// Matroska stores cover art as cover.<ext>/small_cover.<ext>, so only the front
	// cover's role round-trips. Other roles read back as Other, while the description is
	// preserved. Surface that role-only loss as a plan-time warning; WebM picture writes
	// were already refused above.
	if ch.pictures && core.PicturesLoseMetadata(edited.Pictures, core.PictureLossRoleOnly) {
		report.Warnings = core.Warn(report.Warnings, core.WarnPictureMetadataDropped,
			"Matroska preserves only the front cover's role; other picture roles read back as Other")
	}

	pl, err := planAbsorb(d, base, edited, ch, ek, report)
	if err == nil {
		return pl, nil
	}
	if !isFallback(err) {
		return nil, err
	}
	return planShift(d, base, edited, ch, ek, report)
}

// changes records which Segment children an edit touches: the SimpleTag set (any
// canonical key except Title), the Info.Title, the Attachments cover set, and the
// Chapters element.
type changes struct {
	simple   bool
	title    bool
	pictures bool
	chapters bool
}

func (c changes) any() bool { return c.simple || c.title || c.pictures || c.chapters }

// detectChanges splits a tag edit into its Title part (which lives in Info.Title)
// and the rest (which lives in Tags SimpleTags), plus the picture and chapter sets.
func detectChanges(base, edited *core.Media) changes {
	bt, _ := base.Tags.Get(tag.Title)
	et, _ := edited.Tags.Get(tag.Title)
	b := base.Tags.Clone()
	e := edited.Tags.Clone()
	b.Delete(tag.Title)
	e.Delete(tag.Title)
	return changes{
		// Compare the whole Title value list, not just the first: changing or adding
		// a later Title value is a real edit, not a no-op (only the first lands in
		// the single-valued Info.Title, but the edit must not be silently dropped).
		simple:   !b.Equal(e),
		title:    !slices.Equal(bt, et),
		pictures: !core.EqualPictures(base.Pictures, edited.Pictures),
		chapters: !core.EqualChapters(base.Chapters, edited.Chapters),
	}
}

// isWebM reports whether the EBML DocType is the WebM subset, matching the
// case-insensitive comparison the reader uses for the container label.
func isWebM(docType string) bool { return strings.EqualFold(docType, "webm") }

// checkIndexCaptured refuses the edit when a SeekHead/Cues element cannot be
// safely rewritten: more than one is present (a linked index - only the last is
// captured, so the others would be copied with stale offsets), or its single
// instance was not captured at parse (a read failure or over-limit declared size).
// Copying such an element verbatim while other elements shift corrupts its offsets.
func checkIndexCaptured(wb *writeBase) error {
	seeks, cues := 0, 0
	for _, c := range wb.children {
		switch c.id {
		case idSeekHead:
			seeks++
		case idCues:
			cues++
		}
	}
	if seeks > 1 || cues > 1 {
		return fmt.Errorf("%w: multiple SeekHead/Cues elements (a linked index) are not yet writable",
			waxerr.ErrUnsupportedTag)
	}
	if (seeks == 1 && wb.seek == nil) || (cues == 1 && wb.cues == nil) {
		return fmt.Errorf("%w: a Matroska index element (SeekHead/Cues) could not be read for rewrite",
			waxerr.ErrUnsupportedTag)
	}
	return nil
}

// checkPreservable refuses the edit when an element the writer must copy verbatim
// could not be captured (its bytes exceeded the alloc limit, so captureRaw
// returned nil) - dropping it would silently lose data. It covers the groups and
// non-canonical SimpleTags a tag edit preserves and the non-image attachments a
// cover edit preserves.
func checkPreservable(d *doc, ch changes, ek map[tag.Key]bool) error {
	tooBig := func(what string) error {
		return fmt.Errorf("%w: a Matroska %s is too large to rewrite within the alloc limit", waxerr.ErrUnsupportedTag, what)
	}
	if ch.simple {
		albumIdx := albumGroupIndex(d.groups)
		for i, g := range d.groups {
			if i == albumIdx {
				// Synced in place: every SimpleTag the edit keeps verbatim - a non-canonical
				// tag, OR a managed tag whose canonical key was not edited (preserved with its
				// language/binary/nested structure) - needs its captured bytes. A tag the edit
				// drops is re-emitted flat from the canonical set and needs no raw.
				for _, st := range g.tags {
					if droppedByEdit(st, ek) || isManagedTitle(st) {
						continue // re-emitted from the canonical set, or migrated to Info.Title: no raw needed
					}
					if st.raw == nil {
						return tooBig("tag")
					}
				}
				continue
			}
			if !groupTouchedBy(g, ek) {
				// Preserved verbatim: needs the whole Tag element's bytes.
				if g.raw == nil {
					return tooBig("tag group")
				}
				continue
			}
			// Re-rendered to drop its edited keys: every surviving SimpleTag needs
			// its bytes, and a scope-narrowing group needs its Targets bytes too
			// (else the rebuild would silently lose the narrowing). A target-less
			// group needs neither - it carries only the kept SimpleTags.
			kept := 0
			for _, st := range g.tags {
				if droppedByEdit(st, ek) {
					continue // its value now lives at album scope
				}
				if st.raw == nil {
					return tooBig("tag")
				}
				kept++
			}
			if kept > 0 && g.targetsRaw == nil && narrowsScope(g) {
				return tooBig("tag targets")
			}
		}
	}
	if ch.pictures {
		for _, a := range d.attachments {
			if !a.image && a.raw == nil {
				return tooBig("attachment")
			}
		}
	}
	if ch.chapters && d.chapters != nil {
		// The default edition is re-rendered from the parsed model, but every other
		// edition is copied from its captured bytes - refuse if one was too large to
		// capture rather than silently dropping it.
		for i, ed := range d.chapters.editions {
			if i != d.chapters.defIdx && ed.raw == nil {
				return tooBig("chapter edition")
			}
		}
	}
	return nil
}

// errFallback signals that the absorption path cannot apply (no reserved Void, or
// the edited header does not fit) so Plan should try the shift path instead. It
// is internal control flow, never returned to the caller.
var errFallback = fmt.Errorf("matroska: absorption not applicable")

func isFallback(err error) bool { return errors.Is(err, errFallback) }

// renderTags builds the new Tags element bytes from the edited canonical set,
// returning nil when the result would be empty (so the Tags element is dropped).
// It also returns the new group list for the result document. Non-canonical
// SimpleTags (custom, technical, binary, nested) are preserved verbatim from their
// captured raw bytes; canonical keys are synced into the album-scope group, written
// under their Matroska-spec names. A non-album group is preserved verbatim unless it
// carries a key the edit changed, in which case it is re-rendered to drop that key
// (the new value lands at album scope). Title is excluded - it lives in Info.Title.
func renderTags(d *doc, base, edited tag.TagSet, ek map[tag.Key]bool) (raw []byte, groups []tagGroup) {
	albumIdx := albumGroupIndex(d.groups)
	covered, albumOwn := coveredByOtherScopes(d.groups, albumIdx, ek)
	var content []byte

	for i, g := range d.groups {
		newGroup, gb, keep := renderGroup(g, base, edited, covered, albumOwn, ek, i == albumIdx)
		if !keep {
			continue
		}
		content = append(content, gb...)
		groups = append(groups, newGroup)
	}

	// No album group existed: create one carrying the canonical set.
	if albumIdx < 0 {
		newGroup, gb := buildAlbumGroup(nil, base, edited, covered, albumOwn, ek)
		if gb != nil {
			content = append(content, gb...)
			groups = append(groups, newGroup)
		}
	}

	if len(content) == 0 {
		return nil, nil
	}
	// A Tags element carries a leading CRC-32 when the source Tags master did (the
	// mkvmerge convention of a CRC on the master).
	return masterElement(idTags, content, d.wb.tagsCRC), groups
}

// coveredByOtherScopes returns, per canonical key, the projected values a non-album
// group will still carry after the edit, so the album-group sync can leave an
// unchanged value at its own scope instead of re-emitting it at album scope (which
// would duplicate it on every save and risk a spurious cross-scope conflict). The
// values are projected through projectTag - not a bare MatroskaTagKey lookup - so a
// slash number (PART_NUMBER=3/12, which projects to TrackNumber=3 AND TrackTotal=12)
// contributes to both canonical keys; a key-only set would miss the second and
// duplicate it at album scope. Carrying the values (not just the keys) lets the sync
// subtract exactly what a narrower scope preserves, so a key split across scopes with
// different values (ENCODER album=Lavf + track=Lavc) keeps its album-only part.
//
// A SimpleTag the edit will drop (droppedByEdit) carries nothing forward, so it is
// excluded - otherwise its projected values would be subtracted from the album sync as
// if still preserved, and a slash number whose component was edited would lose its
// unedited half (it is dropped from the track group yet skipped at album scope). The
// covered set must reflect post-edit survivors, using the same drop predicate as the
// renderer, so the two cannot disagree on what a scope keeps.
//
// A narrower-scope value that only echoes an album value is not counted as covered.
// projectFlat emits album values verbatim and suppresses the narrower echo, so the
// album scope owns that canonical multiplicity. Subtracting the echo would collapse
// album duplicates during an unrelated edit.
//
// A second group at album scope is different. The reader treats it as part of the
// primary album emit, so its values are covered and subtractFold removes only the
// matching number of album values. That keeps same-scope duplicates stable across
// repeated edits instead of growing or shrinking them.
// It also returns albumOwn: the album group's own surviving values as ordered lists, computed
// from the same projection pass that builds albumFolds. buildAlbumGroup subtracts these from
// its canonical re-emit so a value it preserves verbatim is not also emitted flat - returning
// them here means that pass runs once, not a second time inside buildAlbumGroup.
func coveredByOtherScopes(groups []tagGroup, albumIdx int, ek map[tag.Key]bool) (covered, albumOwn map[tag.Key][]string) {
	// Per key, the case-folded values the album scope itself keeps after the edit, plus the
	// same values as ordered lists (albumOwn) for buildAlbumGroup's subtraction.
	albumFolds := map[tag.Key]map[string]bool{}
	albumOwn = map[tag.Key][]string{}
	var albumScope core.Scope
	if albumIdx >= 0 {
		albumScope = groups[albumIdx].scope
		forEachSurvivingContribution(groups[albumIdx], ek, func(c scopedContribution) {
			if albumFolds[c.key] == nil {
				albumFolds[c.key] = map[string]bool{}
			}
			albumFolds[c.key][core.Fold(c.value)] = true
			albumOwn[c.key] = append(albumOwn[c.key], c.value)
		})
	}

	covered = map[tag.Key][]string{}
	for i, g := range groups {
		if i == albumIdx {
			continue
		}
		// A second album-scope group contributes to the album canonical. Its values are
		// covered and subtracted with multiplicity, not carved out as narrower echoes.
		narrower := albumIdx >= 0 && g.scope != albumScope
		forEachSurvivingContribution(g, ek, func(c scopedContribution) {
			if narrower && albumFolds[c.key][core.Fold(c.value)] {
				return // a narrower-scope echo of an album value: the album scope owns it
			}
			covered[c.key] = append(covered[c.key], c.value)
		})
	}
	return covered, albumOwn
}

// forEachSurvivingContribution invokes fn for each canonical contribution a group's
// SimpleTags still carry after the edit. It skips tags dropped by the edit and tags
// with no string value, then projects the survivors through projectTag.
func forEachSurvivingContribution(g tagGroup, ek map[tag.Key]bool, fn func(scopedContribution)) {
	for _, st := range g.tags {
		if droppedByEdit(st, ek) || !st.hasValue {
			continue
		}
		for _, c := range projectTag(st.name, st.value, g.scope) {
			fn(c)
		}
	}
}

// renderGroup re-renders one Tag group. The album group is synced to the edited
// canonical set; a non-album group is preserved verbatim or, when it carries an
// edited key, re-rendered to drop that key. keep is false when the group becomes
// empty.
func renderGroup(g tagGroup, base, edited tag.TagSet, covered, albumOwn map[tag.Key][]string, ek map[tag.Key]bool, isAlbum bool) (out tagGroup, raw []byte, keep bool) {
	if !isAlbum {
		return renderNonAlbumGroup(g, ek)
	}
	ng, gb := buildAlbumGroup(&g, base, edited, covered, albumOwn, ek)
	if gb == nil {
		return tagGroup{}, nil, false
	}
	return ng, gb, true
}

// renderNonAlbumGroup renders a track/edition/chapter/part-scoped group. When none
// of its SimpleTags map to an edited canonical key it is preserved verbatim from
// its captured bytes (the fast path, keeping any UID, nested, or binary tags). When
// it does carry one, it is rebuilt from the captured Targets plus the surviving
// SimpleTags - dropping every SimpleTag whose canonical key was edited, since that
// value is now written authoritatively at album scope (or cleared) - with the CRC
// recomputed when the source group had one. The group is dropped when nothing
// survives. checkPreservable has already guaranteed every kept SimpleTag's raw (and
// the Targets when the group narrows scope) was captured.
func renderNonAlbumGroup(g tagGroup, ek map[tag.Key]bool) (out tagGroup, raw []byte, keep bool) {
	if !groupTouchedBy(g, ek) {
		if g.raw == nil {
			return tagGroup{}, nil, false
		}
		return g, g.raw, true // preserve verbatim
	}
	out = g
	out.tags = nil
	var simple []byte
	for _, st := range g.tags {
		if droppedByEdit(st, ek) {
			continue // its edited value now lives at album scope (or was cleared)
		}
		simple = append(simple, st.raw...)
		out.tags = append(out.tags, st)
	}
	if len(simple) == 0 {
		return tagGroup{}, nil, false // every SimpleTag was edited away
	}
	var content []byte
	if g.targetsRaw != nil {
		content = append(content, g.targetsRaw...)
	}
	content = append(content, simple...)
	rendered := masterElement(idTag, content, g.hasCRC)
	// Carry the freshly rendered bytes (not the stale input raw, which still holds
	// the dropped SimpleTags) so the returned document's group equals a fresh parse
	// of the output - a re-edit of that document then preserves this group verbatim
	// correctly instead of re-emitting the dropped key or dropping the group.
	out.raw = rendered
	return out, rendered, true
}

// droppedByEdit reports whether a SimpleTag must be dropped from a re-rendered
// group: it maps to a canonical key whose value the edit changed, so the
// authoritative value now lives at album scope (or was cleared). A non-canonical or
// unedited SimpleTag is kept. It is the single drop predicate shared by the
// preservation check (which tags need captured bytes), the covered-set computation,
// and the renderer (which tags are emitted), so they cannot diverge on which tags
// survive.
//
// A slash number (PART_NUMBER=3/12) projects to TWO canonical keys (TrackNumber and
// TrackTotal), so editing EITHER must drop the whole tag - otherwise editing the total
// would leave the stale total lingering at this scope (conflicting with the value
// rewritten at album scope), and dropping it for a TrackNumber edit would lose the
// unedited total entirely. This mirrors projectTag's slash split so the drop and the
// projection stay in agreement.
func droppedByEdit(st simpleTag, ek map[tag.Key]bool) bool {
	k, ok := mapping.MatroskaTagKey(st.name)
	if !ok {
		return false
	}
	if ek[k] {
		return true
	}
	if (k == tag.TrackNumber || k == tag.DiscNumber) && strings.ContainsRune(st.value, '/') {
		return ek[tag.TotalKey(k)]
	}
	return false
}

// meaningfulLang reports whether an EBML language string names a real language, i.e.
// it is neither absent nor the "und" (undetermined) default. Matroska's TagLanguage and
// ChapLanguage both default to "und", so an "und" value carries no information a flat
// re-emit (which omits the element and reads back as "und") would lose.
func meaningfulLang(lang string) bool {
	return lang != "" && !strings.EqualFold(lang, "und")
}

// tagStructureDropped returns the canonical keys whose album-scope SimpleTag carried
// structure the flat canonical model cannot hold - a TagLanguage, a TagBinary value, or
// nested sub-tags - that this edit drops because the key's value changed (droppedByEdit),
// re-emitting it flat at album scope. An unchanged structured tag is preserved verbatim
// (by buildAlbumGroup at album scope, or renderNonAlbumGroup's verbatim carry elsewhere) and
// is not reported. Every scope is scanned, not just album: a track/edition/chapter-scoped
// structured tag whose key is edited is dropped and re-emitted flat at album scope too, the
// same silent loss. Keys are de-duplicated in first-seen order so the warning names each
// affected field once.
func tagStructureDropped(d *doc, ek map[tag.Key]bool) []tag.Key {
	var keys []tag.Key
	seen := map[tag.Key]bool{}
	for _, g := range d.groups {
		for _, st := range g.tags {
			if !droppedByEdit(st, ek) {
				continue
			}
			// A plain string tag loses nothing on a flat re-emit. A TagLanguage of "und"
			// (the EBML default mkvmerge writes on essentially every SimpleTag) is not a
			// meaningful secondary language - re-emitting with no TagLanguage reads back as
			// "und" too - so it does not count as lost structure and must not spuriously warn.
			if !meaningfulLang(st.lang) && st.binary == 0 && len(st.sub) == 0 {
				continue
			}
			k, ok := mapping.MatroskaTagKey(st.name)
			if !ok || seen[k] {
				continue
			}
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys
}

// isManagedTitle reports whether a SimpleTag maps to the canonical Title, which is always
// homed in Info.Title - so it is never kept as an album SimpleTag in the output. The album
// re-emit already skips Title (k == tag.Title), and the preservation loop must skip it too:
// otherwise a file whose title lives only in a SimpleTag, migrated to Info.Title on an
// unrelated edit, would carry the title twice (Info.Title plus the stale SimpleTag).
func isManagedTitle(st simpleTag) bool {
	k, ok := mapping.MatroskaTagKey(st.name)
	return ok && k == tag.Title
}

// groupTouchedBy reports whether any of the group's SimpleTags would be dropped by
// the edit - i.e. whether the group must be re-rendered rather than preserved
// verbatim.
func groupTouchedBy(g tagGroup, ek map[tag.Key]bool) bool {
	for _, st := range g.tags {
		if droppedByEdit(st, ek) {
			return true
		}
	}
	return false
}

// hasManagedTitleTag reports whether any Tag group carries a TITLE SimpleTag, which
// projects into the canonical Title alongside Info.Title. Editing the title must
// drop such a tag (the title is authoritative in Info.Title), but that drop only
// happens on the Tags re-render path - so its presence promotes a title-only edit
// to also re-render the Tags element.
func hasManagedTitleTag(groups []tagGroup) bool {
	for _, g := range groups {
		for _, st := range g.tags {
			if k, ok := mapping.MatroskaTagKey(st.name); ok && k == tag.Title {
				return true
			}
		}
	}
	return false
}

// narrowsScope reports whether the group's Targets restrict it below album scope
// (a track/edition/chapter UID or any explicit target type/level). Such a group
// must keep its captured Targets bytes through a re-render or it would silently
// widen to the default album scope. A target-less group (the album group) is
// handled separately and never reaches here.
func narrowsScope(g tagGroup) bool {
	return g.trackUID || g.editionUID || g.chapterUID || g.targetTypeValue != 0 || g.targetType != ""
}

// editedKeySet returns the canonical keys whose values differ between the base and
// edited tag sets, via the shared tag.Diff primitive. It is the set a Matroska tag
// edit must reach at every scope: each such key is written at album scope and
// dropped from any other group that held it.
func editedKeySet(base, edited tag.TagSet) map[tag.Key]bool {
	ek := map[tag.Key]bool{}
	for _, c := range tag.Diff(base, edited) {
		ek[c.Key] = true
	}
	return ek
}

// buildAlbumGroup renders the album-scope group: the preserved Targets (carrying
// any UID), the kept non-canonical SimpleTags verbatim, then the synced canonical
// SimpleTags. group is the existing album group (nil when creating one). For an
// unchanged key already carried verbatim by another scope, only the canonical values
// that scope does not preserve are re-emitted here - so a value split across scopes
// (ENCODER album=Lavf + track=Lavc) keeps its album-only part instead of being
// dropped wholesale, while a fully covered key stays put (no duplication). A changed
// key re-emits all its values: a canonical edit defaults to album scope and the
// other scopes drop it via renderNonAlbumGroup/droppedByEdit.
// albumOwn is the album group's own surviving projected values (from coveredByOtherScopes),
// subtracted from the canonical re-emit so a value preserved verbatim - with its
// language/binary/nested structure - is not also emitted flat.
func buildAlbumGroup(group *tagGroup, base, edited tag.TagSet, covered, albumOwn map[tag.Key][]string, ek map[tag.Key]bool) (tagGroup, []byte) {
	out := tagGroup{scope: core.ScopeAlbum}
	var simple []byte
	if group != nil {
		out = *group
		out.tags = nil
		// Preserve every SimpleTag the edit does not drop, verbatim from its captured
		// bytes - custom names, technical stats, binary, nested trees, AND managed tags
		// whose canonical key was not edited (keeping the language, binary value, or
		// secondary structure a flat re-emit would lose). A managed tag whose key WAS
		// edited (droppedByEdit) is dropped here; its new value is re-emitted flat at
		// album scope below. checkPreservable has guaranteed each kept tag's raw.
		for _, st := range group.tags {
			if droppedByEdit(st, ek) || isManagedTitle(st) {
				continue
			}
			if st.raw != nil {
				simple = append(simple, st.raw...)
				out.tags = append(out.tags, st)
			}
		}
	}
	// Append the canonical set under the Matroska-spec names, in key order.
	for _, k := range edited.Keys() {
		if k == tag.Title {
			continue // stored in Info.Title
		}
		vals, _ := edited.Get(k)
		if bv, _ := base.Get(k); slices.Equal(bv, vals) {
			// Unchanged key carried verbatim elsewhere - by a narrower scope (covered) or
			// by this album group's own preserved SimpleTags (albumOwn) - is re-emitted
			// only for the canonical values not already preserved. A value split across
			// scopes keeps its album-only part; a fully covered/preserved key is skipped.
			// A changed key re-emits in full (its preserved copies were dropped above).
			if sub := slices.Concat(covered[k], albumOwn[k]); len(sub) > 0 {
				vals = subtractFold(vals, sub)
				if len(vals) == 0 {
					continue
				}
			}
		}
		name := mapping.MatroskaTagName(k)
		for _, v := range vals {
			// A present empty value from `set KEY=` is emitted as a zero-length SimpleTag,
			// not skipped. Matroska preserves it like FLAC and Ogg; only the native
			// WAV/AIFF INFO/text vocabularies drop such a value when no ID3 chunk is
			// available to hold it. hasValue stays true so the result document projects it
			// back into the canonical tag set.
			stb := simpleTagBytes(name, v)
			simple = append(simple, stb...)
			// Carry the freshly rendered bytes as this synthesized tag's raw, so the result
			// document's album group equals a fresh parse of the output. A re-edit of that
			// returned Document then preserves this tag verbatim (it is flat, so there is no
			// structure to lose) instead of tripping checkPreservable's raw-availability gate,
			// which exists to catch a parsed tag whose bytes were too big to capture - a case a
			// synthesized tag must not be mistaken for. Mirrors renderNonAlbumGroup, which
			// carries its re-rendered group bytes the same way.
			out.tags = append(out.tags, simpleTag{name: name, value: v, hasValue: true, raw: stb})
		}
	}
	if len(simple) == 0 {
		return tagGroup{}, nil
	}
	var content []byte
	if group != nil && group.targetsRaw != nil {
		content = append(content, group.targetsRaw...)
	}
	content = append(content, simple...)
	return out, masterElement(idTag, content, group != nil && group.hasCRC)
}

// subtractFold removes covered values from vals by folded form, one occurrence at a
// time, preserving survivor case and order. Per-occurrence subtraction is what keeps
// duplicates stable: if the canonical carries a value twice and another scope covers it
// once, only one copy is removed from the album sync.
func subtractFold(vals, covered []string) []string {
	remaining := make(map[string]int, len(covered))
	for _, c := range covered {
		remaining[core.Fold(c)]++
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if f := core.Fold(v); remaining[f] > 0 {
			remaining[f]--
			continue
		}
		out = append(out, v)
	}
	return out
}

// simpleTagBytes renders a SimpleTag with a name and a single string value.
func simpleTagBytes(name, value string) []byte {
	payload := append(stringElement(idTagName, name), stringElement(idTagString, value)...)
	return encElement(idSimpleTag, payload)
}

// isManaged reports whether a SimpleTag name maps to a canonical key the writer
// owns (so it is re-synced rather than preserved). Title is managed too (dropped
// from SimpleTags, since it lives in Info.Title).
func isManaged(name string) bool {
	_, ok := mapping.MatroskaTagKey(name)
	return ok
}

// albumGroupIndex returns the index of the group to sync canonical tags into: the
// first album-scope group with no track/edition/chapter UID, or -1 if none.
func albumGroupIndex(groups []tagGroup) int {
	for i, g := range groups {
		if g.scope == core.ScopeAlbum && !g.trackUID && !g.editionUID && !g.chapterUID {
			return i
		}
	}
	return -1
}

// renderInfo splices the edited Title into the captured Info bytes (replacing,
// inserting, or removing the Title child) and recomputes the CRC-32. It returns
// the new Info element bytes and the new segment title. present is the Title key's
// presence in the edited set, not "title != """: a present-but-empty title
// (`set TITLE=`) writes a zero-length <Title> element, while an absent title
// (`--clear TITLE`) removes it - the two must stay distinguishable on round-trip.
func renderInfo(ib *infoBlock, title string, present bool) (raw []byte, newTitle string) {
	r := ib.raw
	root, ok := readElement(core.BytesSource(r), 0, int64(len(r)), int64(len(r)))
	if !ok {
		return nil, ""
	}
	headerLen := int(root.dataStart) // ID + size VINT
	var titleEl []byte
	if present {
		titleEl = stringElement(idSegTitle, title)
	}
	// Rebuild the content (everything after the element header) with the Title
	// child replaced/inserted/removed; other children stay byte-identical.
	var content []byte
	if ib.titleOff >= 0 {
		content = append(content, r[headerLen:ib.titleOff]...)
		content = append(content, titleEl...)
		content = append(content, r[ib.titleEnd:]...)
	} else {
		content = append(content, r[headerLen:ib.insertOff]...)
		content = append(content, titleEl...)
		content = append(content, r[ib.insertOff:]...)
	}
	if ib.crc != nil {
		// Recompute the CRC over the new content following the CRC element. The CRC
		// element occupies content[0:6] (its own bytes were copied verbatim above).
		fixed := make([]byte, len(content))
		copy(fixed, content)
		body := fixed[6:]
		crc := crcElement(body)
		copy(fixed[0:6], crc)
		content = fixed
	}
	return encElement(idInfo, content), title
}

// renderAttachments rebuilds the Attachments element from the preserved
// non-image attachments and the edited picture set, returning nil when empty (so
// the element is dropped). It also returns the new attachment list for the result.
func renderAttachments(d *doc, pics []core.Picture) (raw []byte, atts []attachment) {
	var content []byte
	for _, a := range d.attachments {
		if a.image || a.raw == nil {
			continue // images are rebuilt from the picture set below
		}
		content = append(content, a.raw...)
		atts = append(atts, a)
	}
	for _, p := range pics {
		ab, a := attachedFileBytes(p)
		content = append(content, ab...)
		atts = append(atts, a)
	}
	if len(content) == 0 {
		return nil, nil
	}
	hasCRC := d.wb.attach != nil && d.wb.attach.hasCRC
	return masterElement(idAttachments, content, hasCRC), atts
}

// attachedFileBytes renders one AttachedFile from a picture, using the Matroska
// cover-art file-name convention (cover.<ext>) so a re-parse classifies it. The
// mandatory FileUID is random, as the spec advises ("as random as possible").
func attachedFileBytes(p core.Picture) ([]byte, attachment) {
	name := coverFileName(p)
	payload := stringElement(idFileName, name)
	payload = append(payload, stringElement(idFileMime, p.MIME)...)
	if p.Description != "" {
		payload = append(payload, stringElement(idFileDesc, p.Description)...)
	}
	payload = append(payload, encElement(idFileData, p.Data)...)
	payload = append(payload, uintElement(idFileUID, fileUID())...)
	a := attachment{name: name, mime: p.MIME, description: p.Description, size: len(p.Data), image: true}
	return encElement(idAttached, payload), a
}

// fileUID returns a random non-zero AttachedFile UID (per the spec's "as random
// as possible"), making a collision with another attachment's UID negligible.
func fileUID() uint64 { return randomUID() }

// uidFallback makes randomUID's non-crypto path still yield distinct values, so a
// batch of created chapters or attachments cannot collide on one constant UID.
var uidFallback atomic.Uint64

// randomUID returns a random non-zero 64-bit UID, used for a created
// AttachedFile's FileUID and a created ChapterAtom's ChapterUID. Both must be
// non-zero and "as random as possible" per the spec, and - critically for the
// several UIDs minted in one chapter write - must not repeat within a file (a
// duplicate ChapterUID would make a chapter-scoped tag reference ambiguous). The
// crypto/rand read effectively never fails; if it does, a monotonic time+counter
// mix keeps successive UIDs distinct rather than collapsing to one constant.
//
// These UIDs are random per run, so Matroska writes that create or rebuild attachment
// FileUIDs or ChapterUIDs are not byte-reproducible. The README documents that
// limitation. The audio essence is still preserved; deterministic UIDs would need a
// stable seed or content-derived scheme.
func randomUID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		if v := binary.BigEndian.Uint64(b[:]); v != 0 {
			return v
		}
	}
	n := uidFallback.Add(1)
	v := (uint64(time.Now().UnixNano()) << 16) ^ (n * 0x9E3779B97F4A7C15)
	if v == 0 {
		v = n
	}
	return v
}

// coverFileName picks the AttachedFile name encoding the cover role.
func coverFileName(p core.Picture) string {
	ext := imageExt(p.MIME)
	if p.Type == core.PicFrontCover {
		return "cover" + ext
	}
	return "small_cover" + ext
}

// imageExt returns the conventional extension for a cover MIME.
func imageExt(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	default:
		return ".jpg"
	}
}
