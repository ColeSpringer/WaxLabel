package matroska

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"

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
// move — keeping every Cues/SeekHead position valid — which is the layout
// mkvmerge and ffmpeg both write (SeekHead, Void, …, Clusters). Only the SeekHead
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

	// WebM does not include the Attachments element in its subset, so refuse to
	// write cover art into a webm file rather than emit something strict WebM
	// validators reject. Plain tag/Title writes to .webm remain fine.
	if ch.pictures && isWebM(d.docType) {
		return nil, fmt.Errorf("%w: cover art cannot be written to a WebM file (Attachments is not in the WebM subset)",
			waxerr.ErrUnsupportedTag)
	}

	// The segment title lives in Info.Title; a file with no Info element (Info is
	// mandatory, so this is malformed) cannot receive one.
	if ch.title && d.wb.info == nil {
		return nil, fmt.Errorf("%w: cannot write a title to a Matroska file that has no Info element",
			waxerr.ErrUnsupportedTag)
	}

	// A SeekHead/Cues whose structure could not be captured (e.g. an over-limit
	// declared size) cannot be repositioned, and copying it verbatim while other
	// elements move would leave its offsets pointing at the wrong bytes — refuse
	// rather than silently corrupt the index.
	if err := checkIndexCaptured(d.wb); err != nil {
		return nil, err
	}
	if err := checkPreservable(d, ch); err != nil {
		return nil, err
	}

	pl, err := planAbsorb(d, base, edited, ch, report)
	if err == nil {
		return pl, nil
	}
	if !isFallback(err) {
		return nil, err
	}
	return planShift(d, base, edited, ch, report)
}

// changes records which Segment children an edit touches: the SimpleTag set (any
// canonical key except Title), the Info.Title, and the Attachments cover set.
type changes struct {
	simple   bool
	title    bool
	pictures bool
}

func (c changes) any() bool { return c.simple || c.title || c.pictures }

// detectChanges splits a tag edit into its Title part (which lives in Info.Title)
// and the rest (which lives in Tags SimpleTags), plus the picture set.
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
	}
}

// isWebM reports whether the EBML DocType is the WebM subset, matching the
// case-insensitive comparison the reader uses for the container label.
func isWebM(docType string) bool { return strings.EqualFold(docType, "webm") }

// checkIndexCaptured refuses the edit when a SeekHead/Cues element cannot be
// safely rewritten: more than one is present (a linked index — only the last is
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
// returned nil) — dropping it would silently lose data. It covers the groups and
// non-canonical SimpleTags a tag edit preserves and the non-image attachments a
// cover edit preserves.
func checkPreservable(d *doc, ch changes) error {
	tooBig := func(what string) error {
		return fmt.Errorf("%w: a Matroska %s is too large to rewrite within the alloc limit", waxerr.ErrUnsupportedTag, what)
	}
	if ch.simple {
		albumIdx := albumGroupIndex(d.groups)
		for i, g := range d.groups {
			if i != albumIdx {
				if g.raw == nil {
					return tooBig("tag group")
				}
				continue
			}
			for _, st := range g.tags {
				if !isManaged(st.name) && st.raw == nil {
					return tooBig("custom tag")
				}
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
	return nil
}

// errFallback signals that the absorption path cannot apply (no reserved Void, or
// the edited header does not fit) so Plan should try the shift path instead. It
// is internal control flow, never returned to the caller.
var errFallback = fmt.Errorf("matroska: absorption not applicable")

func isFallback(err error) bool { return errors.Is(err, errFallback) }

// --- element renderers ------------------------------------------------------

// renderTags builds the new Tags element bytes from the edited canonical set,
// returning nil when the result would be empty (so the Tags element is dropped).
// It also returns the new group list for the result document. Non-canonical
// SimpleTags (custom, technical, binary, nested) and non-album groups are
// preserved verbatim from their captured raw bytes; canonical keys are synced
// into the album-scope group, written under their Matroska-spec names. Title is
// excluded — it is stored in Info.Title, not a SimpleTag.
func renderTags(d *doc, base, edited tag.TagSet) (raw []byte, groups []tagGroup) {
	albumIdx := albumGroupIndex(d.groups)
	covered := coveredByOtherScopes(d.groups, albumIdx)
	var content []byte

	for i, g := range d.groups {
		newGroup, gb, keep := renderGroup(g, base, edited, covered, i == albumIdx)
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
// canonical set; every other group is preserved verbatim. keep is false when the
// group becomes empty.
func renderGroup(g tagGroup, base, edited tag.TagSet, covered map[tag.Key]bool, isAlbum bool) (out tagGroup, raw []byte, keep bool) {
	if !isAlbum {
		if g.raw == nil {
			return tagGroup{}, nil, false
		}
		return g, g.raw, true // preserve verbatim (keeps any UID, nested/binary tags)
	}
	ng, gb := buildAlbumGroup(&g, base, edited, covered)
	if gb == nil {
		return tagGroup{}, nil, false
	}
	return ng, gb, true
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
// as possible"), making a collision with another attachment's UID negligible. The
// crypto/rand read effectively never fails; a non-zero constant is the fallback.
func fileUID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 1
	}
	if v := binary.BigEndian.Uint64(b[:]); v != 0 {
		return v
	}
	return 1
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
