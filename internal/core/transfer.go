package core

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

// TransferKind names transferred metadata category (field, picture, chapter, synced lyrics).
type TransferKind uint8

const (
	TransferField TransferKind = iota
	TransferPicture
	TransferChapter
	TransferSyncedLyric
)

func (k TransferKind) String() string {
	switch k {
	case TransferPicture:
		return "picture"
	case TransferChapter:
		return "chapter"
	case TransferSyncedLyric:
		return "synced lyrics"
	default:
		return "field"
	}
}

// Disposition grades cross-format transfer outcome (carried, lossy, dropped, excluded).
type Disposition uint8

const (
	// Carried: the destination holds it as the source gives it.
	Carried Disposition = iota
	// Lossy: stored with reduced fidelity.
	Lossy
	// Dropped: destination cannot store it.
	Dropped
	// Excluded: policy skip; not a capability loss.
	Excluded
)

func (d Disposition) String() string {
	switch d {
	case Carried:
		return "carried"
	case Lossy:
		return "lossy"
	case Excluded:
		return "excluded"
	default:
		return "dropped"
	}
}

// TransferItem is one metadata item's transfer fate. Count is set size for picture/chapter/lyrics.
type TransferItem struct {
	Kind        TransferKind
	Key         tag.Key
	Count       int
	Disposition Disposition
	Reason      string
}

// TransferReport grades each item of a source's metadata as a destination stores it. Set items
// may split carried/lossy/dropped. A report prepared onto a file is held to what the plan
// writes ([ReconcileTransfer]); one simulated for a format is graded from its capabilities.
type TransferReport struct {
	Source Format
	Dest   Format
	Items  []TransferItem
}

// Counts tallies by disposition. Set items sum Count; field items count as one each.
func (r TransferReport) Counts() (carried, lossy, dropped int) {
	for _, it := range r.Items {
		n := 1
		if it.Kind == TransferPicture || it.Kind == TransferChapter || it.Kind == TransferSyncedLyric {
			n = it.Count
		}
		switch it.Disposition {
		case Carried:
			carried += n
		case Lossy:
			lossy += n
		case Dropped:
			dropped += n
		}
	}
	return carried, lossy, dropped
}

// Lossless reports whether no item is lossy or dropped.
func (r TransferReport) Lossless() bool {
	_, lossy, dropped := r.Counts()
	return lossy == 0 && dropped == 0
}

// HasDropped reports any Dropped item (distinct from merely lossy).
func (r TransferReport) HasDropped() bool {
	_, _, dropped := r.Counts()
	return dropped > 0
}

// NoChangesLine heads a transfer whose plan writes nothing: the items the destination could not
// store, or nothing left to bring. A transfer is no edit to discard, so this stands in for the
// line such a plan gives an edit ([NoChangesLine]).
func (r TransferReport) NoChangesLine() string {
	if _, _, dropped := r.Counts(); dropped > 0 {
		return fmt.Sprintf("no changes written; %d item(s) could not be stored", dropped)
	}
	return NoChangesLine(false)
}

// TransferValuesEqual reports whether a file of format fa holding a under k and one of format fb
// holding b hold the same thing: the same strings; where either side is MP4 and stores k as a
// number, the same numbers ("03" is 3); or, for a boolean key, words every writer stores as the
// same "1" or "0" ("yes" is "1"). The transfer report and diff both judge by it.
func TransferValuesEqual(k tag.Key, a, b []string, fa, fb Format) bool {
	if slices.Equal(a, b) {
		return true
	}
	if tag.IsBooleanKey(k) {
		return slices.EqualFunc(a, b, func(x, y string) bool { return tag.CanonicalBoolValue(x) == tag.CanonicalBoolValue(y) })
	}
	return (fa == FormatMP4 || fb == FormatMP4) && tag.IsMP4CanonicalKey(k) && tag.NumericValuesEqual(k, a, b)
}

// TransferProjection is a transfer resolved against a destination. Items is the report and the
// rest is what the write sets, so the two cannot disagree about what is stored.
type TransferProjection struct {
	Items []TransferItem
	// Keys are the fields to set, in source order: those graded stored that the destination
	// does not already hold as the source gives them.
	Keys []tag.Key
	// Pictures replace the destination's when SetPictures.
	Pictures    []Picture
	SetPictures bool
	// SetChapters: the source's chapters replace the destination's.
	SetChapters bool
	// SyncedLyrics replace the destination's when SetSyncedLyrics.
	SyncedLyrics    []SyncedLyrics
	SetSyncedLyrics bool

	// The grade of each of the source's pictures, chapters and synced lyric sets, in source
	// order: [ReconcileTransfer] holds each to its own fate in the result.
	pictures, chapters, lyrics []Disposition
	// sets holds the items of those three while the fields ahead of them are graded.
	sets []TransferItem
}

// ProjectTransferOnto grades each metadata item of src against caps, the capabilities of the
// file holding dst (nil for a format alone), and resolves what a write sets. An item dst
// already holds as src gives it is carried and not written again, whatever caps can store, so a
// copy onto the same metadata changes nothing. Text no format stores as given
// ([WritableTextReason]) is graded here, before any writer sees it.
func ProjectTransferOnto(src, dst *Media, caps Capabilities) TransferProjection {
	// The pictures, chapters and synced lyrics first: a format may place its fields by which
	// of them the write brings, so the fields are graded against the source as written.
	var p TransferProjection
	written := *src
	written.Pictures, written.Chapters, written.SyncedLyrics = nil, nil, nil
	if n := len(src.Pictures); n > 0 {
		// Pictures are graded, compared and written as a read settles them ([SniffStored]): a
		// block may declare another type or size than its bytes give, and no format keeps that.
		pics := ProjectPictures(src.Pictures)
		var held []Picture
		if dst != nil {
			held = ProjectPictures(dst.Pictures)
		}
		if dst != nil && EqualPictures(pics, held) {
			p.sets = append(p.sets, TransferItem{Kind: TransferPicture, Count: n, Disposition: Carried})
			p.pictures = make([]Disposition, n)
		} else {
			p.projectPictures(pics, held, dst != nil, caps)
		}
		if p.SetPictures {
			written.Pictures = p.Pictures
		}
	}
	if n := len(src.Chapters); n > 0 {
		if dst != nil && EqualChapters(src.Chapters, dst.Chapters) {
			p.sets = append(p.sets, TransferItem{Kind: TransferChapter, Count: n, Disposition: Carried})
			p.chapters = make([]Disposition, n)
		} else {
			p.projectChapters(src.Chapters, caps)
		}
		if p.SetChapters {
			written.Chapters = src.Chapters
		}
	}
	if n := len(src.SyncedLyrics); n > 0 {
		if dst != nil && EqualSyncedLyrics(src.SyncedLyrics, dst.SyncedLyrics) {
			p.sets = append(p.sets, TransferItem{Kind: TransferSyncedLyric, Count: n, Disposition: Carried})
			p.lyrics = make([]Disposition, n)
		} else {
			p.projectSyncedLyrics(src.SyncedLyrics, caps)
		}
		written.SyncedLyrics = p.SyncedLyrics
	}

	// The fields: the capability table's grade, then the writer's word on those it stores.
	type field struct {
		item   TransferItem
		graded []string
		stored bool // graded stored by the table, so the writer is asked
	}
	keys := src.Tags.Keys()
	fields := make([]field, len(keys))
	var rendered []tag.Key // what a rendering of the write holds: the stored fields and the held ones
	for i, k := range keys {
		vals, _ := src.Tags.Get(k)
		f := &fields[i]
		f.item, f.graded = TransferItem{Kind: TransferField, Key: k, Count: len(vals)}, GradedTransferValues(k, vals)
		switch {
		case k.DescribesOwnAudio():
			// Own-audio keys are excluded; copy stays lossless.
			f.item.Disposition = Excluded
			f.item.Reason = "not transferred; describes this file's own audio, not the work, so the destination keeps whatever it already had for this key"
		case dst != nil && fieldHeld(k, vals, f.graded, src, dst):
			rendered = append(rendered, k)
		default:
			f.item.Disposition, f.item.Reason = tableGrade(k, f.graded, src, caps)
			if f.stored = f.item.Disposition != Dropped; f.stored {
				rendered = append(rendered, k)
			}
		}
	}
	var writers func(tag.Key) (Disposition, string)
	if caps.rendered.Render != nil && slices.ContainsFunc(fields, func(f field) bool { return f.stored }) {
		writers = caps.rendered.Grade(&written, rendered)
	}
	for _, f := range fields {
		if f.stored {
			// The writer's word can only take away from the table's grade: it grades what the
			// table calls carried, and drops what it stores nothing of. A lossy grade is the
			// table's word on the stored form and keeps its reason.
			take := func(d Disposition, reason string) {
				if d == Dropped || f.item.Disposition == Carried {
					f.item.Disposition, f.item.Reason = d, reason
				}
			}
			if caps.fieldClassifier != nil {
				if d, reason, ok := caps.fieldClassifier(f.item.Key, f.graded, &written); ok {
					take(d, reason)
				}
			}
			if writers != nil && f.item.Disposition != Dropped {
				take(writers(f.item.Key))
			}
			if f.item.Disposition != Dropped {
				p.Keys = append(p.Keys, f.item.Key)
			}
		}
		p.Items = append(p.Items, f.item)
	}
	p.Items, p.sets = append(p.Items, p.sets...), nil
	return p
}

// fieldHeld reports whether dst holds k as src gives it: the same values, as the source holds
// them (vals) or as a write would store them (graded), and for a comment or lyric the same
// descriptions and languages.
func fieldHeld(k tag.Key, vals, graded []string, src, dst *Media) bool {
	if held, ok := dst.Tags.Get(k); !ok || !slices.Equal(held, vals) && !slices.Equal(held, graded) {
		return false
	}
	switch k {
	case tag.Comment:
		return slices.Equal(src.Comments, dst.Comments)
	case tag.Lyrics:
		return slices.Equal(src.Lyrics, dst.Lyrics)
	}
	return true
}

// tableGrade grades the values of k by the capability table of caps, before any writer is asked.
func tableGrade(k tag.Key, graded []string, src *Media, caps Capabilities) (Disposition, string) {
	disp, reason := dispose(caps.Field(k), caps.ReadOnly, len(graded), "this field", graded)
	if disp == Dropped {
		return disp, reason
	}
	for _, v := range graded {
		if r := WritableTextReason(v); r != "" {
			return Dropped, "a value " + r
		}
		if caps.maxValueBytes > 0 {
			if r := SizeLimitReason("a value", int64(len(v)), caps.maxValueBytes, caps.valueSizeStore); r != "" {
				return Dropped, r
			}
		}
	}
	// A stored comment or lyric still loses the description or language caps has no field for.
	if disp == Carried {
		var qualified []TextItem
		switch k {
		case tag.Comment:
			qualified = src.Comments
		case tag.Lyrics:
			qualified = src.Lyrics
		}
		if r := TextItemLossReason(qualified, caps.Field(k).TextLoss); r != "" {
			disp, reason = Lossy, r
		}
	}
	return disp, reason
}

// projectPictures grades pics, which a destination holding held (onFile: there is one) does not
// hold as given, and resolves the set written. Both are as a read settles them.
func (p *TransferProjection) projectPictures(pics, held []Picture, onFile bool, caps Capabilities) {
	c := caps.Pictures
	p.pictures = make([]Disposition, len(pics))
	for i := range p.pictures {
		p.pictures[i] = Dropped
	}
	// Split by representable MIME, then by size, then by the pictures a store of uniquely named
	// items holds (APE). The last two are skipped when pictures cannot be written. idx is where
	// each picture left sits in pics.
	rep, idx, unrepMIMEs := PartitionRepresentable(c, pics)
	writable := !caps.ReadOnly && c.Write != AccessNone
	var sizeDropped, slotDropped int
	var sizeReason, slotReason string
	if writable {
		var kept []int
		kept, sizeReason = PartitionPictureSize(c, rep)
		sizeDropped = len(rep) - len(kept)
		rep, idx = pick(rep, kept), pick(idx, kept)
		kept, slotReason = PartitionPictureSlots(c, rep)
		slotDropped = len(rep) - len(kept)
		rep, idx = pick(rep, kept), pick(idx, kept)
	}
	if len(rep) > 0 {
		disp, reason := dispose(c, caps.ReadOnly, len(rep), "pictures", nil)
		// A description no format stores as given is left behind and the picture carried.
		stored := rep
		var undescribed string
		for i, pic := range rep {
			if r := WritableTextReason(pic.Description); r != "" {
				if &stored[0] == &rep[0] {
					stored = slices.Clone(rep)
				}
				stored[i].Description = ""
				undescribed = "a picture description " + r + " and is not stored"
			}
		}
		if disp == Carried {
			// Per-picture loss splits carried vs lossy. One lossy reason: the first found.
			var carried, lossy int
			var lossyReason string
			for i, pic := range rep {
				r := pictureLossReason(pic, c)
				if r == "" && stored[i].Description != pic.Description {
					r = undescribed
				}
				if r == "" {
					carried++
					p.pictures[idx[i]] = Carried
					continue
				}
				lossy++
				p.pictures[idx[i]] = Lossy
				if lossyReason == "" {
					lossyReason = r
				}
			}
			p.sets = appendCarriedLossyItems(p.sets, TransferPicture, carried, lossy, lossyReason)
		} else {
			for _, i := range idx {
				p.pictures[i] = disp
			}
			p.sets = append(p.sets, TransferItem{
				Kind: TransferPicture, Count: len(rep), Disposition: disp, Reason: reason,
			})
		}
		if writable && disp != Dropped {
			p.Pictures = stored
			p.SetPictures = !onFile || !EqualPictures(stored, held)
		}
	}
	if sizeDropped > 0 {
		p.sets = append(p.sets, TransferItem{
			Kind: TransferPicture, Count: sizeDropped, Disposition: Dropped, Reason: sizeReason,
		})
	}
	if slotDropped > 0 {
		p.sets = append(p.sets, TransferItem{
			Kind: TransferPicture, Count: slotDropped, Disposition: Dropped, Reason: slotReason,
		})
	}
	if len(unrepMIMEs) > 0 {
		p.sets = append(p.sets, TransferItem{
			Kind: TransferPicture, Count: len(unrepMIMEs), Disposition: Dropped,
			Reason: UnrepresentableReason(caps.Format, unrepMIMEs),
		})
	}
}

// pick returns the elements of s at idx, s itself when that is all of them.
func pick[T any](s []T, idx []int) []T {
	if len(idx) == len(s) {
		return s
	}
	out := make([]T, len(idx))
	for i, at := range idx {
		out[i] = s[at]
	}
	return out
}

// projectChapters grades chapters, which the destination does not hold as given.
func (p *TransferProjection) projectChapters(chapters []Chapter, caps Capabilities) {
	c := caps.Chapters
	p.chapters = make([]Disposition, len(chapters))
	disp, reason := dispose(c, caps.ReadOnly, len(chapters), "chapters", nil)
	if disp != Dropped {
		// A list is written whole or not at all: leaving one chapter out moves the end of the
		// one before it.
		if r := chaptersUnwritableReason(chapters); r != "" {
			disp, reason = Dropped, r
		}
	}
	p.SetChapters = disp != Dropped
	if disp != Carried {
		for i := range p.chapters {
			p.chapters[i] = disp
		}
		p.sets = append(p.sets, TransferItem{Kind: TransferChapter, Count: len(chapters), Disposition: disp, Reason: reason})
		return
	}
	// Per-chapter loss splits carried vs lossy. One lossy reason: lost metadata, then a time
	// clamped, then a time between the format's steps, then a title cut short.
	var carried, lossy int
	var lostMetadata, clamped, cutTime bool
	for i, ch := range chapters {
		metaLoss := ChapterLosesMetadata(chapters, i, c.ChapterLoss)
		clampLoss := c.ChapterTimeMax > 0 && max(ch.Start, ch.End) > c.ChapterTimeMax
		timeLoss := c.ChapterTimeUnit > 0 && (ch.Start%c.ChapterTimeUnit != 0 || ch.End%c.ChapterTimeUnit != 0)
		titleLoss := c.ChapterTitleByteMax > 0 && len(ch.Title) > c.ChapterTitleByteMax
		if !metaLoss && !clampLoss && !timeLoss && !titleLoss {
			carried++
			continue
		}
		lossy++
		p.chapters[i] = Lossy
		lostMetadata = lostMetadata || metaLoss
		clamped = clamped || clampLoss
		cutTime = cutTime || timeLoss
	}
	lossyReason := "chapter title is too long and was truncated"
	switch {
	case lostMetadata:
		lossyReason = c.Reason()
	case clamped:
		lossyReason = "a chapter time is past the largest the format stores and is clamped"
	case cutTime:
		lossyReason = fmt.Sprintf("a chapter time finer than the %s the format counts in is not stored exactly", c.ChapterTimeUnit)
	}
	p.sets = appendCarriedLossyItems(p.sets, TransferChapter, carried, lossy, lossyReason)
}

// chaptersUnwritableReason names the first text of chapters no format stores as given, "" when
// every title and language can be written.
func chaptersUnwritableReason(chapters []Chapter) string {
	for _, ch := range chapters {
		for _, f := range []struct{ what, text string }{
			{"title", ch.Title}, {"language", ch.Language}, {"IETF language", ch.LanguageIETF},
		} {
			if r := WritableTextReason(f.text); r != "" {
				return "a chapter " + f.what + " " + r
			}
		}
	}
	return ""
}

// syncedLyricsUnwritableReason is [chaptersUnwritableReason] for one synced lyrics set.
func syncedLyricsUnwritableReason(sl SyncedLyrics) string {
	for _, f := range []struct{ what, text string }{{"language", sl.Language}, {"description", sl.Description}} {
		if r := WritableTextReason(f.text); r != "" {
			return "a synced-lyrics " + f.what + " " + r
		}
	}
	for _, ln := range sl.Lines {
		if r := WritableTextReason(ln.Text); r != "" {
			return "a synced-lyrics line " + r
		}
	}
	return ""
}

// projectSyncedLyrics grades sets, which the destination does not hold as given, and resolves
// the sets written: one holding text no format stores is left out, ahead of the count a format
// holds no more than.
func (p *TransferProjection) projectSyncedLyrics(sets []SyncedLyrics, caps Capabilities) {
	c := caps.SyncedLyrics
	p.lyrics = make([]Disposition, len(sets))
	drop := func(idx []int, reason string) {
		for _, i := range idx {
			p.lyrics[i] = Dropped
		}
		if len(idx) > 0 {
			p.sets = append(p.sets, TransferItem{Kind: TransferSyncedLyric, Count: len(idx), Disposition: Dropped, Reason: reason})
		}
	}
	var kept, unwritable []int
	var unwritableReason string
	for i, sl := range sets {
		// A destination that takes no set at all gives its own reason for every one.
		if r := syncedLyricsUnwritableReason(sl); r != "" && !caps.ReadOnly && c.Write != AccessNone {
			unwritable = append(unwritable, i)
			if unwritableReason == "" {
				unwritableReason = r
			}
			continue
		}
		kept = append(kept, i)
	}
	disp, reason := dispose(c, caps.ReadOnly, len(kept), "synced lyrics", nil)
	switch disp {
	case Dropped:
		drop(kept, reason)
		kept = nil
	case Lossy:
		for _, i := range kept {
			p.lyrics[i] = Lossy
		}
		if len(kept) > 0 {
			p.sets = append(p.sets, TransferItem{Kind: TransferSyncedLyric, Count: len(kept), Disposition: Lossy, Reason: reason})
		}
	default:
		// Per-set loss splits carried vs lossy. One lossy reason: lost metadata, then a
		// language stored in another form, then a timestamp clamped.
		var carried, lossy int
		var lostMetadata bool
		var language string
		for _, i := range kept {
			sl := sets[i]
			metaLoss := SyncedLyricsSetLosesMetadata(sl, c.SyncedLyricsLoss)
			stored := sl.Language
			if c.storedLanguage != nil {
				stored = c.storedLanguage(sl.Language)
			}
			if !metaLoss && stored == sl.Language && !SyncedLyricsSetClampOverflows(sl, c.SyncedLyricsTimeMax) {
				carried++
				continue
			}
			lossy++
			p.lyrics[i] = Lossy
			lostMetadata = lostMetadata || metaLoss
			if stored != sl.Language && language == "" {
				language = fmt.Sprintf("language %q reads back as %q", sl.Language, stored)
			}
		}
		lossyReason := "a synced-lyric timestamp is too large and was clamped"
		switch {
		case lostMetadata:
			lossyReason = c.Reason()
		case language != "":
			lossyReason = language
		}
		p.sets = appendCarriedLossyItems(p.sets, TransferSyncedLyric, carried, lossy, lossyReason)
	}
	drop(unwritable, unwritableReason)
	p.SyncedLyrics = pick(sets, kept)
	p.SetSyncedLyrics = len(kept) > 0
}

// GradedTransferValues is vals in the form a transfer grades and writes them for k: a trimmable
// key's tokens are trimmed. vals is returned as given when nothing changes.
func GradedTransferValues(k tag.Key, vals []string) []string {
	if !tag.IsTrimmableKey(k) {
		return vals
	}
	graded := vals
	for i, v := range vals {
		trimmed := tag.TrimTokenValue(k, v)
		if trimmed == v {
			continue
		}
		if &graded[0] == &vals[0] {
			graded = slices.Clone(vals)
		}
		graded[i] = trimmed
	}
	return graded
}

// Reasons [ReconcileTransfer] gives where a writer stored other than the grade said: for an item
// the result holds nothing of, by what became of the value the destination held, and for a
// picture, chapter or synced lyrics set the result holds in another form.
const (
	notStoredReason = "the destination does not store this "
	notStoredValue  = notStoredReason + "value"
	lostHeldReason  = notStoredValue + ", and loses the one it held"
	keptHeldReason  = notStoredValue + " and keeps the one it had"
	lostHeldSuffix  = "; the destination loses the value it held"

	pictureAlteredReason = "stored with another type, description or format than the source gives"
	chapterAlteredReason = "stored with another time, title, language or flag than the source gives"
	lyricsAlteredReason  = "stored with other lines, language or description than the source gives"
)

// ReconcileTransfer is the report of p held to result, what the plan writing p leaves in the
// destination that held before: an item graded carried that result does not hold as src gives
// it is lossy, or dropped where result holds nothing of it. Each picture, chapter and synced
// lyrics set is held to its own fate. src is the media p was projected from. A grade is never
// raised.
func ReconcileTransfer(p TransferProjection, src, before, result *Media) []TransferItem {
	out := make([]TransferItem, 0, len(p.Items))
	for _, it := range p.Items {
		if it.Kind == TransferField {
			it.Disposition, it.Reason = reconcileField(it, src, before, result)
			out = append(out, it)
		}
	}
	// Pictures are compared as a read settles them, as the grades were made.
	pics, stored := ProjectPictures(src.Pictures), ProjectPictures(result.Pictures)
	out = reconcileSet(out, p.Items, TransferPicture, p.pictures, matchFates(p.pictures, len(stored),
		func(i, j int) bool { return equalPicture(pics[i], stored[j]) },
		func(i, j int) bool { return bytes.Equal(pics[i].Data, stored[j].Data) },
	), pictureAlteredReason, "picture")

	chapters := chapterFates(src.Chapters, result.Chapters, src.Properties.Duration(), before.Properties.Duration())
	out = reconcileSet(out, p.Items, TransferChapter, p.chapters, chapters, chapterAlteredReason, "chapter")

	sets, got := src.SyncedLyrics, result.SyncedLyrics
	return reconcileSet(out, p.Items, TransferSyncedLyric, p.lyrics, matchFates(p.lyrics, len(got),
		func(i, j int) bool { return EqualSyncedLyrics(sets[i:i+1], got[j:j+1]) },
		func(i, j int) bool { return slices.Equal(sets[i].Lines, got[j].Lines) },
		// A set whose lines the writer changed reads back in the place it was written.
		func(int, int) bool { return true },
	), lyricsAlteredReason, "synced lyrics set")
}

// WithoutDropped is p less the writes items grade dropped, items being the report of p held to
// a plan's result ([ReconcileTransfer]): a field the destination stores nothing of, and the
// pictures, chapters or synced lyrics where it stores none of them. Left unwritten, the
// destination keeps what it held in their place, and a key of its own a field would only have
// read back under is left as it was. The grades of what is left out stand, and the next
// reconcile says what became of the value the destination held. ok is false when p writes
// nothing items drop.
func (p TransferProjection) WithoutDropped(items []TransferItem) (out TransferProjection, ok bool) {
	written := make(map[tag.Key]bool, len(p.Keys))
	for _, k := range p.Keys {
		written[k] = true
	}
	dropped := map[tag.Key]TransferItem{}
	var stored [TransferSyncedLyric + 1]bool
	for _, it := range items {
		switch {
		case it.Kind != TransferField:
			stored[it.Kind] = stored[it.Kind] || it.Disposition == Carried || it.Disposition == Lossy
		case it.Disposition == Dropped && written[it.Key]:
			dropped[it.Key] = it
		}
	}
	unset := [len(stored)]bool{
		TransferPicture:     p.SetPictures && !stored[TransferPicture],
		TransferChapter:     p.SetChapters && !stored[TransferChapter],
		TransferSyncedLyric: p.SetSyncedLyrics && !stored[TransferSyncedLyric],
	}
	if len(dropped) == 0 && !slices.Contains(unset[:], true) {
		return p, false
	}
	out = p
	out.Keys = slices.DeleteFunc(slices.Clone(p.Keys), func(k tag.Key) bool { _, gone := dropped[k]; return gone })
	out.Items = make([]TransferItem, 0, len(p.Items))
	for kind := range TransferKind(len(unset)) {
		from := p.Items
		if unset[kind] {
			from = items
		}
		for _, it := range from {
			if it.Kind != kind {
				continue
			}
			if d, gone := dropped[it.Key]; gone && kind == TransferField {
				it = d
			}
			out.Items = append(out.Items, it)
		}
	}
	allDropped := func(n int) []Disposition { return slices.Repeat([]Disposition{Dropped}, n) }
	if unset[TransferPicture] {
		out.Pictures, out.SetPictures, out.pictures = nil, false, allDropped(len(p.pictures))
	}
	if unset[TransferChapter] {
		out.SetChapters, out.chapters = false, allDropped(len(p.chapters))
	}
	if unset[TransferSyncedLyric] {
		out.SyncedLyrics, out.SetSyncedLyrics, out.lyrics = nil, false, allDropped(len(p.lyrics))
	}
	return out, true
}

// GradeStoredField grades the field k of src by result, what a write leaves in a destination
// that held before: carried where result holds the values as src gives them, else what became
// of them.
func GradeStoredField(k tag.Key, src, before, result *Media) (Disposition, string) {
	return reconcileField(TransferItem{Kind: TransferField, Key: k}, src, before, result)
}

// reconcileField is the grade of a field item once result is known. Carried is a statement
// about result: it holds the values as src gives them, and for a comment or lyric the
// descriptions and languages src states. A grade is never raised: lossy may be the capability
// table's word on a stored form that reads back the same.
func reconcileField(it TransferItem, src, before, result *Media) (Disposition, string) {
	got, _ := result.Tags.Get(it.Key)
	held, _ := before.Tags.Get(it.Key)
	if it.Disposition == Dropped {
		return Dropped, withHeldFate(it.Reason, held, got)
	}
	if it.Disposition != Carried && it.Disposition != Lossy {
		return it.Disposition, it.Reason
	}
	vals, _ := src.Tags.Get(it.Key)
	want := GradedTransferValues(it.Key, vals)
	switch {
	case TransferValuesEqual(it.Key, want, got, src.Format, result.Format), slices.Equal(vals, got):
		if it.Disposition == Lossy {
			return it.Disposition, it.Reason
		}
		if qualifier, stated, stored, differs := storedQualifier(it.Key, src, result); differs {
			return Lossy, fmt.Sprintf("%s %q reads back as %q", qualifier, stated, stored)
		}
		return Carried, ""
	case len(got) == 0:
		// A grade that already says where the values went needs them there; one made here needs
		// them newly there, or a value the destination held all along would pass for the source's.
		other, stored := storedUnder(want, src, before, result, it.Disposition == Carried)
		if !stored {
			var mixed bool
			if other, mixed = mixedInto(want, src, before, result); !mixed {
				return Dropped, withHeldFate(notStoredValue, held, got)
			}
		}
		// A key only the destination holds is the destination's: a field that joins its value
		// there, or takes its place, is dropped.
		if own, _ := before.Tags.Get(other); !stored || len(own) > 0 && !slices.Equal(own, want) {
			return Dropped, fmt.Sprintf("the destination stores it as %s, which already holds another value", other)
		}
		if it.Disposition == Carried {
			return Lossy, "stored as " + string(other)
		}
		return it.Disposition, it.Reason
	case it.Disposition == Lossy:
		return it.Disposition, it.Reason
	case len(got) < len(want) && slices.Equal(got, want[:len(got)]):
		if len(got) == 1 {
			return Lossy, fmt.Sprintf("only the first of %d values is stored", len(want))
		}
		return Lossy, fmt.Sprintf("only the first %d of %d values are stored", len(got), len(want))
	case slices.Equal(got, held):
		return Dropped, withHeldFate(notStoredValue, held, got)
	case len(got) == 1 && len(want) > 1 && !slices.ContainsFunc(want, func(v string) bool { return !strings.Contains(got[0], v) }):
		return Lossy, fmt.Sprintf("the values are stored joined as %q", got[0])
	}
	quoted := make([]string, len(got))
	for i, v := range got {
		quoted[i] = strconv.Quote(v)
	}
	return Lossy, "stored as " + strings.Join(quoted, ", ")
}

// withHeldFate is reason, why a field is dropped, with what became of the value the destination
// held under its key, held, in a result holding got: nothing to say where it held none, or where
// a reason of the format's own stands and the value is as it was. A value gone went with another
// field's write, the dropped one being left unwritten.
func withHeldFate(reason string, held, got []string) string {
	switch reason {
	case lostHeldReason, keptHeldReason:
		reason = notStoredValue
	default:
		reason = strings.TrimSuffix(reason, lostHeldSuffix)
	}
	lost := len(held) > 0 && !slices.Equal(held, got)
	switch {
	case reason == notStoredValue && lost:
		return lostHeldReason
	case reason == notStoredValue && len(held) > 0:
		return keptHeldReason
	case lost:
		return reason + lostHeldSuffix
	}
	return reason
}

// storedQualifier finds the first description or language src states for a comment or lyric
// that result holds otherwise ([QualifierStoredAs]). Other keys have none.
func storedQualifier(k tag.Key, src, result *Media) (qualifier, stated, stored string, differs bool) {
	switch k {
	case tag.Comment:
		return QualifierStoredAs(src.Comments, result.Comments, TextLossNone)
	case tag.Lyrics:
		return QualifierStoredAs(src.Lyrics, result.Lyrics, TextLossNone)
	}
	return "", "", "", false
}

// blankMedia is a destination that held nothing.
var blankMedia = Media{Tags: tag.NewTagSet()}

// Grade renders keys, the fields of written a transfer sets or the destination holds already,
// and returns the grade of each by what became of it. A field that reads back under its own key
// is graded by what that holds ([GradeStoredField]). One that does not is rendered alone to see
// where it goes: under another key, it is stored as that key where the rendering of them all
// holds it so, and dropped where the transfer writes that key too or other values join it
// there. A dropped field is one the transfer does not write, so it is left out and the rest
// rendered again: once per call, once per field that reads back under no key of its own, and
// once more per pass that drops a field.
func (r RenderedFields) Grade(written *Media, keys []tag.Key) func(tag.Key) (Disposition, string) {
	// The rendering is graded against the fields it holds, so a key the source carries and the
	// transfer leaves out does not pass for the place another one went.
	view := &Media{Format: written.Format, Tags: tag.NewTagSet(), Comments: written.Comments, Lyrics: written.Lyrics}
	for _, k := range keys {
		vals, _ := written.Tags.Get(k)
		view.Tags.Set(k, GradedTransferValues(k, vals)...)
	}
	reason := func(k tag.Key, d Disposition, found string) string {
		if r.Reason != nil {
			if s := r.Reason(k, d, view.Tags); s != "" {
				return s
			}
		}
		return found
	}
	// alone is the key each field that reads back under none of its own goes to when written
	// by itself, "" for nowhere. Two fields holding the same value are told apart by it.
	alone := map[tag.Key]tag.Key{}
	aloneKey := func(k tag.Key, vals []string) tag.Key {
		other, ok := alone[k]
		if !ok {
			only := tag.NewTagSet()
			only.Set(k, vals...)
			for o, got := range r.Render(written, only).Tags.All() {
				if o != k && slices.Equal(got, vals) {
					other = o
				}
			}
			alone[k] = other
		}
		return other
	}
	dropped := map[tag.Key]string{}
	var read *Media
	for settled := false; !settled; {
		read, settled = r.Render(written, view.Tags), true
		for _, k := range view.Tags.Keys() {
			if read.Tags.Has(k) {
				continue
			}
			vals, _ := view.Tags.Get(k)
			other := aloneKey(k, vals)
			found := notStoredValue
			if other != "" {
				if view.Tags.Has(other) {
					found = fmt.Sprintf("the format reads it back as %s, which the source holds too", other)
				} else if got, _ := read.Tags.Get(other); slices.Equal(got, vals) {
					continue
				}
			}
			dropped[k] = reason(k, Dropped, found)
			view.Tags.Delete(k)
			settled = false
		}
	}
	return func(k tag.Key) (Disposition, string) {
		if found, ok := dropped[k]; ok {
			return Dropped, found
		}
		// What the settled rendering left under no key of its own is stored where it went alone.
		if !read.Tags.Has(k) {
			return Lossy, "stored as " + string(alone[k])
		}
		d, found := GradeStoredField(k, view, &blankMedia, read)
		if d != Carried {
			found = reason(k, d, found)
		}
		return d, found
	}
}

// storedUnder finds the key result holds want under, one the source does not carry itself, and
// where newly is set one before did not already hold so.
func storedUnder(want []string, src, before, result *Media, newly bool) (tag.Key, bool) {
	for k, got := range result.Tags.All() {
		if src.Tags.Has(k) || !slices.Equal(got, want) {
			continue
		}
		if held, _ := before.Tags.Get(k); !newly || !slices.Equal(held, got) {
			return k, true
		}
	}
	return "", false
}

// mixedInto finds the key of the destination's own, one the source does not carry, that result
// holds want under beside the values before held there.
func mixedInto(want []string, src, before, result *Media) (tag.Key, bool) {
	for k, got := range result.Tags.All() {
		held, _ := before.Tags.Get(k)
		if src.Tags.Has(k) || len(held) == 0 || slices.Equal(held, got) {
			continue
		}
		if !slices.ContainsFunc(want, func(v string) bool { return !slices.Contains(got, v) }) {
			return k, true
		}
	}
	return "", false
}

// fate is what became of one picture, chapter or synced lyrics set in a result.
type fate uint8

const (
	absent  fate = iota // the result holds nothing of it
	altered             // the result holds it in another form
	exact               // the result holds it as given
)

// matchFates pairs each element a write stores, those graded other than dropped, with one of
// the m a result holds, each taken once: with one the same as it (exact), else with the first
// left that an akin rule calls the element in another form (altered), the rules tried in turn.
func matchFates(grades []Disposition, m int, same func(i, j int) bool, akin ...func(i, j int) bool) []fate {
	fates := make([]fate, len(grades))
	taken := make([]bool, m)
	pair := func(f fate, match func(i, j int) bool) {
		for i, g := range grades {
			if g == Dropped || fates[i] != absent {
				continue
			}
			for j := range taken {
				if !taken[j] && match(i, j) {
					taken[j], fates[i] = true, f
					break
				}
			}
		}
	}
	pair(exact, same)
	for _, match := range akin {
		pair(altered, match)
	}
	return fates
}

// chapterFates is the fate of each chapter of src in stored, the lists of files running srcDur
// and storedDur. A chapter is the one stored at its start under its title, and exact where the
// rest of it reads back too, give or take an end either file rebuilds. One left out leaves the
// chapters after it their own fates. What then is left of both lists pairs up in order: a
// chapter stored at another start or under another title. A list is graded stored or dropped
// whole, so every chapter is looked for.
func chapterFates(src, stored []Chapter, srcDur, storedDur time.Duration) []fate {
	fates := make([]fate, len(src))
	if len(stored) == 0 {
		return fates
	}
	type identity struct {
		start time.Duration
		title string
	}
	at := make(map[identity][]int, len(stored))
	for j, c := range stored {
		id := identity{c.Start, c.Title}
		at[id] = append(at[id], j)
	}
	srcEnds, storedEnds := normalizeReconstructableEnds(src, srcDur), normalizeReconstructableEnds(stored, storedDur)
	taken := make([]bool, len(stored))
	for i, c := range src {
		id := identity{c.Start, c.Title}
		if len(at[id]) == 0 {
			continue
		}
		j := at[id][0]
		at[id], taken[j], fates[i] = at[id][1:], true, altered
		if c == stored[j] || srcEnds[i] == storedEnds[j] {
			fates[i] = exact
		}
	}
	j := 0
	for i := range src {
		if fates[i] != absent {
			continue
		}
		for j < len(stored) && taken[j] {
			j++
		}
		if j == len(stored) {
			break
		}
		fates[i], j = altered, j+1
	}
	return fates
}

// reconcileSet appends the items of kind, each element regraded by its own fate: one graded
// carried is carried only where the result holds it as given and lossy where it holds it in
// another form, and one graded stored that the result holds nothing of is dropped, named by
// noun. A lossy grade keeps its reason.
func reconcileSet(out, items []TransferItem, kind TransferKind, grades []Disposition, fates []fate, alteredReason, noun string) []TransferItem {
	var carried, lossy, newLossy, newDropped int
	for i, g := range grades {
		switch {
		case g != Carried && g != Lossy:
		case fates[i] == absent:
			newDropped++
		case g == Lossy:
			lossy++
		case fates[i] == exact:
			carried++
		default:
			newLossy++
		}
	}
	// The order of a report: carried, lossy, dropped.
	appendNewLossy := func() {
		if newLossy > 0 {
			out = append(out, TransferItem{Kind: kind, Count: newLossy, Disposition: Lossy, Reason: alteredReason})
			newLossy = 0
		}
	}
	for _, it := range items {
		if it.Kind != kind {
			continue
		}
		switch it.Disposition {
		case Carried:
			it.Count = carried
		case Lossy:
			it.Count = lossy
		default:
			appendNewLossy()
		}
		if it.Count > 0 {
			out = append(out, it)
		}
	}
	appendNewLossy()
	if newDropped > 0 {
		out = append(out, TransferItem{Kind: kind, Count: newDropped, Disposition: Dropped, Reason: notStoredReason + noun})
	}
	return out
}

// appendCarriedLossyItems appends carried then merged lossy items (empty reason on carried).
func appendCarriedLossyItems(items []TransferItem, kind TransferKind, carried, lossy int, lossyReason string) []TransferItem {
	if carried > 0 {
		items = append(items, TransferItem{Kind: kind, Count: carried, Disposition: Carried})
	}
	if lossy > 0 {
		items = append(items, TransferItem{Kind: kind, Count: lossy, Disposition: Lossy, Reason: lossyReason})
	}
	return items
}

// dispose grades metadata against capability c. Read-only and MaxItems overrun -> Dropped.
// Does not use MaxValues (discovery hint only; loss is via Fidelity/Constraints).
func dispose(c Capability, readOnly bool, count int, noun string, values []string) (Disposition, string) {
	if readOnly {
		return Dropped, "destination is read-only"
	}
	if c.Write == AccessNone {
		// User-facing drop wording names destination limits, not internal Representation strings.
		if c.Read != AccessNone {
			return Dropped, "destination cannot write " + noun
		}
		return Dropped, "destination format does not store " + noun
	}
	if c.MaxItems > 0 && count > c.MaxItems {
		return Dropped, fmt.Sprintf("exceeds the destination limit of %d", c.MaxItems)
	}
	// dropsValue before reducesValue: omit -> Dropped, not Lossy.
	if c.dropsValue != nil {
		if slices.ContainsFunc(values, c.dropsValue) {
			return Dropped, "the destination cannot store this value"
		}
	}
	// reducesValue: value-dependent Lossy vs Carried.
	if c.reducesValue != nil {
		if slices.ContainsFunc(values, c.reducesValue) {
			return Lossy, c.Reason()
		}
		return Carried, ""
	}
	if c.Write == AccessPartial {
		return Lossy, c.Reason()
	}
	return Carried, ""
}

// UnrepresentableReason names dst and distinct unrepresentable MIMEs (effective MIME). A link
// picture is named for what it is, not by the marker its MIME field holds.
func UnrepresentableReason(dst Format, mimes []string) string {
	seen := map[string]bool{}
	var distinct []string
	for _, m := range mimes {
		if seen[m] {
			continue
		}
		seen[m] = true
		if m == LinkMIME {
			m = "a linked (URL) picture"
		}
		distinct = append(distinct, m)
	}
	return fmt.Sprintf("%s cannot store %s", dst, strings.Join(distinct, ", "))
}
