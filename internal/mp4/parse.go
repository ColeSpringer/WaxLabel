package mp4

import (
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/internal/mpeg4audio"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxMetaChunk bounds structural atom reads (mdat range only recorded). Works with
// MaxAllocBytes (smaller wins).
const maxMetaChunk = 64 << 20

// parse reads MP4 into a Media: ilst tags, sample-table geometry, offset tables and
// mdat ranges for rewrite, top-level atoms as base. Top-level moof is recorded and
// warned (rewrite refused at Plan). mvex without fragments is progressive. Segments
// with no moov are rejected here.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	// Resolve an unset allocation limit (a zero-value ParseOptions) to the library
	// default; ReadSlice requires a positive limit.
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
	var moov node
	haveMoov, extraMoov := false, false
	for _, a := range top {
		d.topLevel = append(d.topLevel, refOf(a))
		switch a.id() {
		case "ftyp":
			// The major brand (e.g. "M4B ") signals an audiobook; it is preserved
			// verbatim on write (ftyp is copied) and surfaced in the native view.
			b, err := readPayloadPrefix(src, a, 4, limit)
			if fatal := bits.Fatal(err); fatal != nil {
				return nil, fatal
			}
			if err == nil && len(b) >= 4 {
				d.majorBrand = string(b[:4])
			}
		case "moof":
			// Movie fragments. The file reads fine (the init moov's ilst carries the tags) but
			// is not rewritable: the stco/co64 fixups cannot reach a fragment's trun sample
			// offsets, so a metadata resize would desynchronize the media.
			d.fragmented = true
		case "mdat":
			d.mdats = append(d.mdats, [2]int64{a.payloadOff(), a.size - a.headerLen})
			if a.truncated {
				d.mdatTruncated = true
			}
			d.mdatOpenEnded = d.mdatOpenEnded || a.openEnded
		case "moov":
			// The first decides, as in ffmpeg: a second one is what joining two files leaves.
			if haveMoov {
				extraMoov = true
				continue
			}
			moov, haveMoov = a, true
		}
	}
	if !haveMoov {
		// A fragmented media segment has no movie box, so it keeps the exit-3 classification
		// rather than the invalid-data "no moov box" diagnostic.
		if d.fragmented || hasTopLevel(d, "styp") {
			return nil, fmt.Errorf("%w: fragmented MP4 media segment (no moov box)",
				waxerr.ErrUnsupportedFormat)
		}
		// A truncated final mdat that overruns its declared end swallows whatever follows
		// it (clamped to EOF). A moov after such an mdat is never seen, so report the
		// truncation, the real cause, instead of "no moov box".
		if d.mdatTruncated {
			return nil, fmt.Errorf("%w: MP4 mdat atom declares more bytes than the file holds (truncated; a trailing moov was overrun)", waxerr.ErrInvalidData)
		}
		return nil, fmt.Errorf("%w: MP4 has no moov box", waxerr.ErrInvalidData)
	}
	d.moov = refPtr(moov)
	d.moovUnsized = moov.truncated || moov.openEnded
	// The moov read is resized by whatever grows it and an mdat is sized or refused where one
	// is appended; any other final atom that reaches EOF by its declared size stays last.
	last := top[len(top)-1]
	unsized := (last.truncated || last.openEnded) && last.id() != "mdat" && last.offset != moov.offset
	d.appendAt = last.end()
	if unsized {
		d.appendAt = last.offset
	}
	// A top-level atom whose declared size ran past EOF is clamped so the complete earlier
	// atoms still read. An mdat and the moov read get their own truncated-audio warnings below.
	if unsized && last.truncated {
		d.oversizedAtom = last.id()
	}

	// A gap between moov's last child and moov.end() corrupts a create-ilst edit as the
	// meta gap below does: with no udta, buildCreated appends the new tag path at
	// moov.end() (write.go, the default branch), but a re-parse resolves moov's children at
	// their recorded ends, so the path sits past a stray all-zero remainder, misaligned.
	if _, ok := moov.find("udta"); !ok {
		gap, err := trailingGap(src, moov, limit)
		if err != nil {
			return nil, err
		}
		if gap > 0 {
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
	// Bytes behind the last whole atom that a table names cannot move unpatched, so a new atom
	// goes after them. Where a size field that forms no atom stopped the walk there, they are
	// media the walk never reached, not a tail.
	if d.appendAt < size && tablesReach(d.appendAt, d.offTables, d.auxTables) {
		if size-last.end() >= 8 {
			return nil, fmt.Errorf("%w: the bytes at %d form no atom, and the chunk offsets point behind them",
				waxerr.ErrInvalidData, last.end())
		}
		d.appendAt = size
	}
	d.hasIloc = hasIlocBox(top, moov)
	tracks, err := readTracks(src, moov, limit)
	if err != nil {
		return nil, err
	}
	if err := parseProperties(src, moov, tracks, d, limit); err != nil {
		return nil, err
	}
	audioBytes, err := readSoundChunks(src, moov, tracks, d, limit)
	if err != nil {
		return nil, err
	}

	// Tag path: moov.udta.meta.ilst, or a meta directly under moov. Each level is optional; record
	// what exists so the writer can either rewrite the ilst or create the missing wrappers.
	var chpls []node
	var udtaStore, moovStore *metaStore
	if udta, ok := moov.find("udta"); ok {
		d.udta = refPtr(udta)
		// Capture the udta payload verbatim so a chapter rewrite can splice the new ilst/chpl
		// byte ranges into it while preserving every other user-data atom.
		if udtaLen := udta.size - udta.headerLen; udtaLen >= 0 {
			raw, err := bits.ReadSlice(src, udta.payloadOff(), udtaLen, limit)
			if bits.Fatal(err) != nil {
				return nil, err
			}
			d.udtaRaw = raw
		}
		for _, c := range udta.children {
			d.udtaKids = append(d.udtaKids, refOf(c))
		}
		chpls = nodesNamed(udta.children, "chpl")
		// Classic QuickTime keeps its tags as direct udta children ("\xa9nam", "\xa9swr",
		// ...) with no meta wrapper. The two stores carry distinct source labels, so a
		// disagreement surfaces as a conflicting family.
		if err := decodeUdtaTexts(src, udta, d, limit); err != nil {
			return nil, err
		}
		if meta, ok := udta.find("meta"); ok {
			st, err := readMetaStore(ctx, src, meta, limit)
			if err != nil {
				return nil, err
			}
			// A gap between meta's last child and meta.end() corrupts a create-ilst edit:
			// buildCreated appends the new ilst at meta.end(), but a re-parse resolves the
			// first child at childStart/last-child-end (earlier), so the ilst is misaligned.
			if st.ilst == nil {
				gap, err := trailingGap(src, meta, limit)
				if err != nil {
					return nil, err
				}
				if gap > 0 {
					return nil, fmt.Errorf("%w: moov.udta.meta has %d unusable trailing byte(s)",
						waxerr.ErrInvalidData, gap)
				}
			}
			udtaStore = &st
		}
	}
	if meta, ok := moov.find("meta"); ok {
		st, err := readMetaStore(ctx, src, meta, limit)
		if err != nil {
			return nil, err
		}
		if st.ilst != nil {
			moovStore = &st
		}
	}
	switch {
	case udtaStore != nil && udtaStore.ilst != nil:
		d.setStore(*udtaStore)
		d.second = moovStore
	case moovStore != nil:
		d.setStore(*moovStore)
		d.metaAtMoov = true
	case udtaStore != nil:
		d.setStore(*udtaStore)
	}

	// Chapters: a Nero chpl list and/or a QuickTime chapter text track project
	// into one deduplicated list; a disagreement between them is warned.
	chapterConflict, err := resolveChapters(src, moov, chpls, tracks, d, limit)
	if err != nil {
		return nil, err
	}
	// Capture the structural refs a chapter write needs to rebuild the QuickTime
	// chapter text track (read-only; tolerant of anything it cannot find).
	if err := collectChapterRefs(src, moov, tracks, d, limit); err != nil {
		return nil, err
	}

	media := &core.Media{Format: core.FormatMP4, Native: d}
	tags, pics, families, numericGenre := project(d)
	media.Tags = tags
	media.Pictures = pics
	media.Chapters = d.chapters
	media.Families = families
	media.Warnings = mediaWarnings(tags, numericGenre)
	media.Warnings = append(media.Warnings, invalidKeyWarnings(d)...)
	media.Warnings = append(media.Warnings, duplicateWarnings(d)...)
	if extraMoov {
		media.Warnings = core.Warn(media.Warnings, core.WarnDuplicateTagBlock,
			"more than one moov atom; the first is read and the rest are kept as they are")
	}
	if tail := size - last.end(); tail > 0 {
		media.Warnings = core.Warn(media.Warnings, core.WarnTrailingBytes,
			fmt.Sprintf("%d byte(s) after the last atom form no atom; preserved verbatim", tail))
	}
	if d.oversizedAtom != "" {
		media.Warnings = core.Warn(media.Warnings, core.WarnOversizedChunk,
			fmt.Sprintf("the %q atom declares more bytes than the file holds and was clamped to EOF; "+
				"bytes appended after the last atom read this way", d.oversizedAtom))
	}
	if chapterConflict {
		media.Warnings = core.Warn(media.Warnings, core.WarnChapterSourceConflict,
			"the Nero chpl list and the QuickTime chapter text track disagree")
	}
	if d.legacyTitles != "" {
		media.Warnings = core.Warn(media.Warnings, core.WarnInvalidText,
			"a chapter title is not valid UTF-8; read as "+d.legacyTitles)
	}
	// An mdat atom declared more bytes than the file holds: a truncated MP4.
	if d.mdatTruncated {
		media.Warnings = core.WarnTruncated(media.Warnings, "an mdat atom")
	}
	// The moov atom itself was clamped to EOF: a truncated download whose remaining bytes
	// still tile cleanly to moov.end(), so the check above accepted it. Warn like the mdat
	// truncation.
	if moov.truncated {
		media.Warnings = core.WarnTruncated(media.Warnings, "the moov atom")
	}
	// Movie fragments: the initial movie box's tags are read exactly, so the wording avoids
	// "best-effort". What degrades is the duration (an empty_moov file reports 0) and the
	// essence digest, which cannot reach a fragment's samples.
	if d.fragmented {
		media.Warnings = core.Warn(media.Warnings, core.WarnFragmented,
			"fragmented MP4 (movie fragments): tags from the initial movie box are read, writing is refused")
	}
	media.Properties = core.Properties{Container: "MP4", Tracks: []core.AudioTrack{d.track}}
	setEssence(d, media)
	// Average bitrate from the track's sample bytes and its duration. Without readable sample
	// tables the essence is all there is to count.
	if !d.sampleExtent {
		for _, r := range media.EssenceRanges() {
			audioBytes += r[1] - r[0]
		}
	}
	media.Properties.Tracks[0].Bitrate = core.AverageBitrate(audioBytes, d.track.Duration.Seconds())
	if err := core.SetIdentity(src, media); err != nil {
		return nil, err
	}
	core.SyncTextItems(media)
	return media, nil
}

// mediaWarnings returns the content-derived warnings for a parsed document: a
// resolved numeric genre and an inherited transcoder stamp (ffmpeg writes
// "Lavf..." into the \xa9too / Encoder atom on acquired files).
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

// duplicateWarnings reports the chpl and udta text atoms the file holds more than one of.
func duplicateWarnings(d *doc) []core.Warning {
	var ws []core.Warning
	if len(d.chplDups) > 0 {
		ws = core.Warn(ws, core.WarnDuplicateTagBlock,
			"more than one chpl atom; the first holding chapters, else the first, is authoritative and a chapter edit drops the rest")
	}
	var names []string
	var keys []tag.Key
	for _, u := range d.udtaTexts {
		if len(u.dups) > 0 {
			names = append(names, itemLabel(u.name))
			keys = append(keys, u.key)
		}
	}
	if len(names) > 0 {
		ws = core.WarnKeyed(ws, core.WarnDuplicateTagBlock, fmt.Sprintf(
			"more than one moov.udta text atom named %s; the first holding a value, else the first, is authoritative and an edit of it drops the rest",
			strings.Join(names, ", ")), keys...)
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

// sampleTables returns every sample table in moov, resolved along the spec path moov >
// trak > mdia > minf > stbl rather than by a name search.
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

// collectOffsetTables parses the offset tables in moov into the doc, so the writer can
// shift them on a metadata resize without re-reading the source: stco/co64 chunk offsets
// into d.offTables and saio sample-auxiliary offsets into d.auxTables.
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
				if a.id() == "saio" && bits.Fatal(err) == nil {
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

// hasIlocBox reports whether a meta box carries an iloc item-location box. Only the three
// placements the spec allows for a meta are checked (top level, moov, and moov.udta), so
// an ilst item named "meta" cannot force a spurious refusal.
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

// boundedCount reports whether `declared` fixed-width entries (each `width` bytes,
// after a `header`-byte table prefix) fit within `avail` bytes.
func boundedCount(declared, header, width, avail int64) bool {
	return header+declared*width <= avail
}

// parseOffsetTable decodes one stco/co64/saio atom: a 4-byte version/flags, a per-box
// optional prefix, a 4-byte entry count, then that many 32- or 64-bit offsets.
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
	// co64) overflows a 32-bit int. The body bound caps count, so the allocation stays
	// proportional to the bytes read.
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
	for i := range count {
		off := 8 + t.entryPrefix + i*width
		if t.co64 {
			t.entries[i] = binary.BigEndian.Uint64(body[off : off+8])
		} else {
			t.entries[i] = uint64(binary.BigEndian.Uint32(body[off : off+4]))
		}
	}
	return t, nil
}

// readMetaStore reads a meta box's tag store. The handler decides how the ilst items are keyed:
// "mdta" makes them 1-based indices into a sibling "keys" box, anything else the four-character
// iTunes names.
func readMetaStore(ctx context.Context, src core.ReaderAtSized, meta node, limit int64) (metaStore, error) {
	s := metaStore{meta: refPtr(meta)}
	var err error
	if hdlr, ok := meta.find("hdlr"); ok {
		if s.handler, err = handlerType(src, hdlr, limit); err != nil {
			return s, err
		}
	}
	if keys, ok := meta.find("keys"); ok {
		s.keys = refPtr(keys)
		b, err := readPayloadWhole(src, keys, maxMetaChunk, limit)
		if bits.Fatal(err) != nil {
			return s, err
		}
		if err == nil {
			s.keyNames = parseKeys(b)
		}
	}
	if ilst, ok := meta.find("ilst"); ok {
		s.ilst = refPtr(ilst)
		if s.items, err = decodeIlst(ctx, src, ilst, s.handler, s.keyNames, limit); err != nil {
			return s, err
		}
		s.free = adjacentFree(meta, ilst)
	}
	return s, nil
}

// decodeIlst reads each ilst child atom's payload, resolving an mdta-handler item's index against
// the keys box. An index the keys table does not cover leaves key empty, so the item is preserved
// verbatim rather than dropped.
func decodeIlst(ctx context.Context, src core.ReaderAtSized, ilst node, handler string, keyNames []string, limit int64) ([]item, error) {
	var items []item
	for _, c := range ilst.children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// ilst/covr items are the legitimately large reads (high-res cover art), so they get
		// the configurable MaxAllocBytes ceiling, not the 64 MiB structural cap, and fail
		// past it instead of truncating.
		payload, err := readPayloadWhole(src, c, limit, limit)
		if err != nil {
			return nil, err
		}
		it := item{name: c.name, payload: payload}
		if handler == mdtaHandler {
			it.key = resolveMdtaKey(c.name, keyNames)
		}
		items = append(items, it)
	}
	return items, nil
}

// decodeUdtaTexts decodes the classic QuickTime text atoms sitting directly under udta into
// d.udtaTexts, one per name. Only the mapped four-character names are decoded; every other udta
// child (chpl, meta, and anything foreign) is left to the verbatim d.udtaRaw splice.
func decodeUdtaTexts(src core.ReaderAtSized, udta node, d *doc, limit int64) error {
	var all []udtaText
	for _, c := range udta.children {
		if _, ok := mapping.MP4UdtaTextKey(c.id()); !ok {
			continue
		}
		payload, err := readPayloadWhole(src, c, maxMetaChunk, limit)
		if err != nil {
			if bits.Fatal(err) != nil {
				return err
			}
			continue
		}
		if u, ok := decodeUdtaText(refOf(c), payload); ok {
			all = append(all, u)
		}
	}
	d.udtaTexts = foldUdtaTextDups(all)
	return nil
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
func parseProperties(src core.ReaderAtSized, moov node, tracks movieTracks, d *doc, limit int64) error {
	if tracks.audio < 0 {
		return nil
	}
	trak := tracks.traks[tracks.audio]
	mdia, _ := trak.find("mdia")
	var timescale uint32
	if mdhd, ok := mdia.find("mdhd"); ok {
		ts, dur, ok, err := mdhdFields(src, mdhd, limit)
		if err != nil {
			return err
		}
		if ok && ts > 0 {
			timescale = ts
			d.track.Duration = scaleToDuration(dur, ts)
		}
	}
	// Report the edit-list-trimmed playable duration.
	mvTs, _, err := movieTimingOf(src, moov, limit)
	if err != nil {
		return err
	}
	if mvTs > 0 {
		edited, err := trackEditedDuration(src, trak, mvTs, limit)
		if err != nil {
			return err
		}
		if edited > 0 && edited < d.track.Duration {
			d.track.Duration = edited
		}
	}
	if stbl, ok := sampleTableOf(trak); ok {
		if stsd, ok := stbl.find("stsd"); ok {
			if err := parseStsd(src, stsd, d, timescale, limit); err != nil {
				return err
			}
			return refineFromFirstSample(src, stbl, d, limit)
		}
	}
	return nil
}

// refineFromFirstSample settles what the sample entry leaves open and the stream itself states:
// the layer of MPEG audio, and the rate and word length of TrueHD.
func refineFromFirstSample(src core.ReaderAtSized, stbl node, d *doc, limit int64) error {
	switch fourcc := string(d.cfg.codec[:]); {
	case fourcc == "mp4a" && d.track.Codec == "MP3":
		head, err := firstSample(src, stbl, d, 4, limit)
		if err != nil {
			return err
		}
		if name := mpegAudioLayer(head); name != "" {
			d.track.Codec = name
		}
	case fourcc == "mlpa":
		head, err := firstSample(src, stbl, d, trueHDSyncLen, limit)
		if err != nil {
			return err
		}
		// The sample entry's own width is the 16 every writer stores there.
		d.track.BitsPerSample = 0
		sync, ok := parseTrueHDMajorSync(head)
		if !ok {
			return nil
		}
		d.track.BitsPerSample = sync.bits
		if sync.rate > 0 {
			d.track.SampleRate = sync.rate
		}
		if sync.mlp {
			d.track.Codec = "MLP"
		}
	}
	return nil
}

// firstSample reads up to n bytes of the track's first sample, which opens its first chunk, and
// nothing when the chunk table places none inside the file.
func firstSample(src core.ReaderAtSized, stbl node, d *doc, n, limit int64) ([]byte, error) {
	t, ok := d.chunkTable(stbl)
	if !ok || len(t.entries) == 0 || t.entries[0] >= uint64(src.Size()) {
		return nil, nil
	}
	off := int64(t.entries[0])
	b, err := bits.ReadSlice(src, off, min(n, src.Size()-off), limit)
	return b, bits.Fatal(err)
}

// sampleTableOf returns a trak's sample table: trak > mdia > minf > stbl.
func sampleTableOf(trak node) (node, bool) {
	mdia, _ := trak.find("mdia")
	minf, _ := mdia.find("minf")
	return minf.find("stbl")
}

// readSoundChunks sets the audio essence from the sample tables of moov's sound tracks: the bytes
// of each chunk, as the sizes of its samples sum. It returns the summed sample size of the first
// sound track, which the bitrate is of. A table that cannot be read, or tables that place nothing
// in the file, leave the essence to the mdats.
func readSoundChunks(src core.ReaderAtSized, moov node, tr movieTracks, d *doc, limit int64) (int64, error) {
	var ranges [][2]int64
	var first int64
	for i, trak := range tr.traks {
		if tr.handlers[i] != "soun" || !ownTrak(moov, trak) {
			continue
		}
		stbl, ok := sampleTableOf(trak)
		if !ok {
			return 0, nil
		}
		chunks, total, ok, err := trackChunks(src, stbl, d, limit)
		if err != nil || !ok {
			return 0, err
		}
		if i == tr.audio {
			first = total
		}
		ranges = append(ranges, chunks...)
	}
	if ranges = mergeRanges(ranges); len(ranges) == 0 {
		return 0, nil
	}
	d.soundChunks, d.sampleExtent = ranges, true
	return first, nil
}

// trackChunks returns the byte range of each chunk of a track, cut to the file, and the summed size
// of its samples. ok is false when a table is missing or does not hold what it declares.
func trackChunks(src core.ReaderAtSized, stbl node, d *doc, limit int64) (ranges [][2]int64, total int64, ok bool, err error) {
	table, found := d.chunkTable(stbl)
	if !found {
		return nil, 0, false, nil
	}
	runs, ok, err := openChunkRuns(src, stbl, limit)
	if err != nil || !ok {
		return nil, 0, false, err
	}
	stsz, found := stbl.find("stsz")
	if !found {
		return nil, 0, false, nil
	}
	head, err := readPayloadPrefix(src, stsz, 12, limit)
	if err != nil || len(head) < 12 {
		return nil, 0, false, bits.Fatal(err)
	}
	shared := int64(binary.BigEndian.Uint32(head[4:8]))
	count := int64(binary.BigEndian.Uint32(head[8:12]))
	if shared == 0 && !boundedCount(count, 12, 4, stsz.size-stsz.headerLen) {
		return nil, 0, false, nil
	}
	sizes := tableReader{src: src, off: stsz.payloadOff() + 12, left: count, width: 4, limit: limit}
	// One size for every sample is a byte size, or the mark of a track that counts frames.
	ratio := frameRatio{samples: 1, bytes: shared}
	if shared != 0 {
		if ratio, ok, err = frameCounted(src, stbl, ratio, limit); err != nil || !ok {
			return nil, 0, false, err
		}
	}
	size := src.Size()
	next := int64(0) // the first sample of the chunk
	for c := range table.entries {
		n, ok, err := runs.samples(c + 1)
		if err != nil || !ok {
			return nil, 0, false, err
		}
		samples := min(int64(n), count-next)
		// Both factors are 32 bits, so their product can pass an int64.
		length := samples / ratio.samples * ratio.bytes
		if ratio.bytes > 0 && samples/ratio.samples > math.MaxInt64/ratio.bytes {
			length = math.MaxInt64
		}
		if shared == 0 {
			if length, ok, err = sumSizes(&sizes, samples); err != nil || !ok {
				return nil, 0, false, err
			}
		}
		next += samples
		if total += length; total < 0 {
			total = math.MaxInt64
		}
		if e := table.entries[c]; e < uint64(size) && length > 0 {
			// A chunk lies in an mdat and ends with it, whatever its sizes say: past it are
			// bytes a write may move or change. One that starts in none places no audio this
			// can vouch for.
			start := int64(e)
			i := mdatAt(d.mdats, start)
			if i < 0 {
				return nil, 0, false, nil
			}
			ranges = append(ranges, [2]int64{start, start + min(length, d.mdats[i][0]+d.mdats[i][1]-start)})
		}
	}
	return ranges, total, true, nil
}

// tableBlock is how much of a sample table a tableReader reads at once.
const tableBlock = 64 << 10

// tableReader reads the fixed-width entries of a sample table in order, a block at a time: a
// long track's stsz and stsc run to megabytes, and only what they add up to is kept. The
// allocation limit bounds a block, not the table.
type tableReader struct {
	src   core.ReaderAtSized
	off   int64 // where the next block starts
	left  int64 // entries not yet read
	width int64
	limit int64
	buf   []byte
}

// fill makes buf hold at least one entry, reading the next block when it holds none. more is
// false at the table's end; ok is false when the source ends inside the table.
func (t *tableReader) fill() (more, ok bool, err error) {
	if int64(len(t.buf)) >= t.width {
		return true, true, nil
	}
	if t.left == 0 {
		return false, true, nil
	}
	// At least one entry: a limit under an entry's width refuses the read.
	want := min(t.left*t.width, max(min(t.limit, tableBlock)/t.width*t.width, t.width))
	b, err := bits.ReadSlice(t.src, t.off, want, t.limit)
	if err != nil || int64(len(b)) < want {
		return false, false, bits.Fatal(err)
	}
	t.buf, t.off, t.left = b, t.off+want, t.left-want/t.width
	return true, true, nil
}

// sumSizes adds up the next n entries of an stsz, of the count the caller holds it to.
func sumSizes(t *tableReader, n int64) (total int64, ok bool, err error) {
	for n > 0 {
		if more, ok, err := t.fill(); err != nil || !ok || !more {
			return 0, false, err
		}
		k := min(n, int64(len(t.buf))/4)
		for i := range k {
			total += int64(binary.BigEndian.Uint32(t.buf[i*4:]))
		}
		t.buf, n = t.buf[k*4:], n-k
	}
	return total, true, nil
}

// chunkRuns walks an stsc: how many samples each chunk holds, asked in chunk order.
type chunkRuns struct {
	table     tableReader
	cur, next stscEntry
	haveNext  bool
}

// openChunkRuns reads an stsc's header. ok is false when the table is missing or does not hold
// the entries it declares.
func openChunkRuns(src core.ReaderAtSized, stbl node, limit int64) (*chunkRuns, bool, error) {
	stsc, found := stbl.find("stsc")
	if !found {
		return nil, false, nil
	}
	head, err := readPayloadPrefix(src, stsc, 8, limit)
	if err != nil || len(head) < 8 {
		return nil, false, bits.Fatal(err)
	}
	count := int64(binary.BigEndian.Uint32(head[4:8]))
	if !boundedCount(count, 8, 12, stsc.size-stsc.headerLen) {
		return nil, false, nil
	}
	return &chunkRuns{table: tableReader{src: src, off: stsc.payloadOff() + 8, left: count, width: 12, limit: limit}}, true, nil
}

// samples returns how many samples the 1-based chunk holds: the count of the last run that
// starts at or before it, none ahead of the first run. Chunks are asked in ascending order.
func (r *chunkRuns) samples(chunk int) (n uint32, ok bool, err error) {
	for {
		if !r.haveNext {
			more, ok, err := r.table.fill()
			if err != nil || !ok {
				return 0, false, err
			}
			if !more {
				break
			}
			b := r.table.buf
			r.next = stscEntry{firstChunk: binary.BigEndian.Uint32(b[0:4]), samplesPerChunk: binary.BigEndian.Uint32(b[4:8])}
			r.table.buf, r.haveNext = b[12:], true
		}
		if int64(r.next.firstChunk) > int64(chunk) {
			break
		}
		r.cur, r.haveNext = r.next, false
	}
	return r.cur.samplesPerChunk, true, nil
}

// frameRatio sizes the samples of a track whose stsz gives one size for all: every `samples` of
// them span `bytes`.
type frameRatio struct{ samples, bytes int64 }

// frameCounted settles what a shared stsz size means. A track whose one stts entry gives each
// sample a duration of 1 counts audio frames, as QuickTime writes uncompressed and fixed-ratio
// audio, and its stsz size is no byte count (often 1): the sample entry sizes a frame, or the
// packet a run of frames makes, as ffmpeg's demuxer takes it. Any other track keeps asStored. ok is
// false for a frame-counted track of a codec whose packets the entry does not size.
func frameCounted(src core.ReaderAtSized, stbl node, asStored frameRatio, limit int64) (frameRatio, bool, error) {
	stts, found := stbl.find("stts")
	if !found {
		return asStored, true, nil
	}
	t, err := readPayloadPrefix(src, stts, 16, limit)
	if err != nil || len(t) < 16 || binary.BigEndian.Uint32(t[4:8]) != 1 || binary.BigEndian.Uint32(t[12:16]) != 1 {
		return asStored, true, bits.Fatal(err)
	}
	stsd, found := stbl.find("stsd")
	if !found {
		return asStored, asStored.bytes != 1, nil
	}
	// The entry as the track's own properties read it: a width can lie in a pcmC box or a
	// version 2 entry's fields.
	var entry doc
	if err := parseStsd(src, stsd, &entry, 0, limit); err != nil {
		return asStored, false, err
	}
	b, err := readPayloadPrefix(src, stsd, 60, limit)
	if err != nil || len(b) < 44 {
		return asStored, asStored.bytes != 1, bits.Fatal(err)
	}
	// A version 1 entry states its packet: samples per packet, and the bytes a packet of every
	// channel takes.
	var stated frameRatio
	if binary.BigEndian.Uint16(b[24:26]) == 1 && len(b) >= 60 {
		stated = frameRatio{int64(binary.BigEndian.Uint32(b[44:48])), int64(binary.BigEndian.Uint32(b[52:56]))}
	}
	channels := int64(entry.track.Channels)
	layout, known := core.FourccSampleLayout(string(entry.cfg.codec[:]))
	switch {
	case known && layout.PacketBytes == 0:
		// Uncompressed: a frame is a sample of the track's width for each channel.
		if frame := channels * int64((entry.track.BitsPerSample+7)/8); frame > 0 {
			return frameRatio{1, frame}, true, nil
		}
	case stated.samples > 1 && stated.bytes > 0:
		return stated, true, nil
	case known && channels > 0:
		return frameRatio{int64(layout.FramesPerPacket), int64(layout.PacketBytes) * channels}, true, nil
	}
	return asStored, asStored.bytes != 1, nil
}

// mergeRanges sorts [start, end) ranges and merges those that touch or overlap.
func mergeRanges(ranges [][2]int64) [][2]int64 {
	slices.SortFunc(ranges, func(a, b [2]int64) int { return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1])) })
	out := ranges[:0]
	for _, r := range ranges {
		if n := len(out); n > 0 && r[0] <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], r[1])
			continue
		}
		out = append(out, r)
	}
	return out
}

// handlerType returns a hdlr atom's 4-character handler type (e.g. "soun").
func handlerType(src core.ReaderAtSized, hdlr node, limit int64) (string, error) {
	b, err := readPayloadPrefix(src, hdlr, 12, limit)
	if err != nil || len(b) < 12 {
		return "", bits.Fatal(err)
	}
	return string(b[8:12]), nil
}

// parseStsd fills the codec name and audio geometry from the first sample entry, read from
// the first 1024 bytes of the stsd payload.
func parseStsd(src core.ReaderAtSized, stsd node, d *doc, timescale uint32, limit int64) error {
	b, err := readPayloadPrefix(src, stsd, 1024, limit)
	if err != nil || len(b) < 44 {
		return bits.Fatal(err)
	}
	copy(d.cfg.codec[:], b[12:16])
	fourcc := string(d.cfg.codec[:])
	d.track.Codec = fourcc
	if tag, ok := core.QuickTimeWaveFormatTag(fourcc); ok {
		// QTFF's spelling for a Windows codec, read as ffmpeg's mov demuxer reads the same
		// bytes.
		d.track.Codec = core.WaveFormatCodec(tag)
	}

	// Asked once: the width the fourcc fixes drives the v0/v1 arm below, and membership
	// alone drives the two rules after the switch.
	layout, fixedLayout := core.FourccSampleLayout(fourcc)
	end := len(b)
	if size := int64(binary.BigEndian.Uint32(b[8:12])); size > 0 && 8+size < int64(end) {
		end = int(8 + size)
	}

	// The sound sample-entry version lives 16 bytes into the entry (which starts at b[8]).
	version := binary.BigEndian.Uint16(b[24:26])
	extOff := 8 + 36 // extensions follow the fixed AudioSampleEntry fields
	switch {
	case version >= 2:
		off, ok := parseSoundEntryV2(b, end, d)
		if !ok {
			return nil
		}
		extOff = off
	default:
		d.cfg.channels = binary.BigEndian.Uint16(b[32:34])
		d.cfg.sampleSize = binary.BigEndian.Uint16(b[34:36])
		d.cfg.sampleRate = uint32(binary.BigEndian.Uint16(b[40:42])) // integer part of 16.16
		d.track.Channels = int(d.cfg.channels)
		d.track.BitsPerSample = int(d.cfg.sampleSize)
		d.track.SampleRate = int(d.cfg.sampleRate)
		if layout.Depth > 0 {
			d.track.BitsPerSample = layout.Depth
		}
		if version == 1 {
			extOff += 16 // v1 appends four QuickTime bytes-per-packet/frame/sample fields
		}
	}
	var cfg entryConfig
	switch fourcc {
	case "alac", "fLaC", "mp4a", "ipcm", "fpcm", "mlpa":
		cfg = scanEntryConfig(b, extOff, end, fourcc)
		applyEntryConfig(d, cfg)
	}
	// ffmpeg stores a TrueHD rate as a whole 32-bit number, which the 16.16 field above reads
	// as none; the stream's own major sync settles it later, where one is found.
	if fourcc == "mlpa" && d.track.SampleRate == 0 && timescale > 0 && timescale < math.MaxInt32 {
		d.track.SampleRate = int(timescale)
	}
	if fixedLayout {
		if fourcc == "fpcm" && cfg.bitDepth == 0 {
			// No readable pcmC. The entry's samplesize is the fixed 16 every writer stores
			// there, and no 16-bit float format exists, so report no width.
			d.track.BitsPerSample = 0
		}
		// A v0 entry stores the rate as 16.16, which holds nothing above 65535, so ffmpeg
		// writes zero for a hi-res ISOBMFF ipcm/fpcm track.
		if d.track.SampleRate == 0 && timescale > 0 && timescale < math.MaxInt32 {
			d.track.SampleRate = int(timescale)
		}
	}
	return nil
}

// parseSoundEntryV2 decodes a QuickTime version 2 sound sample entry's geometry onto
// d.track and returns where its extension boxes start. d.cfg stays zero: filling it
// would change the essence-digest salt for every such file.
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
	// the bytes).
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

// entryConfig is what a codec's own configuration declares.
type entryConfig struct {
	sampleRate, channels, bitDepth int
	streamInfo                     *core.AudioTrack
	codec                          string
	asc                            *mpeg4audio.Config
}

func (c entryConfig) empty() bool { return c == entryConfig{} }

// scanEntryConfig walks sibling boxes in b[off:end] for the sample entry's codec
// configuration, descending into a QuickTime 'wave' wrapper.
func scanEntryConfig(b []byte, off, end int, fourcc string) entryConfig {
	for off+8 <= end {
		size := int(binary.BigEndian.Uint32(b[off : off+4]))
		switch name := string(b[off+4 : off+8]); {
		case name == "dfLa" && fourcc == "fLaC":
			// STREAMINFO sits at a fixed offset, so a dfLa declaring more bytes than the
			// bounded prefix read returned is still usable. The size still has to be large
			// enough to hold one, or the read would take its geometry from the next box.
			return entryConfig{streamInfo: dfLaStreamInfo(b, off, size, end)}
		case name == "esds" && fourcc == "mp4a" && size > end-off:
			// Cut by the prefix read, as an esds holding a whole Vorbis header set is. The
			// object type sits ahead of what is missing.
			return esdsConfig(b, off, end, true)
		case size < 8 || size > end-off:
			return entryConfig{} // malformed or truncated by the prefix read: keep the entry values
		case name == "esds" && fourcc == "mp4a":
			// Matched on the box name, never the four-cc: a QuickTime v1 entry wraps its esds
			// in a wave box beside a 12-byte child literally named "mp4a", and the recursion
			// below has to find the real one inside.
			return esdsConfig(b, off, off+size, false)
		case name == "alac" && fourcc == "alac":
			return alacCookieConfig(b, off, size)
		case name == "pcmC" && (fourcc == "ipcm" || fourcc == "fpcm"):
			return pcmCConfig(b, off, size, fourcc)
		case name == "dmlp" && fourcc == "mlpa" && size >= 9:
			return entryConfig{sampleRate: mlpSampleRate(b[off+8] >> 4)}
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
// FullBox whose 8-byte header and 4 version/flags bytes are followed by format_flags
// and then PCM_sample_size. A width the entry's fourcc does not define is dropped.
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
		// separates the two, so name the codec here.
		cfg.bitDepth, cfg.codec = depth, "IEEE float64"
	}
	return cfg
}

// dfLaStreamInfo decodes the STREAMINFO the dfLa box at b[off:] must open with, or nil
// when the box is too small to hold one, opens with another block type, or is cut short
// of its 34 bytes by the prefix read.
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
// configuration's. Its four-cc stands unless the configuration names the codec more
// precisely, as an esds objectTypeIndication and a 64-bit pcmC width do.
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
// already put on the track.
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
		// entry.
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

// setEssence records the audio-essence byte ranges: the sound tracks' chunks, or the mdats when
// their sample tables do not give them.
func setEssence(d *doc, media *core.Media) {
	ranges := d.soundChunks
	if !d.sampleExtent {
		ranges = essenceMdats(d)
	}
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

// essenceMdats returns the mdat ranges that hold media essence, as [start, end): each mdat from
// the first chunk a media table places in it. When the tables place none anywhere, as damaged ones
// do, every mdat counts whole but those chapter samples fill.
func essenceMdats(d *doc) [][2]int64 {
	first := chunkStarts(d.mdats, nonChapterTables(d))
	var out, whole [][2]int64
	for i, m := range d.mdats {
		end := m[0] + m[1]
		if first[i] >= 0 {
			out = append(out, [2]int64{first[i], end})
		}
		if !d.chapterMdat(m[0]) {
			whole = append(whole, [2]int64{m[0], end})
		}
	}
	if len(out) == 0 {
		return whole
	}
	return out
}

// chunkStarts returns, for each mdat payload (offset, length; in file order), the smallest
// offset the tables place in it, or -1 when they place none there.
func chunkStarts(mdats [][2]int64, tables []offsetTable) []int64 {
	first := make([]int64, len(mdats))
	for i := range first {
		first[i] = -1
	}
	for _, t := range tables {
		for _, e := range t.entries {
			// A co64 offset >= 2^63 is no position in an int64-addressed file.
			if e > math.MaxInt64 {
				continue
			}
			off := int64(e)
			if i := mdatAt(mdats, off); i >= 0 && (first[i] < 0 || off < first[i]) {
				first[i] = off
			}
		}
	}
	return first
}

// mdatAt returns the index of the mdat payload (offset, length; in file order) holding off, or -1.
func mdatAt(mdats [][2]int64, off int64) int {
	i := sort.Search(len(mdats), func(i int) bool { return mdats[i][0]+mdats[i][1] > off })
	if i < len(mdats) && off >= mdats[i][0] {
		return i
	}
	return -1
}

// tablesReach reports whether any table places a chunk at or past off.
func tablesReach(off int64, groups ...[]offsetTable) bool {
	for _, g := range groups {
		for _, t := range g {
			if slices.ContainsFunc(t.entries, func(e uint64) bool { return e >= uint64(off) }) {
				return true
			}
		}
	}
	return false
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

// readPayloadPrefix reads up to prefixLen bytes of an atom's payload, bounded by the
// alloc limit.
func readPayloadPrefix(src core.ReaderAtSized, n node, prefixLen, limit int64) ([]byte, error) {
	return bits.ReadSlice(src, n.payloadOff(), min(n.size-n.headerLen, prefixLen), limit)
}

// readPayloadWhole reads an atom's entire payload, failing with ErrSizeTooLarge when it
// exceeds capBytes rather than truncating it.
func readPayloadWhole(src core.ReaderAtSized, n node, capBytes, limit int64) ([]byte, error) {
	payloadSize := n.size - n.headerLen
	// capBytes <= 0 means no cap; bits.ReadSlice still refuses a read over limit, and any limit
	// that is not positive.
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
