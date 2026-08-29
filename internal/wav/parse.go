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

// maxFactChunk bounds the "fact" read. Only the leading 4-byte dwSampleLength is
// decoded; a longer fact chunk (the spec leaves room for format-specific fields) is
// preserved verbatim on rewrite like any other chunk.
const maxFactChunk = 4

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
	form := string(hdr[0:4])
	rf64 := form == "RF64" || form == "BW64"
	if (!rf64 && form != "RIFF") || string(hdr[8:12]) != "WAVE" {
		return nil, fmt.Errorf("%w: missing RIFF/WAVE marker", waxerr.ErrInvalidData)
	}

	d := &doc{size: size, infoIdx: -1, id3Idx: -1, dataIdx: -1}
	copy(d.form[:], hdr[0:4])

	// The container size delimits it; bytes beyond it are appended out-of-container
	// data (e.g. an ID3v1 tag), not chunks. Trust it as the walk boundary only when
	// sane - a bogus 0 or 0xFFFFFFFF falls back to the file size so no chunk is
	// missed. For RF64 the 32-bit field is the 0xFFFFFFFF marker and the real size
	// lives in ds64, which must therefore be read before the walk.
	declaredSize := uint64(binary.LittleEndian.Uint32(hdr[4:8]))
	if rf64 {
		t, err := parseDS64(src, size, limit)
		if err != nil {
			return nil, err
		}
		d.ds64 = t
		declaredSize = t.riffSize
	}
	riffEnd := 8 + int64(declaredSize)
	if riffEnd < 12 || riffEnd > size || declaredSize > uint64(size) {
		riffEnd = size
	}

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
		case ch.id4() == "fact" && !d.hasFact:
			body, err := bits.ReadSlice(src, ch.bodyOff, min(ch.bodyLen, maxFactChunk), limit)
			if err != nil {
				return nil, err
			}
			if len(body) == maxFactChunk {
				// In an RF64/BW64 container the 32-bit field is the marker and the real count
				// lives in ds64 (EBU Tech 3306), exactly as for the chunk sizes. Reading it
				// from there makes the two agree by construction rather than by the sanity
				// gate happening to reject 0xFFFFFFFF.
				d.factSamples = uint64(binary.LittleEndian.Uint32(body))
				if d.ds64 != nil {
					d.factSamples = d.ds64.sampleCount
				}
				d.hasFact = true
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
		// type already confirmed INFO; a cap breach is a hard error (mirroring the
		// embedded-id3 sibling below), while a truncated/malformed list stays tolerant.
		d.info, err = parseInfo(body, opts.Limits.MaxElements)
		if errors.Is(err, waxerr.ErrSizeTooLarge) {
			return nil, err
		}
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

	d.track = buildTrack(d.fmtCfg, d.dataLen, d.factSamples, d.hasFact)

	media := &core.Media{
		Format:     core.FormatWAV,
		Native:     d,
		AudioStart: d.dataOff,
		AudioEnd:   d.dataOff + d.dataLen,
	}

	tags, pics, chapters, syncedLyrics, families, numericGenre, projWs := project(d)
	media.Tags = tags
	media.Pictures = pics
	media.Chapters = chapters
	media.SyncedLyrics = syncedLyrics
	media.Families = families
	warnings = append(warnings, projWs...)
	warnings = append(warnings, mediaWarnings(d, numericGenre)...)

	media.Properties = core.Properties{Container: d.containerName(), Tracks: []core.AudioTrack{d.track}}
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
func project(d *doc) (tags tag.TagSet, pics []core.Picture, chapters []core.Chapter, syncedLyrics []core.SyncedLyrics, families []core.FamilyValue, numericGenre bool, projWarnings []core.Warning) {
	tags = tag.NewTagSet()
	switch {
	case d.id3 != nil:
		proj := id3.Project(d.id3)
		tags = proj.Tags
		pics = proj.Pictures
		// Chapters and synced lyrics live only in the embedded id3 chunk (CHAP/CTOC, SYLT).
		// A native cue/adtl WAV chapter list is preserved opaque but not projected (a known
		// gap), so a bare WAV reports no chapters.
		chapters = proj.Chapters
		syncedLyrics = proj.SyncedLyrics
		projWarnings = proj.Warnings
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
	return tags, pics, chapters, syncedLyrics, families, numericGenre, projWarnings
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
	// Both regions survive a rewrite byte for byte (write.go), so this is the only
	// place they are ever mentioned. The in-RIFF one is counted in the recomputed
	// container size; the outer one deliberately is not.
	ws = core.WarnTrailing(ws, d.trailingLen, "after the last RIFF chunk", d.trailingWhat())
	ws = core.WarnTrailing(ws, d.outerLen, "after the RIFF container", "")
	return ws
}

// riffDialect parameterizes the shared IFF/RIFF walker for WAV: little-endian chunk
// sizes and a "data" audio chunk.
var riffDialect = iff.Dialect{Order: binary.LittleEndian, AudioID: [4]byte{'d', 'a', 't', 'a'}, Noun: "RIFF chunks"}

// walkChunks records every top-level RIFF chunk by identifier and source range via the
// shared iff walker, then copies the result into d. It reads only chunk headers (never
// bodies), so a large data chunk costs nothing.
func walkChunks(ctx context.Context, src core.ReaderAtSized, d *doc, riffEnd, limit int64, maxElements int) error {
	opts := iff.WalkOptions{
		Size: d.size, End: riffEnd, Limit: limit, MaxElements: maxElements, Dialect: riffDialect,
	}
	// Only an RF64/BW64 file has 64-bit sizes to resolve. Leaving the hook nil for plain
	// RIFF keeps the walk on its original path instead of calling a method that would
	// decline for every chunk of every file.
	if d.ds64 != nil {
		opts.SizeOverride = d.ds64.override
	}
	res, err := iff.WalkChunks(ctx, src, opts)
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
	d.trailingID3v1 = res.TrailingIsID3v1
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

// buildTrack assembles audio properties from the fmt geometry, the data length, and the
// fact chunk's declared sample count.
//
// A constant-rate PCM family (PCM, IEEE float, A-law, mu-law) keeps the byte-rate
// formulas, which are exact for it: duration is dataLen/byteRate, bitrate is the
// declared byte rate, and a sample frame is blockAlign bytes. Everything else has a
// nominal or absent avgBytesPerSec and a blockAlign that is a compressed block rather
// than a sample frame, so those formulas are wrong by whatever the compression ratio
// happens to be - an MS-ADPCM second reads as 1.4 s, and a block count reads as a sample
// count off by three orders of magnitude. For those the declared sample count is the only
// real length, and the bitrate follows from the bytes actually stored. Where neither is
// usable the duration falls back to the nominal byte rate (better than nothing) and the
// sample count stays 0 rather than reporting a block count as samples.
func buildTrack(fc fmtChunk, dataLen int64, factSamples uint64, hasFact bool) core.AudioTrack {
	t := core.AudioTrack{
		Codec: codecName(fc.audioFormat),
		// Cap the uint32->int conversions so a hostile fmt value cannot overflow
		// into a negative property on a 32-bit platform (where int is 32-bit), the
		// same int(uint32) hazard parseInfo guards. Real rates are far below the cap.
		SampleRate:    int(min(int64(fc.sampleRate), math.MaxInt32)),
		Channels:      int(fc.channels),
		BitsPerSample: int(fc.bitsPerSample),
	}
	constant := constantRatePCM(fc.audioFormat)
	if !constant && hasFact {
		if dur, ok := factDuration(factSamples, fc, dataLen); ok {
			t.Duration = dur
			t.TotalSamples = factSamples
		}
	}
	if t.Duration == 0 && fc.byteRate > 0 {
		secs := float64(dataLen) / float64(fc.byteRate)
		if secs > 0 && secs < float64(math.MaxInt64)/float64(time.Second) {
			t.Duration = time.Duration(secs * float64(time.Second))
		}
	}
	if constant {
		if fc.byteRate > 0 {
			t.Bitrate = int(min(int64(fc.byteRate)*8, math.MaxInt32))
		}
		if fc.blockAlign > 0 {
			t.TotalSamples = uint64(dataLen / int64(fc.blockAlign))
		}
		return t
	}
	t.Bitrate = core.AverageBitrate(dataLen, t.Duration.Seconds())
	return t
}

// constantRatePCM reports whether a WAVE format tag names an uncompressed family whose
// avgBytesPerSec is exact and whose blockAlign is one sample frame. WAVE_FORMAT_EXTENSIBLE
// counts: its SubFormat GUID is PCM or IEEE float in every file that uses it in practice,
// and treating it otherwise would regress the ordinary 24-bit and multichannel case.
func constantRatePCM(format uint16) bool {
	switch format {
	case 0x0001, 0x0003, 0x0006, 0x0007, 0xFFFE: // PCM, IEEE float, A-law, mu-law, extensible
		return true
	}
	return false
}

// factSanityRatio bounds how far the duration a fact chunk declares may sit from the one
// the nominal byte rate implies. It is deliberately loose: avgBytesPerSec is the value the
// fact count exists to correct, so this rejects garbage (a 0xFFFFFFFF sample count reads
// as tens of thousands of times the byte-rate estimate) without second-guessing an honest
// disagreement like MS-ADPCM's 1.4x.
const factSanityRatio = 8

// minFactBitrate is the floor an implied average bitrate must clear for a declared sample
// count to be believed: below 1 kbps there is no audio codec a RIFF file carries, only a
// count that is too large for the bytes stored. It backstops the byte-rate comparison, which
// says nothing when the declared rate is itself nonsense.
const minFactBitrate = 1000

// factDuration converts a declared sample count into a duration, reporting ok=false when
// the value cannot be trusted. dwSampleLength is attacker-controlled and some writers put
// bytes there rather than sample frames, so 0xFFFFFFFF on a 20 KB file would otherwise
// report a 27-hour duration and a nonsense bitrate.
//
// Two bounds, because either input can be the nonsense one. The declared duration must land
// within factSanityRatio of the byte-rate estimate when there is a byte rate to compare
// against (an MP3-in-WAV declares 0, so there often is not); and the audio it claims must be
// stored at a plausible bitrate, which catches an absurd sampleRate the byte-rate comparison
// cannot see - rate 1 with 800000 samples is 9.3 days of audio in 100 KB, at 1 bit per
// second.
func factDuration(samples uint64, fc fmtChunk, dataLen int64) (time.Duration, bool) {
	if samples == 0 || dataLen <= 0 {
		return 0, false
	}
	dur := core.SamplesToDuration(samples, int(min(int64(fc.sampleRate), math.MaxInt32)))
	if dur <= 0 {
		return 0, false
	}
	if core.AverageBitrate(dataLen, dur.Seconds()) < minFactBitrate {
		return 0, false
	}
	if fc.byteRate > 0 {
		est := float64(dataLen) / float64(fc.byteRate)
		secs := dur.Seconds()
		if secs > est*factSanityRatio || secs*factSanityRatio < est {
			return 0, false
		}
	}
	return dur, true
}

// codecName maps a WAVE format tag to a codec name. The table is shared with the ASF
// reader, whose Stream Properties object carries the same structure, so one format tag
// cannot read as two different codecs depending on the container.
func codecName(format uint16) string { return core.WaveFormatCodec(format) }
