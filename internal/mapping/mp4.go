package mapping

import "github.com/colespringer/waxlabel/tag"

// This file holds the MP4/iTunes metadata <-> canonical mapping shared by the
// mp4 codec. iTunes-style tags live in a "moov.udta.meta.ilst" atom list. Each
// item is a four-character atom whose payload is one or more "data" sub-atoms;
// the four-character names use 0xA9 ("©") for the classic Apple text atoms.
// A second, open-ended vocabulary lives in "----" freeform atoms keyed by a
// reverse-DNS mean (almost always "com.apple.iTunes") plus a name — that is
// where Picard stores the MusicBrainz/ReplayGain/AcoustID long tail.
//
// The four-character text table mirrors ffmpeg's mov metadata conversion so
// files written by the ffmpeg family (the realistic acquired-M4A case) read
// correctly and our output reads back in ffprobe. trkn/disk/covr/gnre/cpil are
// structured, not plain text, so they are handled in the codec rather than here.

// mp4Text maps a four-character text atom name to its canonical key.
var mp4Text = map[string]tag.Key{
	"\xa9nam": tag.Title,
	"\xa9ART": tag.Artist,
	"aART":    tag.AlbumArtist,
	"\xa9alb": tag.Album,
	"\xa9wrt": tag.Composer,
	"\xa9day": tag.RecordingDate, // iTunes's single date atom; ffmpeg maps it to "date"
	"\xa9cmt": tag.Comment,
	"\xa9gen": tag.Genre,
	"\xa9too": tag.EncodedBy,
	"cprt":    tag.Copyright,
	"\xa9grp": tag.Grouping,
	"\xa9lyr": tag.Lyrics,
	"soal":    tag.AlbumSort,
	"soaa":    tag.AlbumArtistSort,
	"soar":    tag.ArtistSort,
	"sonm":    tag.TitleSort,
	"soco":    tag.ComposerSort,
}

// mp4Freeform maps a "com.apple.iTunes" freeform name to its canonical key.
// These names are the de-facto Picard/MusicBrainz conventions. Note iTunes's
// historical naming: the *recording* MBID is stored under "MusicBrainz Track Id"
// while the *release-track* MBID is "MusicBrainz Release Track Id" — matching our
// MBRecordingID == MUSICBRAINZ_TRACKID convention.
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
}

var (
	keyMP4Text     = map[tag.Key]string{}
	keyMP4Freeform = map[tag.Key]string{}
)

func init() {
	for name, k := range mp4Text {
		keyMP4Text[k] = name
	}
	for name, k := range mp4Freeform {
		keyMP4Freeform[k] = name
	}
}

// MP4TextKey returns the canonical key for a four-character text atom name and
// whether it is one of the mapped names.
func MP4TextKey(name string) (tag.Key, bool) {
	k, ok := mp4Text[name]
	return k, ok
}

// MP4KeyText returns the four-character text atom a canonical key writes to, and
// whether one exists. Keys without a dedicated atom are stored as freeform.
func MP4KeyText(key tag.Key) (string, bool) {
	name, ok := keyMP4Text[key]
	return name, ok
}

// MP4FreeformKey returns the canonical key for a "com.apple.iTunes" freeform
// name and whether it is one of the mapped names.
func MP4FreeformKey(name string) (tag.Key, bool) {
	k, ok := mp4Freeform[name]
	return k, ok
}

// MP4KeyFreeform returns the freeform name a canonical key writes to. For keys
// not in the explicit table, the key string itself is used as the freeform name
// (under the com.apple.iTunes mean), so any canonical custom field round-trips.
func MP4KeyFreeform(key tag.Key) string {
	if name, ok := keyMP4Freeform[key]; ok {
		return name
	}
	return string(key)
}
