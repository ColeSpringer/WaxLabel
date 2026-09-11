package mp4

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/internal/mpeg4audio"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxMetaChunk bounds how large an ilst item or other structural atom this codec
// reads into memory. The mdat media payload is never read here - only its range
// is recorded - so this guards the small structural atoms (plus cover art, whose
// real size is well under the limit) against a hostile declared size. It works
// alongside the user's MaxAllocBytes limit (whichever is smaller wins).
const maxMetaChunk = 64 << 20

// parse reads an MP4 file's atom structure into a neutral Media: the iTunes tags
// from moov.udta.meta.ilst, the audio geometry from the sample tables, the
// chunk-offset tables and mdat ranges a preservation-first rewrite needs, and
// every top-level atom preserved as the rewrite base. A top-level moof (movie
// fragments) is recorded and warned rather than rejected: the initial movie box's
// tags read exactly, and only the rewrite is refused, at Plan. A moov declaring
// mvex without any fragment present is an ordinary progressive file and is read
// and written normally. A fragmented media segment - fragments with no movie box
// at all - is still rejected here.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	// Resolve an unset allocation limit (a zero-value ParseOptions) to the library default, so
	// every bounded read still has a real ceiling now that ReadSlice requires a positive limit.
	// The public Parse path already supplies DefaultLimits; this covers a direct-parse caller.
	limit := opts.Limits.MaxAllocBytes
	if limit <= 0 {
		limit = bits.DefaultLimits.MaxAllocBytes
	}

	depth := bits.NewDepth(opts.Limits.MaxDepth).WithElementCap(opts.Limits.MaxElements, "MP4 atoms")
	top, err := walkAtoms(src, 0, size, depth, limit, true)
	if err != nil {
		return nil, err
	}
	if len(top) == 0 {
		return nil, fmt.Errorf("%w: no MP4 atoms", waxerr.ErrInvalidData)
	}

	d := &doc{size: size}
	// A top-level atom whose declared size ran past EOF is clamped so the complete earlier
	// atoms still read. That is also what junk appended to a file looks like: the first four
	// bytes read as an enormous size and the remainder becomes one phantom atom. mdat and
	// moov get their own truncated-audio warnings below; every other name would otherwise be
	// absorbed in silence, which is the RIFF oversized-chunk condition under another
	// container.
	if last := top[len(top)-1]; last.truncated && last.id() != "mdat" && last.id() != "moov" {
		d.oversizedAtom = last.id()
	}
	var moov node
	haveMoov := false
	for _, a := range top {
		d.topLevel = append(d.topLevel, refOf(a))
		switch a.id() {
		case "ftyp":
			// The major brand (e.g. "M4B ") signals an audiobook; it is preserved
			// verbatim on write (ftyp is copied) and surfaced in the native view.
			if b, err := readPayloadPrefix(src, a, 4, limit); err == nil && len(b) >= 4 {
				d.majorBrand = string(b[:4])
			}
		case "moof":
			// Movie fragments. The file reads fine (the init moov's ilst carries the tags) but
			// is not rewritable: the stco/co64 fixups cannot reach a fragment's trun sample
			// offsets, so a metadata resize would desynchronize the media. Refused at Plan,
			// mirroring how the Ogg codec records a chained stream and refuses only the rewrite.
			d.fragmented = true
		case "mdat":
			d.mdats = append(d.mdats, [2]int64{a.payloadOff(), a.size - a.headerLen})
			if a.truncated {
				d.mdatTruncated = true
			}
		case "moov":
			moov, haveMoov = a, true
		}
	}
	if !haveMoov {
		// A fragmented media segment has no movie box by design, so it keeps the exit-3
		// classification the in-loop rejection used to give it rather than falling through to
		// the invalid-data "no moov box" diagnostic. This deliberately runs ahead of the
		// truncation branch below, inverting the precedence that branch argues for: a segment
		// has no trailing moov to overrun, so "an mdat overran a trailing moov" would name a
		// cause that cannot exist here. Truncation still wins for every non-segment shape.
		if d.fragmented || hasTopLevel(d, "styp") {
			return nil, fmt.Errorf("%w: fragmented MP4 media segment (no moov box)",
				waxerr.ErrUnsupportedFormat)
		}
		// A truncated final mdat that overruns its declared end swallows whatever
		// follows it (clamped to EOF). When a moov sits after such an mdat it is never
		// seen, so report the truncation - the real cause - rather than a misleading
		// "no moov box", independent of box order.
		if d.mdatTruncated {
			return nil, fmt.Errorf("%w: MP4 mdat atom declares more bytes than the file holds (truncated; a trailing moov was overrun)", waxerr.ErrInvalidData)
		}
		return nil, fmt.Errorf("%w: MP4 has no moov box", waxerr.ErrInvalidData)
	}
	d.moov = refPtr(moov)

	// A gap between where moov's children end and moov.end() corrupts a create-ilst edit the
	// same way the meta gap (below) does: with no udta present, buildCreated appends the new
	// udta/meta/ilst at moov.end() (write.go, the default branch), but a re-parse resolves
	// moov's children at their recorded ends (earlier), so the inserted tag path lands past a
	// stray all-zero remainder and misaligns. walkAtoms tolerates that remainder (the
	// udta-terminator rule), so it must be rejected here - the exact analogue of the meta guard,
	// sharing the trailingGap predicate. Scoped to a moov with no udta: with a udta the insert
	// targets udta.end() (guarded by udtaCleanLen) or meta.end() (guarded by the meta gap check),
	// so a valid file that zero-pads moov around a present udta still writes correctly and must not
	// be rejected. A childless truncated moov (gap == moov payload) is rejected too.
	if _, ok := moov.find("udta"); !ok {
		if gap := trailingGap(src, moov, limit); gap > 0 {
			// No "(truncated)" qualifier: the gap can come from a truncated download or a muxer's
			// structural pad, and moov.truncated is surfaced separately as a warning when the clamp
			// tiles cleanly. This matches the meta guard's wording.
			return nil, fmt.Errorf("%w: moov atom has %d unusable trailing byte(s)",
				waxerr.ErrInvalidData, gap)
		}
	}

	if err := collectOffsetTables(ctx, src, moov, d, limit); err != nil {
		return nil, err
	}
	d.hasIloc = hasIlocBox(top, moov)
	parseProperties(src, moov, d, limit)

	// Tag path: moov.udta.meta.ilst. Each level is optional; record what exists so
	// the writer can either rewrite the ilst or create the missing wrappers.
	var chplNode node
	haveChpl := false
	if udta, ok := moov.find("udta"); ok {
		d.udta = refPtr(udta)
		// Capture the udta payload verbatim so a chapter rewrite can splice the new
		// ilst/chpl byte ranges into it while preserving every other user-data atom.
		// The whole payload is read (bounded by the user's alloc limit, not the
		// smaller per-atom cap) so it is never silently truncated - a truncated
		// d.udtaRaw would splice against a delta computed from the full size. If it
		// exceeds the limit, d.udtaRaw stays nil and a chapter rewrite fails loudly.
		if udtaLen := udta.size - udta.headerLen; udtaLen >= 0 {
			if raw, err := bits.ReadSlice(src, udta.payloadOff(), udtaLen, limit); err == nil {
				d.udtaRaw = raw
			}
		}
		for _, c := range udta.children {
			d.udtaKids = append(d.udtaKids, refOf(c))
		}
		if chpl, ok := udta.find("chpl"); ok {
			d.chpl = refPtr(chpl)
			chplNode, haveChpl = chpl, true
		}
		// Classic QuickTime keeps its tags as direct udta children ("\xa9nam", "\xa9swr", ...)
		// with no meta wrapper. Decode them before the meta branch so a file carrying both
		// stores contributes from both; the two carry distinct source labels, so a
		// disagreement surfaces as a conflicting family rather than one silently winning.
		decodeUdtaTexts(src, udta, d, limit)
		if meta, ok := udta.find("meta"); ok {
			ilst, hasIlst := meta.find("ilst")
			// The handler decides how the ilst items are keyed: "mdta" makes them 1-based
			// indices into a sibling "keys" box, anything else the four-character iTunes names.
			// Without this every mdta item falls to the unknown-atom branch and the file reports
			// no tags at all.
			if hdlr, ok := meta.find("hdlr"); ok {
				d.metaHandler = handlerType(src, hdlr, limit)
			}
			if keys, ok := meta.find("keys"); ok {
				d.keys = refPtr(keys)
				if b, err := readPayloadWhole(src, keys, maxMetaChunk, limit); err == nil {
					d.keyNames = parseKeys(b)
				}
			}
			// A gap between where meta's children end and meta.end() corrupts a create-ilst
			// edit: buildCreated appends the new ilst at meta.end(), but a re-parse resolves the
			// first child at childStart/last-child-end (earlier), so the ilst lands misaligned.
			// walkAtoms tolerates an all-zero gap (the udta-terminator rule), so it must be
			// rejected here instead. Only a meta with no ilst can take that append path (an
			// existing ilst is edited in place), so the check is scoped to !hasIlst. trailingGap
			// (shared with the moov guard above) covers the bare (9-11 byte) and FullBox-with-zero-
			// pad (13-15+ byte) shapes uniformly; an empty bare meta (size 8) and an empty FullBox
			// meta (size 12) have no gap and edit cleanly, as does a meta whose hdlr child tiles to
			// its end.
			if !hasIlst {
				if gap := trailingGap(src, meta, limit); gap > 0 {
					return nil, fmt.Errorf("%w: moov.udta.meta has %d unusable trailing byte(s)",
						waxerr.ErrInvalidData, gap)
				}
			}
			d.meta = refPtr(meta)
			if hasIlst {
				d.ilst = refPtr(ilst)
				if err := decodeIlst(ctx, src, ilst, d, limit); err != nil {
					return nil, err
				}
				d.free = adjacentFree(meta, ilst)
			}
		}
	}

	// Chapters: a Nero chpl list and/or a QuickTime chapter text track project
	// into one deduplicated list; a disagreement between them is warned.
	chapterConflict := resolveChapters(src, moov, chplNode, haveChpl, d, limit)
	// Capture the structural refs a chapter write needs to rebuild the QuickTime
	// chapter text track (read-only; tolerant of anything it cannot find).
	collectChapterRefs(src, moov, d, limit)

	media := &core.Media{Format: core.FormatMP4, Native: d}
	tags, pics, families, numericGenre := project(d)
	media.Tags = tags
	media.Pictures = pics
	media.Chapters = d.chapters
	media.Families = families
	media.Warnings = mediaWarnings(tags, numericGenre)
	media.Warnings = append(media.Warnings, invalidKeyWarnings(d)...)
	if d.oversizedAtom != "" {
		media.Warnings = core.Warn(media.Warnings, core.WarnOversizedChunk,
			fmt.Sprintf("the %q atom declares more bytes than the file holds and was clamped to EOF; "+
				"bytes appended after the last atom read this way", d.oversizedAtom))
	}
	if chapterConflict {
		media.Warnings = core.Warn(media.Warnings, core.WarnChapterSourceConflict,
			"the Nero chpl list and the QuickTime chapter text track disagree")
	}
	// An mdat atom declared more bytes than the file holds: a truncated MP4.
	if d.mdatTruncated {
		media.Warnings = core.WarnTruncated(media.Warnings, "an mdat atom")
	}
	// The moov atom itself was clamped to EOF (a truncated download whose remaining bytes
	// still tile cleanly to moov.end(), so the guard above accepted it). Surface it like the
	// mdat truncation so the degraded structure is not silently reported as clean.
	if moov.truncated {
		media.Warnings = core.WarnTruncated(media.Warnings, "the moov atom")
	}
	// Movie fragments: the initial movie box's tags are read exactly (so the wording
	// avoids "best-effort"); what degrades is the duration - an empty_moov file reports 0
	// - and the essence digest, which cannot reach a fragment's samples.
	if d.fragmented {
		media.Warnings = core.Warn(media.Warnings, core.WarnFragmented,
			"fragmented MP4 (movie fragments): tags from the initial movie box are read, writing is refused")
	}
	media.Properties = core.Properties{Container: "MP4", Tracks: []core.AudioTrack{d.track}}
	setEssence(d, media)
	// Average bitrate from the audio-essence byte total and the track duration,
	// via the shared core helper. Computed after setEssence so it reuses the same
	// essence extent the digest covers, rather than re-summing stsz.
	var audioBytes int64
	for _, r := range media.EssenceRanges() {
		audioBytes += r[1] - r[0]
	}
	media.Properties.Tracks[0].Bitrate = core.AverageBitrate(audioBytes, d.track.Duration.Seconds())
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// mediaWarnings returns the content-derived warnings for a parsed or rewritten
// document: a resolved numeric genre and an inherited transcoder stamp (ffmpeg
// writes "Lavf..." into the \xa9too / Encoder atom on acquired files). Sharing
// this lets the post-write document's warnings match a fresh parse of the output.
func mediaWarnings(tags tag.TagSet, numericGenre bool) []core.Warning {
	var ws []core.Warning
	if numericGenre {
		ws = core.Warn(ws, core.WarnNumericGenre, "a numeric genre reference was resolved to a name")
	}
	if vs, ok := tags.Get(tag.Encoder); ok {
		for _, v := range vs {
			if core.IsTranscoderStamp(v) {
				ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder stamp: "+core.WarnSnippet(v))
			}
		}
	}
	return ws
}

// nodesNamed returns the nodes in list carrying the given name.
func nodesNamed(list []node, name string) []node {
	want := atomName(name)
	var out []node
	for _, c := range list {
		if c.name == want {
			out = append(out, c)
		}
	}
	return out
}

// sampleTables returns every sample table in moov, resolved along the spec path
// moov > trak > mdia > minf > stbl rather than by a name search.
//
// The distinction is load-bearing: trak, mdia, minf, and stbl are all container atoms, so
// walkAtoms descends into an ilst item that happens to carry one of those four-ccs, and
// findAll would reach it through udta > meta > ilst. A crafted offset table inside such an
// item is then decoded as a real one - failing the parse on a bogus entry count, or
// emitting a byte patch inside the very region a growing write replaces, which trips
// assemble's overlap guard. Walking the path closes that whole class rather than the
// direct-child case alone.
func sampleTables(moov node) []node {
	var out []node
	for _, trak := range nodesNamed(moov.children, "trak") {
		for _, mdia := range nodesNamed(trak.children, "mdia") {
			for _, minf := range nodesNamed(mdia.children, "minf") {
				out = append(out, nodesNamed(minf.children, "stbl")...)
			}
		}
	}
	return out
}

// collectOffsetTables reads the offset tables in moov into the doc, parsing their
// entries so the writer can shift them when the metadata is resized without re-reading
// the source: the stco/co64 chunk offsets into d.offTables and the saio sample-auxiliary
// offsets into d.auxTables. Scoping to sampleTables also removes any need to reason about
// a traf-contained saio: a traf is never an stbl, and a file carrying fragments is
// refused.
//
// Tables land in document order per stbl, replacing the old "all stco then all co64"
// grouping. The order stays immaterial: each table is patched at its own recorded source
// offset.
//
// A malformed saio degrades to a write refusal instead of failing the parse. The tag list
// and movie box do not depend on it, so a file whose ilst reads perfectly must still dump,
// lint, and verify - and did, before saio was collected at all. A broken stco/co64 stays
// fatal: it leaves the media itself unaddressable.
func collectOffsetTables(ctx context.Context, src core.ReaderAtSized, moov node, d *doc, limit int64) error {
	for _, stbl := range sampleTables(moov) {
		for _, a := range stbl.children {
			switch a.id() {
			case "stco", "co64", "saio":
			default:
				continue
			}
			t, err := parseOffsetTable(src, a, limit)
			if err != nil {
				if a.id() == "saio" {
					d.auxUnknown = true
					continue
				}
				return err
			}
			if a.id() == "saio" {
				d.auxTables = append(d.auxTables, t)
				continue
			}
			d.offTables = append(d.offTables, t)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// hasIlocBox reports whether a meta box carries an iloc item-location box. Its extents
// are absolute file offsets that nothing patches, so a rewrite that moves bytes strands
// them wherever the box sits - not only when it shares the moov.udta.meta this codec
// rewrites. The three placements the spec allows for a meta are checked (top level, moov,
// and moov.udta) rather than searching the tree by name, so an ilst item named "meta"
// cannot force a spurious refusal.
func hasIlocBox(top []node, moov node) bool {
	metas := append(nodesNamed(top, "meta"), nodesNamed(moov.children, "meta")...)
	for _, udta := range nodesNamed(moov.children, "udta") {
		metas = append(metas, nodesNamed(udta.children, "meta")...)
	}
	for _, m := range metas {
		if _, ok := m.find("iloc"); ok {
			return true
		}
	}
	return false
}

// hasTopLevel reports whether the file carries a top-level atom with the given name.
func hasTopLevel(d *doc, name string) bool {
	want := atomName(name)
	for _, a := range d.topLevel {
		if a.name == want {
			return true
		}
	}
	return false
}

// boundedCount reports whether `declared` fixed-width entries (each `width` bytes, after a
// `header`-byte table prefix) fit within `avail` bytes. The single guard every count-driven
// MP4 table decoder shares so none can drift into an unbounded loop. Every caller passes a
// uint32-widened count and a fixed small header/width constant (never data-derived), so
// header+declared*width (<= ~8.6e10) cannot overflow int64 and inputs are never negative - no
// explicit overflow/negative guard is needed (a future untrusted-width caller would add one).
func boundedCount(declared, header, width, avail int64) bool {
	return header+declared*width <= avail
}

// parseOffsetTable decodes one stco/co64/saio atom: a 4-byte version/flags, a
// per-box optional prefix, a 4-byte entry count, then that many 32- or 64-bit
// offsets. The entry width comes from the atom itself rather than from the caller -
// co64 is always 64-bit, and a saio's width is its own FullBox version (0: 32-bit,
// 1: 64-bit) - so a mixed-width set of tables decodes correctly in one pass.
//
// A saio's layout is ISO/IEC 14496-12 section 8.7.13: version/flags(4), then
// aux_info_type(4) + aux_info_type_parameter(4) only when flags&1, then
// entry_count(4), then the offsets.
//
// It stays a pure decoder (no doc argument): the chapter reader parses a track's
// stco/co64 through it with no document in hand, so the one unpatchable case reports
// itself through errSkipTable and the collector records it.
func parseOffsetTable(src core.ReaderAtSized, a node, limit int64) (offsetTable, error) {
	body, err := readPayloadWhole(src, a, maxMetaChunk, limit)
	if err != nil {
		return offsetTable{}, err
	}
	if len(body) < 8 {
		return offsetTable{}, fmt.Errorf("%w: %s atom too short", waxerr.ErrInvalidData, a.id())
	}
	t := offsetTable{offset: a.offset, headerLen: a.headerLen, size: a.size, name: a.name}
	copy(t.verFlags[:], body[0:4])
	switch a.id() {
	case "co64":
		t.co64 = true
	case "saio":
		// The FullBox word is version(1 byte) | flags(3 bytes): body[0] carries the
		// version (the entry width), and the aux_info_type_present flag is the low bit of
		// body[3], the last flags byte.
		switch body[0] {
		case 0: // 32-bit offsets
		case 1:
			t.co64 = true
		default:
			// An unrecognized saio version: the entry width is unknown, so guessing 32-bit
			// would write 4-byte entries over 8-byte data. Like any other saio malformation
			// this drops the table and refuses the write; the file still reads.
			return offsetTable{}, fmt.Errorf("%w: saio version %d is not supported", waxerr.ErrUnsupportedFormat, body[0])
		}
		if body[3]&1 != 0 {
			t.entryPrefix = 8 // aux_info_type + aux_info_type_parameter
		}
	}
	// Re-guard for the prefix: a saio carrying the aux pair has a 16-byte minimum body,
	// so the len < 8 check above does not cover reading its entry_count at body[12:16].
	if int64(len(body)) < 8+t.entryPrefix {
		return offsetTable{}, fmt.Errorf("%w: %s atom too short", waxerr.ErrInvalidData, a.id())
	}
	// int64 throughout: count is a 32-bit field, and count*width (up to ~3.4e10 for
	// co64) overflows a 32-bit int - the body bound caps count so the allocation
	// stays proportional to the bytes actually read.
	count := int64(binary.BigEndian.Uint32(body[4+t.entryPrefix : 8+t.entryPrefix]))
	width := int64(4)
	if t.co64 {
		width = 8
	}
	if !boundedCount(count, 8+t.entryPrefix, width, int64(len(body))) {
		return offsetTable{}, fmt.Errorf("%w: %s declares %d entries but is %d bytes",
			waxerr.ErrInvalidData, a.id(), count, len(body))
	}
	t.entries = make([]uint64, count)
	for i := int64(0); i < count; i++ {
		off := 8 + t.entryPrefix + i*width
		if t.co64 {
			t.entries[i] = binary.BigEndian.Uint64(body[off : off+8])
		} else {
			t.entries[i] = uint64(binary.BigEndian.Uint32(body[off : off+4]))
		}
	}
	return t, nil
}

// decodeIlst reads each ilst child atom's payload into the doc's item list, resolving an
// mdta-handler item's index against the keys box read just above. An index the keys table
// does not cover leaves key empty, so the item is preserved verbatim rather than dropped.
func decodeIlst(ctx context.Context, src core.ReaderAtSized, ilst node, d *doc, limit int64) error {
	for _, c := range ilst.children {
		if err := ctx.Err(); err != nil {
			return err
		}
		// ilst/covr items are the legitimately-large reads (high-res cover art), so they get
		// the configurable MaxAllocBytes ceiling rather than the 64 MiB structural cap - and
		// fail loudly past it instead of silently truncating an item we then could not read back.
		payload, err := readPayloadWhole(src, c, limit, limit)
		if err != nil {
			return err
		}
		it := item{name: c.name, payload: payload}
		if d.metaHandler == mdtaHandler {
			it.key = resolveMdtaKey(c.name, d.keyNames)
		}
		d.items = append(d.items, it)
	}
	return nil
}

// decodeUdtaTexts decodes the classic QuickTime text atoms sitting directly under udta into
// d.udtaTexts. Only the mapped four-character names are decoded; every other udta child
// (chpl, meta, and anything foreign) is left to the verbatim d.udtaRaw splice.
func decodeUdtaTexts(src core.ReaderAtSized, udta node, d *doc, limit int64) {
	for _, c := range udta.children {
		if _, ok := mapping.MP4UdtaTextKey(c.id()); !ok {
			continue
		}
		payload, err := readPayloadWhole(src, c, maxMetaChunk, limit)
		if err != nil {
			continue
		}
		if u, ok := decodeUdtaText(refOf(c), payload); ok {
			d.udtaTexts = append(d.udtaTexts, u)
		}
	}
}

// reusablePadding reports whether an atom id is padding WaxLabel can overwrite in place.
// Both "free" and "skip" are spec padding (native.go marks both as such); iTunes/mutagen
// write "free", but a file may carry "skip", so either is reusable.
func reusablePadding(id string) bool {
	return id == "free" || id == "skip"
}

// adjacentFree returns the free/skip padding atom immediately before or after ilst within
// meta (the only reusable padding this codec considers), or nil. Mirrors iTunes/mutagen,
// which keep padding next to the tag list.
func adjacentFree(meta, ilst node) *atomRef {
	idx := -1
	for i, c := range meta.children {
		if c.offset == ilst.offset {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	if idx > 0 && reusablePadding(meta.children[idx-1].id()) {
		r := refOf(meta.children[idx-1])
		return &r
	}
	if idx+1 < len(meta.children) && reusablePadding(meta.children[idx+1].id()) {
		r := refOf(meta.children[idx+1])
		return &r
	}
	return nil
}

// parseProperties fills the audio track from the first 'soun' trak: the duration
// from mdhd and the codec/geometry from the first stsd sample entry.
func parseProperties(src core.ReaderAtSized, moov node, d *doc, limit int64) {
	var trak node
	found := false
	for _, t := range moov.findAll("trak", nil) {
		mdia, ok := t.find("mdia")
		if !ok {
			continue
		}
		if hdlr, ok := mdia.find("hdlr"); ok && handlerType(src, hdlr, limit) == "soun" {
			trak, found = t, true
			break
		}
	}
	if !found {
		return
	}
	mdia, _ := trak.find("mdia")
	var timescale uint32
	if mdhd, ok := mdia.find("mdhd"); ok {
		if ts, dur, ok := mdhdFields(src, mdhd, limit); ok && ts > 0 {
			timescale = ts
			d.track.Duration = scaleToDuration(dur, ts)
		}
	}
	// Report the edit-list-trimmed playable duration. The raw mdhd duration can include
	// AAC encoder priming that the track's own edit list removes. Only shrink the value;
	// malformed edit lists should not inflate it. Bitrate below is recomputed from the
	// trimmed duration.
	if mvTs, _ := movieTimingOf(src, moov, limit); mvTs > 0 {
		if edited := trackEditedDuration(src, trak, mvTs, limit); edited > 0 && edited < d.track.Duration {
			d.track.Duration = edited
		}
	}
	if minf, ok := mdia.find("minf"); ok {
		if stbl, ok := minf.find("stbl"); ok {
			if stsd, ok := stbl.find("stsd"); ok {
				parseStsd(src, stsd, d, timescale, limit)
			}
		}
	}
}

// handlerType returns a hdlr atom's 4-character handler type (e.g. "soun").
func handlerType(src core.ReaderAtSized, hdlr node, limit int64) string {
	b, err := readPayloadPrefix(src, hdlr, 12, limit)
	if err != nil || len(b) < 12 {
		return ""
	}
	return string(b[8:12])
}

// parseStsd fills the codec name and audio geometry from the first sample entry.
// The AudioSampleEntry layout (after the entry's 8-byte size+4cc header): 6+2+8
// reserved bytes, then channels(2), sample_size(2), 4 skipped, sample_rate(16.16).
// That fixed layout holds for a v0 or v1 sound entry; a v2 entry replaces it with the
// QuickTime structure, whose float64 rate and uint32 channel count sit elsewhere.
//
// For the three entries whose configuration this parser decodes - the ALAC magic cookie,
// the FLAC dfLa STREAMINFO, and the AAC esds AudioSpecificConfig - that configuration is
// the decoder's source of truth and overrides the entry, whose 16.16 rate field cannot
// hold a hi-res rate at all. Only the reported track is corrected; d.cfg keeps the raw
// entry values, so the essence-digest salt (and every stored digest) is unchanged.
//
// AAC alone needs the entry back, because an AudioSpecificConfig describes the core coder
// and a player outputs an SBR stream at twice that rate. Three cases: a config that
// declares SBR gives the played rate outright; a config that says nothing about SBR whose
// entry rate is exactly double the core rate is an implicitly signalled stream the muxer
// already decoded, so the entry stands; otherwise the config's core rate wins, including
// over a config that explicitly denies SBR.
//
// The uncompressed entries need the same correction for a different reason: their fourcc
// carries detail the fixed fields do not. A QuickTime "ms" + WAVE-format-tag fourcc names
// a Windows codec through the same table WAV and ASF read that tag with, so the NUL-bearing
// spelling never reaches the reported codec. The PCM-family fourccs name the sample width,
// which the entry's samplesize field does not (writers store a fixed 16 there whatever the
// real width); the ISOBMFF ipcm/fpcm pair keep theirs in a pcmC box instead; and a v2
// entry's format flags are the only place a float lpcm stream differs from an integer one.
func parseStsd(src core.ReaderAtSized, stsd node, d *doc, timescale uint32, limit int64) {
	// Clamp to the alloc limit rather than letting ReadSlice refuse the read: a caller with
	// a limit below the prefix would otherwise get no codec and no geometry at all, and
	// d.cfg's zeroes would change the essence-digest salt for the same bytes.
	b, err := readPayloadPrefix(src, stsd, min(int64(1024), limit), limit)
	if err != nil || len(b) < 44 {
		return
	}
	copy(d.cfg.codec[:], b[12:16])
	fourcc := string(d.cfg.codec[:])
	d.track.Codec = fourcc
	if strings.HasPrefix(fourcc, "ms") {
		// QTFF's spelling for a Windows codec: "ms" then the WAVE format tag, big-endian,
		// which is how ffmpeg's mov demuxer reads the same bytes. Every tag goes through the
		// shared table, so "ms\x00\x55" names MP3 the way that tag does in a WAV "fmt "
		// chunk or an ASF Stream Properties object, and one the table does not know reports
		// its own hex rather than a fourcc holding a NUL. Fourccs outside this spelling are
		// reported raw; render escapes what is not printable.
		d.track.Codec = core.WaveFormatCodec(binary.BigEndian.Uint16(b[14:16]))
	}

	end := len(b)
	if size := int64(binary.BigEndian.Uint32(b[8:12])); size > 0 && 8+size < int64(end) {
		end = int(8 + size)
	}

	// The sound sample-entry version lives 16 bytes into the entry (which starts at b[8]);
	// the len>=44 guard above already covers b[24:26]. A version past 2 is read with the v2
	// layout: QuickTime has defined no later one, and its struct is self-describing (a
	// declared size gates every field), so a v3 that keeps the prefix reads correctly and
	// one that does not fails the size check rather than misparsing the v0/v1 offsets.
	version := binary.BigEndian.Uint16(b[24:26])
	extOff := 8 + 36 // extensions follow the fixed AudioSampleEntry fields
	switch {
	case version >= 2:
		off, ok := parseSoundEntryV2(b, end, d)
		if !ok {
			return
		}
		extOff = off
	default:
		d.cfg.channels = binary.BigEndian.Uint16(b[32:34])
		d.cfg.sampleSize = binary.BigEndian.Uint16(b[34:36])
		d.cfg.sampleRate = uint32(binary.BigEndian.Uint16(b[40:42])) // integer part of 16.16
		d.track.Channels = int(d.cfg.channels)
		d.track.BitsPerSample = int(d.cfg.sampleSize)
		d.track.SampleRate = int(d.cfg.sampleRate)
		if depth, _ := pcmFourcc(fourcc); depth > 0 {
			d.track.BitsPerSample = depth
		}
		if version == 1 {
			extOff += 16 // v1 appends four QuickTime bytes-per-packet/frame/sample fields
		}
	}
	var cfg entryConfig
	switch fourcc {
	case "alac", "fLaC", "mp4a", "ipcm", "fpcm":
		cfg = scanEntryConfig(b, extOff, end, fourcc)
		applyEntryConfig(d, cfg)
	}
	if _, ok := pcmFourcc(fourcc); ok {
		if fourcc == "fpcm" && cfg.bitDepth == 0 {
			// No readable pcmC. The entry's samplesize is the fixed 16 every writer stores
			// there and no 16-bit float format exists, so report no width rather than one
			// that cannot be true.
			d.track.BitsPerSample = 0
		}
		// A v0 entry stores the rate as 16.16, which holds nothing above 65535, so ffmpeg
		// writes zero for a hi-res ISOBMFF ipcm/fpcm track. These entries carry no
		// configuration to recover it from, leaving the media timescale - which for an
		// uncompressed track is the sample rate, and is what the .mov twin's v2 entry
		// states outright.
		if d.track.SampleRate == 0 && timescale > 0 && timescale < math.MaxInt32 {
			d.track.SampleRate = int(timescale)
		}
	}
}

// pcmFourcc reports whether a fourcc is one of the uncompressed QuickTime/ISOBMFF sample
// entries, and the sample width it names (0 when the width lives elsewhere). Two rules key
// off this one table. For the entries that name a width, the entry's samplesize field is
// decoration - ffmpeg and QuickTime v1 entries write 16 whatever the real width - so the
// fourcc is the figure the WAV twins and ffprobe report. And none of these entries carry a
// codec configuration declaring a rate, so when the 16.16 rate field is zero, which is how
// a v0 entry says the rate does not fit it, the media timescale is the only witness left.
//
// Matched case-insensitively, as the canonical codec table matches the same fourccs.
func pcmFourcc(fourcc string) (depth int, ok bool) {
	switch strings.ToUpper(fourcc) {
	case "IN24":
		return 24, true
	case "IN32", "FL32":
		return 32, true
	case "FL64":
		return 64, true
	case "ULAW", "ALAW":
		return 8, true
	case "IMA4":
		return 4, true
	case "LPCM", "IPCM", "FPCM", "SOWT", "TWOS", "RAW ", "NONE":
		return 0, true // the entry field or a pcmC box carries the width
	}
	return 0, false
}

// parseSoundEntryV2 decodes a QuickTime version 2 sound sample entry's geometry onto
// d.track and returns where its extension boxes start. The v2 struct replaces the fixed
// v0/v1 fields with a declared struct size, a float64 rate, a uint32 channel count, and a
// constant bits-per-channel that compressed codecs leave zero. d.cfg is left zero: the
// digest salt has never carried v2 geometry, and filling it now would change every stored
// digest for such a file.
func parseSoundEntryV2(b []byte, end int, d *doc) (extOff int, ok bool) {
	if len(b) < 68 {
		return 0, false
	}
	// size > end-8 rather than 8+size > end: the declared size is attacker-controlled, and
	// 8+size overflows to a negative extOff on a 32-bit build, which then indexes out of
	// range. A size too large to be an int at all is already negative, so size < 72 rejects it.
	size := int(binary.BigEndian.Uint32(b[44:48]))
	if size < 72 || size > end-8 {
		return 0, false
	}
	// NaN, an infinity, or an absurd magnitude is not a rate; int(NaN) is
	// implementation-defined.
	if f := math.Float64frombits(binary.BigEndian.Uint64(b[48:56])); f > 0 && f < math.MaxInt32 {
		d.track.SampleRate = int(f)
	}
	if ch := binary.BigEndian.Uint32(b[56:60]); ch > 0 && ch <= 255 {
		d.track.Channels = int(ch)
	}
	if depth := binary.BigEndian.Uint32(b[64:68]); depth > 0 && depth <= 64 {
		d.track.BitsPerSample = int(depth)
	}
	// formatSpecificFlags, the word after constBitsPerChannel (size >= 72 above guarantees
	// the bytes). An lpcm fourcc says only "linear PCM", so bit 0 - kAudioFormatFlagIsFloat -
	// is the format's only statement that the samples are floats; the width then names which
	// float form, as it does for the fl32/fl64 fourccs. A width no float format defines
	// leaves the entry naming itself, the same rule the pcmC box follows.
	if flags := binary.BigEndian.Uint32(b[68:72]); string(b[12:16]) == "lpcm" && flags&1 != 0 {
		switch d.track.BitsPerSample {
		case 32:
			d.track.Codec = "IEEE float"
		case 64:
			d.track.Codec = "IEEE float64"
		}
	}
	return 8 + size, true
}

// dfLaStreamInfoEnd is where a dfLa box's first metadata block body ends, relative to the
// box: the 8-byte box header, the 4-byte FullBox version/flags, the 4-byte
// METADATA_BLOCK_HEADER, and the STREAMINFO body.
const dfLaStreamInfoEnd = 8 + 4 + 4 + vorbis.StreamInfoLen

// entryConfig is what a codec's own configuration declares. streamInfo is set only for a
// FLAC entry, whose STREAMINFO the FLAC-in-ISOBMFF spec makes authoritative for the whole
// track, block-size bounds and MD5 included; asc only for an esds, whose AudioSpecificConfig
// needs reconciling with the entry rather than simply replacing it. codec is set where the
// configuration names the codec more precisely than the four-cc: an esds object type, and a
// pcmC width that separates the two float forms one fourcc covers.
type entryConfig struct {
	sampleRate, channels, bitDepth int
	streamInfo                     *core.AudioTrack
	codec                          string
	asc                            *mpeg4audio.Config
}

func (c entryConfig) empty() bool { return c == entryConfig{} }

// scanEntryConfig walks sibling boxes in b[off:end] for the sample entry's codec
// configuration, descending into a QuickTime 'wave' wrapper. The ALAC magic cookie is a
// 12-byte box header (size, type, version/flags) followed by the 24-byte
// ALACSpecificConfig: bitDepth at 5, numChannels at 9, and the sample rate at 20, after
// the u32 frameLength and u8 compatibleVersion. Recursion depth is bounded by the buffer:
// each level consumes an 8-byte header of an at-most-1024-byte prefix.
func scanEntryConfig(b []byte, off, end int, fourcc string) entryConfig {
	for off+8 <= end {
		size := int(binary.BigEndian.Uint32(b[off : off+4]))
		switch name := string(b[off+4 : off+8]); {
		case name == "dfLa" && fourcc == "fLaC":
			// STREAMINFO sits at a fixed offset, so a dfLa declaring more bytes than the
			// bounded prefix read returned is still usable. The size still has to be large
			// enough to hold one, or the read would take its geometry from the next box.
			return entryConfig{streamInfo: dfLaStreamInfo(b, off, size, end)}
		case size < 8 || size > end-off:
			return entryConfig{} // malformed or truncated by the prefix read: keep the entry values
		case name == "esds" && fourcc == "mp4a":
			// Matched on the box name, never the four-cc: a QuickTime v1 entry wraps its esds
			// in a wave box beside a 12-byte child literally named "mp4a", and the recursion
			// below has to find the real one inside.
			return esdsConfig(b, off, off+size)
		case name == "alac" && fourcc == "alac":
			return alacCookieConfig(b, off, size)
		case name == "pcmC" && (fourcc == "ipcm" || fourcc == "fpcm"):
			return pcmCConfig(b, off, size, fourcc)
		case name == "wave":
			if cfg := scanEntryConfig(b, off+8, off+size, fourcc); !cfg.empty() {
				return cfg
			}
		}
		off += size
	}
	return entryConfig{}
}

// alacCookieConfig decodes the ALACSpecificConfig at b[off:], dropping each field the
// cookie leaves implausible. An all-ones rate would convert to a negative int on a 32-bit
// build.
func alacCookieConfig(b []byte, off, size int) entryConfig {
	if size < 36 {
		return entryConfig{}
	}
	var cfg entryConfig
	if depth := int(b[off+17]); depth > 0 && depth <= 32 {
		cfg.bitDepth = depth
	}
	if ch := int(b[off+21]); ch > 0 && ch <= 8 {
		cfg.channels = ch
	}
	if r := binary.BigEndian.Uint32(b[off+32 : off+36]); r > 0 && r < math.MaxInt32 {
		cfg.sampleRate = int(r)
	}
	return cfg
}

// pcmCConfig decodes the PCM configuration box an ISOBMFF ipcm or fpcm entry carries, a
// FullBox whose 8-byte header and 4 version/flags bytes are followed by format_flags and
// then PCM_sample_size. That is where these entries keep the width; their samplesize field
// holds the same fixed 16 every writer puts there. A width the entry's fourcc does not
// define is dropped rather than published. scanEntryConfig has already bounded off+size by
// the buffer, so a declared size that covers the field is enough to read it.
func pcmCConfig(b []byte, off, size int, fourcc string) entryConfig {
	if size < 14 {
		return entryConfig{}
	}
	var cfg entryConfig
	switch depth := int(b[off+13]); {
	case fourcc == "ipcm" && (depth == 16 || depth == 24 || depth == 32):
		cfg.bitDepth = depth
	case fourcc == "fpcm" && depth == 32:
		cfg.bitDepth = depth
	case fourcc == "fpcm" && depth == 64:
		// The fourcc alone canonicalizes to "IEEE float", the 32-bit form; only the width
		// separates the two, so name the codec here rather than let 64-bit samples read as
		// the narrower one.
		cfg.bitDepth, cfg.codec = depth, "IEEE float64"
	}
	return cfg
}

// dfLaStreamInfo decodes the STREAMINFO the dfLa box at b[off:] must open with, or nil
// when the box is too small to hold one, opens with another block type, or is cut short of
// its 34 bytes by the prefix read. Layout: the 8-byte box header, a 4-byte FullBox
// version/flags, then METADATA_BLOCK_HEADER and body. The bound is the sample entry's end,
// not the buffer's: a dfLa declaring more bytes than it has would otherwise decode
// STREAMINFO out of whatever box follows it.
func dfLaStreamInfo(b []byte, off, size, end int) *core.AudioTrack {
	if size < dfLaStreamInfoEnd || off+dfLaStreamInfoEnd > end || b[off+12]&0x7F != vorbis.BlockStreamInfo {
		return nil
	}
	t, err := vorbis.ParseStreamInfo(b[off+16 : off+dfLaStreamInfoEnd])
	if err != nil {
		return nil
	}
	return &t
}

// applyEntryConfig overrides the sample entry's reported geometry with the codec
// configuration's. The entry's Duration and TotalSamples stand, since the timing comes
// from mdhd; its four-cc stands too unless the configuration names the codec more
// precisely, which an esds objectTypeIndication and a 64-bit pcmC width do.
func applyEntryConfig(d *doc, cfg entryConfig) {
	if si := cfg.streamInfo; si != nil {
		d.track.SampleRate = si.SampleRate
		d.track.Channels = si.Channels
		d.track.BitsPerSample = si.BitsPerSample
		d.track.MinBlockSize = si.MinBlockSize
		d.track.MaxBlockSize = si.MaxBlockSize
		d.track.MD5 = si.MD5
		return
	}
	if cfg.codec != "" {
		// The raw spelling; the root parse canonicalizes it and keeps this as the profile.
		d.track.Codec = cfg.codec
	}
	if cfg.asc != nil {
		applyAACConfig(d, *cfg.asc)
	}
	if cfg.sampleRate > 0 {
		d.track.SampleRate = cfg.sampleRate
	}
	if cfg.channels > 0 {
		d.track.Channels = cfg.channels
	}
	if cfg.bitDepth > 0 {
		d.track.BitsPerSample = cfg.bitDepth
	}
}

// applyAACConfig reconciles an AudioSpecificConfig with the geometry the sample entry
// already put on the track. The config describes the core coder, so it cannot simply
// replace the entry: an SBR stream plays at twice the core rate, and a muxer that decoded
// one may have written that played rate into the entry itself.
func applyAACConfig(d *doc, asc mpeg4audio.Config) {
	entryRate, entryChannels := d.track.SampleRate, d.track.Channels
	switch {
	case asc.SBR:
		// Declared outright, including the downsampled case where the extension rate equals
		// the core rate and doubling would be wrong.
		if r := asc.OutputSampleRate(); r > 0 {
			d.track.SampleRate = r
		}
		if ch := asc.OutputChannels(); ch > 0 {
			d.track.Channels = ch
		}
	case !asc.SBRSignalled && asc.ObjectType == aacLC && asc.SampleRate > 0 && entryRate == 2*asc.SampleRate:
		// An implicitly signalled HE-AAC stream copied into MP4: the config is the core
		// coder's, but the muxer decoded the stream and wrote the played geometry into the
		// entry. An entry landing on exactly double the core rate is the signal; corruption
		// does not pick that value. A config that denies SBR never reaches here, and the
		// core has to be AAC LC, which is what HE-AAC is defined over - an AAC LD or
		// xHE-AAC track whose rates happen to sit 2:1 apart must not be relabeled.
		d.track.Codec = "HE-AAC"
		if entryChannels == 2 && asc.ChannelConfig == 1 {
			d.track.Codec = "HE-AAC v2"
		}
	default:
		if asc.SampleRate > 0 {
			d.track.SampleRate = asc.SampleRate
		}
		if asc.Channels > 0 {
			d.track.Channels = asc.Channels
		}
	}
}

// setEssence records the audio-essence byte ranges from the mdat atoms. A single
// mdat uses the contiguous [AudioStart, AudioEnd) extent (which gives the best
// change-detection fingerprint, covering metadata both before and after the
// media); multiple mdats use the multi-segment AudioRanges.
func setEssence(d *doc, media *core.Media) {
	ranges := essenceMdats(d)
	switch len(ranges) {
	case 0:
		// metadata-only file: no essence (the fingerprint then hashes it whole).
	case 1:
		media.AudioStart = ranges[0][0]
		media.AudioEnd = ranges[0][1]
	default:
		media.AudioRanges = ranges
		media.AudioStart = ranges[0][0]
		media.AudioEnd = ranges[len(ranges)-1][1]
	}
}

// essenceMdats returns the mdat ranges that hold media essence, as [start, end). Each
// range begins at its first non-chapter chunk, so front-loaded QuickTime chapter samples
// are excluded. An mdat holding only chapter samples is dropped entirely, including
// chapter mdats leaked by older builds.
//
// The trim is unconditional (single and multi-mdat) and front-only by design: a WaxLabel
// chapter edit copies the shared mdat verbatim and appends a new chapter mdat, so trimming
// to the first audio chunk in both the source and the rewritten result keeps the digest
// stable. Filtering against every non-chapter offset table (not just the first audio
// track's) keeps secondary audio, video, and subtitle tracks. Chapter samples interleaved
// after the first audio chunk stay in the digest, which avoids trimming audio. A file with
// no non-chapter offset table keeps every mdat whole.
func essenceMdats(d *doc) [][2]int64 {
	ranges := make([][2]int64, len(d.mdats))
	for i, m := range d.mdats {
		ranges[i] = [2]int64{m[0], m[0] + m[1]}
	}
	nonChapter := nonChapterTables(d)
	if len(nonChapter) == 0 {
		return ranges
	}
	out := ranges[:0:0]
	for _, r := range ranges {
		if first := firstNonChapterChunk(r, nonChapter); first >= 0 {
			out = append(out, [2]int64{first, r[1]})
		}
	}
	// If no mdat held a referenced non-chapter chunk, keep every mdat whole rather than
	// report zero essence. Real files reference their mdats, so this is a damaged-input
	// fallback.
	if len(out) == 0 {
		return ranges
	}
	return out
}

// firstNonChapterChunk returns the smallest non-chapter chunk offset within the mdat
// range [r[0], r[1]), or -1 if the mdat holds no non-chapter chunk (chapter-only). It is
// the lower bound of the essence trim: everything before it in the mdat is front-loaded
// chapter text.
func firstNonChapterChunk(r [2]int64, tables []offsetTable) int64 {
	first := int64(-1)
	for _, t := range tables {
		for _, e := range t.entries {
			// A co64 offset >= 2^63 cannot be a valid position in an int64-addressed file;
			// skip it rather than let int64(e) wrap negative and spuriously fail the range test.
			if e > math.MaxInt64 {
				continue
			}
			if off := int64(e); r[0] <= off && off < r[1] && (first < 0 || off < first) {
				first = off
			}
		}
	}
	return first
}

// nonChapterTables returns non-empty offset tables outside the chapter text track being
// rewritten. Their chunks mark an mdat as media essence rather than chapter samples.
func nonChapterTables(d *doc) []offsetTable {
	var out []offsetTable
	for _, t := range d.offTables {
		if !withinChapTrak(d, t) && len(t.entries) > 0 {
			out = append(out, t)
		}
	}
	return out
}

// mdatHoldsChunk reports whether any of tables places a chunk inside the mdat range
// [r[0], r[1]).
func mdatHoldsChunk(r [2]int64, tables []offsetTable) bool {
	for _, t := range tables {
		if rangeHoldsChunk(r, t.entries) {
			return true
		}
	}
	return false
}

// mdatHoldsNonChapterChunk reports whether any non-chapter chunk-offset table places a
// chunk inside the mdat range [r[0], r[1]). A positive result means the mdat carries media
// essence and must not be reclaimed as chapter-only storage.
func mdatHoldsNonChapterChunk(d *doc, r [2]int64) bool {
	return mdatHoldsChunk(r, nonChapterTables(d))
}

// rangeHoldsChunk reports whether any chunk offset in entries falls within the mdat
// range [r[0], r[1]).
func rangeHoldsChunk(r [2]int64, entries []uint64) bool {
	for _, e := range entries {
		if off := int64(e); r[0] <= off && off < r[1] {
			return true
		}
	}
	return false
}

// trackOffTable returns the chunk-offset table that lies inside trak's byte range.
func trackOffTable(d *doc, trak *atomRef) (offsetTable, bool) {
	for _, t := range d.offTables {
		if trak != nil && t.offset >= trak.offset && t.offset < trak.end() {
			return t, true
		}
	}
	return offsetTable{}, false
}

// readPayloadPrefix reads up to prefixLen bytes of an atom's payload, bounded by the
// alloc limit. The header-prefix decoders (the ftyp brand, mvhd/tkhd/mdhd/hdlr/stsd
// leading fields) intend to read only the front of a possibly-larger atom, so reading
// min(payloadSize, prefixLen) and never failing on a larger atom is the correct contract.
func readPayloadPrefix(src core.ReaderAtSized, n node, prefixLen, limit int64) ([]byte, error) {
	return bits.ReadSlice(src, n.payloadOff(), min(n.size-n.headerLen, prefixLen), limit)
}

// readPayloadWhole reads an atom's entire payload, failing with ErrSizeTooLarge when it
// exceeds capBytes rather than silently truncating it. The whole-atom decoders (the ilst
// items and the structural offset/sample tables) need every byte, so a payload past the cap
// is a hard error, not a quietly-shortened read that downstream length checks would then
// reject with a misleading diagnostic. capBytes is maxMetaChunk for the structural tables
// (which should never approach it) and the configurable MaxAllocBytes for ilst/covr items
// (legitimately large cover art). It never reads a large mdat (only its range is recorded).
func readPayloadWhole(src core.ReaderAtSized, n node, capBytes, limit int64) ([]byte, error) {
	payloadSize := n.size - n.headerLen
	// capBytes <= 0 means "no cap", matching the limit <= 0 unbounded convention bits.ReadSlice
	// follows: the ilst read passes capBytes = limit (the configurable MaxAllocBytes), so a
	// caller that explicitly disables the alloc limit (limit == 0) also disables this cap.
	if capBytes > 0 && payloadSize > capBytes {
		return nil, fmt.Errorf("%w: MP4 atom %q payload %d exceeds %d", waxerr.ErrSizeTooLarge, n.id(), payloadSize, capBytes)
	}
	return bits.ReadSlice(src, n.payloadOff(), payloadSize, limit)
}

// refOf / refPtr capture a node as a lightweight atomRef for the writer.
func refOf(n node) atomRef {
	return atomRef{name: n.name, offset: n.offset, headerLen: n.headerLen, size: n.size}
}

func refPtr(n node) *atomRef {
	r := refOf(n)
	return &r
}
