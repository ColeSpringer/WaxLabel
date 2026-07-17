package tag

import (
	"errors"
	"maps"
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

func TestTagPatchTouches(t *testing.T) {
	var p TagPatch
	p.Set(Title, "x").Clear(Genre).Add(Artist, "y")
	for _, k := range []Key{Title, Genre, Artist} {
		if !p.Touches(k) {
			t.Errorf("Touches(%s) = false, want true (set/clear/add all count)", k)
		}
	}
	if p.Touches(Album) {
		t.Error("Touches(Album) = true, want false (untouched key)")
	}
	if (TagPatch{}).Touches(Title) {
		t.Error("an empty patch should touch nothing")
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
		Lyricists:   []string{"L1", "L2"},
		Producers:   []string{"P1", "P2"},
		Engineers:   []string{"E1"},
		Mixers:      []string{"M1", "M2"},
		Arrangers:   []string{"Ar1"},
		Writers:     []string{"W1", "W2"},
		DJMixers:    []string{"DJ1"},
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
	if !slices.Equal(out.Lyricists, in.Lyricists) {
		t.Errorf("Lyricists = %v, want %v (multivalued projection must survive)", out.Lyricists, in.Lyricists)
	}
	for _, c := range []struct {
		name      string
		got, want []string
	}{
		{"Producers", out.Producers, in.Producers},
		{"Engineers", out.Engineers, in.Engineers},
		{"Mixers", out.Mixers, in.Mixers},
		{"Arrangers", out.Arrangers, in.Arrangers},
		{"Writers", out.Writers, in.Writers},
		{"DJMixers", out.DJMixers, in.DJMixers},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s = %v, want %v (multivalued role projection must survive)", c.name, c.got, c.want)
		}
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

// TestProjectPlayCountErrorsToZero: the typed PlayCount follows ParseNumPair's convention -
// surrounding whitespace is trimmed, and every parse error (including int overflow) yields 0
// rather than strconv.Atoi's partial value - so a malformed raw PLAYCOUNT does not leak a
// half-parsed or garbage count into the projection. The raw bytes remain via TagSet.Get.
func TestProjectPlayCountErrorsToZero(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"42", 42},
		{"  7 ", 7},                 // surrounding whitespace trimmed
		{"99999999999999999999", 0}, // int overflow -> 0 (not a partial value)
		{"not-a-number", 0},         // non-numeric -> 0
		{"12x", 0},                  // trailing garbage -> 0 (whole-value parse)
		{"", 0},                     // absent -> 0
	}
	for _, c := range cases {
		ts := NewTagSet()
		if c.raw != "" {
			ts.Set(PlayCount, c.raw)
		}
		if got := Project(ts).PlayCount; got != c.want {
			t.Errorf("Project PLAYCOUNT %q = %d, want %d", c.raw, got, c.want)
		}
	}
}

// TestProjectNewAccessors: the audiobook/provenance accessors project from their canonical
// keys and round-trip through Patch (both sides of the mirror), so a Project -> Patch -> Apply
// keeps them rather than silently dropping them. MediaType is distinct from Media.
func TestProjectNewAccessors(t *testing.T) {
	ts := NewTagSet()
	ts.Set(Media, "CD") // the pre-existing release-medium field, distinct from MediaType
	ts.Set(MediaType, "2")
	ts.Set(Description, "short blurb")
	ts.Set(LongDescription, "the full description")
	ts.Set(Narrator, "A Reader")
	ts.Set(SourceURL, "https://example.com/x")
	ts.Set(SourceID, "abc-123")
	ts.Set(AcquisitionDate, "2026-01-02")
	ts.Set(EncodingHistory, "lame 3.100")

	fields := func(tg Tags) map[string]string {
		return map[string]string{
			"Media": tg.Media, "MediaType": tg.MediaType, "Description": tg.Description,
			"LongDescription": tg.LongDescription, "Narrator": tg.Narrator,
			"SourceURL": tg.SourceURL, "SourceID": tg.SourceID,
			"AcquisitionDate": tg.AcquisitionDate, "EncodingHistory": tg.EncodingHistory,
		}
	}
	want := map[string]string{
		"Media": "CD", "MediaType": "2", "Description": "short blurb",
		"LongDescription": "the full description", "Narrator": "A Reader",
		"SourceURL": "https://example.com/x", "SourceID": "abc-123",
		"AcquisitionDate": "2026-01-02", "EncodingHistory": "lame 3.100",
	}

	if got := fields(Project(ts)); !maps.Equal(got, want) {
		t.Errorf("Project accessors = %v, want %v", got, want)
	}
	// Both sides of the mirror: Project reads them AND Patch writes them, so a round-trip
	// preserves every one. A key populated on only one side would drop here.
	round := fields(Project(Project(ts).Patch().Apply(NewTagSet())))
	if !maps.Equal(round, want) {
		t.Errorf("Project -> Patch round-trip = %v, want %v", round, want)
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
	// Out-of-range overflow reads 0, not strconv.Atoi's MaxInt64 saturation, so the
	// typed projection matches the documented "non-numeric ... reads 0" contract.
	n, tot = ParseNumPair("99999999999999999999/88888888888888888888", "")
	if n != 0 || tot != 0 {
		t.Errorf("overflow got %d/%d, want 0/0", n, tot)
	}
	if n, tot = ParseNumPair("5", "99999999999999999999"); n != 5 || tot != 0 {
		t.Errorf("overflow total got %d/%d, want 5/0", n, tot)
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

func TestValidReplayGainValue(t *testing.T) {
	// Accept the conventional decimal gain spellings, with or without the dB unit. A
	// positive gain may carry an explicit '+' (the ReplayGain convention, e.g. "+2.34 dB").
	for _, v := range []string{"-6.5", "-6.5 dB", "-6.5dB", "0.0", "7.30", "12", "-3.2", "+2.34", "+2.34 dB"} {
		if !ValidReplayGainValue(ReplayGainTrackGain, v) {
			t.Errorf("ValidReplayGainValue(gain, %q) = false, want true", v)
		}
	}
	// Reject the spellings strconv.ParseFloat accepts but a ReplayGain figure never
	// uses: scientific, hex, underscored, multiple dots, a lone sign, and non-finite.
	for _, v := range []string{"1e3", "0x1p-2", "1_0.5", ".", "-", "+", "1.2.3", "nan", "inf", "-Inf"} {
		if ValidReplayGainValue(ReplayGainTrackGain, v) {
			t.Errorf("ValidReplayGainValue(gain, %q) = true, want false", v)
		}
	}
	// A peak is never signed: any leading '-' is rejected, including "-0.0" (which a bare
	// f < 0 check would have let through); a non-negative peak (with or without '+') is fine.
	for _, v := range []string{"-0.1", "-0.0"} {
		if ValidReplayGainValue(ReplayGainTrackPeak, v) {
			t.Errorf("a signed peak %q should be rejected", v)
		}
	}
	for _, v := range []string{"0.98", "+0.98"} {
		if !ValidReplayGainValue(ReplayGainTrackPeak, v) {
			t.Errorf("a non-negative peak %q should be valid", v)
		}
	}
	// A non-ReplayGain key is never flagged.
	if !ValidReplayGainValue(Title, "1e3") {
		t.Error("a non-ReplayGain key should never be flagged")
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
	in := []PerformerCredit{{Name: "Foo", Role: "guitar"}, {Name: "Bar"}}
	out := parsePerformers(formatPerformers(in))
	if len(out) != len(in) {
		t.Fatalf("round-trip length = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] { // PerformerCredit is comparable, so no reflect needed
			t.Errorf("performer %d = %+v, want %+v", i, out[i], in[i])
		}
	}
}

// TestPerformersPreserveOrder verifies that a multi-valued PERFORMER's order is significant and
// must survive Project -> Patch -> Apply unchanged. The old map-keyed-by-role projection
// re-sorted it.
func TestPerformersPreserveOrder(t *testing.T) {
	values := []string{"Zoe (vocals)", "Amy (guitar)", "Bob (drums)"}
	ts := NewTagSet()
	ts.Set(Performer, values...)

	got := Project(ts).Patch().Apply(NewTagSet())
	out, _ := got.Get(Performer)
	if !slices.Equal(out, values) {
		t.Errorf("PERFORMER order not preserved: got %v, want %v", out, values)
	}
}

// TestPerformersTrimSurroundingWhitespace: incidental whitespace around a PERFORMER
// value must not hide the "(role)" suffix (a trailing space before the value end) nor
// stick to the parsed name. The native bytes are preserved separately; this only cleans
// the typed projection.
func TestPerformersTrimSurroundingWhitespace(t *testing.T) {
	cases := []struct {
		value string
		want  PerformerCredit
	}{
		{"John Doe (vocals) ", PerformerCredit{Name: "John Doe", Role: "vocals"}}, // trailing space still splits
		{"  Foo (bar)  ", PerformerCredit{Name: "Foo", Role: "bar"}},
		{" Solo Artist ", PerformerCredit{Name: "Solo Artist"}}, // padded bare name trimmed
	}
	for _, c := range cases {
		got := parsePerformers([]string{c.value})
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("parsePerformers(%q) = %+v, want [%+v]", c.value, got, c.want)
		}
	}
}

// TestPerformersParenthesizedValues verifies that a fully-parenthesized value has no name to
// split a role from, so it must be kept whole and re-emit verbatim rather than being
// mangled (the old splitter dropped the parentheses or split an empty name).
func TestPerformersParenthesizedValues(t *testing.T) {
	cases := []struct {
		value string
		want  PerformerCredit
	}{
		{"Foo (guitar)", PerformerCredit{Name: "Foo", Role: "guitar"}},
		{"(note)", PerformerCredit{Name: "(note)"}},
		{"()", PerformerCredit{Name: "()"}},
		{"Name ()", PerformerCredit{Name: "Name ()"}},
		{"Bare", PerformerCredit{Name: "Bare"}},
	}
	for _, c := range cases {
		got := parsePerformers([]string{c.value})
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("parsePerformers(%q) = %+v, want [%+v]", c.value, got, c.want)
			continue
		}
		if rt := formatPerformers(got); len(rt) != 1 || rt[0] != c.value {
			t.Errorf("round-trip %q -> %v (not verbatim)", c.value, rt)
		}
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
