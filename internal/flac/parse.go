package flac

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/waxerr"
)

var flacMagic = []byte("fLaC")

// Parse reads a FLAC file's metadata into a neutral Media. The native document
// (blocks, comments, pictures, and any stray ID3) is preserved as the base for
// later edits; the canonical TagSet and typed projection are derived from it.
func (Codec) Parse(ctx context.Context, src core.ReaderAtSized, opts core.ParseOptions) (*core.Media, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := src.Size()
	limit := opts.Limits.MaxAllocBytes

	d := &doc{}
	var warnings []core.Warning

	// Detect a stray leading ID3v2 tag and preserve it.
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
			// Wrap with %w so the cursor's sentinel (e.g. ErrSizeTooLarge)
			// survives for callers that branch on it.
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
	// Note: FLAC carries no declared encoded-essence size (STREAMINFO holds the
	// decoded TotalSamples, not a byte length), so a mid-stream truncation with an
	// intact STREAMINFO is undetectable without decoding the frames. Unlike WAV/AIFF/
	// MP4 there is nothing to compare the file size against, so no truncated-audio
	// warning is emitted here; a deliberate known limitation, not an oversight. A
	// per-byte bitrate floor was considered and rejected - lossless silence can
	// legitimately compress to tens of bps, which would false-flag valid audio.

	if len(d.blocks) == 0 || d.blocks[0].code != blkStreamInfo {
		return nil, fmt.Errorf("%w: STREAMINFO must be the first block", waxerr.ErrInvalidData)
	}

	// Detect a trailing ID3v1 tag and preserve it. Require it to sit entirely
	// after the metadata region: otherwise audio bytes that merely happen to
	// begin with "TAG" at size-128 would push audioEnd before audioStart,
	// yielding a negative audio length.
	if size >= 128 && size-128 >= d.audioStart {
		if tail, err := bits.ReadSlice(src, size-128, 128, limit); err == nil && string(tail[:3]) == "TAG" {
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

	media := &core.Media{
		Format:     core.FormatFLAC,
		Native:     d,
		AudioStart: d.audioStart,
		AudioEnd:   d.audioEnd,
	}

	// Decode the Vorbis comment block (first wins; warn on extras).
	vcCount := 0
	for _, b := range d.blocks {
		if b.code != blkVorbisComment {
			continue
		}
		vcCount++
		if vcCount > 1 {
			warnings = core.Warn(warnings, core.WarnMultipleVorbisComment,
				"more than one Vorbis comment block; the first is authoritative and the extras are dropped if the file is rewritten")
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
	warnings = append(warnings, encoderNoiseWarnings(d.vendor, d.comments)...)

	// Decode pictures; a malformed picture is warned and skipped (its block is
	// still preserved in the native doc).
	for _, b := range d.blocks {
		if b.code != blkPicture {
			continue
		}
		p, err := parsePictureBlock(b.body, limit)
		if err != nil {
			warnings = core.Warn(warnings, core.WarnInvalidPicture, err.Error())
			continue
		}
		media.Pictures = append(media.Pictures, p)
	}

	for _, b := range d.blocks {
		if b.code > blkPicture && b.code != blkInvalid {
			warnings = core.Warn(warnings, core.WarnUnknownBlock,
				fmt.Sprintf("metadata block type %d preserved verbatim", b.code))
		}
	}

	// Properties, including an average bitrate from the audio extent.
	track := streamInfo
	track.Bitrate = core.AverageBitrate(d.audioEnd-d.audioStart, track.Duration.Seconds())
	media.Properties = core.Properties{Container: "FLAC", Tracks: []core.AudioTrack{track}}

	media.Warnings = warnings
	media.Identity = core.Identity{Size: size}
	media.Identity.Fingerprint, media.Identity.HasFinger = core.Fingerprint(src, media, limit)
	return media, nil
}

// id3v2Len returns the total byte length of a stray leading ID3v2 tag given its
// 10-byte header, or 0 if the header is not a valid ID3v2 tag. It delegates to
// the shared id3 codec so the sync-safe size, footer, and reserved-version
// handling stay in one place.
func id3v2Len(hdr []byte) int64 {
	if n, ok := id3.TagSize(hdr); ok {
		return n
	}
	return 0
}
