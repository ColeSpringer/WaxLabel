package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// Matroska SimpleTag-name <-> canonical mapping. Most names pass through [tag.ParseKey];
// matroskaTags holds ffmpeg/spec names that differ. Structured trkn/disk handled in codec.

// matroskaTags maps TagNames that need translation. Canonical-shaped names use pass-through.
var matroskaTags = map[string]tag.Key{
	"ALBUM_ARTIST":   tag.AlbumArtist,
	"LEAD_PERFORMER": tag.Artist,
	"DATE":           tag.RecordingDate, // ffmpeg flat date
	"DATE_RECORDED":  tag.RecordingDate,
	"DATE_RELEASED":  tag.ReleaseDate,
	"DATE_RELEASE":   tag.ReleaseDate,
	"DATE_ORIGINAL":  tag.OriginalDate,
	"ORIGINAL_DATE":  tag.OriginalDate,
	"ENCODER":        tag.Encoder,
	"ENCODED_BY":     tag.EncodedBy,
	"PART_NUMBER":    tag.TrackNumber, // value may be "n/total"
	"TOTAL_PARTS":    tag.TrackTotal,
	"DISC":           tag.DiscNumber, // ffmpeg flat disc; value may be "n/total"
	"TOTAL_DISCS":    tag.DiscTotal,
	"CATALOG_NUMBER": tag.CatalogNumber,
	"PUBLISHER":      tag.Label,
	"REMIXED_BY":     tag.Remixer,
	"CONTENT_GROUP":  tag.Grouping,
	// Read-only DJMIXER separator variants; write stays identity "DJMIXER".
	"DJ_MIXER": tag.DJMixer,
	"DJ MIXER": tag.DJMixer,
	"DJ-MIXER": tag.DJMixer,
	// Read-only; no tag.AliasKey here. COUNTRY is a nesting qualifier, not RELEASECOUNTRY.
	"MUSICBRAINZ_ALBUMSTATUS": tag.ReleaseStatus,
	"MUSICBRAINZ_ALBUMTYPE":   tag.ReleaseType,
}

// technicalTags are structural/statistics names, not projected (like RIFF ISFT handling).
var technicalTags = map[string]bool{
	"DURATION":                      true,
	"BPS":                           true,
	"NUMBER_OF_FRAMES":              true,
	"NUMBER_OF_BYTES":               true,
	"NUMBER_OF_BYTES_UNCOMPRESSED":  true,
	"NUMBER_OF_FRAMES_UNCOMPRESSED": true,
}

// MatroskaTechnicalName reports BPS/NUMBER_OF_*/DURATION and _STATISTICS-prefixed names.
// Read filter and write gate share this predicate.
func MatroskaTechnicalName(name string) bool {
	return technicalName(normalizeKey(name))
}

// technicalName is MatroskaTechnicalName on an already-normalized name.
func technicalName(up string) bool {
	return technicalTags[up] || strings.HasPrefix(up, "_STATISTICS")
}

// MatroskaTagKey returns the canonical key for a TagName, or ok=false for technical/invalid names.
func MatroskaTagKey(name string) (tag.Key, bool) {
	up := normalizeKey(name)
	if up == "" || technicalName(up) {
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

// matroskaNames is write-side spelling where canonical form differs from spec name.
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

// MatroskaTagName returns the SimpleTag name to write. [tag.Title] goes to Segment.Info.Title.
func MatroskaTagName(key tag.Key) string {
	if n, ok := matroskaNames[key]; ok {
		return n
	}
	return string(key)
}
