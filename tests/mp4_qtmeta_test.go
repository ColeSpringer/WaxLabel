package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// QuickTime language codes used by the udta text fixtures below: langUnd is the packed
// ISO-639-2 "und" ffmpeg writes, langEng is packed "eng", and langDeu is packed "deu".
const (
	langUnd uint16 = 0x55C4
	langEng uint16 = 0x15C7
	langDeu uint16 = 0x0EB5
)

// An ffmpeg "+use_metadata_tags" file keys its ilst items by index into a keys box holding bare
// names. Without the keys index every item falls to the unknown-atom branch and the file reports
// no tags at all.
func TestMP4MdtaBareKeysRead(t *testing.T) {
	data := testfile.MP4MdtaFile(
		[]string{"title", "artist", "encoder"},
		[]string{"Keys Title", "Keys Artist", "Lavf62.3.100"},
	)
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Keys Title" {
		t.Errorf("TITLE = %q, want %q", got, "Keys Title")
	}
	if v, _ := doc.Tags().Get(tag.Artist); len(v) != 1 || v[0] != "Keys Artist" {
		t.Errorf("ARTIST = %v, want [Keys Artist]", v)
	}
	if v, _ := doc.Tags().Get(tag.Encoder); len(v) != 1 || v[0] != "Lavf62.3.100" {
		t.Errorf("ENCODER = %v, want [Lavf62.3.100]", v)
	}
}

// Apple's recorders write the reverse-DNS key form. Stripping the prefix puts both producers on
// the same vocabulary.
func TestMP4MdtaApplePrefixedKeysRead(t *testing.T) {
	data := testfile.MP4MdtaFile(
		[]string{"com.apple.quicktime.title", "com.apple.quicktime.creationdate", "com.apple.quicktime.software"},
		[]string{"Apple Title", "2021-05-04", "QuickTime 10.5"},
	)
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Apple Title" {
		t.Errorf("TITLE = %q, want %q", got, "Apple Title")
	}
	if v, _ := doc.Tags().Get(tag.RecordingDate); len(v) != 1 || v[0] != "2021-05-04" {
		t.Errorf("DATE = %v, want [2021-05-04] (creationdate)", v)
	}
	if v, _ := doc.Tags().Get(tag.Encoder); len(v) != 1 || v[0] != "QuickTime 10.5" {
		t.Errorf("ENCODER = %v, want [QuickTime 10.5] (software)", v)
	}
}

// key outside the vocabulary contributes nothing but must survive a rewrite verbatim, the same
// treatment an unrecognized four-cc atom gets.
func TestMP4MdtaUnknownKeyPreserved(t *testing.T) {
	data := testfile.MP4MdtaFile(
		[]string{"title", "custom"},
		[]string{"Before", "Keeper"},
	)
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Before" {
		t.Fatalf("TITLE = %q, want %q", got, "Before")
	}
	plan, err := doc.Edit().Set(tag.Title, "After").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	re := mustParseBytes(t, out)
	if got := re.Fields().Title; got != "After" {
		t.Errorf("rewritten TITLE = %q, want %q", got, "After")
	}
	if !strings.Contains(string(out), "Keeper") {
		t.Error("the unmapped mdta key's value was dropped by the rewrite; it must be preserved verbatim")
	}
	if !strings.Contains(string(out), "custom") {
		t.Error("the unmapped mdta key's name was dropped from the keys index")
	}
}

// plain .mov keeps its tags as direct udta children with no meta box at all.
func TestMP4UdtaTextRead(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "QT Title")),
		mp4UdtaText("\xa9swr", mp4QTTextEntry(langUnd, "Lavf62.3.100")),
	)
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "QT Title" {
		t.Errorf("TITLE = %q, want %q", got, "QT Title")
	}
	if v, _ := doc.Tags().Get(tag.Encoder); len(v) != 1 || v[0] != "Lavf62.3.100" {
		t.Fatalf("ENCODER = %v, want [Lavf62.3.100] (the \\xa9swr software atom)", v)
	}
	found := false
	for _, fi := range doc.Lint() {
		if fi.Code == "inherited-encoder" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an inherited-encoder finding for the udta Lavf stamp; findings = %+v", doc.Lint())
	}
}

// Several [size][language]<text> entries can sit back to back in one atom (the source of
// ffprobe's "title-eng"). The first undefined/English entry supplies the canonical value, and
// every other entry survives a rewrite verbatim.
func TestMP4UdtaMultiLanguageCanonicalValue(t *testing.T) {
	data := testfile.MP4AssembleUdta(mp4UdtaText("\xa9nam",
		mp4QTTextEntry(langDeu, "Deutscher Titel"),
		mp4QTTextEntry(langEng, "English Title"),
	))
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "English Title" {
		t.Errorf("TITLE = %q, want the English entry, not the leading German one", got)
	}
}

// when both stores hold a key, the ilst is canonical and the udta value is a family entry, so the
// disagreement reaches conflicting-families without the two values merging into a multi-value the
// next write would store as one.
func TestMP4UdtaIlstDisagreementConflicts(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Udta Title")),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Ilst Title"))),
	)
	doc := mustParseBytes(t, data)
	conflict := false
	for _, f := range doc.Families() {
		if f.Family == wl.FamilyMP4 && f.Key == tag.Title && !f.Selected {
			conflict = true
		}
	}
	if !conflict {
		t.Errorf("expected an unselected MP4 Title family entry; families = %+v", doc.Families())
	}
	found := false
	for _, fi := range doc.Lint() {
		if fi.Code == "conflicting-families" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a conflicting-families finding; findings = %+v", doc.Lint())
	}
	if v, _ := doc.Tags().Get(tag.Title); len(v) != 1 || v[0] != "Ilst Title" {
		t.Errorf("TITLE = %v, want the ilst's single value; the udta value belongs in families", v)
	}
	udtaSeen := false
	for _, f := range doc.Families() {
		if f.Key == tag.Title && slices.Contains(f.Values, "Udta Title") {
			udtaSeen = true
		}
	}
	if !udtaSeen {
		t.Errorf("the udta value must stay visible as a family entry; families = %+v", doc.Families())
	}
}

// edit touching neither store must not launder a disagreement away.
func TestMP4UdtaIlstDisagreementSurvivesUnrelatedEdit(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Udta Title")),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Ilst Title"))),
	)
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.Artist, "Unrelated").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if v, _ := re.Tags().Get(tag.Title); len(v) != 1 {
		t.Errorf("TITLE = %v, want one value; the disagreement must not become a multi-value", v)
	}
	// The write syncs udta to the ilst, so the two agree and no conflict remains, but they must
	// agree on the ilst's value, not by having absorbed both.
	if v, _ := re.Tags().Get(tag.Title); len(v) != 1 || v[0] != "Ilst Title" {
		t.Errorf("TITLE = %v, want [Ilst Title]", v)
	}
	for _, fi := range re.Lint() {
		if fi.Code == "single-valued-multi" {
			t.Errorf("the disagreement was laundered into a multi-value:\n%+v", re.Families())
		}
	}
}

// A udta text atom holds one value of at most 65535 bytes. A value the ilst takes and the atom
// cannot leaves no atom: one cut short, or holding the first of several, would disagree with it.
func TestMP4UdtaTextThatCannotHoldTheValueIsRemoved(t *testing.T) {
	long := strings.Repeat("c", 70000)
	withIlst := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9cmt", mp4QTTextEntry(langUnd, "Old")), mp4UdtaText("\xa9ART", mp4QTTextEntry(langUnd, "Old")),
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Kept")),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9cmt", "Old"), testfile.MP4Text("\xa9ART", "Old"), testfile.MP4Text("\xa9nam", "Kept"))),
	)
	udtaOnly := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9cmt", mp4QTTextEntry(langUnd, "Old")), mp4UdtaText("\xa9ART", mp4QTTextEntry(langUnd, "Old")),
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Kept")))
	for name, data := range map[string][]byte{"beside an ilst": withIlst, "udta alone": udtaOnly} {
		for edit, c := range map[string]struct {
			key  tag.Key
			vals []string
			atom string
		}{
			"a 70,000-byte comment": {tag.Comment, []string{long}, "\xa9cmt"},
			"two artists":           {tag.Artist, []string{"One", "Two"}, "\xa9ART"},
		} {
			t.Run(name+", "+edit, func(t *testing.T) {
				plan, err := mustParseBytes(t, data).Edit().Set(c.key, c.vals...).Prepare()
				if err != nil {
					t.Fatal(err)
				}
				if !slices.ContainsFunc(plan.Report().Warnings, func(w wl.Warning) bool {
					return w.Code == wl.WarnNativeValueReduced && slices.Contains(w.Keys, c.key)
				}) {
					t.Errorf("warnings = %v, want native-value-reduced for %s", plan.Report().Warnings, c.key)
				}
				out := applyToBytes(t, data, plan)
				kids := mp4UdtaKids(t, out)
				if slices.Contains(kids, c.atom) || !slices.Contains(kids, "\xa9nam") {
					t.Errorf("udta holds %q, want %q gone and the title atom kept", kids, c.atom)
				}
				re := mustParseBytes(t, out)
				if v, _ := re.Get(c.key); !slices.Equal(v, c.vals) {
					t.Errorf("%s read back as %d value(s)", c.key, len(v))
				}
				if hasLintCode(re, "conflicting-families") {
					t.Errorf("the stores disagree after the write: %v", re.Lint())
				}
			})
		}
	}
}

// The removal is reported by a chapter write that carries the tag edit too, with a chapter track
// and without one (no track id is free).
func TestMP4UdtaTextRemovalReportedByAChapterWrite(t *testing.T) {
	kids := testfile.MP4Atom("udta", slices.Concat(mp4UdtaText("\xa9cmt", mp4QTTextEntry(langUnd, "Old")),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9cmt", "Old")))))
	for name, id := range map[string]int{"chapter track": 1, "chpl alone": -1} { // -1: track id 0xFFFFFFFF
		data := testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
			return slices.Concat(testfile.MP4Mvhd(1000, 1000), testfile.MP4SoundTrak(id, audio), kids)
		})
		plan, err := mustParseBytes(t, data).Edit().SetChapters(chaptersFromTitles("A")...).Set(tag.Comment, strings.Repeat("c", 70000)).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		ops := strings.Join(plan.Report().Operations, "; ")
		if strings.Contains(ops, "QuickTime track") != (name == "chapter track") {
			t.Fatalf("%s: operations %q", name, ops)
		}
		if !reportHasWarning(plan.Report().Warnings, wl.WarnNativeValueReduced) {
			t.Errorf("%s: warnings %v, want native-value-reduced", name, plan.Report().Warnings)
		}
		if kids := mp4UdtaKids(t, applyToBytes(t, data, plan)); slices.Contains(kids, "\xa9cmt") {
			t.Errorf("%s: udta holds %q", name, kids)
		}
	}
}

// A padding floor is applied by a write that changes no value, on the path that syncs a udta atom.
func TestMP4PaddingFloorAppliedWithAUdtaSync(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Udta")),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Ilst Title"))),
	)
	plan, err := mustParseBytes(t, data).Edit().Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 4096, Min: 4096, ReuseInPlace: true}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() || plan.Report().PaddingAfter != 4096 {
		t.Errorf("no-op %v, PaddingAfter %d, want the floor reserved", plan.IsNoOp(), plan.Report().PaddingAfter)
	}
}

// A keys item whose value is no text (Apple's user rating is a float) shows under no key; the
// file says so, and an edit keeps the item.
func TestMP4MdtaNonTextItemWarned(t *testing.T) {
	rating := testfile.MP4KeyItemData(2, testfile.MP4Data(23, []byte{0x40, 0xA0, 0, 0}))
	data := testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "com.apple.quicktime.rating.user", "com.apple.quicktime.artwork"),
		testfile.MP4Ilst(testfile.MP4KeyItem(1, "T"), rating, rating, testfile.MP4KeyItemData(3, testfile.MP4Data(13, testfile.JPEG()))))
	doc := mustParseBytes(t, data)
	var named []string
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnInvalidTagKey {
			named = append(named, w.Message)
		}
	}
	if len(named) != 1 || !strings.Contains(named[0], "com.apple.quicktime.rating.user") {
		t.Errorf("invalid-tag-key warnings = %q, want one naming the rating key", named)
	}
	if doc.Tags().Len() != 1 {
		t.Errorf("keys = %v, want the title alone", doc.Tags().Keys())
	}
	plan, err := doc.Edit().Set(tag.Title, "Other").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(applyToBytes(t, data, plan), rating) {
		t.Error("the rating item did not survive an edit of the title")
	}
}

// An edit that stores nothing writes nothing, the sync of a disagreeing udta atom included.
func TestMP4DiscardedEditDoesNotSyncUdta(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Udta")),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Ilst Title"))),
	)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.BPM, "abc").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("planned %q", plan.Report().Operations)
	}
}

// udta child outside the text vocabulary is not decoded and must survive a rewrite through the
// verbatim udta splice.
func TestMP4UdtaUnmappedAtomPreserved(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Before")),
		testfile.MP4Atom("XTRA", []byte("opaque-user-data")),
	)
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.Artist, "Someone").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !strings.Contains(string(out), "opaque-user-data") {
		t.Error("an unmapped udta atom was dropped by the rewrite")
	}
	if v, _ := mustParseBytes(t, out).Tags().Get(tag.Artist); len(v) != 1 || v[0] != "Someone" {
		t.Errorf("ARTIST = %v, want [Someone]", v)
	}
}

// A set on a keys-indexed file must be written as a keys entry plus an index-keyed item, not as
// a four-character "\xa9nam" atom inside an mdta box where nothing will read it.
func TestMP4MdtaWriteStaysKeyed(t *testing.T) {
	data := testfile.MP4MdtaFile([]string{"title"}, []string{"Before"})
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.Title, "After").Set(tag.Album, "Fresh").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("\xa9nam")) {
		t.Error("the write emitted a four-cc \\xa9nam atom into an mdta store")
	}
	if !bytes.Contains(out, []byte("album")) {
		t.Error("the new key was not added to the keys index")
	}
	re := mustParseBytes(t, out)
	if got := re.Fields().Title; got != "After" {
		t.Errorf("TITLE = %q, want After", got)
	}
	if v, _ := re.Tags().Get(tag.Album); len(v) != 1 || v[0] != "Fresh" {
		t.Errorf("ALBUM = %v, want [Fresh]", v)
	}
	// Re-applying the same edit must be a no-op: the keys index is carried forward by position,
	// so a second write reuses every entry and produces identical bytes.
	plan2, err := re.Edit().Set(tag.Title, "After").Set(tag.Album, "Fresh").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.IsNoOp() {
		t.Errorf("re-applying the same tags should be a no-op; operations: %v", plan2.Report().Operations)
	}
}

// file whose only tag store is udta-level text atoms is edited there, with no meta/ilst created
// beside it, so ffprobe reports one title, not the two a second store would produce.
func TestMP4UdtaOnlyWritesInPlace(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Before")),
		mp4UdtaText("\xa9swr", mp4QTTextEntry(langUnd, "Lavf62.3.100")),
	)
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.Title, "After").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("ilst")) {
		t.Error("a udta-only file gained an ilst; the edit belongs in the store the file already has")
	}
	re := mustParseBytes(t, out)
	if got := re.Fields().Title; got != "After" {
		t.Errorf("TITLE = %q, want After", got)
	}
	if v, _ := re.Tags().Get(tag.Encoder); len(v) != 1 || v[0] != "Lavf62.3.100" {
		t.Errorf("ENCODER = %v, want the untouched [Lavf62.3.100]", v)
	}
}

// rewriting a multi-language atom replaces only the canonical entry; the other translations survive
// verbatim.
func TestMP4UdtaKeepsOtherLanguagesOnWrite(t *testing.T) {
	data := testfile.MP4AssembleUdta(mp4UdtaText("\xa9nam",
		mp4QTTextEntry(langEng, "English Title"),
		mp4QTTextEntry(langDeu, "Deutscher Titel"),
	))
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.Title, "New English").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("Deutscher Titel")) {
		t.Error("the German entry was dropped; entries beyond the canonical one must be preserved")
	}
	if got := mustParseBytes(t, out).Fields().Title; got != "New English" {
		t.Errorf("TITLE = %q, want New English", got)
	}
}

// when both stores exist the ilst is the write target, and the udta-level atom for the same
// canonical key is rewritten to match, so the two cannot disagree after a write.
func TestMP4UdtaSyncedWithIlst(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Stale Title")),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Stale Title"))),
	)
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.Title, "Synced").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("Stale Title")) {
		t.Error("the udta-level atom kept the old value; the two stores would then disagree")
	}
	re := mustParseBytes(t, out)
	if v, _ := re.Tags().Get(tag.Title); len(v) != 1 || v[0] != "Synced" {
		t.Errorf("TITLE = %v, want a single [Synced] from the two agreeing stores", v)
	}
	for _, f := range re.Families() {
		if f.Key == tag.Title && !f.Selected {
			t.Error("the two stores agree after the write; the family must not be marked conflicting")
		}
	}
}

func TestMP4UdtaClearedRemovesAtom(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Doomed")),
		mp4UdtaText("\xa9ART", mp4QTTextEntry(langUnd, "Kept")),
	)
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Clear(tag.Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("Doomed")) {
		t.Error("the cleared key's udta atom survived the write")
	}
	re := mustParseBytes(t, out)
	if re.Tags().Has(tag.Title) {
		t.Errorf("TITLE still present after a clear: %+v", re.Tags())
	}
	if v, _ := re.Tags().Get(tag.Artist); len(v) != 1 || v[0] != "Kept" {
		t.Errorf("ARTIST = %v, want the untouched [Kept]", v)
	}
}

// interoperability proof for both QuickTime stores: after WaxLabel edits an ffmpeg-authored
// "+use_metadata_tags" M4A and a plain .mov, ffprobe must read back exactly the values written,
// once each.
func TestDifferentialFFprobeReadsQuickTimeStores(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()
	cases := []struct {
		name string
		file string
		args []string
	}{
		{"mdta-keys", "keys.m4a", []string{"-movflags", "+use_metadata_tags"}},
		{"udta-text", "plain.mov", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := dir + "/" + c.file
			args := []string{"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "aac"}
			args = append(args, c.args...)
			args = append(args, "-metadata", "title=Original", "-metadata", "track=3/12",
				"-metadata", "album_artist=Original Band", path)
			if err := exec.Command("ffmpeg", args...).Run(); err != nil {
				t.Fatalf("ffmpeg author %s: %v", c.file, err)
			}
			doc := mustParseFile(t, path)
			if got := doc.Fields().Title; got != "Original" {
				t.Fatalf("read back TITLE = %q, want Original (the store was not decoded)", got)
			}
			edit := doc.Edit().
				Set(tag.Title, "Written By WaxLabel").
				Set(tag.Artist, "Written Artist").
				Set(tag.Album, "Written Album").
				Set(tag.Comment, "Written Comment")
			want := map[string]string{
				"title":   "Written By WaxLabel",
				"artist":  "Written Artist",
				"album":   "Written Album",
				"comment": "Written Comment",
			}
			// The names ffmpeg gave the keys index are the ones read and written. A plain .mov
			// stores neither field in a udta text atom ffmpeg wrote.
			if c.name == "mdta-keys" {
				for k, v := range map[tag.Key]string{tag.TrackNumber: "3", tag.TrackTotal: "12", tag.AlbumArtist: "Original Band"} {
					if got, _ := doc.Get(k); !slices.Equal(got, []string{v}) {
						t.Errorf("%s = %v, want [%s]", k, got, v)
					}
				}
				edit = edit.Set(tag.TrackNumber, "5").Set(tag.AlbumArtist, "Written Band").
					Set(tag.TitleSort, "Written Sort").Set(tag.Compilation, "1")
				want["track"], want["album_artist"] = "5/12", "Written Band"
				want["sort_name"], want["compilation"] = "Written Sort", "1"
			}
			plan, err := edit.Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
				t.Fatal(err)
			}
			tags := ffprobeFormatTags(t, path)
			for probeKey, want := range want {
				if got := lookupCI(tags, probeKey); got != want {
					t.Errorf("ffprobe %s = %q, want %q (all tags: %v)", probeKey, got, want, tags)
				}
			}
			// The edit must not have moved the file to a second store: every value reaches
			// ffprobe once, and WaxLabel reads back exactly what it wrote.
			re := mustParseFile(t, path)
			if v, _ := re.Tags().Get(tag.Title); len(v) != 1 || v[0] != "Written By WaxLabel" {
				t.Errorf("re-read TITLE = %v, want one [Written By WaxLabel]", v)
			}
		})
	}
}

// ffprobeFormatTags returns a file's container-level tags as ffprobe reports them.
func ffprobeFormatTags(t *testing.T, path string) map[string]string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_entries", "format_tags", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var probe struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("parse ffprobe json: %v\n%s", err, out)
	}
	return probe.Format.Tags
}

// meta declaring the mdta handler but carrying no keys box is a broken file whose items resolve to
// nothing. Encoding index-keyed items into it would name entries in a table that does not exist, so
// the write falls back to the four-character encoder and the values still read back.
func TestMP4MdtaWithoutKeysBoxFallsBack(t *testing.T) {
	data := testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Before")))
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Before" {
		t.Fatalf("TITLE = %q, want Before (an unresolved item falls through to the four-cc dispatch)", got)
	}
	plan, err := doc.Edit().Set(tag.Title, "After").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("\xa9nam")) {
		t.Error("the write left no four-cc atom; with no keys box an index-keyed item is unreadable")
	}
	if got := mustParseBytes(t, out).Fields().Title; got != "After" {
		t.Errorf("TITLE = %q, want After", got)
	}
}

// An ilst item whose four-cc reads as a keys index the table does not cover resolves to nothing
// and is preserved verbatim. The index is compared as an unsigned value: a name above 2^31
// turns negative under a 32-bit int, which would pass the bounds check and panic the lookup.
func TestMP4MdtaOutOfRangeIndexPreserved(t *testing.T) {
	// "\xa9nam" reads as the index 0xA96E616D (2842583405), far past a one-entry table.
	data := testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Keys("title"),
		testfile.MP4Ilst(testfile.MP4KeyItem(1, "Kept"), testfile.MP4Text("\xa9nam", "Out Of Range")))
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Kept" {
		t.Errorf("TITLE = %q, want Kept (from the in-range keys entry)", got)
	}
	plan, err := doc.Edit().Set(tag.Album, "Fresh").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(applyToBytes(t, data, plan), []byte("Out Of Range")) {
		t.Error("the out-of-range item was dropped; an unresolved item must be preserved verbatim")
	}
}

// Cover art in an mdta store, under the key name WaxLabel once wrote, under Apple's, and in the
// four-character item ffmpeg also reads there: each is a picture, and a tag edit keeps the item as
// it is.
func TestMP4MdtaCoverSurvivesTagEdit(t *testing.T) {
	jpeg := testfile.MP4Data(13, testfile.JPEG())
	for name, c := range map[string]struct {
		keys  []string
		cover []byte
	}{
		"covr key":          {[]string{"title", "covr"}, testfile.MP4KeyItemData(2, jpeg)},
		"artwork key":       {[]string{"title", "com.apple.quicktime.artwork"}, testfile.MP4KeyItemData(2, jpeg)},
		"four-character":    {[]string{"title"}, testfile.MP4Atom("covr", jpeg)},
		"artwork, two data": {[]string{"title", "com.apple.quicktime.artwork"}, testfile.MP4KeyItemData(2, slices.Concat(jpeg, testfile.MP4Data(14, testfile.PNG())))},
	} {
		t.Run(name, func(t *testing.T) {
			data := testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Keys(c.keys...),
				testfile.MP4Ilst(testfile.MP4KeyItem(1, "Before"), c.cover))
			doc := mustParseBytes(t, data)
			want := len(doc.Pictures())
			if want == 0 || doc.Pictures()[0].MIME != "image/jpeg" {
				t.Fatalf("pictures = %d, want the cover read", want)
			}
			if len(doc.Tags().Keys()) != 1 {
				t.Errorf("keys = %v, want the title alone", doc.Tags().Keys())
			}
			plan, err := doc.Edit().Set(tag.Title, "After").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, data, plan)
			if !bytes.Contains(out, c.cover) || !slices.Equal(mp4KeysNames(t, out), c.keys) {
				t.Errorf("the cover item or the keys index changed under a tag edit: keys %v", mp4KeysNames(t, out))
			}
			re := mustParseBytes(t, out)
			if len(re.Pictures()) != want {
				t.Errorf("pictures after a tag-only edit = %d, want %d", len(re.Pictures()), want)
			}
			if got := re.Fields().Title; got != "After" {
				t.Errorf("TITLE = %q, want After", got)
			}
		})
	}
}

// A cover edit of an mdta store writes the cover under Apple's artwork key, the one ffmpeg reads,
// and leaves no cover under another name.
func TestMP4MdtaCoverEditWritesArtworkKey(t *testing.T) {
	const artwork = "com.apple.quicktime.artwork"
	jpeg := testfile.MP4Data(13, testfile.JPEG())
	png := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()}
	for name, c := range map[string]struct {
		keys  []string
		cover []byte
	}{
		"no cover":       {[]string{"title"}, nil},
		"covr key":       {[]string{"title", "covr"}, testfile.MP4KeyItemData(2, jpeg)},
		"artwork key":    {[]string{"title", artwork}, testfile.MP4KeyItemData(2, jpeg)},
		"four-character": {[]string{"title"}, testfile.MP4Atom("covr", jpeg)},
	} {
		t.Run(name, func(t *testing.T) {
			data := testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Keys(c.keys...),
				testfile.MP4Ilst(testfile.MP4KeyItem(1, "T"), c.cover))
			plan, err := mustParseBytes(t, data).Edit().ClearPictures().AddPicture(png).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, data, plan)
			names := mp4KeysNames(t, out)
			at := slices.Index(names, artwork)
			if at < 0 {
				t.Fatalf("keys = %v, want the artwork key", names)
			}
			if !bytes.Contains(out, testfile.MP4KeyItemData(at+1, testfile.MP4Data(14, testfile.PNG()))) {
				t.Error("no item under the artwork key holds the new cover")
			}
			if bytes.Contains(out, testfile.JPEG()) {
				t.Error("the replaced cover is still in the file")
			}
			re := mustParseBytes(t, out)
			if pics := re.Pictures(); len(pics) != 1 || pics[0].MIME != "image/png" {
				t.Errorf("pictures = %d, want the one PNG", len(pics))
			}

			plan, err = re.Edit().ClearPictures().Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out = applyToBytes(t, out, plan)
			if bytes.Contains(out, testfile.PNG()) || len(mustParseBytes(t, out).Pictures()) != 0 {
				t.Error("the cover survived its clear")
			}
		})
	}
}

// ffprobe shows the cover WaxLabel adds to a keys store ffmpeg wrote.
func TestMP4DifferentialMdtaCover(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	path := filepath.Join(t.TempDir(), "keys.m4a")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:a", "aac", "-movflags", "+use_metadata_tags", "-metadata", "title=Original", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	plan, err := mustParseFile(t, path).Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error", "-select_streams", "v",
		"-show_entries", "stream=codec_name:stream_disposition=attached_pic", "-of", "csv=p=0", path).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "png,1" {
		t.Errorf("ffprobe video streams = %q (%v), want one attached PNG", out, err)
	}
	if tags := ffprobeFormatTags(t, path); tags["title"] != "Original" || len(tags["covr"]) != 0 {
		t.Errorf("ffprobe tags = %v", tags)
	}
}

// clearing a key must not take the atom's other-language entries with it, which qtmeta.go promises
// to preserve verbatim.
func TestMP4UdtaClearKeepsOtherLanguages(t *testing.T) {
	data := testfile.MP4AssembleUdta(mp4UdtaText("\xa9nam",
		mp4QTTextEntry(langEng, "English Title"),
		mp4QTTextEntry(langDeu, "Deutscher Titel"),
	))
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Clear(tag.Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("Deutscher Titel")) {
		t.Error("clearing the canonical value destroyed the other-language entry")
	}
	if bytes.Contains(out, []byte("English Title")) {
		t.Error("the cleared canonical value survived")
	}
	if re := mustParseBytes(t, out); re.Tags().Has(tag.Title) {
		t.Errorf("TITLE still present after a clear: %+v", re.Tags())
	}
}

// atom whose canonical entry is empty contributes no tag, so the key is absent from the edit; the
// delete path must still not take the other-language entries with it.
func TestMP4UdtaEmptyCanonicalEntryKeepsSiblings(t *testing.T) {
	data := testfile.MP4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langEng, ""), mp4QTTextEntry(langDeu, "Nur Deutsch")),
		mp4UdtaText("\xa9ART", mp4QTTextEntry(langUnd, "Someone")),
	)
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.Album, "Fresh").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(applyToBytes(t, data, plan), []byte("Nur Deutsch")) {
		t.Error("an atom whose canonical entry is empty lost its other-language entry")
	}
}

// mdta name that is not a valid key as spelled folds to one (uppercased, other bytes replaced), so
// copy and diff see it, and an edit reuses the file's own spelling rather than adding a second
// entry.
func TestMdtaForeignNamesFoldToCustomKeys(t *testing.T) {
	names := []string{"custom_key", "com.apple.quicktime.author", "org.example.thing"}
	data := testfile.MP4MdtaFile(names, []string{"v1", "Ann", "x"})
	doc := mustParseBytes(t, data)
	for k, want := range map[tag.Key]string{"CUSTOM_KEY": "v1", "AUTHOR": "Ann", "ORG.EXAMPLE.THING": "x"} {
		if v, ok := doc.Get(k); !ok || v[0] != want {
			t.Errorf("%s = %v (%v), want %q", k, v, ok, want)
		}
	}
	plan, err := doc.Edit().Set("CUSTOM_KEY", "v2").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Count(out, []byte("custom_key")) != 1 || bytes.Contains(out, []byte("CUSTOM_KEY")) {
		t.Error("the edit should rewrite the existing custom_key entry, not add CUSTOM_KEY")
	}
	if v, _ := mustParseBytes(t, out).Get("CUSTOM_KEY"); !slices.Equal(v, []string{"v2"}) {
		t.Errorf("CUSTOM_KEY after edit = %v", v)
	}
}

// A chapter write leaves the classic udta text atoms alone, so its plan lists no tag as removed
// and an edit of the returned Document changes the tag where it is.
func TestMP4UdtaTextsSurviveChapterWriteResult(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		key  tag.Key
	}{
		{"synthetic", testfile.MP4AssembleUdta(mp4UdtaText("\xa9nam", mp4QTTextEntry(0, "T"))), tag.Title},
		{"mp3.mov", readFixture(t, "../testdata/mp3.mov"), tag.Encoder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "in.mov")
			if err := os.WriteFile(path, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			doc := mustParseFile(t, path)
			plan, err := doc.Edit().SetChapters(wl.Chapter{Title: "Intro"}).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range plan.Changes() {
				if c.Kind == tag.ChangeRemoved {
					t.Errorf("the plan lists %s as removed", c.Key)
				}
			}
			ret, _, err := plan.Execute(context.Background(), wl.SaveBack())
			if err != nil {
				t.Fatal(err)
			}
			if re := mustParseFile(t, path); !ret.Tags().Equal(re.Tags()) || !re.Tags().Equal(doc.Tags()) {
				t.Errorf("tags: returned %v, reparsed %v, source %v", ret.Tags(), re.Tags(), doc.Tags())
			}
			next, err := ret.Edit().Set(tc.key, "Next").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if ch := next.Changes(); len(ch) != 1 || ch[0].Key != tc.key || ch[0].Kind != tag.ChangeChanged {
				t.Errorf("changes = %+v, want %s changed", ch, tc.key)
			}
		})
	}
}

// A tag edit that follows a chapter write on a keys-indexed file still writes to that store.
func TestMP4MdtaStoreSurvivesChapterThenTagEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "in.m4a")
	if err := os.WriteFile(path, testfile.MP4MdtaFile([]string{"title"}, []string{"Before"}), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	plan, err := mustParseFile(t, path).Edit().SetChapters(wl.Chapter{Title: "Intro"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	ret, _, err := plan.Execute(ctx, wl.SaveBack())
	if err != nil {
		t.Fatal(err)
	}
	if plan, err = ret.Edit().Set(tag.Title, "After").Set(tag.Album, "Fresh").Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	out := readFixture(t, path)
	if bytes.Contains(out, []byte("\xa9nam")) || bytes.Contains(out, []byte("mdirappl")) {
		t.Error("the tag edit wrote iTunes atoms into the keys-indexed store")
	}
	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Album); re.Fields().Title != "After" || !slices.Equal(v, []string{"Fresh"}) || len(re.Chapters()) != 1 {
		t.Errorf("title %q, album %v, %d chapter(s)", re.Fields().Title, v, len(re.Chapters()))
	}
}

// Of several udta text atoms with one name the first holding a value is read. An edit of another
// key keeps them all, and an edit of theirs leaves one, reporting the value only a removed
// duplicate held.
func TestMP4DuplicateUdtaTextFirstWins(t *testing.T) {
	nam := func(text string) []byte { return mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, text)) }
	art := mp4UdtaText("\xa9ART", mp4QTTextEntry(langUnd, "Artist"))
	cases := []struct {
		name  string
		data  []byte
		lossy bool // a duplicate holds a value the file holds nowhere else
	}{
		{"second value", testfile.MP4AssembleUdta(nam("First"), art, nam("Second")), true},
		{"same value", testfile.MP4AssembleUdta(nam("First"), art, nam("First")), false},
		{"empty first", testfile.MP4AssembleUdta(nam(""), art, nam("First")), false},
		{"beside an ilst", testfile.MP4AssembleUdta(nam("First"), nam("Second"), art,
			testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "First")))), true},
	}
	namCount := func(data []byte) int {
		n := 0
		for _, k := range mp4UdtaKids(t, data) {
			if k == "\xa9nam" {
				n++
			}
		}
		return n
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParseBytes(t, tc.data)
			if v, _ := doc.Get(tag.Title); !hasWarning(doc, wl.WarnDuplicateTagBlock) || !slices.Equal(v, []string{"First"}) || nativeDuplicates(doc) != 1 {
				t.Fatalf("read: warnings %v, TITLE %q, %d marked duplicate; want duplicate-tag-block, First and 1",
					warningCodes(doc), v, nativeDuplicates(doc))
			}

			plan, err := doc.Edit().Set(tag.Artist, "Unrelated").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, tc.data, plan)
			if v, _ := mustParseBytes(t, out).Get(tag.Title); namCount(out) != 2 || !slices.Equal(v, []string{"First"}) || planHasWarning(plan, wl.WarnDuplicateTagBlockDropped) {
				t.Errorf("unrelated edit: %d \\xa9nam atom(s), TITLE %q, report %+v", namCount(out), v, plan.Report().Warnings)
			}

			if plan, err = doc.Edit().Set(tag.Title, "Next").Prepare(); err != nil {
				t.Fatal(err)
			}
			out = applyToBytes(t, tc.data, plan)
			re := mustParseBytes(t, out)
			if v, _ := re.Get(tag.Title); namCount(out) != 1 || !slices.Equal(v, []string{"Next"}) || hasWarning(re, wl.WarnDuplicateTagBlock) {
				t.Errorf("set: %d \\xa9nam atom(s), TITLE %q, warnings %v", namCount(out), v, warningCodes(re))
			}
			w, dropped := warningFor(plan, wl.WarnDuplicateTagBlockDropped)
			if dropped != tc.lossy || dropped && !slices.Equal(w.Keys, []tag.Key{tag.Title}) {
				t.Errorf("set: duplicate-tag-block-dropped = %v keyed %v, want %v keyed TITLE", dropped, w.Keys, tc.lossy)
			}

			if plan, err = doc.Edit().Clear(tag.Title).Prepare(); err != nil {
				t.Fatal(err)
			}
			out = applyToBytes(t, tc.data, plan)
			if re := mustParseBytes(t, out); namCount(out) != 0 || re.Tags().Has(tag.Title) {
				t.Errorf("clear: %d \\xa9nam atom(s), tags %v", namCount(out), re.Tags())
			}
		})
	}
}

// mp4KeysNames reads the names a file's keys box indexes.
func mp4KeysNames(t *testing.T, data []byte) []string {
	t.Helper()
	p := mp4BoxPayload(t, data, "keys")
	var names []string
	for pos := 8; pos+8 <= len(p); {
		size := int(binary.BigEndian.Uint32(p[pos:]))
		names = append(names, string(p[pos+8:pos+size]))
		pos += size
	}
	return names
}

// ffmpeg's "+use_metadata_tags" names read as the keys the iTunes atoms of the same fields do, so
// a value shows under one name whichever store holds it.
func TestMP4MdtaFFmpegNamesRead(t *testing.T) {
	want := []struct {
		name, value string
		key         tag.Key
		read        string
	}{
		{"album_artist", "AA", tag.AlbumArtist, "AA"},
		{"track", "3/12", tag.TrackNumber, "3"},
		{"disc", "1/2", tag.DiscNumber, "1"},
		{"compilation", "1", tag.Compilation, "1"},
		{"media_type", "2", tag.MediaType, "2"},
		{"sort_name", "SN", tag.TitleSort, "SN"},
		{"sort_artist", "SA", tag.ArtistSort, "SA"},
		{"sort_album", "SAL", tag.AlbumSort, "SAL"},
		{"sort_album_artist", "SAA", tag.AlbumArtistSort, "SAA"},
		{"sort_composer", "SC", tag.ComposerSort, "SC"},
		{"sort_show", "SS", tag.ShowSort, "SS"},
		{"synopsis", "SYN", tag.LongDescription, "SYN"},
		{"gapless_playback", "1", tag.ITunesGapless, "1"},
		{"rating", "1", tag.ITunesAdvisory, "1"},
		{"tmpo", "120", tag.BPM, "120"},
		{"show", "SH", tag.Show, "SH"},
		{"episode_id", "E1", tag.EpisodeID, "E1"},
		{"network", "NET", tag.Network, "NET"},
		{"episode_sort", "4", tag.EpisodeNumber, "4"},
		{"season_number", "2", tag.SeasonNumber, "2"},
	}
	var names, values []string
	for _, w := range want {
		names, values = append(names, w.name), append(values, w.value)
	}
	doc := mustParseBytes(t, testfile.MP4MdtaFile(names, values))
	for _, w := range want {
		if v, _ := doc.Get(w.key); !slices.Equal(v, []string{w.read}) {
			t.Errorf("%s (mdta %q) = %v, want [%s]", w.key, w.name, v, w.read)
		}
	}
	for k, total := range map[tag.Key]string{tag.TrackTotal: "12", tag.DiscTotal: "2"} {
		if v, _ := doc.Get(k); !slices.Equal(v, []string{total}) {
			t.Errorf("%s = %v, want [%s] from the slash pair", k, v, total)
		}
	}
	if n := doc.Tags().Len(); n != len(want)+2 {
		t.Errorf("%d keys, want %d: %v", n, len(want)+2, doc.Tags().Keys())
	}
}

// The number and total of an mdta "track" entry are one value there, as ffmpeg writes it.
func TestMP4MdtaTrackPairStaysOneEntry(t *testing.T) {
	data := testfile.MP4MdtaFile([]string{"title", "track"}, []string{"T", "3/12"})
	edit := func(t *testing.T, e func(*wl.Editor) *wl.Editor) []byte {
		t.Helper()
		plan, err := e(mustParseBytes(t, data).Edit()).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if names := mp4KeysNames(t, out); !slices.Equal(names, []string{"title", "track"}) {
			t.Errorf("keys = %v, want the two the file had", names)
		}
		return out
	}
	t.Run("number edit", func(t *testing.T) {
		out := edit(t, func(e *wl.Editor) *wl.Editor { return e.Set(tag.TrackNumber, "5") })
		if !bytes.Contains(out, testfile.MP4Data(1, []byte("5/12"))) {
			t.Error("the track entry does not hold 5/12")
		}
		re := mustParseBytes(t, out)
		if n, _ := re.Get(tag.TrackNumber); !slices.Equal(n, []string{"5"}) {
			t.Errorf("TRACKNUMBER = %v, want [5]", n)
		}
		if n, _ := re.Get(tag.TrackTotal); !slices.Equal(n, []string{"12"}) {
			t.Errorf("TRACKTOTAL = %v, want [12]", n)
		}
	})
	t.Run("unrelated edit", func(t *testing.T) {
		out := edit(t, func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "Other") })
		if !bytes.Contains(out, testfile.MP4KeyItem(2, "3/12")) {
			t.Error("the track item changed under an edit of the title")
		}
	})
	t.Run("cleared by its alias", func(t *testing.T) {
		out := edit(t, func(e *wl.Editor) *wl.Editor { return e.Clear("TRACK") })
		re := mustParseBytes(t, out)
		if re.Tags().Has(tag.TrackNumber) || re.Tags().Has("TRACK") {
			t.Errorf("the track number survived its clear: %v", re.Tags().Keys())
		}
		if n, _ := re.Get(tag.TrackTotal); !slices.Equal(n, []string{"12"}) {
			t.Errorf("TRACKTOTAL = %v, want [12]", n)
		}
	})
	t.Run("both cleared", func(t *testing.T) {
		plan, err := mustParseBytes(t, data).Edit().Clear(tag.TrackNumber).Clear(tag.TrackTotal).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		re := mustParseBytes(t, applyToBytes(t, data, plan))
		if re.Tags().Has(tag.TrackNumber) || re.Tags().Has(tag.TrackTotal) {
			t.Errorf("keys after clearing both: %v", re.Tags().Keys())
		}
	})
}

// A keys item keeps the encoding the file gave it under an edit elsewhere: here an implicit-typed
// text, and an item under the second of two entries of one name.
func TestMP4MdtaItemKeepsItsEncoding(t *testing.T) {
	implicit := testfile.MP4KeyItemData(2, testfile.MP4Data(0, []byte("Artist")))
	second := testfile.MP4KeyItem(4, "Album")
	data := testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "artist", "album", "album"),
		testfile.MP4Ilst(testfile.MP4KeyItem(1, "T"), implicit, second))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Other").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, implicit) || !bytes.Contains(out, second) {
		t.Error("an edit of the title re-encoded or re-keyed another item")
	}
	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Album); !slices.Equal(v, []string{"Album"}) || re.Fields().Title != "Other" {
		t.Errorf("after the edit: ALBUM %v, TITLE %q", v, re.Fields().Title)
	}
}

// A keys store with a total in the track entry and in an entry of its own. The first is read; an
// edit elsewhere keeps both entries as they are; an edit of the pair leaves the total where the
// one read was, and reports an entry removed only when one is.
func TestMP4MdtaTotalHeldTwice(t *testing.T) {
	t.Run("own entry first", func(t *testing.T) {
		data := testfile.MP4MdtaFile([]string{"TOTALTRACKS", "track", "title"}, []string{"15", "3/12", "T"})
		doc := mustParseBytes(t, data)
		n, _ := doc.Get(tag.TrackNumber)
		total, _ := doc.Get(tag.TrackTotal)
		if !slices.Equal(n, []string{"3"}) || !slices.Equal(total, []string{"15"}) || !hasLintCode(doc, "conflicting-families") {
			t.Errorf("TRACKNUMBER %v, TRACKTOTAL %v, lint %v; want 3, 15 and the conflict", n, total, doc.Lint())
		}
		plan, err := doc.Edit().Set(tag.Title, "Other").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if out := applyToBytes(t, data, plan); !bytes.Contains(out, testfile.MP4KeyItem(1, "15")) || !bytes.Contains(out, testfile.MP4KeyItem(2, "3/12")) {
			t.Error("an edit of the title rewrote the track entries")
		}
		plan, err = doc.Edit().Set(tag.TrackNumber, "5").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if !bytes.Contains(out, testfile.MP4KeyItem(1, "15")) || !bytes.Contains(out, testfile.MP4KeyItem(2, "5")) {
			t.Error("the total did not stay in its own entry, or the number did not land in the track entry")
		}
		if ops := strings.Join(plan.Report().Operations, "; "); strings.Contains(ops, "removed") {
			t.Errorf("operations %q name a removal; both entries stay", ops)
		}
		if re := mustParseBytes(t, out); len(re.Lint()) != 0 {
			t.Errorf("lint after the edit: %v", re.Lint())
		}
		// Naming the total with the value it has settles it: the other total goes.
		plan, err = doc.Edit().Set(tag.TrackTotal, "15").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if plan.IsNoOp() {
			t.Fatal("naming the total planned nothing")
		}
		if out := applyToBytes(t, data, plan); !bytes.Contains(out, testfile.MP4KeyItem(2, "3")) || len(mustParseBytes(t, out).Lint()) != 0 {
			t.Error("the track entry still holds its own total")
		}
	})
	t.Run("track entry first", func(t *testing.T) {
		data := testfile.MP4MdtaFile([]string{"track", "TOTALTRACKS", "title"}, []string{"3/12", "12", "T"})
		doc := mustParseBytes(t, data)
		if len(doc.Lint()) != 0 {
			t.Errorf("lint of two agreeing totals: %v", doc.Lint())
		}
		plan, err := doc.Edit().Set(tag.TrackNumber, "5").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if !bytes.Contains(out, testfile.MP4KeyItem(1, "5/12")) || bytes.Contains(out, testfile.MP4KeyItem(2, "12")) {
			t.Error("the track entry does not hold 5/12 alone")
		}
		if ops := plan.Report().Operations; !slices.Contains(ops, "1 conflicting ilst item removed") {
			t.Errorf("operations = %q, want the removal named", ops)
		}
		re := mustParseBytes(t, out)
		if total, _ := re.Get(tag.TrackTotal); !slices.Equal(total, []string{"12"}) {
			t.Errorf("TRACKTOTAL = %v", total)
		}
	})
}

// An item whose index lies past the keys table names no key. It must not come to name one a write
// appends.
func TestMP4MdtaItemPastTheKeysTableStaysUnnamed(t *testing.T) {
	// Items at 3 and 5 name nothing; so does one at 0, which no table can name.
	data := testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "artist"),
		testfile.MP4Ilst(testfile.MP4KeyItem(1, "T"), testfile.MP4KeyItem(3, "Orphan"), testfile.MP4KeyItem(5, "Another"), testfile.MP4KeyItem(0, "Zero")))
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.Genre, "G").Set(tag.Album, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	re := mustParseBytes(t, out)
	genre, _ := re.Get(tag.Genre)
	album, _ := re.Get(tag.Album)
	if !slices.Equal(genre, []string{"G"}) || !slices.Equal(album, []string{"A"}) || re.Tags().Len() != 3 {
		t.Errorf("GENRE %v, ALBUM %v, keys %v; want the two new values alone beside the title", genre, album, re.Tags().Keys())
	}
	// The one the new keys reach moves past them and past the one already there; the others stay.
	for idx, value := range map[int]string{6: "Orphan", 5: "Another", 0: "Zero"} {
		if !bytes.Contains(out, testfile.MP4KeyItem(idx, value)) {
			t.Errorf("no item %q under index %d", value, idx)
		}
	}
	if names := mp4KeysNames(t, out); !slices.Equal(names, []string{"title", "artist", "genre", "album"}) {
		t.Errorf("keys = %v", names)
	}
}

// A total the file keeps in an entry of its own stays there.
func TestMP4MdtaSeparateTotalEntryKept(t *testing.T) {
	data := testfile.MP4MdtaFile([]string{"track", "TRACKTOTAL"}, []string{"3", "12"})
	plan, err := mustParseBytes(t, data).Edit().Set(tag.TrackNumber, "5").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, testfile.MP4KeyItem(1, "5")) || !bytes.Contains(out, testfile.MP4KeyItem(2, "12")) {
		t.Error("the number and total did not stay in their own entries")
	}
}

// A key new to an mdta store takes ffmpeg's name for it.
func TestMP4MdtaNewKeysTakeFFmpegNames(t *testing.T) {
	data := testfile.MP4MdtaFile([]string{"title"}, []string{"T"})
	plan, err := mustParseBytes(t, data).Edit().
		Set(tag.AlbumArtist, "AA").Set(tag.TrackNumber, "5").Set(tag.TrackTotal, "12").
		Set(tag.DiscTotal, "2").Set(tag.TitleSort, "SN").Set(tag.Compilation, "1").
		Set(tag.Show, "SH").Set(tag.SeasonNumber, "2").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	names := mp4KeysNames(t, out)
	slices.Sort(names)
	want := []string{"album_artist", "compilation", "disc", "season_number", "show", "sort_name", "title", "track"}
	if !slices.Equal(names, want) {
		t.Errorf("keys = %v, want %v", names, want)
	}
	for _, v := range []string{"5/12", "/2"} {
		if !bytes.Contains(out, testfile.MP4Data(1, []byte(v))) {
			t.Errorf("no entry holds %q", v)
		}
	}
	re := mustParseBytes(t, out)
	for k, v := range map[tag.Key]string{
		tag.AlbumArtist: "AA", tag.TrackNumber: "5", tag.TrackTotal: "12", tag.DiscTotal: "2",
		tag.TitleSort: "SN", tag.Compilation: "1", tag.Show: "SH", tag.SeasonNumber: "2",
	} {
		if got, _ := re.Get(k); !slices.Equal(got, []string{v}) {
			t.Errorf("%s = %v, want [%s]", k, got, v)
		}
	}
	if re.Tags().Has(tag.DiscNumber) {
		t.Error("a lone disc total read back with a disc number")
	}
}

// Two mdta entries naming one key: the first is the value read and the other shows in the families.
// An edit elsewhere keeps both; an edit of the key leaves one.
func TestMP4MdtaKeyHeldTwice(t *testing.T) {
	names := []string{"software", "creationdate", "date", "encoder", "title"}
	values := []string{"QuickTime 10", "2021-05-04", "2020", "Lavf60", "T"}
	data := testfile.MP4MdtaFile(names, values)
	doc := mustParseBytes(t, data)
	for k, want := range map[tag.Key]string{tag.Encoder: "QuickTime 10", tag.RecordingDate: "2021-05-04"} {
		if v, _ := doc.Get(k); !slices.Equal(v, []string{want}) {
			t.Errorf("%s = %v, want [%s], the first entry's", k, v, want)
		}
		if selected, ok := familySelected(doc, k); !ok || selected {
			t.Errorf("%s family selected = %v (found %v), want an unselected entry", k, selected, ok)
		}
	}
	if !hasLintCode(doc, "conflicting-families") || hasLintCode(doc, "single-valued-multi") {
		t.Errorf("lint = %v, want conflicting-families and no single-valued-multi", doc.Lint())
	}

	t.Run("unrelated edit", func(t *testing.T) {
		plan, err := doc.Edit().Set(tag.Title, "Other").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		for i, v := range values[:4] {
			if !bytes.Contains(out, testfile.MP4KeyItem(i+1, v)) {
				t.Errorf("the %q item did not survive an edit of the title", names[i])
			}
		}
		re := mustParseBytes(t, out)
		if v, _ := re.Get(tag.Encoder); !slices.Equal(v, []string{"QuickTime 10"}) {
			t.Errorf("ENCODER = %v after the edit", v)
		}
		if got, want := ffprobeTagsIfPresent(t, out), ffprobeTagsIfPresent(t, data); got != nil {
			for _, n := range names[:4] {
				if got[n] != want[n] {
					t.Errorf("ffprobe %s = %q, was %q", n, got[n], want[n])
				}
			}
		}
	})
	for name, value := range map[string]string{"edit of the key": "New", "the same value set again": "QuickTime 10"} {
		t.Run(name, func(t *testing.T) {
			plan, err := doc.Edit().Set(tag.Encoder, value).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if plan.IsNoOp() {
				t.Fatal("the plan is a no-op; the second entry would stay")
			}
			if ops := plan.Report().Operations; !slices.Contains(ops, "1 conflicting ilst item removed") {
				t.Errorf("operations = %v, want the removal named", ops)
			}
			out := applyToBytes(t, data, plan)
			if bytes.Contains(out, []byte("Lavf60")) {
				t.Error("the second ENCODER entry survived an edit of the key")
			}
			if !bytes.Contains(out, testfile.MP4KeyItem(1, value)) || !bytes.Contains(out, testfile.MP4KeyItem(2, "2021-05-04")) || !bytes.Contains(out, testfile.MP4KeyItem(3, "2020")) {
				t.Error("the edit did not land in the first entry, or it touched the date entries")
			}
			re := mustParseBytes(t, out)
			if selected, ok := familySelected(re, tag.Encoder); !ok || !selected {
				t.Errorf("ENCODER family selected = %v (found %v) after the edit", selected, ok)
			}
			if hasLintCode(re, "conflicting-families") != true {
				t.Error("the date entries no longer conflict")
			}
		})
	}
}

// ffprobeTagsIfPresent returns ffprobe's container tags for data, nil when ffprobe is not there.
func ffprobeTagsIfPresent(t *testing.T, data []byte) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		if os.Getenv("WAXLABEL_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffprobe is required and missing")
		}
		return nil
	}
	path := filepath.Join(t.TempDir(), "probe.m4a")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return ffprobeFormatTags(t, path)
}
