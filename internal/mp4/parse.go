package mp4

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

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
	// default, so every bounded read still has a real ceiling now that ReadSlice requires
	// a positive limit.
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
	// atoms still read. mdat and moov get their own truncated-audio warnings below;
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
			// offsets, so a metadata resize would desynchronize the media.
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
		// the invalid-data "no moov box" diagnostic.
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

	// A gap between where moov's children end and moov.end() corrupts a create-ilst edit
	// the same way the meta gap (below) does: with no udta present, buildCreated appends
	// the new udta/meta/ilst at moov.end() (write.go, the default branch), but a re-parse
	// resolves moov's children at their recorded ends (earlier), so the inserted tag path
	// lands past a stray all-zero remainder and misaligns.
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
		// Capture the udta payload verbatim so a chapter rewrite can splice the new ilst/chpl
		// byte ranges into it while preserving every other user-data atom.
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
		// Classic QuickTime keeps its tags as direct udta children ("\xa9nam", "\xa9swr",
		// ...) with no meta wrapper. the two carry distinct source labels, so a disagreement
		// surfaces as a conflicting family rather than one silently winning.
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
// document: a resolved numeric genre and an inherited transcoder stamp (ffmpeg writes
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

// collectOffsetTables reads the offset tables in moov into the doc, parsing their
// entries so the writer can shift them when the metadata is resized without re-reading
// the source: the stco/co64 chunk offsets into d.offTables and the saio
// sample-auxiliary offsets into d.auxTables.
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

// hasIlocBox reports whether a meta box carries an iloc item-location box. The three
// placements the spec allows for a meta are checked (top level, moov, and moov.udta)
// rather than searching the tree by name, so an ilst item named "meta" cannot force a
// spurious refusal.
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
	// Report the edit-list-trimmed playable duration.
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

	// The sound sample-entry version lives 16 bytes into the entry (which starts at b[8]);
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
		if layout.Depth > 0 {
			d.track.BitsPerSample = layout.Depth
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
	if fixedLayout {
		if fourcc == "fpcm" && cfg.bitDepth == 0 {
			// No readable pcmC. The entry's samplesize is the fixed 16 every writer stores
			// there and no 16-bit float format exists, so report no width rather than one
			// that cannot be true.
			d.track.BitsPerSample = 0
		}
		// A v0 entry stores the rate as 16.16, which holds nothing above 65535, so ffmpeg
		// writes zero for a hi-res ISOBMFF ipcm/fpcm track.
		if d.track.SampleRate == 0 && timescale > 0 && timescale < math.MaxInt32 {
			d.track.SampleRate = int(timescale)
		}
	}
}

// parseSoundEntryV2 decodes a QuickTime version 2 sound sample entry's geometry onto
// d.track and returns where its extension boxes start. d.cfg is left zero: the digest
// salt has never carried v2 geometry, and filling it now would change every stored
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
// FullBox whose 8-byte header and 4 version/flags bytes are followed by format_flags
// and then PCM_sample_size. A width the entry's fourcc does not define is dropped
// rather than published.
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
// configuration's. its four-cc stands too unless the configuration names the codec more
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

// setEssence records the audio-essence byte ranges from the mdat atoms.
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

// essenceMdats returns the mdat ranges that hold media essence, as [start, end).
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
// range [r[0], r[1]), or -1 if the mdat holds no non-chapter chunk (chapter-only).
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
// alloc limit.
func readPayloadPrefix(src core.ReaderAtSized, n node, prefixLen, limit int64) ([]byte, error) {
	return bits.ReadSlice(src, n.payloadOff(), min(n.size-n.headerLen, prefixLen), limit)
}

// readPayloadWhole reads an atom's entire payload, failing with ErrSizeTooLarge when it
// exceeds capBytes rather than silently truncating it.
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
