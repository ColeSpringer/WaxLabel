package mp4

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
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
// every top-level atom preserved as the rewrite base. Fragmented files (a
// top-level moof, or a moov declaring movie fragments) are rejected.
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
		case "moof", "styp":
			return nil, fmt.Errorf("%w: fragmented MP4 (moof) is not supported", waxerr.ErrUnsupportedFormat)
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
		// A truncated final mdat that overruns its declared end swallows whatever
		// follows it (clamped to EOF). When a moov sits after such an mdat it is never
		// seen, so report the truncation - the real cause - rather than a misleading
		// "no moov box", independent of box order.
		if d.mdatTruncated {
			return nil, fmt.Errorf("%w: MP4 mdat atom declares more bytes than the file holds (truncated; a trailing moov was overrun)", waxerr.ErrInvalidData)
		}
		return nil, fmt.Errorf("%w: MP4 has no moov box", waxerr.ErrInvalidData)
	}
	if _, ok := moov.find("mvex"); ok {
		return nil, fmt.Errorf("%w: fragmented MP4 (moov/mvex) is not supported", waxerr.ErrUnsupportedFormat)
	}
	d.moov = refPtr(moov)

	if err := collectOffsetTables(ctx, src, moov, d, limit); err != nil {
		return nil, err
	}
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
		if chpl, ok := udta.find("chpl"); ok {
			d.chpl = refPtr(chpl)
			chplNode, haveChpl = chpl, true
		}
		if meta, ok := udta.find("meta"); ok {
			ilst, hasIlst := meta.find("ilst")
			// A gap between where meta's children end and meta.end() corrupts a create-ilst
			// edit: buildCreated appends the new ilst at meta.end(), but a re-parse resolves the
			// first child at childStart/last-child-end (earlier), so the ilst lands misaligned.
			// walkAtoms tolerates an all-zero gap (the udta-terminator rule), so it must be
			// rejected here instead. Only a meta with no ilst can take that append path (an
			// existing ilst is edited in place), so the check is scoped to !hasIlst. childEnd is
			// the last child's end, or - only when there are no children - the child-start
			// position, which is read lazily since a has-children meta (the common case) never
			// needs it. This covers the bare (9-11 byte) and FullBox-with-zero-pad (13-15+ byte)
			// shapes uniformly; an empty bare meta (size 8) and an empty FullBox meta (size 12)
			// have no gap and edit cleanly, as does a meta whose hdlr child tiles to its end.
			if !hasIlst {
				var childEnd int64
				if n := len(meta.children); n > 0 {
					childEnd = meta.children[n-1].end()
				} else {
					childEnd = childStart(src, meta, limit)
				}
				if childEnd < meta.end() {
					return nil, fmt.Errorf("%w: moov.udta.meta has %d unusable trailing byte(s)",
						waxerr.ErrInvalidData, meta.end()-childEnd)
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
	if chapterConflict {
		media.Warnings = core.Warn(media.Warnings, core.WarnChapterSourceConflict,
			"the Nero chpl list and the QuickTime chapter text track disagree")
	}
	// An mdat atom declared more bytes than the file holds: a truncated MP4.
	if d.mdatTruncated {
		media.Warnings = core.WarnTruncated(media.Warnings, "an mdat atom")
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
				ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder stamp: "+v)
			}
		}
	}
	return ws
}

// collectOffsetTables reads every stco/co64 in moov into the doc (all stco then
// all co64; the order is immaterial since each table is later patched at its own
// recorded source offset), parsing their entries so the writer can shift them
// when the metadata is resized without re-reading the source.
func collectOffsetTables(ctx context.Context, src core.ReaderAtSized, moov node, d *doc, limit int64) error {
	stco := moov.findAll("stco", nil)
	co64 := moov.findAll("co64", nil)
	for _, a := range stco {
		t, err := parseOffsetTable(src, a, false, limit)
		if err != nil {
			return err
		}
		d.offTables = append(d.offTables, t)
	}
	for _, a := range co64 {
		t, err := parseOffsetTable(src, a, true, limit)
		if err != nil {
			return err
		}
		d.offTables = append(d.offTables, t)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
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

// parseOffsetTable decodes one stco/co64 atom: a 4-byte version/flags, a 4-byte
// entry count, then that many 32- or 64-bit chunk offsets.
func parseOffsetTable(src core.ReaderAtSized, a node, co64 bool, limit int64) (offsetTable, error) {
	body, err := readPayloadWhole(src, a, maxMetaChunk, limit)
	if err != nil {
		return offsetTable{}, err
	}
	if len(body) < 8 {
		return offsetTable{}, fmt.Errorf("%w: %s atom too short", waxerr.ErrInvalidData, a.id())
	}
	t := offsetTable{offset: a.offset, headerLen: a.headerLen, size: a.size, co64: co64}
	copy(t.verFlags[:], body[0:4])
	// int64 throughout: count is a 32-bit field, and count*width (up to ~3.4e10 for
	// co64) overflows a 32-bit int - the body bound caps count so the allocation
	// stays proportional to the bytes actually read.
	count := int64(binary.BigEndian.Uint32(body[4:8]))
	width := int64(4)
	if co64 {
		width = 8
	}
	if !boundedCount(count, 8, width, int64(len(body))) {
		return offsetTable{}, fmt.Errorf("%w: %s declares %d entries but is %d bytes",
			waxerr.ErrInvalidData, a.id(), count, len(body))
	}
	t.entries = make([]uint64, count)
	for i := int64(0); i < count; i++ {
		off := 8 + i*width
		if co64 {
			t.entries[i] = binary.BigEndian.Uint64(body[off : off+8])
		} else {
			t.entries[i] = uint64(binary.BigEndian.Uint32(body[off : off+4]))
		}
	}
	return t, nil
}

// decodeIlst reads each ilst child atom's payload into the doc's item list.
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
		d.items = append(d.items, item{name: c.name, payload: payload})
	}
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
	if mdhd, ok := mdia.find("mdhd"); ok {
		if dur, ok := parseMdhd(src, mdhd, limit); ok {
			d.track.Duration = dur
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
				parseStsd(src, stsd, d, limit)
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

// parseMdhd returns the media duration from a mdhd atom, reusing the shared field
// decode and the shared unit->Duration conversion.
func parseMdhd(src core.ReaderAtSized, mdhd node, limit int64) (time.Duration, bool) {
	ts, dur, ok := mdhdFields(src, mdhd, limit)
	if !ok || ts == 0 {
		return 0, false
	}
	return scaleToDuration(dur, ts), true
}

// parseStsd fills the codec name and audio geometry from the first sample entry.
// The AudioSampleEntry layout (after the entry's 8-byte size+4cc header): 6+2+8
// reserved bytes, then channels(2), sample_size(2), 4 skipped, sample_rate(16.16).
// That fixed layout holds for a v0 or v1 sound entry; a v2+ entry stores its
// geometry in a different structure (a float64 sample rate and a uint32 channel
// count at other offsets), so its version is checked first.
func parseStsd(src core.ReaderAtSized, stsd node, d *doc, limit int64) {
	b, err := readPayloadPrefix(src, stsd, 256, limit)
	if err != nil || len(b) < 44 {
		return
	}
	// The sound sample-entry version lives 16 bytes into the entry (which starts at
	// b[8]); the len>=44 guard above already covers b[24:26]. A v2+ entry is parseable,
	// but its geometry sits elsewhere - reading the v0/v1 offsets would feed bogus
	// channels/sample-rate into the essence-digest salt. This intentionally degrades the
	// reported properties (channels/sample-rate left unset) rather than misreading them;
	// stsd is still preserved verbatim on write, and the salt stays deterministic. >=2
	// (not ==2) so an unknown future version also skips rather than misparses.
	if version := binary.BigEndian.Uint16(b[24:26]); version >= 2 {
		copy(d.cfg.codec[:], b[12:16])
		d.track.Codec = string(d.cfg.codec[:])
		return
	}
	copy(d.cfg.codec[:], b[12:16])
	d.cfg.channels = binary.BigEndian.Uint16(b[32:34])
	d.cfg.sampleSize = binary.BigEndian.Uint16(b[34:36])
	d.cfg.sampleRate = uint32(binary.BigEndian.Uint16(b[40:42])) // integer part of 16.16
	d.track.Codec = string(d.cfg.codec[:])
	d.track.Channels = int(d.cfg.channels)
	d.track.BitsPerSample = int(d.cfg.sampleSize)
	d.track.SampleRate = int(d.cfg.sampleRate)
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
