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

// multiTitleDroppedReason is the message for a multi-value TITLE that Matroska stores
// only the first of. The write warning ([Codec.Plan]) and the transfer classifier
// ([TransferClassifier]) both use it, so the copy report and the write-time warning cannot
// drift after a future edit.
const multiTitleDroppedReason = "Matroska stores only the first TITLE value; additional values were dropped"

// technicalNameReason is shared by the plan warning and the transfer classifier so
// the write warning and the copy report cannot drift.
func technicalNameReason(name string) string {
	return name + " is a reserved Matroska technical/statistics tag name (derived from the stream, never read back as a tag), so the value is not written"
}

// TransferClassifier grades the two field-level cases the format-level capability cannot
// express. A multi-value TITLE: Matroska homes the canonical Title in the single-valued
// Info.Title element, so only the first value survives - a cardinality loss the per-value
// predicates cannot see. It reports Lossy, not Dropped: the first value is still written,
// so the value is present but reduced, and the write-time warning still fires. It reuses
// multiTitleDroppedReason so the report and that warning stay in step. A reserved Matroska
// technical/statistics name (DURATION, BPS, a NUMBER_OF_* stat, or any _STATISTICS-prefixed
// name): it reports Dropped, reusing technicalNameReason so the report and the write-time
// warning stay in step there too. Every other field is left to the format-level grade. It
// is a plain [core.FieldClassifier] (registered by value, not called), so it captures
// nothing and allocates no closure.
func TransferClassifier(key tag.Key, values []string, _ tag.TagSet) (core.Disposition, string, bool) {
	if key == tag.Title && len(values) > 1 {
		return core.Lossy, multiTitleDroppedReason, true
	}
	if name := mapping.MatroskaTagName(key); mapping.MatroskaTechnicalName(name) {
		return core.Dropped, technicalNameReason(name), true
	}
	return core.Carried, "", false
}

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

	// Matroska cover art is cover.<ext> holding an image, or WaxLabel's unsniffable octet-stream
	// --force cover. A picture with any other MIME (an authored text/plain or application/pdf) is
	// not cover art: the reprojection would drop it, so change detection would collapse the edit to
	// a silent no-op below and the bytes would vanish. Refuse it instead - the Plan-level backstop
	// for a direct Editor.AddPicture with an exotic MIME (checked before the no-op gate, since a
	// dropped picture leaves nothing for that gate to see). The CLI and transfer paths only ever
	// produce image/* or octet-stream picture MIMEs, so this never fires for them; a foreign
	// non-image attachment merely NAMED cover.* is not a projected picture (isCoverAttachment gates
	// on octet-stream), so it is preserved verbatim and never reaches edited.Pictures.
	for _, p := range edited.Pictures {
		if !isCoverAttachment(p.MIME, coverFileName(p)) {
			return nil, fmt.Errorf("%w: a %q picture cannot be stored as Matroska cover art (only an image, or an unsniffable --force cover, is supported)",
				waxerr.ErrUnsupportedTag, p.MIME)
		}
	}

	if !ch.any() {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// The edit's per-tag outcome, computed once and threaded into the preservation
	// check, the covered-set pass, and every group render: a changed key must reach
	// (and be dropped from) every scope that held it, not just album scope.
	ed := computeEditDecisions(d.groups, albumGroupIndex(d.groups), base.Tags, edited.Tags)

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
	// Segment Info title. Info present: renderTags drops the managed TITLE because
	// Info.Title is the canonical home, so force a title render here to migrate the
	// scoped-only title there instead of letting it disappear on an unrelated tag edit.
	// Info absent: there is nowhere to migrate it, so buildAlbumGroup/checkPreservable
	// preserve the SimpleTag verbatim (or refuse if its bytes are uncapturable) - see
	// those functions' infoPresent / d.wb.info branch.
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
	if err := checkSegmentCRCCaptured(d.wb); err != nil {
		return nil, err
	}
	if err := checkPreservable(d, ch, ed); err != nil {
		return nil, err
	}

	// An edit that changes the value of an album-scope SimpleTag carrying structure the
	// flat canonical model cannot hold (a secondary TagLanguage, a TagBinary value, or
	// nested sub-tags) re-emits that value flat and drops the structure. The unchanged-tag
	// case is preserved verbatim by buildAlbumGroup, so this fires only when the key was
	// edited and the old bytes cannot be kept. Emit it as a keyed plan-time warning here,
	// before planAbsorb, so an absorb-then-shift retry cannot render it twice.
	if ch.simple {
		if keys := tagStructureDropped(d, ed); len(keys) > 0 {
			report.Warnings = core.WarnKeyed(report.Warnings, core.WarnTagStructureDropped,
				"an edited album tag dropped its secondary language, binary value, or nested sub-tags", keys...)
		}
	}

	// An edit that supplies a reserved Matroska technical/statistics name (DURATION, BPS, a
	// NUMBER_OF_* stat, or any _STATISTICS-prefixed name) is never emitted: those names are
	// derived from the stream, not descriptive metadata, and the read filter never projects
	// them back as a tag. base.Tags can never hold such a key (the read filter guarantees it),
	// so its presence in edited.Tags means this edit or transfer supplied it - warn once per key.
	if ch.simple {
		for _, k := range edited.Tags.Keys() {
			if k == tag.Title {
				continue
			}
			if name := mapping.MatroskaTagName(k); mapping.MatroskaTechnicalName(name) {
				report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueDropped, technicalNameReason(name), k)
			}
		}
	}

	// Matroska homes the canonical Title in the single-valued Segment.Info.Title element, so
	// only the first TITLE value survives a write (renderChanged pulls it with .First); every
	// other canonical key keeps its full value list via the per-value SimpleTag loop. When an
	// edit leaves more than one TITLE value, the extras are dropped at render - surface that as
	// a keyed, --strict-visible WarnValueDropped (the same code MP4 uses for a trkn the atom
	// cannot hold). The reason is a shared const the transfer classifier also uses, so the
	// write warning and the copy report cannot drift. Gate on ch.title (Title is split out from
	// ch.simple in detectChanges), and place it here, before the planAbsorb/planShift split, so
	// an absorb-then-shift retry cannot render it twice. Storage stays first-value-only; this
	// only closes the honesty gap, and is complementary to the set-time single-valued-multi lint
	// (which flags the input, not the drop).
	if ch.title {
		if vals, _ := edited.Tags.Get(tag.Title); len(vals) > 1 {
			report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueDropped,
				multiTitleDroppedReason, tag.Title)
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
	// A non-image cover embedded under --force no longer needs an honesty warning: it is stored
	// under the cover-art file name and reads back as an Unrecognized() picture (removable, and
	// rebuilt not accumulated on re-add), so the reported "+ pictures" is accurate. It stays
	// flagged as an invalid picture on read/lint, which is the correct place for the "not a
	// recognized image" signal.

	pl, err := planAbsorb(d, base, edited, ch, ed, report)
	if err != nil {
		if !isFallback(err) {
			return nil, err
		}
		if pl, err = planShift(d, base, edited, ch, ed, report); err != nil {
			return nil, err
		}
	}
	// Collapse to a clean no-op when the rendered result re-projects to base: an
	// edit of only reserved technical names, or one whose every value survives at
	// its existing scope. A title-touching edit is never downgraded (ch.title as
	// the structural veto): Info.Title stores a single value, so a multi-value
	// title set must stay a real, warned write even though the projection folds
	// back to base, and a scoped-title migration must still land in Info.Title.
	// The picture/chapter/synced-lyrics sets are compared inside DowngradeNoOp,
	// and Matroska has no padding, legacy container, or vendor-stamp write that
	// could force bytes without a metadata delta.
	if np := core.DowngradeNoOp(core.FormatMatroska, edited.Identity.Size, base, pl.Result,
		base.Tags.Equal(pl.Result.Tags), ch.title, pl.Report.Warnings); np != nil {
		return np, nil
	}
	return pl, nil
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
		simple: !b.Equal(e),
		title:  !slices.Equal(bt, et),
		// Compare base against the reprojected edited set (roles reduced to the cover-art
		// file-name convention, description sanitized, MIME re-sniffed), not the raw edited roles:
		// a role Matroska cannot represent would otherwise look like a change on every copy even
		// though the on-disk cover set is already identical. A --force non-image now reprojects
		// too, so re-adding an identical one is a no-op instead of accumulating a fresh cover_<n>.
		pictures: !core.EqualPictures(base.Pictures, reprojectPictures(edited.Pictures)),
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

// checkSegmentCRCCaptured refuses an edit when a Segment-level CRC-32 is present but its bytes
// could not be captured for neutralization (an over-limit declared size left segVoidFromCRC nil).
// Copying such a CRC verbatim over an edited body would leave a stale, invalid checksum, so this
// fails loudly rather than emit it - the same contract checkIndexCaptured applies to the index
// elements. The Segment CRC is the only CRC-32 that appears directly in wb.children (the rest live
// inside their masters' captured raw bytes), so scanning for idCRC32 here is unambiguous.
func checkSegmentCRCCaptured(wb *writeBase) error {
	if wb.segVoidFromCRC != nil {
		return nil // captured and neutralizable to a Void
	}
	for _, c := range wb.children {
		if c.id == idCRC32 {
			return fmt.Errorf("%w: a Matroska Segment-level CRC-32 could not be read for neutralization",
				waxerr.ErrUnsupportedTag)
		}
	}
	return nil
}

// checkPreservable refuses the edit when an element the writer must copy verbatim
// could not be captured (its bytes exceeded the alloc limit, so captureRaw
// returned nil) - dropping it would silently lose data. It covers the groups and
// non-canonical SimpleTags a tag edit preserves and the non-image attachments a
// cover edit preserves.
func checkPreservable(d *doc, ch changes, ed *editDecisions) error {
	tooBig := func(what string) error {
		return fmt.Errorf("%w: a Matroska %s is too large to rewrite within the alloc limit", waxerr.ErrUnsupportedTag, what)
	}
	if ch.simple {
		for i, g := range d.groups {
			if i == ed.albumIdx {
				// Synced in place: every SimpleTag the edit keeps verbatim - a non-canonical
				// tag, OR a managed tag whose canonical key was not edited (preserved with its
				// language/binary/nested structure) - needs its captured bytes. A tag the edit
				// drops is re-emitted flat from the canonical set and needs no raw.
				for ti, st := range g.tags {
					if ed.dropped(i, ti) || migratesToInfo(st, d.wb.info != nil) {
						continue // re-emitted from the canonical set, or migrated to Info.Title: no raw needed
					}
					if st.raw == nil {
						// Includes a managed TITLE kept because no Info exists to migrate it
						// to. Refuse rather than let buildAlbumGroup skip an uncapturable tag.
						return tooBig("tag")
					}
				}
				continue
			}
			if !groupTouchedBy(len(g.tags), i, ed) {
				if g.raw != nil {
					continue // preserved verbatim from the whole Tag element's bytes
				}
				if len(g.tags) == 0 {
					// No whole-element bytes and no captured SimpleTags to rebuild
					// from: refuse rather than silently dropping the group.
					return tooBig("tag group")
				}
			}
			// Re-rendered to drop its edited keys: every surviving SimpleTag needs
			// its bytes, and a scope-narrowing group needs its Targets bytes too
			// (else the rebuild would silently lose the narrowing). A target-less
			// group needs neither - it carries only the kept SimpleTags. An
			// untouched group whose whole-element bytes were not captured takes
			// this path too: it is rebuilt from its captured parts.
			kept := 0
			for ti, st := range g.tags {
				if ed.dropped(i, ti) {
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
		for i, e := range d.chapters.editions {
			if i != d.chapters.defIdx && e.raw == nil {
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
// under their Matroska-spec names. A non-album group is preserved verbatim unless one
// of its SimpleTags is dropped by the edit's per-tag decision, in which case it is
// re-rendered without that tag; a still-wanted value stays at the scope that holds
// it, and only a dropped value's replacement re-emits at album scope. Title is
// excluded - it lives in Info.Title.
func renderTags(d *doc, base, edited tag.TagSet, ed *editDecisions) (raw []byte, groups []tagGroup) {
	covered, albumOwn, others := coveredByOtherScopes(d.groups, ed)
	// A managed TITLE migrates to Info.Title only when an Info element exists; with
	// none, buildAlbumGroup preserves the SimpleTag verbatim instead of dropping it.
	infoPresent := d.wb.info != nil
	var content []byte

	for i, g := range d.groups {
		newGroup, gb, keep := renderGroup(g, i, base, edited, covered, albumOwn, others, ed, i == ed.albumIdx, infoPresent)
		if !keep {
			continue
		}
		content = append(content, gb...)
		groups = append(groups, newGroup)
	}

	// No album group existed: create one carrying the canonical set.
	if ed.albumIdx < 0 {
		newGroup, gb := buildAlbumGroup(nil, -1, base, edited, covered, albumOwn, others, ed, infoPresent)
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
// A SimpleTag the edit will drop (ed.dropped) carries nothing forward, so it is
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
// The third result, others, is every surviving non-album-group contribution with its
// scope, unfiltered: buildAlbumGroup's re-read simulation needs the full set, echoes
// included, to predict what an emit projects back to.
func coveredByOtherScopes(groups []tagGroup, ed *editDecisions) (covered, albumOwn map[tag.Key][]string, others map[tag.Key][]scopedContribution) {
	// Per key, the case-folded values the album scope itself keeps after the edit, plus the
	// same values as ordered lists (albumOwn) for buildAlbumGroup's subtraction.
	albumFolds := map[tag.Key]map[string]bool{}
	albumOwn = map[tag.Key][]string{}
	var albumScope core.Scope
	if ed.albumIdx >= 0 {
		albumScope = groups[ed.albumIdx].scope
		forEachSurvivingContribution(groups[ed.albumIdx], ed.albumIdx, ed, func(c scopedContribution) {
			if albumFolds[c.key] == nil {
				albumFolds[c.key] = map[string]bool{}
			}
			albumFolds[c.key][core.Fold(c.value)] = true
			albumOwn[c.key] = append(albumOwn[c.key], c.value)
		})
	}

	covered = map[tag.Key][]string{}
	others = map[tag.Key][]scopedContribution{}
	for i, g := range groups {
		if i == ed.albumIdx {
			continue
		}
		// A second album-scope group contributes to the album canonical. Its values are
		// covered and subtracted with multiplicity, not carved out as narrower echoes.
		narrower := ed.albumIdx >= 0 && g.scope != albumScope
		forEachSurvivingContribution(g, i, ed, func(c scopedContribution) {
			others[c.key] = append(others[c.key], c)
			if narrower && albumFolds[c.key][core.Fold(c.value)] {
				return // a narrower-scope echo of an album value: the album scope owns it
			}
			covered[c.key] = append(covered[c.key], c.value)
		})
	}
	return covered, albumOwn, others
}

// forEachSurvivingContribution invokes fn for each canonical contribution a group's
// SimpleTags still carry after the edit. It skips tags dropped by the edit and tags
// with no string value, then projects the survivors through projectTag.
func forEachSurvivingContribution(g tagGroup, gi int, ed *editDecisions, fn func(scopedContribution)) {
	for ti, st := range g.tags {
		if ed.dropped(gi, ti) || !st.hasValue {
			continue
		}
		// Sanitize the raw SimpleTag value to match the canonical TagSet (parse.go
		// projects through core.SanitizeUTF8). Without this, an invalid-UTF-8 value
		// never folds against the sanitized canonical value, so it is never subtracted
		// and gets re-emitted - growing by one copy on every unrelated edit.
		for _, c := range projectTag(st.name, core.SanitizeUTF8(st.value), g.scope) {
			fn(c)
		}
	}
}

// renderGroup re-renders one Tag group. The album group is synced to the edited
// canonical set; a non-album group is preserved verbatim or, when it carries an
// edited key, re-rendered to drop that key. keep is false when the group becomes
// empty.
func renderGroup(g tagGroup, gi int, base, edited tag.TagSet, covered, albumOwn map[tag.Key][]string, others map[tag.Key][]scopedContribution, ed *editDecisions, isAlbum, infoPresent bool) (out tagGroup, raw []byte, keep bool) {
	if !isAlbum {
		return renderNonAlbumGroup(g, gi, ed)
	}
	ng, gb := buildAlbumGroup(&g, gi, base, edited, covered, albumOwn, others, ed, infoPresent)
	if gb == nil {
		return tagGroup{}, nil, false
	}
	return ng, gb, true
}

// renderNonAlbumGroup renders a track/edition/chapter/part-scoped group. When none
// of its SimpleTags is dropped by the edit's per-tag decision, it is preserved
// verbatim from its captured bytes (the fast path, keeping any UID, nested, or
// binary tags - even a tag holding an edited canonical key, when its value is still
// wanted at this scope). When at least one tag is dropped, the group is rebuilt from
// the captured Targets plus the surviving SimpleTags - omitting each dropped tag,
// whose value re-emits at album scope only if it is not kept in place at another
// scope - with the CRC recomputed when the source group had one. The group is
// dropped when nothing survives. checkPreservable has already guaranteed every kept
// SimpleTag's raw (and the Targets when the group narrows scope) was captured.
func renderNonAlbumGroup(g tagGroup, gi int, ed *editDecisions) (out tagGroup, raw []byte, keep bool) {
	if !groupTouchedBy(len(g.tags), gi, ed) && g.raw != nil {
		return g, g.raw, true // preserve verbatim
	}
	// An untouched group without whole-element bytes falls through: it is rebuilt
	// from its captured parts rather than dropped.
	out = g
	out.tags = nil
	var simple []byte
	for ti, st := range g.tags {
		if ed.dropped(gi, ti) {
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

// editDecisions carries the value-level outcome of one tag edit across the render
// helpers: which parsed SimpleTags the edit drops, and per changed key the values
// the album-scope sync must emit. One computation feeds the preservation check,
// the covered-set pass, and both group renderers, so they cannot disagree on what
// survives.
type editDecisions struct {
	ek        map[tag.Key]bool     // keys whose value lists changed (tag.Diff)
	albumIdx  int                  // index of the group buildAlbumGroup syncs into, -1 if none
	drop      map[[2]int]bool      // {group index, tag index} -> dropped by this edit
	albumVals map[tag.Key][]string // per changed key: values to emit at album scope, edited order
}

func (ed *editDecisions) dropped(gi, ti int) bool { return ed.drop[[2]int{gi, ti}] }
func (ed *editDecisions) edited(k tag.Key) bool   { return ed.ek[k] }

// contribDecision is the fate of one canonical contribution a parsed SimpleTag
// projects: whether the value survives at the scope that already holds it, and
// whether keeping it claimed one of the edited values (so the album-scope re-emit
// must not write that value a second time). echo marks a contribution the reader
// suppresses as a cross-scope echo, decided after the album values are known.
type contribDecision struct {
	key       tag.Key
	owner     [2]int // {group index, tag index} of the SimpleTag it came from
	value     string
	echo      bool
	claimable bool // an emitted, non-album, non-boolean, fold-unique contribution
	kept      bool
	claimed   bool
}

// computeEditDecisions resolves one edit against the parsed groups, once per write:
// which SimpleTags drop, and per changed key the values the album-scope sync must
// emit. The rule it implements is the capability constraint in matroska.go: a
// still-wanted value stays at the scope that holds it, removed values drop from
// every scope, and new values write at album scope.
//
// Contributions are classified through the same projectionOrder the reader uses, so
// the write side cannot drift from what a re-read will see. Emitted contributions
// resolve first (album-owned and boolean values always re-emit flat; a narrower one
// is kept only for an exact, fold-unique match), suppressed echoes second, once the
// album values are known. A SimpleTag drops when ANY of its contributions drops: a
// slash number projects two keys, so editing the total away kills the tag even when
// the number half matched, releasing the claimed half back to the album re-emit. A
// tag that projects nothing under an edited key is preserved verbatim, and Title
// stays key-level: it is homed in the single-valued Info.Title.
func computeEditDecisions(groups []tagGroup, albumIdx int, base, edited tag.TagSet) *editDecisions {
	ed := &editDecisions{
		ek:        editedKeySet(base, edited),
		albumIdx:  albumIdx,
		drop:      map[[2]int]bool{},
		albumVals: map[tag.Key][]string{},
	}
	if len(ed.ek) == 0 {
		return ed
	}

	// The contributions each changed key's SimpleTags project, in the order the
	// reader's own projection pass sees them, plus the tag each one came from.
	var keyOrder []tag.Key
	contribs := map[tag.Key][]scopedContribution{}
	owners := map[tag.Key][][2]int{}
	for gi, g := range groups {
		for ti, st := range g.tags {
			if ed.edited(tag.Title) && isManagedTitle(st) {
				ed.drop[[2]int{gi, ti}] = true
				continue
			}
			if !st.hasValue {
				continue
			}
			for _, c := range projectTag(st.name, core.SanitizeUTF8(st.value), g.scope) {
				if c.key == tag.Title || !ed.edited(c.key) {
					continue
				}
				if len(contribs[c.key]) == 0 {
					keyOrder = append(keyOrder, c.key)
				}
				contribs[c.key] = append(contribs[c.key], c)
				owners[c.key] = append(owners[c.key], [2]int{gi, ti})
			}
		}
	}

	// Per changed key, the budget an exact keep draws on: how many copies of each
	// exact string the edit wants, and how many edited values share each folded form.
	exact := make(map[tag.Key]map[string]int, len(ed.ek))
	folds := make(map[tag.Key]map[string]int, len(ed.ek))
	for k := range ed.ek {
		vals, _ := edited.Get(k)
		ex, fl := map[string]int{}, map[string]int{}
		for _, v := range vals {
			ex[v]++
			fl[core.Fold(v)]++
		}
		exact[k], folds[k] = ex, fl
	}

	var ds []contribDecision
	for _, k := range keyOrder {
		boolean := tag.IsBooleanKey(k)
		for _, e := range projectionOrder(k, contribs[k]) {
			d := contribDecision{key: k, owner: owners[k][e.index], value: contribs[k][e.index].value}
			if !e.emitted {
				// An echo does not kill its tag until the album values are known. A
				// boolean echo never survives: the album emit is canonicalized to
				// "1"/"0", so a differently spelled scoped copy would stop folding
				// with it and re-read as a second value on a single-valued key.
				d.echo, d.kept = true, !boolean
				ds = append(ds, d)
				continue
			}
			// Album-scope copies would permute the re-emitted list, boolean copies
			// would dodge the "1"/"0" canonicalization, and a fold-duplicated value
			// kept in place would let the reader's echo suppression halve its
			// multiplicity - none of those may claim.
			d.claimable = groups[d.owner[0]].scope != core.ScopeAlbum && !boolean &&
				folds[k][core.Fold(d.value)] == 1
			ds = append(ds, d)
		}
	}

	// A tag with a contribution that can never be claimed is doomed outright; the
	// remaining claims are then handed out in emission order among the surviving
	// tags, one round per newly doomed tag: a denial dooms the loser's tag (any
	// dropped contribution kills the whole SimpleTag), which releases its own
	// claims for the next round, so a value freed by a dying tag is re-offered to
	// a denied twin instead of being relocated to album scope. Dooming is
	// monotone, so the loop terminates.
	doomed := map[[2]int]bool{}
	for _, d := range ds {
		if !d.echo && (!d.claimable || exact[d.key][d.value] == 0) {
			doomed[d.owner] = true
		}
	}
	for {
		avail := make(map[tag.Key]map[string]int, len(exact))
		for k, ex := range exact {
			cp := make(map[string]int, len(ex))
			for v, n := range ex {
				cp[v] = n
			}
			avail[k] = cp
		}
		for i := range ds {
			d := &ds[i]
			if d.echo {
				continue
			}
			d.kept, d.claimed = false, false
			if !d.claimable || doomed[d.owner] {
				continue
			}
			if avail[d.key][d.value] > 0 {
				avail[d.key][d.value]--
				d.kept, d.claimed = true, true
			}
		}
		grew := false
		for _, d := range ds {
			if !d.echo && !d.kept && !doomed[d.owner] {
				doomed[d.owner], grew = true, true
			}
		}
		if !grew {
			break
		}
	}
	ed.setAlbumVals(edited, ds)

	// Echoes are judged once the album values are known: one survives while its
	// fold stays suppressed on re-read, covered by the album emit or by a value
	// kept in place at a position that projects before it (ds follows
	// projectionOrder per key, so a walk in order sees exactly the earlier folds).
	keptFolds := map[tag.Key]map[string]bool{}
	noteKept := func(d *contribDecision) {
		if d.claimed {
			if keptFolds[d.key] == nil {
				keptFolds[d.key] = map[string]bool{}
			}
			keptFolds[d.key][core.Fold(d.value)] = true
		}
	}
	for i := range ds {
		d := &ds[i]
		if !d.echo {
			noteKept(d)
			continue
		}
		if d.kept {
			d.kept = foldCovered(ed.albumVals[d.key], d.value) || keptFolds[d.key][core.Fold(d.value)]
		}
	}

	// A dropped echo kills its tag, releasing any values that tag had claimed into
	// the album re-emit. That can only grow albumVals, so a final pass re-judges
	// the tags that project nothing but echoes: they hold no claims, so restoring
	// one cannot disturb the values already settled, only preserve more.
	releaseDoomedClaims(ds)
	ed.setAlbumVals(edited, ds)
	hasEmitted := map[[2]int]bool{}
	for _, d := range ds {
		if !d.echo {
			hasEmitted[d.owner] = true
		}
	}
	keptFolds = map[tag.Key]map[string]bool{}
	for i := range ds {
		d := &ds[i]
		if !d.echo {
			noteKept(d)
			continue
		}
		if !d.kept && !hasEmitted[d.owner] && !tag.IsBooleanKey(d.key) {
			d.kept = foldCovered(ed.albumVals[d.key], d.value) || keptFolds[d.key][core.Fold(d.value)]
		}
	}

	for _, d := range ds {
		if !d.kept {
			ed.drop[d.owner] = true
		}
	}
	return ed
}

// releaseDoomedClaims applies the tag-level conjunction: a SimpleTag whose fate is
// already sealed by one dropped contribution cannot carry its other contributions
// either, so the values those had claimed go back to the album-scope re-emit rather
// than disappearing with the tag.
func releaseDoomedClaims(ds []contribDecision) {
	doomed := map[[2]int]bool{}
	for _, d := range ds {
		if !d.kept {
			doomed[d.owner] = true
		}
	}
	for i := range ds {
		if d := &ds[i]; d.claimed && doomed[d.owner] {
			d.kept, d.claimed = false, false
		}
	}
}

// setAlbumVals records, per changed key, the edited values no surviving scoped tag
// claimed - the values the album-scope sync must write - in edited order.
func (ed *editDecisions) setAlbumVals(edited tag.TagSet, ds []contribDecision) {
	claimed := map[tag.Key]map[string]int{}
	for _, d := range ds {
		if !d.claimed {
			continue
		}
		if claimed[d.key] == nil {
			claimed[d.key] = map[string]int{}
		}
		claimed[d.key][d.value]++
	}
	for k := range ed.ek {
		vals, _ := edited.Get(k)
		take := claimed[k]
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			if take[v] > 0 {
				take[v]--
				continue
			}
			out = append(out, v)
		}
		ed.albumVals[k] = out
	}
}

// foldCovered reports whether vals already carries value's case-folded form, i.e.
// whether the album-scope emit will make a narrower-scope copy of it invisible.
func foldCovered(vals []string, value string) bool {
	f := core.Fold(value)
	for _, v := range vals {
		if core.Fold(v) == f {
			return true
		}
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
// nested sub-tags - that this edit drops because the key's value changed (ed.dropped),
// re-emitting it flat at album scope. An unchanged structured tag is preserved verbatim
// (by buildAlbumGroup at album scope, or renderNonAlbumGroup's verbatim carry elsewhere) and
// is not reported. Every scope is scanned, not just album: a track/edition/chapter-scoped
// structured tag whose key is edited is dropped and re-emitted flat at album scope too, the
// same silent loss. Keys are de-duplicated in first-seen order so the warning names each
// affected field once.
func tagStructureDropped(d *doc, ed *editDecisions) []tag.Key {
	var keys []tag.Key
	seen := map[tag.Key]bool{}
	for gi, g := range d.groups {
		for ti, st := range g.tags {
			if !ed.dropped(gi, ti) {
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

// migratesToInfo reports whether a managed TITLE SimpleTag will migrate to Info.Title and
// thus be dropped from the Tags element: it is a managed title AND an Info element exists
// to receive it. With no Info, the SimpleTag is preserved verbatim instead. buildAlbumGroup
// (which drops it) and checkPreservable (which then needs no captured raw for it) both
// consult this one predicate, so the two gates cannot diverge and lose a title that only
// one of them believed was migrating.
func migratesToInfo(st simpleTag, infoPresent bool) bool {
	return isManagedTitle(st) && infoPresent
}

// groupTouchedBy reports whether any of the group's nTags SimpleTags would be
// dropped by the edit - i.e. whether the group must be re-rendered rather than
// preserved verbatim.
func groupTouchedBy(nTags, gi int, ed *editDecisions) bool {
	for ti := 0; ti < nTags; ti++ {
		if ed.dropped(gi, ti) {
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
// edited tag sets, via the shared tag.Diff primitive. computeEditDecisions consumes
// this set to decide, per SimpleTag, whether its contribution stays at its own
// scope or drops.
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
// key re-emits the values ed decided for album scope: a canonical edit defaults to
// album scope and the other scopes drop it via renderNonAlbumGroup/ed.dropped.
// albumOwn is the album group's own surviving projected values (from coveredByOtherScopes),
// subtracted from the canonical re-emit so a value preserved verbatim - with its
// language/binary/nested structure - is not also emitted flat.
func buildAlbumGroup(group *tagGroup, gi int, base, edited tag.TagSet, covered, albumOwn map[tag.Key][]string, others map[tag.Key][]scopedContribution, ed *editDecisions, infoPresent bool) (tagGroup, []byte) {
	out := tagGroup{scope: core.ScopeAlbum}
	var simple []byte
	if group != nil {
		out = *group
		out.tags = nil
		// Preserve every SimpleTag the edit does not drop, verbatim from its captured
		// bytes - custom names, technical stats, binary, nested trees, AND managed tags
		// whose canonical key was not edited (keeping the language, binary value, or
		// secondary structure a flat re-emit would lose). A managed tag whose key WAS
		// edited (ed.dropped) is dropped here; its new value is re-emitted flat at
		// album scope below. checkPreservable has guaranteed each kept tag's raw.
		//
		// A managed TITLE is dropped here only when an Info element exists to migrate it
		// to (Info.Title is its canonical home, and Plan forces a title render so it lands
		// there). With no Info element there is nowhere to migrate it, so it is preserved
		// verbatim like any other kept tag instead of being silently lost on an unrelated
		// edit; the canonical re-emit below still skips Title, so it is not duplicated.
		for ti, st := range group.tags {
			if ed.dropped(gi, ti) || migratesToInfo(st, infoPresent) {
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
			// A changed key instead emits its album-scope decision, computed by ed: the
			// values not kept in place at other scopes (see the else branch below).
			if sub := slices.Concat(covered[k], albumOwn[k]); len(sub) > 0 {
				emit := subtractFold(vals, sub)
				if !reprojectsTo(k, vals, albumOwn[k], emit, others[k]) {
					// A partially subtracted fold would leave the album emit
					// suppressing the surviving narrower copies, shrinking the
					// value's multiplicity on re-read: re-emit everything the album
					// group itself does not preserve, and let the narrower copies
					// ride along as suppressed echoes.
					emit = subtractFold(vals, albumOwn[k])
				}
				vals = emit
				if len(vals) == 0 {
					continue
				}
			}
		} else {
			// A changed key emits what ed decided for album scope. Cloned because the
			// boolean canonicalization below rewrites vals in place and ed is shared
			// across an absorb-then-shift retry.
			vals = slices.Clone(ed.albumVals[k])
		}
		if tag.IsBooleanKey(k) {
			// Canonicalize a recognized boolean word to "1"/"0", matching the Vorbis, ID3,
			// and MP4 writers so every format stores a boolean field identically. Applied
			// after the unchanged-key comparison above, so a preserved verbatim value is
			// untouched and only a fresh emit normalizes. vals is already a private slice
			// (Get clones; subtractFold allocates; the changed-key branch clones
			// albumVals), so rewriting in place is safe.
			for i, v := range vals {
				vals[i] = tag.CanonicalBoolValue(v)
			}
		}
		name := mapping.MatroskaTagName(k)
		if mapping.MatroskaTechnicalName(name) {
			continue // reserved technical name: never emitted, warned at plan time
		}
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
	// out already carries any existing Targets from group. A nil value means this
	// is a newly created album group, which still needs the mandatory Targets
	// child. An empty Targets element defaults to album scope (TargetTypeValue
	// 50). Recording it on out keeps the returned doc consistent with a fresh
	// parse of the rendered bytes.
	if out.targetsRaw == nil {
		out.targetsRaw = encElement(idTargets, nil)
	}
	var content []byte
	content = append(content, out.targetsRaw...)
	content = append(content, simple...)
	rendered := masterElement(idTag, content, out.hasCRC)
	out.raw = rendered
	return out, rendered
}

// subtractFold removes covered values from vals by folded form, one occurrence at a
// time, preserving survivor case and order. Per-occurrence subtraction is what keeps
// duplicates stable: if the canonical carries a value twice and another scope covers it
// once, only one copy is removed from the album sync.
// reprojectsTo simulates a re-read of one key over a hypothetical album emit: the
// album group's preserved copies plus the freshly emitted values, followed by the
// surviving contributions at their own scopes, must project back to exactly the
// base value list. It runs the reader's own projectionOrder so the check cannot
// drift from the real read; order within the album scope mirrors the write
// (preserved tags render before the canonical emit).
func reprojectsTo(key tag.Key, want, albumOwn, emit []string, others []scopedContribution) bool {
	contribs := make([]scopedContribution, 0, len(albumOwn)+len(emit)+len(others))
	for _, v := range albumOwn {
		contribs = append(contribs, scopedContribution{key: key, value: v, scope: core.ScopeAlbum})
	}
	for _, v := range emit {
		contribs = append(contribs, scopedContribution{key: key, value: v, scope: core.ScopeAlbum})
	}
	contribs = append(contribs, others...)
	count := make(map[string]int, len(want))
	for _, v := range want {
		count[v]++
	}
	got := 0
	for _, e := range projectionOrder(key, contribs) {
		if !e.emitted {
			continue
		}
		v := contribs[e.index].value
		if count[v] == 0 {
			return false
		}
		count[v]--
		got++
	}
	return got == len(want)
}

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
		// Recompute the CRC over the new content following the CRC element by reusing
		// recomputeCRC (rather than a hardcoded content[0:6]): EBML permits an overlong CRC
		// size VINT, so the 4 value bytes are not always at index 2. The captured offsets
		// are element-relative (rewrite_read.go) and content excludes the element header, so
		// rebase by headerLen. recomputeCRC rewrites only the 4 value bytes, leaving any
		// overlong size VINT intact.
		fixed := make([]byte, len(content))
		copy(fixed, content)
		recomputeCRC(fixed, &crcSpot{valOff: ib.crc.valOff - headerLen, contentStart: ib.crc.contentStart - headerLen})
		content = fixed
	}
	return encElement(idInfo, content), title
}

// renderAttachments rebuilds the Attachments element from the preserved
// non-image attachments and the edited picture set, returning nil when empty (so
// the element is dropped). It also returns the new attachment list for the result.
func renderAttachments(d *doc, pics []core.Picture) (raw []byte, atts []attachment) {
	var content []byte
	// Track the names already used in this element so two same-role, same-MIME
	// covers (both "cover.png") get distinct FileNames. Seed it with the preserved
	// non-image attachment names so a cover cannot collide with one of those either.
	used := map[string]bool{}
	for _, a := range d.attachments {
		if a.image || a.raw == nil {
			continue // images are rebuilt from the picture set below
		}
		content = append(content, a.raw...)
		atts = append(atts, a)
		used[a.name] = true
	}
	for _, p := range pics {
		name := uniqueAttachmentName(coverFileStem(p), imageExt(p.MIME), used)
		used[name] = true
		ab, a := attachedFileBytes(p, name)
		content = append(content, ab...)
		atts = append(atts, a)
	}
	if len(content) == 0 {
		return nil, nil
	}
	hasCRC := d.wb.attach != nil && d.wb.attach.hasCRC
	return masterElement(idAttachments, content, hasCRC), atts
}

// attachedFileBytes renders one AttachedFile from a picture under an already-unique
// file name. The Matroska cover-art convention (cover.<ext>) lets a later parse
// classify it; renderAttachments resolves names so same-role covers cannot share a
// FileName. The mandatory FileUID is random, as the spec advises.
func attachedFileBytes(p core.Picture, name string) ([]byte, attachment) {
	payload := stringElement(idFileName, name)
	payload = append(payload, stringElement(idFileMime, p.MIME)...)
	if p.Description != "" {
		payload = append(payload, stringElement(idFileDesc, p.Description)...)
	}
	payload = append(payload, encElement(idFileData, p.Data)...)
	payload = append(payload, uintElement(idFileUID, fileUID())...)
	// image mirrors the read gate (isCoverAttachment) so this result attachment matches a fresh
	// reparse: a --force octet-stream cover is written under a cover name, so it must read back as
	// a picture here too, not a plain attachment.
	a := attachment{name: name, mime: p.MIME, description: p.Description, size: len(p.Data), image: isCoverAttachment(p.MIME, name)}
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

// coverFileName is the canonical (un-disambiguated) AttachedFile name for a cover
// role. It backs the result view's Type derivation; the actually-stored name is
// resolved by renderAttachments and may carry a numeric suffix.
func coverFileName(p core.Picture) string {
	return coverFileStem(p) + imageExt(p.MIME)
}

// coverFileStem is the AttachedFile name stem (no extension) encoding the cover role.
func coverFileStem(p core.Picture) string {
	if p.Type == core.PicFrontCover {
		return "cover"
	}
	return "small_cover"
}

// uniqueAttachmentName resolves an AttachedFile name from its role stem and
// extension, inserting a numeric suffix before the extension (cover.png,
// cover_1.png, ...) until it does not collide with a name already used in this
// Attachments element. Two same-role, same-MIME covers would otherwise both render
// "cover.png". Built natively from the parts, so no path/filepath dependency.
func uniqueAttachmentName(stem, ext string, used map[string]bool) string {
	name := stem + ext
	for i := 1; used[name]; i++ {
		name = fmt.Sprintf("%s_%d%s", stem, i, ext)
	}
	return name
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
