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

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// multiTitleDroppedReason: shared by [Codec.Plan] warning and [TransferClassifier].
const multiTitleDroppedReason = "Matroska stores only the first TITLE value; additional values were dropped"

// technicalNameReason: shared by plan warning and transfer classifier.
func technicalNameReason(name string) string {
	return name + " is a reserved Matroska technical/statistics tag name (derived from the stream, never read back as a tag), so the value is not written"
}

// TransferClassifier: multi-value TITLE => Lossy (Info.Title is single-valued;
// first kept); reserved technical/statistics names => Dropped. Reuses shared
// reason consts. Plain [core.FieldClassifier]; other fields use format grade.
func TransferClassifier(key tag.Key, values []string, _ tag.TagSet) (core.Disposition, string, bool) {
	if key == tag.Title && len(values) > 1 {
		return core.Lossy, multiTitleDroppedReason, true
	}
	if name := mapping.MatroskaTagName(key); mapping.MatroskaTechnicalName(name) {
		return core.Dropped, technicalNameReason(name), true
	}
	return core.Carried, "", false
}

// Plan: preservation-first rewrite. Clusters copied byte-for-byte; only Tags,
// Info.Title, Attachments (and chapters) re-rendered. Prefer absorb into Void so
// clusters stay put; else planShift. Seek targets and CRCs stay correct either way.
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

	// Cover is cover.<ext> or --force octet-stream. Other MIMEs refused before no-op
	// (reprojection would drop them to a silent no-op). Editor sniff settles image/*
	// or octet-stream; foreign non-image named cover.* stays as attachment.
	for _, p := range edited.Pictures {
		if !isCoverAttachment(p.MIME, coverFileName(p)) {
			return nil, fmt.Errorf("%w: a %q picture cannot be stored as Matroska cover art (only an image, or an unsniffable --force cover, is supported)",
				waxerr.ErrUnsupportedTag, p.MIME)
		}
	}

	if !ch.any() {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	// Per-tag outcomes for preservation, covered-set, and group render (cross-scope).
	ed := computeEditDecisions(d.groups, albumGroupIndex(d.groups), base.Tags, edited.Tags)

	// TITLE SimpleTag also projects to Title: force ch.simple so title-only edits
	// drop stale scoped TITLE via renderTags (else two titles after edit).
	if ch.title && !ch.simple && hasManagedTitleTag(d.groups) {
		ch.simple = true
	}

	// Scoped-only TITLE (no Info.Title): force title render to migrate when Info
	// present; else buildAlbumGroup/checkPreservable keep or refuse the SimpleTag.
	if ch.simple && !ch.title && !d.hasSegTitle && d.wb.info != nil {
		if _, ok := edited.Tags.First(tag.Title); ok {
			ch.title = true
		}
	}

	// WebM: refuse cover write (Plan backstop; Capabilities gates transfer). Keep isWebM in sync.
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

	// Uncapturable SeekHead/Cues: refuse rather than corrupt indexes on move.
	if err := checkIndexCaptured(d.wb); err != nil {
		return nil, err
	}
	if err := checkSegmentCRCCaptured(d.wb); err != nil {
		return nil, err
	}
	if err := checkPreservable(d, ch, ed); err != nil {
		return nil, err
	}

	// Edited album SimpleTag with uncapturable structure: WarnTagStructureDropped
	// before planAbsorb (avoid double warn on absorb-then-shift).
	if ch.simple {
		if keys := tagStructureDropped(d, ed); len(keys) > 0 {
			report.Warnings = core.WarnKeyed(report.Warnings, core.WarnTagStructureDropped,
				"an edited album tag dropped its secondary language, binary value, or nested sub-tags", keys...)
		}
	}

	// Reserved technical names never emitted; warn once per key if edit supplied them.
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

	// Multi TITLE: only first survives Info.Title; WarnValueDropped before absorb/shift
	// (shared reason with TransferClassifier).
	if ch.title {
		if vals, _ := edited.Tags.Get(tag.Title); len(vals) > 1 {
			report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueDropped,
				multiTitleDroppedReason, tag.Title)
		}
	}

	// Flattening nested/secondary chapter structure: plan-time warning (not on full clear).
	if ch.chapters && len(edited.Chapters) > 0 && d.chapters != nil && d.chapters.defLossy {
		report.Warnings = core.Warn(report.Warnings, core.WarnChaptersFlattened,
			"chapter edit dropped the default edition's nested sub-chapters or secondary-language titles")
	}

	// Non-front cover role: plan-time role-loss warning.
	if ch.pictures && core.PicturesLoseMetadata(edited.Pictures, core.PictureLossRoleOnly) {
		report.Warnings = core.Warn(report.Warnings, core.WarnPictureMetadataDropped,
			"Matroska preserves only the front cover's role; other picture roles read back as Other")
	}
	// --force non-image cover: no honesty warning (reads as Unrecognized; lint flags).

	pl, err := planAbsorb(d, base, edited, ch, ed, report)
	if err != nil {
		if !isFallback(err) {
			return nil, err
		}
		if pl, err = planShift(d, base, edited, ch, ed, report); err != nil {
			return nil, err
		}
	}
	// Collapse to a clean no-op when the rendered result re-projects to base: an edit of
	// only reserved technical names, or one whose every value survives at its existing
	// scope.
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
		// file-name convention, description sanitized, MIME re-sniffed), not the raw edited
		// roles: a role Matroska cannot represent would otherwise look like a change on every
		// copy even though the on-disk cover set is already identical.
		pictures: !core.EqualPictures(base.Pictures, reprojectPictures(edited.Pictures)),
		chapters: !core.EqualChapters(base.Chapters, edited.Chapters),
	}
}

// isWebM reports whether the EBML DocType is the WebM subset, matching the
// case-insensitive comparison the reader uses for the container label.
func isWebM(docType string) bool { return strings.EqualFold(docType, "webm") }

// checkIndexCaptured refuses the edit when a SeekHead/Cues element cannot be safely
// rewritten: more than one is present (a linked index - only the last is captured, so
// the others would be copied with stale offsets), or its single instance was not
// captured at parse (a read failure or over-limit declared size).
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

// checkSegmentCRCCaptured refuses an edit when a Segment-level CRC-32 is present but
// its bytes could not be captured for neutralization (an over-limit declared size left
// segVoidFromCRC nil).
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

// checkPreservable refuses the edit when an element the writer must copy verbatim could
// not be captured (its bytes exceeded the alloc limit, so captureRaw returned nil) -
// dropping it would silently lose data.
func checkPreservable(d *doc, ch changes, ed *editDecisions) error {
	tooBig := func(what string) error {
		return fmt.Errorf("%w: a Matroska %s is too large to rewrite within the alloc limit", waxerr.ErrUnsupportedTag, what)
	}
	if ch.simple {
		for i, g := range d.groups {
			if i == ed.albumIdx {
				// Synced in place: every SimpleTag the edit keeps verbatim - a non-canonical tag,
				// OR a managed tag whose canonical key was not edited (preserved with its
				// language/binary/nested structure) - needs its captured bytes.
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
			// Re-rendered to drop its edited keys: every surviving SimpleTag needs its bytes,
			// and a scope-narrowing group needs its Targets bytes too (else the rebuild would
			// silently lose the narrowing).
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

// renderTags builds the new Tags element bytes from the edited canonical set, returning
// nil when the result would be empty (so the Tags element is dropped).
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
// group will still carry after the edit, so the album-group sync can leave an unchanged
// value at its own scope instead of re-emitting it at album scope (which would
// duplicate it on every save and risk a spurious cross-scope conflict).
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
		// Sanitize the raw SimpleTag value to match the canonical TagSet (parse.go projects
		// through core.SanitizeUTF8).
		for _, c := range projectTag(st.name, core.SanitizeUTF8(st.value), g.scope) {
			fn(c)
		}
	}
}

// renderGroup re-renders one Tag group. The album group is synced to the edited
// canonical set; a non-album group is preserved verbatim or, when it carries an edited
// key, re-rendered to drop that key.
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

// renderNonAlbumGroup renders a track/edition/chapter/part-scoped group.
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
	// Carry the freshly rendered bytes (not the stale input raw, which still holds the
	// dropped SimpleTags) so the returned document's group equals a fresh parse of the
	// output - a re-edit of that document then preserves this group verbatim correctly
	// instead of re-emitting the dropped key or dropping the group.
	out.raw = rendered
	return out, rendered, true
}

// editDecisions carries the value-level outcome of one tag edit across the render
// helpers: which parsed SimpleTags the edit drops, and per changed key the values the
// album-scope sync must emit.
type editDecisions struct {
	ek        map[tag.Key]bool     // keys whose value lists changed (tag.Diff)
	albumIdx  int                  // index of the group buildAlbumGroup syncs into, -1 if none
	drop      map[[2]int]bool      // {group index, tag index} -> dropped by this edit
	albumVals map[tag.Key][]string // per changed key: values to emit at album scope, edited order
}

func (ed *editDecisions) dropped(gi, ti int) bool { return ed.drop[[2]int{gi, ti}] }
func (ed *editDecisions) edited(k tag.Key) bool   { return ed.ek[k] }

// contribDecision is the fate of one canonical contribution a parsed SimpleTag
// projects: whether the value survives at the scope that already holds it, and whether
// keeping it claimed one of the edited values (so the album-scope re-emit must not
// write that value a second time).
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
// which SimpleTags drop, and per changed key the values the album-scope sync must emit.
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
				// An echo does not kill its tag until the album values are known.
				d.echo, d.kept = true, !boolean
				ds = append(ds, d)
				continue
			}
			// Album-scope copies would permute the re-emitted list, boolean copies would dodge
			// the "1"/"0" canonicalization, and a fold-duplicated value kept in place would let
			// the reader's echo suppression halve its multiplicity - none of those may claim.
			d.claimable = groups[d.owner[0]].scope != core.ScopeAlbum && !boolean &&
				folds[k][core.Fold(d.value)] == 1
			ds = append(ds, d)
		}
	}

	// A tag with a contribution that can never be claimed is doomed outright; the
	// remaining claims are then handed out in emission order among the surviving tags, one
	// round per newly doomed tag: a denial dooms the loser's tag (any dropped contribution
	// kills the whole SimpleTag), which releases its own claims for the next round, so a
	// value freed by a dying tag is re-offered to a denied twin instead of being relocated
	// to album scope.
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

	// Echoes are judged once the album values are known: one survives while its fold stays
	// suppressed on re-read, covered by the album emit or by a value kept in place at a
	// position that projects before it (ds follows projectionOrder per key, so a walk in
	// order sees exactly the earlier folds).
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

	// A dropped echo kills its tag, releasing any values that tag had claimed into the
	// album re-emit.
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
func meaningfulLang(lang string) bool {
	return lang != "" && !strings.EqualFold(lang, "und")
}

// tagStructureDropped returns the canonical keys whose album-scope SimpleTag carried
// structure the flat canonical model cannot hold - a TagLanguage, a TagBinary value, or
// nested sub-tags - that this edit drops because the key's value changed (ed.dropped),
// re-emitting it flat at album scope.
func tagStructureDropped(d *doc, ed *editDecisions) []tag.Key {
	var keys []tag.Key
	seen := map[tag.Key]bool{}
	for gi, g := range d.groups {
		for ti, st := range g.tags {
			if !ed.dropped(gi, ti) {
				continue
			}
			// A plain string tag loses nothing on a flat re-emit.
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

// isManagedTitle reports whether a SimpleTag maps to the canonical Title, which is
// always homed in Info.Title - so it is never kept as an album SimpleTag in the output.
func isManagedTitle(st simpleTag) bool {
	k, ok := mapping.MatroskaTagKey(st.name)
	return ok && k == tag.Title
}

// migratesToInfo reports whether a managed TITLE SimpleTag will migrate to Info.Title
// and thus be dropped from the Tags element: it is a managed title AND an Info element
// exists to receive it.
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
// projects into the canonical Title alongside Info.Title.
func hasManagedTitleTag(groups []tagGroup) bool {
	for _, g := range groups {
		for _, st := range g.tags {
			if isManagedTitle(st) {
				return true
			}
		}
	}
	return false
}

// narrowsScope reports whether the group's Targets restrict it below album scope (a
// track/edition/chapter UID or any explicit target type/level).
func narrowsScope(g tagGroup) bool {
	return g.trackUID || g.editionUID || g.chapterUID || g.targetTypeValue != 0 || g.targetType != ""
}

// editedKeySet returns the canonical keys whose values differ between the base and
// edited tag sets, via the shared tag.Diff primitive.
func editedKeySet(base, edited tag.TagSet) map[tag.Key]bool {
	ek := map[tag.Key]bool{}
	for _, c := range tag.Diff(base, edited) {
		ek[c.Key] = true
	}
	return ek
}

// buildAlbumGroup renders the album-scope group: the preserved Targets (carrying any
// UID), the kept non-canonical SimpleTags verbatim, then the synced canonical
// SimpleTags.
func buildAlbumGroup(group *tagGroup, gi int, base, edited tag.TagSet, covered, albumOwn map[tag.Key][]string, others map[tag.Key][]scopedContribution, ed *editDecisions, infoPresent bool) (tagGroup, []byte) {
	out := tagGroup{scope: core.ScopeAlbum}
	var simple []byte
	if group != nil {
		out = *group
		out.tags = nil
		// Preserve every SimpleTag the edit does not drop, verbatim from its captured bytes -
		// custom names, technical stats, binary, nested trees, AND managed tags whose
		// canonical key was not edited (keeping the language, binary value, or secondary
		// structure a flat re-emit would lose).
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
			// Unchanged key carried verbatim elsewhere - by a narrower scope (covered) or by
			// this album group's own preserved SimpleTags (albumOwn) - is re-emitted only for
			// the canonical values not already preserved.
			if sub := slices.Concat(covered[k], albumOwn[k]); len(sub) > 0 {
				emit := subtractFold(vals, sub)
				if !reprojectsTo(k, vals, albumOwn[k], emit, others[k]) {
					// A partially subtracted fold would leave the album emit suppressing the surviving
					// narrower copies, shrinking the value's multiplicity on re-read: re-emit
					// everything the album group itself does not preserve, and let the narrower copies
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
			// Canonicalize a recognized boolean word to "1"/"0", matching the Vorbis, ID3, and
			// MP4 writers so every format stores a boolean field identically.
			for i, v := range vals {
				vals[i] = tag.CanonicalBoolValue(v)
			}
		}
		name := mapping.MatroskaTagName(k)
		if mapping.MatroskaTechnicalName(name) {
			continue // reserved technical name: never emitted, warned at plan time
		}
		for _, v := range vals {
			// A present empty value from `set KEY=` is emitted as a zero-length SimpleTag, not
			// skipped.
			stb := simpleTagBytes(name, v)
			simple = append(simple, stb...)
			// Carry the freshly rendered bytes as this synthesized tag's raw, so the result
			// document's album group equals a fresh parse of the output.
			out.tags = append(out.tags, simpleTag{name: name, value: v, hasValue: true, raw: stb})
		}
	}
	if len(simple) == 0 {
		return tagGroup{}, nil
	}
	// out already carries any existing Targets from group.
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
// time, preserving survivor case and order.
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
// inserting, or removing the Title child) and recomputes the CRC-32.
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
		// size VINT, so the 4 value bytes are not always at index 2.
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
// file name.
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

// randomUID returns a random non-zero 64-bit UID, used for a created AttachedFile's
// FileUID and a created ChapterAtom's ChapterUID.
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

// uniqueAttachmentName resolves an AttachedFile name from its role stem and extension,
// inserting a numeric suffix before the extension (cover.png, cover_1.png, ...) until
// it does not collide with a name already used in this Attachments element.
func uniqueAttachmentName(stem, ext string, used map[string]bool) string {
	name := stem + ext
	for i := 1; used[name]; i++ {
		name = fmt.Sprintf("%s_%d%s", stem, i, ext)
	}
	return name
}

// imageExt returns the conventional extension for a cover MIME.
func imageExt(mime string) string {
	if ext := bits.ImageExtension(mime); ext != "" {
		return ext
	}
	return ".jpg"
}
