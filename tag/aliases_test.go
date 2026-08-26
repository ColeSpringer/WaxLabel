package tag

import (
	"slices"
	"testing"
)

// TestKeyAliases checks KeyAliases returns a key's genuine alternative spellings, sorted, with
// self-aliases excluded and nil for a key that has none.
func TestKeyAliases(t *testing.T) {
	cases := []struct {
		key  Key
		want []string
	}{
		{RecordingDate, []string{"DATE", "DATE_RECORDED", "YEAR"}},
		{OriginalDate, []string{"DATE_ORIGINAL", "ORIGINALYEAR", "ORIGINAL_DATE"}},
		{TrackTotal, []string{"TOTALTRACKS", "TOTAL_PARTS"}}, // self-alias TRACKTOTAL excluded
		{DiscTotal, []string{"TOTALDISCS", "TOTAL_DISCS"}},   // self-alias DISCTOTAL excluded
		{AlbumArtist, []string{"ALBUM ARTIST", "ALBUM_ARTIST"}},
		{Label, []string{"ORGANIZATION", "PUBLISHER"}},
		{Lyrics, []string{"UNSYNCEDLYRICS"}},
		{DiscNumber, []string{"DISC"}},
		{TrackNumber, []string{"PART_NUMBER", "TRACK"}},
		{ReleaseStatus, []string{"MUSICBRAINZ_ALBUMSTATUS"}},
		{ReleaseType, []string{"MUSICBRAINZ_ALBUMTYPE"}},
		{ReleaseCountry, nil}, // every format spells it RELEASECOUNTRY or gets a mapping entry
		{Title, nil},          // a key with no aliases returns nil
		{Artist, []string{"LEAD_PERFORMER"}},
		{ReleaseDate, []string{"DATE_RELEASE", "DATE_RELEASED"}},
		{EncodedBy, []string{"ENCODED_BY"}},
		{CatalogNumber, []string{"CATALOG_NUMBER"}},
		{Remixer, []string{"REMIXED_BY"}},
		{Grouping, []string{"CONTENT_GROUP"}},
	}
	for _, tc := range cases {
		if got := KeyAliases(tc.key); !slices.Equal(got, tc.want) {
			t.Errorf("KeyAliases(%s) = %v, want %v", tc.key, got, tc.want)
		}
	}

	// The self-alias is genuinely excluded, not merely absent: TRACKTOTAL still resolves to
	// TrackTotal via AliasKey (so the uppercased canonical spelling works), yet KeyAliases must
	// not echo it back as an alias of itself.
	if k, ok := AliasKey("TRACKTOTAL"); !ok || k != TrackTotal {
		t.Fatalf("AliasKey(TRACKTOTAL) = %v,%v; want TrackTotal,true (precondition)", k, ok)
	}
	if slices.Contains(KeyAliases(TrackTotal), "TRACKTOTAL") {
		t.Error("KeyAliases(TrackTotal) must not include the self-alias TRACKTOTAL")
	}
}

// TestDJMixerAliases folds the spaced/underscored/hyphenated spellings of the only
// multi-token role key onto canonical DJMIXER, so an edit under "DJ MIXER" resolves to it
// instead of silently becoming a custom key. Bare DJMIXER stays a valid canonical key,
// not an alias of itself.
func TestDJMixerAliases(t *testing.T) {
	for _, spelling := range []string{"DJ MIXER", "DJ-MIXER", "DJ_MIXER", "dj mixer"} {
		if k, ok := AliasKey(spelling); !ok || k != DJMixer {
			t.Errorf("AliasKey(%q) = %q, %v; want DJMIXER, true", spelling, k, ok)
		}
	}
	if k, err := ParseKey("DJMIXER"); err != nil || k != DJMixer {
		t.Errorf("ParseKey(DJMIXER) = %q, %v; want DJMIXER, nil", k, err)
	}
	if _, ok := AliasKey("DJMIXER"); ok {
		t.Error("DJMIXER must not be an alias of itself")
	}
}

// TestReleaseDetailAliases folds the MUSICBRAINZ_ALBUM* spellings (the APE convention and the
// legacy Picard names) onto canonical RELEASESTATUS and RELEASETYPE, so a --set under either
// spelling resolves to the canonical key instead of silently creating a custom one. The
// canonical names stay valid keys, not aliases of themselves.
func TestReleaseDetailAliases(t *testing.T) {
	for _, c := range []struct {
		spelling string
		want     Key
	}{
		{"MUSICBRAINZ_ALBUMSTATUS", ReleaseStatus},
		{"musicbrainz_albumstatus", ReleaseStatus},
		{"MUSICBRAINZ_ALBUMTYPE", ReleaseType},
		{"musicbrainz_albumtype", ReleaseType},
	} {
		if k, ok := AliasKey(c.spelling); !ok || k != c.want {
			t.Errorf("AliasKey(%q) = %q, %v; want %s, true", c.spelling, k, ok, c.want)
		}
	}
	for _, k := range []Key{ReleaseCountry, ReleaseStatus, ReleaseType} {
		if _, ok := AliasKey(string(k)); ok {
			t.Errorf("%s must not be an alias of itself", k)
		}
	}
}

// TestMatroskaNativeSpellingAliases pins the Matroska native spellings as edit
// aliases: each resolves to the canonical key the Matroska reader already
// projects it to, and ENCODER stays out (its canonical spelling is itself).
func TestMatroskaNativeSpellingAliases(t *testing.T) {
	cases := map[string]Key{
		"LEAD_PERFORMER": Artist, "DATE_RECORDED": RecordingDate,
		"DATE_RELEASED": ReleaseDate, "DATE_RELEASE": ReleaseDate,
		"DATE_ORIGINAL": OriginalDate, "ORIGINAL_DATE": OriginalDate,
		"ENCODED_BY": EncodedBy, "PART_NUMBER": TrackNumber,
		"TOTAL_PARTS": TrackTotal, "TOTAL_DISCS": DiscTotal,
		"CATALOG_NUMBER": CatalogNumber, "PUBLISHER": Label,
		"REMIXED_BY": Remixer, "CONTENT_GROUP": Grouping,
	}
	for spelling, want := range cases {
		got, ok := AliasKey(spelling)
		if !ok || got != want {
			t.Errorf("AliasKey(%q) = %v, %v; want %v, true", spelling, got, ok, want)
		}
	}
	if _, ok := AliasKey("ENCODER"); ok {
		t.Error("ENCODER must not be an alias entry; ParseKey already resolves it")
	}
}
