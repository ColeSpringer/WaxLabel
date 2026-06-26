package matroska

import (
	"cmp"
	"math"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// Matroska chapters live in a Chapters element, a small tree:
//
//	Chapters > EditionEntry > ChapterAtom
//
// Each EditionEntry is one independent chapter set; the edition flagged default
// (or the first, when none is flagged) projects into core.Media.Chapters. A
// ChapterAtom carries ChapterTimeStart and an optional ChapterTimeEnd - absolute
// nanoseconds by spec, with the segment TimestampScale deliberately *not* applied
// (unlike Cluster timestamps) - plus a ChapterDisplay > ChapString title. A surviving
// chapter keeps its ChapterUID by matching the saved UID on start time, which preserves
// chapter-scoped SimpleTags through inserts, deletes, and renames. If an edit changes
// a chapter's start time, core.Chapter has no UID field to track it, so the writer must
// mint a fresh UID. Every non-default edition is preserved verbatim.
//
// Like Tags, the full parsed tree is retained on the native doc: when chapters
// are not edited the whole Chapters element is copied byte-for-byte (nested
// sub-atoms, all editions, languages intact); only a SetChapters/ClearChapters
// edit re-renders the default edition from the flat []core.Chapter.
//
// Reimplemented from the Matroska specification (RFC 9559); nothing is copied.

// chapterDoc is the parsed Chapters element retained on the native doc for the
// dump view, verbatim preservation, and re-rendering on a chapter edit.
type chapterDoc struct {
	hasCRC   bool // the Chapters master led with a CRC-32
	editions []chapterEdition
	defIdx   int  // index of the default edition, or -1 when there are none
	defLossy bool // the default edition carries nested atoms or multi-language
	// displays that a flat re-render would drop (drives WarnChaptersFlattened).
}

// chapterEdition is one parsed EditionEntry. raw is the whole element, copied
// verbatim for a non-default edition (the default edition is re-rendered, so its
// raw is dropped at parse); prefix is its non-atom leading children (EditionUID,
// the edition flags) with any CRC stripped, reused when the default edition is
// re-rendered so its UID and flags survive; startUIDs records each top-level atom's
// start time and ChapterUID in file order. Re-rendering uses those pairs to keep a
// surviving chapter's UID by start time rather than by list position, since inserts
// and deletes shift positions but leave surviving starts unchanged.
type chapterEdition struct {
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

// parseChapters reads a Chapters element into the native chapterDoc and returns
// the default edition projected as []core.Chapter (nil when there are no
// editions), stably ordered by start time. It records every edition so a later
// edit can preserve the non-default ones verbatim and reuse the default edition's
// UIDs; only the default edition's projection is retained (the others need just
// their raw bytes).
func parseChapters(src core.ReaderAtSized, chapters element, depth *bits.Depth, limit int64, d *doc) ([]core.Chapter, error) {
	cd := &chapterDoc{defIdx: -1, hasCRC: firstChildIsCRC(src, chapters, limit)}
	var defChapters []core.Chapter
	err := eachChild(src, chapters.dataStart, chapters.dataEnd, depth, limit, func(el element) error {
		if el.id != idEditionEntry {
			return nil
		}
		ed, isDefault, chs, lossy, err := parseEdition(src, el, depth, limit)
		if err != nil {
			return err
		}
		idx := len(cd.editions)
		cd.editions = append(cd.editions, ed)
		// The default edition (the first flagged one, else the first) supplies the
		// projection; idx == 0 is the tentative fallback until a flag appears.
		if (isDefault && cd.defIdx < 0) || (cd.defIdx < 0 && idx == 0) {
			if isDefault {
				cd.defIdx = idx
			}
			defChapters, cd.defLossy = chs, lossy
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.chapters = cd
	if len(cd.editions) == 0 {
		return nil, nil
	}
	if cd.defIdx < 0 {
		cd.defIdx = 0 // no edition flagged default: the first one is the default
	}
	// The default edition is re-rendered on an edit, never copied verbatim, so its
	// captured bytes are dead weight on the retained document - drop them.
	cd.editions[cd.defIdx].raw = nil
	// SetChapters stable-sorts by start; ordering the projection the same way makes
	// SetChapters(doc.Chapters()...) a no-op even when the source stored atoms out of
	// start order. UIDs are reassigned by start time during re-render, so no parallel
	// UID slice needs to be sorted with the projected chapters.
	sortChapters(defChapters)
	return defChapters, nil
}

// sortChapters stably orders chs by start time. Stability keeps same-start atoms in
// file order, which is the order the UID queue consumes them.
func sortChapters(chs []core.Chapter) {
	slices.SortStableFunc(chs, func(a, b core.Chapter) int { return cmp.Compare(a.Start, b.Start) })
}

// parseEdition reads one EditionEntry: whether it is the default edition, the
// non-atom children kept verbatim in prefix (EditionUID and the edition flags),
// the ordered ChapterAtom UIDs, and the atoms projected as chapters. lossy reports
// whether the edition carries nested sub-atoms or multi-language displays that a
// flat re-render would drop.
func parseEdition(src core.ReaderAtSized, ed element, depth *bits.Depth, limit int64) (out chapterEdition, isDefault bool, chs []core.Chapter, lossy bool, err error) {
	out = chapterEdition{raw: captureRaw(src, ed, limit), hasCRC: firstChildIsCRC(src, ed, limit)}
	err = eachChild(src, ed.dataStart, ed.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idCRC32:
			// Stripped from the prefix; a re-render recomputes its own CRC.
		case idChapterAtom:
			ch, uid, atomLossy, err := parseChapterAtom(src, el, depth, limit)
			if err != nil {
				return err
			}
			// Keep one entry per atom in file order, including a UID of 0. A popped zero
			// means "mint fresh"; for same-start atoms it also keeps later real UIDs from
			// sliding onto earlier UID-less chapters.
			out.startUIDs = append(out.startUIDs, chapterStartUID{ch.Start, uid})
			chs = append(chs, ch)
			lossy = lossy || atomLossy
		case idEditionFlagDf:
			if readUint(src, el, limit) != 0 {
				isDefault = true
			}
			out.prefix = append(out.prefix, captureRaw(src, el, limit)...)
		default:
			// EditionUID and any other non-atom child: kept verbatim in the prefix so
			// edition-scoped tags and unmodeled fields survive a chapter edit.
			out.prefix = append(out.prefix, captureRaw(src, el, limit)...)
		}
		return nil
	})
	return out, isDefault, chs, lossy, err
}

// parseChapterAtom reads one ChapterAtom's UID, start/end (absolute nanoseconds), the
// hidden/enabled flags, and the first ChapterDisplay (title + language). Only the top-level
// atom and its first display are projected; lossy reports a nested sub-atom, a second
// display (other-language title), or any other unmodeled child - structure the flat
// []core.Chapter model cannot hold and a re-render would drop.
func parseChapterAtom(src core.ReaderAtSized, atom element, depth *bits.Depth, limit int64) (ch core.Chapter, uid uint64, lossy bool, err error) {
	var startNs, endNs int64
	displays := 0
	err = eachChild(src, atom.dataStart, atom.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idChapterUID:
			uid = readUint(src, el, limit)
		case idChapTimeStart:
			startNs = clampNs(readUint(src, el, limit))
		case idChapTimeEnd:
			endNs = clampNs(readUint(src, el, limit))
		case idChapFlagHidden:
			ch.Hidden = readUint(src, el, limit) != 0 // EBML default 0; 1 = hidden
		case idChapFlagEnabled:
			ch.Disabled = readUint(src, el, limit) == 0 // EBML default 1; 0 = disabled
		case idChapDisplay:
			displays++
			if displays > 1 {
				return nil // a second display is an other-language title we cannot carry
			}
			title, lang, langIETF, dispLossy, err := readChapterDisplay(src, el, depth, limit)
			if err != nil {
				return err
			}
			ch.Title, ch.Language, ch.LanguageIETF = title, lang, langIETF
			lossy = lossy || dispLossy
		case idChapterAtom:
			lossy = true // a nested sub-chapter
		case idCRC32, idVoid:
			// Structural framing, not chapter data: a re-render recomputes its own CRC and
			// drops padding, so neither is a content loss.
		default:
			// Any other unmodeled ChapterAtom child (ChapterSegmentUID, ChapProcess,
			// ChapterTrack, ...) the flat model cannot carry: flag it lossy so the flatten
			// warning covers the whole silent-loss class, not just nested atoms and
			// multi-language displays.
			lossy = true
		}
		return nil
	})
	lossy = lossy || displays > 1
	ch.Start = time.Duration(startNs)
	if endNs > startNs {
		ch.End = time.Duration(endNs) // an end <= start (or absent) means "open"
	}
	return ch, uid, lossy, err
}

// readChapterDisplay reads one ChapterDisplay: the first ChapString title, the
// ChapLanguage (ISO-639-2), and the ChapLanguageIETF (BCP-47). An absent or "und"
// ChapLanguage normalizes to "" so a freshly written chapter carries no spurious
// language. lossy reports an unmodeled ChapDisplay child (e.g. ChapCountry) the flat
// model cannot hold, so the flatten warning covers it.
func readChapterDisplay(src core.ReaderAtSized, disp element, depth *bits.Depth, limit int64) (title, lang, langIETF string, lossy bool, err error) {
	got := false
	err = eachChild(src, disp.dataStart, disp.dataEnd, depth, limit, func(el element) error {
		switch el.id {
		case idChapString:
			if got {
				lossy = true // a second ChapString in one display: the flat model keeps only the first
				return nil
			}
			s, err := readString(src, el, limit)
			if err != nil {
				return err
			}
			// Sanitize an invalid-UTF-8 title to "" (matching MP4's chpl and the QuickTime
			// chapter track) so a later --json dump cannot emit raw invalid bytes and every
			// chapter source behaves the same on read. Prepare separately rejects an
			// invalid-UTF-8 title on write, so a value read back here is always valid.
			if !utf8.ValidString(s) {
				s = ""
			}
			title, got = s, true
		case idChapLang:
			lang, _ = readString(src, el, limit) // informational; degrade gracefully
		case idChapLangIETF:
			langIETF, _ = readString(src, el, limit)
		case idCRC32, idVoid:
			// Structural framing, not display content; never a loss.
		default:
			lossy = true // an unmodeled ChapDisplay child (ChapCountry, ...) the flat model drops
		}
		return nil
	})
	// Drop a language WaxLabel should not surface or re-emit: invalid UTF-8 (sanitized like
	// the title, so no raw bytes reach --json/copy) or the "und"/absent default (which carries
	// no information and would print a spurious "[lang: und]"). Both ChapLanguage and the IETF
	// tag default to "und" on modern mkvmerge output, so both are normalized the same way.
	lang = normalizeChapLang(lang)
	langIETF = normalizeChapLang(langIETF)
	return title, lang, langIETF, lossy, err
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

// chaptersFromRaw parses a standalone Chapters element's bytes back into a
// chapterDoc and its projected chapters, so buildResult derives the post-write
// chapter view from the rendered bytes (the seekFromRaw/infoFromRaw pattern) - for
// every realistic edit this makes the returned Document equal a fresh parse.
//
// It returns nil on a parse failure, like its seek/cues/info siblings. Because it
// re-reads bytes this package just encoded, the only reachable failure is the
// reader's own alloc cap - a chapter title past maxElement (64 MiB), far beyond any
// real title and not a Matroska limit but ours. In that degenerate case the result
// view degrades to "no chapters" rather than crashing; the written bytes are still
// valid EBML another reader accepts, so propagating the error to refuse the write
// would wrongly impose our read cap on the write path.
func chaptersFromRaw(raw []byte, depth *bits.Depth, limit int64) (*chapterDoc, []core.Chapter) {
	rs := core.BytesSource(raw)
	root, ok := readElement(rs, 0, int64(len(raw)), limit)
	if !ok || root.id != idChapters {
		return nil, nil
	}
	var d doc
	chs, err := parseChapters(rs, root, depth, limit, &d)
	if err != nil {
		return nil, nil
	}
	return d.chapters, chs
}

// renderChapters builds the new Chapters element bytes from the edited chapters,
// re-rendering the default edition and preserving every other edition verbatim.
// It returns nil to drop the Chapters element entirely (a clear that empties the
// only edition), which the writer turns into a removal.
func renderChapters(d *doc, chs []core.Chapter) []byte {
	// Clearing chapters (an empty list) removes them entirely. The flat model cannot
	// keep a hidden non-default edition without it surfacing as the default on
	// reparse, so "no chapters" drops the whole Chapters element - every edition -
	// rather than silently promoting a previously-invisible edition into view.
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
// chapter its original ChapterUID by start time. Position-based reuse is unsafe: an
// insert, delete, or reorder can move a surviving chapter to a different list index
// and break chapter-scoped tags. A chapter whose start matches no saved UID is minted
// a fresh UID. It returns nil for an empty chapter list, since an EditionEntry requires
// at least one ChapterAtom.
func renderDefaultEdition(ed chapterEdition, chs []core.Chapter) []byte {
	if len(chs) == 0 {
		return nil
	}
	// Size content up front: the prefix plus a per-chapter estimate (UID + start +
	// end + display framing ~ 48 bytes, plus the title) so the accumulator does not
	// repeatedly grow.
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
		uid := popChapterUID(queue, ch.Start)
		if uid == 0 {
			uid = randomUID()
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

// renderChapterAtom encodes one ChapterAtom: its UID, the absolute-nanosecond
// start (and end when the chapter is closed), the non-default ChapterFlagHidden/
// ChapterFlagEnabled flags, and a ChapterDisplay (title + language) when titled.
func renderChapterAtom(uid uint64, ch core.Chapter) []byte {
	content := make([]byte, 0, 48+len(ch.Title))
	content = append(content, uintElement(idChapterUID, uid)...)
	content = append(content, uintElement(idChapTimeStart, uint64(chapNanos(ch.Start)))...)
	// Only a closed chapter writes ChapterTimeEnd. End <= Start (a zero-length or
	// backwards span) is treated as "open", symmetric with the read path's
	// endNs > startNs guard - writing such an end would only emit a value the reader
	// then ignores.
	if ch.End > ch.Start {
		content = append(content, uintElement(idChapTimeEnd, uint64(chapNanos(ch.End)))...)
	}
	// Emit a flag only for its non-default state: ChapterFlagHidden defaults to 0 (emit
	// 1 only when Hidden), ChapterFlagEnabled defaults to 1 (emit 0 only when Disabled).
	// A zero-value Chapter (a CLI --add-chapter) writes neither and reads back visible
	// and enabled, exactly as before these fields existed.
	if ch.Hidden {
		content = append(content, uintElement(idChapFlagHidden, 1)...)
	}
	if ch.Disabled {
		content = append(content, uintElement(idChapFlagEnabled, 0)...)
	}
	// Emit a ChapterDisplay when the chapter has a title OR a modeled language to carry. A
	// title-less chapter still needs a display (with an empty, spec-mandatory ChapString) to
	// preserve a language - the case an invalid-UTF-8 title sanitized to "" on read produces -
	// which a "title != ''" gate would silently drop on re-render. A chapter with neither (a
	// bare CLI --add-chapter) writes no display, exactly as before.
	if ch.Title != "" || ch.Language != "" || ch.LanguageIETF != "" {
		disp := stringElement(idChapString, ch.Title) // mandatory; an empty string is allowed
		// ChapLanguage is mandatory; fall back to the spec "und" default when no language
		// was modeled, which a re-parse normalizes back to "" (so the round-trip is stable).
		lang := ch.Language
		if lang == "" {
			lang = "und"
		}
		disp = append(disp, stringElement(idChapLang, lang)...)
		// ChapLanguageIETF only when set - modern mkvmerge writes it, so preserving it keeps
		// a real file's chapters from re-rendering lossily and tripping the flatten warning.
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
