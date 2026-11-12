package mp4

import (
	"cmp"
	"encoding/binary"
	"math"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// Two chapter representations live in an MP4: a Nero "chpl" list under moov.udta, and a
// QuickTime chapter text track that other tracks name in a tref "chap" entry.

// chplStartUnit is the chpl start-time resolution: 1/10,000,000 second (100 ns),
// per the Nero convention ffmpeg's mov_read_chpl follows.
const chplStartUnit = 10_000_000

// maxChapterSamples bounds the QuickTime sample-table walk so a crafted text track
// cannot make a metadata read iterate unboundedly. It is far above any plausible count.
const maxChapterSamples = 1 << 16

// titleByteMax is the chpl per-title cap: its length prefix is a single byte, so
// a title cannot exceed 255 bytes on write.
const titleByteMax = 255

// chapterMediaTimescale is the fixed media timescale WaxLabel writes the QuickTime
// chapter text track at, decoupled from the (often coarse) movie timescale.
const chapterMediaTimescale = 90_000

// resolveChapters decodes both chapter representations into the doc's projected list
// and records the structural facts the writer needs (the chpl version to preserve,
// whether a QuickTime track is present, the mdats its samples fill).
func resolveChapters(src core.ReaderAtSized, moov node, chpls []node, tr movieTracks, d *doc, limit int64) (conflict bool, err error) {
	d.chplVersion = 1 // ffmpeg's form; used if a chpl is later created from scratch
	// A Nero list comes from Windows tools and a chapter track from QuickTime; a track beside
	// a list of 8-bit titles holds the same bytes as the list.
	chplTitles := titleReader{page: &windows1252}
	chplChapters, err := pickChpl(src, chpls, d, &chplTitles, limit)
	if err != nil {
		return false, err
	}
	trackTitles := titleReader{page: &macRoman}
	if chplTitles.legacy {
		trackTitles.page = &windows1252
	}
	qt, haveQT, err := decodeQTChapters(src, moov, tr, &trackTitles, limit)
	if err != nil {
		return false, err
	}
	d.hasQTChapters = haveQT
	if haveQT {
		d.chapMdats = chapterOnlyMdats(qt.samples, d.mdats)
	}
	// A chpl with no readable chapters counts as none, so it cannot conflict with a real
	// QuickTime chapter track.
	haveChpl := len(chplChapters) > 0
	d.chapters, conflict = mergeChapters(chplChapters, haveChpl, qt.chapters, haveQT, qt.saturated)
	// The titles shown are the track's when it stands alone or overrules the list.
	shown := chplTitles
	if haveQT && (!haveChpl || conflict) {
		shown = trackTitles
	}
	if shown.legacy {
		d.legacyTitles = shown.page.name
	}
	return conflict, nil
}

// pickChpl settles which chpl atom the chapters come from: the first holding chapters, else the
// first. The rest become d.chplDups, and titles ends as the reader of the one picked.
func pickChpl(src core.ReaderAtSized, chpls []node, d *doc, titles *titleReader, limit int64) ([]core.Chapter, error) {
	if len(chpls) == 0 {
		return nil, nil
	}
	var picked []core.Chapter
	pick := 0
	for i, c := range chpls {
		r := titleReader{page: titles.page}
		ver, chs, ok, err := decodeChpl(src, c, &r, limit)
		if err != nil {
			return nil, err
		}
		if ok && len(chs) > 0 {
			pick, picked, *titles = i, chs, r
			d.chplVersion, d.chplCount = ver, len(chs)
			break
		}
	}
	d.chpl = refPtr(chpls[pick])
	for i, c := range chpls {
		if i != pick {
			d.chplDups = append(d.chplDups, refOf(c))
		}
	}
	return picked, nil
}

// chplHasReserved reports whether a Nero chpl atom carries the 32-bit reserved field
// before the chapter count.
func chplHasReserved(version uint8) bool { return version != 0 }

// decodeChpl reads a Nero chpl atom into chapters; ok is false for one that does not parse.
func decodeChpl(src core.ReaderAtSized, chpl node, titles *titleReader, limit int64) (version uint8, chapters []core.Chapter, ok bool, err error) {
	b, err := readPayloadWhole(src, chpl, maxMetaChunk, limit)
	if err != nil {
		return 0, nil, false, bits.Fatal(err)
	}
	version, chapters, ok = parseChpl(b, titles)
	return version, chapters, ok, nil
}

// parseChpl parses a chpl payload, supporting both versions: version(1) + flags(3), then a
// reserved 4-byte field (see chplHasReserved), then an 8-bit chapter count, then each entry as a
// 64-bit 100 ns start plus a length-prefixed title.
func parseChpl(b []byte, titles *titleReader) (version uint8, chapters []core.Chapter, ok bool) {
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
	for range count {
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
		title := titles.read(b[pos : pos+titleLen])
		pos += titleLen
		chapters = append(chapters, core.Chapter{Start: scaleToDuration(start, chplStartUnit), Title: title})
	}
	core.FillInteriorEnds(chapters)
	return version, chapters, true
}

// renderChpl encodes chapters into a Nero chpl atom, preserving the parsed version
// (defaulting to 1, the form ffmpeg writes). Each title is truncated to 255 bytes on a
// UTF-8 boundary because the length prefix is a single byte.
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

// movieTracks is what the chapter read needs of the movie's tracks, read once.
type movieTracks struct {
	traks    []node
	ids      []uint32 // each trak's track id, where known says its tkhd gave one
	known    []bool
	handlers []string // each trak's media handler type, "" when it has none
	audio    int      // the first sound trak, -1 when there is none
	// text is the chapter track, -1 when there is none, and refs the traks a chapter write may
	// rewrite the references of.
	text int
	refs []trefChap
}

// trefChap is a trak whose tref holds a "chap" entry, or one a new chapter track is named from.
type trefChap struct {
	trak    atomRef
	mdiaOff int64    // where a tref goes in a trak that has none
	tref    *atomRef // the trak's tref, if any, and its payload
	raw     []byte
	video   []uint32 // the video tracks its "chap" entries name, in order: chapter thumbnails
	names   bool     // a sound trak, or a video trak that holds no thumbnails
}

// readTracks reads each trak's id and handler and settles the chapter track.
func readTracks(src core.ReaderAtSized, moov node, limit int64) (movieTracks, error) {
	tr := movieTracks{traks: moov.findAll("trak", nil), audio: -1, text: -1}
	n := len(tr.traks)
	tr.ids, tr.known, tr.handlers = make([]uint32, n), make([]bool, n), make([]string, n)
	for i, t := range tr.traks {
		var err error
		if tkhd, ok := t.find("tkhd"); ok {
			if tr.ids[i], tr.known[i], err = trackID(src, tkhd, limit); err != nil {
				return tr, err
			}
		}
		if tr.handlers[i], err = trakHandler(src, t, limit); err != nil {
			return tr, err
		}
		if tr.audio < 0 && tr.handlers[i] == "soun" {
			tr.audio = i
		}
	}
	return tr, findChapterTrack(src, moov, &tr, limit)
}

// ownTrak reports whether t is a child of moov. A trak nested deeper is left alone: a write sizes
// no atom between moov and it.
func ownTrak(moov, t node) bool {
	_, ok := slices.BinarySearchFunc(moov.children, t.offset, func(c node, off int64) int { return cmp.Compare(c.offset, off) })
	return ok
}

// findChapterTrack reads the "chap" lists of moov's own traks into tr.refs and settles the
// chapter track: the first text or subtitle trak another trak names. A video trak named there
// holds thumbnails. A trak only its own list names is none: a rewrite would replace it, list and
// all.
func findChapterTrack(src core.ReaderAtSized, moov node, tr *movieTracks, limit int64) error {
	textOf, video := map[uint32]int{}, map[uint32]bool{}
	for i, t := range tr.traks {
		if !tr.known[i] || !ownTrak(moov, t) {
			continue
		}
		switch tr.handlers[i] {
		case "text", "sbtl":
			if _, dup := textOf[tr.ids[i]]; !dup {
				textOf[tr.ids[i]] = i
			}
		case "vide":
			video[tr.ids[i]] = true
		}
	}
	thumbs := map[uint32]bool{}
	var owners []int // the trak each ref is
	for i, t := range tr.traks {
		if !ownTrak(moov, t) {
			continue
		}
		ref := trefChap{trak: refOf(t)}
		if mdia, ok := t.find("mdia"); ok {
			ref.mdiaOff = mdia.offset
		}
		named := false
		if tref, ok := t.find("tref"); ok {
			raw, err := readPayloadWhole(src, tref, maxMetaChunk, limit)
			if err != nil {
				if bits.Fatal(err) != nil {
					return err
				}
				continue // a tref that cannot be read cannot be rewritten
			}
			ref.tref, ref.raw = refPtr(tref), raw
			var ids []uint32
			ids, named = chapIDs(raw)
			for _, id := range ids {
				if text, ok := textOf[id]; ok && text != i && tr.text < 0 {
					tr.text = text
				}
				if video[id] {
					ref.video, thumbs[id] = append(ref.video, id), true
				}
			}
		}
		if h := tr.handlers[i]; named || h == "soun" || h == "vide" {
			tr.refs, owners = append(tr.refs, ref), append(owners, i)
		}
	}
	for r, i := range owners {
		tr.refs[r].names = tr.handlers[i] == "soun" || tr.handlers[i] == "vide" && !(tr.known[i] && thumbs[tr.ids[i]])
	}
	return nil
}

// collectChapterRefs captures the structural references a QuickTime chapter write needs
// to rebuild that track without re-reading the source: the traks whose "chap" lists it
// rewrites, the existing chapter text track to replace, and the movie header fields
// (timescale/duration and a free track id) a new track is built from.
func collectChapterRefs(src core.ReaderAtSized, moov node, tr movieTracks, d *doc, limit int64) error {
	if mvhd, ok := moov.find("mvhd"); ok {
		d.mvhd = refPtr(mvhd)
		if err := collectMvhd(src, mvhd, d, limit); err != nil {
			return err
		}
	}
	// A track id free of every existing track, used when mvhd's next_track_ID is
	// missing, the all-ones sentinel, or stale (not past every track). nextTrackID 0
	// means none is free: a track already holds the max id, and maxID+1 would wrap to
	// the invalid id 0.
	maxID := uint32(0)
	for i, id := range tr.ids {
		if tr.known[i] && id > maxID {
			maxID = id
		}
	}
	switch {
	case maxID == 0xFFFFFFFF:
		d.nextTrackID = 0
	case d.nextTrackID == 0 || d.nextTrackID == 0xFFFFFFFF || d.nextTrackID <= maxID:
		d.nextTrackID = maxID + 1
	}

	d.chapRefs = tr.refs
	if tr.text >= 0 {
		d.chapTrak = refPtr(tr.traks[tr.text])
		d.chapTrackID = tr.ids[tr.text] // reused when the track is rebuilt in place
	}
	return nil
}

// chapterOnlyMdats returns the payload offsets of the mdats (offset, length; in file order)
// that samples cover end to end, as those of the mdat a chapter write appends do: with no gap,
// overlapping samples covering their bytes once, and none running past the end.
func chapterOnlyMdats(samples []sampleOffset, mdats [][2]int64) []int64 {
	sorted := slices.Clone(samples)
	slices.SortFunc(sorted, func(a, b sampleOffset) int { return cmp.Compare(a.off, b.off) })
	// reach is how far the samples cover each mdat from its start, -1 once a gap shows.
	reach := make([]int64, len(mdats))
	for i, m := range mdats {
		reach[i] = m[0]
	}
	for _, s := range sorted {
		i := mdatAt(mdats, s.off)
		if i < 0 {
			continue
		}
		if reach[i] < 0 || s.off > reach[i] {
			reach[i] = -1
			continue
		}
		reach[i] = max(reach[i], s.off+s.size)
	}
	var out []int64
	for i, m := range mdats {
		if m[1] > 0 && reach[i] == m[0]+m[1] {
			out = append(out, m[0])
		}
	}
	return out
}

// collectMvhd reads the movie header's timescale, duration, and next_track_ID: a new
// chapter track places itself on the movie timescale, ends an open last chapter at the
// movie duration, and takes next_track_ID as its track id.
func collectMvhd(src core.ReaderAtSized, mvhd node, d *doc, limit int64) error {
	b, err := readPayloadPrefix(src, mvhd, 120, limit)
	if err != nil || len(b) < 1 {
		return bits.Fatal(err)
	}
	po := mvhd.payloadOff()
	// Read the timescale/duration through readMvhdTiming, the decode movieTimingOf uses on
	// a reparse, so the write path's d.movieTimescale/d.movieDuration match it. A mismatch
	// would desync the chapter last-end recovery and rewrite the file.
	d.movieTimescale, d.movieDuration = readMvhdTiming(b)
	switch b[0] {
	case 0:
		if len(b) >= 100 {
			d.nextTrackID = binary.BigEndian.Uint32(b[96:100])
			d.nextTrackIDOff = po + 96
		}
	case 1:
		if len(b) >= 112 {
			d.nextTrackID = binary.BigEndian.Uint32(b[108:112])
			d.nextTrackIDOff = po + 108
		}
	}
	return nil
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

// trackChapters is a chapter text track as read: saturated marks a clamped stts delta or leading
// edit, and samples where each title sits.
type trackChapters struct {
	chapters  []core.Chapter
	saturated bool
	samples   []sampleOffset
}

// decodeQTChapters reads the QuickTime chapter text track: it walks that track's sample tables
// and decodes each text sample (a 16-bit length prefix plus the title) into a chapter.
func decodeQTChapters(src core.ReaderAtSized, moov node, tr movieTracks, titles *titleReader, limit int64) (trackChapters, bool, error) {
	if tr.text < 0 {
		return trackChapters{}, false, nil
	}
	text := tr.traks[tr.text]
	// A chapter text track's stts decode times always run from zero, so a first chapter
	// that starts after t=0 carries that offset in a leading empty edit in the track's
	// elst (the standard MP4 delayed-track form WaxLabel writes).
	movieTimescale, movieDuration, err := movieTimingOf(src, moov, limit)
	if err != nil {
		return trackChapters{}, false, err
	}
	ed, err := chapterEdits(src, text, movieTimescale, limit)
	if err != nil {
		return trackChapters{}, false, err
	}
	tc, ok, err := decodeTextTrack(src, text, ed, movieTimescale, movieDuration, titles, limit)
	if err != nil || !ok {
		return trackChapters{}, false, err
	}
	// A clamped leading empty edit (a first chapter past the u32 movie-timescale ceiling)
	// is a saturation the stts-delta scan cannot see, since the over-range start lives in
	// the edit list, so fold it in here too.
	tc.saturated = tc.saturated || ed.saturated
	return tc, true, nil
}

// movieTimingOf reads moov's mvhd movie timescale and duration at resolveChapters time,
// before collectMvhd populates d.
func movieTimingOf(src core.ReaderAtSized, moov node, limit int64) (timescale uint32, duration uint64, err error) {
	mvhd, ok := moov.find("mvhd")
	if !ok {
		return 0, 0, nil
	}
	b, err := readPayloadPrefix(src, mvhd, 32, limit)
	if err != nil {
		return 0, 0, bits.Fatal(err)
	}
	timescale, duration = readMvhdTiming(b)
	return timescale, duration, nil
}

// readMvhdTiming decodes the movie timescale and duration from a mvhd payload prefix
// (version 0 at bytes 12/16, version 1 at bytes 20/24), mapping the all-ones "unknown
// duration" sentinel to zero.
func readMvhdTiming(b []byte) (timescale uint32, duration uint64) {
	if len(b) < 1 {
		return 0, 0
	}
	switch b[0] {
	case 0:
		if len(b) >= 16 {
			timescale = binary.BigEndian.Uint32(b[12:16])
		}
		if len(b) >= 20 {
			duration = sentinelToZero64(uint64(binary.BigEndian.Uint32(b[16:20])), 0xFFFFFFFF)
		}
	case 1:
		if len(b) >= 24 {
			timescale = binary.BigEndian.Uint32(b[20:24])
		}
		if len(b) >= 32 {
			duration = sentinelToZero64(binary.BigEndian.Uint64(b[24:32]), 0xFFFFFFFFFFFFFFFF)
		}
	}
	return timescale, duration
}

// trackEdits is where a chapter track's edit list puts its media on the movie timeline: offset is
// the delay of a leading empty edit, which places a first chapter that starts after t=0, played
// the span of the edits that play media, 0 when unknown, and saturated an offset clamped when
// written.
type trackEdits struct {
	offset, played time.Duration
	saturated      bool
}

// chapterEdits reads trak's edit list, which says nothing where the movie timescale is unknown.
func chapterEdits(src core.ReaderAtSized, trak node, movieTimescale uint32, limit int64) (trackEdits, error) {
	if movieTimescale == 0 {
		return trackEdits{}, nil
	}
	p, err := elstPayload(src, trak, limit)
	if err != nil || p == nil {
		return trackEdits{}, err
	}
	return readEdits(p, movieTimescale), nil
}

// readEdits is an elst payload as trackEdits.
func readEdits(p []byte, movieTimescale uint32) trackEdits {
	lead, played, clamped := editListSpans(p)
	return trackEdits{
		offset: scaleToDuration(lead, movieTimescale), played: scaleToDuration(played, movieTimescale), saturated: clamped,
	}
}

// elstPayload returns the payload of the edit list in trak's edts, nil when it has none. A real
// elst is a few 12- or 20-byte entries, so the 64-bit and to-end-of-box size forms cutAtom stops
// at do not occur.
func elstPayload(src core.ReaderAtSized, trak node, limit int64) ([]byte, error) {
	edts, ok := trak.find("edts")
	if !ok {
		return nil, nil
	}
	b, err := readPayloadWhole(src, edts, maxMetaChunk, limit)
	if err != nil {
		return nil, bits.Fatal(err)
	}
	for a, rest, ok := cutAtom(b); ok; a, rest, ok = cutAtom(rest) {
		if string(a[4:8]) == "elst" {
			return a[8:], nil
		}
	}
	return nil, nil
}

// editListSpans reads an elst payload, in movie units: lead is the segment of a first entry that
// is an empty edit (media_time -1), played the summed segments that play media. A 32-bit segment
// of exactly MaxUint32 was clamped when written: clamped reports such a lead, and such a span
// leaves played unknown, as 0.
func editListSpans(p []byte) (lead, played uint64, clamped bool) {
	if len(p) < 8 {
		return 0, 0, false
	}
	var width int
	switch p[0] {
	case 0:
		width = 12
	case 1:
		width = 20
	default:
		return 0, 0, false
	}
	count := binary.BigEndian.Uint32(p[4:8])
	unknown := false
	for i, e := uint32(0), p[8:]; i < count && len(e) >= width; i, e = i+1, e[width:] {
		seg, empty := uint64(binary.BigEndian.Uint32(e)), int32(binary.BigEndian.Uint32(e[4:])) == -1
		if width == 20 {
			seg, empty = binary.BigEndian.Uint64(e), int64(binary.BigEndian.Uint64(e[8:])) == -1
		}
		atCeiling := width == 12 && seg == math.MaxUint32
		switch {
		case !empty:
			unknown = unknown || atCeiling
			if played += seg; played < seg {
				played = math.MaxUint64
			}
		case i == 0:
			// The first chapter starts past the u32 movie-timescale ceiling, so its QuickTime
			// start is lossy.
			lead, clamped = seg, atCeiling
		}
	}
	if unknown {
		played = 0
	}
	return lead, played, clamped
}

// gridSlack is how far apart the rounding of the movie and the media timescale can put two
// instants that are one.
func gridSlack(movieTimescale, mediaTimescale uint32) time.Duration {
	return scaleToDuration(1, movieTimescale) + scaleToDuration(1, mediaTimescale)
}

// lastChapterEnd is the last chapter's end as a read places it, 0 for an open one. lastDelta is
// its stts span and endUnits where that ends, in media units.
func lastChapterEnd(lastStart time.Duration, endUnits, lastDelta uint64, ed trackEdits, mts, movieTimescale uint32, movieDuration uint64) time.Duration {
	// The span is compared in media units: scaleToDuration rounds, so a Duration test for the
	// placeholder would fire on some timescales and not others.
	if isPlaceholderTail(lastStart, lastDelta, mts, movieTimescale, movieDuration) {
		return 0
	}
	return trackEnd(scaleToDuration(endUnits, mts), lastStart, ed, gridSlack(movieTimescale, mts))
}

// trackEnd places the end of a chapter track's media, where its last chapter ends, on the movie
// timeline. Media that ends inside its edits ends the track, and media that runs past them ends
// with them, unless it reaches the track's end (a track written to span the movie): its own
// duration is then that instant on the finer grid. Within the slack the nearer end decides. An
// end at or before lastStart is none, and the media's own stands.
func trackEnd(mediaEnd, lastStart time.Duration, ed trackEdits, slack time.Duration) time.Duration {
	own, end := addClamp(mediaEnd, ed.offset), addClamp(ed.played, ed.offset)
	over, past := mediaEnd-ed.played, mediaEnd-end // how far the media runs past the edits, and the track
	placed := end
	switch {
	case ed.played == 0, over <= slack && over.Abs() <= past.Abs():
		return own
	case past.Abs() <= slack:
		placed = mediaEnd
	}
	if placed <= lastStart {
		return own
	}
	return placed
}

// trackEditedDuration returns trak's total edit-list playable duration, or 0 when the
// track has no edit list. This is the track's own trimmed length, not the movie duration;
// for AAC, it removes encoder priming that is still present in the raw mdhd duration.
func trackEditedDuration(src core.ReaderAtSized, trak node, movieTimescale uint32, limit int64) (time.Duration, error) {
	if movieTimescale == 0 {
		return 0, nil
	}
	p, err := elstPayload(src, trak, limit)
	if err != nil || p == nil {
		return 0, err
	}
	return scaleToDuration(elstSegmentDurationSum(p), movieTimescale), nil
}

// elstSegmentDurationSum sums the segment_duration field of every edit-list entry (in
// movie-timescale units).
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
		for i := range count {
			off := 8 + i*12
			sum += uint64(binary.BigEndian.Uint32(p[off : off+4]))
		}
	case 1:
		if !boundedCount(count, 8, 20, int64(len(p))) {
			return 0
		}
		for i := range count {
			off := 8 + i*20
			sum += binary.BigEndian.Uint64(p[off : off+8])
		}
	}
	return sum
}

// trakHandler returns a trak's media handler type ("soun", "text", ...), "" when it has none.
func trakHandler(src core.ReaderAtSized, trak node, limit int64) (string, error) {
	mdia, ok := trak.find("mdia")
	if !ok {
		return "", nil
	}
	hdlr, ok := mdia.find("hdlr")
	if !ok {
		return "", nil
	}
	return handlerType(src, hdlr, limit)
}

// cutAtom splits the payload of an atom the walker treats as a leaf, a tref or an edts, into its
// first child atom and what follows it. ok is false where no whole atom with a 32-bit size starts.
func cutAtom(p []byte) (atom, rest []byte, ok bool) {
	if len(p) < 8 {
		return nil, p, false
	}
	size := int64(binary.BigEndian.Uint32(p))
	if size < 8 || size > int64(len(p)) {
		return nil, p, false
	}
	return p[:size], p[size:], true
}

// chapIDs returns the track ids the "chap" entries of a tref payload name, in order, and whether
// it holds such an entry at all.
func chapIDs(payload []byte) (ids []uint32, found bool) {
	for ref, rest, ok := cutAtom(payload); ok; ref, rest, ok = cutAtom(rest) {
		if string(ref[4:8]) != "chap" {
			continue
		}
		found = true
		for list := ref[8:]; len(list) >= 4; list = list[4:] {
			ids = append(ids, binary.BigEndian.Uint32(list))
		}
	}
	return ids, found
}

// trackID reads the track_id from a tkhd atom (version 0 places it at byte 12;
// version 1's 64-bit times push it to byte 20).
func trackID(src core.ReaderAtSized, tkhd node, limit int64) (uint32, bool, error) {
	b, err := readPayloadPrefix(src, tkhd, 32, limit)
	if err != nil || len(b) < 1 {
		return 0, false, bits.Fatal(err)
	}
	off := 12
	if b[0] == 1 {
		off = 20
	}
	if len(b) < off+4 {
		return 0, false, nil
	}
	return binary.BigEndian.Uint32(b[off : off+4]), true, nil
}

// decodeTextTrack reconstructs chapters from a text track's sample tables: the
// per-sample decode time (stts) gives each chapter's start, and the sample's bytes in
// mdat (located via stsc/stsz/stco|co64) carry its title.
func decodeTextTrack(src core.ReaderAtSized, trak node, ed trackEdits, movieTimescale uint32, movieDuration uint64, titles *titleReader, limit int64) (tc trackChapters, ok bool, err error) {
	mdia, ok := trak.find("mdia")
	if !ok {
		return trackChapters{}, false, nil
	}
	mdhd, ok := mdia.find("mdhd")
	if !ok {
		return trackChapters{}, false, nil
	}
	timescale, ok, err := mdhdTimescale(src, mdhd, limit)
	if err != nil || !ok {
		return trackChapters{}, false, err
	}
	stbl, ok := sampleTableOf(trak)
	if !ok {
		return trackChapters{}, false, nil
	}

	times, endTime, completed, ok, err := sampleTimes(src, stbl, limit)
	if err != nil || !ok || len(times) == 0 {
		return trackChapters{}, false, err
	}
	// A written stts delta that clamped is read back as exactly MaxUint32.
	for i := 1; i < len(times); i++ {
		if times[i]-times[i-1] == math.MaxUint32 {
			tc.saturated = true
			break
		}
	}
	offsets, ok, err := sampleOffsets(src, stbl, len(times), limit)
	if err != nil || !ok {
		return trackChapters{}, false, err
	}

	chapters := make([]core.Chapter, 0, len(offsets))
	for i, so := range offsets {
		title, err := readTextSample(src, so.off, so.size, titles, limit)
		if err != nil {
			return trackChapters{}, false, err
		}
		ch := core.Chapter{Start: addClamp(scaleToDuration(times[i], timescale), ed.offset), Title: title}
		if i+1 < len(times) {
			ch.End = addClamp(scaleToDuration(times[i+1], timescale), ed.offset)
		}
		chapters = append(chapters, ch)
	}
	// Recover the last chapter's end from the stts running total (endTime, the decode time
	// past the final sample); the per-sample loop above has no next start to fill it from.
	if completed && len(chapters) == len(times) {
		last := len(chapters) - 1
		chapters[last].End = lastChapterEnd(chapters[last].Start, endTime, endTime-times[last], ed, timescale, movieTimescale, movieDuration)
	}
	tc.chapters, tc.samples = chapters, offsets
	return tc, true, nil
}

// sampleOffset is one decoded sample's file location and byte length.
type sampleOffset struct {
	off  int64
	size int64
}

// sampleTimes returns each sample's cumulative decode time (in the media timescale)
// from the stts table, plus endTime, the running total past the final sample. The
// maxChapterSamples early return leaves endTime mid-table (completed false), so the
// caller must not treat it as the last chapter's end there.
func sampleTimes(src core.ReaderAtSized, stbl node, limit int64) (times []uint64, endTime uint64, completed, ok bool, err error) {
	stts, found := stbl.find("stts")
	if !found {
		return nil, 0, false, false, nil
	}
	b, err := readPayloadWhole(src, stts, maxMetaChunk, limit)
	if err != nil || len(b) < 8 {
		return nil, 0, false, false, bits.Fatal(err)
	}
	count := int64(binary.BigEndian.Uint32(b[4:8]))
	if !boundedCount(count, 8, 8, int64(len(b))) {
		return nil, 0, false, false, nil
	}
	var t uint64
	for i := range count {
		o := 8 + i*8
		n := binary.BigEndian.Uint32(b[o : o+4])
		delta := binary.BigEndian.Uint32(b[o+4 : o+8])
		for range n {
			if len(times) >= maxChapterSamples {
				return times, t, false, true, nil // capped: t is not the true final boundary
			}
			times = append(times, t)
			t += uint64(delta)
		}
	}
	return times, t, true, true, nil
}

// sampleOffsets locates each of the first nSamples samples in mdat by combining
// the sample sizes (stsz), the sample-to-chunk map (stsc), and the chunk offsets
// (stco/co64).
func sampleOffsets(src core.ReaderAtSized, stbl node, nSamples int, limit int64) ([]sampleOffset, bool, error) {
	sizes, ok, err := sampleSizes(src, stbl, nSamples, limit)
	if err != nil || !ok {
		return nil, false, err
	}
	chunks, ok, err := chunkOffsets(src, stbl, limit)
	if err != nil || !ok || len(chunks) == 0 {
		return nil, false, err
	}
	stsc, ok, err := stscEntries(src, stbl, limit)
	if err != nil || !ok {
		return nil, false, err
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
		return nil, false, nil
	}
	return out, true, nil
}

// sampleSizes returns the size of each of the first nSamples samples from stsz
// (a single shared size, or a per-sample table).
func sampleSizes(src core.ReaderAtSized, stbl node, nSamples int, limit int64) ([]uint32, bool, error) {
	stsz, ok := stbl.find("stsz")
	if !ok {
		return nil, false, nil
	}
	b, err := readPayloadWhole(src, stsz, maxMetaChunk, limit)
	if err != nil || len(b) < 12 {
		return nil, false, bits.Fatal(err)
	}
	shared := binary.BigEndian.Uint32(b[4:8])
	count := min(int64(binary.BigEndian.Uint32(b[8:12])), int64(nSamples), maxChapterSamples)
	sizes := make([]uint32, count)
	if shared != 0 {
		for i := range sizes {
			sizes[i] = shared
		}
		return sizes, true, nil
	}
	if !boundedCount(count, 12, 4, int64(len(b))) {
		return nil, false, nil
	}
	for i := range count {
		o := 12 + i*4
		sizes[i] = binary.BigEndian.Uint32(b[o : o+4])
	}
	return sizes, true, nil
}

// chunkOffsets returns the chunk file offsets from stco (32-bit) or co64 (64-bit).
func chunkOffsets(src core.ReaderAtSized, stbl node, limit int64) ([]uint64, bool, error) {
	if stco, ok := stbl.find("stco"); ok {
		t, err := parseOffsetTable(src, stco, limit)
		if err != nil {
			return nil, false, bits.Fatal(err)
		}
		return t.entries, true, nil
	}
	if co64, ok := stbl.find("co64"); ok {
		t, err := parseOffsetTable(src, co64, limit)
		if err != nil {
			return nil, false, bits.Fatal(err)
		}
		return t.entries, true, nil
	}
	return nil, false, nil
}

// stscEntries parses the sample-to-chunk table (first_chunk, samples_per_chunk;
// the sample-description index is not needed here).
func stscEntries(src core.ReaderAtSized, stbl node, limit int64) ([]stscEntry, bool, error) {
	stsc, ok := stbl.find("stsc")
	if !ok {
		return nil, false, nil
	}
	b, err := readPayloadWhole(src, stsc, maxMetaChunk, limit)
	if err != nil || len(b) < 8 {
		return nil, false, bits.Fatal(err)
	}
	count := int64(binary.BigEndian.Uint32(b[4:8]))
	if !boundedCount(count, 8, 12, int64(len(b))) {
		return nil, false, nil
	}
	out := make([]stscEntry, count)
	for i := range count {
		o := 8 + i*12
		out[i] = stscEntry{
			firstChunk:      binary.BigEndian.Uint32(b[o : o+4]),
			samplesPerChunk: binary.BigEndian.Uint32(b[o+4 : o+8]),
		}
	}
	return out, true, nil
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
	for c := range nChunks {
		for ei+1 < len(entries) && int(entries[ei+1].firstChunk) <= c+1 {
			ei++
		}
		if int(entries[ei].firstChunk) <= c+1 {
			out[c] = entries[ei].samplesPerChunk
		}
	}
	return out
}

// readTextSample reads a QuickTime text/tx3g sample's title: a 16-bit big-endian length, then
// that many bytes of text, cut to the sample. Style atoms after it are never read. A title the
// file does not fully hold reads as empty.
func readTextSample(src core.ReaderAtSized, off, size int64, titles *titleReader, limit int64) (string, error) {
	if size < 2 {
		return "", nil
	}
	head, err := bits.ReadSlice(src, off, 2, limit)
	if err != nil {
		return "", bits.Fatal(err)
	}
	n := min(int64(binary.BigEndian.Uint16(head)), size-2)
	if n > src.Size()-off-2 {
		return "", nil
	}
	raw, err := bits.ReadSlice(src, off+2, n, limit)
	if err != nil {
		return "", bits.Fatal(err)
	}
	return titles.read(raw), nil
}

// mergeChapters projects the two MP4 chapter representations into one list.
func mergeChapters(chpl []core.Chapter, haveChpl bool, qt []core.Chapter, haveQT, qtSaturated bool) (chapters []core.Chapter, conflict bool) {
	switch {
	case haveQT && haveChpl:
		if chaptersAgree(chpl, qt) {
			return mergeChplStartsQTEnd(chpl, qt), false
		}
		if qtSaturated && sameCountAndTitles(chpl, qt) {
			return mergeChplStartsQTEnd(chpl, qt), false
		}
		return qt, true
	case haveQT:
		return qt, false
	case haveChpl:
		return chpl, false
	default:
		return nil, false
	}
}

// sameCountAndTitles reports whether the chpl and the chapter track have equal length and
// matching titles, ignoring starts.
func sameCountAndTitles(chpl, qt []core.Chapter) bool {
	if len(chpl) != len(qt) {
		return false
	}
	for i := range chpl {
		if !titlesMatch(chpl[i].Title, qt[i].Title) {
			return false
		}
	}
	return true
}

// titlesMatch reports whether a chpl title and the chapter track's are one title: a track title
// that reads empty takes the chpl's.
func titlesMatch(chpl, qt string) bool { return qt == "" || chpl == qt }

// mergeChplStartsQTEnd combines two agreeing chapter sources: chpl's titles, exact uint64
// starts and interior ends (filled from its next starts, free of the QuickTime stts
// drift), plus the last chapter's end from the QuickTime track, the only source that
// has it.
func mergeChplStartsQTEnd(chpl, qt []core.Chapter) []core.Chapter {
	out := core.CloneChapters(chpl)
	if n := len(out); n > 0 && n == len(qt) && qt[n-1].End >= out[n-1].Start {
		out[n-1].End = qt[n-1].End
	}
	return out
}

// chaptersAgree reports whether the chpl and the chapter track describe the same chapters:
// equal count, matching titles, and starts within a small tolerance (the two
// representations use different time bases, so exact equality is too strict).
func chaptersAgree(chpl, qt []core.Chapter) bool {
	if len(chpl) != len(qt) {
		return false
	}
	const tol = 500 * time.Millisecond
	for i := range chpl {
		if !titlesMatch(chpl[i].Title, qt[i].Title) {
			return false
		}
		d := chpl[i].Start - qt[i].Start
		if d < -tol || d > tol {
			return false
		}
	}
	return true
}

// isPlaceholderTail reports whether the last chapter's stts span is the synthetic
// one-unit tail chapterDeltas writes for an open last chapter that starts at or past a
// known movie duration (its "next = starts[i] + 1" default branch).
func isPlaceholderTail(lastStart time.Duration, lastDeltaUnits uint64, mediaTimescale uint32, movieTimescale uint32, movieDuration uint64) bool {
	if movieTimescale == 0 || movieDuration == 0 {
		return false
	}
	if lastStart < scaleToDuration(movieDuration, movieTimescale) {
		return false
	}
	return lastDeltaUnits == 1 || (mediaTimescale != 0 && lastDeltaUnits == uint64(mediaTimescale))
}

// mdhdFields decodes a mdhd atom's media timescale and duration (version 0 or 1). The
// audio-property duration read and the chapter-track timescale read share it.
func mdhdFields(src core.ReaderAtSized, mdhd node, limit int64) (timescale uint32, duration uint64, ok bool, err error) {
	b, err := readPayloadPrefix(src, mdhd, 32, limit)
	if err != nil || len(b) < 4 {
		return 0, 0, false, bits.Fatal(err)
	}
	switch b[0] {
	case 0:
		if len(b) < 20 {
			return 0, 0, false, nil
		}
		return binary.BigEndian.Uint32(b[12:16]), uint64(binary.BigEndian.Uint32(b[16:20])), true, nil
	case 1:
		if len(b) < 32 {
			return 0, 0, false, nil
		}
		return binary.BigEndian.Uint32(b[20:24]), binary.BigEndian.Uint64(b[24:32]), true, nil
	default:
		return 0, 0, false, nil
	}
}

// mdhdTimescale reads the media timescale from a mdhd atom. A zero timescale is
// rejected: it is invalid and would collapse every chapter time to zero.
func mdhdTimescale(src core.ReaderAtSized, mdhd node, limit int64) (uint32, bool, error) {
	ts, _, ok, err := mdhdFields(src, mdhd, limit)
	return ts, ok && ts != 0, err
}

// addClamp returns a + b saturated at MaxInt64, so adding the edit-list offset to a
// per-sample time preserves scaleToDuration's clamp-not-overflow guarantee end to end.
func addClamp(a, b time.Duration) time.Duration {
	if s := a + b; s >= a {
		return s
	}
	return time.Duration(math.MaxInt64)
}

// scaleToDuration converts a count of timescale units into a time.Duration, clamping
// rather than overflowing on absurd inputs.
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
