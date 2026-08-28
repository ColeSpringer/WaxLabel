// This file covers ASF/WMA descriptor names. ASF splits its metadata across a
// fixed five-field Content Description object and an open-ended list of "WM/*"
// descriptors; both resolve here.
package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// asfNames maps an ASF descriptor name to its canonical key. The table follows what
// Windows Media Player and the ffmpeg family actually write, so a file acquired as
// WMA reads the same way it would in any other container. Matching is
// case-insensitive.
var asfNames = map[string]tag.Key{
	// Content Description object's five fixed fields.
	"title":       tag.Title,
	"author":      tag.Artist,
	"copyright":   tag.Copyright,
	"description": tag.Comment,

	// Extended Content Description descriptors.
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
	// Both name the encoder to a reader: WM/ToolName is what Windows Media writes,
	// WM/EncodingSettings is where the ffmpeg family puts its "Lavf..." stamp, and
	// ffmpeg reads both back as its single "encoder" tag.
	"wm/toolname":             tag.Encoder,
	"wm/encodingsettings":     tag.Encoder,
	"wm/beatsperminute":       tag.BPM,
	"wm/titlesortorder":       tag.TitleSort,
	"wm/artistsortorder":      tag.ArtistSort,
	"wm/albumsortorder":       tag.AlbumSort,
	"wm/albumartistsortorder": tag.AlbumArtistSort,
	"wm/composersortorder":    tag.ComposerSort,
	// WM/ContentDistributor and WM/Provider are deliberately absent: they name the
	// distributor and the metadata provider, not the label, and Windows Media writes them
	// alongside WM/Publisher. Folding all three onto LABEL made a file assert a
	// multi-valued label nothing in it claims. They fall through to custom keys instead,
	// which keeps the values without inventing the relationship.
	"wm/mediaclassprimaryid":   "", // a GUID naming the content class; not a tag value
	"wm/mediaclasssecondaryid": "",
	"wm/wmcollectiongroupid":   "",
	"wm/wmcollectionid":        "",
	"wm/wmcontentid":           "",
	"wm/uniquefileidentifier":  "",
	"wm/provider style":        "",
	"wm/encodingtime":          "",
	"wm/mcdi":                  "",
	// WM/Track is the deprecated zero-based track number. Windows Media writes it
	// alongside the one-based WM/TrackNumber, so reading both would give TRACKNUMBER two
	// values, the first off by one.
	"wm/track": "",
	// The Content Description "Rating" field is free text with no agreed scale ("5 stars",
	// "PG-13"), unlike the numeric WM/SharedUserRating. Projecting it onto the canonical
	// rating key would put uninterpretable text into transfers and lint.
	"rating": "",

	// Per-stream and player bookkeeping the Metadata objects carry alongside real
	// tags. These describe the encode or the decoder's buffering, not the work, so
	// they are suppressed rather than surfaced as custom fields.
	"aspectratiox":              "",
	"aspectratioy":              "",
	"isvbr":                     "",
	"wmfsdkversion":             "",
	"wmfsdkneeded":              "",
	"deviceconformancetemplate": "",
	"buffer average":            "",
	"vbr peak":                  "",

	// MusicBrainz and AcoustID, as Picard writes them into ASF.
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

// CanonicalASF maps an ASF descriptor name to its canonical key. It reports
// ok=false both for a name that resolves to nothing and for one the table
// deliberately suppresses - the Windows Media bookkeeping GUIDs, which are player
// state rather than metadata about the work.
//
// A "WM/"-prefixed name the table does not list is retried without the prefix
// against the shared alias table and the canonical vocabulary, so a descriptor
// nobody has mapped by hand still surfaces under a sensible key instead of
// vanishing.
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
