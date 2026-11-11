package flac

import (
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
)

// parseStreamInfo decodes the 34-byte STREAMINFO body. Shared with the Ogg FLAC
// mapping via internal/vorbis.
func parseStreamInfo(body []byte) (core.AudioTrack, error) {
	return vorbis.ParseStreamInfo(body)
}

// Comment list, PICTURE codec, projection, and rebuild live in internal/vorbis
// (shared with Ogg); these name them for the FLAC code.

// parseVorbisComment decodes a Vorbis comment block body (LE lengths, no framing bit).
func parseVorbisComment(body []byte, limit int64, maxElements int) (vendor string, comments []comment, err error) {
	vendor, comments, _, err = vorbis.ParseCommentList(body, limit, maxElements)
	return vendor, comments, err
}

// renderVorbisComment encodes vendor and comments into a block body. Deterministic.
func renderVorbisComment(vendor string, comments []comment) []byte {
	return vorbis.RenderCommentList(vendor, comments)
}

// parsePictureBlock decodes a PICTURE block body.
func parsePictureBlock(body []byte, limit int64) (core.Picture, error) {
	return vorbis.ParsePicture(body, limit)
}

// renderPicture encodes a Picture into a PICTURE block body.
func renderPicture(p core.Picture) []byte {
	return vorbis.RenderPicture(p)
}

// projectComments builds TagSet and family/source view from Vorbis comments.
func projectComments(comments []comment) (tag.TagSet, []core.FamilyValue) {
	return vorbis.Project(comments)
}

// diffKeys returns canonical keys that differ between base and edited.
func diffKeys(base, edited tag.TagSet) map[tag.Key]bool {
	return vorbis.DiffKeys(base, edited)
}

// renderBlock encodes a metadata block: 1-byte header (last flag bit 7, type bits 0-6),
// 24-bit BE length, then body.
func renderBlock(code byte, last bool, body []byte) []byte {
	hdr := make([]byte, 4)
	hdr[0] = code & 0x7F
	if last {
		hdr[0] |= 0x80
	}
	n := len(body)
	hdr[1] = byte(n >> 16)
	hdr[2] = byte(n >> 8)
	hdr[3] = byte(n)
	return append(hdr, body...)
}
