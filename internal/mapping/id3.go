package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// ID3v2 <-> canonical mapping. Simple text frames and TXXX descriptions here; TCON, TRCK/TPOS,
// dates, UFID, COMM/USLT handled in internal/id3. Frame ids are v2.3/v2.4 four-char form.

// id3TextFrames maps text frame ids to canonical keys. Unlisted frames pass through as custom keys.
var id3TextFrames = map[string]tag.Key{
	"TIT2": tag.Title,
	"TPE1": tag.Artist,
	"TALB": tag.Album,
	"TPE2": tag.AlbumArtist,
	"TCOM": tag.Composer,
	// TEXT is lyricist (v2.2 TXT upgraded). TEXT + TXXX:LYRICIST both project; LYRICIST is multivalued.
	"TEXT": tag.Lyricist,
	"TPE3": tag.Conductor,
	"TPE4": tag.Remixer,
	"TCOP": tag.Copyright,
	"TPUB": tag.Label,
	"TMED": tag.Media,
	"TIT1": tag.Grouping,
	"TSST": tag.DiscSubtitle,
	"TSRC": tag.ISRC,
	"TENC": tag.EncodedBy,
	"TSSE": tag.Encoder, // Lavf stamp
	"TSOT": tag.TitleSort,
	"TSOP": tag.ArtistSort,
	"TSOA": tag.AlbumSort,
	"TSO2": tag.AlbumArtistSort,
	"TSOC": tag.ComposerSort,
	"TCMP": tag.Compilation,
	"TBPM": tag.BPM,
	// MVNM is not T-prefixed; id3 read handles it separately.
	"MVNM": tag.MovementName,
}

// id3KeyFrames is the inverse of id3TextFrames, built at init.
var id3KeyFrames = map[tag.Key]string{}

// txxxAliases maps uppercased TXXX descriptions to canonical keys.
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
	// Picard mixed-case names; bare canonical spellings use tag.ParseKey fallthrough.
	"MUSICBRAINZ ALBUM RELEASE COUNTRY": tag.ReleaseCountry,
	"MUSICBRAINZ ALBUM STATUS":          tag.ReleaseStatus,
	"MUSICBRAINZ ALBUM TYPE":            tag.ReleaseType,
	// No tag.AliasKey on this path.
	"MUSICBRAINZ_ALBUMSTATUS": tag.ReleaseStatus,
	"MUSICBRAINZ_ALBUMTYPE":   tag.ReleaseType,
	// ffmpeg TXXX:TCMP; write still uses TCMP frame. Both TCMP frame + TXXX:TCMP -> lint single-valued-multi.
	"TCMP": tag.Compilation,
	// Read-only DJMIXER variants; write uses TIPL/IPLS.
	"DJ MIXER": tag.DJMixer,
	"DJ_MIXER": tag.DJMixer,
	"DJ-MIXER": tag.DJMixer,
	// Matroska native spellings (tag/aliases.go); read-only.
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
}

// txxxDescForKey is preferred TXXX write description. Unlisted keys write string(key).
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
	tag.Writer:              "Writer",
	tag.ReleaseCountry:      "MusicBrainz Album Release Country",
	tag.ReleaseStatus:       "MusicBrainz Album Status",
	tag.ReleaseType:         "MusicBrainz Album Type",
}

// id3InvolvedRoles maps credit keys to Picard TIPL/IPLS strings. MIXER->"mix", DJMIXER->"DJ-mix".
// WRITER is TXXX:Writer, not involved-people.
var id3InvolvedRoles = map[tag.Key]string{
	tag.Producer: "producer",
	tag.Engineer: "engineer",
	tag.Mixer:    "mix",
	tag.Arranger: "arranger",
	tag.DJMixer:  "DJ-mix",
}

// id3InvolvedOrder is deterministic TIPL/IPLS emit order.
var id3InvolvedOrder = []tag.Key{tag.Producer, tag.Engineer, tag.Mixer, tag.Arranger, tag.DJMixer}

// id3InvolvedReadAliases: read-only spellings from Kid3/Mp3tag/foobar; write stays Picard form.
var id3InvolvedReadAliases = map[string]tag.Key{
	"mixer":    tag.Mixer,
	"djmixer":  tag.DJMixer,
	"dj-mixer": tag.DJMixer,
	"dj mix":   tag.DJMixer,
	"dj mixer": tag.DJMixer,
	"dj_mixer": tag.DJMixer,
}

// id3InvolvedByFunc maps case-folded involvement function to canonical key.
var id3InvolvedByFunc = map[string]tag.Key{}

func init() {
	for id, k := range id3TextFrames {
		id3KeyFrames[k] = id
	}
	for k, fn := range id3InvolvedRoles {
		id3InvolvedByFunc[strings.ToLower(fn)] = k
	}
	for fn, k := range id3InvolvedReadAliases {
		id3InvolvedByFunc[strings.ToLower(fn)] = k
	}
}

// ID3FrameKey returns the canonical key for a simple text frame.
func ID3FrameKey(id string) (tag.Key, bool) {
	k, ok := id3TextFrames[id]
	return k, ok
}

// ID3KeyFrame returns the text frame for a canonical key, if any.
func ID3KeyFrame(key tag.Key) (string, bool) {
	id, ok := id3KeyFrames[key]
	return id, ok
}

// ID3TXXXKey maps a TXXX description to its canonical key via txxxAliases or tag.ParseKey.
func ID3TXXXKey(desc string) (tag.Key, bool) {
	up := normalizeKey(desc)
	if k, ok := txxxAliases[up]; ok {
		return k, true
	}
	k, err := tag.ParseKey(up)
	if err != nil {
		return "", false
	}
	return k, true
}

// ID3TXXXDesc returns the TXXX description to write for a canonical key.
func ID3TXXXDesc(key tag.Key) string {
	if d, ok := txxxDescForKey[key]; ok {
		return d
	}
	return string(key)
}

// ID3InvolvedFunction returns the TIPL/IPLS involvement string to write, if key is a credit role.
func ID3InvolvedFunction(key tag.Key) (string, bool) {
	fn, ok := id3InvolvedRoles[key]
	return fn, ok
}

// ID3InvolvedRoleKey maps an involvement function to its canonical key (Picard + read aliases).
func ID3InvolvedRoleKey(fn string) (tag.Key, bool) {
	k, ok := id3InvolvedByFunc[strings.ToLower(strings.TrimSpace(fn))]
	return k, ok
}

// technicalCommentDescs are machine COMM descriptions, not projected as COMMENT.
var technicalCommentDescs = map[string]bool{
	"ITUNNORM": true,
	"ITUNSMPB": true,
	"ITUNPGAP": true,
	"ITUNMOVI": true,
	"ITUNEXTC": true,
}

// technicalCommentPrefixes cover families with varying descriptions.
var technicalCommentPrefixes = []string{"ITUNES_CDDB", "REPLAYGAIN"}

// ID3TechnicalCommentDesc reports machine COMM frames. Shared by read filter and writer gate
// (same pattern as [MatroskaTechnicalName]). List is intentionally incomplete.
func ID3TechnicalCommentDesc(desc string) bool {
	up := normalizeKey(desc)
	if technicalCommentDescs[up] {
		return true
	}
	for _, p := range technicalCommentPrefixes {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	return false
}

// ID3InvolvedKeys returns involved-people role keys in emit order. Shared slice; do not mutate.
func ID3InvolvedKeys() []tag.Key {
	return id3InvolvedOrder
}
