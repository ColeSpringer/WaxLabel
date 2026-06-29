package wav

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/iff"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxMetaChunk bounds how large a metadata chunk (LIST, id3) we will read into
// memory. The data chunk is never read here - only its range is recorded - so
// this guards only the small structural chunks against a hostile size. It works
// alongside the user's MaxAllocBytes limit (whichever is smaller wins).
const maxMetaChunk = 64 << 20

// maxFmtChunk bounds the "fmt " read. Only the first 16 bytes are decoded (and a
// WAVE_FORMAT_EXTENSIBLE chunk is 40), so there is no reason to read a chunk that
// declares a larger body into memory - the rest is copied from the source on
// rewrite regardless.
const maxFmtChunk = 40

// parse reads a WAV file's chunk structure into a neutral Media: the audio
// geometry from "fmt ", the canonical tags from the id3 chunk (authoritative)
// or LIST/INFO (the fallback authority), the family/source view for both, and
// every chunk preserved as the base for a preservation-first rewrite.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes

	hdr, err := bits.ReadSlice(src, 0, 12, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: WAV file shorter than a RIFF header", waxerr.ErrInvalidData)
	}
	switch {
	case string(hdr[0:4]) == "RF64" || string(hdr[0:4]) == "BW64":
		return nil, fmt.Errorf("%w: RF64/BW64 (>4 GiB WAV) is out of scope", waxerr.ErrUnsupportedFormat)
	case string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE":
		return nil, fmt.Errorf("%w: missing RIFF/WAVE marker", waxerr.ErrInvalidData)
	}

	// The RIFF size delimits the container; bytes beyond it are appended out-of-RIFF
	// data (e.g. an ID3v1 tag), not chunks. Trust it as the walk boundary only when
	// sane - a bogus 0 or 0xFFFFFFFF falls back to the file size so no chunk is
	// missed.
	riffEnd := 8 + int64(binary.LittleEndian.Uint32(hdr[4:8]))
	if riffEnd < 12 || riffEnd > size {
		riffEnd = size
	}

	d := &doc{size: size, infoIdx: -1, id3Idx: -1, dataIdx: -1}
	if err := walkChunks(ctx, src, d, riffEnd, limit, opts.Limits.MaxElements); err != nil {
		return nil, err
	}

	var warnings []core.Warning

	// Decode the small structural chunks.
	if d.dataIdx >= 0 {
		d.dataOff = d.chunks[d.dataIdx].bodyOff
		d.dataLen = d.chunks[d.dataIdx].bodyLen
	}
	// First pass over the already-walked chunks: parse fmt, and collect the INFO
	// list and id3 chunk candidate indices (resolving the authoritative one and
	// duplicates afterward, so a corrupt-then-valid id3 pair is handled correctly).
	fmtFound := false
	var infoIdxs, id3Idxs []int
	for i := range d.chunks {
		ch := d.chunks[i]
		switch {
		case ch.id4() == "fmt " && !fmtFound:
			body, err := bits.ReadSlice(src, ch.bodyOff, min(ch.bodyLen, maxFmtChunk), limit)
			if err != nil {
				return nil, err
			}
			if fc, ok := parseFmt(body); ok {
				d.fmtCfg = fc
				fmtFound = true
			}
		case ch.id4() == "LIST":
			// Peek the list type before reading the whole body, so a large non-INFO
			// list (e.g. "adtl") is preserved verbatim without being read into memory.
			typ, err := bits.ReadSlice(src, ch.bodyOff, min(ch.bodyLen, 4), limit)
			if err != nil {
				return nil, err
			}
			if string(typ) == "INFO" {
				infoIdxs = append(infoIdxs, i)
			}
		case isID3Chunk(ch.id4()):
			id3Idxs = append(id3Idxs, i)
		}
	}

	// The first INFO list is authoritative; parse it and mark any extras dropped.
	if len(infoIdxs) > 0 {
		i := infoIdxs[0]
		body, err := bits.ReadSlice(src, d.chunks[i].bodyOff, min(d.chunks[i].bodyLen, maxMetaChunk), limit)
		if err != nil {
			return nil, err
		}
		d.info, _ = parseInfo(body) // type already confirmed INFO
		d.infoIdx = i
		markDup(d, infoIdxs[1:])
	}

	// The first id3 chunk that parses is authoritative; every other id3 chunk -
	// a duplicate, or a corrupt one sitting beside a valid one - is marked dropped
	// so the output never carries two id3 chunks.
	for _, i := range id3Idxs {
		body, err := bits.ReadSlice(src, d.chunks[i].bodyOff, min(d.chunks[i].bodyLen, maxMetaChunk), limit)
		if err != nil {
			return nil, err
		}
		tg, perr := id3.ParseTag(body, opts.Limits.MaxElements)
		if perr == nil {
			d.id3 = tg
			d.id3Idx = i
			break
		}
		// A bounded-allocation cap breach (a hostile frame flood hitting MaxElements) is a hard
		// error, not a benign "this chunk is not a tag": swallowing it would silently treat a
		// structurally-valid id3 chunk as absent and rewrite the file without it. Surface it like
		// the MP3/AAC front-tag path does. An ordinary malformed chunk still falls through to the
		// LIST/INFO fallback.
		if errors.Is(perr, waxerr.ErrSizeTooLarge) {
			return nil, perr
		}
	}
	if d.id3Idx >= 0 {
		for _, i := range id3Idxs {
			if i != d.id3Idx {
				d.chunks[i].dupTag = true
			}
		}
	}

	if len(infoIdxs) > 1 {
		warnings = core.Warn(warnings, core.WarnDuplicateTagBlock,
			"more than one LIST/INFO chunk; the first is authoritative and the rest are dropped on rewrite")
	}
	if len(id3Idxs) > 1 && d.id3Idx >= 0 {
		warnings = core.Warn(warnings, core.WarnDuplicateTagBlock,
			"more than one id3 chunk; the first that parses is authoritative and the rest are dropped on rewrite")
	}
	// The data chunk declared more bytes than the file holds: a truncated WAV.
	if d.dataTruncated {
		warnings = core.WarnTruncated(warnings, "the data chunk")
	}
	// A non-audio chunk declared more bytes than the file holds and was clamped.
	for _, id := range d.oversizedChunks {
		warnings = core.Warn(warnings, core.WarnOversizedChunk,
			fmt.Sprintf("the %q chunk declares more bytes than the file holds and was clamped to EOF", string(id[:])))
	}

	d.track = buildTrack(d.fmtCfg, d.dataLen)

	media := &core.Media{
		Format:     core.FormatWAV,
		Native:     d,
		AudioStart: d.dataOff,
		AudioEnd:   d.dataOff + d.dataLen,
	}

	tags, pics, chapters, families, numericGenre, chapterWs := project(d)
	media.Tags = tags
	media.Pictures = pics
	media.Chapters = chapters
	media.Families = families
	warnings = append(warnings, chapterWs...)
	warnings = append(warnings, mediaWarnings(d, numericGenre)...)

	media.Properties = core.Properties{Container: "WAV", Tracks: []core.AudioTrack{d.track}}
	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// project derives the canonical view from a parsed (or rewritten) document under
// the read-precedence policy: the embedded id3 chunk is authoritative when
// present, and LIST/INFO fills in any canonical key id3 does not carry - so an
// INFO-only value (e.g. a Copyright present only in INFO) enters the canonical
// set and survives a rewrite rather than being silently dropped. When there is
// no id3 chunk, INFO is the sole authority. Either way INFO also contributes
// family entries with conflicts flagged (mirroring how MP3 surfaces ID3v1/APEv2).
// It is shared by Parse and the post-write result so they cannot disagree.
func project(d *doc) (tags tag.TagSet, pics []core.Picture, chapters []core.Chapter, families []core.FamilyValue, numericGenre bool, chapterWarnings []core.Warning) {
	tags = tag.NewTagSet()
	switch {
	case d.id3 != nil:
		proj := id3.Project(d.id3)
		tags = proj.Tags
		pics = proj.Pictures
		// Chapters live only in the embedded id3 chunk (CHAP/CTOC). A native cue/adtl
		// WAV chapter list is preserved opaque but not projected (a known gap), so a bare
		// WAV reports no chapters.
		chapters = proj.Chapters
		chapterWarnings = proj.Warnings
		families = proj.Families
		numericGenre = proj.NumericGenre
		// id3 wins on conflict; INFO fills keys id3 lacks (precedence merge).
		infoSet := infoTags(d.info)
		for _, k := range infoSet.Keys() {
			if tags.Has(k) {
				continue
			}
			vs, _ := infoSet.Get(k)
			tags.Add(k, vs...)
		}
		families = append(families, infoFamilies(tags, d.info)...)
	case len(d.info) > 0:
		tags = infoTags(d.info)
		families = infoFamilies(tags, d.info)
	}
	return tags, pics, chapters, families, numericGenre, chapterWarnings
}

// markDup flags the given chunk indices as redundant duplicate tag containers,
// so they are dropped when the file is rewritten.
func markDup(d *doc, idxs []int) {
	for _, i := range idxs {
		d.chunks[i].dupTag = true
	}
}

// mediaWarnings returns the content-derived warnings for a parsed or rewritten
// document: a resolved numeric genre and inherited-encoder stamps. Structural
// warnings found only while walking the source (duplicate tag blocks) are added
// by Parse itself. Sharing this lets the post-write document's warnings match a
// fresh parse of the output rather than echoing the original parse's warnings.
func mediaWarnings(d *doc, numericGenre bool) []core.Warning {
	var ws []core.Warning
	if numericGenre {
		ws = core.Warn(ws, core.WarnNumericGenre, "a numeric genre reference was resolved to a name")
	}
	ws = append(ws, encoderNoise(d.info)...)
	ws = append(ws, id3.EncoderNoise(d.id3)...)
	return ws
}

// riffDialect parameterizes the shared IFF/RIFF walker for WAV: little-endian chunk
// sizes and a "data" audio chunk.
var riffDialect = iff.Dialect{Order: binary.LittleEndian, AudioID: [4]byte{'d', 'a', 't', 'a'}, Noun: "RIFF chunks"}

// walkChunks records every top-level RIFF chunk by identifier and source range via the
// shared iff walker, then copies the result into d. It reads only chunk headers (never
// bodies), so a large data chunk costs nothing.
func walkChunks(ctx context.Context, src core.ReaderAtSized, d *doc, riffEnd, limit int64, maxElements int) error {
	res, err := iff.WalkChunks(ctx, src, d.size, riffEnd, limit, maxElements, riffDialect)
	if err != nil {
		return err
	}
	d.chunks = make([]chunk, len(res.Chunks))
	for i, c := range res.Chunks {
		d.chunks[i] = chunk{id: c.ID, bodyOff: c.BodyOff, bodyLen: c.BodyLen}
	}
	d.dataIdx = res.AudioIdx
	d.dataTruncated = res.AudioTruncated
	d.oversizedChunks = res.OversizedChunks
	d.trailingOff, d.trailingLen = res.TrailingOff, res.TrailingLen
	d.outerOff, d.outerLen = res.OuterOff, res.OuterLen
	return nil
}

// isID3Chunk reports whether a chunk identifier holds an embedded ID3v2 tag.
// "id3 " is the de-facto identifier; "ID3 " is the uppercase variant some tools
// emit. Both are read; the writer emits "id3 ".
func isID3Chunk(id string) bool { return id == "id3 " || id == "ID3 " }

// parseFmt decodes the common leading fields of a "fmt " chunk. The first 16
// bytes cover PCM and the common compressed forms; WAVE_FORMAT_EXTENSIBLE and
// longer fmt chunks carry extra bytes after these, which are not needed here.
func parseFmt(b []byte) (fmtChunk, bool) {
	if len(b) < 16 {
		return fmtChunk{}, false
	}
	return fmtChunk{
		audioFormat:   binary.LittleEndian.Uint16(b[0:2]),
		channels:      binary.LittleEndian.Uint16(b[2:4]),
		sampleRate:    binary.LittleEndian.Uint32(b[4:8]),
		byteRate:      binary.LittleEndian.Uint32(b[8:12]),
		blockAlign:    binary.LittleEndian.Uint16(b[12:14]),
		bitsPerSample: binary.LittleEndian.Uint16(b[14:16]),
	}, true
}

// buildTrack assembles audio properties from the fmt geometry and data length.
// For PCM (and other constant-rate forms) the duration follows directly from the
// byte rate; total samples follow from the block alignment.
func buildTrack(fc fmtChunk, dataLen int64) core.AudioTrack {
	t := core.AudioTrack{
		Codec: codecName(fc.audioFormat),
		// Cap the uint32->int conversions so a hostile fmt value cannot overflow
		// into a negative property on a 32-bit platform (where int is 32-bit), the
		// same int(uint32) hazard parseInfo guards. Real rates are far below the cap.
		SampleRate:    int(min(int64(fc.sampleRate), math.MaxInt32)),
		Channels:      int(fc.channels),
		BitsPerSample: int(fc.bitsPerSample),
	}
	if fc.byteRate > 0 {
		t.Bitrate = int(min(int64(fc.byteRate)*8, math.MaxInt32))
		secs := float64(dataLen) / float64(fc.byteRate)
		if secs > 0 && secs < float64(math.MaxInt64)/float64(time.Second) {
			t.Duration = time.Duration(secs * float64(time.Second))
		}
	}
	if fc.blockAlign > 0 {
		t.TotalSamples = uint64(dataLen / int64(fc.blockAlign))
	}
	return t
}

// codecName maps a WAVE format tag to a human-readable codec name. Only the
// common tags are named; the rest report their numeric tag.
func codecName(format uint16) string {
	switch format {
	case 0x0001:
		return "PCM"
	case 0x0003:
		return "IEEE float"
	case 0x0006:
		return "A-law"
	case 0x0007:
		return "mu-law"
	case 0x0011:
		return "IMA ADPCM"
	case 0x0055:
		return "MP3"
	case 0xFFFE:
		return "PCM (extensible)"
	default:
		return fmt.Sprintf("WAVE format 0x%04X", format)
	}
}
