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
