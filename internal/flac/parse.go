package flac

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

var flacMagic = []byte("fLaC")

// Parse reads FLAC metadata into a Media. Native doc is the edit base; TagSet and
// typed fields are derived from it.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes

	d := &doc{}
	var warnings []core.Warning

	// Stray leading ID3v2, preserved.
	if hdr, err := bits.ReadSlice(src, 0, 10, limit); err == nil {
		if n := id3v2Len(hdr); n > 0 && n <= size {
			d.leadingID3, err = bits.ReadSlice(src, 0, n, limit)
			if err != nil {
				return nil, err
			}
			d.flacStart = n
			warnings = core.Warn(warnings, core.WarnStrayLeadingID3,
				fmt.Sprintf("ID3v2 tag of %d bytes precedes the FLAC stream; preserved", n))
		}
	}

	c := bits.NewCursorAt(src, d.flacStart, size-d.flacStart, limit)
	if magic := c.Bytes(4); string(magic) != string(flacMagic) {
		return nil, fmt.Errorf("%w: missing fLaC marker", waxerr.ErrInvalidData)
	}

	maxElements := opts.Limits.MaxElements
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := bits.CheckElementCap(len(d.blocks), maxElements, "FLAC metadata blocks"); err != nil {
			return nil, err
		}
		h0 := c.Byte()
		length := c.U24BE()
		if c.Err() != nil {
			// Keep cursor sentinel (e.g. ErrSizeTooLarge) via %w.
			return nil, fmt.Errorf("truncated block header: %w", c.Err())
		}
		code := h0 & 0x7F
		last := h0&0x80 != 0
		if code == blkInvalid {
			return nil, fmt.Errorf("%w: invalid block type 127", waxerr.ErrInvalidData)
		}
		body := c.Bytes(int64(length))
		if c.Err() != nil {
			return nil, fmt.Errorf("truncated %s block: %w", blockName(code), c.Err())
		}
		d.blocks = append(d.blocks, block{code: code, body: body})
		if last {
			break
		}
	}
	d.audioStart = c.Pos()
	d.audioEnd = size

	if len(d.blocks) == 0 || d.blocks[0].code != blkStreamInfo {
		return nil, fmt.Errorf("%w: STREAMINFO must be the first block", waxerr.ErrInvalidData)
	}

	// Trailing ID3v1, only if entirely after metadata (else "TAG" in audio at size-128
	// would push audioEnd before audioStart).
	if size >= 128 && size-128 >= d.audioStart {
		if tail, err := bits.ReadSlice(src, size-128, 128, limit); err == nil && id3.LooksLikeID3v1(tail) {
			d.trailingID3v1 = tail
			d.audioEnd = size - 128
			warnings = core.Warn(warnings, core.WarnTrailingID3v1,
				"legacy ID3v1 tag follows the audio; preserved")
		}
	}

	streamInfo, err := parseStreamInfo(d.blocks[0].body)
	if err != nil {
		return nil, err
	}
	d.streamInfo = streamInfo

	// No encoded byte length in STREAMINFO; truncation/junk come from frames.
	// Located trailing region is carved from the audio extent (out of digest/bitrate)
	// but still copied on write.
	tailWarnings, trailingJunk, err := frameTailWarnings(ctx, src, d, limit)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, tailWarnings...)
	d.trailingJunk = trailingJunk
	d.audioEnd -= trailingJunk

	media := &core.Media{
		Format:     core.FormatFLAC,
		Native:     d,
		AudioStart: d.audioStart,
		AudioEnd:   d.audioEnd,
	}

	// First Vorbis comment block wins; warn on extras.
	vcCount := 0
	for _, b := range d.blocks {
		if b.code != blkVorbisComment {
			continue
		}
		vcCount++
		if vcCount > 1 {
			warnings = core.Warn(warnings, core.WarnMultipleVorbisComment,
				"more than one Vorbis comment block; the first is authoritative and the extras are dropped if the file is rewritten")
			// Writer grades extras against what it stores; unparseable extras stay silent.
			if _, extra, err := parseVorbisComment(b.body, limit, maxElements); err == nil {
				lose, _ := projectComments(extra)
				d.dupContent = append(d.dupContent, core.DuplicateContent{Tags: lose})
			}
			continue
		}
		vendor, comments, err := parseVorbisComment(b.body, limit, maxElements)
		if err != nil {
			return nil, err
		}
		d.vendor = vendor
		d.comments = comments
	}

	media.Tags, media.Families = projectComments(d.comments)
	// Legacy ID3 into family entries (not canonical); conflicts flagged. Opaque when
	// leading ID3v2 holds non-tag content a strip cannot prove redundant.
	legacyFams, legacyOpaque := flacLegacyFamilies(media.Tags, d.leadingID3, d.trailingID3v1, maxElements)
	media.Families = append(media.Families, legacyFams...)
	media.LegacyOpaqueContent = legacyOpaque
	media.Chapters = projectChapters(d.comments)
	var syncedWarnings []core.Warning
	media.SyncedLyrics, syncedWarnings = projectSyncedLyricsReport(d.comments)
	warnings = append(warnings, syncedWarnings...)
	warnings = append(warnings, encoderNoiseWarnings(d.vendor, d.comments)...)
	warnings = append(warnings, invalidKeyWarnings(d.comments)...)

	// Malformed picture: warn, skip from Media.Pictures, keep raw body for re-emit.
	for _, b := range d.blocks {
		if b.code != blkPicture {
			continue
		}
		p, err := parsePictureBlock(b.body, limit)
		if err != nil {
			warnings = core.Warn(warnings, core.WarnInvalidPicture, err.Error())
			d.malformedPictureBlocks = append(d.malformedPictureBlocks, b.body)
			continue
		}
		media.Pictures = append(media.Pictures, p)
	}

	// Base64 METADATA_BLOCK_PICTURE comments (Ogg-style). Decode, strip from tags,
	// record for materialization on rewrite.
	var commentPics []core.Picture
	var picWarnings []core.Warning
	d.comments, commentPics, picWarnings = extractCommentPictures(d.comments, limit)
	d.commentPictures = commentPics
	media.Pictures = append(media.Pictures, commentPics...)
	warnings = append(warnings, picWarnings...)
	// media.Pictures keeps stored MIME/dimensions (edit/write source). Sniffed type
	// is applied at display (Document.Pictures / lint via core.ProjectPictures).

	for _, b := range d.blocks {
		if b.code > blkPicture && b.code != blkInvalid {
			warnings = core.Warn(warnings, core.WarnUnknownBlock,
				fmt.Sprintf("metadata block type %d preserved verbatim", b.code))
		}
	}

	track := streamInfo
	track.Bitrate = core.AverageBitrate(d.audioEnd-d.audioStart, track.Duration.Seconds())
	media.Properties = core.Properties{Container: "FLAC", Tracks: []core.AudioTrack{track}}

	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// extractCommentPictures splits METADATA_BLOCK_PICTURE comments out. Malformed
// picture comments stay verbatim and are warned (same as Ogg via vorbis). Fast path
// returns the input unchanged when none are present.
func extractCommentPictures(comments []comment, limit int64) (kept []comment, pics []core.Picture, ws []core.Warning) {
	has := false
	for _, cm := range comments {
		if vorbis.IsPictureComment(cm.name) {
			has = true
			break
		}
	}
	if !has {
		return comments, nil, nil
	}
	kept = make([]comment, 0, len(comments))
	for _, cm := range comments {
		if !vorbis.IsPictureComment(cm.name) {
			kept = append(kept, cm)
			continue
		}
		// Shared with Ogg so decode and invalid-base64 wording stay aligned.
		pic, err := vorbis.DecodePictureComment(cm.value, limit)
		if err != nil {
			ws = core.Warn(ws, core.WarnInvalidPicture, err.Error())
			kept = append(kept, cm)
			continue
		}
		pics = append(pics, pic)
	}
	return kept, pics, ws
}

// flacLegacyFamilies projects leading ID3v2 / trailing ID3v1 into family entries
// (like MP3). Marks Legacy/unselected on conflict with Vorbis. Opaque when leading
// ID3v2 has non-tag content. Re-parses leading bytes in memory.
func flacLegacyFamilies(auth tag.TagSet, leadingID3, trailingID3v1 []byte, maxElements int) (fams []core.FamilyValue, opaque bool) {
	fams = id3.LegacyV1Families(auth, trailingID3v1)
	leading, opaque := id3.LegacyV2Families(auth, leadingID3, maxElements)
	return append(fams, leading...), opaque
}

// id3v2Len is the total length of a leading ID3v2 tag from its 10-byte header, or 0.
// Delegates to id3 so sync-safe size / footer / reserved version stay shared.
func id3v2Len(hdr []byte) int64 {
	if n, ok := id3.TagSize(hdr); ok {
		return n
	}
	return 0
}
