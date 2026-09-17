package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// MP4/iTunes metadata <-> canonical mapping. Text atoms in ilst; Picard long tail in
// com.apple.iTunes freeforms. trkn/disk/covr/gnre/cpil handled in codec. Text table
// follows ffmpeg mov metadata conversion.

// mp4Text maps a four-character text atom name to its canonical key.
var mp4Text = map[string]tag.Key{
	"\xa9nam": tag.Title,
	"\xa9ART": tag.Artist,
	"aART":    tag.AlbumArtist,
	"\xa9alb": tag.Album,
	"\xa9wrt": tag.Composer,
	"\xa9day": tag.RecordingDate, // iTunes date; ffmpeg maps to "date"
	"\xa9cmt": tag.Comment,
	"\xa9gen": tag.Genre,
	"\xa9too": tag.Encoder, // Lavf stamp
	"cprt":    tag.Copyright,
	"\xa9grp": tag.Grouping,
	"\xa9lyr": tag.Lyrics,
	"desc":    tag.Description,
	"ldes":    tag.LongDescription,
	"soal":    tag.AlbumSort,
	"soaa":    tag.AlbumArtistSort,
	"soar":    tag.ArtistSort,
	"sonm":    tag.TitleSort,
	"soco":    tag.ComposerSort,
	"\xa9wrk": tag.Work,
	"\xa9mvn": tag.MovementName,
	// ©enc is encoded-by person; ©too is tool. ffmpeg folds both to encoder; iTunes keeps distinct.
	"\xa9enc": tag.EncodedBy,
}

// mp4Freeform maps com.apple.iTunes freeform names to canonical keys.
// MBRecordingID uses "MusicBrainz Track Id"; MBReleaseTrackID uses "... Release Track Id".
var mp4Freeform = map[string]tag.Key{
	"MusicBrainz Track Id":         tag.MBRecordingID,
	"MusicBrainz Release Track Id": tag.MBReleaseTrackID,
	"MusicBrainz Album Id":         tag.MBReleaseID,
	"MusicBrainz Release Group Id": tag.MBReleaseGroupID,
	"MusicBrainz Artist Id":        tag.MBArtistID,
	"MusicBrainz Album Artist Id":  tag.MBAlbumArtistID,
	"MusicBrainz Work Id":          tag.MBWorkID,
	"MusicBrainz Disc Id":          tag.MBDiscID,
	"Acoustid Id":                  tag.AcoustID,
	"Acoustid Fingerprint":         tag.AcoustIDFingerprint,
	"replaygain_track_gain":        tag.ReplayGainTrackGain,
	"replaygain_track_peak":        tag.ReplayGainTrackPeak,
	"replaygain_album_gain":        tag.ReplayGainAlbumGain,
	"replaygain_album_peak":        tag.ReplayGainAlbumPeak,
	"BARCODE":                      tag.Barcode,
	"CATALOGNUMBER":                tag.CatalogNumber,
	"LABEL":                        tag.Label,
	"MEDIA":                        tag.Media,
	"ISRC":                         tag.ISRC,
	"originaldate":                 tag.OriginalDate,
	"NARRATOR":                     tag.Narrator,
	"LYRICIST":                     tag.Lyricist, // no standard MP4 lyricist atom
	// Mixed-case Picard names; lowercase misses decodeFreeform validKeyByte fallback.
	"MusicBrainz Album Release Country": tag.ReleaseCountry,
	"MusicBrainz Album Status":          tag.ReleaseStatus,
	"MusicBrainz Album Type":            tag.ReleaseType,
	// No standard MP4 role atoms; MP4 uses MIXER/DJMIXER not ID3 mix/DJ-mix.
	"PRODUCER": tag.Producer,
	"ENGINEER": tag.Engineer,
	"MIXER":    tag.Mixer,
	"ARRANGER": tag.Arranger,
	"WRITER":   tag.Writer,
	"DJMIXER":  tag.DJMixer,
}

// quickTimeKeyPrefix is stripped from mdta keys so Apple and ffmpeg bare names match.
const quickTimeKeyPrefix = "com.apple.quicktime."

// mp4Mdta maps bare mdta key names to canonical keys. Duplicate names (date/creationdate,
// encoder/software) stay as separate source labels on read.
var mp4Mdta = map[string]tag.Key{
	"title":        tag.Title,
	"artist":       tag.Artist,
	"albumartist":  tag.AlbumArtist,
	"album":        tag.Album,
	"composer":     tag.Composer,
	"comment":      tag.Comment,
	"description":  tag.Description,
	"genre":        tag.Genre,
	"encoder":      tag.Encoder,
	"software":     tag.Encoder,
	"copyright":    tag.Copyright,
	"date":         tag.RecordingDate,
	"creationdate": tag.RecordingDate,
	"publisher":    tag.Label,
	"grouping":     tag.Grouping,
	"lyrics":       tag.Lyrics,
}

// keyMP4Mdta is hand-written write spelling; mp4Mdta is many-to-one (inverting would be nondeterministic).
var keyMP4Mdta = map[tag.Key]string{
	tag.Title:         "title",
	tag.Artist:        "artist",
	tag.AlbumArtist:   "albumartist",
	tag.Album:         "album",
	tag.Composer:      "composer",
	tag.Comment:       "comment",
	tag.Description:   "description",
	tag.Genre:         "genre",
	tag.Encoder:       "encoder",
	tag.Copyright:     "copyright",
	tag.RecordingDate: "date",
	tag.Label:         "publisher",
	tag.Grouping:      "grouping",
	tag.Lyrics:        "lyrics",
}

// mp4UdtaText: udta-only text atoms not in ilst (mp4Text consulted first on read).
var mp4UdtaText = map[string]tag.Key{
	"\xa9swr": tag.Encoder, // QuickTime software / Lavf stamp on .mov
	"\xa9inf": tag.Comment,
}

var (
	keyMP4Text     = map[tag.Key]string{}
	keyMP4Freeform = map[tag.Key]string{}
	// freeformFold is read-only case fold; mp4Freeform/keyMP4Freeform keep exact write spelling.
	freeformFold = map[string]tag.Key{}
)

func init() {
	for name, k := range mp4Text {
		keyMP4Text[k] = name
	}
	for name, k := range mp4Freeform {
		keyMP4Freeform[k] = name
		freeformFold[normalizeKey(name)] = k
	}
	// Read-only DJMIXER separator variants; write stays "DJMIXER".
	for _, name := range []string{"DJ MIXER", "DJ_MIXER", "DJ-MIXER"} {
		freeformFold[normalizeKey(name)] = tag.DJMixer
	}
	// Read-only; no tag.AliasKey on this path.
	freeformFold[normalizeKey("MUSICBRAINZ_ALBUMSTATUS")] = tag.ReleaseStatus
	freeformFold[normalizeKey("MUSICBRAINZ_ALBUMTYPE")] = tag.ReleaseType
	// Matroska native spellings (tag/aliases.go); read-only so sets replace, not append.
	for name, k := range map[string]tag.Key{
		"LEAD_PERFORMER": tag.Artist,
		"DATE_RECORDED":  tag.RecordingDate,
		"DATE_RELEASED":  tag.ReleaseDate,
		"DATE_RELEASE":   tag.ReleaseDate,
		"DATE_ORIGINAL":  tag.OriginalDate,
		"ORIGINAL_DATE":  tag.OriginalDate,
		"ENCODED_BY":     tag.EncodedBy,
		"PART_NUMBER":    tag.TrackNumber,
		"TOTAL_PARTS":    tag.TrackTotal,
		"TOTAL_DISCS":    tag.DiscTotal,
		"CATALOG_NUMBER": tag.CatalogNumber,
		"PUBLISHER":      tag.Label,
		"REMIXED_BY":     tag.Remixer,
		"CONTENT_GROUP":  tag.Grouping,
	} {
		freeformFold[normalizeKey(name)] = k
	}
	// iTunes structured keys + ENCODEDBY; read-only (write uses structured or ©-text atoms).
	for _, k := range []tag.Key{
		tag.ITunesAdvisory, tag.ITunesGapless, tag.ShowMovement, tag.BPM,
		tag.Work, tag.MovementName, tag.Movement, tag.MovementTotal, tag.EncodedBy,
	} {
		freeformFold[normalizeKey(string(k))] = k
	}
}

// MP4TextKey returns the canonical key for a four-character text atom name.
func MP4TextKey(name string) (tag.Key, bool) {
	k, ok := mp4Text[name]
	return k, ok
}

// MP4KeyText returns the text atom a canonical key writes to, if any.
func MP4KeyText(key tag.Key) (string, bool) {
	name, ok := keyMP4Text[key]
	return name, ok
}

// MP4FreeformKey returns the canonical key for a freeform name. Case folds via freeformFold
// (like [ID3TXXXKey]/[MatroskaTagKey]). Separators are not normalized; DJMIXER variants are
// seeded in init.
func MP4FreeformKey(name string) (tag.Key, bool) {
	if k, ok := mp4Freeform[name]; ok {
		return k, true
	}
	k, ok := freeformFold[normalizeKey(name)]
	return k, ok
}

// MP4KeyFreeform returns the freeform name to write. Unlisted keys write string(key).
func MP4KeyFreeform(key tag.Key) string {
	if name, ok := keyMP4Freeform[key]; ok {
		return name
	}
	return string(key)
}

// MP4MdtaKey strips quickTimeKeyPrefix, then maps or falls through [tag.FoldKey].
func MP4MdtaKey(name string) (tag.Key, bool) {
	bare := strings.TrimPrefix(name, quickTimeKeyPrefix)
	if k, ok := mp4Mdta[bare]; ok {
		return k, true
	}
	return tag.FoldKey(bare)
}

// MP4KeyMdta returns the bare mdta key to write. Unlisted keys write string(key).
func MP4KeyMdta(key tag.Key) string {
	if name, ok := keyMP4Mdta[key]; ok {
		return name
	}
	return string(key)
}

// MP4UdtaTextKey is MP4TextKey plus mp4UdtaText (udta-only atoms).
func MP4UdtaTextKey(name string) (tag.Key, bool) {
	if k, ok := mp4Text[name]; ok {
		return k, true
	}
	k, ok := mp4UdtaText[name]
	return k, ok
}
