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
// directly under moov.udta.
//
// The store is chosen once per file, never per key, so no key can live in two stores
// with different values:
//
//   - A file with an ilst writes there: moov.udta.meta's, else the one under moov.meta.
//     Every udta-level text atom, and a second ilst under moov.meta, is then rewritten to
//     the canonical value of each key it holds, or loses the key when that is gone.
//   - A file whose only store is udta-level text atoms writes there while udta can hold
//     the whole edit (see udtaCanHoldAll). One key it cannot represent moves the whole
//     edit to the ilst.
//   - A file with no store at all keeps the mdirappl creation path.

// mdtaStore reports whether the file's meta box keys its ilst items through a "keys"
// index.
func mdtaStore(d *doc) bool {
	return d.metaHandler == mdtaHandler && d.keys != nil && len(d.keyNames) > 0
}

// buildMdtaItems is buildItems for an mdta store: every canonical value becomes a keys
// entry plus an ilst item named by that entry's 1-based index. A track or disc number and its
// total share one.
func buildMdtaItems(edited tag.TagSet, covr coverWrite, preserved []item, baseKeys []string, ownTotal map[tag.Key]bool) ([]item, []string) {
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
	text := func(vals []string) []byte {
		var payload []byte
		for _, v := range vals {
			payload = append(payload, renderData(typeUTF8, []byte(v))...)
		}
		return payload
	}
	consumed := map[tag.Key]bool{}
	for _, key := range edited.Keys() {
		vals, _ := edited.Get(key)
		if len(vals) == 0 || consumed[key] {
			continue // nothing to store, or stored with its pair
		}
		// A total the file reads from an entry of its own stays there.
		if numKey, ok := pairNumberKey(key); ok && !ownTotal[numKey] {
			totKey := tag.TotalKey(numKey)
			if pair, ok := mdtaPair(edited, numKey, totKey); ok {
				consumed[numKey], consumed[totKey] = true, true
				key, vals = numKey, []string{pair}
			}
		}
		if i, ok := byKey[key]; ok {
			emit(i, names[i-1], text(vals))
			continue
		}
		add(mapping.MP4KeyMdta(key), text(vals))
	}
	// Kept cover items stay as they are, an index-keyed one under its own entry.
	for _, c := range covr.items {
		if covr.fresh {
			add(mdtaArtworkKey, c.payload)
			continue
		}
		out = append(out, c)
	}
	return keepUnnamed(dedupePreservedItems(out, preserved), len(baseKeys), len(names)), names
}

// keepUnnamed renumbers each item whose index lay past the keys table read, naming no entry, and
// would name one of the entries a write appended: it moves past the table's new end.
func keepUnnamed(items []item, had, have int) []item {
	used := map[[4]byte]bool{}
	for _, it := range items {
		used[it.name] = true
	}
	next := uint32(have)
	for i, it := range items {
		if idx := mdtaIndex(it.name); it.mdta() || idx <= uint32(had) || idx > uint32(have) {
			continue
		}
		for {
			next++
			binary.BigEndian.PutUint32(it.name[:], next)
			if !used[it.name] {
				break
			}
		}
		used[it.name], items[i] = true, it
	}
	return items
}

// ownTotals names the number keys whose total is read from a keys entry of the total's own, not
// from the number's entry, where a lone total is "/12".
func ownTotals(rd reading) map[tag.Key]bool {
	own := map[tag.Key]bool{}
	for i, it := range rd.items {
		if !it.mdta() {
			continue
		}
		if k, _ := mapping.MP4MdtaKey(it.key); k == tag.TrackTotal || k == tag.DiscTotal {
			num, _ := pairNumberKey(k)
			own[num] = own[num] || rd.read(i, k)
		}
	}
	return own
}

// pairNumberKey returns the number key of the track or disc pair key belongs to.
func pairNumberKey(key tag.Key) (tag.Key, bool) {
	switch key {
	case tag.TrackNumber, tag.TrackTotal:
		return tag.TrackNumber, true
	case tag.DiscNumber, tag.DiscTotal:
		return tag.DiscNumber, true
	}
	return "", false
}

// mdtaPair returns the "n/total" text one mdta entry holds for a number and its total, as ffmpeg
// writes it, when a total is set and the text reads back as the two values.
func mdtaPair(ts tag.TagSet, numKey, totKey tag.Key) (string, bool) {
	nums, _ := ts.Get(numKey)
	tots, _ := ts.Get(totKey)
	if len(nums) > 1 || len(tots) != 1 || tots[0] == "" {
		return "", false
	}
	num, _ := ts.First(numKey)
	joined := num + "/" + tots[0]
	if n, t, split := tag.NumberTotalSplit(numKey, joined); split && n == num && t == tots[0] {
		return joined, true
	}
	return "", false
}

// buildIlstItems renders the ilst items for whichever keying the file's meta box uses,
// returning the mdta keys index alongside them (nil for an iTunes store).
func buildIlstItems(d *doc, edited tag.TagSet, covr coverWrite, numericGenre bool, cf conflicts) (items []item, keys []string, respelled int) {
	rd := d.reading()
	edited, preserved := cf.rebuilt(edited), preservedItems(rd, cf.frozen)
	if mdtaStore(d) {
		items, keys = buildMdtaItems(edited, covr, preserved, d.keyNames, ownTotals(rd))
	} else {
		items = buildItems(edited, covr.items, preserved, numericGenre)
	}
	items = reuseParsedItems(items, rd)
	return items, keys, cf.respelled(rd, items)
}

// respelledOps names, for the report, the items a rebuild moved to the atom of their key.
func respelledOps(n int) []string {
	if n == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d ilst item%s respelled", n, pluralS(n))}
}

// mdtaArtworkKey is the keys name new cover art takes in an mdta store: Apple's, which ffmpeg
// reads as a picture.
const mdtaArtworkKey = "com.apple.quicktime.artwork"

// isMdtaCoverKey reports whether a keys name holds cover art: Apple's, or the "covr" WaxLabel
// wrote before it.
func isMdtaCoverKey(name string) bool {
	return name == mdtaArtworkKey || name == "covr"
}

// keysRep returns the udta-payload-relative replacement for the "keys" box when the index
// gained entries, and whether one is needed. A file whose keys index is unchanged (every
// canonical key already had an entry) takes the ordinary in-place ilst path.
func keysRep(d *doc, newKeys []string, ups int64) (byteRep, bool) {
	if !d.keysGrew(newKeys) || d.metaAtMoov {
		return byteRep{}, false
	}
	return byteRep{
		start:  d.keys.offset - ups,
		oldLen: d.keys.size,
		repl:   renderAtom(atomName("keys"), renderKeys(newKeys)),
	}, true
}

// keysGrew reports whether a write's keys index holds entries the file's does not.
func (d *doc) keysGrew(newKeys []string) bool {
	return d.keys != nil && len(newKeys) != len(d.keyNames)
}

// renderIlst renders an ilst of items, refusing one over the 32-bit box size.
func renderIlst(items []item) ([]byte, error) {
	var payload []byte
	for _, it := range items {
		payload = append(payload, itemBytes(it)...)
	}
	if err := checkBoxSize32(atomName("ilst"), 8+int64(len(payload))); err != nil {
		return nil, err
	}
	return renderAtom(atomName("ilst"), payload), nil
}

// metaEdits returns the edits that place a rebuilt ilst, and a grown keys index, in a store
// under moov.meta, which no udta region holds: the ilst with the padding left beside it, the
// keys box and the meta's size.
func metaEdits(d *doc, newIlst []byte, newKeys []string, pol core.PaddingPolicy) (edits []edit, freeContent int64, clamped bool, err error) {
	start, end := d.ilstRegion()
	region, freeContent, clamped := fitIlst(newIlst, end-start, pol)
	edits = []edit{{off: start, oldLen: end - start, lit: region}}
	if d.keysGrew(newKeys) {
		edits = append(edits, edit{off: d.keys.offset, oldLen: d.keys.size, lit: renderAtom(atomName("keys"), renderKeys(newKeys))})
	}
	edits, err = sizedMeta(*d.meta, edits)
	return edits, freeContent, clamped, err
}

// sizedMeta adds the meta's new size to edits inside it.
func sizedMeta(meta atomRef, edits []edit) ([]edit, error) {
	delta := sumDelta(edits)
	if delta == 0 {
		return edits, nil
	}
	if err := checkSizes([]atomRef{meta}, delta); err != nil {
		return nil, err
	}
	return append(edits, sizePatch(meta, delta)), nil
}

// secondWrite is how a write leaves the store under moov.meta that sits beside the one written.
type secondWrite struct {
	edits []edit
	items []item
}

// syncSecond brings the second store in step with a write, as the udta text atoms are: each key
// it holds takes the value written or goes with it, and it gains none. Its cover goes when the
// edit changes the pictures, which the store written then holds.
func syncSecond(s *metaStore, edited *core.Media, picturesChanged bool) (secondWrite, error) {
	rd := readItems(s.items)
	contribs, _, pics, _ := storeContribs(rd)
	held := core.BuildTagSet(contribs)
	tag.NormalizeNumberPairs(&held)
	want := tag.NewTagSet()
	for _, k := range held.Keys() {
		if vals, ok := edited.Tags.Get(k); ok {
			want.Set(k, vals...)
		}
	}
	dropCover := picturesChanged && len(pics) > 0
	if held.Equal(want) && !dropCover {
		return secondWrite{items: s.items}, nil
	}
	// A value the store holds unread stays with its key while the sync leaves that key alone.
	cf := resolveConflicts(rd, core.DiffKeys(held, want))
	covr := coverWrite{fresh: dropCover}
	if !dropCover {
		covr.items = covrItems(s.items)
	}
	want, preserved := cf.rebuilt(want), preservedItems(rd, cf.frozen)
	var items []item
	keys := s.keyNames
	if s.handler == mdtaHandler && s.keys != nil && len(s.keyNames) > 0 {
		items, keys = buildMdtaItems(want, covr, preserved, s.keyNames, ownTotals(rd))
	} else {
		items = buildItems(want, covr.items, preserved, numericGenreEncoding(s.items, want, false))
	}
	items = reuseParsedItems(items, rd)
	ilst, err := renderIlst(items)
	if err != nil {
		return secondWrite{}, err
	}
	edits := []edit{{off: s.ilst.offset, oldLen: s.ilst.size, lit: ilst}}
	if len(keys) != len(s.keyNames) {
		edits = append(edits, edit{off: s.keys.offset, oldLen: s.keys.size, lit: renderAtom(atomName("keys"), renderKeys(keys))})
	}
	edits, err = sizedMeta(*s.meta, edits)
	return secondWrite{edits: edits, items: items}, err
}

// udtaCanHoldAll reports whether every canonical value in tags fits the udta-level text
// atoms: a single value per key, within the entry's 16-bit size field, and a
// four-character atom to hold it. Pictures never fit (cover art needs an ilst covr), so
// the caller checks those separately.
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

// planUdtaTexts renders the udta-level text atoms for an edit: an atom whose canonical
// value the edit changed is rewritten, one whose key the edit no longer carries is
// deleted, and one already holding the canonical value is left alone so an unrelated
// edit does not rewrite the whole user-data box.
func planUdtaTexts(d *doc, tags tag.TagSet, create bool, ups int64) (reps []byteRep, appends []byte, dups []core.DuplicateContent, reduced []tag.Key) {
	held := map[tag.Key]bool{}
	for _, u := range d.udtaTexts {
		held[u.key] = true
		vals, _ := tags.Get(u.key)
		fits := len(fitting(vals)) == len(vals)
		repl, changed := udtaTextEdit(u, fitting(vals))
		if !changed {
			continue
		}
		if !fits {
			reduced = append(reduced, u.key)
		}
		reps = append(reps, byteRep{start: u.ref.offset - ups, oldLen: u.ref.size, repl: repl})
		// The atom's duplicates go with it, or a re-read would promote one in its place.
		for _, dup := range u.dups {
			reps = append(reps, byteRep{start: dup.ref.offset - ups, oldLen: dup.ref.size})
		}
		if len(u.dups) > 0 {
			dups = append(dups, u.dupContent())
		}
	}
	if !create {
		return reps, nil, dups, reduced
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
	}
	return reps, appends, dups, reduced
}

// fitting returns the values a udta text atom takes for its key: vals, or none when the atom
// cannot hold them (several, or one over its 16-bit size). Such a value is the ilst's alone: an
// atom holding it cut short, or the first of several, would read as a disagreement.
func fitting(vals []string) []string {
	if len(vals) > 1 || len(vals) == 1 && len(vals[0]) > qtTextMax {
		return nil
	}
	return vals
}

// reducedTextWarnings reports the udta text atoms that lost a value that outgrew them: the atom
// goes, or its entry is cleared where other languages share it.
func reducedTextWarnings(ws []core.Warning, keys []tag.Key) []core.Warning {
	for _, k := range keys {
		ws = core.WarnKeyed(ws, core.WarnNativeValueReduced, fmt.Sprintf(
			"%s does not fit a moov.udta text atom (one value of at most %d bytes); the ilst alone holds it", k, qtTextMax), k)
	}
	return ws
}

// udtaTextEdit returns the atom's bytes once its key holds vals, nil to delete it; changed is
// false when the atom already reads that way.
func udtaTextEdit(u udtaText, vals []string) (repl []byte, changed bool) {
	entries := slices.Clone(u.entries)
	if len(vals) == 0 {
		// Drop the atom only when the canonical entry is all it holds. Otherwise blank that
		// entry: the other-language entries stay, and the key reads back absent either way.
		if len(entries) <= 1 {
			return nil, true
		}
		i := canonicalEntry(entries)
		if entries[i].text == "" {
			return nil, false
		}
		entries[i].text = ""
		return renderAtom(u.name, renderQTText(entries)), true
	}
	if len(entries) == 0 {
		entries = []qtTextEntry{{lang: langUnd}}
	}
	i := canonicalEntry(entries)
	if entries[i].text == vals[0] {
		return nil, false
	}
	entries[i].text = vals[0]
	return renderAtom(u.name, renderQTText(entries)), true
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
	// change inside the meta box, which meta's own size field must absorb.
	reps      []byteRep
	appends   []byte
	metaDelta int64
	// textsChanged records that some udta text atom was rewritten or deleted.
	textsChanged bool
	// textDups is what the duplicate text atoms removed with theirs hold.
	textDups []core.DuplicateContent
	// reducedTexts names the keys whose text atom went because it cannot hold the value.
	reducedTexts []tag.Key
	// moovKeys records a grown keys index in a store under moov.meta, placed with its ilst.
	moovKeys bool
	// second is how the write leaves the store under moov.meta beside the one written.
	second secondWrite
}

// needsRegions reports whether this write touches bytes outside the ilst region, so it cannot
// take the in-place ilst layout.
func (w qtMetaWrite) needsRegions() bool {
	return w.udtaOnly || len(w.reps) > 0 || len(w.appends) > 0 || w.moovKeys || len(w.second.edits) > 0
}

// planQTMeta resolves which store an edit writes to and what has to change outside the
// ilst.
func planQTMeta(d *doc, edited *core.Media, newKeys []string, allowUdtaOnly, picturesChanged bool) (qtMetaWrite, error) {
	w := qtMetaWrite{keys: newKeys, moovKeys: d.metaAtMoov && d.keysGrew(newKeys), second: secondWrite{items: d.secondItems()}}
	if d.second != nil {
		var err error
		if w.second, err = syncSecond(d.second, edited, picturesChanged); err != nil {
			return qtMetaWrite{}, err
		}
	}
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
		reps, appends, dups, reduced := planUdtaTexts(d, edited.Tags, w.udtaOnly, ups)
		w.reps = append(w.reps, reps...)
		w.appends, w.textDups, w.reducedTexts = appends, dups, reduced
		w.textsChanged = len(reps) > 0 || len(appends) > 0
	}
	// Splicing needs the captured udta payload.
	if d.udtaRaw == nil && (w.udtaOnly || len(w.reps) > 0 || len(w.appends) > 0) {
		return qtMetaWrite{}, fmt.Errorf("%w: MP4 udta bytes were not captured, so the QuickTime metadata stores cannot be rewritten",
			waxerr.ErrInvalidData)
	}
	return w, nil
}

// planQTMetaWrite computes the rewrite when a tag edit has to touch bytes outside the ilst
// region: an mdta keys index that gained an entry, or udta-level text atoms and a second store
// being kept in sync with the canonical value.
func planQTMetaWrite(d *doc, base, edited *core.Media, newItems []item, qw qtMetaWrite, extraOps []string, structural bool, opts core.WriteOptions, report core.WriteReport) (*core.WritePlan, error) {
	w := udtaWrite{reps: qw.reps, appends: qw.appends, metaDelta: qw.metaDelta}
	edits := slices.Clone(qw.second.edits)
	free, clamped := d.PaddingBytes(), false
	if !qw.udtaOnly {
		ilst, err := renderIlst(newItems)
		if err != nil {
			return nil, err
		}
		if d.metaAtMoov {
			es, fc, cl, err := metaEdits(d, ilst, qw.keys, opts.Padding)
			if err != nil {
				return nil, err
			}
			edits, free, clamped = append(edits, es...), fc, cl
		} else {
			w.ilst, w.needIlst = ilst, true
		}
	}
	texts := d.udtaTexts
	if w.needIlst || len(w.reps) > 0 || len(w.appends) > 0 {
		reg, err := buildUdtaRegion(d, w, opts)
		if err != nil {
			return nil, err
		}
		if err := checkBoxSize32(atomName("udta"), 8+int64(len(reg.udtaPayload))); err != nil {
			return nil, err
		}
		edits, texts = append(edits, reg.edit()), udtaTextsIn(reg)
		if w.needIlst {
			free, clamped = reg.freeContent, reg.paddingClamped
		}
	}
	first := firstOffset(edits)
	edits, delta, err := closeMoov(d, edits)
	if err != nil {
		return nil, err
	}
	segs, err := assemble(edits, d.size)
	if err != nil {
		return nil, err
	}

	report.BytesAfter = d.size + delta
	report.PaddingAfter = free
	report.Warnings = reducedTextWarnings(paddingClampWarning(report.Warnings, clamped), qw.reducedTexts)
	report.Operations = append(qtMetaOps(d, qw, delta, first, len(edited.Pictures)), extraOps...)

	// The ilst is untouched on the udta-only path, so the result keeps the parsed items.
	resultItems := newItems
	if qw.udtaOnly {
		resultItems = d.items
	}
	result := tagResult(edited, d, stores{items: resultItems, second: qw.second.items, texts: texts},
		newShifts(edits), core.CloneChapters(d.chapters))
	report.Warnings = droppedDupWarnings(report.Warnings, d, base, result, qw.textDups, false)
	// Collapse to a no-op when the rebuild re-projected to base's values, as the in-place
	// ilst path does.
	if np := core.DowngradeNoOp(core.FormatMP4, edited.Identity.Size, base, result,
		base.Tags.Equal(result.Tags), structural || opts.PaddingExplicit && delta != 0, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: result}, nil
}

// qtMetaOps names what the rebuild did, in the same voice as the ilst and chapter paths.
func qtMetaOps(d *doc, qw qtMetaWrite, delta, insertion int64, pics int) []string {
	var ops []string
	if qw.udtaOnly {
		ops = append(ops, "moov.udta text atom rewrite")
	} else {
		ops = append(ops, "ilst rewrite")
		if qw.textsChanged {
			ops = append(ops, "moov.udta text atom rewrite")
		}
	}
	if mdtaStore(d) && d.keysGrew(qw.keys) {
		ops = append(ops, d.storePath()+".keys rewrite")
	}
	ops = append(ops, qw.second.ops()...)
	ops = appendShiftOp(ops, d, delta, insertion, nil)
	if pics > 0 {
		ops = append(ops, fmt.Sprintf("pictures: %d", pics))
	}
	return ops
}

// ops names the second store's rewrite, if the write makes one.
func (w secondWrite) ops() []string {
	if len(w.edits) == 0 {
		return nil
	}
	return []string{"moov.meta ilst sync"}
}
