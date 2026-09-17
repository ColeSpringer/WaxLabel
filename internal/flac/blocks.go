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
// (shared with Ogg). FLAC keeps its own comment type and adapts here.

func toVorbis(cs []comment) []vorbis.Comment {
	out := make([]vorbis.Comment, len(cs))
	for i, c := range cs {
		out[i] = vorbis.Comment{Name: c.name, Value: c.value, Unseparated: c.unseparated}
	}
	return out
}

func fromVorbis(cs []vorbis.Comment) []comment {
	out := make([]comment, len(cs))
	for i, c := range cs {
		out[i] = comment{name: c.Name, value: c.Value, unseparated: c.Unseparated}
	}
	return out
}

// parseVorbisComment decodes a Vorbis comment block body (LE lengths, no framing bit).
func parseVorbisComment(body []byte, limit int64, maxElements int) (vendor string, comments []comment, err error) {
	vendor, cs, _, err := vorbis.ParseCommentList(body, limit, maxElements)
	return vendor, fromVorbis(cs), err
}

// renderVorbisComment encodes vendor and comments into a block body. Deterministic.
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

// projectComments builds TagSet and family/source view from Vorbis comments.
func projectComments(comments []comment) (tag.TagSet, []core.FamilyValue) {
	return vorbis.Project(toVorbis(comments))
}

// projectChapters decodes the CHAPTERxxx convention from Vorbis comments.
func projectChapters(comments []comment) []core.Chapter {
	return vorbis.ProjectChapters(toVorbis(comments))
}

// projectSyncedLyrics decodes the SYNCEDLYRICS (LRC) convention from Vorbis comments.
func projectSyncedLyrics(comments []comment) []core.SyncedLyrics {
	return vorbis.ProjectSyncedLyrics(toVorbis(comments))
}

// projectSyncedLyricsReport is projectSyncedLyrics plus a warning when LRC hits the line cap.
func projectSyncedLyricsReport(comments []comment) ([]core.SyncedLyrics, []core.Warning) {
	return vorbis.ProjectSyncedLyricsReport(toVorbis(comments))
}

// encoderNoiseWarnings flags inherited transcoder stamps (e.g. ffmpeg "encoder=Lavf...").
func encoderNoiseWarnings(vendor string, comments []comment) []core.Warning {
	return vorbis.EncoderNoise(vendor, toVorbis(comments))
}

// invalidKeyWarnings flags comments whose native name is not a valid canonical key
// (empty, or rejected by Key.Valid()). Project drops them; this surfaces them at parse.
func invalidKeyWarnings(comments []comment) []core.Warning {
	return vorbis.InvalidKeyWarnings(toVorbis(comments))
}

// diffKeys returns canonical keys that differ between base and edited.
func diffKeys(base, edited tag.TagSet) map[tag.Key]bool {
	return vorbis.DiffKeys(base, edited)
}

// rebuildComments rebuilds the Vorbis list with minimal change. Owns CHAPTERxxx and
// SYNCEDLYRICS (dropped and re-emitted only on the matching structured edit).
func rebuildComments(orig []comment, edited tag.TagSet, changed map[tag.Key]bool, chapters []core.Chapter, chaptersChanged bool, syncedLyrics []core.SyncedLyrics, syncedLyricsChanged bool) ([]comment, vorbis.RebuildInfo) {
	cs, info := vorbis.Rebuild(toVorbis(orig), edited, changed, chapters, chaptersChanged, syncedLyrics, syncedLyricsChanged)
	return fromVorbis(cs), info
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
