package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// This file holds the Matroska SimpleTag-name <-> canonical mapping used by the
// matroska codec for both read and write. Matroska tags live in
// Segment.Tags.Tag.SimpleTag elements: each SimpleTag has a TagName (an
// uppercase, underscore-separated UTF-8 string) and a TagString value, grouped
// under a Targets element that scopes them to the whole segment, a track, an
// edition, or a chapter.
//
// Matroska tag names are uppercase with underscores - the same shape as a
// canonical [tag.Key] - so most names map to themselves by passing through
// [tag.ParseKey] (ARTIST, ALBUM, COMPOSER, GENRE, COMMENT, COPYRIGHT, ISRC,
// LABEL, BARCODE, and the MUSICBRAINZ_* / REPLAYGAIN_* long tail all already
// equal their canonical keys). The explicit table below holds only the names
// that differ from the canonical key or that ffmpeg writes in its own flat
// convention (the realistic acquired case: ffmpeg's matroska muxer dumps generic
// metadata as flat SimpleTags rather than following Matroska's hierarchical
// target semantics).
//
// The names are reimplemented from the Matroska tagging spec (matroska.org /
// RFC 9559) and ffmpeg's matroskaenc metadata conventions; nothing is copied.

// matroskaTags maps a Matroska TagName that needs translation to its canonical
// key. Names that already equal a canonical key are handled by the pass-through
// in [MatroskaTagKey]; they are intentionally absent here.
var matroskaTags = map[string]tag.Key{
	"ALBUM_ARTIST":   tag.AlbumArtist,
	"LEAD_PERFORMER": tag.Artist,
	"DATE":           tag.RecordingDate, // ffmpeg's flat "date"
	"DATE_RECORDED":  tag.RecordingDate, // Matroska spec
	"DATE_RELEASED":  tag.ReleaseDate,
	"DATE_RELEASE":   tag.ReleaseDate,
	"DATE_ORIGINAL":  tag.OriginalDate,
	"ORIGINAL_DATE":  tag.OriginalDate,
	"ENCODER":        tag.Encoder,     // the Lavf... transcoder stamp lands here
	"ENCODED_BY":     tag.EncodedBy,   // the encoding person
	"PART_NUMBER":    tag.TrackNumber, // value may be "n/total"
	"TOTAL_PARTS":    tag.TrackTotal,
	"DISC":           tag.DiscNumber, // ffmpeg's flat "disc"; value may be "n/total"
	"TOTAL_DISCS":    tag.DiscTotal,
	"CATALOG_NUMBER": tag.CatalogNumber,
	"PUBLISHER":      tag.Label,
	"REMIXED_BY":     tag.Remixer,
	"CONTENT_GROUP":  tag.Grouping,
}

// technicalTags are SimpleTag names that are structural/statistical rather than
// descriptive metadata. They are preserved in the native tree but not projected
// into the canonical set, the same way the WAV codec preserves the ISFT software
// stamp without surfacing it. mkvmerge writes the BPS / NUMBER_OF_* / DURATION
// statistics; "_STATISTICS_"-prefixed names are excluded by prefix.
var technicalTags = map[string]bool{
	"DURATION":                      true,
	"BPS":                           true,
	"NUMBER_OF_FRAMES":              true,
	"NUMBER_OF_BYTES":               true,
	"NUMBER_OF_BYTES_UNCOMPRESSED":  true,
	"NUMBER_OF_FRAMES_UNCOMPRESSED": true,
}

// MatroskaTagKey returns the canonical key a Matroska TagName projects to, and
// whether it projects at all. A technical/statistics name, or a name that is not
// a valid canonical key, returns ok=false (preserved in the native tree but kept
// out of the canonical set). The pass-through means an unmapped but
// canonical-shaped name (e.g. MUSICBRAINZ_ALBUMID, REPLAYGAIN_TRACK_GAIN) round-
// trips to the matching custom key without an explicit entry.
func MatroskaTagKey(name string) (tag.Key, bool) {
	up := normalizeKey(name)
	if up == "" || technicalTags[up] || strings.HasPrefix(up, "_STATISTICS") {
		return "", false
	}
	if k, ok := matroskaTags[up]; ok {
		return k, true
	}
	k, err := tag.ParseKey(up)
	if err != nil {
		return "", false
	}
	return k, true
}

// matroskaNames is the write-side inverse of [MatroskaTagKey]: a canonical key to
// the Matroska-spec SimpleTag name players expect, for the keys whose canonical
// spelling differs from the spec name (the canonical form has no underscores, the
// numbering keys use PART_NUMBER/TOTAL_PARTS, dates use DATE_*). Each target name
// reads back to the same canonical key through the table above, so the round-trip
// is exact. A key absent here writes its own canonical string, which also
// round-trips via the [tag.ParseKey] pass-through.
var matroskaNames = map[tag.Key]string{
	tag.AlbumArtist:   "ALBUM_ARTIST",
	tag.TrackNumber:   "PART_NUMBER",
	tag.TrackTotal:    "TOTAL_PARTS",
	tag.DiscNumber:    "DISC",
	tag.DiscTotal:     "TOTAL_DISCS",
	tag.RecordingDate: "DATE_RECORDED",
	tag.ReleaseDate:   "DATE_RELEASED",
	tag.OriginalDate:  "DATE_ORIGINAL",
	tag.Encoder:       "ENCODER",
	tag.EncodedBy:     "ENCODED_BY",
	tag.CatalogNumber: "CATALOG_NUMBER",
	tag.Remixer:       "REMIXED_BY",
	tag.Label:         "PUBLISHER",
	tag.Grouping:      "CONTENT_GROUP",
}

// MatroskaTagName returns the SimpleTag name to write for a canonical key. The
// caller excludes [tag.Title] (it is written to Segment.Info.Title, not a
// SimpleTag); every other key maps either through matroskaNames or to its own
// uppercase canonical string.
func MatroskaTagName(key tag.Key) string {
	if n, ok := matroskaNames[key]; ok {
		return n
	}
	return string(key)
}
