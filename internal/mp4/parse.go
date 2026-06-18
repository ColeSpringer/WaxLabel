package mp4

import (
	"context"
	"encoding/binary"
	"fmt"
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
	limit := opts.Limits.MaxAllocBytes

	top, err := walkAtoms(src, 0, size, bits.NewDepth(opts.Limits.MaxDepth), limit, true)
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
			if b, err := readPayload(src, a, 4, limit); err == nil && len(b) >= 4 {
				d.majorBrand = string(b[:4])
			}
		case "moof", "styp":
			return nil, fmt.Errorf("%w: fragmented MP4 (moof) is not supported", waxerr.ErrUnsupportedFormat)
		case "mdat":
			d.mdats = append(d.mdats, [2]int64{a.payloadOff(), a.size - a.headerLen})
		case "moov":
			moov, haveMoov = a, true
		}
	}
	if !haveMoov {
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
			d.meta = refPtr(meta)
			if ilst, ok := meta.find("ilst"); ok {
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
	media.Properties = core.Properties{Container: "MP4", Tracks: []core.AudioTrack{d.track}}
	setEssence(d, media)
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// mediaWarnings returns the content-derived warnings for a parsed or rewritten
// document: a resolved numeric genre and an inherited transcoder stamp (ffmpeg
// writes "Lavf..." into the \xa9too / EncodedBy atom on acquired files). Sharing
// this lets the post-write document's warnings match a fresh parse of the output.
func mediaWarnings(tags tag.TagSet, numericGenre bool) []core.Warning {
	var ws []core.Warning
	if numericGenre {
		ws = core.Warn(ws, core.WarnNumericGenre, "a numeric genre reference was resolved to a name")
	}
	if vs, ok := tags.Get(tag.EncodedBy); ok {
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

// parseOffsetTable decodes one stco/co64 atom: a 4-byte version/flags, a 4-byte
// entry count, then that many 32- or 64-bit chunk offsets.
func parseOffsetTable(src core.ReaderAtSized, a node, co64 bool, limit int64) (offsetTable, error) {
	body, err := readPayload(src, a, maxMetaChunk, limit)
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
	if 8+count*width > int64(len(body)) {
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
		payload, err := readPayload(src, c, maxMetaChunk, limit)
		if err != nil {
			return err
		}
		d.items = append(d.items, item{name: c.name, payload: payload})
	}
	return nil
}

// adjacentFree returns the free atom immediately before or after ilst within
// meta (the only reusable padding this codec considers), or nil. Mirrors
// iTunes/mutagen, which keep padding next to the tag list.
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
	if idx > 0 && meta.children[idx-1].id() == "free" {
		r := refOf(meta.children[idx-1])
		return &r
	}
	if idx+1 < len(meta.children) && meta.children[idx+1].id() == "free" {
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
	b, err := readPayload(src, hdlr, 12, limit)
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
func parseStsd(src core.ReaderAtSized, stsd node, d *doc, limit int64) {
	b, err := readPayload(src, stsd, 256, limit)
	if err != nil || len(b) < 44 {
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

// essenceMdats returns the mdat ranges that hold audio, as [start, end). It drops
// a separate chapter-sample mdat - the one a QuickTime chapter write appends at
// end-of-file - so the change-detection fingerprint keeps hashing any metadata (a
// trailing moov) that would otherwise sit in the un-hashed gap between the audio
// mdat and the appended chapter mdat. The fast common path (no chapter track, or a
// single mdat whose chapter samples share the audio) returns every mdat.
func essenceMdats(d *doc) [][2]int64 {
	ranges := make([][2]int64, len(d.mdats))
	for i, m := range d.mdats {
		ranges[i] = [2]int64{m[0], m[0] + m[1]}
	}
	if d.chapTrak == nil || d.audioTrak == nil || len(ranges) <= 1 {
		return ranges
	}
	chapOff, ok := chapterChunkOffset(d)
	maxAudio, ok2 := maxAudioChunkOffset(d)
	if !ok || !ok2 || chapOff <= maxAudio {
		return ranges // chapter samples lie within the audio (e.g. ffmpeg's layout)
	}
	out := ranges[:0:0]
	for _, r := range ranges {
		if r[0] <= chapOff && chapOff < r[1] {
			continue // the standalone chapter mdat
		}
		out = append(out, r)
	}
	return out
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

// chapterChunkOffset returns the chapter text track's (single) chunk offset.
func chapterChunkOffset(d *doc) (int64, bool) {
	t, ok := trackOffTable(d, d.chapTrak)
	if !ok || len(t.entries) == 0 {
		return 0, false
	}
	return int64(t.entries[0]), true
}

// maxAudioChunkOffset returns the largest chunk offset of the audio track, used to
// tell a chapter mdat that follows the audio (a separate appended mdat) from
// chapter samples interleaved within the audio mdat.
func maxAudioChunkOffset(d *doc) (int64, bool) {
	t, ok := trackOffTable(d, d.audioTrak)
	if !ok || len(t.entries) == 0 {
		return 0, false
	}
	var mx uint64
	for _, e := range t.entries {
		if e > mx {
			mx = e
		}
	}
	return int64(mx), true
}

// readPayload reads up to capBytes of an atom's payload, bounded by the alloc
// limit. The small leaf-atom decoders share it; it never reads a large mdat
// (only its range is recorded).
func readPayload(src core.ReaderAtSized, n node, capBytes, limit int64) ([]byte, error) {
	return bits.ReadSlice(src, n.payloadOff(), min(n.size-n.headerLen, capBytes), limit)
}

// refOf / refPtr capture a node as a lightweight atomRef for the writer.
func refOf(n node) atomRef {
	return atomRef{name: n.name, offset: n.offset, headerLen: n.headerLen, size: n.size}
}

func refPtr(n node) *atomRef {
	r := refOf(n)
	return &r
}
