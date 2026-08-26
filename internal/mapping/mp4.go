package mapping

import "github.com/colespringer/waxlabel/tag"

// This file holds the MP4/iTunes metadata <-> canonical mapping shared by the
// mp4 codec. iTunes-style tags live in a "moov.udta.meta.ilst" atom list. Each
// item is a four-character atom whose payload is one or more "data" sub-atoms;
// the four-character names use the 0xA9 byte for the classic Apple text atoms.
// A second, open-ended vocabulary lives in "----" freeform atoms keyed by a
// reverse-DNS mean (almost always "com.apple.iTunes") plus a name - that is
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
	"\xa9too": tag.Encoder, // iTunes "encoding tool" - the Lavf... stamp
	"cprt":    tag.Copyright,
	"\xa9grp": tag.Grouping,
	"\xa9lyr": tag.Lyrics,
	"desc":    tag.Description,     // iTunes short description (audiobooks, podcasts)
	"ldes":    tag.LongDescription, // iTunes long description
	"soal":    tag.AlbumSort,
	"soaa":    tag.AlbumArtistSort,
	"soar":    tag.ArtistSort,
	"sonm":    tag.TitleSort,
	"soco":    tag.ComposerSort,
	"\xa9wrk": tag.Work,         // classical work title
	"\xa9mvn": tag.MovementName, // movement name
	// iTunes "encoded by" - the person, distinct from the ©too tool stamp. One known
	// interop wrinkle: ffmpeg folds BOTH ©too and ©enc onto its single "encoder" tag, so
	// ffprobe shows whichever atom comes later when a file carries both; iTunes and Mp3tag
	// keep them distinct, which is the semantic this mapping follows.
	"\xa9enc": tag.EncodedBy,
}

// mp4Freeform maps a "com.apple.iTunes" freeform name to its canonical key.
// These names are the de-facto Picard/MusicBrainz conventions. Note iTunes's
// historical naming: the *recording* MBID is stored under "MusicBrainz Track Id"
// while the *release-track* MBID is "MusicBrainz Release Track Id" - matching our
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
	"NARRATOR":                     tag.Narrator, // de-facto audiobook narrator freeform
	"LYRICIST":                     tag.Lyricist, // MP4 has no standard lyricist atom
	// Release-level detail, under the same mixed-case Picard names ID3 uses as TXXX
	// descriptions. Without these the atoms miss decodeFreeform's valid-key fallback
	// (validKeyByte rejects lowercase) and stay preserved but invisible.
	"MusicBrainz Album Release Country": tag.ReleaseCountry,
	"MusicBrainz Album Status":          tag.ReleaseStatus,
	"MusicBrainz Album Type":            tag.ReleaseType,
	// Contributor roles: MP4 has no standard atoms, so store the canonical uppercase names
	// as com.apple.iTunes freeforms (MP4 uses MIXER/DJMIXER, not the ID3-only mix/DJ-mix).
	"PRODUCER": tag.Producer,
	"ENGINEER": tag.Engineer,
	"MIXER":    tag.Mixer,
	"ARRANGER": tag.Arranger,
	"WRITER":   tag.Writer,
	"DJMIXER":  tag.DJMixer,
}

var (
	keyMP4Text     = map[tag.Key]string{}
	keyMP4Freeform = map[tag.Key]string{}
	// freeformFold is the case-folded read index: an upper-cased freeform name to its
	// canonical key. It is separate from mp4Freeform (which drives the write spelling) so
	// folding the read path never disturbs the exact Picard/ReplayGain names WaxLabel writes.
	// The current names have no upper-case collisions, so the fold is unambiguous.
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
	// Read-only aliases: fold the multi-token DJMIXER spelling variants a foreign freeform
	// might use onto the canonical key. Seeded into freeformFold only, never keyMP4Freeform:
	// giving one key several names would make its single write spelling nondeterministic, so
	// writes still emit the canonical "DJMIXER".
	for _, name := range []string{"DJ MIXER", "DJ_MIXER", "DJ-MIXER"} {
		freeformFold[normalizeKey(name)] = tag.DJMixer
	}
	// Likewise the APE/legacy-Picard spellings for release status and type: this path never
	// consults tag.AliasKey, so without these the same string folds on Vorbis and stays a
	// custom key here. Writes still emit the Picard "MusicBrainz Album Status"/"... Type".
	freeformFold[normalizeKey("MUSICBRAINZ_ALBUMSTATUS")] = tag.ReleaseStatus
	freeformFold[normalizeKey("MUSICBRAINZ_ALBUMTYPE")] = tag.ReleaseType
	// The Matroska native tag spellings, read-only like the entries above: they are
	// edit aliases on every format (tag/aliases.go), so a freeform atom using one
	// must fold onto the same canonical key or a set under the spelling would
	// append beside the atom instead of replacing it. Writes keep the canonical
	// spellings (none is seeded into mp4Freeform).
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
	// The iTunes structured keys and ENCODEDBY, read-only: a mixed-case freeform variant
	// ("iTunesAdvisory") folds onto the canonical key like it already does on ID3/Vorbis.
	// Never seeded into mp4Freeform - these keys write structured or ©-text atoms, and the
	// upper-case canonical spellings already fall through decodeFreeform's valid-key path.
	for _, k := range []tag.Key{
		tag.ITunesAdvisory, tag.ITunesGapless, tag.ShowMovement, tag.BPM,
		tag.Work, tag.MovementName, tag.Movement, tag.MovementTotal, tag.EncodedBy,
	} {
		freeformFold[normalizeKey(string(k))] = k
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
// name and whether it is one of the mapped names. The match folds case (like the
// ID3/Matroska read paths at [ID3TXXXKey]/[MatroskaTagKey]) so a foreign or
// hand-edited atom using non-standard casing still resolves into the canonical
// view rather than being hidden from dump/diff/copy. Case and surrounding whitespace
// are folded (via the shared normalizeKey), not separators, so "musicbrainz_album_id"
// still misses "MusicBrainz Album Id"; the one exception is the multi-token DJMIXER key,
// whose separator variants ("DJ MIXER"/"DJ_MIXER"/"DJ-MIXER") are seeded as explicit read
// aliases in init.
func MP4FreeformKey(name string) (tag.Key, bool) {
	// Fast path for standard-cased names (the common case): an exact hit avoids the normalizeKey
	// allocation. The fold table has no collisions, so this returns the same key the fold would.
	if k, ok := mp4Freeform[name]; ok {
		return k, true
	}
	k, ok := freeformFold[normalizeKey(name)]
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
