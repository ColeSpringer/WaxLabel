package waxlabel_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

const (
	idTagAttachUID = 0x63C6
	idTagEditUID   = 0x63C9
	idTrackUID     = 0x73C5
	idTrackNumber  = 0xD7
)

// mkTrack is a TrackEntry of the given type (1 video, 2 audio) and UID.
func mkTrack(number, uid, trackType uint64, codec string) []byte {
	return testfile.El(testfile.IDTrackEntry, testfile.Uint(idTrackNumber, number), testfile.Uint(idTrackUID, uid),
		testfile.Uint(testfile.IDTrackType, trackType), mkStr(testfile.IDCodecID, codec))
}

// mkTargeted is a Tag whose Targets holds one UID element.
func mkTargeted(uidElement, uid uint64, simples ...[]byte) []byte {
	return testfile.El(testfile.IDTag, testfile.El(testfile.IDTargets, testfile.Uint(uidElement, uid)), concat(simples...))
}

// targetedTags is what scopedFile holds beside its album Tag, each a whole Tag element.
type targetedTags struct{ attachment, chapter, edition, video, audio []byte }

// scopedFile holds a video track (UID 1) and an audio track (UID 2), with tags for the file, for
// an attachment, a chapter, an edition and each track. The attachment's Tag comes first.
func scopedFile() (src []byte, tt targetedTags) {
	tt = targetedTags{
		attachment: mkTargeted(idTagAttachUID, 0x99, testfile.SimpleTag("TITLE", "Booklet"), testfile.SimpleTag("ARTIST", "Scanner")),
		chapter:    mkTargeted(idTagChapUID, 11, testfile.SimpleTag("artist", "Chapter Artist"), testfile.SimpleTag("TITLE", "Chapter One")),
		edition:    mkTargeted(idTagEditUID, 5, testfile.SimpleTag("TITLE", "Edition")),
		video:      mkTargeted(testfile.IDTagTrackUID, 1, testfile.SimpleTag("HANDLER_NAME", "VideoHandler"), testfile.SimpleTag("ENCODER", "Lavc61 libx264")),
		audio:      mkTargeted(testfile.IDTagTrackUID, 2, testfile.SimpleTag("ENCODER", "flac 1.4")),
	}
	album := testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "File Artist"), testfile.SimpleTag("COMMENT", "File note"))
	tracks := testfile.El(testfile.IDTracks, mkTrack(1, 1, 1, "V_MPEG4/ISO/AVC"), mkTrack(2, 2, 2, "A_FLAC"))
	tags := testfile.El(testfile.IDTags, tt.attachment, album, tt.chapter, tt.edition, tt.video, tt.audio)
	return testfile.Matroska("matroska", "Song", tracks, tags), tt
}

// scopedMatroska is the files whose tags turn on targets, levels and titles, for the fuzz seeds:
// a title edit of each takes a path of its own.
func scopedMatroska() [][]byte {
	scoped, _ := scopedFile()
	levels, _, _ := levelTagged()
	titles, _ := twoTitles()
	booklet, _, _, _ := bookletFile()
	album, _ := mkLevelTag(50, testfile.SimpleTag("TITLE", "The Album"), testfile.SimpleTag("ARTIST", "Album Artist"))
	return [][]byte{scoped, levels, titles, booklet, testfile.Matroska("matroska", "Track Title", testfile.El(testfile.IDTags, album))}
}

func (tt targetedTags) all() map[string][]byte {
	return map[string][]byte{"attachment": tt.attachment, "chapter": tt.chapter, "edition": tt.edition, "video track": tt.video, "audio track": tt.audio}
}

// The file's tags hold what is tagged for the file and its audio: a value tagged for an
// attachment, a chapter, an edition or a video track is none of them and conflicts with none.
func TestMatroskaTargetedTagsStayOutOfTheFileTags(t *testing.T) {
	src, _ := scopedFile()
	doc := mustParseBytes(t, src)
	for key, want := range map[tag.Key][]string{
		tag.Title:   {"Song"},
		tag.Artist:  {"File Artist"},
		tag.Comment: {"File note"},
		tag.Encoder: {"flac 1.4"},
	} {
		if got := tagValues(doc, key); !slices.Equal(got, want) {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if doc.Tags().Has(tag.MustKey("HANDLER_NAME")) {
		t.Error("a video track's HANDLER_NAME is among the file's tags")
	}
	if w, ok := docWarning(doc, wl.WarnConflictingFamilies); ok {
		t.Errorf("read warns of a conflict: %s", w.Message)
	}

	// A targeted entry says so: the scope alone does not tell a video track's ENCODER from the
	// audio track's, which is among the tags.
	type entry struct {
		key      tag.Key
		scope    wl.Scope
		value    string
		targeted bool
	}
	var got []entry
	for _, f := range doc.Families() {
		if !f.Selected {
			t.Errorf("family entry %s %v %q is marked a conflict", f.Key, f.Scope, f.Values)
		}
		for _, v := range f.Values {
			got = append(got, entry{f.Key, f.Scope, v, f.Targeted})
		}
	}
	for _, want := range []entry{
		{tag.Title, wl.ScopeAttachment, "Booklet", true},
		{tag.Artist, wl.ScopeAttachment, "Scanner", true},
		{tag.Artist, wl.ScopeChapter, "Chapter Artist", true},
		{tag.Title, wl.ScopeChapter, "Chapter One", true},
		{tag.Title, wl.ScopeEdition, "Edition", true},
		{tag.MustKey("HANDLER_NAME"), wl.ScopeTrack, "VideoHandler", true},
		{tag.Encoder, wl.ScopeTrack, "Lavc61 libx264", true},
		{tag.Encoder, wl.ScopeTrack, "flac 1.4", false},
		{tag.Artist, wl.ScopeAlbum, "File Artist", false},
		{tag.Title, wl.ScopeAlbum, "Song", false},
	} {
		if !slices.Contains(got, want) {
			t.Errorf("families lack %s at %v scope = %q, targeted %v", want.key, want.scope, want.value, want.targeted)
		}
	}
	if s := wl.ScopeAttachment.String(); s != "attachment" {
		t.Errorf("ScopeAttachment prints %q", s)
	}
}

// An edit of the file's tags leaves every targeted Tag as it is: a comment is not written into
// the attachment's Tag, and a new title or artist takes none of theirs away.
func TestMatroskaEditKeepsTargetedTags(t *testing.T) {
	src, tt := scopedFile()
	for _, c := range []struct {
		name string
		edit func(*wl.Editor) *wl.Editor
		key  tag.Key
		want []string
	}{
		{"comment", func(e *wl.Editor) *wl.Editor { return e.Set(tag.Comment, "New note") }, tag.Comment, []string{"New note"}},
		{"title", func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "New Song") }, tag.Title, []string{"New Song"}},
		{"artist", func(e *wl.Editor) *wl.Editor { return e.Set(tag.Artist, "New Artist") }, tag.Artist, []string{"New Artist"}},
		{"encoder", func(e *wl.Editor) *wl.Editor { return e.Clear(tag.Encoder) }, tag.Encoder, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, _ := saveMatroska(t, src, c.edit(mustParseBytes(t, src).Edit()))
			for name, raw := range tt.all() {
				if name == "audio track" && c.name == "encoder" {
					continue // the one targeted Tag this edit is of
				}
				if !bytes.Contains(out, raw) {
					t.Errorf("the %s's Tag is not carried byte for byte", name)
				}
			}
			if got := tagValues(mustParseBytes(t, out), c.key); !slices.Equal(got, c.want) {
				t.Errorf("%s after the edit = %q, want %q", c.key, got, c.want)
			}
		})
	}
}

// A copy carries the file's own values: the artist of a chapter or an attachment is not the
// destination's.
func TestMatroskaCopyCarriesFileLevelTagsOnly(t *testing.T) {
	src, _ := scopedFile()
	dstBytes := readFixture(t, "../testdata/notags.flac")
	plan, _, err := mustParseBytes(t, src).Transfer().Prepare(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatal(err)
	}
	got := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	if v := tagValues(got, tag.Artist); !slices.Equal(v, []string{"File Artist"}) {
		t.Errorf("copied ARTIST = %q, want File Artist alone", v)
	}
	if v := tagValues(got, tag.Title); !slices.Equal(v, []string{"Song"}) {
		t.Errorf("copied TITLE = %q, want Song alone", v)
	}
}

// A Tag naming a track the file does not hold, or every track, is still read as the file's.
func TestMatroskaTagOfAnUnknownTrackIsStillRead(t *testing.T) {
	tracks := testfile.El(testfile.IDTracks, mkTrack(1, 1, 1, "V_MPEG4/ISO/AVC"), mkTrack(2, 2, 2, "A_FLAC"))
	both := testfile.El(testfile.IDTag, testfile.El(testfile.IDTargets,
		testfile.Uint(testfile.IDTagTrackUID, 1), testfile.Uint(testfile.IDTagTrackUID, 2)), testfile.SimpleTag("GENRE", "Both"))
	tags := testfile.El(testfile.IDTags,
		mkTargeted(testfile.IDTagTrackUID, 7, testfile.SimpleTag("COMPOSER", "Unknown track")),
		mkTargeted(testfile.IDTagTrackUID, 0, testfile.SimpleTag("LABEL", "Every track")), both)
	doc := mustParseBytes(t, testfile.Matroska("matroska", "x", tracks, tags))
	for key, want := range map[tag.Key]string{tag.Composer: "Unknown track", tag.Label: "Every track", tag.Genre: "Both"} {
		if got := tagValues(doc, key); !slices.Equal(got, []string{want}) {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// ffmpeg writes an attachment's and a video stream's own metadata as Tags naming them. They are
// not the file's tags, and after an edit ffprobe reads them where they were.
func TestMatroskaTargetedTagsForFFprobe(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()
	page := filepath.Join(dir, "page.jpg")
	if err := os.WriteFile(page, testfile.JPEG(), 0o644); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(dir, "built.mkv")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.2", "-f", "lavfi", "-i", "sine=d=0.2",
		"-attach", page, "-c:v", "mpeg4", "-c:a", "flac",
		"-metadata", "artist=File Artist",
		"-metadata:s:v:0", "handler_name=VideoHandler", "-metadata:s:v:0", "artist=Camera",
		"-metadata:s:t:0", "mimetype=image/jpeg", "-metadata:s:t:0", "filename=page.jpg",
		"-metadata:s:t:0", "artist=Scanner", built)
	if log, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, log)
	}
	src, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	doc := mustParseBytes(t, src)
	if got := tagValues(doc, tag.Artist); !slices.Equal(got, []string{"File Artist"}) {
		t.Fatalf("ARTIST of ffmpeg's file = %q, want File Artist alone", got)
	}
	out, _ := saveMatroska(t, src, doc.Edit().Set(tag.Comment, "New note"))

	var probe struct {
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	raw, err := exec.Command("ffprobe", "-v", "error", "-of", "json", "-show_streams", "-show_format", writeTempFile(t, "out.mkv", out)).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if got := lookupCI(probe.Format.Tags, "comment"); got != "New note" {
		t.Errorf("ffprobe format comment = %q, want New note (all: %v)", got, probe.Format.Tags)
	}
	if got := lookupCI(probe.Format.Tags, "artist"); got != "File Artist" {
		t.Errorf("ffprobe format artist = %q, want File Artist", got)
	}
	// ffprobe lists an image attachment as a video stream: tell the two apart by their own tags.
	want := map[string]string{"handler_name": "Camera", "filename": "Scanner"}
	for _, s := range probe.Streams {
		for marker, artist := range want {
			if lookupCI(s.Tags, marker) == "" {
				continue
			}
			if got := lookupCI(s.Tags, "artist"); got != artist {
				t.Errorf("ffprobe artist of the stream with a %s = %q, want %q (all: %v)", marker, got, artist, s.Tags)
			}
			delete(want, marker)
		}
	}
	if len(want) > 0 {
		t.Errorf("ffprobe lists no stream for %v", want)
	}
}

// A targeted Tag raises nothing about the file's tags: a name the tag vocabulary cannot hold is
// no invalid key, a value cut at a NUL names no key, and a key in conflict among the file's own
// scopes leaves the targeted entry of that key selected.
func TestMatroskaTargetedTagRaisesNoFileWarning(t *testing.T) {
	chapter := mkTargeted(idTagChapUID, 11,
		testfile.SimpleTag("bad=name", "x"), testfile.SimpleTag("ARTIST", "Chapter\x00Artist"))
	album := testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "Album Scope"))
	track, _ := mkLevelTag(30, testfile.SimpleTag("ARTIST", "Track Scope"))
	doc := mustParseBytes(t, testfile.Matroska("matroska", "x", testfile.El(testfile.IDTags, chapter, album, track)))

	if w, ok := docWarning(doc, wl.WarnInvalidTagKey); ok {
		t.Errorf("read warns of an invalid key: %s", w.Message)
	}
	cut, ok := docWarning(doc, wl.WarnMalformedTagEntry)
	if !ok || len(cut.Keys) != 0 {
		t.Errorf("NUL-cut warning = %q keys %v (found %v), want one naming no key", cut.Message, cut.Keys, ok)
	}
	if _, ok := docWarning(doc, wl.WarnConflictingFamilies); !ok {
		t.Fatal("setup: the album and track artists should conflict")
	}
	for _, f := range doc.Families() {
		if f.Key != tag.Artist {
			continue
		}
		if want := f.Scope == wl.ScopeChapter; f.Selected != want {
			t.Errorf("ARTIST at %v scope %q: selected %v, want %v", f.Scope, f.Values, f.Selected, want)
		}
	}
}

// A Tag naming a video track is known for one once the Tracks are read, wherever they stand: here
// behind the Tags.
func TestMatroskaTracksBehindTheTags(t *testing.T) {
	tracks := testfile.El(testfile.IDTracks, mkTrack(1, 1, 1, "V_MPEG4/ISO/AVC"), mkTrack(2, 2, 2, "A_FLAC"))
	tags := testfile.El(testfile.IDTags,
		mkTargeted(testfile.IDTagTrackUID, 1, testfile.SimpleTag("ENCODER", "Lavc61 libx264")),
		mkTargeted(testfile.IDTagTrackUID, 2, testfile.SimpleTag("ENCODER", "flac 1.4")))
	doc := mustParseBytes(t, testfile.Matroska("matroska", "x", tags, tracks))
	if got := tagValues(doc, tag.Encoder); !slices.Equal(got, []string{"flac 1.4"}) {
		t.Errorf("ENCODER = %q, want the audio track's alone", got)
	}
}

// A key set to one value reads back as that one value, and the same edit again has nothing to do,
// whatever scopes held the value before. Here the album-scope value the edit removes was what
// kept the edition's two copies and the track's one out of the tags.
func TestMatroskaSetToOneValueReadsBackOnce(t *testing.T) {
	edition, _ := mkLevelTag(70, testfile.SimpleTag("ARTIST", "A"), testfile.SimpleTag("ARTIST", "A"))
	album, _ := mkLevelTag(45, testfile.SimpleTag("ARTIST", "Long Value"))
	track, _ := mkLevelTag(30, testfile.SimpleTag("ARTIST", "A"), testfile.SimpleTag("GENRE", "Jazz"))
	src := testfile.Matroska("matroska", "x", testfile.El(testfile.IDTags, edition, album, track))
	doc := mustParseBytes(t, src)
	if got := tagValues(doc, tag.Artist); !slices.Equal(got, []string{"Long Value", "A"}) {
		t.Fatalf("setup: ARTIST = %q, want Long Value, A", got)
	}
	out, _ := saveMatroska(t, src, doc.Edit().Set(tag.Artist, "A"))
	re := mustParseBytes(t, out)
	if got := tagValues(re, tag.Artist); !slices.Equal(got, []string{"A"}) {
		t.Errorf("ARTIST after the edit = %q, want A once", got)
	}
	if got := tagValues(re, tag.Genre); !slices.Equal(got, []string{"Jazz"}) {
		t.Errorf("GENRE after the edit = %q, want Jazz", got)
	}
	plan, err := re.Edit().Set(tag.Artist, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("the edit repeated on its output writes: %v", plan.Report().Operations)
	}
}

// A target UID of 0 names every track, edition, chapter or attachment: the Tag is the file's own,
// as ffmpeg reads it. Its values are among the tags, an edit writes into it, and a copy carries
// them.
func TestMatroskaZeroUIDTargetsTheWholeFile(t *testing.T) {
	for name, uids := range map[string][]byte{
		"a chapter UID of 0":     testfile.Uint(idTagChapUID, 0),
		"an edition UID of 0":    testfile.Uint(idTagEditUID, 0),
		"an attachment UID of 0": testfile.Uint(idTagAttachUID, 0),
		"a track UID of 0":       testfile.Uint(testfile.IDTagTrackUID, 0),
		"every UID 0, as older mkvmerge wrote them": concat(testfile.Uint(testfile.IDTagTrackUID, 0), testfile.Uint(idTagEditUID, 0),
			testfile.Uint(idTagChapUID, 0), testfile.Uint(idTagAttachUID, 0)),
	} {
		group := testfile.El(testfile.IDTag, testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, 50), uids),
			testfile.SimpleTag("ARTIST", "Band"), testfile.SimpleTag("ALBUM", "Record"))
		src := testfile.Matroska("matroska", "Song", testfile.El(testfile.IDTags, group))
		doc := mustParseBytes(t, src)
		want := map[tag.Key][]string{tag.Title: {"Song"}, tag.Artist: {"Band"}, tag.Album: {"Record"}}
		if got := tagMap(doc); !maps.EqualFunc(got, want, slices.Equal[[]string]) {
			t.Errorf("%s: tags = %v, want %v", name, got, want)
			continue
		}
		for _, f := range doc.Families() {
			if f.Key == tag.Artist && f.Scope != wl.ScopeAlbum {
				t.Errorf("%s: ARTIST is at %v scope, want album", name, f.Scope)
			}
		}
		out, _ := saveMatroska(t, src, doc.Edit().Set(tag.Genre, "Rock"))
		re := mustParseBytes(t, out)
		if got := tagMap(re); !maps.EqualFunc(got, with(want, tag.Genre, "Rock"), slices.Equal[[]string]) {
			t.Errorf("%s: tags after an edit = %v", name, got)
		}
		tagElements := 0
		for _, e := range re.Native().Describe() {
			if e.Kind == "Tag" {
				tagElements++
			}
		}
		if tagElements != 1 {
			t.Errorf("%s: %d Tag elements after the edit, want the one the file had", name, tagElements)
		}
	}
}
