// This file covers APEv2 item names. Convention table for foobar2000/Mp3tag spellings.
package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// apeKeys folds common APE item names onto canonical keys. Unlisted names fall through to
// [tag.ParseKey]. Case-insensitive.
var apeKeys = map[string]tag.Key{
	"title":                   tag.Title,
	"artist":                  tag.Artist,
	"album":                   tag.Album,
	"album artist":            tag.AlbumArtist,
	"composer":                tag.Composer,
	"lyricist":                tag.Lyricist,
	"producer":                tag.Producer,
	"engineer":                tag.Engineer,
	"mixer":                   tag.Mixer,
	"arranger":                tag.Arranger,
	"writer":                  tag.Writer,
	"djmixer":                 tag.DJMixer,
	"genre":                   tag.Genre,
	"track":                   tag.TrackNumber,
	"disc":                    tag.DiscNumber,
	"year":                    tag.RecordingDate,
	"comment":                 tag.Comment,
	"lyrics":                  tag.Lyrics,
	"isrc":                    tag.ISRC,
	"catalog":                 tag.CatalogNumber,
	"label":                   tag.Label,
	"musicbrainz_albumstatus": tag.ReleaseStatus,
	"musicbrainz_albumtype":   tag.ReleaseType,
	// Matroska native spellings (tag/aliases.go edit aliases); needed so sets replace, not append.
	"lead_performer": tag.Artist,
	"date_recorded":  tag.RecordingDate,
	"date_released":  tag.ReleaseDate,
	"date_release":   tag.ReleaseDate,
	"date_original":  tag.OriginalDate,
	"original_date":  tag.OriginalDate,
	"encoded_by":     tag.EncodedBy,
	"part_number":    tag.TrackNumber,
	"total_parts":    tag.TrackTotal,
	"total_discs":    tag.DiscTotal,
	"catalog_number": tag.CatalogNumber,
	"publisher":      tag.Label,
	"remixed_by":     tag.Remixer,
	"content_group":  tag.Grouping,
}

// apeNames is write-side spelling where the conventional APE name differs from the key.
var apeNames = map[tag.Key]string{
	tag.Title:         "Title",
	tag.Artist:        "Artist",
	tag.Album:         "Album",
	tag.AlbumArtist:   "Album Artist",
	tag.Composer:      "Composer",
	tag.Lyricist:      "Lyricist",
	tag.Producer:      "Producer",
	tag.Engineer:      "Engineer",
	tag.Mixer:         "Mixer",
	tag.Arranger:      "Arranger",
	tag.Writer:        "Writer",
	tag.DJMixer:       "DJMixer",
	tag.Genre:         "Genre",
	tag.TrackNumber:   "Track",
	tag.DiscNumber:    "Disc",
	tag.RecordingDate: "Year",
	tag.Comment:       "Comment",
	tag.Lyrics:        "Lyrics",
	tag.ISRC:          "ISRC",
	tag.CatalogNumber: "Catalog",
	tag.Label:         "Label",
}

// CanonicalAPE maps a native APE item name to its canonical key: apeKeys, then [tag.AliasKey],
// then [tag.ParseKey].
func CanonicalAPE(name string) (tag.Key, bool) {
	if k, ok := apeKeys[strings.ToLower(strings.TrimSpace(name))]; ok {
		return k, true
	}
	if k, ok := tag.AliasKey(normalizeKey(name)); ok {
		return k, true
	}
	k, err := tag.ParseKey(name)
	if err != nil {
		return "", false
	}
	return k, true
}

// APEName returns the APE item name to write for a canonical key.
func APEName(key tag.Key) string {
	if name, ok := apeNames[key]; ok {
		return name
	}
	return string(key)
}
