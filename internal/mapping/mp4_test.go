package mapping

import (
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// MP4FreeformKey folds case on read; separators are not normalized.
func TestMP4FreeformKeyFoldsCase(t *testing.T) {
	for _, name := range []string{"MusicBrainz Album Id", "musicbrainz album id", "MUSICBRAINZ ALBUM ID"} {
		if k, ok := MP4FreeformKey(name); !ok || k != tag.MBReleaseID {
			t.Errorf("MP4FreeformKey(%q) = %q, %v; want MBReleaseID, true (case must fold)", name, k, ok)
		}
	}
	if k, ok := MP4FreeformKey("musicbrainz_album_id"); ok {
		t.Errorf("MP4FreeformKey(%q) = %q, true; want no match (folding is case-only, not separator-normalizing)", "musicbrainz_album_id", k)
	}
	if k, ok := MP4FreeformKey("Unknown Freeform"); ok {
		t.Errorf("MP4FreeformKey(%q) = %q, true; want no match", "Unknown Freeform", k)
	}
}

// Write spelling unchanged by read-side freeformFold.
func TestMP4KeyFreeformSpellingUnchanged(t *testing.T) {
	if got := MP4KeyFreeform(tag.MBReleaseID); got != "MusicBrainz Album Id" {
		t.Errorf("MP4KeyFreeform(MBReleaseID) = %q, want the unchanged Picard spelling %q", got, "MusicBrainz Album Id")
	}
}

// LYRICIST freeform: write uppercase; read folds case.
func TestMP4LyricistFreeform(t *testing.T) {
	if got := MP4KeyFreeform(tag.Lyricist); got != "LYRICIST" {
		t.Errorf("MP4KeyFreeform(Lyricist) = %q, want LYRICIST", got)
	}
	for _, name := range []string{"LYRICIST", "Lyricist", "lyricist"} {
		if k, ok := MP4FreeformKey(name); !ok || k != tag.Lyricist {
			t.Errorf("MP4FreeformKey(%q) = %q, %v; want LYRICIST, true (case must fold)", name, k, ok)
		}
	}
}

// Contributor roles as freeforms; MIXER/DJMIXER not ID3 mix/DJ-mix spellings.
func TestMP4RoleFreeforms(t *testing.T) {
	cases := []struct {
		key       tag.Key
		name      string
		spellings []string
	}{
		{tag.Producer, "PRODUCER", []string{"PRODUCER", "Producer", "producer"}},
		{tag.Engineer, "ENGINEER", []string{"ENGINEER", "Engineer", "engineer"}},
		{tag.Mixer, "MIXER", []string{"MIXER", "Mixer", "mixer"}},
		{tag.Arranger, "ARRANGER", []string{"ARRANGER", "Arranger", "arranger"}},
		{tag.Writer, "WRITER", []string{"WRITER", "Writer", "writer"}},
		{tag.DJMixer, "DJMIXER", []string{"DJMIXER", "djmixer", "DjMixer", "DJ MIXER", "DJ_MIXER", "DJ-MIXER", "dj mixer"}},
	}
	for _, c := range cases {
		if got := MP4KeyFreeform(c.key); got != c.name {
			t.Errorf("MP4KeyFreeform(%s) = %q, want %q", c.key, got, c.name)
		}
		for _, spelling := range c.spellings {
			if k, ok := MP4FreeformKey(spelling); !ok || k != c.key {
				t.Errorf("MP4FreeformKey(%q) = %q, %v; want %s, true (case must fold)", spelling, k, ok, c.key)
			}
		}
	}
}

// Release-detail freeforms use Picard mixed-case names; uppercase canonical keys use codec fallback.
func TestMP4ReleaseDetailFreeforms(t *testing.T) {
	cases := []struct {
		key  tag.Key
		name string
	}{
		{tag.ReleaseCountry, "MusicBrainz Album Release Country"},
		{tag.ReleaseStatus, "MusicBrainz Album Status"},
		{tag.ReleaseType, "MusicBrainz Album Type"},
	}
	for _, c := range cases {
		if got := MP4KeyFreeform(c.key); got != c.name {
			t.Errorf("MP4KeyFreeform(%s) = %q, want %q", c.key, got, c.name)
		}
		for _, spelling := range []string{c.name, strings.ToLower(c.name), strings.ToUpper(c.name)} {
			if k, ok := MP4FreeformKey(spelling); !ok || k != c.key {
				t.Errorf("MP4FreeformKey(%q) = %q, %v; want %s, true (case must fold)", spelling, k, ok, c.key)
			}
		}
	}
	// Uppercase canonical keys are not in mp4Freeform; codec valid-key fallback owns them.
	for _, c := range cases {
		if k, ok := MP4FreeformKey(string(c.key)); ok {
			t.Errorf("MP4FreeformKey(%q) = %q, true; want no table entry (the codec's valid-key fallback owns it)", c.key, k)
		}
	}
	// APE/Picard underscored spellings fold on read only.
	for _, c := range []struct {
		name string
		want tag.Key
	}{
		{"MUSICBRAINZ_ALBUMSTATUS", tag.ReleaseStatus},
		{"musicbrainz_albumtype", tag.ReleaseType},
	} {
		if k, ok := MP4FreeformKey(c.name); !ok || k != c.want {
			t.Errorf("MP4FreeformKey(%q) = %q, %v; want %s, true", c.name, k, ok, c.want)
		}
	}
	if got := MP4KeyFreeform(tag.ReleaseStatus); got != picardStatusName {
		t.Errorf("MP4KeyFreeform(RELEASESTATUS) = %q, want the Picard name %q", got, picardStatusName)
	}
}

const picardStatusName = "MusicBrainz Album Status"

// Matroska native spellings fold on freeform read; write keeps canonical spellings.
func TestMP4FreeformKeyMatroskaNativeSpellings(t *testing.T) {
	for name, want := range map[string]tag.Key{
		"LEAD_PERFORMER": tag.Artist, "DATE_RECORDED": tag.RecordingDate,
		"DATE_RELEASED": tag.ReleaseDate, "DATE_RELEASE": tag.ReleaseDate,
		"DATE_ORIGINAL": tag.OriginalDate, "ORIGINAL_DATE": tag.OriginalDate,
		"ENCODED_BY": tag.EncodedBy, "PART_NUMBER": tag.TrackNumber,
		"TOTAL_PARTS": tag.TrackTotal, "TOTAL_DISCS": tag.DiscTotal,
		"CATALOG_NUMBER": tag.CatalogNumber, "publisher": tag.Label,
		"REMIXED_BY": tag.Remixer, "CONTENT_GROUP": tag.Grouping,
	} {
		if k, ok := MP4FreeformKey(name); !ok || k != want {
			t.Errorf("MP4FreeformKey(%q) = %q, %v; want %s, true", name, k, ok, want)
		}
	}
}

// ffmpeg writes its own metadata names into an mdta keys index; each reads as the key its ilst
// atom does.
func TestMP4MdtaKeyFFmpegNames(t *testing.T) {
	for name, want := range map[string]tag.Key{
		"album_artist": tag.AlbumArtist, "track": tag.TrackNumber, "disc": tag.DiscNumber,
		"sort_name": tag.TitleSort, "sort_artist": tag.ArtistSort, "sort_album": tag.AlbumSort,
		"sort_album_artist": tag.AlbumArtistSort, "sort_composer": tag.ComposerSort,
		"sort_show": tag.ShowSort, "synopsis": tag.LongDescription,
		"compilation": tag.Compilation, "gapless_playback": tag.ITunesGapless,
		"media_type": tag.MediaType, "rating": tag.ITunesAdvisory, "tmpo": tag.BPM,
		"show": tag.Show, "episode_id": tag.EpisodeID, "network": tag.Network,
		"episode_sort": tag.EpisodeNumber, "season_number": tag.SeasonNumber,
		"com.apple.quicktime.album_artist": tag.AlbumArtist,
		"albumartist":                      tag.AlbumArtist, "USLT": tag.Lyrics, "TOTALTRACKS": tag.TrackTotal,
		"year": tag.RecordingDate,
	} {
		if k, ok := MP4MdtaKey(name); !ok || k != want {
			t.Errorf("MP4MdtaKey(%q) = %q, %v; want %s", name, k, ok, want)
		}
	}
	if k, _ := MP4MdtaKey("custom_key"); k != "CUSTOM_KEY" {
		t.Errorf("MP4MdtaKey(custom_key) = %q, want the folded custom key", k)
	}
	// A custom key is written in capitals: one spelled like a name of the table is still itself.
	for name := range mp4Mdta {
		custom := tag.Key(strings.ToUpper(name))
		if custom.Known() {
			continue
		}
		if _, alias := tag.AliasKey(string(custom)); alias {
			continue // an alias cannot be a custom key: the editor resolves it
		}
		if k, ok := MP4MdtaKey(MP4KeyMdta(custom)); !ok || k != custom {
			t.Errorf("custom key %s is written as %q, which reads as %s", custom, MP4KeyMdta(custom), k)
		}
	}
}

// A new mdta entry takes ffmpeg's spelling, so ffprobe names the value as it names the ilst atom's.
func TestMP4KeyMdtaWritesFFmpegSpellings(t *testing.T) {
	for key, want := range map[tag.Key]string{
		tag.AlbumArtist: "album_artist", tag.TrackNumber: "track", tag.DiscNumber: "disc",
		tag.TitleSort: "sort_name", tag.ArtistSort: "sort_artist", tag.AlbumSort: "sort_album",
		tag.AlbumArtistSort: "sort_album_artist", tag.ComposerSort: "sort_composer",
		tag.ShowSort: "sort_show", tag.LongDescription: "synopsis", tag.MediaType: "media_type",
		tag.Compilation: "compilation", tag.ITunesGapless: "gapless_playback",
		tag.ITunesAdvisory: "rating", tag.BPM: "tmpo", tag.Show: "show",
		tag.EpisodeID: "episode_id", tag.Network: "network", tag.EpisodeNumber: "episode_sort",
		tag.SeasonNumber: "season_number", tag.Title: "title", tag.MBReleaseID: "MUSICBRAINZ_ALBUMID",
	} {
		if got := MP4KeyMdta(key); got != want {
			t.Errorf("MP4KeyMdta(%s) = %q, want %q", key, got, want)
		}
		if back, ok := MP4MdtaKey(want); !ok || back != key {
			t.Errorf("MP4MdtaKey(%q) = %q, %v; want %s back", want, back, ok, key)
		}
	}
}

// A freeform name that is an alias reads as the key the alias names, or the key could not be
// cleared by the name it shows under.
func TestMP4FreeformKeyFoldsAliases(t *testing.T) {
	for name, want := range map[string]tag.Key{
		"USLT": tag.Lyrics, "TOTALTRACKS": tag.TrackTotal, "totaldiscs": tag.DiscTotal,
		"DATE": tag.RecordingDate, "Year": tag.RecordingDate, "ORGANIZATION": tag.Label,
		"Album Artist": tag.AlbumArtist,
	} {
		if k, ok := MP4FreeformKey(name); !ok || k != want {
			t.Errorf("MP4FreeformKey(%q) = %q, %v; want %s", name, k, ok, want)
		}
	}
}

// Every canonical key reads back from the name a new mdta entry gives it: a key with no name of
// ffmpeg's is written under its own spelling, which no name of ffmpeg's may capture.
func TestMP4MdtaKeyRoundTripsEveryKnownKey(t *testing.T) {
	for _, k := range tag.KnownKeys() {
		if k == tag.TrackTotal || k == tag.DiscTotal {
			continue // stored with the number, or under an alias of its own
		}
		if back, ok := MP4MdtaKey(MP4KeyMdta(k)); !ok || back != k {
			t.Errorf("%s is written as %q, which reads as %q", k, MP4KeyMdta(k), back)
		}
	}
	for name, want := range map[string]tag.Key{"RATING": tag.Rating, "rating": tag.ITunesAdvisory, "com.apple.quicktime.RATING": tag.Rating} {
		if k, _ := MP4MdtaKey(name); k != want {
			t.Errorf("MP4MdtaKey(%q) = %s, want %s", name, k, want)
		}
	}
}
