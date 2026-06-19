package tag

import (
	"errors"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/waxerr"
)

func TestKeyValidation(t *testing.T) {
	cases := []struct {
		in   string
		want string // expected normalized key, or "" if invalid
	}{
		{"title", "TITLE"},
		{"TITLE", "TITLE"},
		{"MusicBrainz_AlbumId", "MUSICBRAINZ_ALBUMID"},
		{"", ""},
		{"bad=key", ""},    // '=' is forbidden
		{"bad\x00key", ""}, // control byte
		{"with space", "WITH SPACE"},
	}
	for _, c := range cases {
		k, err := ParseKey(c.in)
		if c.want == "" {
			if err == nil {
				t.Errorf("ParseKey(%q) = %q, want error", c.in, k)
			} else if !errors.Is(err, waxerr.ErrInvalidKey) {
				t.Errorf("ParseKey(%q) err = %v, want ErrInvalidKey", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseKey(%q) unexpected err: %v", c.in, err)
		} else if string(k) != c.want {
			t.Errorf("ParseKey(%q) = %q, want %q", c.in, k, c.want)
		}
	}
}

func TestKeyUppercaseEnforcement(t *testing.T) {
	// A hand-built lowercase key is invalid; ParseKey normalizes instead.
	if Key("title").Valid() {
		t.Error(`Key("title") should be invalid (lowercase)`)
	}
	if !Title.Valid() {
		t.Error("Title constant should be valid")
	}
	k, err := ParseKey("title")
	if err != nil || k != Title {
		t.Errorf("ParseKey(title) = %q, %v; want TITLE", k, err)
	}
	if k2, _ := ParseKey("My_Custom"); k2 != "MY_CUSTOM" {
		t.Errorf("ParseKey(My_Custom) = %q, want MY_CUSTOM", k2)
	}
	// Symbols/digits (no case) remain valid.
	if !Key("REPLAYGAIN_TRACK_GAIN").Valid() {
		t.Error("underscore/uppercase key should be valid")
	}
}

func TestKeyKnown(t *testing.T) {
	if !Title.Known() {
		t.Error("Title should be known")
	}
	if Title.Description() == "" {
		t.Error("Title should have a description")
	}
	custom := Key("MY_CUSTOM_FIELD")
	if custom.Known() {
		t.Error("custom field should not be known")
	}
	if custom.Description() != "" {
		t.Error("custom field should have no description")
	}
}

// The presence-aware contract: absent, present-empty, and present-with-values
// are three distinct states.
func TestTagSetPresenceStates(t *testing.T) {
	var s TagSet

	if s.Has(Title) {
		t.Error("absent key should report Has=false")
	}
	if _, ok := s.Get(Title); ok {
		t.Error("absent key Get should report ok=false")
	}

	s.Set(Title) // present, empty
	if !s.Has(Title) {
		t.Error("present-empty key should report Has=true")
	}
	vals, ok := s.Get(Title)
	if !ok || len(vals) != 0 {
		t.Errorf("present-empty Get = (%v,%v), want ([], true)", vals, ok)
	}
	if _, ok := s.First(Title); ok {
		t.Error("present-empty First should report ok=false")
	}

	s.Set(Title, "Song")
	if v, ok := s.First(Title); !ok || v != "Song" {
		t.Errorf("present First = (%q,%v), want (Song,true)", v, ok)
	}

	s.Delete(Title)
	if s.Has(Title) {
		t.Error("deleted key should be absent")
	}
}

func TestTagSetOrderPreserved(t *testing.T) {
	var s TagSet
	s.Set(Album, "A")
	s.Set(Artist, "B")
	s.Set(Title, "C")
	want := []Key{Album, Artist, Title}
	if got := s.Keys(); !slices.Equal(got, want) {
		t.Errorf("Keys order = %v, want %v", got, want)
	}
	// Re-setting an existing key keeps its position.
	s.Set(Album, "A2")
	if got := s.Keys(); !slices.Equal(got, want) {
		t.Errorf("after re-set, Keys order = %v, want %v", got, want)
	}
}

func TestTagSetCloneIsolation(t *testing.T) {
	var s TagSet
	s.Set(Artist, "X", "Y")
	c := s.Clone()
	c.Add(Artist, "Z")
	c.Set(Title, "new")
	if v, _ := s.Get(Artist); len(v) != 2 {
		t.Errorf("clone mutation leaked into original: Artist = %v", v)
	}
	if s.Has(Title) {
		t.Error("clone mutation added a key to original")
	}
}

func TestTagPatchApply(t *testing.T) {
	base := NewTagSet()
	base.Set(Title, "Old")
	base.Set(Artist, "Keep")
	base.Set(Genre, "Rock")

	var p TagPatch
	p.Set(Title, "New").Clear(Genre).Add(Artist, "Second")

	got := p.Apply(base)

	if v, _ := got.First(Title); v != "New" {
		t.Errorf("Title = %q, want New", v)
	}
	if got.Has(Genre) {
		t.Error("Genre should be cleared")
	}
	if v, _ := got.Get(Artist); !slices.Equal(v, []string{"Keep", "Second"}) {
		t.Errorf("Artist = %v, want [Keep Second]", v)
	}
	// base is untouched.
	if v, _ := base.First(Title); v != "Old" {
		t.Errorf("base mutated: Title = %q", v)
	}
}

func TestTagPatchLastWins(t *testing.T) {
	var p TagPatch
	p.Set(Title, "first").Set(Title, "second")
	got := p.Apply(NewTagSet())
	if v, _ := got.First(Title); v != "second" {
		t.Errorf("Title = %q, want second (last op wins)", v)
	}
}

func TestProjectAndPatchRoundTrip(t *testing.T) {
	in := Tags{
		Title:       "T",
		Artists:     []string{"A1", "A2"},
		Album:       "Alb",
		TrackNumber: 3,
		TrackTotal:  12,
		Genres:      []string{"Jazz"},
		Compilation: true,
		MusicBrainz: MusicBrainzIDs{RecordingID: "mbid-123"},
	}
	ts := in.Patch().Apply(NewTagSet())
	out := Project(ts)

	if out.Title != in.Title || out.Album != in.Album {
		t.Errorf("scalar mismatch: %+v", out)
	}
	if !slices.Equal(out.Artists, in.Artists) {
		t.Errorf("Artists = %v, want %v", out.Artists, in.Artists)
	}
	if out.TrackNumber != 3 || out.TrackTotal != 12 {
		t.Errorf("track = %d/%d, want 3/12", out.TrackNumber, out.TrackTotal)
	}
	if !out.Compilation {
		t.Error("Compilation lost")
	}
	if out.MusicBrainz.RecordingID != "mbid-123" {
		t.Errorf("MB RecordingID = %q", out.MusicBrainz.RecordingID)
	}
}

func TestParseNumPairSlashConvention(t *testing.T) {
	n, tot := ParseNumPair("3/12", "")
	if n != 3 || tot != 12 {
		t.Errorf("got %d/%d, want 3/12", n, tot)
	}
	// Explicit total field wins.
	n, tot = ParseNumPair("3", "20")
	if n != 3 || tot != 20 {
		t.Errorf("got %d/%d, want 3/20", n, tot)
	}
}

func TestValidNumericValue(t *testing.T) {
	// Valid: plain ints, ParseNumPair-tolerant whitespace, leading sign, and the
	// "n/total" convention on the pair keys.
	valid := []struct {
		k Key
		v string
	}{
		{TrackNumber, "3"}, {TrackNumber, " 3 "}, {TrackNumber, "3/4"}, {TrackNumber, "-1"},
		{DiscNumber, "1/2"}, {TrackTotal, "12"}, {PlayCount, "0"},
		// An empty pair-side round-trips through ParseNumPair (to 0), so it is not flagged.
		{TrackNumber, "3/"}, {DiscNumber, "/2"},
		{Title, "not-a-number"}, // a non-numeric key is never flagged
	}
	for _, c := range valid {
		if !ValidNumericValue(c.k, c.v) {
			t.Errorf("ValidNumericValue(%s, %q) = false, want true", c.k, c.v)
		}
	}
	// Malformed: non-numeric, or "/" on a key that does not take it (a total).
	invalid := []struct {
		k Key
		v string
	}{
		{TrackNumber, "abc"}, {TrackNumber, "3/x"}, {TrackTotal, "3/4"}, {PlayCount, "lots"},
	}
	for _, c := range invalid {
		if ValidNumericValue(c.k, c.v) {
			t.Errorf("ValidNumericValue(%s, %q) = true, want false", c.k, c.v)
		}
	}
}

func TestValidPartialDate(t *testing.T) {
	for _, d := range []string{"2021", "2021-06", "2021-06-15", "2020-02-29"} {
		if !ValidPartialDate(d) {
			t.Errorf("ValidPartialDate(%q) = false, want true", d)
		}
	}
	// Calendar-invalid, non-zero-padded, and non-date values are rejected.
	for _, d := range []string{"banana", "2021-13-01", "2021-02-31", "2021-6-1", "2021-02-29"} {
		if ValidPartialDate(d) {
			t.Errorf("ValidPartialDate(%q) = true, want false", d)
		}
	}
}

func TestNumericAndDateKeySets(t *testing.T) {
	// Rating (free-form string) and MediaType (vocabulary-only) are not numeric.
	if IsNumericKey(Rating) || IsNumericKey(MediaType) {
		t.Error("Rating/MediaType must not be classified numeric")
	}
	if !IsNumericKey(PlayCount) || !IsNumericKey(TrackNumber) {
		t.Error("PlayCount/TrackNumber should be numeric")
	}
	// AcquisitionDate joins the date set alongside the recording/release/original dates.
	if !IsDateKey(AcquisitionDate) || !IsDateKey(RecordingDate) {
		t.Error("AcquisitionDate/RecordingDate should be date keys")
	}
	if IsDateKey(Title) {
		t.Error("Title is not a date key")
	}
}

func TestPerformersRoundTrip(t *testing.T) {
	in := map[string][]string{"guitar": {"Foo"}, "": {"Bar"}}
	formatted := formatPerformers(in)
	out := parsePerformers(formatted)
	if !slices.Equal(out["guitar"], []string{"Foo"}) {
		t.Errorf("guitar = %v", out["guitar"])
	}
	if !slices.Equal(out[""], []string{"Bar"}) {
		t.Errorf("unqualified = %v", out[""])
	}
}

func TestMergeStrategies(t *testing.T) {
	base := NewTagSet()
	base.Set(Title, "BaseTitle")
	base.Set(Artist, "") // present but empty
	base.Set(Genre, "Rock")

	incoming := NewTagSet()
	incoming.Set(Title, "IncTitle")
	incoming.Set(Artist, "IncArtist")
	incoming.Set(Album, "IncAlbum")

	t.Run("PreferIncoming", func(t *testing.T) {
		out, _ := Merge(base, incoming, PreferIncoming)
		if v, _ := out.First(Title); v != "IncTitle" {
			t.Errorf("Title = %q, want IncTitle", v)
		}
		if v, _ := out.First(Genre); v != "Rock" {
			t.Errorf("Genre = %q, want Rock (incoming absent)", v)
		}
	})
	t.Run("PreferBase", func(t *testing.T) {
		out, _ := Merge(base, incoming, PreferBase)
		if v, _ := out.First(Title); v != "BaseTitle" {
			t.Errorf("Title = %q, want BaseTitle", v)
		}
		if v, _ := out.First(Album); v != "IncAlbum" {
			t.Errorf("Album = %q, want IncAlbum (base absent)", v)
		}
	})
	t.Run("FillEmpty", func(t *testing.T) {
		out, _ := Merge(base, incoming, FillEmpty)
		if v, _ := out.First(Title); v != "BaseTitle" {
			t.Errorf("Title = %q, want BaseTitle (non-empty kept)", v)
		}
		if v, _ := out.First(Artist); v != "IncArtist" {
			t.Errorf("Artist = %q, want IncArtist (base was empty)", v)
		}
	})
	t.Run("Union", func(t *testing.T) {
		b := NewTagSet()
		b.Set(Genre, "Rock", "Pop")
		i := NewTagSet()
		i.Set(Genre, "pop", "Jazz") // "pop" duplicates "Pop" after normalization
		out, _ := Merge(b, i, Union)
		got, _ := out.Get(Genre)
		if !slices.Equal(got, []string{"Rock", "Pop", "Jazz"}) {
			t.Errorf("Union = %v, want [Rock Pop Jazz]", got)
		}
	})
}

func TestMergeProvenance(t *testing.T) {
	base := NewTagSet()
	base.Set(Title, "BaseTitle")
	incoming := NewTagSet()
	incoming.Set(Title, "IncTitle")

	_, prov := Merge(base, incoming, PreferIncoming)
	if len(prov) != 1 {
		t.Fatalf("got %d provenance entries, want 1", len(prov))
	}
	p := prov[0]
	if p.Source != "incoming" {
		t.Errorf("Source = %q, want incoming", p.Source)
	}
	if !slices.Equal(p.Selected, []string{"IncTitle"}) {
		t.Errorf("Selected = %v", p.Selected)
	}
	if !slices.Equal(p.Rejected, []string{"BaseTitle"}) {
		t.Errorf("Rejected = %v, want [BaseTitle]", p.Rejected)
	}
	if p.Reason == "" {
		t.Error("expected a non-empty reason")
	}
}
