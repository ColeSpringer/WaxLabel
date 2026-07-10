package id3

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"slices"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// MaxChapters is the most chapters an ID3 tag can carry because the CTOC entry count is a
// single byte. The editor enforces the same value through Capabilities.Chapters.MaxItems.
const MaxChapters = 255

// CheckChapterCount rejects a chapter list past the CTOC's single-byte count, so a write
// never wraps the count field in encodeCTOC.
func CheckChapterCount(chapters []core.Chapter) error {
	if len(chapters) > MaxChapters {
		return fmt.Errorf("%w: %d chapters exceeds the %d an ID3 CTOC can store",
			waxerr.ErrUnsupportedTag, len(chapters), MaxChapters)
	}
	return nil
}

// ID3v2 chapters are reference-based. CHAP and CTOC are top-level frames: CHAP stores a
// time span and optional subframes, while CTOC stores child element IDs. This codec
// decodes each CHAP one subframe level deep for its TIT2 title and uses the selected CTOC
// only for ordering. It does not recurse through child references, so malformed or hostile
// tags cannot create deep traversal.
//
// Reimplemented from the ID3v2 Chapter Frame Addendum; reference implementations were
// consulted for design only.

const (
	// chapFieldUnused is the 0xFFFFFFFF sentinel the spec defines for a CHAP byte-offset
	// field that is "not used". We always write it for both offset fields (we navigate by
	// time, not byte offset) and reuse it as the "no explicit end time" marker for the end
	// time field, so an open-ended chapter (End == 0) round-trips.
	chapFieldUnused uint32 = 0xFFFFFFFF
	// chapTimeMax is the largest chapter time we store: one below the unused sentinel, so a
	// clamped overflow never collides with "no end time".
	chapTimeMax uint32 = 0xFFFFFFFE
)

// CTOC flag bits (the flags byte is %000000ab: a = top-level, b = ordered).
const (
	ctocOrdered  byte = 0x01
	ctocTopLevel byte = 0x02
)

// Element IDs generated on write. The spec treats them as opaque; these names follow the
// common "toc" plus "chpN" convention.
const (
	ctocElementID     = "toc"
	chapElementPrefix = "chp"
)

// maxChapterSubframes caps how many subframes one CHAP is parsed for - defense-in-depth
// against a hostile frame, since in practice a CHAP carries just a TIT2.
const maxChapterSubframes = 16

// ProjectChapters decodes a tag's CHAP/CTOC frames into a start-ordered, flat chapter list
// plus any read warnings (a flattened nested table of contents). It returns nil chapters
// when the tag carries none. Chapters are stable-sorted by start time, so a source that stored
// CHAP/CTOC out of start order still projects in time order, making a load->store round-trip a
// no-op (mirrors the Vorbis and Matroska projectors). The CTOC/CHAP order built below only breaks
// ties between equal-start chapters: the top-level CTOC's child element-ID list when present,
// falling back to the first CTOC, then to the CHAP frames' file order; a CHAP not referenced by
// the chosen CTOC is appended in file order so no chapter is lost.
func ProjectChapters(t *Tag) ([]core.Chapter, []core.Warning) {
	if t == nil {
		return nil, nil
	}
	major := t.srcVersion
	if major < 3 {
		major = 3 // CHAP/CTOC are v2.3+; their subframes use the 10-byte header geometry
	}
	// Keep every CHAP in file order; do not key by element ID, which would collapse
	// duplicate or empty IDs (multiple CHAP frames sharing an ID, or several with none)
	// into one chapter and lose the rest. CTOC ordering is resolved against this slice
	// below using a parallel emitted[] marker, so each TOC reference consumes one distinct
	// CHAP rather than overwriting a map entry.
	var chaps []decodedCHAP
	var tocs []ctocFrame
	for _, f := range t.frames {
		// A compressed or encrypted frame body is uninterpretable here. Preserve opaque
		// frames through the rebuild path, but do not project them as chapters.
		if f.Opaque {
			continue
		}
		switch f.ID {
		case "CHAP":
			if id, ch, ok := decodeCHAP(f.Body, major); ok {
				chaps = append(chaps, decodedCHAP{id: id, ch: ch})
			}
		case "CTOC":
			if c, ok := decodeCTOC(f.Body); ok {
				tocs = append(tocs, c)
			}
		}
	}
	if len(chaps) == 0 {
		return nil, nil
	}

	// Index the CHAP positions by element ID so the TOC walk consumes them in O(1) rather than
	// rescanning the whole slice per child (a crafted tag can hold many CHAP frames up to the
	// element cap). byID[id] is the file-order queue of not-yet-placed CHAP indices for that ID;
	// taken[i] marks chaps[i] as placed. The chosen TOC orders the list: for each child
	// element-ID, take the first un-placed CHAP with that ID. Any CHAP the TOC did not reference
	// (or all of them when there is no TOC) is appended in file order, so no chapter is lost -
	// including duplicate and empty IDs, which each occupy their own slot.
	byID := make(map[string][]int, len(chaps))
	for i, c := range chaps {
		byID[c.id] = append(byID[c.id], i)
	}
	taken := make([]bool, len(chaps))
	ordered := make([]core.Chapter, 0, len(chaps))
	if toc := pickTOC(tocs); toc != nil {
		for _, child := range toc.children {
			q := byID[child]
			if len(q) == 0 {
				continue
			}
			idx := q[0]
			byID[child] = q[1:] // pop the first un-placed CHAP with this ID
			taken[idx] = true
			ordered = append(ordered, chaps[idx].ch)
		}
	}
	for i := range chaps {
		if !taken[i] {
			ordered = append(ordered, chaps[i].ch)
		}
	}
	// Stable-sort by start so an out-of-order source projects in time order and a load->store
	// round-trip is a no-op; the CTOC/file order built above breaks ties for equal-start chapters
	// deterministically. Mirrors internal/vorbis/chapters.go and internal/matroska/sortChapters.
	// Only the projected view changes - a tag-only edit still preserves the on-disk frame order.
	slices.SortStableFunc(ordered, func(a, b core.Chapter) int { return cmp.Compare(a.Start, b.Start) })

	var ws []core.Warning
	if len(tocs) > 1 {
		// More than one CTOC means a nested table-of-contents hierarchy; the flat chapter
		// model keeps a single ordered list, so the nesting is dropped.
		ws = core.Warn(ws, core.WarnChaptersFlattened,
			"ID3 table of contents has a nested hierarchy; chapters were flattened to a single ordered list")
	}
	return ordered, ws
}

// decodedCHAP is one CHAP frame's element ID and projected chapter, kept in file order so
// duplicate or empty element IDs each retain a distinct slot (see ProjectChapters).
type decodedCHAP struct {
	id string
	ch core.Chapter
}

// ctocFrame is a decoded CTOC: its element ID, whether it is the top-level table of
// contents, and the ordered child element-ID list.
type ctocFrame struct {
	id       string
	topLevel bool
	children []string
}

// pickTOC selects the table of contents to order by: the first top-level CTOC, else the
// first CTOC, else nil (no CTOC - order by CHAP file order).
func pickTOC(tocs []ctocFrame) *ctocFrame {
	for i := range tocs {
		if tocs[i].topLevel {
			return &tocs[i]
		}
	}
	if len(tocs) > 0 {
		return &tocs[0]
	}
	return nil
}

// decodeCHAP decodes a CHAP frame body into its element ID and a chapter. Layout:
// element-id (NUL-terminated Latin-1), start ms (uint32 BE), end ms (uint32 BE), start
// byte offset (uint32 BE), end byte offset (uint32 BE), then optional subframes. The byte
// offsets are ignored (we navigate by time); the title comes from a TIT2 subframe parsed
// one level deep. An end of chapFieldUnused (or 0) means "no explicit end".
//
// A start of chapFieldUnused (0xFFFFFFFF) is the spec's "time not used" sentinel for a
// chapter located purely by byte offset. Since this decoder navigates by time and ignores
// the byte offsets, such a chapter has no usable position and is reported unrepresentable
// (ok == false) rather than projected at a bogus ~49.7-day timestamp; it is preserved
// verbatim on an unrelated edit.
func decodeCHAP(body []byte, major byte) (string, core.Chapter, bool) {
	id, rest, ok := cutLatin1(body)
	if !ok || len(rest) < 16 {
		return "", core.Chapter{}, false
	}
	startMs := binary.BigEndian.Uint32(rest[0:4])
	if startMs == chapFieldUnused {
		return "", core.Chapter{}, false
	}
	endMs := binary.BigEndian.Uint32(rest[4:8])
	ch := core.Chapter{Start: msToDuration(startMs)}
	if endMs != chapFieldUnused {
		ch.End = msToDuration(endMs)
	}
	if subs, err := parseFrames(rest[16:], major, false, maxChapterSubframes); err == nil {
		for _, sf := range subs {
			if sf.ID == "TIT2" {
				if vals := decodeTextFrame(sf.Body); len(vals) > 0 {
					ch.Title = vals[0]
				}
				break
			}
		}
	}
	return id, ch, true
}

// chapHasExtraSubframes reports whether a CHAP body carries any subframe other than the
// TIT2 title, such as a per-chapter image or URL that the flat chapter model cannot hold.
func chapHasExtraSubframes(body []byte, major byte) bool {
	_, rest, ok := cutLatin1(body)
	if !ok || len(rest) < 16 {
		return false
	}
	subs, err := parseFrames(rest[16:], major, false, maxChapterSubframes)
	if err != nil {
		return false
	}
	for _, sf := range subs {
		if sf.ID != "TIT2" {
			return true
		}
	}
	return false
}

// decodeCTOC decodes a CTOC frame body: element-id (NUL-terminated Latin-1), flags (1),
// entry count (1), then that many child element-ID strings (each NUL-terminated Latin-1).
// Trailing subframes (an optional TOC title) are ignored. Children are read flat, never
// followed, so no recursion through child references occurs.
func decodeCTOC(body []byte) (ctocFrame, bool) {
	id, rest, ok := cutLatin1(body)
	if !ok || len(rest) < 2 {
		return ctocFrame{}, false
	}
	flags := rest[0]
	count := int(rest[1])
	rest = rest[2:]
	c := ctocFrame{id: id, topLevel: flags&ctocTopLevel != 0}
	for i := 0; i < count; i++ {
		child, r, cok := cutLatin1(rest)
		if !cok {
			break // truncated child list; keep what parsed
		}
		c.children = append(c.children, child)
		rest = r
	}
	return c, true
}

// chapterFrames renders a chapter list as the CHAP frames (one per chapter, in order)
// followed by a single ordered top-level CTOC referencing them. It reports whether any
// chapter time was clamped to the 32-bit millisecond field. Emitting the CHAP frames in
// chapter order means a reader that ignores the CTOC still reads them correctly.
//
// It first materializes concrete ends for open-ended chapters (End == 0), so a
// spec-conforming reader (ffprobe, players) sees bounded chapters instead of the 0xFFFFFFFF
// "unused" sentinel encodeCHAP would otherwise emit (~49.7 days). This fill is ID3-local: the
// canonical core.Chapter{End:0} "open" model is unchanged and MP4/Matroska keep omitting or
// inferring ends as before. The fill runs on a clone, so the caller's chapter slice is not
// mutated. Two separate rules apply:
//   - Interior open chapter -> the next chapter's start (a gapless interval) via the shared
//     core.FillInteriorEnds, so the ID3 writer and the MP4 read/write paths cannot drift on it.
//   - Trailing open chapter -> a bounded end, whenever the duration is known (> 0): the media
//     duration when it is past the last start, or a zero-length end (End = Start) when the last
//     start is at or past the ms-floored duration, so a past/at-duration trailing chapter
//     serializes endMs == startMs instead of the sentinel. This is genuinely ID3-local
//     (core.FillInteriorEnds leaves the last chapter open; MP4 derives a bounded last end from the
//     QuickTime text track's last-sample duration, not from Chapter.End; and Matroska's
//     renderChapterAtom still treats End == Start as open, a divergence that only affects
//     deliberately zero-length library-authored chapters and is accepted to keep this fix scoped
//     to ID3). When the duration is unknown (0) the trailing chapter stays open and encodeCHAP
//     emits the sentinel - no worse than before.
//
// Precondition: len(chs) <= 255. The CTOC entry count is a single byte (see encodeCTOC), so
// a longer list would wrap it. Callers must enforce MaxChapters before writing.
func chapterFrames(chs []core.Chapter, duration time.Duration, version byte) (frames []Frame, overflow bool) {
	filled := core.CloneChapters(chs)
	// Interior open ends -> the next chapter's start (gapless), shared with the MP4 paths so the
	// rule cannot drift. It leaves the last chapter open. FillInteriorEnds gates on a strictly
	// greater next start, so two chapters sharing a start (a degenerate case already surfaced by a
	// duplicate-chapter warning) leave the interior one open rather than giving it End == Start, an
	// empty interval. That is why coincident-start chapters serialize with asymmetric ends: the
	// interior one stays open (encodeCHAP's 0xFFFFFFFF sentinel) while the trailing fill below
	// bounds only the last chapter. No data is lost, and the duplicate start is already warned, so
	// this is left as-is rather than special-cased in cross-format shared code.
	core.FillInteriorEnds(filled)
	// Trailing open chapter -> a bounded end (ID3-local), whenever the duration is known (> 0).
	// A duration past the last start fills a run-to-EOF end (End = duration); a duration at or
	// before the last start (a chapter authored past the ms-floored media duration) fills a
	// bounded zero-length end (End = Start) rather than leaving it open, so encodeCHAP serializes
	// endMs == startMs instead of the 0xFFFFFFFF sentinel a player renders as ~49.7 days. Using
	// max(duration, start) keeps the end from running backwards. When the duration is unknown (0)
	// the chapter stays open and encodeCHAP emits the sentinel - no worse than before.
	if n := len(filled); n > 0 && filled[n-1].End == 0 && duration > 0 {
		filled[n-1].End = max(duration, filled[n-1].Start)
	}
	childIDs := make([]string, len(filled))
	for i, ch := range filled {
		id := fmt.Sprintf("%s%d", chapElementPrefix, i)
		childIDs[i] = id
		body, ov := encodeCHAP(id, ch, version)
		overflow = overflow || ov
		frames = append(frames, Frame{ID: "CHAP", Body: body})
	}
	frames = append(frames, Frame{ID: "CTOC", Body: encodeCTOC(ctocElementID, childIDs)})
	return frames, overflow
}

// encodeCHAP renders a CHAP frame body for the write version. The TIT2 subframe is
// emitted only for a non-empty title, via renderFrame so the subframe is serialized with
// the enclosing tag's version geometry. Byte-offset fields are always the unused sentinel.
func encodeCHAP(id string, ch core.Chapter, version byte) ([]byte, bool) {
	startMs, ov1 := durationToMs(ch.Start, chapTimeMax)
	endMs, ov2 := chapFieldUnused, false
	// A closed chapter writes an explicit end, including a bounded zero-length end (End == Start,
	// nonzero) so a past/at-duration trailing chapter serializes endMs == startMs rather than the
	// ~49.7-day sentinel. A backwards end (End < Start) or an unset one (End == 0) is treated as
	// "open" and emits the unused sentinel - serializing it would only write an invalid interval.
	// This bounds a zero-length end everywhere except at t=0: a chapter authored with
	// Start == End == 0 cannot be distinguished from an unset end, so it stays open (the one
	// documented corner of the bounded-zero-length behavior).
	if ch.End >= ch.Start && ch.End > 0 {
		endMs, ov2 = durationToMs(ch.End, chapTimeMax)
	}
	out := append(encodeLatin1(id), 0)
	var b [16]byte
	binary.BigEndian.PutUint32(b[0:4], startMs)
	binary.BigEndian.PutUint32(b[4:8], endMs)
	binary.BigEndian.PutUint32(b[8:12], chapFieldUnused)
	binary.BigEndian.PutUint32(b[12:16], chapFieldUnused)
	out = append(out, b[:]...)
	if ch.Title != "" {
		enc := chooseEncoding(version, []string{ch.Title})
		out = append(out, renderFrame(version, Frame{ID: "TIT2", Body: encodeTextFrame(enc, []string{ch.Title})})...)
	}
	return out, ov1 || ov2
}

// encodeCTOC renders a CTOC frame body: the element ID, a top-level+ordered flags byte
// (0x03), the child count, and each child element ID NUL-terminated. The count is a single
// byte, so the caller must pass at most MaxChapters child IDs.
func encodeCTOC(id string, childIDs []string) []byte {
	out := append(encodeLatin1(id), 0)
	out = append(out, ctocTopLevel|ctocOrdered, byte(len(childIDs)))
	for _, c := range childIDs {
		out = append(out, encodeLatin1(c)...)
		out = append(out, 0)
	}
	return out
}

// durationToMs converts a chapter or lyric offset to uint32 milliseconds, clamps
// values past maxMs, and reports the clamp. CHAP passes chapTimeMax, one below
// the reserved "field unused" sentinel; SYLT has no sentinel and uses the full
// uint32 range.
func durationToMs(d time.Duration, maxMs uint32) (uint32, bool) {
	if d < 0 {
		d = 0
	}
	ms := int64(d / time.Millisecond)
	if ms > int64(maxMs) {
		return maxMs, true
	}
	return uint32(ms), false
}

// msToDuration widens a uint32 millisecond field to a duration. The product cannot
// overflow int64 (max ~4.3e9 ms * 1e6 ns/ms is well within range).
func msToDuration(ms uint32) time.Duration { return time.Duration(ms) * time.Millisecond }
