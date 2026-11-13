package matroska

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// levelGroup is a group at a target level holding the named SimpleTags, each with a value.
func levelGroup(level uint64, names ...string) tagGroup {
	g := tagGroup{targetTypeValue: level}
	for _, n := range names {
		g.tags = append(g.tags, simple(n, n+" value"))
	}
	g.scope = resolveScope(g)
	return g
}

// A file is tagged by level when it holds a TITLE at album level, in a group naming no track,
// beside a TITLE at level 40 or under. Nothing else of the file has a say.
func TestTitleConvention(t *testing.T) {
	chapter := levelGroup(30, "TITLE")
	chapter.chapterUID = true
	chapter.scope = resolveScope(chapter)
	ofTrack := levelGroup(50, "TITLE")
	ofTrack.trackUID = true
	ofTrack.scope = resolveScope(ofTrack)
	ofEdition := levelGroup(50, "TITLE")
	ofEdition.editionUID = true
	ofEdition.scope = resolveScope(ofEdition)
	valueless := func(level uint64) tagGroup {
		g := tagGroup{targetTypeValue: level, tags: []simpleTag{{name: "TITLE"}}}
		g.scope = resolveScope(g)
		return g
	}
	translated := levelGroup(50, "TITLE")
	translated.tags[0].lang = "ger"
	for _, c := range []struct {
		name   string
		groups []tagGroup
		want   bool
	}{
		{"levels 50 and 30", []tagGroup{levelGroup(50, "TITLE"), levelGroup(30, "TITLE")}, true},
		{"levels 30 and 50", []tagGroup{levelGroup(30, "TITLE"), levelGroup(50, "TITLE")}, true},
		{"levels 50 and 40", []tagGroup{levelGroup(50, "TITLE"), levelGroup(40, "TITLE")}, true},
		{"levels 59 and 30", []tagGroup{levelGroup(59, "TITLE"), levelGroup(30, "TITLE")}, true},
		{"the default level and 30", []tagGroup{levelGroup(0, "TITLE"), levelGroup(30, "TITLE")}, true},
		{"a translated album", []tagGroup{translated, levelGroup(30, "TITLE")}, true},
		{"an ALBUM beside both levels", []tagGroup{levelGroup(50, "TITLE", "ALBUM"), levelGroup(30, "TITLE")}, true},
		// Only a TITLE at album level is an album's: one above it names no album.
		{"levels 70 and 30", []tagGroup{levelGroup(70, "TITLE"), levelGroup(30, "TITLE")}, false},
		{"levels 60 and 30", []tagGroup{levelGroup(60, "TITLE"), levelGroup(30, "TITLE")}, false},
		{"level 50 alone", []tagGroup{levelGroup(50, "TITLE")}, false},
		{"level 30 alone", []tagGroup{levelGroup(30, "TITLE")}, false},
		{"levels 50 and 45", []tagGroup{levelGroup(50, "TITLE"), levelGroup(45, "TITLE")}, false},
		{"levels 50 and 41", []tagGroup{levelGroup(50, "TITLE"), levelGroup(41, "TITLE")}, false},
		{"no TITLE at level 50", []tagGroup{levelGroup(50, "ARTIST"), levelGroup(30, "TITLE")}, false},
		{"no TITLE below", []tagGroup{levelGroup(50, "TITLE"), levelGroup(30, "ARTIST")}, false},
		{"the lower title is a chapter's", []tagGroup{levelGroup(50, "TITLE"), chapter}, false},
		{"the album-level title is a track's", []tagGroup{ofTrack, levelGroup(30, "TITLE")}, false},
		{"the album-level title is an edition's", []tagGroup{ofEdition, levelGroup(30, "TITLE")}, false},
		{"the lower TITLE holds no value", []tagGroup{levelGroup(50, "TITLE"), valueless(30)}, false},
		{"the album-level TITLE holds no value", []tagGroup{valueless(50), levelGroup(30, "TITLE")}, false},
		{"no Tags", nil, false},
	} {
		if got := titleConvention(c.groups); got != c.want {
			t.Errorf("%s: titleConvention = %v, want %v", c.name, got, c.want)
		}
	}
}

// The album-level TITLE and ARTIST of a file tagged by level read as the album's; at any other
// level, at track scope, and in any other file they read as themselves.
func TestTagKeyByLevel(t *testing.T) {
	ofTrack := levelGroup(50)
	ofTrack.trackUID = true
	ofTrack.scope = resolveScope(ofTrack)
	for _, c := range []struct {
		name       string
		g          tagGroup
		tagName    string
		convention bool
		want       tag.Key
	}{
		{"album TITLE", levelGroup(50), "TITLE", true, tag.Album},
		{"album ARTIST", levelGroup(50), "ARTIST", true, tag.AlbumArtist},
		{"album LEAD_PERFORMER", levelGroup(50), "LEAD_PERFORMER", true, tag.AlbumArtist},
		{"album GENRE", levelGroup(50), "GENRE", true, tag.Genre},
		{"album TITLE of another file", levelGroup(50), "TITLE", false, tag.Title},
		{"track TITLE", levelGroup(30), "TITLE", true, tag.Title},
		{"part TITLE", levelGroup(40), "TITLE", true, tag.Title},
		{"collection TITLE", levelGroup(70), "TITLE", true, tag.Title},
		{"album-level TITLE of a track", ofTrack, "TITLE", true, tag.Title},
	} {
		if got, ok := tagKey(simple(c.tagName, "v"), c.g, c.convention); !ok || got != c.want {
			t.Errorf("%s: tagKey = %s, %v; want %s", c.name, got, ok, c.want)
		}
	}
}

// The title and artist of a file tagged by level go to the first group at level 40 or under that
// holds a TITLE, never to a targeted one.
func TestTrackGroupIndex(t *testing.T) {
	chapter := levelGroup(30, "TITLE")
	chapter.chapterUID = true
	for _, c := range []struct {
		name   string
		groups []tagGroup
		idx    int
	}{
		{"the group with the TITLE", []tagGroup{levelGroup(50, "TITLE"), levelGroup(30, "ARTIST"), levelGroup(30, "TITLE")}, 2},
		{"the first of two", []tagGroup{levelGroup(30, "TITLE"), levelGroup(50, "TITLE"), levelGroup(40, "TITLE")}, 0},
		{"no TITLE below album level", []tagGroup{levelGroup(50, "TITLE"), levelGroup(40, "GENRE"), levelGroup(30, "ARTIST")}, -1},
		{"none at level 40 or under", []tagGroup{levelGroup(50, "TITLE"), levelGroup(45, "TITLE")}, -1},
		{"not a chapter's", []tagGroup{levelGroup(50, "TITLE"), chapter}, -1},
	} {
		if idx := trackGroupIndex(c.groups); idx != c.idx {
			t.Errorf("%s: trackGroupIndex = %d, want %d", c.name, idx, c.idx)
		}
	}
}

// What an edit decides for a file tagged by level: where the title and artist are written, and
// which album-level SimpleTags are written again when the edit ends the convention.
func TestEditDecisionsByLevel(t *testing.T) {
	groups := []tagGroup{levelGroup(50, "TITLE", "ARTIST"), levelGroup(30, "TITLE", "ARTIST")}
	base := tagSet(
		keyVals{tag.Album, []string{"TITLE value"}}, keyVals{tag.AlbumArtist, []string{"ARTIST value"}},
		keyVals{tag.Title, []string{"TITLE value"}}, keyVals{tag.Artist, []string{"ARTIST value"}})
	edit := func(key tag.Key, vals ...string) *editDecisions {
		edited := base.Clone()
		if len(vals) == 0 {
			edited.Delete(key)
		} else {
			edited.Set(key, vals...)
		}
		return computeEditDecisions(groups, 0, tagsOnly(base), tagsOnly(edited))
	}
	dropped := func(ed *editDecisions) [][2]int {
		var out [][2]int
		for at := range ed.drop {
			out = append(out, at)
		}
		slices.SortFunc(out, func(a, b [2]int) int { return (a[0]-b[0])*2 + a[1] - b[1] })
		return out
	}

	ed := edit(tag.Title, "New")
	if !ed.conv || !ed.convAfter || ed.trackIdx != 1 || ed.writesInfoTitle(false) || !ed.writesInfoTitle(true) {
		t.Errorf("title edit: conv %v, after %v, track group %d, writes Info.Title %v with none, %v with one",
			ed.conv, ed.convAfter, ed.trackIdx, ed.writesInfoTitle(false), ed.writesInfoTitle(true))
	}
	if got := dropped(ed); !slices.Equal(got, [][2]int{{1, 0}}) {
		t.Errorf("title edit drops %v, want the track-level TITLE alone", got)
	}
	if want := []trackValue{{key: tag.Title, value: "New"}}; !slices.Equal(ed.trackEmit, want) || !ed.writesTo(1) || ed.writesTo(0) {
		t.Errorf("title edit writes %v at track level, want %v", ed.trackEmit, want)
	}

	ed = edit(tag.Artist, "One", "ARTIST value")
	if got := dropped(ed); !slices.Equal(got, [][2]int{{1, 1}}) {
		t.Errorf("artist edit drops %v, want the track-level ARTIST: none keeps a value in place", got)
	}
	if want := []trackValue{{key: tag.Artist, value: "One"}, {key: tag.Artist, value: "ARTIST value"}}; !slices.Equal(ed.trackEmit, want) {
		t.Errorf("artist edit writes %v at track level, want %v", ed.trackEmit, want)
	}

	ed = edit(tag.Title)
	if ed.convAfter || len(ed.trackEmit) != 0 {
		t.Errorf("cleared title: convention after %v, track-level writes %v; want neither", ed.convAfter, ed.trackEmit)
	}
	if got := dropped(ed); !slices.Equal(got, [][2]int{{0, 0}, {0, 1}, {1, 0}}) {
		t.Errorf("cleared title drops %v, want the title and both album-level SimpleTags", got)
	}
	if !ed.edited(tag.Album) || !ed.edited(tag.AlbumArtist) || ed.edited(tag.Artist) {
		t.Errorf("cleared title writes again %v, want the album and its artist", ed.ek)
	}

	ed = edit(tag.Album)
	if ed.convAfter || !ed.edited(tag.AlbumArtist) || ed.edited(tag.Title) {
		t.Errorf("cleared album: convention after %v, written again %v; want the album artist alone", ed.convAfter, ed.ek)
	}

	// A file that is not tagged by level writes the Info.Title whether or not it holds one, and
	// nothing at track level.
	plain := computeEditDecisions(groups[:1], 0, tagsOnly(base), tagsOnly(tagSet(keyVals{tag.Title, []string{"New"}}, keyVals{tag.Artist, []string{"One"}})))
	if plain.conv || plain.convAfter || !plain.writesInfoTitle(false) || plain.trackIdx != -1 || len(plain.trackEmit) != 0 {
		t.Errorf("album-level tags alone: conv %v, after %v, writes Info.Title %v, track group %d, track-level writes %v",
			plain.conv, plain.convAfter, plain.writesInfoTitle(false), plain.trackIdx, plain.trackEmit)
	}
	if unchanged := computeEditDecisions(groups[:1], 0, tagsOnly(base), tagsOnly(base)); unchanged.rewritesTags() {
		t.Error("an edit of nothing rewrites the Tags")
	}
}

// A Targets naming an attachment and a chapter both is the attachment's, as in ffmpeg.
func TestResolveScopeOrder(t *testing.T) {
	for _, c := range []struct {
		g    tagGroup
		want core.Scope
	}{
		{tagGroup{attachUID: true, chapterUID: true, editionUID: true}, core.ScopeAttachment},
		{tagGroup{chapterUID: true, editionUID: true}, core.ScopeChapter},
		{tagGroup{editionUID: true, trackUID: true}, core.ScopeEdition},
		{tagGroup{trackUID: true}, core.ScopeTrack},
		{tagGroup{}, core.ScopeAlbum},
	} {
		if got := resolveScope(c.g); got != c.want {
			t.Errorf("resolveScope(%+v) = %v, want %v", c.g, got, c.want)
		}
	}
}

// A dropped SimpleTag stands for a value the edit writes only where it holds that value and no
// other: one that splits into a number and a total holds two.
func TestHoldsJust(t *testing.T) {
	g := levelGroup(50)
	ed := &editDecisions{}
	for _, c := range []struct {
		st    simpleTag
		key   tag.Key
		value string
		want  bool
	}{
		{simple("ARTIST", "A"), tag.Artist, "A", true},
		{simple("ARTIST", "A"), tag.Artist, "B", false},
		{simple("ARTIST", "A"), tag.Composer, "A", false},
		{simple("PART_NUMBER", "3"), tag.TrackNumber, "3", true},
		{simple("PART_NUMBER", "3/12"), tag.TrackNumber, "3", false},
		{simpleTag{name: "ARTIST", value: "A", hasValue: true}, tag.Artist, "A", false}, // no bytes to write again
	} {
		if got := ed.holdsJust(c.st, g, c.key, c.value, ""); got != c.want {
			t.Errorf("holdsJust(%s=%q, %s, %q) = %v, want %v", c.st.name, c.st.value, c.key, c.value, got, c.want)
		}
	}
}
