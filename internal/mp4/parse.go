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
// reads into memory. The mdat media payload is never read here — only its range
// is recorded — so this guards the small structural atoms (plus cover art, whose
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

	top, err := walkAtoms(src, 0, size, bits.NewDepth(opts.Limits.MaxDepth), limit)
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
	if udta, ok := moov.find("udta"); ok {
		d.udta = refPtr(udta)
		if chpl, ok := udta.find("chpl"); ok {
			d.chapters = parseChplCount(src, chpl, limit)
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

	media := &core.Media{Format: core.FormatMP4, Native: d}
	tags, pics, families, numericGenre := project(d)
	media.Tags = tags
	media.Pictures = pics
	media.Families = families
	media.Warnings = mediaWarnings(tags, numericGenre)
	media.Properties = core.Properties{Container: "MP4", Tracks: []core.AudioTrack{d.track}}
	setEssence(d, media)
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// mediaWarnings returns the content-derived warnings for a parsed or rewritten
// document: a resolved numeric genre and an inherited transcoder stamp (ffmpeg
// writes "Lavf..." into the ©too / EncodedBy atom on acquired files). Sharing
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
	// co64) overflows a 32-bit int — the body bound caps count so the allocation
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

// parseMdhd returns the media duration from a mdhd atom (timescale + duration,
// version 0 or 1).
func parseMdhd(src core.ReaderAtSized, mdhd node, limit int64) (time.Duration, bool) {
	b, err := readPayload(src, mdhd, 32, limit)
	if err != nil || len(b) < 4 {
		return 0, false
	}
	var timescale, duration uint64
	switch b[0] {
	case 0:
		if len(b) < 20 {
			return 0, false
		}
		timescale = uint64(binary.BigEndian.Uint32(b[12:16]))
		duration = uint64(binary.BigEndian.Uint32(b[16:20]))
	case 1:
		if len(b) < 32 {
			return 0, false
		}
		timescale = uint64(binary.BigEndian.Uint32(b[20:24]))
		duration = binary.BigEndian.Uint64(b[24:32])
	default:
		return 0, false
	}
	if timescale == 0 {
		return 0, false
	}
	secs := float64(duration) / float64(timescale)
	if secs <= 0 || secs >= float64(math.MaxInt64)/float64(time.Second) {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
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

// parseChplCount returns the number of chapters declared in a Nero chpl atom
// (for the native view only; the chapter data is preserved verbatim on rewrite).
// The payload is [1 version][3 flags][4 reserved][1 count][entries...], so the
// count is the ninth byte.
func parseChplCount(src core.ReaderAtSized, chpl node, limit int64) int {
	b, err := readPayload(src, chpl, 9, limit)
	if err != nil || len(b) < 9 {
		return 0
	}
	return int(b[8])
}

// setEssence records the audio-essence byte ranges from the mdat atoms. A single
// mdat uses the contiguous [AudioStart, AudioEnd) extent (which gives the best
// change-detection fingerprint, covering metadata both before and after the
// media); multiple mdats use the multi-segment AudioRanges.
func setEssence(d *doc, media *core.Media) {
	switch len(d.mdats) {
	case 0:
		// metadata-only file: no essence (the fingerprint then hashes it whole).
	case 1:
		media.AudioStart = d.mdats[0][0]
		media.AudioEnd = d.mdats[0][0] + d.mdats[0][1]
	default:
		ranges := make([][2]int64, len(d.mdats))
		for i, m := range d.mdats {
			ranges[i] = [2]int64{m[0], m[0] + m[1]}
		}
		media.AudioRanges = ranges
		media.AudioStart = ranges[0][0]
		media.AudioEnd = ranges[len(ranges)-1][1]
	}
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
