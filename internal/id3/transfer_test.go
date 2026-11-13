package id3

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

func mediaOf(kv ...string) *core.Media {
	m := &core.Media{Format: core.FormatFLAC, Tags: tag.NewTagSet()}
	for i := 0; i+1 < len(kv); i += 2 {
		m.Tags.Add(tag.Key(kv[i]), kv[i+1])
	}
	core.SyncTextItems(m)
	return m
}

// gradeRendered grades key of src as a transfer of every field of src onto an empty tag stores it.
func gradeRendered(src *core.Media, key tag.Key, version byte, opts WriteOpts) (core.Disposition, string) {
	return TransferRendering(version, opts).Grade(src, src.Tags.Keys())(key)
}

// The rendering grades a field by what a tag written from the source reads back as, so a loss
// the frame writer makes is one the grade states.
func TestTransferRenderingGradesWhatTheWriterStores(t *testing.T) {
	const joined = `the values are stored joined as "AC/DC / Queen"`
	const first = "only the first of 2 values is stored"
	for _, c := range []struct {
		name    string
		version byte
		pol     core.ID3MultiValuePolicy
		src     *core.Media
		key     tag.Key
		want    core.Disposition
		reason  string
	}{
		{"a value in a frame of its own", 3, 0, mediaOf("TITLE", "T"), "TITLE", core.Carried, ""},
		{"two dates in one date frame", 3, 0, mediaOf("RECORDINGDATE", "2023", "RECORDINGDATE", "2022"), "RECORDINGDATE", core.Lossy, first},
		{"two dates in v2.4", 4, 0, mediaOf("RECORDINGDATE", "2023", "RECORDINGDATE", "2022"), "RECORDINGDATE", core.Lossy, first},
		{"two totals", 3, 0, mediaOf("TRACKNUMBER", "3", "TRACKTOTAL", "12", "TRACKTOTAL", "13"), "TRACKTOTAL", core.Lossy, first},
		{"two recording ids", 4, 0, mediaOf("MUSICBRAINZ_TRACKID", "a", "MUSICBRAINZ_TRACKID", "b"), "MUSICBRAINZ_TRACKID", core.Lossy, first},
		{"a v2.3 release date is a TXXX", 3, 0, mediaOf("RELEASEDATE", "2001", "RELEASEDATE", "2002"), "RELEASEDATE", core.Carried, ""},
		{"a v2.4 release date is one TDRL", 4, 0, mediaOf("RELEASEDATE", "2001", "RELEASEDATE", "2002"), "RELEASEDATE", core.Lossy, first},
		{"two lyrics under one description", 3, 0, mediaOf("LYRICS", "a", "LYRICS", "b"), "LYRICS", core.Lossy, first},
		{"two artists", 3, 0, mediaOf("ARTIST", "AC/DC", "ARTIST", "Queen"), "ARTIST", core.Carried, ""},
		{"two artists joined", 3, core.ID3MultiSlash, mediaOf("ARTIST", "AC/DC", "ARTIST", "Queen"), "ARTIST", core.Lossy, joined},
		{"v2.4 does not join", 4, core.ID3MultiSlash, mediaOf("ARTIST", "AC/DC", "ARTIST", "Queen"), "ARTIST", core.Carried, ""},
		{"an empty value at the end", 3, 0, mediaOf("GENRE", "X", "GENRE", ""), "GENRE", core.Lossy, first},
		{"a custom key in a TXXX", 3, 0, mediaOf("TSRC", "USRC17607839"), "TSRC", core.Carried, ""},
		{"a custom key read as another", 3, 0, mediaOf("TCMP", "1"), "TCMP", core.Lossy, "stored as COMPILATION"},
		{"a total beside a number that is none", 3, 0, mediaOf("TRACKNUMBER", "A", "TRACKTOTAL", "12"), "TRACKTOTAL", core.Dropped,
			"the number it attaches to is non-numeric, so ID3 cannot store this total (a total is written only as the second half of \"number/total\")"},
		// The movement pair reads back with a total of the same value: that is the movement's.
		{"a total that cannot join, beside a movement pair with that total", 4, 0, mediaOf("TRACKNUMBER", "A", "TRACKTOTAL", "12", "MOVEMENT", "3/12"), "TRACKTOTAL", core.Dropped,
			"the number it attaches to is non-numeric, so ID3 cannot store this total (a total is written only as the second half of \"number/total\")"},
		// A token is rendered as a transfer writes it, without the spaces around it.
		{"a v2.3 date with spaces around it", 3, 0, mediaOf("RECORDINGDATE", " 2001 "), "RECORDINGDATE", core.Carried, ""},
		{"a v2.3 date with no year", 3, 0, mediaOf("RECORDINGDATE", "soon"), "RECORDINGDATE", core.Dropped, "the destination does not store this value"},
		{"a movement spelled as a pair", 4, 0, mediaOf("MOVEMENT", "3/12"), "MOVEMENT", core.Lossy,
			"stored in the MVIN \"number/total\" frame, which reads back split into MOVEMENT and MOVEMENTTOTAL"},
		{"a movement total beside a number that is none", 4, 0, mediaOf("MOVEMENT", "x", "MOVEMENTTOTAL", "5"), "MOVEMENTTOTAL", core.Dropped,
			"it cannot join the movement number in a valid \"number/total\" MVIN frame, so ID3 cannot store this total"},
	} {
		if d, reason := gradeRendered(c.src, c.key, c.version, WriteOpts{Multi: c.pol}); d != c.want || reason != c.reason {
			t.Errorf("%s: %s %q, want %s %q", c.name, d, reason, c.want, c.reason)
		}
	}
}

// A field the writer cannot store is not written by a transfer, so the fields beside it are
// graded as a tag without it reads back: a custom TCMP beside COMPILATION would read back as a
// second COMPILATION value, and is left out instead.
func TestTransferRenderingGradesWithoutTheFieldsItDrops(t *testing.T) {
	src := mediaOf("COMPILATION", "0", "TCMP", "1", "TRACKNUMBER", "A", "TRACKTOTAL", "12", "TITLE", "T")
	for _, c := range []struct {
		key    tag.Key
		want   core.Disposition
		reason string
	}{
		{"COMPILATION", core.Carried, ""},
		{"TCMP", core.Dropped, "the format reads it back as COMPILATION, which the source holds too"},
		{"TRACKNUMBER", core.Carried, ""},
		{"TITLE", core.Carried, ""},
	} {
		if d, reason := gradeRendered(src, c.key, 3, WriteOpts{}); d != c.want || reason != c.reason {
			t.Errorf("%s: %s %q, want %s %q", c.key, d, reason, c.want, c.reason)
		}
	}
	// A key the transfer leaves out is no place another one went: with COMPILATION not written,
	// TCMP is stored, under the name it reads back as.
	grade := TransferRendering(3, WriteOpts{}).Grade(src, []tag.Key{"TCMP", tag.Title})
	if d, reason := grade("TCMP"); d != core.Lossy || reason != "stored as COMPILATION" {
		t.Errorf("TCMP without COMPILATION: %s %q, want lossy, stored as COMPILATION", d, reason)
	}
}

// The tag a transfer writes holds the comment and lyric items of the source, so a language the
// frame cuts to three bytes is graded.
func TestTransferRenderingGradesQualifiers(t *testing.T) {
	src := mediaOf("COMMENT", "c", "TITLE", "T")
	src.Comments = []core.TextItem{{Text: "c", Language: "en-us"}}
	if d, reason := gradeRendered(src, tag.Comment, 4, WriteOpts{}); d != core.Lossy || reason != `language "en-us" reads back as "en-"` {
		t.Errorf("COMMENT in en-us: %s %q, want lossy for the cut language", d, reason)
	}
	src = mediaOf("COMMENT", "c")
	src.Comments = []core.TextItem{{Text: "c", Description: "d", Language: "deu"}}
	if d, reason := gradeRendered(src, tag.Comment, 4, WriteOpts{}); d != core.Carried {
		t.Errorf("COMMENT described in deu: %s %q, want carried", d, reason)
	}
}

// A synced lyrics set's language is stored in the frame's three bytes, read in lowercase, with
// none for the spec's "xxx".
func TestSyncedLyricsCapabilityGradesTheStoredLanguage(t *testing.T) {
	set := func(language string) *core.Media {
		return &core.Media{Format: core.FormatFLAC, Tags: tag.NewTagSet(), SyncedLyrics: []core.SyncedLyrics{
			{Language: language, Lines: []core.SyncedLine{{Time: time.Second, Text: "la"}}},
		}}
	}
	full := core.Capability{Read: core.AccessFull, Write: core.AccessFull}
	caps := core.NewCapabilities(core.FormatMP3, false, full, full, full, core.AccessNone, nil).WithSyncedLyrics(SyncedLyricsCapability())
	for _, c := range []struct {
		language string
		want     core.Disposition
		reason   string
	}{
		{"", core.Carried, ""},
		{"deu", core.Carried, ""},
		{"english", core.Lossy, `language "english" reads back as "eng"`},
		{"ENG", core.Lossy, `language "ENG" reads back as "eng"`},
		{"xxx", core.Lossy, `language "xxx" reads back as ""`},
		{"de", core.Carried, ""},
	} {
		items := core.ProjectTransferOnto(set(c.language), nil, caps).Items
		if len(items) != 1 || items[0].Disposition != c.want || items[0].Reason != c.reason {
			t.Errorf("language %q: %+v, want %s %q", c.language, items, c.want, c.reason)
		}
	}
}

// A rebuild under the slash policy names the keys whose values it joined into one.
func TestRebuildReportsJoinedValues(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.Artist, "AC/DC", "Queen")
	edited.Set(tag.Title, "One")
	edited.Set(tag.Comment, "c1", "c2")
	edited.Set("MYKEY", "a", "b")
	// One value of two in a frame that holds one is not a join.
	edited.Set(tag.RecordingDate, "2001", "2002")
	for _, c := range []struct {
		version byte
		pol     core.ID3MultiValuePolicy
		want    []tag.Key
	}{
		{3, core.ID3MultiSlash, []tag.Key{tag.Artist, tag.Comment, "MYKEY"}},
		{4, core.ID3MultiSlash, nil},
		{3, core.ID3MultiNullSep, nil},
		{3, core.ID3MultiRepeatFrame, nil},
	} {
		_, info := RebuildFrames(nil, tag.NewTagSet(), edited, c.version, StructuredEdit{}, WriteOpts{Multi: c.pol})
		if !slices.Equal(info.JoinedValues, c.want) {
			t.Errorf("v2.%d policy %v: JoinedValues = %v, want %v", c.version, c.pol, info.JoinedValues, c.want)
		}
	}
	// A key the edit leaves alone is not reported again.
	base := edited.Clone()
	edited.Set(tag.Title, "Two")
	if _, info := RebuildFrames(nil, base, edited, 3, StructuredEdit{}, WriteOpts{Multi: core.ID3MultiSlash}); len(info.JoinedValues) != 0 {
		t.Errorf("an edit of TITLE alone reports %v joined", info.JoinedValues)
	}
	ws := AppendRebuildWarnings(nil, RebuildInfo{JoinedValues: []tag.Key{tag.Artist}}, tag.NewTagSet())
	if len(ws) != 1 || ws[0].Code != core.WarnValueDropped || !slices.Equal(ws[0].Keys, []tag.Key{tag.Artist}) {
		t.Errorf("warnings = %+v, want one value-dropped keyed ARTIST", ws)
	}
}

// Grading a source is linear in its fields: one rendering serves every key, where a rendering
// per key would be quadratic. Measured here: 8,000 custom keys grade in about 60 ms; a rendering
// per key would take over a minute. The bound is far from both.
func TestTransferRenderingScalesLinearly(t *testing.T) {
	src := &core.Media{Format: core.FormatFLAC, Tags: tag.NewTagSet()}
	for i := range 8000 {
		src.Tags.Set(tag.Key(fmt.Sprintf("CUSTOM%05d", i)), "value")
	}
	start := time.Now()
	grade := TransferRendering(4, WriteOpts{}).Grade(src, src.Tags.Keys())
	for k := range src.Tags.All() {
		if d, reason := grade(k); d != core.Carried {
			t.Fatalf("%s: %s %q", k, d, reason)
		}
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("grading 8,000 keys took %s, want it linear in the keys", took)
	}
}
