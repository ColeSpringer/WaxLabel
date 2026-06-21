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
// The size change is absorbed into a reserved Void element so the clusters do not
// move - keeping every Cues/SeekHead position valid - which is the layout
// mkvmerge and ffmpeg both write (SeekHead, Void, ..., Clusters). Only the SeekHead
// entries for the header elements that shift within the rebuilt header are
// patched, in place at their original width, and the affected CRC-32s recomputed.
// When the file has no usable Void, the tail is shifted instead (see planShift).
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

	// Re-rendering a default edition that carried nested sub-chapters or
	// secondary-language titles drops that structure (the flat chapter model cannot
	// hold it). Surface it as a plan-time warning rather than flattening silently -
	// the established precedent for a lossy chapter write. A full clear is a removal,
	// not a flatten, so it does not warn.
	if ch.chapters && len(edited.Chapters) > 0 && d.chapters != nil && d.chapters.defLossy {
		report.Warnings = core.Warn(report.Warnings, core.WarnChaptersFlattened,
			"chapter edit dropped the default edition's nested sub-chapters or secondary-language titles")
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
				// Synced in place: only its kept non-canonical SimpleTags need raw.
				for _, st := range g.tags {
					if !isManaged(st.name) && st.raw == nil {
						return tooBig("custom tag")
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
	covered := coveredByOtherScopes(d.groups, albumIdx)
	var content []byte

	for i, g := range d.groups {
		newGroup, gb, keep := renderGroup(g, base, edited, covered, ek, i == albumIdx)
		if !keep {
			continue
		}
		content = append(content, gb...)
		groups = append(groups, newGroup)
	}

	// No album group existed: create one carrying the canonical set.
	if albumIdx < 0 {
		newGroup, gb := buildAlbumGroup(nil, base, edited, covered)
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

// coveredByOtherScopes returns the canonical keys carried by non-album groups
// (preserved verbatim), so the album-group sync can leave an unchanged such key at
// its own scope instead of re-emitting it at album scope (which would duplicate it
// on every save and risk a spurious cross-scope conflict).
func coveredByOtherScopes(groups []tagGroup, albumIdx int) map[tag.Key]bool {
	covered := map[tag.Key]bool{}
	for i, g := range groups {
		if i == albumIdx {
			continue
		}
		for _, st := range g.tags {
			if k, ok := mapping.MatroskaTagKey(st.name); ok {
				covered[k] = true
			}
		}
	}
	return covered
}

// renderGroup re-renders one Tag group. The album group is synced to the edited
// canonical set; a non-album group is preserved verbatim or, when it carries an
// edited key, re-rendered to drop that key. keep is false when the group becomes
// empty.
func renderGroup(g tagGroup, base, edited tag.TagSet, covered, ek map[tag.Key]bool, isAlbum bool) (out tagGroup, raw []byte, keep bool) {
	if !isAlbum {
		return renderNonAlbumGroup(g, ek)
	}
	ng, gb := buildAlbumGroup(&g, base, edited, covered)
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
// group: its name maps to a canonical key whose value the edit changed, so the
// authoritative value now lives at album scope (or was cleared). A non-canonical or
// unedited SimpleTag is kept. It is the single drop predicate shared by the
// preservation check (which tags need captured bytes) and the renderer (which tags
// are emitted), so the two cannot diverge on which tags survive.
func droppedByEdit(st simpleTag, ek map[tag.Key]bool) bool {
	k, ok := mapping.MatroskaTagKey(st.name)
	return ok && ek[k]
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
// SimpleTags. group is the existing album group (nil when creating one). A key
// already carried verbatim by another scope is re-emitted here only if the edit
// changed it (a canonical edit defaults to album scope); an unchanged one stays
// put, avoiding duplication.
func buildAlbumGroup(group *tagGroup, base, edited tag.TagSet, covered map[tag.Key]bool) (tagGroup, []byte) {
	out := tagGroup{scope: core.ScopeAlbum}
	var simple []byte
	if group != nil {
		out = *group
		out.tags = nil
		// Keep non-canonical tags verbatim (custom names, technical stats, binary,
		// nested trees) in place.
		for _, st := range group.tags {
			if isManaged(st.name) {
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
		if covered[k] {
			if bv, _ := base.Get(k); slices.Equal(bv, vals) {
				continue // unchanged and owned by another scope: leave it there
			}
		}
		name := mapping.MatroskaTagName(k)
		for _, v := range vals {
			if v == "" {
				continue
			}
			simple = append(simple, simpleTagBytes(name, v)...)
			out.tags = append(out.tags, simpleTag{name: name, value: v})
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
// the new Info element bytes and the new segment title.
func renderInfo(ib *infoBlock, title string) (raw []byte, newTitle string) {
	r := ib.raw
	root, ok := readElement(core.BytesSource(r), 0, int64(len(r)), int64(len(r)))
	if !ok {
		return nil, ""
	}
	headerLen := int(root.dataStart) // ID + size VINT
	var titleEl []byte
	if title != "" {
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
