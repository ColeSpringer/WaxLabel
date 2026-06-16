package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// This file holds the ID3v2 <-> canonical mapping tables shared by the id3
// codec. Only the simple 1:1 text frames and the TXXX user-frame descriptions
// live here as data; the version- and semantics-dependent frames (genre TCON,
// the TRCK/TPOS number+total pairs, the date frames, UFID, COMM/USLT) are
// decoded by the id3 package itself. Frame identifiers are the modern
// v2.3/v2.4 four-character form (the codec upgrades v2.2 on read).

// id3TextFrames maps a text frame identifier to its canonical key, for frames
// that carry exactly one canonical field as their text. Frames absent here pass
// through under their own identifier as a canonical custom key.
var id3TextFrames = map[string]tag.Key{
	"TIT2": tag.Title,
	"TPE1": tag.Artist,
	"TALB": tag.Album,
	"TPE2": tag.AlbumArtist,
	"TCOM": tag.Composer,
	"TPE3": tag.Conductor,
	"TPE4": tag.Remixer,
	"TCOP": tag.Copyright,
	"TPUB": tag.Label,
	"TMED": tag.Media,
	"TIT1": tag.Grouping,
	"TSST": tag.DiscSubtitle,
	"TSRC": tag.ISRC,
	"TENC": tag.EncodedBy,
	"TSOT": tag.TitleSort,
	"TSOP": tag.ArtistSort,
	"TSOA": tag.AlbumSort,
	"TSO2": tag.AlbumArtistSort,
	"TSOC": tag.ComposerSort,
	"TCMP": tag.Compilation,
}

// id3KeyFrames is the inverse of id3TextFrames, built at init.
var id3KeyFrames = map[tag.Key]string{}

// txxxAliases maps an uppercased TXXX description to a canonical key, folding the
// Picard/MusicBrainz long-tail spellings (mixed case, spaces) onto the canonical
// vocabulary. Descriptions not listed map to the uppercased description as a
// custom key, so nothing is lost.
var txxxAliases = map[string]tag.Key{
	"MUSICBRAINZ ALBUM ID":         tag.MBReleaseID,
	"MUSICBRAINZ ARTIST ID":        tag.MBArtistID,
	"MUSICBRAINZ ALBUM ARTIST ID":  tag.MBAlbumArtistID,
	"MUSICBRAINZ RELEASE GROUP ID": tag.MBReleaseGroupID,
	"MUSICBRAINZ RELEASE TRACK ID": tag.MBReleaseTrackID,
	"MUSICBRAINZ WORK ID":          tag.MBWorkID,
	"MUSICBRAINZ DISC ID":          tag.MBDiscID,
	"ACOUSTID ID":                  tag.AcoustID,
	"ACOUSTID FINGERPRINT":         tag.AcoustIDFingerprint,
	"BARCODE":                      tag.Barcode,
	"CATALOGNUMBER":                tag.CatalogNumber,
	"REPLAYGAIN_TRACK_GAIN":        tag.ReplayGainTrackGain,
	"REPLAYGAIN_TRACK_PEAK":        tag.ReplayGainTrackPeak,
	"REPLAYGAIN_ALBUM_GAIN":        tag.ReplayGainAlbumGain,
	"REPLAYGAIN_ALBUM_PEAK":        tag.ReplayGainAlbumPeak,
}

// txxxDescForKey gives the preferred TXXX description to write for a canonical
// key whose natural home is a user frame. Keys not listed write their own name
// as the description.
var txxxDescForKey = map[tag.Key]string{
	tag.MBReleaseID:         "MusicBrainz Album Id",
	tag.MBArtistID:          "MusicBrainz Artist Id",
	tag.MBAlbumArtistID:     "MusicBrainz Album Artist Id",
	tag.MBReleaseGroupID:    "MusicBrainz Release Group Id",
	tag.MBReleaseTrackID:    "MusicBrainz Release Track Id",
	tag.MBWorkID:            "MusicBrainz Work Id",
	tag.MBDiscID:            "MusicBrainz Disc Id",
	tag.AcoustID:            "Acoustid Id",
	tag.AcoustIDFingerprint: "Acoustid Fingerprint",
}

func init() {
	for id, k := range id3TextFrames {
		id3KeyFrames[k] = id
	}
}

// ID3FrameKey returns the canonical key for a simple text frame and whether it
// is known here. Special frames (TCON, TRCK, TPOS, dates, TXXX, UFID, COMM,
// USLT, APIC) are not listed; the id3 package decodes those directly.
func ID3FrameKey(id string) (tag.Key, bool) {
	k, ok := id3TextFrames[id]
	return k, ok
}

// ID3KeyFrame returns the simple text frame for a canonical key, if one exists.
func ID3KeyFrame(key tag.Key) (string, bool) {
	id, ok := id3KeyFrames[key]
	return id, ok
}

// ID3TXXXKey maps a TXXX description to its canonical key. A known alias folds
// onto the vocabulary; otherwise the uppercased description becomes a custom
// key. ok is false only when the description cannot form a valid key.
func ID3TXXXKey(desc string) (tag.Key, bool) {
	up := strings.ToUpper(strings.TrimSpace(desc))
	if k, ok := txxxAliases[up]; ok {
		return k, true
	}
	k, err := tag.ParseKey(up)
	if err != nil {
		return "", false
	}
	return k, true
}

// ID3TXXXDesc returns the TXXX description to write for a canonical key: a
// preferred Picard spelling when one exists, else the key's own name.
func ID3TXXXDesc(key tag.Key) string {
	if d, ok := txxxDescForKey[key]; ok {
		return d
	}
	return string(key)
}
