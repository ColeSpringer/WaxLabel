package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestMatroskaDJMixerRead folds Matroska's underscore/space DJMIXER spellings onto the
// canonical key on read, while the write side keeps the identity "DJMIXER". Matroska tag
// names are conventionally uppercase-underscore, so "DJ_MIXER" is the spelling a foreign
// file most likely uses for this multi-token role key.
func TestMatroskaDJMixerRead(t *testing.T) {
	for _, name := range []string{"DJ_MIXER", "DJ MIXER", "DJ-MIXER", "dj_mixer", "DJMIXER"} {
		if k, ok := MatroskaTagKey(name); !ok || k != tag.DJMixer {
			t.Errorf("MatroskaTagKey(%q) = %q, %v; want DJMIXER, true", name, k, ok)
		}
	}
	// The write side stays identity (no matroskaNames entry): the canonical "DJMIXER" is
	// emitted and reads back to the same key, so the round-trip is exact.
	if got := MatroskaTagName(tag.DJMixer); got != "DJMIXER" {
		t.Errorf("MatroskaTagName(DJMIXER) = %q, want DJMIXER (identity write)", got)
	}
}

// TestMatroskaReleaseDetail checks the release-detail names round-trip identity, and that
// the APE/legacy-Picard spellings fold on read (this path never consults tag.AliasKey, so
// without an entry the same string would fold on Vorbis and stay custom here).
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

// TestMatroskaCountryNotReleaseCountry pins that a bare COUNTRY SimpleTag stays a custom
// key. The Matroska spec defines COUNTRY as a nesting qualifier that scopes sibling tags to
// a country, not as this release's country, so folding it onto RELEASECOUNTRY would both
// misread the file and subject a free-text value to the two-letter malformed-country lint.
func TestMatroskaCountryNotReleaseCountry(t *testing.T) {
	k, ok := MatroskaTagKey("COUNTRY")
	if !ok || k != tag.Key("COUNTRY") {
		t.Errorf("MatroskaTagKey(\"COUNTRY\") = %q, %v; want the custom key COUNTRY, true", k, ok)
	}
}

// TestMatroskaTechnicalNamesSharedPredicate pins the reserved technical set the
// read filter and the write gate share: the literal statistics names, any
// _STATISTICS-prefixed name, and nothing else.
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
