package matroska

import (
	"cmp"
	"math"
	"slices"
	"time"

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
// (unlike Cluster timestamps) - plus a ChapterDisplay > ChapString title. The
// ChapterUIDs are preserved across an edit so chapter-scoped SimpleTags that
// reference them stay valid, and every non-default edition is preserved verbatim.
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
// re-rendered so its UID and flags survive; uids are the top-level ChapterAtom
// UIDs in order, reused by position so chapter-scoped tags keep resolving across
// an edit.
type chapterEdition struct {
	raw    []byte
	prefix []byte
	hasCRC bool
	uids   []uint64
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
	// SetChapters stable-sorts by start; ordering the projection (and its UIDs) the
	// same way makes SetChapters(doc.Chapters()...) a true no-op even for a source
	// whose atoms were stored out of start order, and keeps each chapter aligned
	// with its own ChapterUID on re-render.
	sortChaptersWithUIDs(defChapters, cd.editions[cd.defIdx].uids)
	return defChapters, nil
}

// sortChaptersWithUIDs stably orders chs by start time, keeping each chapter's
// ChapterUID (uids, parallel by index) aligned.
func sortChaptersWithUIDs(chs []core.Chapter, uids []uint64) {
	type entry struct {
		ch  core.Chapter
		uid uint64
	}
	es := make([]entry, len(chs))
	for i := range chs {
		var uid uint64
		if i < len(uids) {
			uid = uids[i]
		}
		es[i] = entry{chs[i], uid}
	}
	slices.SortStableFunc(es, func(a, b entry) int { return cmp.Compare(a.ch.Start, b.ch.Start) })
	for i, e := range es {
		chs[i] = e.ch
		if i < len(uids) {
			uids[i] = e.uid
		}
	}
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
			out.uids = append(out.uids, uid)
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

// parseChapterAtom reads one ChapterAtom's UID, start/end (absolute nanoseconds),
// and the first ChapterDisplay title. Only the top-level atom and its first
// display are projected; lossy reports a nested sub-atom or a second display
// (other-language title) - structure the flat []core.Chapter model cannot hold
// and a re-render would drop.
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
		case idChapDisplay:
			displays++
			if displays > 1 {
				return nil // a second display is an other-language title we cannot carry
			}
			title, err := readChapterTitle(src, el, depth, limit)
			if err != nil {
				return err
			}
			ch.Title = title
		case idChapterAtom:
			lossy = true // a nested sub-chapter
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

// readChapterTitle returns the first ChapString within a ChapterDisplay.
func readChapterTitle(src core.ReaderAtSized, disp element, depth *bits.Depth, limit int64) (string, error) {
	var title string
	got := false
	err := eachChild(src, disp.dataStart, disp.dataEnd, depth, limit, func(el element) error {
		if el.id == idChapString && !got {
			s, err := readString(src, el, limit)
			if err != nil {
				return err
			}
			title, got = s, true
		}
		return nil
	})
	return title, err
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
// keeping its preserved prefix (EditionUID/flags) and reusing each chapter's
// original ChapterUID by position. It returns nil for an empty chapter list,
// since an EditionEntry requires at least one ChapterAtom.
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
	for i, ch := range chs {
		uid := uint64(0)
		if i < len(ed.uids) {
			uid = ed.uids[i]
		}
		if uid == 0 {
			uid = randomUID()
		}
		content = append(content, renderChapterAtom(uid, ch)...)
	}
	return masterElement(idEditionEntry, content, ed.hasCRC)
}

// renderChapterAtom encodes one ChapterAtom: its UID, the absolute-nanosecond
// start (and end when the chapter is closed), and a ChapterDisplay title when set.
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
	if ch.Title != "" {
		disp := stringElement(idChapString, ch.Title)
		disp = append(disp, stringElement(idChapLang, "und")...)
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
