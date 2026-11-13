package matroska

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
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

// splitNumberReason grades a slashed number among several values, which [splitNumberValues]
// writes split.
const splitNumberReason = "a slashed number among several values is stored as the number alone; its total goes under the total key where that holds none"

// splitNumberValues is ts as a write stores it: of several TRACKNUMBER or DISCNUMBER values, one
// spelled "n/total" is written as the number. Its total goes under the total key only where ts
// holds none there: a total the set states is the total, and one read out of a number does not
// join it. A read splits every such SimpleTag ([projectTag]), so one left slashed would read
// back as a second total on each rewrite. A sole value is the editor's to split. split is false,
// and ts returned as given, when nothing splits.
func splitNumberValues(ts tag.TagSet) (out tag.TagSet, split bool) {
	out = ts
	for _, numKey := range []tag.Key{tag.TrackNumber, tag.DiscNumber} {
		vals, _ := ts.Get(numKey)
		if len(vals) < 2 {
			continue
		}
		nums := make([]string, 0, len(vals))
		var totals []string
		slashed := false
		for _, v := range vals {
			num, total, ok := tag.NumberTotalSplit(numKey, v)
			if !ok {
				nums = append(nums, v)
				continue
			}
			slashed = true
			if num != "" {
				nums = append(nums, num)
			}
			if total != "" && !slices.Contains(totals, total) {
				totals = append(totals, total)
			}
		}
		if !slashed {
			continue
		}
		if !split {
			out, split = ts.Clone(), true
		}
		out.Set(numKey, nums...)
		if totalKey := tag.TotalKey(numKey); len(totals) > 0 && !ts.Has(totalKey) {
			out.Set(totalKey, totals...)
		}
	}
	return out, split
}

// TransferClassifier: multi-value TITLE => Lossy (Info.Title is single-valued;
// first kept); a slashed number among several values => Lossy (stored split); reserved
// technical/statistics names => Dropped. Reuses shared reason consts. Plain
// [core.FieldClassifier]; other fields use format grade.
func TransferClassifier(key tag.Key, values []string, _ *core.Media) (core.Disposition, string, bool) {
	if key == tag.Title && len(values) > 1 {
		return core.Lossy, multiTitleDroppedReason, true
	}
	if (key == tag.TrackNumber || key == tag.DiscNumber) && len(values) > 1 &&
		slices.ContainsFunc(values, func(v string) bool { _, _, split := tag.NumberTotalSplit(key, v); return split }) {
		return core.Lossy, splitNumberReason, true
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
	if tags, split := splitNumberValues(edited.Tags); split {
		e := *edited
		e.Tags = tags
		edited = &e
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
	if d.wb.unreadFrom > 0 {
		return nil, fmt.Errorf("%w: the read of the Segment stops at offset %d, at bytes that are no element, so the file cannot be rewritten safely",
			waxerr.ErrInvalidData, d.wb.unreadFrom)
	}

	// Per-tag outcomes for preservation, covered-set, and group render (cross-scope).
	ed := computeEditDecisions(d.groups, albumGroupIndex(d.groups), base, edited)

	// A title edit drops the TITLE SimpleTags that read as the title (else two titles after the
	// edit) and writes it to Info.Title; in a file tagged by level that keeps no Info.Title it is
	// written to the track-level Tag alone. Either way the Tags are rendered again, as they are
	// for an edit that ends the level convention. Any other edit leaves the title's SimpleTags,
	// and the Info, as they are.
	ch.simple = ch.simple || ed.rewritesTags()
	ch.info = ch.title && ed.writesInfoTitle(d.hasSegTitle)

	// WebM: refuse cover write here too (Capabilities gates transfer). Keep isWebM in sync.
	if ch.pictures && isWebM(d.docType) {
		return nil, fmt.Errorf("%w: cover art cannot be written to %s WebM file (Attachments is not in the WebM subset)",
			waxerr.ErrUnsupportedTag, core.IndefiniteArticle("WebM"))
	}

	// The segment title lives in Info.Title; a file with no Info element (Info is
	// mandatory, so this is malformed) cannot receive one.
	if ch.info && d.wb.info == nil {
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

	// A rewritten element is rendered from what was read of it.
	if ch.simple {
		report.Warnings = d.tagsDamage.warnDropped(report.Warnings, "Tags")
	}
	if ch.info {
		report.Warnings = d.infoDamage.warnDropped(report.Warnings, "Info")
	}
	if ch.pictures {
		report.Warnings = d.attachDamage.warnDropped(report.Warnings, "Attachments")
	}
	if ch.chapters {
		report.Warnings = d.chapDamage.warnDropped(report.Warnings, "Chapters")
		if d.dupChapters > 0 {
			report.Warnings = core.Warn(report.Warnings, core.WarnDuplicateTagBlockDropped, fmt.Sprintf(
				"%d Chapters element(s) behind the first, which were not read, are not carried into the rewritten file", d.dupChapters))
		}
	}

	// An edited SimpleTag holding what a flat one cannot: WarnTagStructureDropped
	// before planAbsorb (avoid double warn on absorb-then-shift).
	if ch.simple {
		if keys := tagStructureDropped(d, ed, base); len(keys) > 0 {
			report.Warnings = core.WarnKeyed(report.Warnings, core.WarnTagStructureDropped,
				"an edited tag dropped what a plain SimpleTag cannot hold: a language, a TagDefault of 0, a binary value, or nested sub-tags", keys...)
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

	// Multi TITLE: only the first is stored; WarnValueDropped before absorb/shift
	// (shared reason with TransferClassifier).
	if ch.title {
		if vals, _ := edited.Tags.Get(tag.Title); len(vals) > 1 {
			report.Warnings = core.WarnKeyed(report.Warnings, core.WarnValueDropped,
				multiTitleDroppedReason, tag.Title)
		}
	}

	// A clear removes the Chapters element, so every edition goes, not the default one alone:
	// leaving the others would bring one of them into view as the file's chapters.
	otherEditions := 0
	if ch.chapters && len(edited.Chapters) == 0 && d.chapters != nil {
		otherEditions = max(len(d.chapters.editions)-1, 0)
	}
	if otherEditions > 0 {
		report.Warnings = core.Warn(report.Warnings, core.WarnChapterMetadataDropped, fmt.Sprintf(
			"clearing the chapters removes the %d other edition(s) the file holds along with the default one", otherEditions))
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
	// --force non-image cover: no warning here (reads as Unrecognized; lint flags it).

	pl, err := planAbsorb(d, base, edited, ch, ed, report)
	if err != nil {
		if !isFallback(err) {
			return nil, err
		}
		if pl, err = planShift(d, base, edited, ch, ed, report); err != nil {
			return nil, err
		}
	}
	if otherEditions == 1 {
		pl.Report.Operations = append(pl.Report.Operations, "Chapters: 1 other edition removed")
	} else if otherEditions > 1 {
		pl.Report.Operations = append(pl.Report.Operations, fmt.Sprintf("Chapters: %d other editions removed", otherEditions))
	}
	// Collapse to a clean no-op when the rendered result re-projects to base: an edit of
	// only reserved technical names, one whose every value survives at its existing scope, or
	// a title edit that leaves the Info.Title's text and only asks for values a single title
	// cannot hold.
	title, _ := edited.Tags.First(tag.Title)
	if np := core.DowngradeNoOp(core.FormatMatroska, edited.Identity.Size, base, pl.Result,
		base.Tags.Equal(pl.Result.Tags), ch.info && title != d.segTitle, pl.Report.Warnings); np != nil {
		return np, nil
	}
	return pl, nil
}

// changes records what an edit touches: the SimpleTag set (any canonical key except Title, and
// a comment's or lyric's language), the title, the Attachments cover set, and the Chapters
// element. info is set by Plan where the title edit writes the Info.Title; in a file tagged by
// level that keeps none it writes a SimpleTag instead.
type changes struct {
	simple   bool
	title    bool
	info     bool
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
		// Compare the whole Title value list: adding or changing a later value is an
		// edit even though only the first reaches the single-valued Info.Title.
		simple: !b.Equal(e) || textLanguagesChanged(base, edited, tag.Comment) || textLanguagesChanged(base, edited, tag.Lyrics),
		title:  !slices.Equal(bt, et),
		// Compare base against the reprojected edited set (roles reduced to the cover-art
		// file names, description sanitized, MIME re-sniffed). A role Matroska cannot
		// represent would otherwise look like a change on every copy.
		pictures: !core.EqualPictures(base.Pictures, reprojectPictures(edited.Pictures)),
		chapters: !core.EqualChapters(base.Chapters, edited.Chapters),
	}
}

// isWebM reports whether the EBML DocType is WebM, case-insensitively like containerName.
func isWebM(docType string) bool { return strings.EqualFold(docType, "webm") }

// checkIndexCaptured refuses the edit when a SeekHead/Cues element cannot be
// rewritten: more than one is present (a linked index; only the last is captured, so
// the others would copy with stale offsets), or the single instance was not captured
// at parse (a failed read).
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
// its bytes could not be captured for neutralization (a failed read left segVoidFromCRC
// nil).
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

// checkPreservable refuses the edit when an element the writer must copy verbatim was
// not captured (a failed read left captureRaw's bytes nil).
func checkPreservable(d *doc, ch changes, ed *editDecisions) error {
	uncaptured := func(what string) error {
		return fmt.Errorf("%w: a Matroska %s could not be read for rewrite", waxerr.ErrUnsupportedTag, what)
	}
	if ch.simple {
		for i, g := range d.groups {
			if i == ed.albumIdx {
				// Album group, synced in place: every SimpleTag kept verbatim (a non-canonical
				// tag, or a managed tag whose key was not edited) needs its captured bytes.
				for ti, st := range g.tags {
					if ed.dropped(i, ti) {
						continue // re-emitted from the canonical set, or moved to Info.Title: no raw needed
					}
					if st.raw == nil {
						return uncaptured("tag")
					}
				}
				continue
			}
			if !groupTouchedBy(len(g.tags), i, ed) && !ed.writesTo(i) {
				if g.raw != nil {
					continue // preserved verbatim from the whole Tag element's bytes
				}
				if len(g.tags) == 0 && !g.malformed {
					// No whole-element bytes and no SimpleTags to rebuild from.
					return uncaptured("tag group")
				}
			}
			// Re-rendered to drop its edited keys, or to take a title or an artist written at
			// track level: every surviving SimpleTag needs its bytes, and a scope-narrowing
			// group needs its Targets bytes too.
			kept := 0
			for ti, st := range g.tags {
				if ed.dropped(i, ti) {
					continue // its value now lives at album scope
				}
				if st.raw == nil {
					return uncaptured("tag")
				}
				kept++
			}
			if (kept > 0 || ed.writesTo(i)) && g.targetsRaw == nil && narrowsScope(g) {
				return uncaptured("tag targets")
			}
		}
	}
	if ch.pictures {
		// An attachment that is no picture is copied; one that is can be rendered from its picture.
		for _, a := range d.attachments {
			if !a.image && a.raw == nil {
				return uncaptured("attachment")
			}
		}
	}
	if ch.chapters && d.chapters != nil {
		// The default edition is re-rendered from the parsed model; every other edition
		// is copied from its captured bytes.
		for i, e := range d.chapters.editions {
			if i != d.chapters.defIdx && e.raw == nil {
				return uncaptured("chapter edition")
			}
		}
	}
	return nil
}

// errFallback signals that absorption cannot apply (no reserved Void, or the edited
// header does not fit) and Plan should try the shift path. Never returned to the caller.
var errFallback = fmt.Errorf("matroska: absorption not applicable")

func isFallback(err error) bool { return errors.Is(err, errFallback) }

// renderTags builds the new Tags element bytes from the edited canonical set, returning
// nil when the result would be empty (so the Tags element is dropped).
func renderTags(d *doc, base, edited tag.TagSet, ed *editDecisions) (raw []byte, groups []tagGroup) {
	covered, albumOwn, others := coveredByOtherScopes(d.groups, ed)
	var content []byte
	add := func(g tagGroup, gb []byte) {
		if gb != nil {
			content = append(content, gb...)
			groups = append(groups, g)
		}
	}
	sync := func(group *tagGroup, gi int, album, track bool) {
		add(buildSyncGroup(group, gi, album, track, base, edited, covered, albumOwn, others, ed))
	}

	for i, g := range d.groups {
		album, track := i == ed.albumIdx, ed.writesTo(i)
		if album || track {
			sync(&g, i, album, track)
		} else if out, gb, keep := renderNonAlbumGroup(g, i, ed); keep {
			add(out, gb)
		}
	}
	// No album group existed: create one carrying the canonical set.
	if ed.albumIdx < 0 {
		sync(nil, -1, true, false)
	}

	if len(content) == 0 {
		return nil, nil
	}
	// Lead with a CRC-32 when the source Tags master did (the mkvmerge convention).
	return masterElement(idTags, content, d.wb.tagsCRC), groups
}

// coveredByOtherScopes returns, per canonical key, the projected values a non-album
// group still carries after the edit. The album-group sync leaves those at their own
// scope, since re-emitting them at album scope would duplicate them on every save
// and risk a spurious cross-scope conflict.
func coveredByOtherScopes(groups []tagGroup, ed *editDecisions) (covered, albumOwn map[tag.Key][]string, others map[tag.Key][]scopedContribution) {
	// Per key, the case-folded values the album scope itself keeps after the edit, plus the
	// same values as ordered lists (albumOwn) for buildSyncGroup's subtraction.
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
		if i == ed.albumIdx || !g.fileLevel() {
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
		for _, c := range projectTag(st, g, ed.conv) {
			fn(c)
		}
	}
}

// renderNonAlbumGroup renders a track/edition/chapter/part-scoped group.
func renderNonAlbumGroup(g tagGroup, gi int, ed *editDecisions) (out tagGroup, raw []byte, keep bool) {
	if !groupTouchedBy(len(g.tags), gi, ed) && g.raw != nil {
		return g, g.raw, true // preserve verbatim
	}
	// An untouched group without whole-element bytes, a malformed one among them, is rebuilt
	// from its captured parts.
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
	return out, masterElement(idTag, content, g.hasCRC), true
}

// editDecisions carries the value-level outcome of one tag edit across the render
// helpers: which parsed SimpleTags the edit drops, and per changed key the values the
// album-scope sync must emit.
type editDecisions struct {
	ek        map[tag.Key]bool     // keys whose value lists, or comment/lyrics languages, changed
	albumIdx  int                  // index of the group buildSyncGroup syncs into, -1 if none
	drop      map[[2]int]bool      // {group index, tag index} -> dropped by this edit
	albumVals map[tag.Key][]string // per changed key: values to emit at album scope, edited order
	// albumLangs holds the language of each albumVals value of COMMENT and LYRICS, and langs
	// the language of each edited value of those keys ([textLangs]).
	albumLangs, langs map[tag.Key][]string

	// conv: the file is tagged by level ([titleConvention]), which is how its SimpleTags read.
	// convAfter: it still is once the edit is written, which is how the new ones are named.
	conv, convAfter bool
	// trackIdx is the group a title or an artist is written to in a file that stays tagged by
	// level, where at album level they would read as the album's ([trackGroupIndex]). -1 in any
	// other.
	trackIdx int
	// trackEmit is what the edit writes there, in order.
	trackEmit []trackValue
	// reuse marks the dropped SimpleTags whose bytes are written again: each holds just a value
	// the edit writes back into its group ([editDecisions.holdsJust]). albumNew reports an album
	// value no such SimpleTag holds, written as a new one.
	reuse    map[[2]int]bool
	albumNew bool
}

// trackValue is one SimpleTag an edit writes to the track-level group.
type trackValue struct {
	key   tag.Key
	value string
}

// rewritesTags reports whether the edit drops a SimpleTag, which a title edit does without
// changing any key but the title, as does an edit that ends the level convention.
func (ed *editDecisions) rewritesTags() bool { return len(ed.drop) > 0 }

// albumScoped reports whether the values of key k are written to the album-scope group: not the
// title, which lives in Info.Title or at track level, nor the artist of a file tagged by level.
func (ed *editDecisions) albumScoped(k tag.Key) bool {
	return k != tag.Title && !(ed.convAfter && k == tag.Artist)
}

// writesTo reports whether the edit writes a title or an artist into group gi.
func (ed *editDecisions) writesTo(gi int) bool { return gi == ed.trackIdx && len(ed.trackEmit) > 0 }

// writesInfoTitle reports whether an edit of the title writes the Info.Title: always, but in a
// file that stays tagged by level and keeps none (infoTitle false), whose title is a track-level
// SimpleTag.
func (ed *editDecisions) writesInfoTitle(infoTitle bool) bool { return !ed.convAfter || infoTitle }

// trackGroupIndex picks the group a title or an artist is written to in a file tagged by level:
// the first at level 40 or under that holds a TITLE, which such a file has. -1 with none.
func trackGroupIndex(groups []tagGroup) int {
	return slices.IndexFunc(groups, func(g tagGroup) bool {
		return g.fileLevel() && g.lowerLevel() && slices.ContainsFunc(g.tags, simpleTag.titled)
	})
}

// textLangs returns the modeled language of each of m's COMMENT or LYRICS values, nil for any
// other key.
func textLangs(m *core.Media, key tag.Key) []string {
	var items []core.TextItem
	switch key {
	case tag.Comment:
		items = m.Comments
	case tag.Lyrics:
		items = m.Lyrics
	}
	if items == nil {
		return nil
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = tagLanguage(it.Language)
	}
	return out
}

// textLanguagesChanged reports whether an edit changes the language of a COMMENT or LYRICS
// value, which the tag sets do not show.
func textLanguagesChanged(base, edited *core.Media, key tag.Key) bool {
	return !slices.Equal(textLangs(base, key), textLangs(edited, key))
}

// claimOf is what a SimpleTag kept in place claims of an edited key: the value, and for a
// comment or lyric its language too, so a tag whose language the edit changes is rendered again.
func claimOf(key tag.Key, value, lang string) string {
	if key != tag.Comment && key != tag.Lyrics {
		return value
	}
	return value + "\x00" + lang
}

// langAt returns langs[i], or "" past its end.
func langAt(langs []string, i int) string {
	if i < len(langs) {
		return langs[i]
	}
	return ""
}

func (ed *editDecisions) dropped(gi, ti int) bool { return ed.drop[[2]int{gi, ti}] }
func (ed *editDecisions) edited(k tag.Key) bool   { return ed.ek[k] }

// contribDecision is the fate of one canonical contribution a parsed SimpleTag
// projects: whether the value survives at its own scope, and whether keeping it
// claimed one of the edited values, which the album-scope re-emit then omits.
type contribDecision struct {
	key       tag.Key
	owner     [2]int // {group index, tag index} of the SimpleTag it came from
	value     string
	claim     string // what keeping it claims of the edited values ([claimOf])
	echo      bool
	claimable bool // an emitted, non-album, non-boolean, fold-unique contribution
	kept      bool
	claimed   bool
}

// computeEditDecisions resolves one edit against the parsed groups, once per write:
// which SimpleTags drop, and per changed key the values the album-scope sync must emit.
func computeEditDecisions(groups []tagGroup, albumIdx int, base, edited *core.Media) *editDecisions {
	ek := editedKeySet(base, edited)
	conv := titleConvention(groups)
	ed := decideEdit(groups, albumIdx, conv, conv, ek, edited).settle(groups, edited)
	// No edit starts the convention: outside it none writes a TITLE SimpleTag.
	if !conv || ed.conventionAfter(groups) {
		return ed
	}
	// The edit leaves no TITLE at level 40 or under, or none at album level, so what stays at
	// album level would read as the file's own title and artist. The album's are written again
	// under the names that say so, the artist is written at album scope like any key, and an
	// edited title goes to the Info.Title.
	for gi, g := range groups {
		for ti, st := range g.tags {
			if k, ok := tagKey(st, g, true); ok && g.fileLevel() && !ed.dropped(gi, ti) && (k == tag.Album || k == tag.AlbumArtist) {
				if plain, _ := tagKey(st, g, false); plain != k {
					ek[k] = true
				}
			}
		}
	}
	return decideEdit(groups, albumIdx, true, false, ek, edited).settle(groups, edited)
}

// settle completes the decisions with what follows from them: where a title and an artist are
// written in a file tagged by level, and which dropped SimpleTags are written again.
func (ed *editDecisions) settle(groups []tagGroup, edited *core.Media) *editDecisions {
	ed.trackIdx = -1
	if ed.convAfter {
		ed.trackIdx = trackGroupIndex(groups)
		if title, ok := edited.Tags.First(tag.Title); ok && ed.edited(tag.Title) {
			ed.trackEmit = append(ed.trackEmit, trackValue{tag.Title, title})
		}
		for _, v := range ed.albumVals[tag.Artist] {
			ed.trackEmit = append(ed.trackEmit, trackValue{tag.Artist, v})
		}
	}
	ed.markReused(groups)
	return ed
}

// conventionAfter reports whether a file tagged by level still is once the edit is written as
// one: the read's rule ([titleConvention]) asked of the TITLE SimpleTags the edit leaves and the
// ones it writes. A new album value is an album-level TITLE, the album-scope sync group of such
// a file standing at album level, and an edited title a track-level one.
func (ed *editDecisions) conventionAfter(groups []tagGroup) bool {
	upper, lower := titleLevels(groups, func(gi, ti int) bool {
		at := [2]int{gi, ti}
		return !ed.drop[at] || ed.reuse[at]
	})
	titled := slices.ContainsFunc(ed.trackEmit, func(tv trackValue) bool { return tv.key == tag.Title })
	return (upper || ed.albumNew) && (lower || titled)
}

// holdsJust reports whether SimpleTag st of group g holds the value the edit writes for key k
// and no other, under a name that reads as k once written. Its bytes can then stand for the
// value, with what a flat SimpleTag cannot say: a TagLanguageBCP47, a TagDefault, nested
// SimpleTags.
func (ed *editDecisions) holdsJust(st simpleTag, g tagGroup, k tag.Key, value, lang string) bool {
	if st.raw == nil || !st.hasValue {
		return false
	}
	if written, ok := tagKey(st, g, ed.convAfter); !ok || written != k {
		return false
	}
	cs := projectTag(st, g, ed.conv)
	return len(cs) == 1 && cs[0].key == k && cs[0].value == value &&
		claimOf(k, value, cs[0].lang) == claimOf(k, value, lang)
}

// markReused finds, for each value the edit writes into the album group or the track-level one,
// the first SimpleTag it drops there that holds just that value ([editDecisions.reuse]).
func (ed *editDecisions) markReused(groups []tagGroup) {
	ed.reuse, ed.albumNew = map[[2]int]bool{}, false
	take := func(gi int, k tag.Key, value, lang string) bool {
		if gi < 0 {
			return false
		}
		for ti, st := range groups[gi].tags {
			if at := [2]int{gi, ti}; ed.drop[at] && !ed.reuse[at] && ed.holdsJust(st, groups[gi], k, value, lang) {
				ed.reuse[at] = true
				return true
			}
		}
		return false
	}
	for k := range ed.ek {
		if !ed.albumScoped(k) {
			continue
		}
		for i, v := range ed.albumVals[k] {
			if tag.IsBooleanKey(k) {
				v = tag.CanonicalBoolValue(v)
			}
			if !take(ed.albumIdx, k, v, langAt(ed.albumLangs[k], i)) && k == tag.Album {
				ed.albumNew = true
			}
		}
	}
	for _, tv := range ed.trackEmit {
		take(ed.trackIdx, tv.key, tv.value, "")
	}
}

// decideEdit is computeEditDecisions for the keys ek. conv is whether the file is tagged by
// level, convAfter whether the written one is: there the artist is written whole to the
// track-level group. A key is written whole, too, where the values its SimpleTags would keep in
// place do not read back as the edit asks ([editDecisions.readsBack]).
func decideEdit(groups []tagGroup, albumIdx int, conv, convAfter bool, ek map[tag.Key]bool, edited *core.Media) *editDecisions {
	whole := map[tag.Key]bool{tag.Artist: convAfter}
	for {
		ed := decideKeeping(groups, albumIdx, conv, convAfter, ek, edited, whole)
		left := ed.leftInPlace(groups)
		settled := true
		for k := range ek {
			if k != tag.Title && !whole[k] && !ed.readsBack(k, left[k], edited) {
				whole[k], settled = true, false
			}
		}
		if settled {
			return ed
		}
	}
}

// leftInPlace returns, per edited key, what the file-level SimpleTags the edit does not drop
// contribute to it, in file order.
func (ed *editDecisions) leftInPlace(groups []tagGroup) map[tag.Key][]scopedContribution {
	left := map[tag.Key][]scopedContribution{}
	for gi, g := range groups {
		if !g.fileLevel() {
			continue
		}
		for ti, st := range g.tags {
			if ed.dropped(gi, ti) || !st.hasValue {
				continue
			}
			for _, c := range projectTag(st, g, ed.conv) {
				if ed.edited(c.key) {
					left[c.key] = append(left[c.key], c)
				}
			}
		}
	}
	return left
}

// readsBack reports whether key k reads back as the edit asks once the decisions are written:
// the values left in place for it ([editDecisions.leftInPlace]) and the ones written at album
// scope, projected as a read projects them. Keeping a value in place counts on the scopes ahead
// of it staying; with the album-scope values gone, what were suppressed copies at another scope
// lead the projection.
func (ed *editDecisions) readsBack(k tag.Key, left []scopedContribution, edited *core.Media) bool {
	// Album scope leads the projection wherever its SimpleTags stand.
	contribs := slices.Clone(left)
	for _, v := range ed.albumVals[k] {
		contribs = append(contribs, scopedContribution{key: k, value: v, scope: core.ScopeAlbum})
	}
	var got []string
	for _, e := range projectionOrder(k, contribs) {
		if e.emitted {
			got = append(got, contribs[e.index].value)
		}
	}
	want, _ := edited.Tags.Get(k)
	want = slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want) // a reorder across scopes is the read's own
}

// decideKeeping is decideEdit with the keys of whole written whole: every SimpleTag that holds
// one is dropped, and none keeps a value of it in place.
func decideKeeping(groups []tagGroup, albumIdx int, conv, convAfter bool, ek map[tag.Key]bool, edited *core.Media, whole map[tag.Key]bool) *editDecisions {
	ed := &editDecisions{
		ek:         ek,
		albumIdx:   albumIdx,
		drop:       map[[2]int]bool{},
		albumVals:  map[tag.Key][]string{},
		albumLangs: map[tag.Key][]string{},
		langs:      map[tag.Key][]string{tag.Comment: textLangs(edited, tag.Comment), tag.Lyrics: textLangs(edited, tag.Lyrics)},
		conv:       conv,
		convAfter:  convAfter,
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
		if !g.fileLevel() {
			continue // a targeted group's tags are not the file's: no edit of them reaches it
		}
		for ti, st := range g.tags {
			if ed.edited(tag.Title) && managedTitle(st, g, conv) {
				ed.drop[[2]int{gi, ti}] = true
				continue
			}
			if !st.hasValue {
				continue
			}
			for _, c := range projectTag(st, g, conv) {
				if c.key == tag.Title || !ed.edited(c.key) {
					continue
				}
				if whole[c.key] {
					ed.drop[[2]int{gi, ti}] = true
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
	// exact claim the edit wants, and how many edited values share each folded form.
	exact := make(map[tag.Key]map[string]int, len(ed.ek))
	folds := make(map[tag.Key]map[string]int, len(ed.ek))
	for k := range ed.ek {
		vals, _ := edited.Tags.Get(k)
		ex, fl := map[string]int{}, map[string]int{}
		for i, v := range vals {
			ex[claimOf(k, v, langAt(ed.langs[k], i))]++
			fl[core.Fold(v)]++
		}
		exact[k], folds[k] = ex, fl
	}

	var ds []contribDecision
	for _, k := range keyOrder {
		boolean := tag.IsBooleanKey(k)
		for _, e := range projectionOrder(k, contribs[k]) {
			c := contribs[k][e.index]
			d := contribDecision{key: k, owner: owners[k][e.index], value: c.value, claim: claimOf(k, c.value, c.lang)}
			if !e.emitted {
				// An echo does not kill its tag until the album values are known.
				d.echo, d.kept = true, !boolean
				ds = append(ds, d)
				continue
			}
			// Not claimable: an album-scope copy would permute the re-emitted list, a boolean
			// copy would skip the "1"/"0" canonicalization, and a fold-duplicated value kept
			// in place would let the reader's echo suppression halve its multiplicity.
			d.claimable = groups[d.owner[0]].scope != core.ScopeAlbum && !boolean &&
				folds[k][core.Fold(d.value)] == 1
			ds = append(ds, d)
		}
	}

	// A tag with a contribution that can never be claimed is doomed outright. Claims are
	// then handed out in emission order among the surviving tags, one round per newly
	// doomed tag: a denied claim dooms its tag (any dropped contribution drops the whole
	// SimpleTag), which releases that tag's claims for the next round, so a freed value
	// is re-offered to a denied twin rather than relocated to album scope.
	// A tag a title edit or a key written whole already drops is doomed too.
	doomed := maps.Clone(ed.drop)
	for _, d := range ds {
		if !d.echo && (!d.claimable || exact[d.key][d.claim] == 0) {
			doomed[d.owner] = true
		}
	}
	for {
		avail := make(map[tag.Key]map[string]int, len(exact))
		for k, ex := range exact {
			avail[k] = maps.Clone(ex)
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
			if avail[d.key][d.claim] > 0 {
				avail[d.key][d.claim]--
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

	// Judge echoes once the album values are known: one survives while its fold stays
	// suppressed on re-read, covered by the album emit or by a value kept in place that
	// projects before it (ds follows projectionOrder per key, so a walk in order sees
	// exactly the earlier folds).
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

// releaseDoomedClaims applies the tag-level conjunction: a SimpleTag doomed by one
// dropped contribution cannot carry its other contributions either, so the values
// those had claimed return to the album-scope re-emit.
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
// claimed, in edited order. These are the values the album-scope sync writes.
func (ed *editDecisions) setAlbumVals(edited *core.Media, ds []contribDecision) {
	claimed := map[tag.Key]map[string]int{}
	for _, d := range ds {
		if !d.claimed {
			continue
		}
		if claimed[d.key] == nil {
			claimed[d.key] = map[string]int{}
		}
		claimed[d.key][d.claim]++
	}
	for k := range ed.ek {
		vals, _ := edited.Tags.Get(k)
		take := claimed[k]
		out := make([]string, 0, len(vals))
		var langs []string
		for i, v := range vals {
			lang := langAt(ed.langs[k], i)
			if id := claimOf(k, v, lang); take[id] > 0 {
				take[id]--
				continue
			}
			out = append(out, v)
			langs = append(langs, lang)
		}
		ed.albumVals[k], ed.albumLangs[k] = out, langs
	}
}

// unchangedLangs returns the language of each of vals, values of the unchanged key k: that of
// the first edited value of that text not yet taken.
func (ed *editDecisions) unchangedLangs(edited tag.TagSet, k tag.Key, vals []string) []string {
	langs := ed.langs[k]
	if langs == nil {
		return nil
	}
	all, _ := edited.Get(k)
	byText := make(map[string][]string, len(all)) // a text's languages, first to last
	for j, a := range all {
		byText[a] = append(byText[a], langAt(langs, j))
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		if q := byText[v]; len(q) > 0 {
			out[i], byText[v] = q[0], q[1:]
		}
	}
	return out
}

// foldCovered reports whether vals carries value's case-folded form, in which case
// the album-scope emit suppresses a narrower-scope copy of it.
func foldCovered(vals []string, value string) bool {
	f := core.Fold(value)
	for _, v := range vals {
		if core.Fold(v) == f {
			return true
		}
	}
	return false
}

// meaningfulLang reports whether a language string is set and not "und".
func meaningfulLang(lang string) bool {
	return lang != "" && !strings.EqualFold(lang, "und")
}

// tagStructureDropped returns the canonical keys whose dropped SimpleTag (ed.dropped)
// carried structure the flat model cannot hold: a TagLanguage or TagLanguageBCP47 naming a
// language, a TagDefault of 0, a TagBinary value, or nested sub-tags. The edit re-emits such a
// key flat. The language of a comment or lyric base reads is in the model, so its tag loses
// none, and a SimpleTag written again as it is ([editDecisions.reuse]) loses nothing.
func tagStructureDropped(d *doc, ed *editDecisions, base *core.Media) []tag.Key {
	var keys []tag.Key
	seen := map[tag.Key]bool{}
	modeled := map[tag.Key]map[core.TextItem]bool{tag.Comment: {}, tag.Lyrics: {}}
	for _, it := range base.Comments {
		modeled[tag.Comment][it] = true
	}
	for _, it := range base.Lyrics {
		modeled[tag.Lyrics][it] = true
	}
	for gi, g := range d.groups {
		for ti, st := range g.tags {
			if !ed.dropped(gi, ti) || ed.reuse[[2]int{gi, ti}] {
				continue
			}
			k, ok := tagKey(st, g, ed.conv)
			if !ok || seen[k] {
				continue
			}
			langModeled := modeled[k][core.TextItem{Text: core.SanitizeUTF8(st.value), Language: st.language()}]
			// A plain string tag loses nothing on a flat re-emit.
			if (!meaningfulLang(st.lang) || langModeled) && !meaningfulLang(st.langBCP47) && !st.nonDefault && st.binary == 0 && len(st.sub) == 0 {
				continue
			}
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys
}

// managedTitle reports whether a SimpleTag of group g reads as the canonical Title, convention
// being the file's [titleConvention]. An edit of the title drops every such SimpleTag and writes
// the title where the file keeps it; an edit of another key keeps them.
func managedTitle(st simpleTag, g tagGroup, convention bool) bool {
	k, ok := tagKey(st, g, convention)
	return ok && k == tag.Title
}

// groupTouchedBy reports whether the edit drops any of the group's nTags SimpleTags,
// in which case the group is re-rendered rather than preserved verbatim.
func groupTouchedBy(nTags, gi int, ed *editDecisions) bool {
	for ti := range nTags {
		if ed.dropped(gi, ti) {
			return true
		}
	}
	return false
}

// narrowsScope reports whether the group's Targets restrict it below album scope (a
// track/edition/chapter/attachment UID or any explicit target type/level).
func narrowsScope(g tagGroup) bool {
	return g.trackUID || g.editionUID || g.chapterUID || g.attachUID || g.targetTypeValue != 0 || g.targetType != ""
}

// editedKeySet returns the canonical keys whose values differ between the base and
// edited tag sets, via the shared tag.Diff primitive, and COMMENT or LYRICS when only a
// language differs.
func editedKeySet(base, edited *core.Media) map[tag.Key]bool {
	ek := map[tag.Key]bool{}
	for _, c := range tag.Diff(base.Tags, edited.Tags) {
		ek[c.Key] = true
	}
	for _, k := range []tag.Key{tag.Comment, tag.Lyrics} {
		if edited.Tags.Has(k) && textLanguagesChanged(base, edited, k) {
			ek[k] = true
		}
	}
	return ek
}

// buildSyncGroup renders a group the edit writes into: the preserved Targets (carrying any
// UID), the kept SimpleTags verbatim, then the new ones. album is the album-scope group, synced
// to the canonical set; track is the group below album level that takes the title and artist of
// a file tagged by level ([editDecisions.trackEmit]). A nil group is the album-scope one,
// created at album level.
func buildSyncGroup(group *tagGroup, gi int, album, track bool, base, edited tag.TagSet, covered, albumOwn map[tag.Key][]string, others map[tag.Key][]scopedContribution, ed *editDecisions) (tagGroup, []byte) {
	out := tagGroup{scope: core.ScopeAlbum}
	level := 50
	var simple []byte
	if group != nil {
		out = *group
		out.tags = nil
		level = int(min(group.level(), 100))
		// Preserve every SimpleTag the edit does not drop, verbatim from its captured
		// bytes: custom names, technical stats, binary, nested trees, and managed tags
		// whose canonical key was not edited (keeping structure a flat re-emit would lose).
		for ti, st := range group.tags {
			if ed.dropped(gi, ti) {
				continue
			}
			if st.raw != nil {
				simple = append(simple, st.raw...)
				out.tags = append(out.tags, st)
			}
		}
	}
	written := map[int]bool{} // the dropped SimpleTags of the group written again as they are
	emit := func(k tag.Key, value, lang string) {
		if group != nil {
			for ti, st := range group.tags {
				if ed.reuse[[2]int{gi, ti}] && !written[ti] && ed.holdsJust(st, *group, k, value, lang) {
					written[ti] = true
					simple = append(simple, st.raw...)
					out.tags = append(out.tags, st)
					return
				}
			}
		}
		name := mapping.MatroskaTagNameForLevel(k, level, ed.convAfter)
		simple = append(simple, simpleTagBytes(name, value, lang)...)
		out.tags = append(out.tags, simpleTag{name: name, value: value, hasValue: true, lang: lang})
	}
	// Append the canonical set under the Matroska-spec names, in key order.
	for _, k := range edited.Keys() {
		if !album || !ed.albumScoped(k) {
			continue
		}
		vals, _ := edited.Get(k)
		var langs []string
		if bv, _ := base.Get(k); slices.Equal(bv, vals) && !ed.edited(k) {
			// An unchanged key carried verbatim elsewhere, by a narrower scope (covered) or by
			// this group's own preserved SimpleTags (albumOwn), re-emits only the values not
			// already preserved.
			if sub := slices.Concat(covered[k], albumOwn[k]); len(sub) > 0 {
				vs := subtractFold(vals, sub)
				if !reprojectsTo(k, vals, albumOwn[k], vs, others[k]) {
					// A partially subtracted fold would leave the album emit suppressing the
					// surviving narrower copies and shrink the value's multiplicity on re-read.
					// Re-emit everything the album group itself does not preserve; the narrower
					// copies become suppressed echoes.
					vs = subtractFold(vals, albumOwn[k])
				}
				vals = vs
				if len(vals) == 0 {
					continue
				}
			}
			langs = ed.unchangedLangs(edited, k, vals)
		} else {
			// A changed key emits what ed decided for album scope. Cloned because the
			// boolean canonicalization below rewrites vals in place and ed is shared
			// across an absorb-then-shift retry.
			vals, langs = slices.Clone(ed.albumVals[k]), ed.albumLangs[k]
		}
		if tag.IsBooleanKey(k) {
			// Canonicalize a recognized boolean word to "1"/"0", matching the Vorbis, ID3, and
			// MP4 writers so every format stores a boolean field identically.
			for i, v := range vals {
				vals[i] = tag.CanonicalBoolValue(v)
			}
		}
		if mapping.MatroskaTechnicalName(mapping.MatroskaTagName(k)) {
			continue // reserved technical name: never emitted, warned at plan time
		}
		for i, v := range vals {
			// A present empty value (set KEY=) is emitted as a zero-length SimpleTag.
			emit(k, v, langAt(langs, i))
		}
	}
	if track {
		for _, tv := range ed.trackEmit {
			emit(tv.key, tv.value, "")
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
	return out, masterElement(idTag, content, out.hasCRC)
}

// reprojectsTo reports whether albumOwn, emit, and the other scopes' contributions
// project back to exactly want, with the same multiplicity.
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

// subtractFold removes covered values from vals by folded form, one occurrence at a
// time, preserving survivor case and order.
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

// simpleTagBytes renders a SimpleTag with a name, a single string value and, when lang is set,
// its TagLanguage.
func simpleTagBytes(name, value, lang string) []byte {
	payload := stringElement(idTagName, name)
	if lang != "" {
		payload = append(payload, stringElement(idTagLang, lang)...)
	}
	return encElement(idSimpleTag, append(payload, stringElement(idTagString, value)...))
}

// albumGroupIndex returns the index of the group to sync canonical tags into: the first
// album-scope group at album level with no track/edition/chapter UID, else the first such group
// below it (level 40 to 49), or -1 if none. A group naming an attachment is at attachment scope,
// never this one.
func albumGroupIndex(groups []tagGroup) int {
	below := -1
	for i, g := range groups {
		if g.scope != core.ScopeAlbum || g.trackUID || g.editionUID || g.chapterUID {
			continue
		}
		if g.level() >= 50 {
			return i
		}
		if below < 0 {
			below = i
		}
	}
	return below
}

// renderInfo splices the edited Title into the captured Info's whole children (replacing,
// inserting, or removing the Title child) and recomputes the CRC-32.
func renderInfo(ib *infoBlock, title string, present bool) (raw []byte, newTitle string) {
	r := ib.raw[:ib.wholeEnd]
	root, ok, err := readElement(core.BytesSource(r), 0, int64(len(r)), int64(len(r)))
	if !ok || err != nil {
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
		// Recompute the CRC via recomputeCRC rather than a hardcoded content[0:6]: EBML
		// permits an overlong CRC size VINT, so the 4 value bytes are not always at index 2.
		fixed := make([]byte, len(content))
		copy(fixed, content)
		recomputeCRC(fixed, &crcSpot{valOff: ib.crc.valOff - headerLen, contentStart: ib.crc.contentStart - headerLen})
		content = fixed
	}
	return encElement(idInfo, content), title
}

// renderAttachments rebuilds the Attachments element for the edited pictures, returning nil when
// it holds nothing (so the element is dropped). An attachment that is no picture, and one whose
// picture the edit keeps, is copied as it is where it stands: name, type, description and UID.
// The pictures read back in the order of pics, so a new one is written ahead of the first kept
// attachment that follows it there, or at the end.
func renderAttachments(d *doc, base, pics []core.Picture) []byte {
	kept := keptAttachments(d.attachments, base, pics)
	owner := make(map[int]int, len(kept)) // attachment index -> the picture that keeps it
	for pi, ai := range kept {
		if ai >= 0 {
			owner[ai] = pi
		}
	}
	// Names used so far, so two same-role, same-MIME covers (both "cover.png") get
	// distinct FileNames. Seeded with the names of the attachments copied.
	used := map[string]bool{}
	for ai, a := range d.attachments {
		if _, ok := owner[ai]; ok || !a.image {
			used[a.name] = true
		}
	}
	var content []byte
	next := 0 // the first picture not yet written
	writeThrough := func(last int) {
		for ; next <= last; next++ {
			if ai := kept[next]; ai >= 0 {
				content = append(content, d.attachments[ai].raw...)
				continue
			}
			p := pics[next]
			name := uniqueAttachmentName(coverFileStem(p), imageExt(p.MIME), used)
			used[name] = true
			content = append(content, attachedFileBytes(p, name)...)
		}
	}
	for ai, a := range d.attachments {
		if !a.image {
			content = append(content, a.raw...)
		} else if pi, ok := owner[ai]; ok {
			writeThrough(pi) // nothing when a picture behind it in the file came first
		}
	}
	writeThrough(len(pics) - 1)
	if len(content) == 0 {
		return nil
	}
	hasCRC := d.wb.attach != nil && d.wb.attach.hasCRC
	return masterElement(idAttachments, content, hasCRC)
}

// keptAttachments returns, for each of pics, the index of the attachment it keeps: the first not
// yet taken whose picture, one of base, is what pics[i] reads back as. -1 is a picture to write.
func keptAttachments(atts []attachment, base, pics []core.Picture) []int {
	kept := make([]int, len(pics))
	taken := make([]bool, len(atts))
	for pi, p := range pics {
		kept[pi] = -1
		np, ok := reprojectPicture(p)
		if !ok {
			continue
		}
		for ai, a := range atts {
			if taken[ai] || a.raw == nil || !a.image || a.pic >= len(base) {
				continue
			}
			if core.EqualPictures(base[a.pic:a.pic+1], []core.Picture{np}) {
				kept[pi], taken[ai] = ai, true
				break
			}
		}
	}
	return kept
}

// attachedFileBytes renders one AttachedFile from a picture under an already-unique
// file name.
func attachedFileBytes(p core.Picture, name string) []byte {
	payload := stringElement(idFileName, name)
	payload = append(payload, stringElement(idFileMime, p.MIME)...)
	if p.Description != "" {
		payload = append(payload, stringElement(idFileDesc, p.Description)...)
	}
	payload = append(payload, encElement(idFileData, p.Data)...)
	payload = append(payload, encElement(idFileUID, mintUID())...)
	return encElement(idAttached, payload)
}

// mintUID is the value of a new UID element: a random UID in eight bytes whatever its
// magnitude, so the size a plan reports does not turn on the UID drawn.
func mintUID() []byte { return uintDataWidth(randomUID(), 8) }

// uidFallback makes randomUID's non-crypto path still yield distinct values, so a
// batch of created chapters or attachments cannot collide on one constant UID.
var uidFallback atomic.Uint64

// randomUID returns a random non-zero 64-bit UID (per the spec's "as random as possible"), used
// for a created AttachedFile's FileUID and a created ChapterAtom's ChapterUID.
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

// coverFileName is the canonical AttachedFile name for a cover role, used to derive
// the result view's Type. The stored name comes from renderAttachments and may carry
// a numeric suffix.
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

// imageExt returns the extension a cover of the given MIME is named with: the sniffer's for a
// type it knows, the short subtype of an image type it does not (svg+xml is .svg), else .jpg.
func imageExt(mime string) string {
	if ext := bits.ImageExtension(mime); ext != "" {
		return ext
	}
	sub, ok := strings.CutPrefix(strings.ToLower(mime), "image/")
	if !ok {
		return ".jpg"
	}
	switch sub, _, _ = strings.Cut(sub, "+"); sub {
	case "x-icon", "vnd.microsoft.icon":
		return ".ico"
	}
	// A file name takes a subtype only where it is a plain short word.
	if len(sub) < 2 || len(sub) > 4 || strings.ContainsFunc(sub, func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') }) {
		return ".jpg"
	}
	return "." + sub
}
