package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// DJ_MIXER/DJ MIXER/DJ-MIXER read as DJMIXER; write stays "DJMIXER".
func TestMatroskaDJMixerRead(t *testing.T) {
	for _, name := range []string{"DJ_MIXER", "DJ MIXER", "DJ-MIXER", "dj_mixer", "DJMIXER"} {
		if k, ok := MatroskaTagKey(name); !ok || k != tag.DJMixer {
			t.Errorf("MatroskaTagKey(%q) = %q, %v; want DJMIXER, true", name, k, ok)
		}
	}
	if got := MatroskaTagName(tag.DJMixer); got != "DJMIXER" {
		t.Errorf("MatroskaTagName(DJMIXER) = %q, want DJMIXER (identity write)", got)
	}
}

// Release detail keys round-trip identity; APE/Picard underscored spellings fold on read.
func TestMatroskaReleaseDetail(t *testing.T) {
	for _, k := range []tag.Key{tag.ReleaseCountry, tag.ReleaseStatus, tag.ReleaseType} {
		if got := MatroskaTagName(k); got != string(k) {
			t.Errorf("MatroskaTagName(%s) = %q, want the identity name", k, got)
		}
		if got, ok := MatroskaTagKey(string(k)); !ok || got != k {
			t.Errorf("MatroskaTagKey(%q) = %q, %v; want %s, true", k, got, ok, k)
		}
	}
	for _, c := range []struct {
		name string
		want tag.Key
	}{
		{"MUSICBRAINZ_ALBUMSTATUS", tag.ReleaseStatus},
		{"musicbrainz_albumtype", tag.ReleaseType},
	} {
		if k, ok := MatroskaTagKey(c.name); !ok || k != c.want {
			t.Errorf("MatroskaTagKey(%q) = %q, %v; want %s, true", c.name, k, ok, c.want)
		}
	}
}

// COUNTRY is a nesting qualifier in the spec, not RELEASECOUNTRY.
func TestMatroskaCountryNotReleaseCountry(t *testing.T) {
	k, ok := MatroskaTagKey("COUNTRY")
	if !ok || k != tag.Key("COUNTRY") {
		t.Errorf("MatroskaTagKey(\"COUNTRY\") = %q, %v; want the custom key COUNTRY, true", k, ok)
	}
}

// Read filter and write gate share MatroskaTechnicalName.
func TestMatroskaTechnicalNamesSharedPredicate(t *testing.T) {
	for _, name := range []string{"DURATION", "BPS", "NUMBER_OF_FRAMES", "NUMBER_OF_BYTES",
		"NUMBER_OF_BYTES_UNCOMPRESSED", "NUMBER_OF_FRAMES_UNCOMPRESSED",
		"_STATISTICS_WRITING_APP", "_STATISTICS_TAGS", "_STATISTICS_ANYTHING", "duration"} {
		if !MatroskaTechnicalName(name) {
			t.Errorf("MatroskaTechnicalName(%q) = false, want true", name)
		}
		if _, ok := MatroskaTagKey(name); ok {
			t.Errorf("MatroskaTagKey(%q) projects, want filtered", name)
		}
	}
	for _, name := range []string{"_STATISTIC_X", "STATISTICS_X", "DURATION_X", "X_DURATION", "BPS_X", "NUMBER_OF_X", "TITLE"} {
		if MatroskaTechnicalName(name) {
			t.Errorf("MatroskaTechnicalName(%q) = true, want false", name)
		}
	}
}

// A name spelled as an alias reads under the key the alias names: no SimpleTag reaches the tags
// under a key the editor resolves away.
func TestMatroskaAliasNamesFold(t *testing.T) {
	for name, want := range map[string]tag.Key{
		"YEAR": tag.RecordingDate, "uslt": tag.Lyrics, "UNSYNCEDLYRICS": tag.Lyrics, "TOTALTRACKS": tag.TrackTotal,
		"ORGANIZATION": tag.Label, "ALBUM ARTIST": tag.AlbumArtist, "TRACK": tag.TrackNumber,
		// The spec's and ffmpeg's spellings, once copied here by hand.
		"ALBUM_ARTIST": tag.AlbumArtist, "LEAD_PERFORMER": tag.Artist, "DATE": tag.RecordingDate,
		"DATE_RELEASED": tag.ReleaseDate, "PART_NUMBER": tag.TrackNumber, "TOTAL_PARTS": tag.TrackTotal,
		"DISC": tag.DiscNumber, "PUBLISHER": tag.Label, "ENCODER": tag.Encoder, "ENCODED_BY": tag.EncodedBy,
	} {
		if k, ok := MatroskaTagKey(name); !ok || k != want {
			t.Errorf("MatroskaTagKey(%q) = %q, %v; want %s, true", name, k, ok, want)
		}
	}
}

// In a file tagged by level the album-level TITLE and ARTIST are the album's, read and written;
// below album level, and in any other file, a name stands for its own key.
func TestMatroskaLevelNames(t *testing.T) {
	for _, c := range []struct {
		key        tag.Key
		level      int
		convention bool
		name       string
	}{
		{tag.Album, 50, true, "TITLE"},
		{tag.AlbumArtist, 50, true, "ARTIST"},
		{tag.Album, 30, true, "ALBUM"},
		{tag.AlbumArtist, 40, true, "ALBUM_ARTIST"},
		{tag.Album, 50, false, "ALBUM"},
		{tag.AlbumArtist, 50, false, "ALBUM_ARTIST"},
		{tag.Genre, 50, true, "GENRE"},
		{tag.Title, 30, true, "TITLE"},
		{tag.Artist, 30, true, "ARTIST"},
	} {
		if got := MatroskaTagNameForLevel(c.key, c.level, c.convention); got != c.name {
			t.Errorf("MatroskaTagNameForLevel(%s, %d, %v) = %q, want %q", c.key, c.level, c.convention, got, c.name)
		}
		read, _ := MatroskaTagKey(c.name)
		if got := MatroskaLevelKey(read, c.level, c.convention); got != c.key {
			t.Errorf("a %s at level %d (convention %v) reads as %s, want %s", c.name, c.level, c.convention, got, c.key)
		}
	}
	if got := MatroskaLevelKey(tag.Title, 50, false); got != tag.Title {
		t.Errorf("an album-level TITLE outside the convention reads as %s, want TITLE", got)
	}
	if got := MatroskaLevelKey(tag.Title, 40, true); got != tag.Title {
		t.Errorf("a TITLE below album level reads as %s, want TITLE", got)
	}
	for level, want := range map[int]bool{49: false, 50: true, 59: true, 60: false} {
		if got := MatroskaAlbumLevel(level); got != want {
			t.Errorf("MatroskaAlbumLevel(%d) = %v, want %v", level, got, want)
		}
	}
	// Above album level a name stands for its own key again.
	for _, level := range []int{60, 70} {
		if got := MatroskaLevelKey(tag.Artist, level, true); got != tag.Artist {
			t.Errorf("an ARTIST at level %d reads as %s, want ARTIST", level, got)
		}
		if got := MatroskaTagNameForLevel(tag.Album, level, true); got != "ALBUM" {
			t.Errorf("the album at level %d is written as %s, want ALBUM", level, got)
		}
	}
}
