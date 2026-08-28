package flac

import (
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
)

// parseStreamInfo decodes the 34-byte STREAMINFO body into an AudioTrack. The
// decode is shared with the Ogg FLAC mapping via internal/vorbis, whose
// identification packet ends with the same block.
func parseStreamInfo(body []byte) (core.AudioTrack, error) {
	return vorbis.ParseStreamInfo(body)
}

// The Vorbis comment list and PICTURE block byte codecs, the canonical
// projection, and the minimal-change rebuild are shared with Ogg via
// internal/vorbis. FLAC keeps its own comment type (so its native document and
// tests stay stable) and adapts at these thin boundaries.

func toVorbis(cs []comment) []vorbis.Comment {
	out := make([]vorbis.Comment, len(cs))
	for i, c := range cs {
		out[i] = vorbis.Comment{Name: c.name, Value: c.value}
	}
	return out
}

func fromVorbis(cs []vorbis.Comment) []comment {
	out := make([]comment, len(cs))
	for i, c := range cs {
		out[i] = comment{name: c.Name, value: c.Value}
	}
	return out
}

// parseVorbisComment decodes a Vorbis comment block body (little-endian
// lengths, no FLAC framing bit) into a vendor string and ordered comments.
func parseVorbisComment(body []byte, limit int64, maxElements int) (vendor string, comments []comment, err error) {
	vendor, cs, _, err := vorbis.ParseCommentList(body, limit, maxElements)
	return vendor, fromVorbis(cs), err
}

// renderVorbisComment encodes a vendor string and comments back into a block
// body. Deterministic: same inputs produce identical bytes.
func renderVorbisComment(vendor string, comments []comment) []byte {
	return vorbis.RenderCommentList(vendor, toVorbis(comments))
}

// parsePictureBlock decodes a PICTURE block body.
func parsePictureBlock(body []byte, limit int64) (core.Picture, error) {
	return vorbis.ParsePicture(body, limit)
}

// renderPicture encodes a Picture into a PICTURE block body.
func renderPicture(p core.Picture) []byte {
	return vorbis.RenderPicture(p)
}

// projectComments builds the canonical TagSet and the family/source view from
// decoded Vorbis comments, preserving order and surfacing conflicts.
func projectComments(comments []comment) (tag.TagSet, []core.FamilyValue) {
	return vorbis.Project(toVorbis(comments))
}

// projectChapters decodes the CHAPTERxxx chapter convention from the Vorbis comments.
func projectChapters(comments []comment) []core.Chapter {
	return vorbis.ProjectChapters(toVorbis(comments))
}

// projectSyncedLyrics decodes the SYNCEDLYRICS (LRC) convention from the Vorbis comments.
func projectSyncedLyrics(comments []comment) []core.SyncedLyrics {
	return vorbis.ProjectSyncedLyrics(toVorbis(comments))
}

// projectSyncedLyricsReport is projectSyncedLyrics plus the read warning for an LRC document
// truncated at the modeled line cap, used by the parse path so that truncation is surfaced
// rather than silent.
func projectSyncedLyricsReport(comments []comment) ([]core.SyncedLyrics, []core.Warning) {
	return vorbis.ProjectSyncedLyricsReport(toVorbis(comments))
}

// encoderNoiseWarnings flags inherited transcoder stamps (e.g. ffmpeg's
// "encoder=Lavf..."), the typical signature of an acquired file.
func encoderNoiseWarnings(vendor string, comments []comment) []core.Warning {
	return vorbis.EncoderNoise(vendor, toVorbis(comments))
}

// invalidKeyWarnings flags Vorbis comments whose native name has no valid canonical tag key
// (an empty name, or one the writer's Key.Valid() gate rejects). Project drops such a key
// from the canonical model, so this surfaces it at parse for dump and lint to report.
func invalidKeyWarnings(comments []comment) []core.Warning {
	return vorbis.InvalidKeyWarnings(toVorbis(comments))
}

// diffKeys returns the canonical keys whose values differ between base and
// edited (added, removed, or modified).
func diffKeys(base, edited tag.TagSet) map[tag.Key]bool {
	return vorbis.DiffKeys(base, edited)
}

// rebuildComments produces the new Vorbis comment list with minimal change, owning the
// CHAPTERxxx chapter and SYNCEDLYRICS comments (dropped and re-emitted only on the matching
// structured edit).
func rebuildComments(orig []comment, edited tag.TagSet, changed map[tag.Key]bool, chapters []core.Chapter, chaptersChanged bool, syncedLyrics []core.SyncedLyrics, syncedLyricsChanged bool) ([]comment, vorbis.RebuildInfo) {
	cs, info := vorbis.Rebuild(toVorbis(orig), edited, changed, chapters, chaptersChanged, syncedLyrics, syncedLyricsChanged)
	return fromVorbis(cs), info
}

// renderBlock encodes a full metadata block: a 1-byte header (last-block flag
// in bit 7, type code in bits 0-6), a 24-bit big-endian length, then the body.
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
