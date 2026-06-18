package matroska

import (
	"fmt"
	"strconv"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// outItem is one element in the planned output: a literal (re-rendered) or a
// verbatim copy from the source. origStart is the element's original file offset
// (-1 for a created or flex element) so SeekHead targets can be remapped to new
// positions; outOff is its offset in the output, filled during layout.
type outItem struct {
	id        uint64
	lit       []byte // non-nil: emit literally
	srcOff    int64  // copy source when lit == nil
	n         int64  // byte length (len(lit) when literal)
	origStart int64
	outOff    int64
	kind      itemKind
}

type itemKind uint8

const (
	itemOther itemKind = iota
	itemSeek
	itemVoid
	itemInfo
	itemTags
	itemAttach
	itemChapters
)

// layout is the resolved output shape both write strategies produce: the segment
// list, the new top-level child list, and the new file positions and bytes of the
// re-derivable structures (SeekHead/Cues/Info/Attachments). buildResult turns it
// into the post-write Media.
type layout struct {
	segs         []bits.Segment
	children     []l1elem
	size         int64
	clusterStart int64
	delta        int64 // tail shift (0 for absorption)

	seekRaw     []byte
	seekStart   int64
	cuesRaw     []byte
	cuesStart   int64
	infoRaw     []byte
	infoStart   int64
	chaptersRaw []byte // re-rendered Chapters bytes (nil with a chapter edit = dropped)
	hasAttach   bool
	attach      attachBlock
}

// planAbsorb realizes the edit by absorbing its size change into a reserved Void
// so the clusters never move. It re-renders the changed Segment children, sizes
// the flex Void to keep the header's total length, patches the SeekHead positions
// of the elements that shifted within the header (in place, at original width),
// and copies the clusters and everything after them byte-for-byte. It returns
// errFallback when the layout is not absorption-friendly (no Void, an edit in the
// post-cluster tail, a drop of an element, or a position that would overflow its
// width), so Plan tries planShift instead.
func planAbsorb(d *doc, base, edited *core.Media, ch changes, report core.WriteReport) (*core.WritePlan, error) {
	wb := d.wb

	r, err := renderChanged(d, base, edited, ch)
	if err != nil {
		return nil, err
	}

	// A changed element in the post-cluster tail, or a drop of an existing
	// element, is not absorption-friendly - the shift path rebuilds the SeekHead.
	for _, c := range wb.children {
		switch {
		case c.id == idTags && ch.simple:
			if c.start >= wb.clusterStart || r.tags == nil {
				return nil, errFallback
			}
		case c.id == idInfo && ch.title:
			if c.start >= wb.clusterStart {
				return nil, errFallback
			}
		case c.id == idAttachments && ch.pictures:
			if c.start >= wb.clusterStart || r.attach == nil {
				return nil, errFallback
			}
		case c.id == idChapters && ch.chapters:
			if c.start >= wb.clusterStart || r.chapters == nil {
				return nil, errFallback
			}
		}
	}

	// Build the header output items in source order, substituting changed children
	// and marking the first Void as the flex absorber.
	var items []outItem
	flexIdx, seekIdx := -1, -1
	tagsPlaced, attachPlaced, chaptersPlaced := false, false, false
	for _, c := range wb.children {
		if c.start >= wb.clusterStart {
			break
		}
		switch {
		case c.id == idSeekHead && wb.seek != nil:
			seekIdx = len(items)
			items = append(items, outItem{id: c.id, srcOff: c.start, n: c.total(), origStart: c.start, kind: itemSeek})
		case c.id == idVoid && flexIdx < 0:
			flexIdx = len(items)
			items = append(items, outItem{id: idVoid, origStart: c.start, kind: itemVoid})
		case c.id == idTags && ch.simple:
			if tagsPlaced {
				continue // a second Tags master: the first already carries every group
			}
			tagsPlaced = true
			items = append(items, litItem(idTags, r.tags, c.start, itemTags))
		case c.id == idInfo && ch.title:
			items = append(items, litItem(idInfo, r.info, c.start, itemInfo))
		case c.id == idAttachments && ch.pictures:
			if attachPlaced {
				continue // a second Attachments master: the first already carries every file
			}
			attachPlaced = true
			items = append(items, litItem(idAttachments, r.attach, c.start, itemAttach))
		case c.id == idChapters && ch.chapters:
			if chaptersPlaced {
				continue
			}
			chaptersPlaced = true
			items = append(items, litItem(idChapters, r.chapters, c.start, itemChapters))
		default:
			items = append(items, outItem{id: c.id, srcOff: c.start, n: c.total(), origStart: c.start})
		}
	}
	if flexIdx < 0 {
		return nil, errFallback
	}
	// Created top-level elements (origStart -1) are appended but not added to an
	// existing SeekHead, matching the shift path: the index is patched in place at a
	// stable size, and SeekHead is an optional index (readers scan level-1 elements
	// to find an unindexed Tags/Attachments/Chapters).
	if ch.simple && !tagsPlaced && r.tags != nil {
		items = append(items, litItem(idTags, r.tags, -1, itemTags))
	}
	if ch.pictures && !attachPlaced && r.attach != nil {
		items = append(items, litItem(idAttachments, r.attach, -1, itemAttach))
	}
	if ch.chapters && !chaptersPlaced && r.chapters != nil {
		items = append(items, litItem(idChapters, r.chapters, -1, itemChapters))
	}

	// Size the flex Void so the header's total byte length is preserved.
	headerLen := wb.clusterStart - wb.segDataStart
	var others int64
	for i, it := range items {
		if i == flexIdx {
			continue
		}
		others += it.n
	}
	void := headerLen - others
	vb := voidOfTotal(void)
	if vb == nil { // void < 2: the edited header does not fit; shift the tail instead
		return nil, errFallback
	}
	items[flexIdx].lit = vb
	items[flexIdx].n = void

	// Lay the items out and map each original start to its new start.
	off := wb.segDataStart
	oldToNew := map[int64]int64{}
	for i := range items {
		items[i].outOff = off
		if items[i].origStart >= 0 {
			oldToNew[items[i].origStart] = off
		}
		off += items[i].n
	}
	if off != wb.clusterStart {
		return nil, errFallback
	}

	// Patch the SeekHead positions of the header elements that moved.
	if seekIdx >= 0 {
		patched, ok := patchSeekAbsorb(wb.seek, wb.segDataStart, oldToNew)
		if !ok {
			return nil, errFallback
		}
		items[seekIdx].lit, items[seekIdx].srcOff = patched, 0
	}

	lay := assembleItems(wb, items, 0)
	report.BytesAfter = wb.size
	report.Operations = absorbOps(ch, len(edited.Pictures))
	result := buildResult(d, edited, r, ch, lay)
	return &core.WritePlan{Segments: lay.segs, NoOp: false, Report: report, Result: result}, nil
}

// rendered holds every Segment child an edit re-rendered, so the two write
// strategies and buildResult thread one value rather than a long parameter list.
// A nil tags/attach/chapters byte slice means that element is dropped.
type rendered struct {
	tags     []byte
	groups   []tagGroup
	info     []byte
	title    string
	attach   []byte
	atts     []attachment
	chapters []byte
}

// renderChanged renders every Segment child the edit touches. The caller (Plan)
// has already guaranteed an Info element exists when the title changed, so the
// only failure here is an unparseable captured Info - a real ErrInvalidData, kept
// distinct from the internal errFallback that signals "try the shift path".
func renderChanged(d *doc, base, edited *core.Media, ch changes) (*rendered, error) {
	r := &rendered{title: d.segTitle}
	if ch.simple {
		r.tags, r.groups = renderTags(d, base.Tags, edited.Tags)
	}
	if ch.title {
		et, _ := edited.Tags.First(tag.Title)
		r.info, r.title = renderInfo(d.wb.info, et)
		if r.info == nil {
			return nil, fmt.Errorf("%w: Matroska Info element could not be re-rendered", waxerr.ErrInvalidData)
		}
	}
	if ch.pictures {
		r.attach, r.atts = renderAttachments(d, edited.Pictures)
	}
	if ch.chapters {
		r.chapters = renderChapters(d, edited.Chapters)
	}
	return r, nil
}

func litItem(id uint64, b []byte, origStart int64, kind itemKind) outItem {
	return outItem{id: id, lit: b, n: int64(len(b)), origStart: origStart, kind: kind}
}

// assembleItems turns the header items into a segment list (EBML+Segment header
// verbatim, each header item literal-or-copy, then the cluster tail verbatim) and
// records the structures' new positions/bytes for buildResult. segHeader covers
// [0, segDataStart): in absorption the Segment size is unchanged so it is copied;
// the shift path passes a literal segHeader instead (delta != 0).
func assembleItems(wb *writeBase, items []outItem, delta int64) layout {
	lay := layout{size: wb.size + delta, clusterStart: wb.clusterStart, delta: delta}
	lay.segs = []bits.Segment{bits.Copy(0, wb.segDataStart)}
	lay.children = make([]l1elem, 0, len(items)+4)
	for _, it := range items {
		if it.lit != nil {
			lay.segs = append(lay.segs, bits.Lit(it.lit))
		} else {
			lay.segs = append(lay.segs, bits.Copy(it.srcOff, it.n))
		}
		lay.children = append(lay.children, l1elem{id: it.id, start: it.outOff, dataEnd: it.outOff + it.n})
		switch it.kind {
		case itemSeek:
			lay.seekRaw, lay.seekStart = it.lit, it.outOff
		case itemInfo:
			lay.infoRaw, lay.infoStart = it.lit, it.outOff
		case itemAttach:
			lay.hasAttach = true
			lay.attach = attachBlock{start: it.outOff, end: it.outOff + it.n, hasCRC: wb.attach != nil && wb.attach.hasCRC}
		case itemChapters:
			lay.chaptersRaw = it.lit
		}
	}
	if wb.size > wb.clusterStart {
		lay.segs = append(lay.segs, bits.Copy(wb.clusterStart, wb.size-wb.clusterStart))
	}
	for _, c := range wb.children {
		if c.start < wb.clusterStart {
			continue
		}
		nc := c
		nc.start += delta
		nc.dataStart += delta
		nc.dataEnd += delta
		lay.children = append(lay.children, nc)
		if c.id == idCues && wb.cues != nil {
			lay.cuesStart = nc.start
		}
	}
	// Cues bytes for the result: unchanged in absorption (clusters fixed), so the
	// base bytes apply at the (possibly shifted) start. The shift path overrides
	// this with patched bytes.
	if wb.cues != nil && lay.cuesRaw == nil {
		lay.cuesRaw = wb.cues.raw
		if lay.cuesStart == 0 {
			lay.cuesStart = childStart(lay.children, idCues)
		}
	}
	// SeekHead bytes for the result when it was copied unchanged (no entry moved).
	if wb.seek != nil && lay.seekRaw == nil {
		lay.seekRaw, lay.seekStart = wb.seek.raw, childStart(lay.children, idSeekHead)
	}
	// Info bytes for the result when it was copied unchanged.
	if wb.info != nil && lay.infoRaw == nil {
		lay.infoRaw, lay.infoStart = wb.info.raw, childStart(lay.children, idInfo)
	}
	return lay
}

// childStart returns the new file offset of the first output child with the given
// ID, so the result document's SeekHead/Cues/Info positions reflect their true new
// location (a copied-but-relocated element does not simply shift by the total
// delta) and so equal a fresh parse of the output.
func childStart(children []l1elem, id uint64) int64 {
	for _, c := range children {
		if c.id == id {
			return c.start
		}
	}
	return 0
}

// voidOfTotal renders a Void element whose total byte length is exactly total
// (>= 2): the ID, a size VINT of the smallest width that lets the zero-filled
// content reach the target, then that content.
func voidOfTotal(total int64) []byte {
	if total < 2 {
		return nil // a Void is at least its 1-byte ID + 1-byte size; the caller guards this
	}
	for w := 1; w <= 8; w++ {
		content := total - 1 - int64(w)
		if content < 0 {
			break
		}
		if sz, ok := sizeVINTWidthOK(uint64(content), w); ok {
			out := make([]byte, 0, total)
			out = append(out, byte(idVoid))
			out = append(out, sz...)
			out = append(out, make([]byte, content)...)
			return out
		}
	}
	return nil
}

// patchSeekAbsorb copies the SeekHead bytes and rewrites each SeekPosition whose
// target moved (per oldToNew) in place at its original width, then recomputes the
// CRC. ok is false if a new value does not fit its slot, so Plan falls back to
// the shift path (which rebuilds the SeekHead at minimal width).
func patchSeekAbsorb(sh *seekHead, segDataStart int64, oldToNew map[int64]int64) ([]byte, bool) {
	raw := make([]byte, len(sh.raw))
	copy(raw, sh.raw)
	for _, e := range sh.entries {
		newAbs, ok := oldToNew[segDataStart+int64(e.target)]
		if !ok {
			continue
		}
		val := uintDataWidth(uint64(newAbs-segDataStart), e.valLen)
		if val == nil {
			return nil, false
		}
		copy(raw[e.valOff:e.valOff+e.valLen], val)
	}
	recomputeCRC(raw, sh.crc)
	return raw, true
}

// recomputeCRC rewrites a master element's CRC-32 over its (patched) content; a
// no-op when the element carried no CRC.
func recomputeCRC(raw []byte, c *crcSpot) {
	if c == nil {
		return
	}
	crc := crcElement(raw[c.contentStart:])
	copy(raw[c.valOff:c.valOff+4], crc[2:])
}

func absorbOps(ch changes, pics int) []string {
	var ops []string
	if ch.simple {
		ops = append(ops, "rewrote Tags (clusters not moved)")
	}
	if ch.title {
		ops = append(ops, "rewrote Info.Title")
	}
	if ch.pictures {
		ops = append(ops, "rewrote Attachments", "pictures: "+strconv.Itoa(pics))
	}
	if ch.chapters {
		ops = append(ops, "rewrote Chapters (clusters not moved)")
	}
	return ops
}

// buildResult constructs the post-write Media so the engine returns a Document
// without re-parsing. Its canonical view is re-projected from the written groups
// (the same project Parse uses), so it equals a fresh parse of the output.
func buildResult(d *doc, edited *core.Media, r *rendered, ch changes, lay layout) *core.Media {
	depth := bits.NewDepth(64)
	limit := int64(maxElement)

	nd := &doc{
		docType:     d.docType,
		segTitle:    r.title,
		groups:      d.groups,
		attachments: d.attachments,
		chapters:    d.chapters,
		tracks:      d.tracks,
		codecID:     d.codecID,
		sampleRate:  d.sampleRate,
		channels:    d.channels,
		bitDepth:    d.bitDepth,
	}
	if ch.simple {
		nd.groups = r.groups
	}
	if ch.pictures {
		nd.attachments = r.atts
	}

	// Re-derive the chapter view from the rendered bytes (re-parsing them, the
	// seekFromRaw pattern) so the returned Document's chapters equal a fresh parse;
	// an edit that dropped the Chapters element leaves none.
	var resChapters []core.Chapter
	if ch.chapters {
		nd.chapters, resChapters = chaptersFromRaw(lay.chaptersRaw, depth, limit)
	} else {
		resChapters = core.CloneChapters(edited.Chapters)
	}

	wb := &writeBase{
		size:         lay.size,
		segStart:     d.wb.segStart,
		segSizeOff:   d.wb.segSizeOff,
		segSizeLen:   d.wb.segSizeLen,
		segUnknown:   d.wb.segUnknown,
		segDataStart: d.wb.segDataStart,
		segDataEnd:   d.wb.segDataEnd + lay.delta,
		children:     lay.children,
		clusterStart: lay.clusterStart,
		tagsCRC:      d.wb.tagsCRC,
	}
	if lay.seekRaw != nil {
		wb.seek = seekFromRaw(lay.seekRaw, lay.seekStart, depth, limit)
	}
	if lay.cuesRaw != nil {
		wb.cues = cuesFromRaw(lay.cuesRaw, lay.cuesStart, depth, limit)
	}
	if lay.infoRaw != nil {
		wb.info = infoFromRaw(lay.infoRaw, lay.infoStart, depth, limit)
	}
	if lay.hasAttach {
		ab := lay.attach
		wb.attach = &ab
	} else if d.wb.attach != nil && !ch.pictures {
		ab := *d.wb.attach
		ab.start += lay.delta
		ab.end += lay.delta
		wb.attach = &ab
	}
	nd.wb = wb

	tags, families := project(nd)
	res := &core.Media{
		Format:     core.FormatMatroska,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   resultPictures(edited.Pictures, ch),
		Chapters:   resChapters,
		Families:   families,
		Warnings:   mediaWarnings(tags, families),
		Native:     nd,
		Identity:   core.Identity{Size: lay.size},
	}
	if lay.clusterStart < lay.size {
		res.AudioStart = lay.clusterStart
		res.AudioEnd = clusterEnd(lay.children)
	}
	return res
}

// clusterEnd returns the end of the last Cluster among the output children.
func clusterEnd(children []l1elem) int64 {
	var end int64
	for _, c := range children {
		if c.id == idCluster && c.dataEnd > end {
			end = c.dataEnd
		}
	}
	return end
}

// resultPictures returns the picture set the post-write Document reports. When the
// covers were rewritten, it re-derives them as a fresh parse would - the role is
// normalized through the cover-art file-name convention (only front cover survives
// as a distinct role; others read back as Other) and the geometry is re-sniffed -
// so the returned document equals a re-parse of the output. When pictures were not
// touched they were preserved verbatim, so the input set already matches.
func resultPictures(pics []core.Picture, ch changes) []core.Picture {
	if !ch.pictures {
		return clonePics(pics)
	}
	out := make([]core.Picture, len(pics))
	for i, p := range pics {
		np := core.Picture{
			Type:        pictureType(coverFileName(p)),
			MIME:        p.MIME,
			Description: p.Description,
			Data:        p.Data,
		}
		np.SniffInto()
		out[i] = np
	}
	return out
}

func clonePics(p []core.Picture) []core.Picture {
	if p == nil {
		return nil
	}
	out := make([]core.Picture, len(p))
	copy(out, p)
	return out
}
