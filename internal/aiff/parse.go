package aiff

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// maxMetaChunk bounds how large a metadata chunk (a native text chunk or ID3) we
// will read into memory. The SSND sound chunk is never read here — only its range
// is recorded — so this guards only the small structural chunks against a hostile
// size, alongside the user's MaxAllocBytes limit (whichever is smaller wins).
const maxMetaChunk = 64 << 20

// maxCommChunk bounds the "COMM" read. The 18-byte common fields plus an AIFF-C
// compression type fit well within this; the rest (an AIFF-C compression name) is
// not decoded, and the chunk is copied from the source on rewrite regardless.
const maxCommChunk = 64

// ssndHeaderLen is the size of SSND's offset + blockSize sub-header, which
// precedes the sample frames and is not itself audio.
const ssndHeaderLen = 8

// parse reads an AIFF file's chunk structure into a neutral Media: the audio
// geometry from "COMM", the canonical tags from the ID3 chunk (authoritative) or
// the native text chunks (the fallback authority), the family/source view for
// both, and every chunk preserved as the base for a preservation-first rewrite.
func parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes

	hdr, err := bits.ReadSlice(src, 0, 12, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: AIFF file shorter than a FORM header", waxerr.ErrInvalidData)
	}
	if string(hdr[0:4]) != "FORM" {
		return nil, fmt.Errorf("%w: missing FORM marker", waxerr.ErrInvalidData)
	}
	formType := string(hdr[8:12])
	if formType != "AIFF" && formType != "AIFC" {
		return nil, fmt.Errorf("%w: FORM type %q is not AIFF/AIFC", waxerr.ErrInvalidData, formType)
	}

	// The FORM size delimits the container; bytes beyond it are appended out-of-FORM
	// data, not chunks. Trust it as the walk boundary only when sane — a bogus 0 or
	// 0xFFFFFFFF falls back to the file size so no chunk is missed.
	formEnd := 8 + int64(binary.BigEndian.Uint32(hdr[4:8]))
	if formEnd < 12 || formEnd > size {
		formEnd = size
	}

	d := &doc{size: size, commIdx: -1, ssndIdx: -1, id3Idx: -1}
	copy(d.formType[:], hdr[8:12])
	if err := walkChunks(ctx, src, d, formEnd, limit); err != nil {
		return nil, err
	}

	var warnings []core.Warning
	isAIFC := formType == "AIFC"

	// First pass over the walked chunks: decode COMM, collect the native text
	// chunks and the ID3 chunk candidate indices (resolving the authoritative ID3
	// and duplicates afterward, so a corrupt-then-valid ID3 pair is handled right).
	commFound := false
	var id3Idxs []int
	for i := range d.chunks {
		ch := d.chunks[i]
		switch {
		case ch.id4() == "COMM" && !commFound:
			body, err := bits.ReadSlice(src, ch.bodyOff, min(ch.bodyLen, maxCommChunk), limit)
			if err != nil {
				return nil, err
			}
			if c, ok := parseCOMM(body, isAIFC); ok {
				d.comm = c
				d.commIdx = i
				commFound = true
			}
		case isTextChunk(ch.id4()):
			body, err := bits.ReadSlice(src, ch.bodyOff, min(ch.bodyLen, maxMetaChunk), limit)
			if err != nil {
				return nil, err
			}
			d.textIdx = append(d.textIdx, i)
			d.texts = append(d.texts, textItem{id: ch.id, raw: textValue(body)})
		case isID3Chunk(ch.id4()):
			id3Idxs = append(id3Idxs, i)
		}
	}

	// The first ID3 chunk that parses is authoritative; every other ID3 chunk — a
	// duplicate, or a corrupt one beside a valid one — is marked dropped so the
	// output never carries two ID3 chunks.
	for _, i := range id3Idxs {
		body, err := bits.ReadSlice(src, d.chunks[i].bodyOff, min(d.chunks[i].bodyLen, maxMetaChunk), limit)
		if err != nil {
			return nil, err
		}
		if tg, perr := id3.ParseTag(body); perr == nil {
			d.id3 = tg
			d.id3Idx = i
			break
		}
	}
	if d.id3Idx >= 0 {
		for _, i := range id3Idxs {
			if i != d.id3Idx {
				d.chunks[i].dupTag = true
			}
		}
	}
	if len(id3Idxs) > 1 && d.id3Idx >= 0 {
		warnings = core.Warn(warnings, core.WarnDuplicateTagBlock,
			"more than one ID3 chunk; the first is authoritative and the rest are dropped on rewrite")
	}

	if d.ssndIdx >= 0 {
		ch := d.chunks[d.ssndIdx]
		d.audioOff = soundDataStart(ch.bodyOff, ch.bodyLen)
		d.audioEnd = ch.bodyOff + ch.bodyLen
	}

	d.track = buildTrack(d.comm)

	media := &core.Media{
		Format:     core.FormatAIFF,
		Native:     d,
		AudioStart: d.audioOff,
		AudioEnd:   d.audioEnd,
	}

	tags, pics, families, numericGenre := project(d)
	media.Tags = tags
	media.Pictures = pics
	media.Families = families
	warnings = append(warnings, mediaWarnings(d, numericGenre)...)

	media.Properties = core.Properties{Container: formType, Tracks: []core.AudioTrack{d.track}}
	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// project derives the canonical view from a parsed (or rewritten) document under
// the read-precedence policy: the embedded ID3 chunk is authoritative when
// present, and the native text chunks fill in any canonical key ID3 does not
// carry — so a native-only value (a Copyright in a "(c) " chunk, say) enters the
// canonical set and survives a rewrite rather than being silently dropped. When
// there is no ID3 chunk, the native chunks are the sole authority. Either way the
// native chunks also contribute family entries with conflicts flagged. It is
// shared by Parse and the post-write result so they cannot disagree.
func project(d *doc) (tags tag.TagSet, pics []core.Picture, families []core.FamilyValue, numericGenre bool) {
	tags = tag.NewTagSet()
	switch {
	case d.id3 != nil:
		proj := id3.Project(d.id3)
		tags = proj.Tags
		pics = proj.Pictures
		families = proj.Families
		numericGenre = proj.NumericGenre
		// ID3 wins on conflict; the native chunks fill keys ID3 lacks (precedence
		// merge), so a native-only value is not lost.
		nativeSet := textTags(d.texts)
		for _, k := range nativeSet.Keys() {
			if tags.Has(k) {
				continue
			}
			vs, _ := nativeSet.Get(k)
			tags.Add(k, vs...)
		}
		families = append(families, textFamilies(tags, d.texts)...)
	case len(d.texts) > 0:
		tags = textTags(d.texts)
		families = textFamilies(tags, d.texts)
	}
	return tags, pics, families, numericGenre
}

// mediaWarnings returns the content-derived warnings for a parsed or rewritten
// document: a resolved numeric genre and an inherited-encoder stamp from the ID3
// chunk's TSSE/TENC frame (the AIFF analogue of WAV's ISFT scan — ffmpeg writes
// the "Lavf..." stamp into ID3, not the native chunks). Structural warnings found
// only while walking the source (duplicate ID3 chunks) are added by Parse itself.
// Sharing this lets the post-write document's warnings match a fresh parse.
func mediaWarnings(d *doc, numericGenre bool) []core.Warning {
	var ws []core.Warning
	if numericGenre {
		ws = core.Warn(ws, core.WarnNumericGenre, "a numeric genre reference was resolved to a name")
	}
	ws = append(ws, id3.EncoderNoise(d.id3)...)
	return ws
}

// walkChunks records every top-level IFF chunk by identifier and source range,
// noting the index of the first COMM and SSND chunk. It reads only chunk headers
// (never bodies), so a large SSND chunk costs nothing.
func walkChunks(ctx context.Context, src core.ReaderAtSized, d *doc, formEnd, limit int64) error {
	size := d.size
	off := int64(12)
	for off+8 <= size && off < formEnd {
		if err := ctx.Err(); err != nil {
			return err
		}
		head, err := bits.ReadSlice(src, off, 8, limit)
		if err != nil {
			return err
		}
		var id [4]byte
		copy(id[:], head[0:4])
		bodyLen := int64(binary.BigEndian.Uint32(head[4:8]))
		bodyOff := off + 8
		// Clamp a declared size that runs past EOF (corrupt or truncated) so the
		// range stays valid; this becomes the last chunk.
		if bodyLen > size-bodyOff {
			bodyLen = size - bodyOff
		}
		idx := len(d.chunks)
		d.chunks = append(d.chunks, chunk{id: id, bodyOff: bodyOff, bodyLen: bodyLen})
		if id == [4]byte{'S', 'S', 'N', 'D'} && d.ssndIdx < 0 {
			d.ssndIdx = idx
		}
		next := bodyOff + bodyLen + (bodyLen & 1) // word-alignment pad byte
		if next <= off {
			break // no forward progress (corrupt) — stop and preserve the rest
		}
		off = next
	}
	// Leftover bytes still inside the FORM chunk (a corrupt region): preserved and
	// counted in the FORM size.
	if off < formEnd {
		d.trailingOff = off
		d.trailingLen = formEnd - off
	}
	// Bytes after the FORM chunk: preserved verbatim but kept outside the
	// recomputed FORM size. max(off, formEnd) avoids double-counting a final chunk
	// whose declared body straddled the boundary.
	if outerStart := max(off, formEnd); outerStart < size {
		d.outerOff = outerStart
		d.outerLen = size - outerStart
	}
	if len(d.chunks) == 0 {
		return fmt.Errorf("%w: no IFF chunks", waxerr.ErrInvalidData)
	}
	return nil
}

// soundDataStart returns the offset of the first sample frame within an SSND
// chunk: the body offset advanced past the 8-byte offset/blockSize sub-header.
// A too-short SSND (no room for the sub-header) falls back to the body offset.
func soundDataStart(bodyOff, bodyLen int64) int64 {
	if bodyLen >= ssndHeaderLen {
		return bodyOff + ssndHeaderLen
	}
	return bodyOff
}

// isTextChunk reports whether a chunk identifier is a native AIFF text chunk
// (NAME/AUTH/"(c) "/ANNO).
func isTextChunk(id string) bool {
	_, ok := mapping.AIFFTextKey(id)
	return ok
}

// isID3Chunk reports whether a chunk identifier holds an embedded ID3v2 tag.
// "ID3 " is the de-facto AIFF identifier; "id3 " is the lowercase variant some
// tools emit. Both are read; the writer emits "ID3 ".
func isID3Chunk(id string) bool { return id == "ID3 " || id == "id3 " }

// textValue extracts a native text chunk's value: the character run up to the
// first NUL (AIFF text chunks are commonly NUL-padded). Cutting at the first NUL
// — rather than only trimming trailing NULs — keeps an interior NUL from later
// truncating an ID3 text frame when the value is promoted to the ID3 chunk.
func textValue(body []byte) []byte {
	for i, b := range body {
		if b == 0 {
			return slices.Clone(body[:i])
		}
	}
	return slices.Clone(body)
}
