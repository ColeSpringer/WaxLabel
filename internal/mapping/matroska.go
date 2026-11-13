package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// Matroska SimpleTag-name <-> canonical mapping. A name reads through the shared alias table
// ([tag.AliasKey]), which holds the spec's and ffmpeg's spellings, then [tag.ParseKey];
// matroskaNames holds the spellings written. Structured trkn/disk handled in codec.

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
// COUNTRY is a nesting qualifier in the spec and no alias, so it stays a key of its own.
func MatroskaTagKey(name string) (tag.Key, bool) {
	up := normalizeKey(name)
	if up == "" || technicalName(up) {
		return "", false
	}
	if k, ok := tag.AliasKey(up); ok {
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

// MatroskaAlbumLevel reports a target level of the album tier: 50 up to the edition tier at 60.
// It is the one definition of the level whose TITLE and ARTIST a file tagged by level keeps for
// the album.
func MatroskaAlbumLevel(level int) bool { return level >= 50 && level < 60 }

// MatroskaLevelKey returns the key a SimpleTag that reads as key stands for at a target level.
// In a file tagged by level (convention) the TITLE and ARTIST at album level are the album's
// title and artist.
func MatroskaLevelKey(key tag.Key, level int, convention bool) tag.Key {
	if convention && MatroskaAlbumLevel(level) {
		switch key {
		case tag.Title:
			return tag.Album
		case tag.Artist:
			return tag.AlbumArtist
		}
	}
	return key
}

// MatroskaTagNameForLevel returns the SimpleTag name to write key under at a target level, the
// mirror of [MatroskaLevelKey]: in a file tagged by level the album and its artist are written
// at album level as TITLE and ARTIST.
func MatroskaTagNameForLevel(key tag.Key, level int, convention bool) string {
	if convention && MatroskaAlbumLevel(level) {
		switch key {
		case tag.Album:
			return "TITLE"
		case tag.AlbumArtist:
			return "ARTIST"
		}
	}
	return MatroskaTagName(key)
}
