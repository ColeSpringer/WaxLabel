package tag

import (
	"slices"
	"strings"
	"testing"
)

// TestParseKeyRejectsNonASCII checks that ParseKey rejects non-ASCII input before case
// folding, including confusable letters that would otherwise become valid ASCII keys.
func TestParseKeyRejectsNonASCII(t *testing.T) {
	t.Parallel()
	if k, err := ParseKey("ARTıST"); err == nil {
		t.Errorf("ParseKey(ARTıST) = %q, nil; want an error (non-ASCII must not fold to ARTIST)", k)
	}
	// A plain ASCII key still parses and uppercases, with surrounding space trimmed.
	if k, err := ParseKey("  artist  "); err != nil || k != Artist {
		t.Errorf("ParseKey(artist) = %q, %v; want ARTIST", k, err)
	}
}

// TestMergeCapsSingleValuedKey checks that single-valued keys are capped during merge and
// that the dropped value is recorded in provenance. Multivalued keys keep all values.
func TestMergeCapsSingleValuedKey(t *testing.T) {
	t.Parallel()
	base := NewTagSet()
	base.Set(Title, "First")
	incoming := NewTagSet()
	incoming.Set(Title, "Second")

	out, prov := Merge(base, incoming, Union)
	if got, _ := out.Get(Title); !slices.Equal(got, []string{"First"}) {
		t.Errorf("merged TITLE = %v, want [First] (single-valued cap)", got)
	}
	var p FieldProvenance
	for _, pv := range prov {
		if pv.Key == Title {
			p = pv
		}
	}
	if !slices.Contains(p.Rejected, "Second") {
		t.Errorf("Rejected = %v, want it to include the capped value Second", p.Rejected)
	}
	if !strings.Contains(p.Reason, "capped") {
		t.Errorf("Reason = %q, want it to mention the single-valued cap", p.Reason)
	}

	// A multivalued key (GENRE) keeps both values.
	b2, i2 := NewTagSet(), NewTagSet()
	b2.Set(Genre, "Rock")
	i2.Set(Genre, "Jazz")
	out2, _ := Merge(b2, i2, Union)
	if got, _ := out2.Get(Genre); len(got) != 2 {
		t.Errorf("merged GENRE = %v, want 2 values (multivalued, not capped)", got)
	}
}

// TestPartialDateToleratesWhitespace checks that validation tolerates incidental
// surrounding whitespace and storage trims date keys to the same clean form.
func TestPartialDateToleratesWhitespace(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"2021", " 2021 ", "2021-05", "\t2021-05-03\n"} {
		if !ValidPartialDate(v) {
			t.Errorf("ValidPartialDate(%q) = false, want true (whitespace tolerated)", v)
		}
	}
	if ValidPartialDate(" not-a-date ") {
		t.Error("ValidPartialDate(' not-a-date ') = true, want false (still rejects a non-date)")
	}
	if got := TrimTokenValue(RecordingDate, " 2021 "); got != "2021" {
		t.Errorf("TrimTokenValue(RECORDINGDATE, ' 2021 ') = %q, want 2021 (date keys now trim)", got)
	}
	if got := TrimTokenValue(Title, "  spaced  "); got != "  spaced  " {
		t.Errorf("TrimTokenValue(TITLE) = %q, want it unchanged (non-numeric, non-date)", got)
	}
}

// TestDescribesOwnAudio checks the copy-exclusion property: keys that describe a file's
// own audio are flagged, while portable work metadata is not.
func TestDescribesOwnAudio(t *testing.T) {
	t.Parallel()
	own := []Key{
		Encoder, EncodedBy, EncodingHistory, AcoustIDFingerprint,
		ReplayGainTrackGain, ReplayGainTrackPeak, ReplayGainAlbumGain, ReplayGainAlbumPeak,
	}
	for _, k := range own {
		if !k.DescribesOwnAudio() {
			t.Errorf("%s.DescribesOwnAudio() = false, want true (describes this file's own audio)", k)
		}
	}
	// The AcoustID ID identifies the recording; only the fingerprint is file-specific.
	notOwn := []Key{AcoustID, Title, Artist, RecordingDate, MBRecordingID, ReplayGainTrackGain + "X"}
	for _, k := range notOwn {
		if k.DescribesOwnAudio() {
			t.Errorf("%s.DescribesOwnAudio() = true, want false (not an own-audio key)", k)
		}
	}
}

// TestTrimTokenValueMediaTypeReplayGain checks that MEDIATYPE and the REPLAYGAIN_* keys are
// single-token values, so TrimTokenValue strips their surrounding whitespace the same way it does
// numeric and date keys. Internal whitespace (the space before "dB") is preserved, and a
// free-text key is left untouched.
func TestTrimTokenValueMediaTypeReplayGain(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		k    Key
		in   string
		want string
	}{
		{MediaType, " 2 ", "2"},
		{ReplayGainTrackGain, " -7.30 dB ", "-7.30 dB"},
		{ReplayGainAlbumGain, "  -3.21 dB  ", "-3.21 dB"},
		{ReplayGainTrackPeak, "\t0.998643\n", "0.998643"},
		{Title, " keep me ", " keep me "}, // a free-text key is left untouched
	} {
		if got := TrimTokenValue(c.k, c.in); got != c.want {
			t.Errorf("TrimTokenValue(%s, %q) = %q, want %q", c.k, c.in, got, c.want)
		}
	}
}

// TestValidPartialDateRejectsYearZero covers the cosmetic year-0000 guard: time.Parse would accept
// "0000", but it is not a meaningful year, so ValidPartialDate rejects it (and its month/day
// extensions) while still accepting real dates - keeping lint and set-time validation in agreement.
func TestValidPartialDateRejectsYearZero(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"0000", "0000-01", "0000-01-01"} {
		if ValidPartialDate(s) {
			t.Errorf("ValidPartialDate(%q) = true, want false (year 0000 is not valid)", s)
		}
	}
	for _, s := range []string{"2021", "2021-05", "2021-05-03", "0001"} {
		if !ValidPartialDate(s) {
			t.Errorf("ValidPartialDate(%q) = false, want true (a real date must stay valid)", s)
		}
	}
}
