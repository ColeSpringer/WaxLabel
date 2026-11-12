package waxlabel_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// A total written with no number, "/12" in the track entry, stays there under the next edit.
func TestMP4MdtaLoneTotalSurvivesAnotherEdit(t *testing.T) {
	data := testfile.MP4MdtaFile([]string{"title"}, []string{"T"})
	plan, err := mustParseBytes(t, data).Edit().Set(tag.TrackTotal, "12").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	first := applyToBytes(t, data, plan)
	if !bytes.Contains(first, testfile.MP4KeyItem(2, "/12")) {
		t.Fatal("the total was not written as /12 in the track entry")
	}
	plan, err = mustParseBytes(t, first).Edit().Set(tag.Title, "Other").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	second := applyToBytes(t, first, plan)
	if names := mp4KeysNames(t, second); !bytes.Contains(second, testfile.MP4KeyItem(2, "/12")) || !slices.Equal(names, []string{"title", "track"}) {
		t.Errorf("an edit of the title moved the total: keys %v", names)
	}
}

// A custom key spelled like a name of ffmpeg's or Apple's, in capitals as it is written, reads
// back as itself.
func TestMP4MdtaCustomKeyNamedLikeAnFFmpegName(t *testing.T) {
	data := testfile.MP4MdtaFile([]string{"title"}, []string{"T"})
	ed := mustParseBytes(t, data).Edit()
	keys := []tag.Key{"SOFTWARE", "CREATIONDATE", "SYNOPSIS", "TMPO", "SORT_NAME", "MEDIA_TYPE", "EPISODE_SORT"}
	for _, k := range keys {
		ed.Set(k, "x")
	}
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	for _, k := range keys {
		if v, _ := re.Get(k); !slices.Equal(v, []string{"x"}) {
			t.Errorf("%s read back as %v", k, v)
		}
	}
	if re.Tags().Len() != len(keys)+1 {
		t.Errorf("keys = %v", re.Tags().Keys())
	}
}

// The store under moov.meta beside the one written: a value it holds without reading it shows as a
// conflict and stays through a write; a key it alone holds is not copied into the store written;
// and naming a key the two disagree on settles it.
func TestMP4SecondStoreIsLeftItsOwn(t *testing.T) {
	udta := testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Udta Title"))))
	pop := testfile.MP4KeyItem(3, "Pop")
	meta := testfile.MP4Meta(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "genre", "com.apple.quicktime.genre", "artist", "tmpo"),
		testfile.MP4Ilst(testfile.MP4KeyItem(1, "Meta Title"), testfile.MP4KeyItem(2, "Rock"), pop, testfile.MP4KeyItem(4, "Meta Artist"), testfile.MP4KeyItem(5, "120.5")))
	data := mp4MoovMetaFile(udta, meta)
	doc := mustParseBytes(t, data)
	if g, _ := doc.Get(tag.Genre); !slices.Equal(g, []string{"Rock"}) || !slices.ContainsFunc(doc.Families(), func(f wl.FamilyValue) bool {
		return f.Key == tag.Genre && !f.Selected && slices.Contains(f.Values, "Pop")
	}) {
		t.Errorf("GENRE = %v, families %+v; want Rock read and Pop shown as a conflict", g, doc.Families())
	}
	udtaOf := func(out []byte) []byte {
		moov, moovLen := mp4FindBox(t, out, 0, len(out), "moov")
		off, size := mp4FindBox(t, out, moov+8, moov+moovLen, "udta")
		return out[off : off+size]
	}
	t.Run("unrelated edit", func(t *testing.T) {
		plan, err := doc.Edit().Set(tag.Album, "A").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if !bytes.Contains(out, pop) || !bytes.Contains(out, testfile.MP4KeyItem(2, "Rock")) {
			t.Error("the second store lost a genre item to an edit of the album")
		}
		if u := udtaOf(out); bytes.Contains(u, []byte("\xa9ART")) || bytes.Contains(u, []byte("\xa9gen")) {
			t.Error("keys only the second store holds were copied into the store written")
		}
		re := mustParseBytes(t, out)
		if a, _ := re.Get(tag.Artist); !slices.Equal(a, []string{"Meta Artist"}) {
			t.Errorf("ARTIST = %v", a)
		}
		// Its BPM is text there: nothing rounds it.
		if ws := plan.Report().Warnings; len(ws) != 0 || !bytes.Contains(out, testfile.MP4KeyItem(5, "120.5")) {
			t.Errorf("warnings %v for a key the second store keeps as it is", ws)
		}
	})
	t.Run("the item read stands behind the one not read in the keys table", func(t *testing.T) {
		// The Apple-named entry comes first in the ilst, so it is the one read.
		swapped := testfile.MP4Meta(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "genre", "com.apple.quicktime.genre"),
			testfile.MP4Ilst(testfile.MP4KeyItem(1, "Meta Title"), pop, testfile.MP4KeyItem(2, "Rock")))
		data := mp4MoovMetaFile(udta, swapped)
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "A").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if out := applyToBytes(t, data, plan); !bytes.Contains(out, pop) || !bytes.Contains(out, testfile.MP4KeyItem(2, "Rock")) {
			t.Error("the sync of the title rewrote or dropped a genre item it had no call to touch")
		}
	})
	t.Run("a number and total it alone holds behind a slash", func(t *testing.T) {
		slashed := testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(mp4Freeform("com.apple.iTunes", "TRACKNUMBER", "3/12")))
		data := mp4MoovMetaFile(udta, slashed)
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "A").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if bytes.Contains(udtaOf(out), []byte("trkn")) {
			t.Error("the total behind the second store's slash was copied into the store written")
		}
		if total, _ := mustParseBytes(t, out).Get(tag.TrackTotal); !slices.Equal(total, []string{"12"}) {
			t.Errorf("TRACKTOTAL = %v", total)
		}
	})
	t.Run("edit of a key it alone holds", func(t *testing.T) {
		plan, err := doc.Edit().Set(tag.Artist, "New Artist").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if !bytes.Contains(out, testfile.MP4KeyItem(4, "New Artist")) || bytes.Contains(udtaOf(out), []byte("\xa9ART")) {
			t.Error("the edit did not land in the second store alone")
		}
		if a, _ := mustParseBytes(t, out).Get(tag.Artist); !slices.Equal(a, []string{"New Artist"}) {
			t.Errorf("ARTIST = %v", a)
		}
	})
	t.Run("naming a key the stores disagree on", func(t *testing.T) {
		plan, err := doc.Edit().Set(tag.Title, "Udta Title").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if plan.IsNoOp() {
			t.Fatal("naming the title planned nothing; an unrelated edit would have synced it")
		}
		if out := applyToBytes(t, data, plan); !bytes.Contains(out, testfile.MP4KeyItem(1, "Udta Title")) {
			t.Error("the second store's title was not settled")
		}
	})
}

// A udta text atom that disagrees with the ilst is settled by naming its key, as an unrelated
// edit settles it.
func TestMP4UdtaDisagreementSettledByNamingTheKey(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Udta")),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Ilst Title"))),
	)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Ilst Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("naming the title planned nothing")
	}
	if re := mustParseBytes(t, applyToBytes(t, data, plan)); hasLintCode(re, "conflicting-families") {
		t.Errorf("the stores still disagree: %v", re.Lint())
	}
}

// A value kept byte for byte because its key is held twice is not reported as rounded or dropped.
func TestMP4KeptValueIsNotReportedAsCoerced(t *testing.T) {
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), mp4Freeform("com.apple.iTunes", "BPM", "120.5"),
		testfile.MP4Atom("tmpo", testfile.MP4Data(21, []byte{0, 120})))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "U").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if ws := plan.Report().Warnings; reportHasWarning(ws, wl.WarnValueCoerced) || reportHasWarning(ws, wl.WarnValueDropped) {
		t.Errorf("warnings %v for a BPM the write keeps as it is", ws)
	}
	if out := applyToBytes(t, data, plan); !bytes.Contains(out, []byte("120.5")) {
		t.Error("the freeform BPM did not survive")
	}
}

// Junk ahead of the mdat is no tail: the media behind it would go unread.
func TestMP4JunkAheadOfTheMediaIsRefused(t *testing.T) {
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	mdat := bytes.Index(data, []byte("mdat")) - 4
	broken := slices.Concat(data[:mdat], []byte("\x00\x00\x00\x05junk"), data[mdat:])
	if doc, err := wl.Parse(context.Background(), wl.BytesSource(broken)); err == nil {
		t.Errorf("parsed with warnings %v; the chunk offsets point behind the junk", doc.Warnings())
	}
}

// The year atom that goes with an edit of its date is no conflict, and is not reported as one.
func TestMP4YearEchoRemovalIsNotCalledAConflict(t *testing.T) {
	data := testfile.MP4Tagged(mp4Freeform("com.apple.iTunes", "originalyear", "1999"), mp4Freeform("com.apple.iTunes", "originaldate", "1999-03-01"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.OriginalDate, "2001-02-03").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	ops := strings.Join(plan.Report().Operations, "; ")
	if strings.Contains(ops, "conflicting") || !strings.Contains(ops, "1 year echo of an edited date removed") {
		t.Errorf("operations = %q", ops)
	}
}

// The season and episode atoms are signed: a number past 2^31-1 goes to a freeform.
func TestMP4TVNumberAtomHoldsNoNegative(t *testing.T) {
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	for value, atom := range map[string]bool{"2147483647": true, "2147483648": false, "3000000000": false} {
		plan, err := mustParseBytes(t, data).Edit().Set(tag.EpisodeNumber, value).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if got := bytes.Contains(out, []byte("tves")); got != atom {
			t.Errorf("EPISODENUMBER %s: integer atom written = %v, want %v", value, got, atom)
		}
		if v, _ := mustParseBytes(t, out).Get(tag.EpisodeNumber); !slices.Equal(v, []string{value}) {
			t.Errorf("EPISODENUMBER %s read back as %v", value, v)
		}
	}
}

// A chunk table that points into no mdat gives no essence of its own: the mdats stand, and a tag
// edit keeps the digest.
func TestMP4ChunkOutsideEveryMdatFallsBack(t *testing.T) {
	data := mp4AV(0x11, nil)
	stco := bytes.Index(data, []byte("stco"))
	copy(data[stco+12:], testfile.BE32(40)) // the first chunk, moved into the moov
	digest := essenceOf(t, data)
	if digest.ExtentVersion != "mp4-mdat-v3" {
		t.Fatalf("extent = %q, want mp4-mdat-v3", digest.ExtentVersion)
	}
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, strings.Repeat("T", 3000)).Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	if d := essenceOf(t, applyToBytes(t, data, plan)); !d.Equal(digest) {
		t.Errorf("digest after a tag edit %s, want %s", d, digest)
	}
}

// A chapter edit that clears the last tag of a file whose meta holds no ilst creates none.
func TestMP4ChapterEditCreatesNoEmptyIlstInAMeta(t *testing.T) {
	data := testfile.MP4AssembleUdta(mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "T")), testfile.MP4Meta(testfile.MP4HdlrMdir()))
	plan, err := mustParseBytes(t, data).Edit().SetChapters(chaptersFromTitles("A")...).Clear(tag.Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("ilst")) || bytes.Contains(out, []byte("free")) {
		t.Error("the write created an empty ilst or padding")
	}
	if re := mustParseBytes(t, out); re.Tags().Len() != 0 || len(re.Chapters()) != 1 {
		t.Errorf("keys %v, %d chapters", re.Tags().Keys(), len(re.Chapters()))
	}
}

// A keys item that is no text warns when it hides a value a key would show: Apple's user rating,
// or an entry named for a canonical key. Other binary items, which an iPhone recording is full of,
// are kept without a word.
func TestMP4MdtaBinaryItemsWarnOnlyWhereAKeyIsHidden(t *testing.T) {
	float := testfile.MP4Data(23, []byte{0x40, 0xA0, 0, 0})
	data := testfile.MP4Assemble(testfile.MP4HdlrMdta(),
		testfile.MP4Keys("com.apple.quicktime.rating.user", "com.apple.quicktime.location.accuracy.horizontal", "com.apple.quicktime.live-photo.auto", "title", "artist"),
		testfile.MP4Ilst(testfile.MP4KeyItemData(1, float), testfile.MP4KeyItemData(2, float), testfile.MP4KeyItemData(3, testfile.MP4Data(65, []byte{1})),
			testfile.MP4KeyItemData(4, float), testfile.MP4KeyItem(5, "A")))
	var named []string
	for _, w := range mustParseBytes(t, data).Warnings() {
		if w.Code == wl.WarnInvalidTagKey {
			named = append(named, w.Message)
		}
	}
	if len(named) != 2 || !strings.Contains(named[0], "rating.user") || !strings.Contains(named[1], "title") {
		t.Errorf("invalid-tag-key warnings = %q, want the rating and the title entry", named)
	}
}

// A text atom with entries in other languages keeps them when the value outgrows it; the warning
// does not say the atom is gone.
func TestMP4UdtaTextReducedKeepsOtherLanguages(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9cmt", mp4QTTextEntry(langUnd, "Old"), mp4QTTextEntry(langDeu, "Alt")),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9cmt", "Old"))))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, strings.Repeat("c", 70000)).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var msg string
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnNativeValueReduced {
			msg = w.Message
		}
	}
	out := applyToBytes(t, data, plan)
	if msg == "" || strings.Contains(msg, "removed") || !bytes.Contains(out, []byte("Alt")) || !slices.Contains(mp4UdtaKids(t, out), "\xa9cmt") {
		t.Errorf("warning %q; the atom and its other entry must stay", msg)
	}
}

// An unrelated edit that moves a value to the atom of its key says so; an edit that respells
// nothing does not.
func TestMP4RespelledItemIsReported(t *testing.T) {
	for name, c := range map[string]struct {
		item      []byte
		respelled bool
	}{
		"a freeform under an alias": {mp4Freeform("com.apple.iTunes", "DATE", "2020"), true},
		"the key's own atom":        {testfile.MP4Text("\xa9day", "2020"), false},
	} {
		data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), c.item)
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "A").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		ops := strings.Join(plan.Report().Operations, "; ")
		if strings.Contains(ops, "respelled") != c.respelled || c.respelled && !strings.Contains(ops, "1 ilst item respelled") {
			t.Errorf("%s: operations %q", name, ops)
		}
		if d, _ := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tag.RecordingDate); !slices.Equal(d, []string{"2020"}) {
			t.Errorf("%s: RECORDINGDATE = %v", name, d)
		}
	}
	// An edit of the key itself is no respelling to report.
	data := testfile.MP4Tagged(mp4Freeform("com.apple.iTunes", "DATE", "2020"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.RecordingDate, "2021").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if ops := strings.Join(plan.Report().Operations, "; "); strings.Contains(ops, "respelled") {
		t.Errorf("operations %q", ops)
	}
}
