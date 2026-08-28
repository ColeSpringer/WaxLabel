// This file covers APEv2 item names. APE keys are free-form UTF-8 with no
// registry, so the mapping is a convention table: the spellings foobar2000,
// Mp3tag, and the Monkey's Audio tools actually write.
package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// apeKeys folds common APE item names onto canonical keys. Names not listed fall
// through to [tag.ParseKey], so an unrecognized item still becomes a custom field.
// Matching is case-insensitive.
var apeKeys = map[string]tag.Key{
	"title":        tag.Title,
	"artist":       tag.Artist,
	"album":        tag.Album,
	"album artist": tag.AlbumArtist,
	"composer":     tag.Composer,
	"lyricist":     tag.Lyricist,
	"producer":     tag.Producer,
	"engineer":     tag.Engineer,
	"mixer":        tag.Mixer,
	"arranger":     tag.Arranger,
	"writer":       tag.Writer,
	"djmixer":      tag.DJMixer,
	"genre":        tag.Genre,
	"track":        tag.TrackNumber,
	"disc":         tag.DiscNumber,
	"year":         tag.RecordingDate,
	"comment":      tag.Comment,
	"lyrics":       tag.Lyrics,
	"isrc":         tag.ISRC,
	"catalog":      tag.CatalogNumber,
	"label":        tag.Label,
	// APE's own spellings for release status and type. The shared alias table carries
	// them too, so these rows are belt and braces: they pin the spelling to this mapping
	// rather than leaving it to a table another format owns.
	"musicbrainz_albumstatus": tag.ReleaseStatus,
	"musicbrainz_albumtype":   tag.ReleaseType,
	// The Matroska native tag spellings are edit aliases on every format
	// (tag/aliases.go), so APE items using them must project onto the same
	// canonical keys here or a set under the spelling would append beside the
	// item instead of replacing it.
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

// apeNames is the write-side spelling for the canonical keys whose conventional
// APE item name is not the key itself. APE display names are mixed case by
// convention ("Album Artist", not "ALBUMARTIST"), and third-party tools match on
// them case-insensitively but show what is stored, so writing the conventional
// form is what makes the file look right in foobar2000 and Mp3tag.
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

// CanonicalAPE maps a native APE item name (any case, ignoring surrounding
// whitespace) to its canonical key: the APE convention table first, then the shared
// read-side alias table every other codec resolves through (so an APE "DATE" reads
// as RECORDINGDATE exactly as a Vorbis one does), then the key itself. It reports
// ok=false for a name that is none of those, which the caller drops from the
// canonical view while preserving the item's bytes.
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

// APEName maps a canonical key to the item name used when writing it. Keys with no
// convention write their own name verbatim.
func APEName(key tag.Key) string {
	if name, ok := apeNames[key]; ok {
		return name
	}
	return string(key)
}
