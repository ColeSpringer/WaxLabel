package flac

import (
	"context"
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
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

	for {
		if err := ctx.Err(); err != nil {
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
		vendor, comments, err := parseVorbisComment(b.body, limit)
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
	if audioBytes := d.audioEnd - d.audioStart; audioBytes > 0 && track.Duration > 0 {
		track.Bitrate = int(float64(audioBytes) * 8 / track.Duration.Seconds())
	}
	media.Properties = core.Properties{Container: "FLAC", Tracks: []core.AudioTrack{track}}

	media.Warnings = warnings
	media.Identity = core.Identity{
		Size:        size,
		Fingerprint: bits.SHA256(fingerprintRegion(src, d, limit)),
		HasFinger:   true,
	}
	return media, nil
}

// projectComments builds the canonical TagSet and the family/source view from
// decoded Vorbis comments, preserving order. A canonical key fed by two or more
// distinct native field names with disagreeing values (e.g. DATE=2020 and
// YEAR=2019, both mapping to RecordingDate) is a genuine conflict and is marked
// unselected so it surfaces in the family view and Lint. Repeats of the same
// native name (ARTIST=A, ARTIST=B) are an ordinary multi-value, not a conflict.
func projectComments(comments []comment) (tag.TagSet, []core.FamilyValue) {
	ts := tag.NewTagSet()
	famIndex := map[tag.Key]int{}
	names := map[tag.Key]map[string]bool{} // distinct native names per key
	var fams []core.FamilyValue
	for _, cm := range comments {
		key := mapping.CanonicalVorbis(cm.name)
		ts.Add(key, cm.value)
		if i, ok := famIndex[key]; ok {
			fams[i].Values = append(fams[i].Values, cm.value)
		} else {
			famIndex[key] = len(fams)
			names[key] = map[string]bool{}
			fams = append(fams, core.FamilyValue{
				Key: key, Family: core.FamilyVorbis, Scope: core.ScopeTrack,
				Values: []string{cm.value}, Selected: true,
			})
		}
		names[key][strings.ToUpper(cm.name)] = true
	}
	for key, i := range famIndex {
		if len(names[key]) > 1 && distinctValues(fams[i].Values) > 1 {
			fams[i].Selected = false
		}
	}
	return ts, fams
}

// distinctValues counts the distinct case- and space-insensitive values.
func distinctValues(vals []string) int {
	seen := map[string]bool{}
	for _, v := range vals {
		seen[strings.ToLower(strings.TrimSpace(v))] = true
	}
	return len(seen)
}

// encoderNoiseWarnings flags inherited transcoder stamps (e.g. ffmpeg's
// "encoder=Lavf..."), the typical signature of an acquired file.
func encoderNoiseWarnings(vendor string, comments []comment) []core.Warning {
	var ws []core.Warning
	noisy := func(s string) bool {
		s = strings.ToLower(s)
		return strings.Contains(s, "lavf") || strings.Contains(s, "libavformat")
	}
	if noisy(vendor) {
		ws = core.Warn(ws, core.WarnInheritedEncoder, "vendor string is a transcoder stamp: "+vendor)
	}
	for _, cm := range comments {
		if strings.EqualFold(cm.name, "ENCODER") && noisy(cm.value) {
			ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder comment: "+cm.value)
		}
	}
	return ws
}

// fingerprintRegion returns the header-plus-metadata bytes used in the source
// identity fingerprint. On read error it returns nil (fingerprint of nothing),
// which simply weakens the check rather than failing the parse.
func fingerprintRegion(src core.ReaderAtSized, d *doc, limit int64) []byte {
	region, err := bits.ReadSlice(src, 0, d.audioStart, limit)
	if err != nil {
		return nil
	}
	return region
}

// id3v2Len returns the total byte length of an ID3v2 tag given its 10-byte
// header, or 0 if the header is not "ID3". The size field is sync-safe (each
// byte contributes only 7 bits); a present footer adds another 10 bytes.
func id3v2Len(hdr []byte) int64 {
	if len(hdr) < 10 || string(hdr[:3]) != "ID3" {
		return 0
	}
	size := int64(hdr[6]&0x7F)<<21 | int64(hdr[7]&0x7F)<<14 | int64(hdr[8]&0x7F)<<7 | int64(hdr[9]&0x7F)
	total := 10 + size
	if hdr[5]&0x10 != 0 { // footer present
		total += 10
	}
	return total
}
