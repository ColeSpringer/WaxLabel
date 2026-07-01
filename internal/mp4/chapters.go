package mp4

import (
	"encoding/binary"
	"math"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// Two chapter representations live in an MP4: a Nero "chpl" list under
// moov.udta, and a QuickTime chapter "text" track referenced from the audio
// track via a tref "chap". Both project into one []core.Chapter. The reader here
// decodes both; a chapter edit rewrites both (the chpl in write_chapters.go, the
// QuickTime track in write_qtchapters.go).

// chplStartUnit is the chpl start-time resolution: 1/10,000,000 second (100 ns),
// per the Nero convention ffmpeg's mov_read_chpl follows.
const chplStartUnit = 10_000_000

// maxChapterSamples bounds the QuickTime sample-table walk so a crafted text
// track cannot make a metadata read iterate unboundedly. Real chapter tracks
// hold a handful of samples; this is far above any plausible count.
const maxChapterSamples = 1 << 16

// titleByteMax is the chpl per-title cap: its length prefix is a single byte, so
// a title cannot exceed 255 bytes on write.
const titleByteMax = 255

// resolveChapters decodes both chapter representations into the doc's projected
// list and records the structural facts the writer needs (the chpl version to
// preserve, whether a QuickTime track is present). It returns whether the two
// sources disagree, so parse can raise WarnChapterSourceConflict.
func resolveChapters(src core.ReaderAtSized, moov, chpl node, haveChpl bool, d *doc, limit int64) (conflict bool) {
	d.chplVersion = 1 // ffmpeg's form; used if a chpl is later created from scratch
	var chplChapters []core.Chapter
	if haveChpl {
		if ver, chs, ok := decodeChpl(src, chpl, limit); ok && len(chs) > 0 {
			d.chplVersion = ver
			d.chplCount = len(chs)
			chplChapters = chs
		} else {
			// An empty or unparsable chpl is treated as no chpl, so it does not
			// spuriously conflict with a real QuickTime chapter track.
			haveChpl = false
		}
	}
	qt, haveQT := decodeQTChapters(src, moov, limit)
	d.hasQTChapters = haveQT
	d.chapters, conflict = mergeChapters(chplChapters, haveChpl, qt, haveQT)
	d.chapterConflict = conflict
	return conflict
}

// chplHasReserved reports whether a Nero chpl atom carries the 32-bit reserved
// field before the chapter count. ffmpeg's mov_read_chpl skips that field for
// any non-zero version; no public chpl v2+ spec is known. decodeChpl and
// renderChpl share this predicate so reads and writes stay symmetric.
func chplHasReserved(version uint8) bool { return version != 0 }

// decodeChpl parses a Nero chpl atom into chapters, supporting both versions:
// version(1) + flags(3), then a reserved 4-byte field (see chplHasReserved), then an 8-bit
// chapter count, then each entry as a 64-bit 100 ns start plus a length-prefixed UTF-8 title.
// The 8-bit count caps chpl at 255 chapters. It returns ok==false on any malformation so the
// caller treats the file as carrying no chpl chapters.
func decodeChpl(src core.ReaderAtSized, chpl node, limit int64) (version uint8, chapters []core.Chapter, ok bool) {
	b, err := readPayloadWhole(src, chpl, maxMetaChunk, limit)
	if err != nil {
		return 0, nil, false
	}
	n := int64(len(b))
	if n < 5 {
		return 0, nil, false
	}
	version = b[0]
	pos := int64(4) // version(1) + flags(3)
	if chplHasReserved(version) {
		pos += 4 // skip the reserved 32-bit field
	}
	if pos+1 > n {
		return 0, nil, false
	}
	count := int64(b[pos])
	pos++
	chapters = make([]core.Chapter, 0, count)
	for i := int64(0); i < count; i++ {
		if pos+9 > n { // 8-byte start + 1-byte length
			return 0, nil, false
		}
		start := binary.BigEndian.Uint64(b[pos : pos+8])
		pos += 8
		titleLen := int64(b[pos])
		pos++
		if pos+titleLen > n {
			return 0, nil, false
		}
		// Match the QuickTime path: an invalid-UTF-8 title yields an empty title
		// (not invalid bytes that a later JSON dump would mangle), so both chapter
		// sources behave the same.
		title := ""
		if raw := b[pos : pos+titleLen]; utf8.Valid(raw) {
			title = string(raw)
		}
		pos += titleLen
		chapters = append(chapters, core.Chapter{Start: scaleToDuration(start, chplStartUnit), Title: title})
	}
	fillChapterEnds(chapters)
	return version, chapters, true
}

// renderChpl encodes chapters into a Nero chpl atom, preserving the parsed
// version (defaulting to 1, the form ffmpeg writes). The caller guarantees at
// most 255 chapters; each title is truncated to 255 bytes on a UTF-8 boundary
// because the length prefix is a single byte.
func renderChpl(version uint8, chapters []core.Chapter) []byte {
	payload := make([]byte, 0, 8+len(chapters)*16)
	payload = append(payload, version, 0, 0, 0) // version + flags
	if chplHasReserved(version) {
		payload = append(payload, 0, 0, 0, 0) // reserved 32-bit field
	}
	payload = append(payload, byte(len(chapters)))
	for _, ch := range chapters {
		var start [8]byte
		binary.BigEndian.PutUint64(start[:], durationToUnits(ch.Start, chplStartUnit))
		payload = append(payload, start[:]...)
		title := truncateUTF8(ch.Title, titleByteMax)
		payload = append(payload, byte(len(title)))
		payload = append(payload, title...)
	}
	return renderAtom(atomName("chpl"), payload)
}

// collectChapterRefs captures the structural references a QuickTime chapter
// write needs to rebuild that track without re-reading the source: the audio
// track to hang a tref on, where its mdia begins (the tref insertion point), any
// existing tref, the existing chapter text track to replace, and the movie
// header fields (timescale/duration and a free track id) a new track is built
// from. It is read-only and tolerant - a field it cannot resolve is left zero
// and the writer falls back to a chpl-only write when the audio track is absent.
func collectChapterRefs(src core.ReaderAtSized, moov node, d *doc, limit int64) {
	traks := moov.findAll("trak", nil)
	if mvhd, ok := moov.find("mvhd"); ok {
		d.mvhd = refPtr(mvhd)
		collectMvhd(src, mvhd, d, limit)
	}
	// A track id free of every existing track, used when mvhd's next_track_ID is
	// missing, the all-ones sentinel, or stale (not actually past every track).
	// nextTrackID 0 means none is free (a track already holds the max id), so a new
	// chapter track cannot be created - maxID+1 would wrap to the invalid id 0.
	maxID := uint32(0)
	for _, t := range traks {
		if tkhd, ok := t.find("tkhd"); ok {
			if id, ok := trackID(src, tkhd, limit); ok && id > maxID {
				maxID = id
			}
		}
	}
	switch {
	case maxID == 0xFFFFFFFF:
		d.nextTrackID = 0
	case d.nextTrackID == 0 || d.nextTrackID == 0xFFFFFFFF || d.nextTrackID <= maxID:
		d.nextTrackID = maxID + 1
	}

	audio, ok := trakOfHandler(src, traks, "soun", limit)
	if !ok {
		return
	}
	d.audioTrak = refPtr(audio)
	if mdia, ok := audio.find("mdia"); ok {
		d.audioMdiaOff = mdia.offset
	}
	if tref, ok := audio.find("tref"); ok {
		d.audioTref = refPtr(tref)
		if raw, err := readPayloadWhole(src, tref, maxMetaChunk, limit); err == nil {
			d.audioTrefRaw = raw
		}
	}
	if ids := chapterTrackIDs(src, audio, limit); len(ids) > 0 {
		d.audioHasChap = true // a "chap" reference exists, even if it does not resolve
		if text, ok := trakByID(src, traks, ids, limit); ok {
			d.chapTrak = refPtr(text)
			if tkhd, ok := text.find("tkhd"); ok {
				if id, ok := trackID(src, tkhd, limit); ok {
					d.chapTrackID = id // reused when the track is rebuilt in place
				}
			}
		}
	}
}

// collectMvhd reads the movie header's timescale, duration, and next_track_ID:
// a new chapter track shares the movie timescale, ends its last chapter at the
// movie duration, and takes next_track_ID as its track id. The field's absolute
// offset is recorded so a created track can bump it.
func collectMvhd(src core.ReaderAtSized, mvhd node, d *doc, limit int64) {
	b, err := readPayloadPrefix(src, mvhd, 120, limit)
	if err != nil || len(b) < 1 {
		return
	}
	po := mvhd.payloadOff()
	switch b[0] {
	case 0:
		if len(b) < 100 {
			return
		}
		d.movieTimescale = binary.BigEndian.Uint32(b[12:16])
		d.movieDuration = sentinelToZero64(uint64(binary.BigEndian.Uint32(b[16:20])), 0xFFFFFFFF)
		d.nextTrackID = binary.BigEndian.Uint32(b[96:100])
		d.nextTrackIDOff = po + 96
	case 1:
		if len(b) < 112 {
			return
		}
		d.movieTimescale = binary.BigEndian.Uint32(b[20:24])
		d.movieDuration = sentinelToZero64(binary.BigEndian.Uint64(b[24:32]), 0xFFFFFFFFFFFFFFFF)
		d.nextTrackID = binary.BigEndian.Uint32(b[108:112])
		d.nextTrackIDOff = po + 108
	}
}

// sentinelToZero64 maps an MP4 "unknown duration" all-ones sentinel to zero so a
// chapter write does not give the final chapter a multi-week span. A real zero is
// already "unknown" to the chapter-span logic, so collapsing the two is safe.
func sentinelToZero64(v, sentinel uint64) uint64 {
	if v == sentinel {
		return 0
	}
	return v
}

// decodeQTChapters reads a QuickTime chapter text track: it resolves the audio
// track's tref "chap" reference to a text track, walks that track's sample
// tables, and decodes each text sample (a 16-bit length prefix plus UTF-8) into
// a chapter. It returns ok==false (no QuickTime chapters) on anything unexpected.
//
// It uses the first audio ("soun") track's reference, consistent with the rest of
// the codec (which reads properties from the first audio track). A chapter track
// referenced only by a secondary audio track - a rare multi-audio-track file - is
// not resolved.
func decodeQTChapters(src core.ReaderAtSized, moov node, limit int64) ([]core.Chapter, bool) {
	traks := moov.findAll("trak", nil) // collected once, scanned for both the audio and text track
	audio, ok := trakOfHandler(src, traks, "soun", limit)
	if !ok {
		return nil, false
	}
	ids := chapterTrackIDs(src, audio, limit)
	if len(ids) == 0 {
		return nil, false
	}
	text, ok := trakByID(src, traks, ids, limit)
	if !ok {
		return nil, false
	}
	// A chapter text track's stts decode times always run from zero, so a first
	// chapter that starts after t=0 carries that offset in a leading empty edit in
	// the track's elst (the standard MP4 delayed-track form WaxLabel writes). Read it
	// and shift every chapter so the QuickTime starts are absolute - and thus agree
	// with the absolute chpl rather than self-reporting a source conflict. The movie
	// timescale is read locally because collectMvhd has not populated d.movieTimescale
	// at this point (resolveChapters runs before it).
	offset := chapterEditOffset(src, text, movieTimescaleOf(src, moov, limit), limit)
	return decodeTextTrack(src, text, offset, limit)
}

// movieTimescaleOf reads moov's mvhd movie timescale (version 0 at byte 12,
// version 1 at byte 20). It reads only the timescale, so it needs far fewer bytes
// than collectMvhd (which also reads next_track_ID); it is used at resolveChapters
// time, before collectMvhd runs. Returns 0 when absent, so the caller applies no
// edit-list offset.
func movieTimescaleOf(src core.ReaderAtSized, moov node, limit int64) uint32 {
	mvhd, ok := moov.find("mvhd")
	if !ok {
		return 0
	}
	b, err := readPayloadPrefix(src, mvhd, 32, limit)
	if err != nil || len(b) < 1 {
		return 0
	}
	switch b[0] {
	case 0:
		if len(b) >= 16 {
			return binary.BigEndian.Uint32(b[12:16])
		}
	case 1:
		if len(b) >= 24 {
			return binary.BigEndian.Uint32(b[20:24])
		}
	}
	return 0
}

// chapterEditOffset returns the presentation delay a leading empty edit in trak's
// elst imposes - the standard MP4 way a chapter track whose first chapter starts
// after t=0 is positioned (its stts decode times run from zero, so the start lives
// in the edit list). It returns 0 when there is no edit list, the first entry is a
// normal edit (media_time != -1), or the movie timescale is unknown, so a foreign
// track without an empty edit reads unchanged. edts is a leaf to the atom walker
// (not in containerAtoms), so its elst is parsed by hand, mirroring chapterTrackIDs'
// tref scan. An elst segment_duration is in movie-timescale units, not the chapter
// track's media timescale.
func chapterEditOffset(src core.ReaderAtSized, trak node, movieTimescale uint32, limit int64) time.Duration {
	if movieTimescale == 0 {
		return 0
	}
	edts, ok := trak.find("edts")
	if !ok {
		return 0
	}
	b, err := readPayloadWhole(src, edts, maxMetaChunk, limit)
	if err != nil {
		return 0
	}
	n := int64(len(b))
	for pos := int64(0); pos+8 <= n; {
		size := int64(binary.BigEndian.Uint32(b[pos : pos+4]))
		// size < 8 is the 64-bit-size form (the 32-bit field reads 1, the real size
		// following the type) or the size-0 "to end of box" form. A real elst is a few
		// 12/20-byte entries and uses neither, so stop and report no offset rather than
		// grow this by-hand scan for a case that cannot occur on a chapter track.
		if size < 8 || pos+size > n {
			break
		}
		if string(b[pos+4:pos+8]) == "elst" {
			return emptyEditOffset(b[pos+8:pos+size], movieTimescale)
		}
		pos += size
	}
	return 0
}

// emptyEditOffset reads an elst box payload (version/flags, entry_count, then the
// entries) and, when the first entry is an empty edit (media_time == -1), returns
// its segment_duration scaled by the movie timescale; a normal first entry yields
// 0. It handles version 0 (u32 segment_duration, i32 media_time) and version 1
// (u64, i64).
func emptyEditOffset(p []byte, movieTimescale uint32) time.Duration {
	if len(p) < 8 || binary.BigEndian.Uint32(p[4:8]) == 0 { // version/flags + entry_count
		return 0
	}
	switch p[0] {
	case 0:
		if len(p) < 8+12 || int32(binary.BigEndian.Uint32(p[12:16])) != -1 { // media_time
			return 0
		}
		return scaleToDuration(uint64(binary.BigEndian.Uint32(p[8:12])), movieTimescale)
	case 1:
		if len(p) < 8+20 || int64(binary.BigEndian.Uint64(p[16:24])) != -1 { // media_time
			return 0
		}
		return scaleToDuration(binary.BigEndian.Uint64(p[8:16]), movieTimescale)
	}
	return 0
}

// trackEditedDuration returns trak's total edit-list playable duration, or 0 when the
// track has no edit list. This is the track's own trimmed length, not the movie duration;
// for AAC, it removes encoder priming that is still present in the raw mdhd duration.
func trackEditedDuration(src core.ReaderAtSized, trak node, movieTimescale uint32, limit int64) time.Duration {
	if movieTimescale == 0 {
		return 0
	}
	edts, ok := trak.find("edts")
	if !ok {
		return 0
	}
	b, err := readPayloadWhole(src, edts, maxMetaChunk, limit)
	if err != nil {
		return 0
	}
	n := int64(len(b))
	for pos := int64(0); pos+8 <= n; {
		size := int64(binary.BigEndian.Uint32(b[pos : pos+4]))
		if size < 8 || pos+size > n { // 64-bit/size-0 forms do not occur on a real elst
			break
		}
		if string(b[pos+4:pos+8]) == "elst" {
			return scaleToDuration(elstSegmentDurationSum(b[pos+8:pos+size]), movieTimescale)
		}
		pos += size
	}
	return 0
}

// elstSegmentDurationSum sums the segment_duration field of every edit-list entry (in
// movie-timescale units). A v0 entry is 12 bytes (segment_duration u32, media_time i32,
// rate u32); a v1 entry is 20 bytes (u64, i64, u32). The declared entry_count is bounded
// against the payload up front (boundedCount), like the sibling sample-table decoders, so a
// hostile count cannot drive the loop past the bytes actually present.
func elstSegmentDurationSum(p []byte) uint64 {
	if len(p) < 8 {
		return 0
	}
	count := int64(binary.BigEndian.Uint32(p[4:8]))
	var sum uint64
	switch p[0] {
	case 0:
		if !boundedCount(count, 8, 12, int64(len(p))) {
			return 0
		}
		for i := int64(0); i < count; i++ {
			off := 8 + i*12
			sum += uint64(binary.BigEndian.Uint32(p[off : off+4]))
		}
	case 1:
		if !boundedCount(count, 8, 20, int64(len(p))) {
			return 0
		}
		for i := int64(0); i < count; i++ {
			off := 8 + i*20
			sum += binary.BigEndian.Uint64(p[off : off+8])
		}
	}
	return sum
}

// trakOfHandler returns the first trak whose media handler matches want (e.g.
// "soun" for audio, "text" for a chapter track).
func trakOfHandler(src core.ReaderAtSized, traks []node, want string, limit int64) (node, bool) {
	for _, t := range traks {
		mdia, ok := t.find("mdia")
		if !ok {
			continue
		}
		if hdlr, ok := mdia.find("hdlr"); ok && handlerType(src, hdlr, limit) == want {
			return t, true
		}
	}
	return node{}, false
}

// chapterTrackIDs returns the track IDs referenced by the audio track's tref
// "chap" entry (the QuickTime way an audio track points at its chapter track).
// tref is a leaf to the atom walker, so its sub-atoms are parsed here by hand.
func chapterTrackIDs(src core.ReaderAtSized, trak node, limit int64) []uint32 {
	tref, ok := trak.find("tref")
	if !ok {
		return nil
	}
	body, err := readPayloadWhole(src, tref, maxMetaChunk, limit)
	if err != nil {
		return nil
	}
	n := int64(len(body))
	for pos := int64(0); pos+8 <= n; {
		size := int64(binary.BigEndian.Uint32(body[pos : pos+4]))
		if size < 8 || pos+size > n {
			break
		}
		if string(body[pos+4:pos+8]) == "chap" {
			var ids []uint32
			for off := pos + 8; off+4 <= pos+size; off += 4 {
				ids = append(ids, binary.BigEndian.Uint32(body[off:off+4]))
			}
			return ids
		}
		pos += size
	}
	return nil
}

// trakByID returns the trak whose tkhd track_id is one of ids.
func trakByID(src core.ReaderAtSized, traks []node, ids []uint32, limit int64) (node, bool) {
	for _, t := range traks {
		tkhd, ok := t.find("tkhd")
		if !ok {
			continue
		}
		id, ok := trackID(src, tkhd, limit)
		if !ok {
			continue
		}
		for _, want := range ids {
			if id == want {
				return t, true
			}
		}
	}
	return node{}, false
}

// trackID reads the track_id from a tkhd atom (version 0 places it at byte 12;
// version 1's 64-bit times push it to byte 20).
func trackID(src core.ReaderAtSized, tkhd node, limit int64) (uint32, bool) {
	b, err := readPayloadPrefix(src, tkhd, 32, limit)
	if err != nil || len(b) < 1 {
		return 0, false
	}
	off := 12
	if b[0] == 1 {
		off = 20
	}
	if len(b) < off+4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[off : off+4]), true
}

// decodeTextTrack reconstructs chapters from a text track's sample tables: the
// per-sample decode time (stts) gives each chapter's start, and the sample's
// bytes in mdat (located via stsc/stsz/stco|co64) carry its title. offset is the
// edit-list presentation delay (0 when there is none), added to every start and
// closed End so the chapter times are absolute; the last chapter's open End (0)
// stays open.
func decodeTextTrack(src core.ReaderAtSized, trak node, offset time.Duration, limit int64) ([]core.Chapter, bool) {
	mdia, ok := trak.find("mdia")
	if !ok {
		return nil, false
	}
	mdhd, ok := mdia.find("mdhd")
	if !ok {
		return nil, false
	}
	timescale, ok := mdhdTimescale(src, mdhd, limit)
	if !ok {
		return nil, false
	}
	minf, ok := mdia.find("minf")
	if !ok {
		return nil, false
	}
	stbl, ok := minf.find("stbl")
	if !ok {
		return nil, false
	}

	times, ok := sampleTimes(src, stbl, limit)
	if !ok || len(times) == 0 {
		return nil, false
	}
	offsets, ok := sampleOffsets(src, stbl, len(times), limit)
	if !ok {
		return nil, false
	}

	chapters := make([]core.Chapter, 0, len(offsets))
	for i, so := range offsets {
		title := readTextSample(src, so.off, so.size, limit)
		ch := core.Chapter{Start: addClamp(scaleToDuration(times[i], timescale), offset), Title: title}
		if i+1 < len(times) {
			ch.End = addClamp(scaleToDuration(times[i+1], timescale), offset)
		}
		chapters = append(chapters, ch)
	}
	return chapters, true
}

// sampleOffset is one decoded sample's file location and byte length.
type sampleOffset struct {
	off  int64
	size int64
}

// sampleTimes returns each sample's cumulative decode time (in the media
// timescale) from the stts table.
func sampleTimes(src core.ReaderAtSized, stbl node, limit int64) ([]uint64, bool) {
	stts, ok := stbl.find("stts")
	if !ok {
		return nil, false
	}
	b, err := readPayloadWhole(src, stts, maxMetaChunk, limit)
	if err != nil || len(b) < 8 {
		return nil, false
	}
	count := int64(binary.BigEndian.Uint32(b[4:8]))
	if !boundedCount(count, 8, 8, int64(len(b))) {
		return nil, false
	}
	var times []uint64
	var t uint64
	for i := int64(0); i < count; i++ {
		o := 8 + i*8
		n := binary.BigEndian.Uint32(b[o : o+4])
		delta := binary.BigEndian.Uint32(b[o+4 : o+8])
		for j := uint32(0); j < n; j++ {
			if len(times) >= maxChapterSamples {
				return times, true
			}
			times = append(times, t)
			t += uint64(delta)
		}
	}
	return times, true
}

// sampleOffsets locates each of the first nSamples samples in mdat by combining
// the sample sizes (stsz), the sample-to-chunk map (stsc), and the chunk offsets
// (stco/co64).
func sampleOffsets(src core.ReaderAtSized, stbl node, nSamples int, limit int64) ([]sampleOffset, bool) {
	sizes, ok := sampleSizes(src, stbl, nSamples, limit)
	if !ok {
		return nil, false
	}
	chunks, ok := chunkOffsets(src, stbl, limit)
	if !ok || len(chunks) == 0 {
		return nil, false
	}
	stsc, ok := stscEntries(src, stbl, limit)
	if !ok {
		return nil, false
	}
	perChunk := expandStsc(stsc, len(chunks))

	out := make([]sampleOffset, 0, len(sizes))
	si := 0
	for c := 0; c < len(chunks) && si < len(sizes); c++ {
		off := int64(chunks[c])
		for k := uint32(0); k < perChunk[c] && si < len(sizes); k++ {
			sz := int64(sizes[si])
			out = append(out, sampleOffset{off: off, size: sz})
			off += sz
			si++
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// sampleSizes returns the size of each of the first nSamples samples from stsz
// (a single shared size, or a per-sample table).
func sampleSizes(src core.ReaderAtSized, stbl node, nSamples int, limit int64) ([]uint32, bool) {
	stsz, ok := stbl.find("stsz")
	if !ok {
		return nil, false
	}
	b, err := readPayloadWhole(src, stsz, maxMetaChunk, limit)
	if err != nil || len(b) < 12 {
		return nil, false
	}
	shared := binary.BigEndian.Uint32(b[4:8])
	count := int64(binary.BigEndian.Uint32(b[8:12]))
	if count > int64(nSamples) {
		count = int64(nSamples)
	}
	if count > maxChapterSamples {
		count = maxChapterSamples
	}
	sizes := make([]uint32, count)
	if shared != 0 {
		for i := range sizes {
			sizes[i] = shared
		}
		return sizes, true
	}
	if !boundedCount(count, 12, 4, int64(len(b))) {
		return nil, false
	}
	for i := int64(0); i < count; i++ {
		o := 12 + i*4
		sizes[i] = binary.BigEndian.Uint32(b[o : o+4])
	}
	return sizes, true
}

// chunkOffsets returns the chunk file offsets from stco (32-bit) or co64 (64-bit).
func chunkOffsets(src core.ReaderAtSized, stbl node, limit int64) ([]uint64, bool) {
	if stco, ok := stbl.find("stco"); ok {
		t, err := parseOffsetTable(src, stco, false, limit)
		if err != nil {
			return nil, false
		}
		return t.entries, true
	}
	if co64, ok := stbl.find("co64"); ok {
		t, err := parseOffsetTable(src, co64, true, limit)
		if err != nil {
			return nil, false
		}
		return t.entries, true
	}
	return nil, false
}

// stscEntries parses the sample-to-chunk table (first_chunk, samples_per_chunk;
// the sample-description index is not needed here).
func stscEntries(src core.ReaderAtSized, stbl node, limit int64) ([]stscEntry, bool) {
	stsc, ok := stbl.find("stsc")
	if !ok {
		return nil, false
	}
	b, err := readPayloadWhole(src, stsc, maxMetaChunk, limit)
	if err != nil || len(b) < 8 {
		return nil, false
	}
	count := int64(binary.BigEndian.Uint32(b[4:8]))
	if !boundedCount(count, 8, 12, int64(len(b))) {
		return nil, false
	}
	out := make([]stscEntry, count)
	for i := int64(0); i < count; i++ {
		o := 8 + i*12
		out[i] = stscEntry{
			firstChunk:      binary.BigEndian.Uint32(b[o : o+4]),
			samplesPerChunk: binary.BigEndian.Uint32(b[o+4 : o+8]),
		}
	}
	return out, true
}

// stscEntry is one sample-to-chunk run: the first chunk it applies to (1-based)
// and how many samples each such chunk holds.
type stscEntry struct {
	firstChunk      uint32
	samplesPerChunk uint32
}

// expandStsc resolves the per-chunk sample count for every chunk from the run-
// length stsc entries.
func expandStsc(entries []stscEntry, nChunks int) []uint32 {
	out := make([]uint32, nChunks)
	if len(entries) == 0 {
		return out
	}
	ei := 0
	for c := 0; c < nChunks; c++ {
		for ei+1 < len(entries) && int(entries[ei+1].firstChunk) <= c+1 {
			ei++
		}
		if int(entries[ei].firstChunk) <= c+1 {
			out[c] = entries[ei].samplesPerChunk
		}
	}
	return out
}

// readTextSample reads a QuickTime text/tx3g sample and returns its title: a
// 16-bit big-endian length prefix followed by that many UTF-8 bytes (trailing
// style atoms, if any, are ignored). Invalid UTF-8 yields an empty title.
func readTextSample(src core.ReaderAtSized, off, size, limit int64) string {
	if size < 2 {
		return ""
	}
	b, err := bits.ReadSlice(src, off, min(size, maxMetaChunk), limit)
	if err != nil || len(b) < 2 {
		return ""
	}
	textLen := int64(binary.BigEndian.Uint16(b[0:2]))
	if 2+textLen > int64(len(b)) {
		textLen = int64(len(b)) - 2
	}
	title := b[2 : 2+textLen]
	if !utf8.Valid(title) {
		return ""
	}
	return string(title)
}

// mergeChapters projects the two MP4 chapter representations into one list. The
// QuickTime track is preferred when present (it carries End); when both exist and
// disagree, the disagreement is flagged so the caller can warn.
func mergeChapters(chpl []core.Chapter, haveChpl bool, qt []core.Chapter, haveQT bool) (chapters []core.Chapter, conflict bool) {
	switch {
	case haveQT && haveChpl:
		return qt, !chaptersAgree(chpl, qt)
	case haveQT:
		return qt, false
	case haveChpl:
		return chpl, false
	default:
		return nil, false
	}
}

// chaptersAgree reports whether two chapter lists describe the same chapters:
// equal count, equal titles, and starts within a small tolerance (the two
// representations use different time bases, so exact equality is too strict).
func chaptersAgree(a, b []core.Chapter) bool {
	if len(a) != len(b) {
		return false
	}
	const tol = 500 * time.Millisecond
	for i := range a {
		if a[i].Title != b[i].Title {
			return false
		}
		d := a[i].Start - b[i].Start
		if d < -tol || d > tol {
			return false
		}
	}
	return true
}

// fillChapterEnds sets each chapter's End to the next chapter's Start when End is
// unset, so a start-only source (chpl) still yields closed intervals (the last
// chapter's End stays zero - "until end of file"). It only fills when the next
// start is later, so an out-of-order chapter list does not produce a degenerate
// End < Start.
func fillChapterEnds(chs []core.Chapter) {
	for i := range chs {
		if chs[i].End == 0 && i+1 < len(chs) && chs[i+1].Start > chs[i].Start {
			chs[i].End = chs[i+1].Start
		}
	}
}

// mdhdFields decodes a mdhd atom's media timescale and duration (version 0 or 1).
// It is shared by the audio-property duration read and the chapter-track timescale
// read so the field layout lives in one place.
func mdhdFields(src core.ReaderAtSized, mdhd node, limit int64) (timescale uint32, duration uint64, ok bool) {
	b, err := readPayloadPrefix(src, mdhd, 32, limit)
	if err != nil || len(b) < 4 {
		return 0, 0, false
	}
	switch b[0] {
	case 0:
		if len(b) < 20 {
			return 0, 0, false
		}
		return binary.BigEndian.Uint32(b[12:16]), uint64(binary.BigEndian.Uint32(b[16:20])), true
	case 1:
		if len(b) < 32 {
			return 0, 0, false
		}
		return binary.BigEndian.Uint32(b[20:24]), binary.BigEndian.Uint64(b[24:32]), true
	default:
		return 0, 0, false
	}
}

// mdhdTimescale reads the media timescale from a mdhd atom. A zero timescale is
// rejected: it is invalid and would otherwise collapse every chapter time to zero
// rather than failing the track over to no QuickTime chapters.
func mdhdTimescale(src core.ReaderAtSized, mdhd node, limit int64) (uint32, bool) {
	ts, _, ok := mdhdFields(src, mdhd, limit)
	return ts, ok && ts != 0
}

// addClamp returns a + b saturated at MaxInt64, so adding the edit-list offset to
// a per-sample time preserves scaleToDuration's "clamp rather than overflow"
// guarantee end to end. Both operands are non-negative here (a duration from
// scaleToDuration and a non-negative empty-edit offset), so only positive overflow
// is possible: the sum wrapping below a signals it. Without this a hostile elst
// whose empty edit drives the offset to ~MaxInt64 could wrap a chapter Start
// negative on the parse path.
func addClamp(a, b time.Duration) time.Duration {
	if s := a + b; s >= a {
		return s
	}
	return time.Duration(math.MaxInt64)
}

// scaleToDuration converts a count of timescale units into a time.Duration,
// clamping rather than overflowing on absurd inputs. It rounds to the nearest
// nanosecond: a plain float-to-int conversion truncates, so floating-point error
// (e.g. a result of 1.9999999999 instead of 2.0) would drop a whole nanosecond.
func scaleToDuration(units uint64, timescale uint32) time.Duration {
	if timescale == 0 {
		return 0
	}
	secs := float64(units) / float64(timescale)
	if secs <= 0 {
		return 0
	}
	if secs >= float64(math.MaxInt64)/float64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(math.Round(secs * float64(time.Second)))
}

// durationToUnits is the inverse of scaleToDuration for encoding (e.g. chpl
// 100 ns units), clamping negatives to zero. It rounds to the nearest unit so a
// float result just under an integer is not truncated down to the prior unit.
func durationToUnits(d time.Duration, timescale uint32) uint64 {
	if d <= 0 {
		return 0
	}
	return uint64(math.Round(d.Seconds() * float64(timescale)))
}

// truncateUTF8 returns s trimmed to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
