package waxlabel_test

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// transferSource is one source of the transfer matrix: values a destination's writer may store in
// another shape than its capability table says.
type transferSource struct {
	name string
	data func(t *testing.T) []byte
	opts []wl.WriteOption
}

func flacSource(entries ...string) func(*testing.T) []byte {
	return func(*testing.T) []byte { return testfile.TaggedFLAC("matrix", entries...) }
}

// linkPictureFLAC holds one PICTURE block whose MIME is the URL marker and whose data is the URL.
func linkPictureFLAC(description string) []byte {
	url := "http://example.com/cover.jpg"
	body := slices.Concat(
		testfile.BE32(3), testfile.BE32(len(core.LinkMIME)), []byte(core.LinkMIME),
		testfile.BE32(len(description)), []byte(description),
		testfile.BE32(1), testfile.BE32(1), testfile.BE32(24), testfile.BE32(0),
		testfile.BE32(len(url)), []byte(url))
	return testfile.FLAC(testfile.CommentBlock("matrix", "TITLE=Linked"), testfile.FLACBlock(6, body))
}

// rawPictureBlock is a FLAC PICTURE block with the fields as given, whatever the bytes are.
func rawPictureBlock(role int, mime string, width, height, depth int, data []byte) []byte {
	return testfile.FLACBlock(6, slices.Concat(
		testfile.BE32(role), testfile.BE32(len(mime)), []byte(mime), testfile.BE32(0),
		testfile.BE32(width), testfile.BE32(height), testfile.BE32(depth), testfile.BE32(0),
		testfile.BE32(len(data)), data))
}

// loosePictureFLAC holds pictures whose blocks say other than their bytes: a type spelled
// another way, no size, a wrong size, no type at all. A read settles each from its bytes.
func loosePictureFLAC() []byte {
	return testfile.FLAC(testfile.CommentBlock("matrix", "TITLE=Loose"),
		rawPictureBlock(3, "image/jpg", 0, 0, 0, testfile.JPEG()),
		rawPictureBlock(4, "IMAGE/PNG", 600, 600, 8, testfile.PNG()),
		rawPictureBlock(8, "", 0, 0, 0, testfile.GIF()))
}

var transferSources = []transferSource{
	{name: "frame-id keys", data: flacSource(
		"TITLE=T", "ARTIST=Real", "TSRC=USRC17607839", "TORY=1999", "TMED=CD", "TCMP=1", "TXXX=foo",
		"TCON=Rock", "TRCK=3", "TPOS=1", "TIPL=x", "TYER=1998", "TDRC=2001", "TIT2=Other",
		"TPE1=Someone", "TKEY=Am", "TSO2=Sort", "TDAT=0101", "TIME=1200", "TDOR=1990", "TDRL=2000")},
	{name: "single-slot keys", data: flacSource(
		"DATE=2023", "YEAR=2022", "TRACKNUMBER=3", "TRACKTOTAL=12", "TOTALTRACKS=13",
		"DISCNUMBER=1", "DISCNUMBER=2", "DISCTOTAL=2", "DISCTOTAL=3",
		"ORIGINALDATE=1999", "ORIGINALDATE=2000", "MOVEMENT=1", "MOVEMENT=2",
		"MOVEMENTTOTAL=3", "MOVEMENTTOTAL=4", "LYRICS=first", "LYRICS=second",
		"MUSICBRAINZ_TRACKID=8f3471b5-7e6a-48da-86a9-c1c07a0f47ae",
		"MUSICBRAINZ_TRACKID=9c9f1380-2516-4fc9-a3e6-f9f61941d090",
		"RELEASEDATE=2001", "RELEASEDATE=2002")},
	{name: "two titles", data: flacSource("TITLE=One", "TITLE=Two", "ARTIST=A")},
	{name: "slash policy", data: flacSource("ARTIST=AC/DC", "ARTIST=Queen", "GENRE=Rock", "GENRE=Pop", "TITLE=T"),
		opts: []wl.WriteOption{wl.WithID3MultiValue(wl.ID3MultiSlash)}},
	{name: "NUL values", data: flacSource("TITLE=abc\x00", "ARTIST=A", "ALBUM=a\x00b", "COMMENT=x\x00")},
	{name: "empty values", data: flacSource(
		"ARTIST=A", "ARTIST=", "ARTIST=B", "GENRE=X", "GENRE=", "ALBUM=", "COMPOSER=", "COMPOSER=")},
	{name: "several values on every kind of key", data: flacSource(
		"COMMENT=c1", "COMMENT=c2", "ALBUM=A1", "ALBUM=A2", "ALBUMARTIST=X", "ALBUMARTIST=Y",
		"BPM=120", "BPM=121", "COMPILATION=1", "COMPILATION=0", "ISRC=USRC17607839", "ISRC=USRC17607840",
		"MEDIA=CD", "MEDIA=LP", "COMPOSER=C1", "COMPOSER=C2", "MYKEY=one", "MYKEY=two",
		"GENRE=Rock", "GENRE=Pop", "LABEL=L1", "LABEL=L2", "COPYRIGHT=c", "COPYRIGHT=d",
		"TITLESORT=a", "TITLESORT=b", "LYRICIST=l1", "LYRICIST=l2", "PRODUCER=p1", "PRODUCER=p2")},
	{name: "number pairs and dates", data: flacSource(
		"TRACKNUMBER=03/12", "DISCNUMBER=1/2", "DATE=2021-03-15T10:30:45", "ORIGINALDATE=1999-05",
		"RELEASEDATE=2001-01-01", "BPM=120.5", "GENRE=17", "COMPILATION=yes", "MOVEMENT=2/5")},
	{name: "a frame-id key beside the key it reads as", data: flacSource("TCMP=1", "COMPILATION=0", "TITLE=T")},
	{name: "empty credits", data: flacSource("PRODUCER=", "PRODUCER=P", "ENGINEER=E", "ENGINEER=", "TITLE=")},
	{name: "values a native chunk holds", data: flacSource("DATE=", "TRACKNUMBER=", "GENRE=17", "TITLE=T")},
	{name: "an empty comment beside one", data: flacSource("COMMENT=x", "COMMENT=")},
	{name: "a picture beside a value a native chunk alone holds", data: func(*testing.T) []byte {
		return testfile.FLAC(testfile.CommentBlock("matrix", "DATE=", "TRACKNUMBER=", "TITLE=T"),
			rawPictureBlock(3, "image/png", 0, 0, 0, testfile.PNG()))
	}},
	{name: "a numeric genre alone", data: flacSource("GENRE=17")},
	{name: "empty numbers", data: flacSource(
		"TRACKNUMBER=", "BPM=", "COMPILATION=", "MOVEMENT=", "TRACKTOTAL=", "DISCNUMBER=", "MEDIATYPE=", "TITLE=T")},
	{name: "a key stored under a name read as another", data: flacSource("MUSICBRAINZ ALBUM ID=abc", "TITLE=T")},
	{name: "that key beside the one it reads as", data: flacSource("MUSICBRAINZ ALBUM ID=a", "MUSICBRAINZ_ALBUMID=b", "TITLE=T")},
	{name: "a slashed number among several", data: flacSource("TRACKNUMBER=3/12", "TRACKNUMBER=4", "TITLE=T")},
	{name: "a total beside a number that is none", data: flacSource("TRACKNUMBER=A", "TRACKTOTAL=12", "MOVEMENT=3/12", "DISCTOTAL=2")},
	{name: "a numeric genre under the numeric option", data: flacSource("GENRE=Rock", "GENRE=Not A Genre"),
		opts: []wl.WriteOption{wl.WithNumericGenre()}},
	{name: "pictures declared loosely", data: func(*testing.T) []byte { return loosePictureFLAC() }},
	{name: "link picture", data: func(*testing.T) []byte { return linkPictureFLAC("") }},
	{name: "described link picture", data: func(*testing.T) []byte { return linkPictureFLAC("the cover") }},
	{name: "chapters off the millisecond", data: func(t *testing.T) []byte {
		return readFixture(t, "../testdata/chapters.mpc")
	}},
	{name: "chapters with ends, languages and flags", data: func(t *testing.T) []byte {
		return writeBack(t, "../testdata/notags.mka", func(e *wl.Editor) {
			e.Set(tag.Title, "Chaptered").SetChapters(
				wl.Chapter{Start: 0, End: 400 * time.Millisecond, Title: "gapped"},
				wl.Chapter{Start: 500 * time.Millisecond, End: 900 * time.Millisecond, Title: "en français", Language: "fre"},
				wl.Chapter{Start: 900 * time.Millisecond, Title: "hidden", Hidden: true},
				wl.Chapter{Start: 950 * time.Millisecond, Title: "last"})
		})
	}},
	{name: "synced lyrics sets", data: func(t *testing.T) []byte {
		return writeBack(t, notagsMP3, func(e *wl.Editor) {
			e.Set(tag.Title, "Sung").SetSyncedLyrics(
				wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: 0, Text: "one"}, {Time: time.Second, Text: "two"}}},
				wl.SyncedLyrics{Language: "deu", Description: "auf Deutsch", Lines: []wl.SyncedLine{{Time: 0, Text: "eins"}, {Time: time.Second, Text: "zwei\ndrei"}}})
		})
	}},
}

// transferDestination is one destination of the transfer matrix.
type transferDestination struct {
	name string
	data func(t *testing.T) []byte
}

func fixtureDestinations(names ...string) []transferDestination {
	dests := make([]transferDestination, len(names))
	for i, name := range names {
		dests[i] = transferDestination{name, func(t *testing.T) []byte { return readFixture(t, filepath.Join("..", "testdata", name)) }}
	}
	return dests
}

// holdingDestination is fixture holding metadata of its own a copy has no business with: keys a
// source's item may read back under, a total a slashed number may bring, and where the format
// stores them a cover, chapters and a synced lyrics set.
func holdingDestination(fixture string) transferDestination {
	return transferDestination{"holding " + fixture, func(t *testing.T) []byte {
		data := readFixture(t, filepath.Join("..", "testdata", fixture))
		doc := mustParseBytes(t, data)
		caps := doc.Capabilities()
		ed := doc.Edit().Set(tag.Album, "Own").Set(tag.Compilation, "0").Set("MUSICBRAINZ_ALBUMID", "zzz").
			Set(tag.TrackTotal, "99").Set(tag.DiscTotal, "9").Set(tag.MovementTotal, "7").Set(tag.Movement, "6")
		if caps.Pictures.Write == wl.AccessFull {
			// Bytes no source of the matrix holds.
			ed.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Description: "own", Data: append(testfile.PNG(), 0)})
		}
		if caps.Chapters.Write == wl.AccessFull {
			ed.SetChapters(wl.Chapter{Title: "own one"}, wl.Chapter{Start: 300 * time.Millisecond, Title: "own two"})
		}
		if caps.SyncedLyrics.Write == wl.AccessFull {
			ed.SetSyncedLyrics(wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: 0, Text: "own"}}})
		}
		plan, err := ed.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		var out writerTo
		if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&out, wl.BytesSource(data))); err != nil {
			t.Fatal(err)
		}
		return out.b
	}}
}

// transferDestinations are the files a transfer can write: blank, tagged, and holding metadata
// of their own the sources would run into.
var transferDestinations = append(fixtureDestinations(
	"notags.mp3", "notags.flac", "notags.ogg", "notags.opus", "notags.oga", "notags.m4a", "notags.mka",
	"notags.wv", "notags.ape", "notags.wav", "notags.aiff", "notags.aac", "chapters.mpc",
	"sample.webm", "sample-rf64.wav", "sample.aifc",
	"sample.mp3", "sample.flac", "sample.m4a", "sample.mka", "sample.wv", "sample.aiff", "sample.ogg",
),
	holdingDestination("notags.mp3"), holdingDestination("notags.flac"), holdingDestination("notags.ogg"),
	holdingDestination("notags.m4a"), holdingDestination("notags.mka"), holdingDestination("notags.ape"),
	holdingDestination("notags.wav"), holdingDestination("notags.aiff"),
)

// blankTransferDestinations hold no metadata of their own.
var blankTransferDestinations = fixtureDestinations(
	"notags.mp3", "notags.flac", "notags.ogg", "notags.opus", "notags.oga", "notags.m4a", "notags.mka",
	"notags.wv", "notags.ape", "notags.wav", "notags.aiff", "notags.aac",
)

// transferLossCodes are the plan warnings that say the write stored less than it was given.
var transferLossCodes = []wl.WarningCode{
	wl.WarnValueDropped, wl.WarnValueCoerced, wl.WarnValueReduced, wl.WarnNumericGenre,
	wl.WarnPictureMetadataDropped, wl.WarnPictureUnsupported, wl.WarnChapterTitleTruncated,
	wl.WarnChapterStartOverflow, wl.WarnChapterMetadataDropped, wl.WarnChaptersUnsupported,
	wl.WarnSyncedLyricsMetadataDropped, wl.WarnSyncedLyricsTimestampClamped,
	wl.WarnSyncedLyricsUnsupported,
}

// gradedValues is the source's values for key in the form a transfer grades and writes.
func gradedValues(src *wl.Document, key tag.Key) []string {
	vals, _ := src.Get(key)
	vals = slices.Clone(vals)
	if tag.IsTrimmableKey(key) {
		for i, v := range vals {
			vals[i] = tag.TrimTokenValue(key, v)
		}
	}
	return vals
}

// storedUnderAnotherKey reports a key the write gave want that the destination did not hold so.
func storedUnderAnotherKey(want []string, before, after *wl.Document) bool {
	for k, got := range after.Tags().All() {
		if held, _ := before.Get(k); slices.Equal(got, want) && !slices.Equal(held, got) {
			return true
		}
	}
	return false
}

// qualifiersDiffer reports a comment or lyric whose description or language the write did not
// keep as the source gives it. A language the source leaves unset is the destination's to fill.
func qualifiersDiffer(key tag.Key, src, after *wl.Document) bool {
	type item struct{ description, language string }
	var want, got []item
	switch key {
	case tag.Comment:
		for _, c := range src.Comments() {
			want = append(want, item{c.Description, c.Language})
		}
		for _, c := range after.Comments() {
			got = append(got, item{c.Description, c.Language})
		}
	case tag.Lyrics:
		for _, l := range src.Lyrics() {
			want = append(want, item{l.Description, l.Language})
		}
		for _, l := range after.Lyrics() {
			got = append(got, item{l.Description, l.Language})
		}
	}
	for i, w := range want {
		if i >= len(got) || w.description != got[i].description || w.language != "" && w.language != got[i].language {
			return true
		}
	}
	return false
}

// setCounts tallies the items of one kind by disposition.
type setCounts struct{ carried, lossy, dropped int }

func (c setCounts) stored() int { return c.carried + c.lossy }

// assertTransferMatchesResult holds the report to what the write left: a carried item reads back
// as the source gave it, a lossy one is there in another form, a dropped one left the destination
// as it was. Each picture, chapter and synced lyrics set is looked up by itself, wherever it
// reads back. Nothing else in the destination moved unsaid: a key of its own is as it was, or
// went with a write the plan warns of by key, and a key that appeared came out of an item
// graded lossy. A report that grades nothing lossy or dropped comes with no plan warning of a
// loss.
func assertTransferMatchesResult(t *testing.T, src, before, after *wl.Document, plan *wl.Plan, report wl.TransferReport) {
	t.Helper()
	sets := map[wl.TransferKind]*setCounts{wl.TransferPicture: {}, wl.TransferChapter: {}, wl.TransferSyncedLyric: {}}
	// warned reports a plan warning that the write stores less under k than it was given.
	warned := func(k tag.Key) bool {
		return slices.ContainsFunc(plan.Report().Warnings, func(w wl.Warning) bool {
			return slices.Contains(transferLossCodes, w.Code) && slices.Contains(w.Keys, k)
		})
	}
	var lossyValues []string
	for _, it := range report.Items {
		if it.Kind != wl.TransferField {
			c := sets[it.Kind]
			switch it.Disposition {
			case wl.Carried:
				c.carried += it.Count
			case wl.Lossy:
				c.lossy += it.Count
			case wl.Dropped:
				c.dropped += it.Count
			}
			if (it.Disposition == wl.Carried) != (it.Reason == "") {
				t.Errorf("%s (%d) graded %s with reason %q", it.Kind, it.Count, it.Disposition, it.Reason)
			}
			continue
		}
		want := gradedValues(src, it.Key)
		got, _ := after.Get(it.Key)
		held, _ := before.Get(it.Key)
		same := core.TransferValuesEqual(it.Key, want, got, src.Format(), after.Format())
		switch it.Disposition {
		case wl.Carried:
			if raw, _ := src.Get(it.Key); !same && !slices.Equal(raw, got) {
				t.Errorf("%s graded carried, reads back %q, want %q", it.Key, got, want)
			}
			if it.Reason != "" {
				t.Errorf("%s graded carried with a reason: %s", it.Key, it.Reason)
			}
		case wl.Lossy:
			lossyValues = append(lossyValues, want...)
			if len(got) == 0 && !storedUnderAnotherKey(want, before, after) {
				t.Errorf("%s graded lossy (%s), but the destination holds nothing of it", it.Key, it.Reason)
			}
			// A genre kept as a numeric reference is graded by its stored form: this reader
			// resolves the reference back to the name it was given.
			if same && !qualifiersDiffer(it.Key, src, after) && it.Key != tag.Genre {
				t.Errorf("%s graded lossy (%s), but reads back as the source's %q", it.Key, it.Reason, got)
			}
		case wl.Dropped:
			// A dropped field is not written. The value the destination held under its key
			// stays, or goes with another field's write, which the reason and the plan both say.
			lost := strings.Contains(it.Reason, "loses the")
			switch {
			case slices.Equal(got, held):
				if lost {
					t.Errorf("%s graded dropped (%s), but the destination still holds %q", it.Key, it.Reason, held)
				}
			case len(got) > 0 || !lost || !warned(it.Key):
				t.Errorf("%s graded dropped (%s), but the destination went from %q to %q (plan warns of it: %v)", it.Key, it.Reason, held, got, warned(it.Key))
			}
			if it.Reason == "" {
				t.Errorf("%s graded dropped with no reason", it.Key)
			}
		}
	}

	// The copy overlays the source. A key only the destination holds is kept as it was, unless
	// the plan warns of what the write does to it. One that appears under no source key is a
	// value an item graded lossy was stored as.
	fromLossy := func(vals []string) bool {
		return !slices.ContainsFunc(vals, func(v string) bool {
			return !slices.ContainsFunc(lossyValues, func(l string) bool { return strings.Contains(l, v) })
		})
	}
	for k, got := range after.Tags().All() {
		held, was := before.Get(k)
		switch {
		case src.Tags().Has(k) || k.DescribesOwnAudio() || slices.Equal(got, held):
		case was && !warned(k):
			t.Errorf("%s, a key of the destination's own, went from %q to %q", k, held, got)
		case !was && !fromLossy(got):
			t.Errorf("%s = %q appeared, from no item graded lossy", k, got)
		}
	}
	for k, held := range before.Tags().All() {
		if !src.Tags().Has(k) && !after.Tags().Has(k) && !warned(k) {
			t.Errorf("%s, a key of the destination's own, went from %q to nothing, and the plan does not warn of it", k, held)
		}
	}

	// Each picture, chapter and synced lyrics set of the source is graded once.
	for kind, n := range map[wl.TransferKind]int{
		wl.TransferPicture: len(src.Pictures()), wl.TransferChapter: len(src.Chapters()), wl.TransferSyncedLyric: len(src.SyncedLyrics()),
	} {
		if c := sets[kind]; c.stored()+c.dropped != n {
			t.Errorf("%s: %d carried, %d lossy, %d dropped, of %d in the source", kind, c.carried, c.lossy, c.dropped, n)
		}
	}

	pics := sets[wl.TransferPicture]
	var exact, partial, absent int
	for _, p := range src.Pictures() {
		switch {
		case slices.ContainsFunc(after.Pictures(), func(q wl.Picture) bool {
			return wl.EqualPictures([]wl.Picture{p}, []wl.Picture{q})
		}):
			exact++
		case slices.ContainsFunc(after.Pictures(), func(q wl.Picture) bool { return bytes.Equal(p.Data, q.Data) }):
			partial++
		default:
			absent++
		}
	}
	if exact != pics.carried || partial != pics.lossy || absent != pics.dropped {
		t.Errorf("pictures graded %d carried, %d lossy, %d dropped; the destination holds %d as given, %d altered, %d not at all",
			pics.carried, pics.lossy, pics.dropped, exact, partial, absent)
	}
	if pics.stored() == 0 && !wl.EqualPictures(after.Pictures(), before.Pictures()) {
		t.Errorf("no picture graded stored, but the destination's pictures changed")
	}

	// A chapter is found by all but its end, which a list may rebuild; the ends are held to the
	// source's where the whole list is graded carried.
	chapters := sets[wl.TransferChapter]
	srcChapters, gotChapters := src.Chapters(), after.Chapters()
	endless := func(c wl.Chapter) wl.Chapter { c.End = 0; return c }
	found := 0
	for _, c := range srcChapters {
		if slices.ContainsFunc(gotChapters, func(g wl.Chapter) bool { return endless(g) == endless(c) }) {
			found++
		}
	}
	switch {
	case chapters.stored() == 0:
		if !wl.EqualChapters(gotChapters, before.Chapters()) {
			t.Errorf("no chapter graded stored, but the destination's chapters changed")
		}
	case chapters.carried > found:
		t.Errorf("%d chapters graded carried, %d read back as given: %+v", chapters.carried, found, gotChapters)
	case chapters.stored() > len(gotChapters):
		t.Errorf("%d chapters graded stored, the destination holds %d", chapters.stored(), len(gotChapters))
	case chapters.carried == len(srcChapters) &&
		!wl.EqualChaptersModuloEnds(srcChapters, gotChapters, src.Properties().Duration(), after.Properties().Duration()):
		t.Errorf("every chapter graded carried, but the list reads back %+v, want %+v", gotChapters, srcChapters)
	}

	lyrics := sets[wl.TransferSyncedLyric]
	gotLyrics := after.SyncedLyrics()
	found = 0
	for _, sl := range src.SyncedLyrics() {
		if slices.ContainsFunc(gotLyrics, func(g wl.SyncedLyrics) bool {
			return wl.EqualSyncedLyrics([]wl.SyncedLyrics{sl}, []wl.SyncedLyrics{g})
		}) {
			found++
		}
	}
	switch {
	case lyrics.stored() == 0:
		if !wl.EqualSyncedLyrics(gotLyrics, before.SyncedLyrics()) {
			t.Errorf("no synced lyrics set graded stored, but the destination's changed")
		}
	case lyrics.carried > found:
		t.Errorf("%d synced lyrics sets graded carried, %d read back as given: %+v", lyrics.carried, found, gotLyrics)
	case lyrics.stored() != len(gotLyrics):
		t.Errorf("%d synced lyrics sets graded stored, the destination holds %d", lyrics.stored(), len(gotLyrics))
	}

	if report.Lossless() {
		for _, w := range plan.Report().Warnings {
			if slices.Contains(transferLossCodes, w.Code) {
				t.Errorf("report grades nothing lossy or dropped, but the plan warns [%s] %s", w.Code, w.Message)
			}
		}
	}
}

// forEachTransfer runs fn over every matrix source and each of dests.
func forEachTransfer(t *testing.T, dests []transferDestination, fn func(t *testing.T, s transferSource, srcBytes, dstBytes []byte)) {
	t.Helper()
	for _, s := range transferSources {
		for _, d := range dests {
			t.Run(s.name+"/"+d.name, func(t *testing.T) {
				t.Parallel()
				fn(t, s, s.data(t), d.data(t))
			})
		}
	}
}
