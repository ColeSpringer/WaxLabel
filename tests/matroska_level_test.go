package waxlabel_test

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// mkLevelTag is a Tag at a target level, and the bytes of its Targets and SimpleTags.
func mkLevelTag(level uint64, simples ...[]byte) (tagEl, body []byte) {
	body = concat(testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, level)), concat(simples...))
	return testfile.El(testfile.IDTag, body), body
}

// levelTagged is tagged by level, as the Matroska tagging guide has it: the album's title and
// artist at level 50, the track's at level 30, and no Info.Title.
func levelTagged() (src, albumBody, track []byte) {
	album, albumBody := mkLevelTag(50,
		testfile.SimpleTag("TITLE", "The Album"), testfile.SimpleTag("ARTIST", "Album Artist"), testfile.SimpleTag("DATE_RELEASED", "1999"))
	track, _ = mkLevelTag(30,
		testfile.SimpleTag("TITLE", "Track Title"), testfile.SimpleTag("ARTIST", "Track Artist"), testfile.SimpleTag("PART_NUMBER", "3"))
	return matroskaNoTitle(testfile.El(testfile.IDTags, album, track)), albumBody, track
}

// tagMap is the keys of a document that hold a value, with their values.
func tagMap(doc *wl.Document) map[tag.Key][]string {
	out := map[tag.Key][]string{}
	for _, k := range doc.Tags().Keys() {
		out[k], _ = doc.Tags().Get(k)
	}
	return out
}

func assertTags(t *testing.T, doc *wl.Document, want map[tag.Key][]string) {
	t.Helper()
	assertTagsOf(t, "file", doc, want)
}

// assertTagsOf is assertTags for the file a table case names.
func assertTagsOf(t *testing.T, name string, doc *wl.Document, want map[tag.Key][]string) {
	t.Helper()
	if got := tagMap(doc); !maps.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Errorf("%s: tags = %v, want %v", name, got, want)
	}
	if w, ok := docWarning(doc, wl.WarnConflictingFamilies); ok {
		t.Errorf("%s: read warns of a conflict: %s", name, w.Message)
	}
}

var levelTaggedTags = map[tag.Key][]string{
	tag.Album:       {"The Album"},
	tag.AlbumArtist: {"Album Artist"},
	tag.ReleaseDate: {"1999"},
	tag.Title:       {"Track Title"},
	tag.Artist:      {"Track Artist"},
	tag.TrackNumber: {"3"},
}

// with returns a copy of tags with key set to vals, or without it when there are none.
func with(tags map[tag.Key][]string, key tag.Key, vals ...string) map[tag.Key][]string {
	out := maps.Clone(tags)
	if len(vals) == 0 {
		delete(out, key)
	} else {
		out[key] = vals
	}
	return out
}

// In a file that holds a title at album level and another below it, the album-level TITLE and
// ARTIST are the album's and the lower ones the track's.
func TestMatroskaLevelTaggedFileReadsAlbumAndTrack(t *testing.T) {
	src, _, _ := levelTagged()
	assertTags(t, mustParseBytes(t, src), levelTaggedTags)
}

// A file with album-level tags alone reads as before: its TITLE and ARTIST are the file's.
func TestMatroskaAlbumLevelTagsAloneReadAsTitleAndArtist(t *testing.T) {
	album, _ := mkLevelTag(50, testfile.SimpleTag("TITLE", "Song"), testfile.SimpleTag("ARTIST", "Band"), testfile.SimpleTag("ALBUM", "Record"))
	src := matroskaNoTitle(testfile.El(testfile.IDTags, album))
	assertTags(t, mustParseBytes(t, src), map[tag.Key][]string{
		tag.Title: {"Song"}, tag.Artist: {"Band"}, tag.Album: {"Record"},
	})
}

// Each key is written where the file keeps it: the title and artist at track level, the album
// and its artist as the album-level TITLE and ARTIST. The other Tag is left as it is.
func TestMatroskaLevelTaggedEdits(t *testing.T) {
	src, albumBody, track := levelTagged()
	for _, c := range []struct {
		name      string
		edit      func(*wl.Editor) *wl.Editor
		want      map[tag.Key][]string
		keeps     []byte   // bytes the output still holds
		lacks     []string // names the output holds nowhere
		titleTags int      // TITLE SimpleTags in the output
	}{
		{"title", func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "New") },
			with(levelTaggedTags, tag.Title, "New"), albumBody, []string{"ALBUM"}, 2},
		{"artist", func(e *wl.Editor) *wl.Editor { return e.Set(tag.Artist, "One", "Two") },
			with(levelTaggedTags, tag.Artist, "One", "Two"), albumBody, []string{"ALBUM"}, 2},
		{"album", func(e *wl.Editor) *wl.Editor { return e.Set(tag.Album, "X") },
			with(levelTaggedTags, tag.Album, "X"), track, []string{"ALBUM"}, 2},
		{"album added", func(e *wl.Editor) *wl.Editor { return e.Add(tag.Album, "Second Album") },
			with(levelTaggedTags, tag.Album, "The Album", "Second Album"), track, []string{"ALBUM"}, 3},
		{"album artist", func(e *wl.Editor) *wl.Editor { return e.Set(tag.AlbumArtist, "Y") },
			with(levelTaggedTags, tag.AlbumArtist, "Y"), track, []string{"ALBUM"}, 2},
		{"genre", func(e *wl.Editor) *wl.Editor { return e.Set(tag.Genre, "Rock") },
			with(levelTaggedTags, tag.Genre, "Rock"), track, []string{"ALBUM"}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, _ := saveMatroska(t, src, c.edit(mustParseBytes(t, src).Edit()))
			assertTags(t, mustParseBytes(t, out), c.want)
			if !bytes.Contains(out, c.keeps) {
				t.Error("the Tag the edit is not of is not carried byte for byte")
			}
			for _, name := range c.lacks {
				if bytes.Contains(out, []byte(name)) {
					t.Errorf("the output holds a SimpleTag named %s", name)
				}
			}
			if n := bytes.Count(out, []byte("TITLE")); n != c.titleTags {
				t.Errorf("TITLE SimpleTags = %d, want %d", n, c.titleTags)
			}
			for _, f := range mustParseBytes(t, out).Families() {
				if f.Key == tag.Title && f.Scope != wl.ScopeTrack {
					t.Errorf("the title is stored at %v scope %q, want it at track level alone", f.Scope, f.Values)
				}
			}
		})
	}
}

// An edit that leaves the file without a lower title, or without an album-level TITLE, respells
// what stays at album level, so every other key reads as it did.
func TestMatroskaLevelTaggedEditThatEndsTheConvention(t *testing.T) {
	src, _, _ := levelTagged()
	for _, c := range []struct {
		name string
		edit func(*wl.Editor) *wl.Editor
		want map[tag.Key][]string
	}{
		{"clear title", func(e *wl.Editor) *wl.Editor { return e.Clear(tag.Title) }, with(levelTaggedTags, tag.Title)},
		{"clear album", func(e *wl.Editor) *wl.Editor { return e.Clear(tag.Album) }, with(levelTaggedTags, tag.Album)},
		{"clear both", func(e *wl.Editor) *wl.Editor { return e.Clear(tag.Title).Clear(tag.Album) },
			with(with(levelTaggedTags, tag.Title), tag.Album)},
		{"clear title, set album", func(e *wl.Editor) *wl.Editor { return e.Clear(tag.Title).Set(tag.Album, "X") },
			with(with(levelTaggedTags, tag.Title), tag.Album, "X")},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, _ := saveMatroska(t, src, c.edit(mustParseBytes(t, src).Edit()))
			assertTags(t, mustParseBytes(t, out), c.want)
		})
	}

	// The file is like any other once the convention ends: an artist value the edit keeps stays
	// in the SimpleTag that holds it, and the new one is written at album scope.
	kept := testfile.SimpleTag("ARTIST", "Track Artist")
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().Clear(tag.Title).Set(tag.Artist, "Track Artist", "New"))
	if !bytes.Contains(out, kept) {
		t.Error("the SimpleTag of the artist the edit keeps is not carried byte for byte")
	}
	if got := tagValues(mustParseBytes(t, out), tag.Artist); !slices.Equal(got, []string{"New", "Track Artist"}) {
		t.Errorf("ARTIST = %q, want New at album scope ahead of Track Artist at track scope", got)
	}
}

// A SimpleTag named by an alias spelling reads under the key the alias names, as in every other
// format, so no key the editor cannot address reaches the tags.
func TestMatroskaAliasNamedSimpleTags(t *testing.T) {
	album, _ := mkLevelTag(50, testfile.SimpleTag("YEAR", "1999"), testfile.SimpleTag("USLT", "la la"),
		testfile.SimpleTag("TOTALTRACKS", "12"), testfile.SimpleTag("ORGANIZATION", "Some Label"))
	doc := mustParseBytes(t, testfile.Matroska("matroska", "x", testfile.El(testfile.IDTags, album)))
	assertTags(t, doc, map[tag.Key][]string{
		tag.Title: {"x"}, tag.RecordingDate: {"1999"}, tag.Lyrics: {"la la"}, tag.TrackTotal: {"12"}, tag.Label: {"Some Label"},
	})
}

// Whatever the file's layout of levels, an edit of one key changes that key and no other: the
// keys read back from the output as the edit asked, and the same edit of the output is a no-op.
func TestMatroskaLevelEditMatrix(t *testing.T) {
	simple := testfile.SimpleTag
	album := func(level uint64) []byte {
		el, _ := mkLevelTag(level, simple("TITLE", "The Album"), simple("ARTIST", "Album Artist"), simple("GENRE", "Jazz"))
		return el
	}
	track, _ := mkLevelTag(30, simple("TITLE", "Track Title"), simple("ARTIST", "Track Artist"))
	trackUID := testfile.El(testfile.IDTag,
		testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, 30), testfile.Uint(testfile.IDTagTrackUID, 7)),
		simple("TITLE", "Track Title"), simple("ARTIST", "Track Artist"))
	collection, _ := mkLevelTag(70, simple("TITLE", "The Box"))
	albumArtistOnly, _ := mkLevelTag(50, simple("ARTIST", "Album Artist"))
	part, _ := mkLevelTag(40, simple("TITLE", "Track Title"))
	named, _ := mkLevelTag(50, simple("TITLE", "The Album"), simple("ALBUM", "Named Album"), simple("ARTIST", "Album Artist"))
	namedArtist, _ := mkLevelTag(50, simple("TITLE", "Other Title"), simple("ARTIST", "Band"), simple("ALBUM_ARTIST", "Various"))
	collectionArtist, _ := mkLevelTag(70, simple("TITLE", "The Box"), simple("ARTIST", "Box Artist"))
	translated, _ := mkLevelTag(50, mkLangSimple("TITLE", "eng", "Track Title"), mkLangSimple("TITLE", "ger", "Titel"), simple("ARTIST", "Album Artist"))
	tags := func(groups ...[]byte) []byte { return testfile.El(testfile.IDTags, groups...) }

	files := map[string][]byte{
		"levels 50 and 30":                      matroskaNoTitle(tags(album(50), track)),
		"track Tag first":                       matroskaNoTitle(tags(track, album(50))),
		"Info.Title and level 50":               testfile.Matroska("matroska", "Track Title", tags(album(50))),
		"Info.Title, levels 50 and 30":          testfile.Matroska("matroska", "Track Title", tags(album(50), track)),
		"track Tag names a track":               matroskaNoTitle(tags(album(50), trackUID)),
		"levels 50 and 40":                      matroskaNoTitle(tags(album(50), part)),
		"levels 70, 50 and 30":                  matroskaNoTitle(tags(collection, albumArtistOnly, track)),
		"a collection ahead of both levels":     matroskaNoTitle(tags(collection, album(50), track)),
		"Info.Title, a collection, both levels": testfile.Matroska("matroska", "Track Title", tags(collectionArtist, album(50), track)),
		"level 50 alone":                        matroskaNoTitle(tags(album(50))),
		"an ALBUM beside both levels":           matroskaNoTitle(tags(named, track)),
		"an ALBUM beside the Info.Title":        testfile.Matroska("matroska", "Track Title", tags(named)),
		"translated titles":                     testfile.Matroska("matroska", "Track Title", tags(translated)),
		"a translated album":                    matroskaNoTitle(tags(translated, track)),
		"level 40 ahead of level 50":            matroskaNoTitle(tags(part, album(50))),
		"levels 70 and 30":                      matroskaNoTitle(tags(collectionArtist, track)),
		"an ALBUM_ARTIST beside the Info.Title": testfile.Matroska("matroska", "Track Title", tags(namedArtist)),
		"Info.Title and a custom ALBUM":         testfile.Matroska("matroska", "Song", tags(testfile.TagGroup(0, simple("ALBUM", "Record"), simple("ARTIST", "Band")))),
	}
	type edit struct {
		key  tag.Key
		vals []string
	}
	edits := [][]edit{
		{{tag.Title, []string{"New"}}}, {{tag.Title, nil}}, {{tag.Title, []string{"New", "Second"}}}, {{tag.Title, []string{""}}},
		{{tag.Artist, []string{"New"}}}, {{tag.Artist, nil}}, {{tag.Artist, []string{"One", "Two"}}},
		{{tag.Album, []string{"New"}}}, {{tag.Album, nil}},
		{{tag.Album, []string{"Track Title"}}}, {{tag.Title, []string{"The Album"}}}, {{tag.Album, []string{"Named Album"}}},
		{{tag.Album, []string{"The Album", "Second Album"}}},
		{{tag.AlbumArtist, []string{"New"}}}, {{tag.AlbumArtist, nil}},
		{{tag.Genre, []string{"Rock"}}}, {{tag.Composer, []string{"Bach"}}},
		{{tag.Title, nil}, {tag.Album, []string{"New"}}},
		{{tag.Album, nil}, {tag.Title, []string{"New"}}},
		{{tag.Title, []string{"New"}}, {tag.Artist, []string{"One"}}, {tag.Album, []string{"X"}}, {tag.AlbumArtist, []string{"Y"}}},
		{{tag.Album, nil}, {tag.AlbumArtist, nil}},
		{{tag.Title, nil}, {tag.Artist, []string{"One", "Two"}}},
		{{tag.Album, nil}, {tag.Artist, []string{"One"}}},
		{{tag.Album, nil}, {tag.Genre, []string{"Rock"}}},
	}
	for name, src := range files {
		base := tagMap(mustParseBytes(t, src))
		for _, es := range edits {
			e, want, label := mustParseBytes(t, src).Edit(), maps.Clone(base), name+":"
			for _, ed := range es {
				label += " " + string(ed.key) + "=" + fmt.Sprint(ed.vals)
				switch {
				case len(ed.vals) == 0:
					e.Clear(ed.key)
					delete(want, ed.key)
				case ed.key == tag.Title:
					e.Set(ed.key, ed.vals...)
					want[ed.key] = ed.vals[:1] // a file holds one title
				default:
					e.Set(ed.key, ed.vals...)
					want[ed.key] = ed.vals
				}
			}
			plan, err := e.Prepare()
			if err != nil {
				t.Errorf("%s: Prepare: %v", label, err)
				continue
			}
			if plan.IsNoOp() {
				if !maps.EqualFunc(base, want, slices.Equal[[]string]) {
					t.Errorf("%s: the plan is a no-op, want %v", label, want)
				}
				continue
			}
			out := applyToBytes(t, src, plan)
			if got := tagMap(mustParseBytes(t, out)); !maps.EqualFunc(got, want, slices.Equal[[]string]) {
				t.Errorf("%s:\n got %v\nwant %v", label, got, want)
				continue
			}
			// The same edit of its own output has nothing left to do.
			again := mustParseBytes(t, out).Edit()
			for _, ed := range es {
				if len(ed.vals) == 0 {
					again.Clear(ed.key)
				} else {
					again.Set(ed.key, ed.vals...)
				}
			}
			if plan, err := again.Prepare(); err != nil || !plan.IsNoOp() {
				t.Errorf("%s: the edit repeated on its output: err %v, operations %v", label, err, plan.Report().Operations)
			}
		}
	}
}

// In a file whose album-scope Tags stand at level 40 and at level 50, the album is written to the
// level-50 one, as its TITLE: the level-40 Tag is left as it is.
func TestMatroskaAlbumIsWrittenAtAlbumLevel(t *testing.T) {
	part, partBody := mkLevelTag(40, testfile.SimpleTag("TITLE", "Track Title"), testfile.SimpleTag("ARTIST", "Track Artist"))
	album, _ := mkLevelTag(50, testfile.SimpleTag("TITLE", "The Album"), testfile.SimpleTag("GENRE", "Jazz"))
	src := matroskaNoTitle(testfile.El(testfile.IDTags, part, album))
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Album, "New Album"))
	if !bytes.Contains(out, partBody) {
		t.Error("the level-40 Tag is not carried byte for byte")
	}
	if bytes.Contains(out, []byte("ALBUM")) {
		t.Error("the album is written under the name ALBUM, not as the album-level TITLE")
	}
	assertTags(t, mustParseBytes(t, out), map[tag.Key][]string{
		tag.Title: {"Track Title"}, tag.Artist: {"Track Artist"}, tag.Album: {"New Album"}, tag.Genre: {"Jazz"},
	})
}

// A file tagged by level that keeps its title in a track-level SimpleTag holds one title there:
// asked for a second beside the one stored, the edit changes nothing and says so.
func TestMatroskaLevelTaggedMultiValueTitleIsAWarnedNoOp(t *testing.T) {
	src, _, _ := levelTagged()
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Track Title", "Second").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("a 2nd title beside the stored one plans a write: %v", plan.Report().Operations)
	}
	if !planWarns(t, plan, wl.WarnValueDropped) {
		t.Errorf("warnings = %v, want value-dropped for the 2nd title", plan.Report().Warnings)
	}
}

// An Info.Title starts no reading by level: the tagging guide gives it no level, and ffmpeg
// writes a file's title there beside album-level tags that are the file's own. Whatever the
// album-level TITLE beside it holds, it is a title and the ARTIST the artist.
func TestMatroskaInfoTitleStartsNoReadingByLevel(t *testing.T) {
	simple := testfile.SimpleTag
	for name, c := range map[string]struct {
		album []byte
		want  map[tag.Key][]string
	}{
		"another title": {
			concat(simple("TITLE", "Other"), simple("ARTIST", "Band")),
			map[tag.Key][]string{tag.Title: {"Song", "Other"}, tag.Artist: {"Band"}},
		},
		"translations of the title": {
			concat(mkLangSimple("TITLE", "eng", "Song"), mkLangSimple("TITLE", "ger", "Lied"), simple("ARTIST", "Band")),
			map[tag.Key][]string{tag.Title: {"Song", "Lied"}, tag.Artist: {"Band"}},
		},
		"the title repeated": {
			concat(simple("TITLE", "song"), simple("ARTIST", "Band")),
			map[tag.Key][]string{tag.Title: {"Song"}, tag.Artist: {"Band"}},
		},
		"an ALBUM beside the TITLE": {
			concat(simple("TITLE", "Other"), simple("ARTIST", "Band"), simple("ALBUM", "Record")),
			map[tag.Key][]string{tag.Title: {"Song", "Other"}, tag.Artist: {"Band"}, tag.Album: {"Record"}},
		},
		"an ALBUM_ARTIST beside the TITLE": {
			concat(simple("TITLE", "Other"), simple("ARTIST", "Band"), simple("ALBUM_ARTIST", "Various")),
			map[tag.Key][]string{tag.Title: {"Song", "Other"}, tag.Artist: {"Band"}, tag.AlbumArtist: {"Various"}},
		},
	} {
		el, _ := mkLevelTag(50, c.album)
		assertTagsOf(t, name, mustParseBytes(t, testfile.Matroska("matroska", "Song", testfile.El(testfile.IDTags, el))), c.want)
	}
}

// A title edit of such a file leaves one title, in the Info.Title, and the artist where it was.
func TestMatroskaInfoTitleFileEditsAsAFlatFile(t *testing.T) {
	artist := testfile.SimpleTag("ARTIST", "Band")
	album, _ := mkLevelTag(50, testfile.SimpleTag("TITLE", "Other"), artist)
	src := testfile.Matroska("matroska", "Song", testfile.El(testfile.IDTags, album))
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Title, "New"))
	assertTags(t, mustParseBytes(t, out), map[tag.Key][]string{tag.Title: {"New"}, tag.Artist: {"Band"}})
	if !bytes.Contains(out, artist) || bytes.Contains(out, []byte("TITLE")) {
		t.Error("want the ARTIST SimpleTag carried byte for byte and no TITLE SimpleTag: the title lives in the Info.Title")
	}

	out, _ = saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Album, "Record"))
	assertTags(t, mustParseBytes(t, out), map[tag.Key][]string{tag.Title: {"Song", "Other"}, tag.Artist: {"Band"}, tag.Album: {"Record"}})
	if !bytes.Contains(out, []byte("ALBUM")) {
		t.Error("the album is not written under its own name")
	}
}

// An album cut down to one of its values stays the album-level TITLE it was: the SimpleTag of
// the value kept is written again as it is, and nothing is respelled.
func TestMatroskaAlbumCutToOneValueStaysTheAlbumLevelTitle(t *testing.T) {
	kept := mkLangSimple("TITLE", "und", "The Album")
	album, _ := mkLevelTag(50, kept, testfile.SimpleTag("TITLE", "Other Name"), testfile.SimpleTag("ARTIST", "Album Artist"))
	track, _ := mkLevelTag(30, testfile.SimpleTag("TITLE", "Track Title"))
	src := matroskaNoTitle(testfile.El(testfile.IDTags, album, track))
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Album, "The Album"))
	if !bytes.Contains(out, kept) {
		t.Error("the SimpleTag of the album value the edit keeps is not carried byte for byte")
	}
	if bytes.Contains(out, []byte("ALBUM")) {
		t.Error("the album, or its artist, is respelled under its own name")
	}
	assertTags(t, mustParseBytes(t, out), map[tag.Key][]string{
		tag.Album: {"The Album"}, tag.AlbumArtist: {"Album Artist"}, tag.Title: {"Track Title"},
	})
}

// Naming or clearing the album of a file not tagged by level touches the album alone: every other
// SimpleTag is carried as it is, in the Tag it stood in, and no Tag is made.
func TestMatroskaAlbumNameEditsLeaveTheTitleInPlace(t *testing.T) {
	// A TITLE with what a plain SimpleTag cannot hold.
	title := mkSimpleNested("TITLE", "Other Title", testfile.SimpleTag("SORT_WITH", "Title, Other"))
	for name, naming := range map[string][]byte{
		"ALBUM":        testfile.SimpleTag("ALBUM", "Record"),
		"ALBUM_ARTIST": testfile.SimpleTag("ALBUM_ARTIST", "Various"),
	} {
		album, _ := mkLevelTag(50, title, testfile.SimpleTag("ARTIST", "Band"), naming)
		part, partBody := mkLevelTag(45, testfile.SimpleTag("TITLE", "Part Title"))
		track, trackBody := mkLevelTag(30, testfile.SimpleTag("GENRE", "Jazz"))
		chapter := mkTargeted(idTagChapUID, 11, testfile.SimpleTag("TITLE", "Chapter One"))
		src := testfile.Matroska("matroska", "Song", testfile.El(testfile.IDTags, album, part, track, chapter))
		doc := mustParseBytes(t, src)
		base := tagMap(doc)
		if _, ok := docWarning(doc, wl.WarnConflictingFamilies); ok {
			t.Fatalf("%s: setup: the file reads with a conflict", name)
		}
		key, _ := map[string]tag.Key{"ALBUM": tag.Album, "ALBUM_ARTIST": tag.AlbumArtist}[name]
		kept := map[string][]byte{
			"the album-level TITLE and ARTIST": concat(title, testfile.SimpleTag("ARTIST", "Band")),
			"the level-45 Tag":                 partBody, "the level-30 Tag": trackBody, "the chapter's Tag": chapter,
		}
		for what, edit := range map[string]func(*wl.Editor) *wl.Editor{
			"renamed": func(e *wl.Editor) *wl.Editor { return e.Set(key, "Renamed") },
			"cleared": func(e *wl.Editor) *wl.Editor { return e.Clear(key) },
		} {
			plan, err := edit(mustParseBytes(t, src).Edit()).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if w, ok := warningFor(plan, wl.WarnTagStructureDropped); ok {
				t.Errorf("%s %s: the plan reports structure lost: %s", name, what, w.Message)
			}
			out := applyToBytes(t, src, plan)
			re := mustParseBytes(t, out)
			want := with(base, key)
			if what == "renamed" {
				want = with(base, key, "Renamed")
			}
			assertTagsOf(t, name+" "+what, re, want)
			for part, raw := range kept {
				if !bytes.Contains(out, raw) {
					t.Errorf("%s %s: %s is not carried byte for byte", name, what, part)
				}
			}
			tags := 0
			for _, e := range re.Native().Describe() {
				if e.Kind == "Tag" {
					tags++
				}
			}
			if tags != 4 {
				t.Errorf("%s %s: Tags = %d, want the 4 the file held", name, what, tags)
			}
		}
	}
}

// A TITLE above album level, a collection's, is no album title: beside a track-level TITLE it
// starts no reading by level, and the album-level ARTIST stays the artist.
func TestMatroskaCollectionTitleIsNoAlbumTitle(t *testing.T) {
	box, _ := mkLevelTag(70, testfile.SimpleTag("TITLE", "Box"))
	album, _ := mkLevelTag(50, testfile.SimpleTag("ARTIST", "AA"))
	track, _ := mkLevelTag(30, testfile.SimpleTag("TITLE", "Song"))
	doc := mustParseBytes(t, matroskaNoTitle(testfile.El(testfile.IDTags, box, album, track)))
	if got := tagValues(doc, tag.Artist); !slices.Equal(got, []string{"AA"}) {
		t.Errorf("ARTIST = %q, want AA", got)
	}
	if doc.Tags().Has(tag.AlbumArtist) || doc.Tags().Has(tag.Album) {
		t.Errorf("tags = %v, want no ALBUM or ALBUMARTIST", tagMap(doc))
	}
}
