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
		{RecordingDate, []string{"DATE", "YEAR"}},
		{OriginalDate, []string{"ORIGINALYEAR"}},
		{TrackTotal, []string{"TOTALTRACKS"}}, // self-alias TRACKTOTAL excluded
		{DiscTotal, []string{"TOTALDISCS"}},   // self-alias DISCTOTAL excluded
		{AlbumArtist, []string{"ALBUM ARTIST", "ALBUM_ARTIST"}},
		{Label, []string{"ORGANIZATION"}},
		{Lyrics, []string{"UNSYNCEDLYRICS"}},
		{DiscNumber, []string{"DISC"}},
		{TrackNumber, []string{"TRACK"}},
		{ReleaseStatus, []string{"MUSICBRAINZ_ALBUMSTATUS"}},
		{ReleaseType, []string{"MUSICBRAINZ_ALBUMTYPE"}},
		{ReleaseCountry, nil}, // every format spells it RELEASECOUNTRY or gets a mapping entry
		{Title, nil},          // a key with no aliases returns nil
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
