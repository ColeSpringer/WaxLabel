// ASF/WMA descriptor names. Content Description five fields plus WM/* descriptors.
package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// asfNames maps an ASF descriptor name to its canonical key. Case-insensitive.
var asfNames = map[string]tag.Key{
	// Content Description object.
	"title":       tag.Title,
	"author":      tag.Artist,
	"copyright":   tag.Copyright,
	"description": tag.Comment,

	// Extended Content Description.
	"wm/albumtitle":              tag.Album,
	"wm/albumartist":             tag.AlbumArtist,
	"wm/composer":                tag.Composer,
	"wm/writer":                  tag.Lyricist,
	"wm/conductor":               tag.Conductor,
	"wm/producer":                tag.Producer,
	"wm/engineer":                tag.Engineer,
	"wm/mixer":                   tag.Mixer,
	"wm/modifiedby":              tag.Remixer,
	"wm/genre":                   tag.Genre,
	"wm/year":                    tag.RecordingDate,
	"wm/originalreleaseyear":     tag.OriginalDate,
	"wm/originalreleasetime":     tag.OriginalDate,
	"wm/tracknumber":             tag.TrackNumber,
	"wm/partofset":               tag.DiscNumber,
	"wm/setsubtitle":             tag.DiscSubtitle,
	"wm/publisher":               tag.Label,
	"wm/isrc":                    tag.ISRC,
	"wm/barcode":                 tag.Barcode,
	"wm/catalogno":               tag.CatalogNumber,
	"wm/media":                   tag.Media,
	"wm/lyrics":                  tag.Lyrics,
	"wm/contentgroupdescription": tag.Grouping,
	"wm/encodedby":               tag.EncodedBy,
	// WM/ToolName and WM/EncodingSettings both map to ENCODER (ffmpeg reads both as encoder).
	"wm/toolname":             tag.Encoder,
	"wm/encodingsettings":     tag.Encoder,
	"wm/beatsperminute":       tag.BPM,
	"wm/titlesortorder":       tag.TitleSort,
	"wm/artistsortorder":      tag.ArtistSort,
	"wm/albumsortorder":       tag.AlbumSort,
	"wm/albumartistsortorder": tag.AlbumArtistSort,
	"wm/composersortorder":    tag.ComposerSort,
	// WM/ContentDistributor and WM/Provider are not LABEL (distributor vs publisher).
	"wm/mediaclassprimaryid":   "", // GUID, not a tag value
	"wm/mediaclasssecondaryid": "",
	"wm/wmcollectiongroupid":   "",
	"wm/wmcollectionid":        "",
	"wm/wmcontentid":           "",
	"wm/uniquefileidentifier":  "",
	"wm/provider style":        "",
	"wm/encodingtime":          "",
	"wm/mcdi":                  "",
	// WM/Track is deprecated zero-based; WM/TrackNumber is one-based. Reading both duplicates TRACKNUMBER.
	"wm/track": "",
	// Content Description Rating is free text, not numeric WM/SharedUserRating.
	"rating": "",

	// Encode/player bookkeeping, not work metadata.
	"aspectratiox":              "",
	"aspectratioy":              "",
	"isvbr":                     "",
	"wmfsdkversion":             "",
	"wmfsdkneeded":              "",
	"deviceconformancetemplate": "",
	"buffer average":            "",
	"vbr peak":                  "",

	// MusicBrainz, AcoustID, ReplayGain (Picard spellings).
	"musicbrainz/album id":              tag.MBReleaseID,
	"musicbrainz/release group id":      tag.MBReleaseGroupID,
	"musicbrainz/track id":              tag.MBRecordingID,
	"musicbrainz/release track id":      tag.MBReleaseTrackID,
	"musicbrainz/work id":               tag.MBWorkID,
	"musicbrainz/disc id":               tag.MBDiscID,
	"musicbrainz/artist id":             tag.MBArtistID,
	"musicbrainz/album artist id":       tag.MBAlbumArtistID,
	"musicbrainz/album status":          tag.ReleaseStatus,
	"musicbrainz/album type":            tag.ReleaseType,
	"musicbrainz/album release country": tag.ReleaseCountry,
	"acoustid/id":                       tag.AcoustID,
	"acoustid/fingerprint":              tag.AcoustIDFingerprint,
	"replaygain_track_gain":             tag.ReplayGainTrackGain,
	"replaygain_track_peak":             tag.ReplayGainTrackPeak,
	"replaygain_album_gain":             tag.ReplayGainAlbumGain,
	"replaygain_album_peak":             tag.ReplayGainAlbumPeak,
}

// ASFUnrepresentable reports whether a name is dropped because it cannot parse as a key,
// not deliberate suppression (empty-key table entries).
func ASFUnrepresentable(name string) bool {
	norm := strings.ToLower(strings.TrimSpace(name))
	if _, listed := asfNames[norm]; listed {
		return false
	}
	_, ok := CanonicalASF(name)
	return !ok
}

// CanonicalASF maps an ASF descriptor name to its canonical key. ok=false for unlisted names
// and deliberate suppressions. Unlisted WM/* names retry without prefix via tag.AliasKey and
// tag.ParseKey.
func CanonicalASF(name string) (tag.Key, bool) {
	norm := strings.ToLower(strings.TrimSpace(name))
	if k, ok := asfNames[norm]; ok {
		return k, k != ""
	}
	bare := norm
	if i := strings.IndexByte(bare, '/'); i >= 0 {
		bare = bare[i+1:]
	}
	if k, ok := tag.AliasKey(normalizeKey(bare)); ok {
		return k, true
	}
	k, err := tag.ParseKey(bare)
	if err != nil {
		return "", false
	}
	return k, true
}
