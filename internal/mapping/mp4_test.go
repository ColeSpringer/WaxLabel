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
