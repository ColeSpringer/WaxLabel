package mp4

import (
	"encoding/binary"
	"math"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
)

// This file builds a QuickTime chapter text track — the representation iTunes and
// Apple Books read (they ignore the Nero chpl). A chapter edit writes both: the
// chpl (write_chapters.go) and this track. The track is a "text" trak in moov,
// referenced from the audio track via a tref "chap"; its samples (one per
// chapter: a 16-bit length, the UTF-8 title, and an "encd" text-encoding box)
// live in a fresh mdat appended at end-of-file, so audio data never moves to make
// room. The static box shapes mirror what ffmpeg's muxer writes, for maximum
// player compatibility; only the dynamic parts (track id, durations, and the
// sample tables) are computed per file.

// encdBox is the text-encoding modifier ffmpeg appends to each chapter text
// sample (12 bytes: size, "encd", and the 0x0100 encoding value). The sample
// readers here and in ffmpeg take the title from the 16-bit length prefix and
// ignore this trailer, but it is included verbatim so output matches ffmpeg's.
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

// clampU32 saturates a 64-bit count to 32 bits for a v0 box field (the
// tkhd/mdhd/elst durations). At an audio file's movie timescale (~1 ms) this
// never bites — MaxUint32 ms is ~49 days; only an extreme media length under an
// unusually high movie timescale (e.g. a video-style 90 kHz over ~13 h) could
// saturate a duration. A per-sample stts delta is a 32-bit field by spec anyway.
func clampU32(n uint64) uint32 {
	if n > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n)
}

// chapterTextEntry is ffmpeg's QuickTime "text" sample description, captured
// verbatim from a real chapter track (it carries a self data-reference index of
// 1 and an empty default font table). It is opaque styling; only its presence
// and shape matter, so it is embedded rather than reconstructed field by field.
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

// chapterEdts maps the whole media into the movie timeline once at normal rate.
func chapterEdts(duration uint64) []byte {
	entry := make([]byte, 12)
	binary.BigEndian.PutUint32(entry[0:4], clampU32(duration)) // segment_duration (movie ts)
	binary.BigEndian.PutUint32(entry[8:12], 0x00010000)        // media_rate 1.0 (media_time 0)
	elst := renderFullBox(atomName("elst"), slices.Concat(be32(1), entry))
	return renderAtom(atomName("edts"), elst)
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

// chapterDeltas returns each chapter sample's stts duration in movie-timescale
// units: the gap to the next chapter, and for the last chapter its own End (or
// the movie duration) so the track spans the whole movie. Deltas are clamped
// non-negative so an out-of-order list cannot encode a negative duration. The
// reader sums these from zero, so they double as the basis for the result view.
func chapterDeltas(chapters []core.Chapter, mts uint32, movieDuration uint64) []uint32 {
	n := len(chapters)
	starts := make([]uint64, n)
	for i, ch := range chapters {
		starts[i] = durationToUnits(ch.Start, mts)
	}
	deltas := make([]uint32, n)
	for i := range n {
		var next uint64
		switch {
		case i+1 < n:
			next = starts[i+1]
		case chapters[i].End > chapters[i].Start:
			next = durationToUnits(chapters[i].End, mts)
		case movieDuration > starts[i]:
			next = movieDuration
		default:
			next = starts[i] + uint64(mts) // a one-second tail when nothing else bounds it
		}
		if next < starts[i] {
			next = starts[i]
		}
		deltas[i] = clampU32(next - starts[i])
	}
	return deltas
}

// buildChapterTrak renders the whole chapter text trak with a placeholder chunk
// offset (the appended mdat's address is not known until the moov delta is). It
// returns the trak bytes and the byte offset of the placeholder offset entry
// within them: because the offset table is the last atom in the track, that
// entry is simply the final 4 (stco) or 8 (co64) bytes.
func buildChapterTrak(trackID, mts uint32, movieDuration uint64, chapters []core.Chapter, co64 bool) (trak []byte, stcoEntryOff int) {
	deltas := chapterDeltas(chapters, mts, movieDuration)
	var totalDur uint64
	for _, d := range deltas {
		totalDur += uint64(d)
	}

	stbl := renderAtom(atomName("stbl"), slices.Concat(
		chapterStsd(), buildStts(deltas), buildStsc(len(chapters)), buildStsz(chapters), buildStco(co64)))
	minf := renderAtom(atomName("minf"), slices.Concat(chapterGmhd(), chapterDinf(), stbl))
	mdia := renderAtom(atomName("mdia"), slices.Concat(chapterMdhd(mts, totalDur), chapterHdlr(), minf))
	trak = renderAtom(atomName("trak"), slices.Concat(chapterTkhd(trackID, totalDur), chapterEdts(totalDur), mdia))

	width := 4
	if co64 {
		width = 8
	}
	return trak, len(trak) - width
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

// qtWriteRoundTrip returns the chapters a fresh parse of the written QuickTime
// track yields: the decode-time of each sample (the running sum of the stts
// deltas, from zero) scaled back to a Start, the next sample's time as End (the
// last End is left open, as the reader leaves it), and titles capped like the
// samples. It mirrors decodeTextTrack exactly, so the post-write result equals a
// reparse. (Because decode times run from zero, a first chapter that does not
// start at zero is normalized to zero here — the same as on read.)
func qtWriteRoundTrip(chapters []core.Chapter, mts uint32, movieDuration uint64) []core.Chapter {
	if len(chapters) == 0 {
		return nil
	}
	deltas := chapterDeltas(chapters, mts, movieDuration)
	cum := make([]uint64, len(chapters))
	for i := 1; i < len(chapters); i++ {
		cum[i] = cum[i-1] + uint64(deltas[i-1])
	}
	out := make([]core.Chapter, len(chapters))
	for i := range chapters {
		out[i] = core.Chapter{
			Start: scaleToDuration(cum[i], mts),
			Title: truncateUTF8(chapters[i].Title, titleByteMax),
		}
		if i+1 < len(chapters) {
			out[i].End = scaleToDuration(cum[i+1], mts)
		}
	}
	return out
}
