package matroska

import (
	"math"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// Matroska chapters form a small tree: Chapters > EditionEntry > ChapterAtom. Each
// EditionEntry is one independent chapter set; the edition flagged default (or the
// first, when none is flagged) projects into core.Media.Chapters.

// chapterDoc is the parsed Chapters element retained on the native doc for the
// dump view, verbatim preservation, and re-rendering on a chapter edit.
type chapterDoc struct {
	hasCRC   bool // the Chapters master led with a CRC-32
	editions []chapterEdition
	defIdx   int  // index of the default edition, or -1 when there are none
	defLossy bool // the default edition carries nested atoms or multi-language
	// displays that a flat re-render would drop (drives WarnChaptersFlattened).
}

// chapterEdition is one parsed EditionEntry.
type chapterEdition struct {
	// raw is the EditionEntry as a rewrite copies it: its bytes, or for one that overruns the
	// Chapters element or holds bytes that are no whole element, an EditionEntry of its whole
	// children, empty when there are none.
	raw       []byte
	prefix    []byte
	hasCRC    bool
	startUIDs []chapterStartUID
}

// chapterStartUID pairs a parsed chapter's start time with its ChapterUID, using 0
// when the atom carried none. Zero entries stay in the queue: for same-start atoms,
// dropping a zero would shift a later real UID onto the wrong chapter.
type chapterStartUID struct {
	start time.Duration
	uid   uint64
}

// parseChapters reads a Chapters element into the native chapterDoc and returns the
// default edition projected as []core.Chapter (nil when there are no editions), stably
// ordered by start time.
func parseChapters(src core.ReaderAtSized, chapters element, depth *bits.Depth, limit int64, d *doc) ([]core.Chapter, error) {
	hasCRC, err := firstChildIsCRC(src, chapters, limit)
	if err != nil {
		return nil, err
	}
	cd := &chapterDoc{defIdx: -1, hasCRC: hasCRC}
	var defChapters []core.Chapter
	stop, err := eachChildReport(src, chapters.dataStart, chapters.dataEnd, depth, limit, func(el element) error {
		if el.id != idEditionEntry {
			if el.overruns() {
				d.chapDamage.unread += el.dataEnd - el.start
			}
			return nil
		}
		r, err := parseEdition(src, el, depth, limit)
		if err != nil {
			return err
		}
		d.chapDamage.add(r.unread, el)
		idx := len(cd.editions)
		cd.editions = append(cd.editions, r.ed)
		// The default edition (the first flagged one, else the first) supplies the
		// projection; idx == 0 is the tentative fallback until a flag appears.
		if (r.isDefault && cd.defIdx < 0) || (cd.defIdx < 0 && idx == 0) {
			if r.isDefault {
				cd.defIdx = idx
			}
			defChapters, cd.defLossy, d.cuts.chapters = r.chapters, r.lossy, r.cuts
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.chapDamage.unread += chapters.dataEnd - stop
	d.chapters = cd
	if len(cd.editions) == 0 {
		return nil, nil
	}
	if cd.defIdx < 0 {
		cd.defIdx = 0 // no edition flagged default: the first one is the default
	}
	// The default edition is re-rendered on an edit, never copied verbatim, so its
	// captured bytes are not needed.
	cd.editions[cd.defIdx].raw = nil
	// SetChapters stable-sorts by start; ordering the projection the same way makes
	// SetChapters(doc.Chapters()...) a no-op even when the source stored atoms out of
	// start order.
	core.SortChaptersByStart(defChapters)
	return defChapters, nil
}

// editionRead is what parseEdition makes of one EditionEntry.
type editionRead struct {
	ed        chapterEdition
	isDefault bool
	chapters  []core.Chapter // its ChapterAtoms that are whole
	lossy     bool           // they hold what a flat re-render drops
	unread    int64          // bytes of it that are no whole element, such a ChapterAtom among them
	cuts      int            // titles of chapters read up to a NUL that other bytes follow
}

// parseEdition reads one EditionEntry: whether it is the default edition, the non-atom
// children kept verbatim in prefix (EditionUID and the edition flags), the ordered
// ChapterAtom UIDs, and the atoms projected as chapters.
func parseEdition(src core.ReaderAtSized, ed element, depth *bits.Depth, limit int64) (r editionRead, err error) {
	held, err := captureHeld(src, ed, limit)
	if err != nil {
		return r, err
	}
	if r.ed.hasCRC, err = firstChildIsCRC(src, ed, limit); err != nil {
		return r, err
	}
	bytesOf := func(el element) []byte {
		if held == nil {
			return nil
		}
		return held[el.start-ed.start : el.dataEnd-ed.start]
	}
	var whole []element // its whole children, less a CRC-32
	stop, err := eachChildReport(src, ed.dataStart, ed.dataEnd, depth, limit, func(el element) error {
		if el.overruns() {
			r.unread += el.dataEnd - el.start
			return nil
		}
		switch el.id {
		case idCRC32:
			return nil // stripped from the prefix; a re-render recomputes its own CRC
		case idChapterAtom:
			a, err := parseChapterAtom(src, el, depth, limit)
			if err != nil {
				return err
			}
			if !a.whole {
				r.unread += el.dataEnd - el.start
				return nil
			}
			// Keep one entry per atom in file order, including a UID of 0. A popped zero
			// means "mint fresh"; for same-start atoms it also keeps later real UIDs from
			// sliding onto earlier UID-less chapters.
			r.ed.startUIDs = append(r.ed.startUIDs, chapterStartUID{a.ch.Start, a.uid})
			r.chapters = append(r.chapters, a.ch)
			r.lossy = r.lossy || a.lossy
			r.cuts += btoi(a.cut)
		case idEditionFlagDf:
			v, err := readUint(src, el, limit)
			if err != nil {
				return err
			}
			r.isDefault = r.isDefault || v != 0
			r.ed.prefix = append(r.ed.prefix, bytesOf(el)...)
		default:
			// EditionUID and any other non-atom child: kept verbatim in the prefix so
			// edition-scoped tags and unmodeled fields survive a chapter edit.
			r.ed.prefix = append(r.ed.prefix, bytesOf(el)...)
		}
		whole = append(whole, el)
		return nil
	})
	if err != nil {
		return r, err
	}
	r.unread += ed.dataEnd - stop
	switch {
	case held == nil:
		// A read that came up short: with no raw the writer refuses to copy the edition.
	case !ed.overruns() && r.unread == 0:
		r.ed.raw = held
	default:
		r.ed.raw = []byte{}
		var content []byte
		for _, el := range whole {
			content = append(content, bytesOf(el)...)
		}
		if len(content) > 0 {
			r.ed.raw = masterElement(idEditionEntry, content, r.ed.hasCRC)
		}
	}
	return r, nil
}

// wholeChildren reports whether el holds nothing but whole elements, one level down.
func wholeChildren(src core.ReaderAtSized, el element, depth *bits.Depth, limit int64) (bool, error) {
	whole := true
	stop, err := eachChildReport(src, el.dataStart, el.dataEnd, depth, limit, func(c element) error {
		whole = whole && !c.overruns()
		return nil
	})
	return whole && stop == el.dataEnd, err
}

// chapterAtom is what parseChapterAtom reads of one ChapterAtom.
type chapterAtom struct {
	ch    core.Chapter
	uid   uint64
	lossy bool // holds what the flat model does not carry
	whole bool // its children, and those of its displays, are whole elements
	cut   bool // the title was read up to a NUL that other bytes follow
}

// parseChapterAtom reads one ChapterAtom's UID, start/end (absolute nanoseconds), the
// hidden/enabled flags, and the first ChapterDisplay (title + language). A nested atom, like any
// child the flat model does not carry, is not looked into: it is an element, kept or dropped
// as one.
func parseChapterAtom(src core.ReaderAtSized, atom element, depth *bits.Depth, limit int64) (a chapterAtom, err error) {
	var startNs, endNs int64
	displays := 0
	a.whole = true
	stop, err := eachChildReport(src, atom.dataStart, atom.dataEnd, depth, limit, func(el element) error {
		if el.overruns() {
			a.whole = false
			return nil
		}
		switch el.id {
		case idChapterUID, idChapTimeStart, idChapTimeEnd, idChapFlagHidden, idChapFlagEnabled:
			v, err := readUint(src, el, limit)
			if err != nil {
				return err
			}
			switch el.id {
			case idChapterUID:
				a.uid = v
			case idChapTimeStart:
				startNs = clampNs(v)
			case idChapTimeEnd:
				endNs = clampNs(v)
			case idChapFlagHidden:
				a.ch.Hidden = v != 0 // EBML default 0; 1 = hidden
			default:
				a.ch.Disabled = v == 0 // EBML default 1; 0 = disabled
			}
		case idChapDisplay:
			displays++
			if displays > 1 {
				// A second display is an other-language title the flat model cannot carry.
				whole, err := wholeChildren(src, el, depth, limit)
				a.whole = a.whole && whole
				return err
			}
			disp, err := readChapterDisplay(src, el, depth, limit)
			if err != nil {
				return err
			}
			a.ch.Title, a.ch.Language, a.ch.LanguageIETF = disp.title, disp.lang, disp.langIETF
			a.lossy = a.lossy || disp.lossy
			a.whole = a.whole && disp.whole
			a.cut = disp.cut
		case idChapterAtom:
			a.lossy = true // a nested sub-chapter
		case idCRC32, idVoid:
			// Structural framing, not chapter data: a re-render recomputes its own CRC and
			// drops padding, so neither is a content loss.
		default:
			// Any other ChapterAtom child (ChapterSegmentUID, ChapProcess, ChapterTrack, ...)
			// is unmodeled, so the flatten warning covers it too.
			a.lossy = true
		}
		return nil
	})
	a.lossy = a.lossy || displays > 1
	a.ch.Start = time.Duration(startNs)
	if endNs > startNs {
		a.ch.End = time.Duration(endNs) // an end <= start (or absent) means "open"
	}
	a.whole = a.whole && stop == atom.dataEnd
	return a, err
}

// chapterDisplay is what readChapterDisplay reads of one ChapterDisplay.
type chapterDisplay struct {
	title, lang, langIETF string
	lossy                 bool // holds a child the flat model drops
	whole                 bool // its children are whole elements
	cut                   bool // the title was read up to a NUL that other bytes follow
}

// readChapterDisplay reads one ChapterDisplay: the first ChapString title, the
// ChapLanguage (ISO-639-2), and the ChapLanguageIETF (BCP-47). Any other child
// (ChapCountry, ...) is unmodeled and flagged lossy.
func readChapterDisplay(src core.ReaderAtSized, disp element, depth *bits.Depth, limit int64) (d chapterDisplay, err error) {
	got := false
	d.whole = true
	stop, err := eachChildReport(src, disp.dataStart, disp.dataEnd, depth, limit, func(el element) error {
		if el.overruns() {
			d.whole = false
			return nil
		}
		var err error
		switch el.id {
		case idChapString:
			if got {
				d.lossy = true // a second ChapString in one display: the flat model keeps only the first
				return nil
			}
			if d.title, d.cut, err = readString(src, el, limit); err != nil {
				return err
			}
			// An invalid-UTF-8 title reads as "", matching MP4's chpl and the QuickTime chapter
			// track, so --json never emits raw invalid bytes.
			if !utf8.ValidString(d.title) {
				d.title = ""
			}
			got = true
		case idChapLang:
			d.lang, err = readLabel(src, el, limit) // informational
		case idChapLangIETF:
			d.langIETF, err = readLabel(src, el, limit)
		case idCRC32, idVoid:
			// Structural framing, not display content; never a loss.
		default:
			d.lossy = true // an unmodeled ChapDisplay child (ChapCountry, ...) the flat model drops
		}
		return err
	})
	// Drop a language not worth surfacing or re-emitting: invalid UTF-8, or the "und"
	// default, which would print a spurious "[lang: und]".
	d.lang = normalizeChapLang(d.lang)
	d.langIETF = normalizeChapLang(d.langIETF)
	d.whole = d.whole && stop == disp.dataEnd
	return d, err
}

// normalizeChapLang returns "" for a chapter language that carries no information: invalid
// UTF-8, or the "und"/absent default. Otherwise it returns the language verbatim.
func normalizeChapLang(s string) string {
	if !utf8.ValidString(s) || !meaningfulLang(s) {
		return ""
	}
	return s
}

// clampNs converts an EBML timestamp to int64 nanoseconds, capping a hostile
// out-of-range value so it stays a non-negative time.Duration.
func clampNs(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// chaptersFromRaw parses a standalone Chapters element's bytes into its projected
// chapters. buildResult uses it on the rendered bytes so the plan's result equals a
// fresh parse.
func chaptersFromRaw(raw []byte, depth *bits.Depth, limit int64) []core.Chapter {
	rs := core.BytesSource(raw)
	root, ok, err := readElement(rs, 0, int64(len(raw)), limit)
	if !ok || err != nil || root.id != idChapters {
		return nil
	}
	chs, err := parseChapters(rs, root, depth, limit, &doc{})
	if err != nil {
		return nil
	}
	return chs
}

// renderChapters builds the new Chapters element bytes from the edited chapters,
// re-rendering the default edition and preserving every other edition verbatim.
func renderChapters(d *doc, chs []core.Chapter) []byte {
	// Clearing chapters (an empty list) removes them entirely.
	if len(chs) == 0 {
		return nil
	}
	old := d.chapters
	hasCRC := false
	wroteDefault := false
	var content []byte

	if old != nil {
		hasCRC = old.hasCRC
		for i, ed := range old.editions {
			if i == old.defIdx {
				if eb := renderDefaultEdition(ed, chs); eb != nil {
					content = append(content, eb...)
					wroteDefault = true
				}
				continue
			}
			if ed.raw != nil {
				content = append(content, ed.raw...) // non-default edition: verbatim
			}
		}
	}
	// Creating chapters on a file with none: synthesize a fresh default edition.
	if !wroteDefault {
		content = append(content, renderDefaultEdition(chapterEdition{}, chs)...)
	}
	if len(content) == 0 {
		return nil
	}
	return masterElement(idChapters, content, hasCRC)
}

// renderDefaultEdition rebuilds the default EditionEntry from the edited chapters,
// keeping its preserved prefix (EditionUID/flags) and reassigning each surviving
// chapter its original ChapterUID by start time.
func renderDefaultEdition(ed chapterEdition, chs []core.Chapter) []byte {
	if len(chs) == 0 {
		return nil
	}
	// Size content up front: the prefix plus ~48 bytes per chapter (UID, start, end,
	// display framing) and its title.
	size := len(ed.prefix)
	for _, ch := range chs {
		size += 48 + len(ch.Title)
	}
	content := make([]byte, 0, size)
	if len(ed.prefix) > 0 {
		content = append(content, ed.prefix...)
	} else {
		content = append(content, uintElement(idEditionFlagDf, 1)...) // mark default, as ffmpeg does
	}
	queue := chapterUIDQueue(ed.startUIDs)
	for _, ch := range chs {
		var uid []byte
		if kept := popChapterUID(queue, ch.Start); kept != 0 {
			uid = uintData(kept) // in as few bytes as it takes, like every integer of the atom
		} else {
			uid = mintUID()
		}
		content = append(content, renderChapterAtom(uid, ch)...)
	}
	return masterElement(idEditionEntry, content, ed.hasCRC)
}

// chapterUIDQueue groups saved UIDs by start time. Each queue preserves file order, so
// duplicate-start atoms keep their UIDs in the same order during re-render.
func chapterUIDQueue(pairs []chapterStartUID) map[time.Duration][]uint64 {
	q := make(map[time.Duration][]uint64, len(pairs))
	for _, p := range pairs {
		q[p.start] = append(q[p.start], p.uid)
	}
	return q
}

// popChapterUID removes and returns the next saved ChapterUID at start. It returns 0
// when none remains; the caller treats that as a request to mint a fresh UID.
func popChapterUID(q map[time.Duration][]uint64, start time.Duration) uint64 {
	ids := q[start]
	if len(ids) == 0 {
		return 0
	}
	q[start] = ids[1:]
	return ids[0]
}

// renderChapterAtom encodes one ChapterAtom: its UID (the value's bytes), the
// absolute-nanosecond start (and end when the chapter is closed), the non-default
// ChapterFlagHidden/ChapterFlagEnabled flags, and a ChapterDisplay (title + language) when titled.
func renderChapterAtom(uid []byte, ch core.Chapter) []byte {
	content := make([]byte, 0, 48+len(ch.Title))
	content = append(content, encElement(idChapterUID, uid)...)
	content = append(content, uintElement(idChapTimeStart, uint64(chapNanos(ch.Start)))...)
	// Only a closed chapter writes ChapterTimeEnd.
	if ch.End > ch.Start {
		content = append(content, uintElement(idChapTimeEnd, uint64(chapNanos(ch.End)))...)
	}
	// Emit a flag only for its non-default state: ChapterFlagHidden defaults to 0 (emit 1
	// only when Hidden), ChapterFlagEnabled defaults to 1 (emit 0 only when Disabled).
	if ch.Hidden {
		content = append(content, uintElement(idChapFlagHidden, 1)...)
	}
	if ch.Disabled {
		content = append(content, uintElement(idChapFlagEnabled, 0)...)
	}
	// Emit a ChapterDisplay when the chapter has a title OR a modeled language to carry.
	if ch.Title != "" || ch.Language != "" || ch.LanguageIETF != "" {
		disp := stringElement(idChapString, ch.Title) // mandatory; an empty string is allowed
		// ChapLanguage is mandatory: write the spec default "und" when none is modeled,
		// which a re-parse normalizes back to "".
		lang := ch.Language
		if lang == "" {
			lang = "und"
		}
		disp = append(disp, stringElement(idChapLang, lang)...)
		// ChapLanguageIETF only when set. mkvmerge writes it, so dropping it would make a
		// re-render lossy and trip the flatten warning.
		if ch.LanguageIETF != "" {
			disp = append(disp, stringElement(idChapLangIETF, ch.LanguageIETF)...)
		}
		content = append(content, encElement(idChapDisplay, disp)...)
	}
	return encElement(idChapterAtom, content)
}

// chapNanos returns a chapter offset in nanoseconds, flooring a negative value to
// zero (a Duration is already nanoseconds, so no scaling is needed).
func chapNanos(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d)
}
