package mapping

import (
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TXXX:TCMP folds to COMPILATION (ffmpeg spelling).
func TestID3TXXXKeyTCMP(t *testing.T) {
	for _, desc := range []string{"TCMP", "tcmp", " Tcmp "} {
		if k, ok := ID3TXXXKey(desc); !ok || k != tag.Compilation {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want COMPILATION, true", desc, k, ok)
		}
	}
	if k, ok := ID3TXXXKey("SOMETHINGELSE"); !ok || k == tag.Compilation {
		t.Errorf("ID3TXXXKey(unlisted) = %q, %v; want a custom key (not COMPILATION)", k, ok)
	}
}

// TEXT frame and TXXX:LYRICIST both map to LYRICIST.
func TestID3LyricistFrame(t *testing.T) {
	if k, ok := ID3FrameKey("TEXT"); !ok || k != tag.Lyricist {
		t.Errorf("ID3FrameKey(\"TEXT\") = %q, %v; want LYRICIST, true", k, ok)
	}
	if id, ok := ID3KeyFrame(tag.Lyricist); !ok || id != "TEXT" {
		t.Errorf("ID3KeyFrame(LYRICIST) = %q, %v; want TEXT, true", id, ok)
	}
	if k, ok := ID3TXXXKey("LYRICIST"); !ok || k != tag.Lyricist {
		t.Errorf("ID3TXXXKey(\"LYRICIST\") = %q, %v; want LYRICIST, true", k, ok)
	}
}

// TIPL/IPLS roles: Picard write spellings, read aliases, WRITER excluded.
func TestID3InvolvedRoles(t *testing.T) {
	cases := []struct {
		key tag.Key
		fn  string
	}{
		{tag.Producer, "producer"},
		{tag.Engineer, "engineer"},
		{tag.Mixer, "mix"},
		{tag.Arranger, "arranger"},
		{tag.DJMixer, "DJ-mix"},
	}
	for _, c := range cases {
		if got, ok := ID3InvolvedFunction(c.key); !ok || got != c.fn {
			t.Errorf("ID3InvolvedFunction(%s) = %q, %v; want %q, true", c.key, got, ok, c.fn)
		}
		if got, ok := ID3InvolvedRoleKey(c.fn); !ok || got != c.key {
			t.Errorf("ID3InvolvedRoleKey(%q) = %q, %v; want %s, true", c.fn, got, ok, c.key)
		}
	}

	if fn, ok := ID3InvolvedFunction(tag.Writer); ok {
		t.Errorf("ID3InvolvedFunction(WRITER) = %q, true; want false (WRITER is a TXXX frame)", fn)
	}

	for _, c := range []struct {
		fn   string
		want tag.Key
	}{
		{"Mix", tag.Mixer},
		{"DJ-MIX", tag.DJMixer},
		{"PRODUCER", tag.Producer},
	} {
		if got, ok := ID3InvolvedRoleKey(c.fn); !ok || got != c.want {
			t.Errorf("ID3InvolvedRoleKey(%q) = %q, %v; want %s, true (case must fold)", c.fn, got, ok, c.want)
		}
	}

	for _, c := range []struct {
		fn   string
		want tag.Key
	}{
		{"mixer", tag.Mixer},
		{"dj-mixer", tag.DJMixer},
		{"djmixer", tag.DJMixer},
		{"dj mix", tag.DJMixer},
		{"dj mixer", tag.DJMixer},
		{"dj_mixer", tag.DJMixer},
		{"DJ_MIXER", tag.DJMixer},
	} {
		if got, ok := ID3InvolvedRoleKey(c.fn); !ok || got != c.want {
			t.Errorf("ID3InvolvedRoleKey(%q) = %q, %v; want %s, true (read alias must fold)", c.fn, got, ok, c.want)
		}
	}
	if got, _ := ID3InvolvedFunction(tag.Mixer); got != "mix" {
		t.Errorf("ID3InvolvedFunction(MIXER) = %q, want mix (write stays canonical, not the read alias)", got)
	}
	if got, _ := ID3InvolvedFunction(tag.DJMixer); got != "DJ-mix" {
		t.Errorf("ID3InvolvedFunction(DJMIXER) = %q, want DJ-mix", got)
	}

	if k, ok := ID3InvolvedRoleKey("mastering"); ok {
		t.Errorf("ID3InvolvedRoleKey(mastering) = %q, true; want no match", k)
	}

	want := []tag.Key{tag.Producer, tag.Engineer, tag.Mixer, tag.Arranger, tag.DJMixer}
	if got := ID3InvolvedKeys(); !slices.Equal(got, want) {
		t.Errorf("ID3InvolvedKeys() = %v, want %v", got, want)
	}
}

// TXXX DJMIXER separator variants fold on read; write uses TIPL/IPLS.
func TestID3TXXXKeyDJMixer(t *testing.T) {
	for _, desc := range []string{"DJ MIXER", "DJ-MIXER", "DJ_MIXER", "dj mixer"} {
		if k, ok := ID3TXXXKey(desc); !ok || k != tag.DJMixer {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want DJMIXER, true", desc, k, ok)
		}
	}
}

// Release-detail TXXX: Picard names on write; ParseKey fallthrough for bare canonical spellings.
func TestID3ReleaseDetailTXXX(t *testing.T) {
	cases := []struct {
		key  tag.Key
		desc string
	}{
		{tag.ReleaseCountry, "MusicBrainz Album Release Country"},
		{tag.ReleaseStatus, "MusicBrainz Album Status"},
		{tag.ReleaseType, "MusicBrainz Album Type"},
	}
	for _, c := range cases {
		if k, ok := ID3TXXXKey(c.desc); !ok || k != c.key {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want %s, true", c.desc, k, ok, c.key)
		}
		if k, ok := ID3TXXXKey(strings.ToLower(c.desc)); !ok || k != c.key {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want %s, true (case must fold)", strings.ToLower(c.desc), k, ok, c.key)
		}
		if k, ok := ID3TXXXKey(string(c.key)); !ok || k != c.key {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want %s, true", c.key, k, ok, c.key)
		}
		if got := ID3TXXXDesc(c.key); got != c.desc {
			t.Errorf("ID3TXXXDesc(%s) = %q, want %q", c.key, got, c.desc)
		}
	}
	for _, c := range []struct {
		desc string
		want tag.Key
	}{
		{"MUSICBRAINZ_ALBUMSTATUS", tag.ReleaseStatus},
		{"musicbrainz_albumtype", tag.ReleaseType},
	} {
		if k, ok := ID3TXXXKey(c.desc); !ok || k != c.want {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want %s, true", c.desc, k, ok, c.want)
		}
	}
}

// Matroska native spellings fold on TXXX read (no tag.AliasKey on this path).
func TestID3TXXXKeyMatroskaNativeSpellings(t *testing.T) {
	for desc, want := range map[string]tag.Key{
		"LEAD_PERFORMER": tag.Artist, "DATE_RECORDED": tag.RecordingDate,
		"DATE_RELEASED": tag.ReleaseDate, "DATE_RELEASE": tag.ReleaseDate,
		"DATE_ORIGINAL": tag.OriginalDate, "ORIGINAL_DATE": tag.OriginalDate,
		"ENCODED_BY": tag.EncodedBy, "PART_NUMBER": tag.TrackNumber,
		"TOTAL_PARTS": tag.TrackTotal, "TOTAL_DISCS": tag.DiscTotal,
		"CATALOG_NUMBER": tag.CatalogNumber, "publisher": tag.Label,
		"REMIXED_BY": tag.Remixer, "CONTENT_GROUP": tag.Grouping,
	} {
		if k, ok := ID3TXXXKey(desc); !ok || k != want {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want %s, true", desc, k, ok, want)
		}
	}
}
