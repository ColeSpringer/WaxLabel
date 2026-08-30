package mp4

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// This file is the write half of the two QuickTime metadata stores qtmeta.go reads: an
// mdta-handler meta (keys index plus index-keyed ilst items) and the classic text atoms
// sitting directly under moov.udta.
//
// Which store a canonical value goes to is decided once per file, never per key, so no key
// can ever live in two stores holding different values:
//
//   - A file with an ilst writes there. Every udta-level text atom is then rewritten to the
//     canonical value for its key, or removed when that key is gone, so the two agree.
//   - A file whose only store is udta-level text atoms writes there, but only while udta can
//     hold the whole edit (see udtaCanHoldAll). One key it cannot represent moves the whole
//     edit to the ilst rather than splitting the write across both stores.
//   - A file with no store at all keeps the mdirappl creation path.

// mdtaStore reports whether the file's meta box keys its ilst items through a "keys" index.
// The table must have parsed: with an absent or unreadable keys box, index-keyed items would
// name entries nothing holds, and rewriting the box would wipe a table the file owns and
// silently re-point its preserved items. Such a file keeps the four-character encoder, which
// this reader still reads back (an unresolved item falls through to the four-cc dispatch).
func mdtaStore(d *doc) bool {
	return d.metaHandler == mdtaHandler && d.keys != nil && len(d.keyNames) > 0
}

// buildMdtaItems is buildItems for an mdta store: every canonical value becomes a keys entry
// plus an ilst item named by that entry's 1-based index. Emitting a four-character atom here
// instead is what put a mixed iTunes item inside an mdta box.
//
// baseKeys is the file's existing keys index, carried forward by position and only ever
// appended to. Holding an index stable is what keeps a preserved item (one whose key the
// canonical vocabulary cannot represent) pointing at the same name after the rewrite, and it
// makes a re-edit of the same tags produce byte-identical output.
func buildMdtaItems(edited tag.TagSet, covr []item, preserved []item, baseKeys []string) ([]item, []string) {
	names := slices.Clone(baseKeys)
	index := make(map[string]int, len(names))
	byKey := make(map[tag.Key]int, len(names))
	for i, n := range names {
		if _, dup := index[n]; !dup {
			index[n] = i + 1
		}
		// An Apple recorder spells its keys "com.apple.quicktime.title" while the write
		// spelling is bare, so a name lookup alone would append a duplicate entry and orphan
		// the file's own. Reuse whichever entry already resolves to this canonical key.
		if k, ok := mapping.MP4MdtaKey(n); ok {
			if _, dup := byKey[k]; !dup {
				byKey[k] = i + 1
			}
		}
	}
	var out []item
	emit := func(i int, name string, payload []byte) {
		var nm [4]byte
		binary.BigEndian.PutUint32(nm[:], uint32(i))
		out = append(out, item{name: nm, payload: payload, key: name})
	}
	add := func(name string, payload []byte) {
		i, ok := index[name]
		if !ok {
			names = append(names, name)
			i = len(names)
			index[name] = i
		}
		emit(i, name, payload)
	}
	for _, key := range edited.Keys() {
		vals, _ := edited.Get(key)
		if len(vals) == 0 {
			continue // present-but-empty: nothing to store
		}
		var payload []byte
		for _, v := range vals {
			payload = append(payload, renderData(typeUTF8, []byte(v))...)
		}
		if i, ok := byKey[key]; ok {
			emit(i, names[i-1], payload)
			continue
		}
		add(mapping.MP4KeyMdta(key), payload)
	}
	// Cover art keeps the "covr" name it has in an iTunes ilst, reached through a keys entry.
	for _, c := range covr {
		add(mdtaCoverKey, c.payload)
	}
	return dedupePreservedItems(out, preserved), names
}

// buildIlstItems renders the ilst items for whichever keying the file's meta box uses,
// returning the mdta keys index alongside them (nil for an iTunes store). It is the one
// place the two encoders are chosen between, so no caller can build four-character items
// for an mdta box.
func buildIlstItems(d *doc, edited tag.TagSet, covr []item, numericGenre bool) ([]item, []string) {
	if mdtaStore(d) {
		return buildMdtaItems(edited, covr, preservedItems(d.items), d.keyNames)
	}
	return buildItems(edited, covr, preservedItems(d.items), numericGenre), nil
}

// mdtaCoverKey is the keys name cover art is stored under in an mdta store.
const mdtaCoverKey = "covr"

// keysRep returns the udta-payload-relative replacement for the "keys" box when the index
// gained entries, and whether one is needed. A file whose keys index is unchanged (every
// canonical key already had an entry) takes the ordinary in-place ilst path.
func keysRep(d *doc, newKeys []string, ups int64) (byteRep, bool) {
	if d.keys == nil || len(newKeys) == len(d.keyNames) {
		return byteRep{}, false
	}
	return byteRep{
		start:  d.keys.offset - ups,
		oldLen: d.keys.size,
		repl:   renderAtom(atomName("keys"), renderKeys(newKeys)),
	}, true
}

// udtaCanHoldAll reports whether every canonical value in tags fits the udta-level text
// atoms: a single value per key, within the entry's 16-bit size field, and a four-character
// atom to hold it. Pictures never fit (cover art needs an ilst covr), so the caller checks
// those separately. One value that does not fit moves the whole edit to the ilst rather than
// being silently truncated by renderQTText.
func udtaCanHoldAll(texts []udtaText, tags tag.TagSet) bool {
	held := map[tag.Key]bool{}
	for _, u := range texts {
		held[u.key] = true
	}
	for _, k := range tags.Keys() {
		vals, _ := tags.Get(k)
		if len(vals) == 0 {
			continue
		}
		if len(vals) > 1 || len(vals[0]) > qtTextMax {
			return false
		}
		if held[k] {
			continue
		}
		if _, ok := mapping.MP4KeyText(k); !ok {
			return false
		}
	}
	return true
}

// planUdtaTexts renders the udta-level text atoms for an edit: an atom whose canonical value
// the edit changed is rewritten, one whose key the edit no longer carries is deleted, and one
// already holding the canonical value is left alone so an unrelated edit does not rewrite the
// whole user-data box. When create is true (the udta store is the file's write target) a
// canonical key with no atom yet gets a fresh one under its four-character name.
//
// Rewriting an atom keeps its other-language entries verbatim and replaces only the entry
// canonicalEntry selects, so a multi-language "\xa9nam" keeps its translations while its
// canonical value tracks the edit. An atom the file stored in the ilst-style "data" shape is
// re-rendered in the classic entry-sequence shape, which is what QuickTime and ffmpeg write
// and what this reader prefers; an unchanged one keeps its original bytes.
//
// It returns the replacements (udta-payload-relative), the atoms to append, and the udta
// text set the written file will hold, so the post-write result matches a fresh parse.
func planUdtaTexts(d *doc, tags tag.TagSet, create bool, ups int64) ([]byteRep, []byte, []udtaText) {
	var reps []byteRep
	var appends []byte
	var out []udtaText
	held := map[tag.Key]bool{}
	for _, u := range d.udtaTexts {
		held[u.key] = true
		vals, ok := tags.Get(u.key)
		if !ok || len(vals) == 0 {
			// Drop the atom only when the canonical entry is all it holds. Blanking that one
			// entry instead keeps the other-language entries this store is meant to preserve,
			// and reads back with the key absent either way.
			if len(u.entries) > 1 {
				entries := slices.Clone(u.entries)
				i := canonicalEntry(entries)
				if entries[i].text == "" {
					continue
				}
				entries[i].text = ""
				reps = append(reps, byteRep{start: u.ref.offset - ups, oldLen: u.ref.size,
					repl: renderAtom(u.name, renderQTText(entries))})
				out = append(out, udtaText{name: u.name, key: u.key, entries: entries})
				continue
			}
			reps = append(reps, byteRep{start: u.ref.offset - ups, oldLen: u.ref.size})
			continue
		}
		entries := slices.Clone(u.entries)
		if len(entries) == 0 {
			entries = []qtTextEntry{{lang: langUnd}}
		}
		i := canonicalEntry(entries)
		if entries[i].text == vals[0] {
			continue // already the canonical value: leave the atom's bytes alone
		}
		entries[i].text = vals[0]
		reps = append(reps, byteRep{start: u.ref.offset - ups, oldLen: u.ref.size,
			repl: renderAtom(u.name, renderQTText(entries))})
		out = append(out, udtaText{name: u.name, key: u.key, entries: entries})
	}
	if !create {
		return reps, nil, out
	}
	for _, k := range tags.Keys() {
		vals, _ := tags.Get(k)
		if len(vals) == 0 || held[k] {
			continue
		}
		name, ok := mapping.MP4KeyText(k)
		if !ok {
			continue
		}
		entries := []qtTextEntry{{lang: langUnd, text: vals[0]}}
		appends = append(appends, renderAtom(atomName(name), renderQTText(entries))...)
		out = append(out, udtaText{name: atomName(name), key: k, entries: entries})
	}
	return reps, appends, out
}

// qtMetaWrite is the resolved QuickTime-store decision for one edit.
type qtMetaWrite struct {
	// udtaOnly means the canonical values go into the udta-level text atoms and no ilst is
	// written or created.
	udtaOnly bool
	// keys is the mdta keys index the write emits (nil for an iTunes store).
	keys []string
	// reps and appends are the udta-payload-relative edits outside the ilst region: the
	// rewritten keys box and the synced udta text atoms. metaDelta is the part of that byte
	// change that lands inside the meta box, which meta's own size field must absorb.
	reps      []byteRep
	appends   []byte
	metaDelta int64
	// texts is the udta text set the written file holds, for the post-write result.
	// textsChanged records that some udta atom was rewritten OR deleted; texts alone misses a
	// delete-only sync, which still changes bytes and must be reported.
	texts        []udtaText
	textsChanged bool
}

// needsUdtaRegion reports whether this write touches udta bytes outside the ilst region, so
// it must go through the whole-udta rebuild rather than the in-place ilst layout.
func (w qtMetaWrite) needsUdtaRegion() bool {
	return w.udtaOnly || len(w.reps) > 0 || len(w.appends) > 0
}

// planQTMeta resolves which store an edit writes to and what has to change outside the ilst.
// newKeys is the keys index buildMdtaItems produced (nil for an iTunes store).
//
// allowUdtaOnly is false on the chapter paths: those already rewrite the whole udta with an
// ilst in it, so letting them also elect the udta-only store would mean two write targets
// for one edit.
func planQTMeta(d *doc, edited *core.Media, newKeys []string, allowUdtaOnly bool) (qtMetaWrite, error) {
	w := qtMetaWrite{keys: newKeys}
	if d.udta == nil {
		return w, nil
	}
	ups := d.udta.offset + d.udta.headerLen
	w.udtaOnly = allowUdtaOnly && d.ilst == nil && len(d.udtaTexts) > 0 &&
		len(edited.Pictures) == 0 && udtaCanHoldAll(d.udtaTexts, edited.Tags)
	// The keys index only matters when the ilst store is the write target: on the udta-only
	// path no ilst is rendered, so nothing would reference a rewritten index and meta's size
	// field is never patched to absorb its growth.
	if !w.udtaOnly {
		if rep, ok := keysRep(d, newKeys, ups); ok {
			w.reps = append(w.reps, rep)
			w.metaDelta = int64(len(rep.repl)) - rep.oldLen
		}
	}
	if len(d.udtaTexts) > 0 {
		reps, appends, texts := planUdtaTexts(d, edited.Tags, w.udtaOnly, ups)
		w.reps = append(w.reps, reps...)
		w.appends = appends
		w.texts = texts
		w.textsChanged = len(reps) > 0 || len(appends) > 0
	}
	// Splicing needs the captured udta payload. Without it a keys rewrite would leave items
	// naming a table entry no box holds, and a skipped text sync would leave the two stores
	// disagreeing - the split brain the store rule exists to prevent. Fail as a chapter
	// rewrite does on the same missing bytes.
	if d.udtaRaw == nil && w.needsUdtaRegion() {
		return qtMetaWrite{}, fmt.Errorf("%w: MP4 udta bytes were not captured, so the QuickTime metadata stores cannot be rewritten",
			waxerr.ErrInvalidData)
	}
	return w, nil
}

// planQTMetaWrite computes the rewrite when a tag edit has to touch udta bytes outside the
// ilst region: an mdta keys index that gained an entry, or udta-level text atoms being kept
// in sync with the canonical value. It rewrites the whole moov.udta as one contiguous
// region, the way the chpl-only chapter path does, so every change folds into a single delta
// the existing chunk-offset machinery consumes unchanged.
func planQTMetaWrite(d *doc, base, edited *core.Media, newItems []item, qw qtMetaWrite, encodingRewrite bool, opts core.WriteOptions, report core.WriteReport) (*core.WritePlan, error) {
	w := udtaWrite{reps: qw.reps, appends: qw.appends, metaDelta: qw.metaDelta}
	if !qw.udtaOnly {
		var payload []byte
		for _, it := range newItems {
			payload = append(payload, itemBytes(it)...)
		}
		if err := checkBoxSize32(atomName("ilst"), 8+int64(len(payload))); err != nil {
			return nil, err
		}
		w.ilst = renderAtom(atomName("ilst"), payload)
		w.needIlst = true
	}
	reg, err := buildUdtaRegion(d, w, opts)
	if err != nil {
		return nil, err
	}

	delta := int64(len(reg.regionBytes)) - (reg.regionEnd - reg.regionStart)
	total := d.size + delta
	if err := checkSizes(reg.ancestors, delta); err != nil {
		return nil, err
	}
	if err := checkBoxSize32(atomName("udta"), 8+int64(len(reg.udtaPayload))); err != nil {
		return nil, err
	}

	edits := []edit{{off: reg.regionStart, oldLen: reg.regionEnd - reg.regionStart, lit: reg.regionBytes}}
	if delta != 0 {
		for _, anc := range reg.ancestors {
			edits = append(edits, sizePatch(anc, delta))
		}
		es, err := patchTables(delta, reg.regionStart, nil, d.offTables, d.auxTables)
		if err != nil {
			return nil, err
		}
		edits = append(edits, es...)
	}
	segs, err := assemble(edits, d.size)
	if err != nil {
		return nil, err
	}

	report.BytesAfter = total
	report.PaddingAfter = reg.freeContent
	report.Warnings = paddingClampWarning(report.Warnings, reg.paddingClamped)
	report.Operations = qtMetaOps(d, qw, delta, len(edited.Pictures))
	if encodingRewrite {
		report.Operations = append(report.Operations, core.EncodingRewriteOp("genre"))
	}

	// The ilst is untouched on the udta-only path, so the result keeps the parsed items.
	resultItems := newItems
	if qw.udtaOnly {
		resultItems = d.items
	}
	result := buildQTMetaResult(edited, d, resultItems, qw, reg, delta, total)
	// Collapse to a true no-op when the rebuild re-projected to base's values, the same
	// guard the in-place ilst path applies. A grown region (delta != 0) is a real
	// structural change and blocks the collapse.
	if np := core.DowngradeNoOp(core.FormatMP4, edited.Identity.Size, base, result,
		base.Tags.Equal(result.Tags), delta != 0 || encodingRewrite, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// qtMetaOps names what the udta rebuild did, in the same voice as the ilst and chapter paths.
func qtMetaOps(d *doc, qw qtMetaWrite, delta int64, pics int) []string {
	var ops []string
	if qw.udtaOnly {
		ops = append(ops, "moov.udta text atom rewrite")
	} else {
		ops = append(ops, "ilst rewrite")
		if qw.textsChanged {
			ops = append(ops, "moov.udta text atom rewrite")
		}
	}
	if mdtaStore(d) && len(qw.keys) != len(d.keyNames) {
		ops = append(ops, "moov.udta.meta.keys rewrite")
	}
	if delta != 0 {
		ops = append(ops, fmt.Sprintf("%d offset table shift(s)", len(d.offTables)+len(d.auxTables)))
	}
	if pics > 0 {
		ops = append(ops, fmt.Sprintf("pictures: %d", pics))
	}
	return ops
}

// buildQTMetaResult constructs the post-write Media for a udta-region tag rewrite, the
// analogue of buildChapterResult for an edit that changes no chapters: the chapter model is
// carried forward verbatim, and the atom refs come from re-walking the rendered udta.
func buildQTMetaResult(edited *core.Media, src *doc, items []item, qw qtMetaWrite, reg udtaRegion, delta, total int64) *core.Media {
	nd := &doc{
		size:            total,
		cfg:             src.cfg,
		track:           src.track,
		majorBrand:      src.majorBrand,
		items:           items,
		chapters:        core.CloneChapters(src.chapters),
		chplVersion:     src.chplVersion,
		chplCount:       src.chplCount,
		hasQTChapters:   src.hasQTChapters,
		chapterConflict: src.chapterConflict,
		udtaRaw:         reg.udtaPayload,
	}
	// metaHandler, keyNames, udtaTexts and every udta atom ref come from applyUdtaRefs
	// re-reading the bytes just rendered, so they match a fresh parse rather than the
	// write's own intent.
	shiftStructure(nd, src, reg.regionStart, reg.regionEnd, delta)
	carryChapterRefs(nd, src, reg.regionEnd, delta)
	applyUdtaRefs(nd, reg)

	tags, pics, families, numericGenre := project(nd)
	out := &core.Media{
		Format:     core.FormatMP4,
		Properties: edited.Properties.Clone(),
		Tags:       tags,
		Pictures:   pics,
		Chapters:   nd.chapters,
		Families:   families,
		Warnings:   chapterWarnings(mediaWarnings(tags, numericGenre), src.chapterConflict),
		Native:     nd,
		Identity:   core.Identity{Size: total},
	}
	setEssence(nd, out)
	return out
}
