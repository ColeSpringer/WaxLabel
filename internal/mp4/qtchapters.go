package mp4

import (
	"encoding/binary"
	"math"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// This file builds a QuickTime chapter text track - the representation iTunes and Apple
// Books read (they ignore the Nero chpl).

// encdBox is the text-encoding modifier ffmpeg appends to each chapter text sample (12
// bytes: size, "encd", and the 0x0100 encoding value).
var encdBox = []byte{0x00, 0x00, 0x00, 0x0c, 'e', 'n', 'c', 'd', 0x00, 0x00, 0x01, 0x00}

// unityMatrix is the standard 3x3 fixed-point transform (identity with the
// trailing 0x40000000) used in tkhd and the text media-info box.
func unityMatrix() []byte {
	return []byte{
		0x00, 0x01, 0x00, 0x00, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0x00, 0x01, 0x00, 0x00, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0x00, 0x00, 0x00,
	}
}

func be32(n int) []byte { return be32u(uint32(n)) }
func be32u(n uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], n)
	return b[:]
}

// clampU32 saturates a 64-bit count to 32 bits for a v0 box field. the movie-unit
// fields clamp at the movie-timescale ceiling instead - larger for a coarse ~1 ms
// timescale (MaxUint32 ms is ~49.7 days), or smaller for a hi-res file whose movie
// timescale exceeds 90 kHz.
func clampU32(n uint64) uint32 {
	if n > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n)
}

// chapterTextEntry is ffmpeg's QuickTime "text" sample description, captured verbatim
// from a real chapter track (it carries a self data-reference index of 1 and an empty
// default font table). only its presence and shape matter, so it is embedded rather
// than reconstructed field by field.
var chapterTextEntry = []byte{
	0x00, 0x00, 0x00, 0x3b, 0x74, 0x65, 0x78, 0x74, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x0d, 0x66, 0x74, 0x61, 0x62, 0x00, 0x01, 0x00, 0x01, 0x00,
}

// chapterStsd wraps the text sample entry in a one-entry stsd (sample
// description) box.
func chapterStsd() []byte {
	return renderFullBox(atomName("stsd"), slices.Concat(be32(1), chapterTextEntry))
}

// chapterHdlr is the media handler for a text track ("text" handler type, named
// "SubtitleHandler" as ffmpeg writes).
func chapterHdlr() []byte {
	payload := slices.Concat(
		make([]byte, 8),  // version/flags + pre_defined
		[]byte("text"),   // handler_type
		make([]byte, 12), // reserved
		append([]byte("SubtitleHandler"), 0),
	)
	return renderAtom(atomName("hdlr"), payload)
}

// chapterGmhd is the base media information box a text track requires: a gmin
// (generic media info) plus the QuickTime text media-info matrix box.
func chapterGmhd() []byte {
	gmin := renderAtom(atomName("gmin"), []byte{
		0, 0, 0, 0, // version/flags
		0x00, 0x40, // graphicsmode (copy)
		0x80, 0x00, 0x80, 0x00, 0x80, 0x00, // opcolor R/G/B
		0, 0, // balance
		0, 0, // reserved
	})
	textInfo := renderAtom(atomName("text"), unityMatrix())
	return renderAtom(atomName("gmhd"), slices.Concat(gmin, textInfo))
}

// chapterDinf is the data information box: a single self-contained data
// reference (the media is in this file).
func chapterDinf() []byte {
	url := renderAtom(atomName("url "), []byte{0, 0, 0, 1}) // flags 1 = self-contained
	dref := renderFullBox(atomName("dref"), slices.Concat(be32(1), url))
	return renderAtom(atomName("dinf"), dref)
}

// chapterMdhd is the media header (v0): the media timescale (shared with the
// movie) and the track's total media duration.
func chapterMdhd(timescale uint32, duration uint64) []byte {
	p := make([]byte, 24) // vf, ctime, mtime, timescale, duration, language, quality
	binary.BigEndian.PutUint32(p[12:16], timescale)
	binary.BigEndian.PutUint32(p[16:20], clampU32(duration))
	return renderAtom(atomName("mdhd"), p)
}

// chapterTkhd is the track header (v0). flags 0x000002 marks the track present
// in the movie but not enabled (a chapter track is navigation, not playback),
// matching ffmpeg.
func chapterTkhd(trackID uint32, duration uint64) []byte {
	p := make([]byte, 84)
	p[3] = 0x02 // flags: TRACK_IN_MOVIE
	binary.BigEndian.PutUint32(p[12:16], trackID)
	binary.BigEndian.PutUint32(p[20:24], clampU32(duration))
	copy(p[40:76], unityMatrix())
	return renderAtom(atomName("tkhd"), p)
}

// chapterEdts maps the media into the movie timeline at normal rate.
func chapterEdts(firstStart, mediaDur uint64) []byte {
	normal := make([]byte, 12)
	binary.BigEndian.PutUint32(normal[0:4], clampU32(mediaDur)) // segment_duration (movie ts)
	binary.BigEndian.PutUint32(normal[8:12], 0x00010000)        // media_rate 1.0 (media_time 0)
	entries, count := normal, 1
	if firstStart > 0 {
		empty := make([]byte, 12)
		binary.BigEndian.PutUint32(empty[0:4], clampU32(firstStart)) // segment_duration = first chapter start
		binary.BigEndian.PutUint32(empty[4:8], 0xFFFFFFFF)           // media_time -1: an empty edit
		binary.BigEndian.PutUint32(empty[8:12], 0x00010000)          // media_rate 1.0
		entries, count = slices.Concat(empty, normal), 2
	}
	elst := renderFullBox(atomName("elst"), slices.Concat(be32(count), entries))
	return renderAtom(atomName("edts"), elst)
}

// chapterFirstStart is the first chapter's start in movie-timescale units - the leading
// empty-edit offset that positions a chapter track whose first chapter is not at t=0.
func chapterFirstStart(movieTimescale uint32, chapters []core.Chapter) uint64 {
	if movieTimescale == 0 || len(chapters) == 0 {
		return 0
	}
	return durationToUnits(chapters[0].Start, movieTimescale)
}

// chapterSamples renders the text-track media payload (the new mdat's contents):
// one sample per chapter, each a 16-bit title length, the UTF-8 title (capped at
// the shared 255-byte chapter-title limit so the chpl and this track agree), and
// the encd trailer.
func chapterSamples(chapters []core.Chapter) []byte {
	var out []byte
	for _, ch := range chapters {
		title := truncateUTF8(ch.Title, titleByteMax)
		out = append(out, byte(len(title)>>8), byte(len(title)))
		out = append(out, title...)
		out = append(out, encdBox...)
	}
	return out
}

// chapterDeltas returns each chapter sample's stts duration in the chapter media
// timescale (mts): the gap to the next chapter, and for the last chapter its own End
// (or the movie duration) so the track spans the whole movie.
func chapterDeltas(chapters []core.Chapter, mts, movieTimescale uint32, movieDuration uint64) (deltas []uint32, saturated bool) {
	n := len(chapters)
	starts := make([]uint64, n)
	for i, ch := range chapters {
		starts[i] = durationToUnits(ch.Start, mts)
	}
	// Convert the movie duration (movie-timescale units) into mts units so it can bound the
	// open last chapter on the same grid as starts. When the timescales coincide, or the movie
	// timescale is unknown, the value is used as-is.
	movieDurUnits := movieDuration
	if movieTimescale != 0 && movieTimescale != mts {
		movieDurUnits = durationToUnits(scaleToDuration(movieDuration, movieTimescale), mts)
	}
	deltas = make([]uint32, n)
	// debt counts units borrowed to separate coincident starts. The unavoidable +1 stays
	// only on the duplicate start.
	var debt uint64
	for i := range n {
		var next uint64
		switch {
		case i+1 < n:
			next = starts[i+1]
		case chapters[i].End > chapters[i].Start:
			next = durationToUnits(chapters[i].End, mts)
		case movieDurUnits > starts[i]:
			next = movieDurUnits
		default:
			// A one-media-unit tail when nothing else bounds it: the chapter starts at or past
			// the movie duration, so there is no real end to encode.
			next = starts[i] + 1
		}
		// The input is stably sorted by start, so starts[i] <= next normally holds; a
		// zero raw gap is a genuine collision (or a last chapter the movie duration does
		// not extend), not disorder.
		var rawDelta uint64
		if next > starts[i] {
			rawDelta = next - starts[i]
		}
		var delta uint64
		if rawDelta == 0 {
			delta = 1 // smallest representable nonzero duration; keeps starts increasing
			debt++
		} else {
			delta = rawDelta
			if debt > 0 && delta > 1 { // repay borrowed units from this chapter's slack
				repay := min(debt, delta-1)
				delta -= repay
				debt -= repay
			}
		}
		if delta > math.MaxUint32 {
			saturated = true // clampU32 will saturate this to a 32-bit field
		}
		deltas[i] = clampU32(delta)
	}
	return deltas, saturated
}

// buildChapterTrak renders the whole chapter text trak with a placeholder chunk offset
// (the appended mdat's address is not known until the moov delta is).
func buildChapterTrak(trackID, mts, movieTimescale uint32, movieDuration uint64, chapters []core.Chapter, co64 bool) (trak []byte, stcoEntryOff int, saturated bool) {
	deltas, saturated := chapterDeltas(chapters, mts, movieTimescale, movieDuration)
	var totalDur uint64 // the summed media (mts) span; mdhd.duration and the stts deltas use it
	for _, d := range deltas {
		totalDur += uint64(d)
	}
	// The first chapter's start becomes a leading empty edit, so the track presentation
	// (tkhd) spans firstStart + the media, while the media itself (mdhd) stays totalDur.
	firstStart := chapterFirstStart(movieTimescale, chapters)
	// mdhd.duration and the stts deltas are media-timescale (mts);
	totalDurMovie := totalDur
	if movieTimescale != 0 && movieTimescale != mts {
		totalDurMovie = durationToUnits(scaleToDuration(totalDur, mts), movieTimescale)
	}
	// buildChapterTrak writes four clampU32 duration fields: the 90 kHz mdhd (totalDur)
	// and three movie-unit fields - tkhd (firstStart+totalDurMovie), the elst normal
	// segment (totalDurMovie), and the elst empty-edit segment (firstStart).
	tkhdDur := firstStart + totalDurMovie
	saturated = saturated || totalDur > math.MaxUint32 || tkhdDur > math.MaxUint32

	stbl := renderAtom(atomName("stbl"), slices.Concat(
		chapterStsd(), buildStts(deltas), buildStsc(len(chapters)), buildStsz(chapters), buildStco(co64)))
	minf := renderAtom(atomName("minf"), slices.Concat(chapterGmhd(), chapterDinf(), stbl))
	mdia := renderAtom(atomName("mdia"), slices.Concat(chapterMdhd(mts, totalDur), chapterHdlr(), minf))
	trak = renderAtom(atomName("trak"), slices.Concat(chapterTkhd(trackID, tkhdDur), chapterEdts(firstStart, totalDurMovie), mdia))

	width := 4
	if co64 {
		width = 8
	}
	return trak, len(trak) - width, saturated
}

// buildStts renders the time-to-sample table: one run per sample (sample_count 1,
// the chapter's duration), so arbitrary per-chapter spans encode exactly.
func buildStts(deltas []uint32) []byte {
	body := make([]byte, 0, 8+8*len(deltas))
	body = append(body, 0, 0, 0, 0)
	body = append(body, be32(len(deltas))...)
	for _, d := range deltas {
		body = append(body, be32(1)...)
		body = append(body, be32u(d)...)
	}
	return renderAtom(atomName("stts"), body)
}

// buildStsc maps all samples into one chunk (first_chunk 1, samples_per_chunk n,
// sample_description_index 1).
func buildStsc(n int) []byte {
	body := slices.Concat([]byte{0, 0, 0, 0}, be32(1), be32(1), be32(n), be32(1))
	return renderAtom(atomName("stsc"), body)
}

// buildStsz renders the per-sample size table (sample_size 0 = sizes follow);
// each size is the title's 16-bit length prefix plus the title plus the encd
// trailer, matching chapterSamples.
func buildStsz(chapters []core.Chapter) []byte {
	body := make([]byte, 0, 12+4*len(chapters))
	body = append(body, 0, 0, 0, 0)             // version/flags
	body = append(body, 0, 0, 0, 0)             // sample_size 0 (table follows)
	body = append(body, be32(len(chapters))...) // sample_count
	for _, ch := range chapters {
		sz := 2 + len(truncateUTF8(ch.Title, titleByteMax)) + len(encdBox)
		body = append(body, be32(sz)...)
	}
	return renderAtom(atomName("stsz"), body)
}

// buildStco renders a single-chunk offset table with a placeholder address the
// caller backpatches once the appended mdat's location is known. co64 selects a
// 64-bit table for a file whose appended mdat lands past 4 GiB.
func buildStco(co64 bool) []byte {
	if co64 {
		return renderAtom(atomName("co64"), slices.Concat([]byte{0, 0, 0, 0}, be32(1), make([]byte, 8)))
	}
	return renderAtom(atomName("stco"), slices.Concat([]byte{0, 0, 0, 0}, be32(1), make([]byte, 4)))
}

// qtWriteRoundTrip returns the chapters a fresh parse of the written QuickTime track
// yields: the decode-time of each sample (the running sum of the stts deltas, from
// zero) scaled to a Start and shifted by the leading empty-edit offset, the next
// sample's time as End, and titles capped like the samples.
func qtWriteRoundTrip(chapters []core.Chapter, mts, movieTimescale uint32, movieDuration uint64) (out []core.Chapter, saturated bool) {
	if len(chapters) == 0 {
		return nil, false
	}
	deltas, _ := chapterDeltas(chapters, mts, movieTimescale, movieDuration)
	// chapterEdts writes firstStart as a u32 segment_duration via clampU32; a reparse reads that
	// clamped value back, so derive the offset from it too - the prediction then stays equal even
	// past the 2^32-unit edge, and addClamp matches the read's saturating add.
	clampedFirstStart := clampU32(chapterFirstStart(movieTimescale, chapters))
	// Saturation has two sources, mirroring the read: an over-range stts delta (a >13.25 h
	// gap clamped by buildStts, read back as a MaxUint32 delta) and a leading empty-edit
	// offset whose clamped u32 segment_duration reads back as MaxUint32 (emptyEditOffset).
	saturated = slices.Contains(deltas, uint32(math.MaxUint32)) || clampedFirstStart == math.MaxUint32
	offset := scaleToDuration(uint64(clampedFirstStart), movieTimescale)
	cum := make([]uint64, len(chapters))
	for i := 1; i < len(chapters); i++ {
		cum[i] = cum[i-1] + uint64(deltas[i-1])
	}
	out = make([]core.Chapter, len(chapters))
	for i := range chapters {
		out[i] = core.Chapter{
			Start: addClamp(scaleToDuration(cum[i], mts), offset),
			Title: truncateUTF8(chapters[i].Title, titleByteMax),
		}
		if i+1 < len(chapters) {
			out[i].End = addClamp(scaleToDuration(cum[i+1], mts), offset)
		}
	}
	// Mirror decodeTextTrack's last-end recovery exactly: the final stts boundary (the
	// last cumulative sum plus the last delta) is the last chapter's end, reported
	// verbatim except for the synthetic one-unit placeholder tail on a chapter starting
	// at/past a known movie duration (isPlaceholderTail), which stays open.
	last := len(chapters) - 1
	lastEnd := addClamp(scaleToDuration(cum[last]+uint64(deltas[last]), mts), offset)
	if !isPlaceholderTail(out[last].Start, uint64(deltas[last]), mts, movieTimescale, movieDuration) {
		out[last].End = lastEnd
	}
	return out, saturated
}
