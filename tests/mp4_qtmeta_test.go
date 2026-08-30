package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// QuickTime language codes used by the udta text fixtures below: langUnd is the packed
// ISO-639-2 "und" ffmpeg writes, langEng is packed "eng", and langDeu is packed "deu".
const (
	langUnd uint16 = 0x55C4
	langEng uint16 = 0x15C7
	langDeu uint16 = 0x0EB5
)

// mp4MdtaFile builds a file whose moov.udta.meta carries an mdta handler, a keys index over
// names, and an ilst whose items are keyed by index into it - the shape ffmpeg's
// "-movflags +use_metadata_tags" produces.
func mp4MdtaFile(names []string, values []string) []byte {
	items := make([][]byte, 0, len(values))
	for i, v := range values {
		items = append(items, mp4KeyItem(i+1, v))
	}
	return mp4Assemble(mp4HdlrMdta(), mp4Keys(names...), mp4Ilst(items...))
}

// TestMP4MdtaBareKeysRead is the report's repro: an ffmpeg "+use_metadata_tags" file keys
// its ilst items by index into a keys box holding bare names. Without the keys index every
// item falls to the unknown-atom branch and the file reports no tags at all.
func TestMP4MdtaBareKeysRead(t *testing.T) {
	data := mp4MdtaFile(
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

// TestMP4MdtaApplePrefixedKeysRead: Apple's own recorders write the reverse-DNS key form.
// Stripping the prefix lands both producers on the same vocabulary.
func TestMP4MdtaApplePrefixedKeysRead(t *testing.T) {
	data := mp4MdtaFile(
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

// TestMP4MdtaUnknownKeyPreserved: a key outside the vocabulary contributes nothing but must
// survive a rewrite verbatim, the same treatment an unrecognized four-cc atom gets.
func TestMP4MdtaUnknownKeyPreserved(t *testing.T) {
	data := mp4MdtaFile(
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

// TestMP4UdtaTextRead: a plain .mov keeps its tags as direct udta children with no meta box
// at all. \xa9swr is the QuickTime software atom, which is where a Lavf stamp lands, so the
// read is what lets lint report the inherited encoder.
func TestMP4UdtaTextRead(t *testing.T) {
	data := mp4AssembleUdta(
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

// TestMP4UdtaMultiLanguageCanonicalValue: several [size][language]<text> entries can sit
// back to back in one atom (this is where ffprobe's "title-eng" comes from). The first
// undefined/English entry supplies the canonical value, and every other entry survives a
// rewrite verbatim rather than being flattened away.
func TestMP4UdtaMultiLanguageCanonicalValue(t *testing.T) {
	data := mp4AssembleUdta(mp4UdtaText("\xa9nam",
		mp4QTTextEntry(langDeu, "Deutscher Titel"),
		mp4QTTextEntry(langEng, "English Title"),
	))
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "English Title" {
		t.Errorf("TITLE = %q, want the English entry, not the leading German one", got)
	}
}

// TestMP4UdtaIlstDisagreementConflicts: when both stores hold a key, the ilst is canonical
// and the udta value is a family entry, so the disagreement reaches conflicting-families
// without the two values merging into a multi-value the next write would store as one.
func TestMP4UdtaIlstDisagreementConflicts(t *testing.T) {
	data := mp4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Udta Title")),
		mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "Ilst Title"))),
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

// TestMP4UdtaIlstDisagreementSurvivesUnrelatedEdit: an edit touching neither store must not
// launder a disagreement away. Merging both stores into one canonical multi-value made the
// write store both in the ilst, after which the two agreed and the conflict vanished.
func TestMP4UdtaIlstDisagreementSurvivesUnrelatedEdit(t *testing.T) {
	data := mp4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Udta Title")),
		mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "Ilst Title"))),
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
	// The write syncs udta to the ilst, so the two now agree and no conflict remains - but
	// they must agree on the ilst's value, not by having absorbed both.
	if v, _ := re.Tags().Get(tag.Title); len(v) != 1 || v[0] != "Ilst Title" {
		t.Errorf("TITLE = %v, want [Ilst Title]", v)
	}
	for _, fi := range re.Lint() {
		if fi.Code == "single-valued-multi" {
			t.Errorf("the disagreement was laundered into a multi-value:\n%+v", re.Families())
		}
	}
}

// TestMP4UdtaUnmappedAtomPreserved: a udta child outside the text vocabulary is not decoded
// and must survive a rewrite through the verbatim udta splice.
func TestMP4UdtaUnmappedAtomPreserved(t *testing.T) {
	data := mp4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Before")),
		mp4Atom("XTRA", []byte("opaque-user-data")),
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

// TestMP4MdtaWriteStaysKeyed is the write half of the report's repro: a set on a keys-indexed
// file must land as a keys entry plus an index-keyed item, not as a four-character
// "\xa9nam" atom sitting inside an mdta box where nothing will read it.
func TestMP4MdtaWriteStaysKeyed(t *testing.T) {
	data := mp4MdtaFile([]string{"title"}, []string{"Before"})
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
	// Re-applying the same edit must be a true no-op: the keys index is carried forward by
	// position, so a second write reuses every entry and produces identical bytes.
	plan2, err := re.Edit().Set(tag.Title, "After").Set(tag.Album, "Fresh").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.IsNoOp() {
		t.Errorf("re-applying the same tags should be a no-op; operations: %v", plan2.Report().Operations)
	}
}

// TestMP4UdtaOnlyWritesInPlace: a file whose only tag store is udta-level text atoms is
// edited there, with no meta/ilst created beside it - so ffprobe reports one title, not the
// two a second store would produce.
func TestMP4UdtaOnlyWritesInPlace(t *testing.T) {
	data := mp4AssembleUdta(
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

// TestMP4UdtaKeepsOtherLanguagesOnWrite: rewriting a multi-language atom replaces only the
// canonical entry; the other translations survive verbatim.
func TestMP4UdtaKeepsOtherLanguagesOnWrite(t *testing.T) {
	data := mp4AssembleUdta(mp4UdtaText("\xa9nam",
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

// TestMP4UdtaSyncedWithIlst: when both stores exist the ilst is the write target, and the
// udta-level atom for the same canonical key is rewritten to match, so the two cannot
// disagree after a write.
func TestMP4UdtaSyncedWithIlst(t *testing.T) {
	data := mp4AssembleUdta(
		mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "Stale Title")),
		mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "Stale Title"))),
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

// TestMP4UdtaClearedRemovesAtom: clearing a key removes the udta atom that held it rather
// than leaving a value the canonical view no longer reports.
func TestMP4UdtaClearedRemovesAtom(t *testing.T) {
	data := mp4AssembleUdta(
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

// TestDifferentialFFprobeReadsQuickTimeStores is the interoperability proof for both
// QuickTime stores: after WaxLabel edits an ffmpeg-authored "+use_metadata_tags" M4A and a
// plain .mov, ffprobe must read back exactly the values written, once each.
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
			args = append(args, "-metadata", "title=Original", path)
			if err := exec.Command("ffmpeg", args...).Run(); err != nil {
				t.Fatalf("ffmpeg author %s: %v", c.file, err)
			}
			doc := mustParseFile(t, path)
			if got := doc.Fields().Title; got != "Original" {
				t.Fatalf("read back TITLE = %q, want Original (the store was not decoded)", got)
			}
			plan, err := doc.Edit().
				Set(tag.Title, "Written By WaxLabel").
				Set(tag.Artist, "Written Artist").
				Set(tag.Album, "Written Album").
				Set(tag.Comment, "Written Comment").
				Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
				t.Fatal(err)
			}
			tags := ffprobeFormatTags(t, path)
			for probeKey, want := range map[string]string{
				"title":   "Written By WaxLabel",
				"artist":  "Written Artist",
				"album":   "Written Album",
				"comment": "Written Comment",
			} {
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

// TestMP4MdtaWithoutKeysBoxFallsBack: a meta declaring the mdta handler but carrying no keys
// box is a broken file whose items resolve to nothing. Encoding index-keyed items into it
// would name entries in a table that does not exist, so the write falls back to the
// four-character encoder and the values still read back.
func TestMP4MdtaWithoutKeysBoxFallsBack(t *testing.T) {
	data := mp4Assemble(mp4HdlrMdta(), mp4Ilst(mp4Text("\xa9nam", "Before")))
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

// TestMP4MdtaOutOfRangeIndexPreserved: an ilst item whose four-cc reads as a keys index the
// table does not cover resolves to nothing and is preserved verbatim. The index is compared
// as an unsigned value: a name above 2^31 turns negative under a 32-bit int, which made the
// bounds check pass and the lookup panic.
func TestMP4MdtaOutOfRangeIndexPreserved(t *testing.T) {
	// "\xa9nam" reads as the index 0xA96E616D (2842583405), far past a one-entry table.
	data := mp4Assemble(mp4HdlrMdta(), mp4Keys("title"),
		mp4Ilst(mp4KeyItem(1, "Kept"), mp4Text("\xa9nam", "Out Of Range")))
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

// TestMP4MdtaCoverSurvivesTagEdit: an mdta cover item is named by keys index, so matching
// cover art by the "covr" four-cc alone found none and a tag-only edit deleted it.
func TestMP4MdtaCoverSurvivesTagEdit(t *testing.T) {
	data := mp4Assemble(mp4HdlrMdta(), mp4Keys("title", "covr"),
		mp4Ilst(mp4KeyItem(1, "Before"), mp4KeyItemData(2, mp4Data(13, tinyJPEG()))))
	doc := mustParseBytes(t, data)
	if len(doc.Pictures()) != 1 {
		t.Fatalf("setup: pictures = %d, want 1", len(doc.Pictures()))
	}
	plan, err := doc.Edit().Set(tag.Title, "After").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if len(re.Pictures()) != 1 {
		t.Errorf("pictures after a tag-only edit = %d, want 1 (the cover must carry)", len(re.Pictures()))
	}
	if got := re.Fields().Title; got != "After" {
		t.Errorf("TITLE = %q, want After", got)
	}
}

// TestMP4UdtaClearKeepsOtherLanguages: clearing a key must not take the atom's other-language
// entries with it, which qtmeta.go promises to preserve verbatim.
func TestMP4UdtaClearKeepsOtherLanguages(t *testing.T) {
	data := mp4AssembleUdta(mp4UdtaText("\xa9nam",
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

// TestMP4UdtaEmptyCanonicalEntryKeepsSiblings: an atom whose canonical entry is empty
// contributes no tag, so the key is absent from the edit - the delete path must still not
// take the other-language entries with it.
func TestMP4UdtaEmptyCanonicalEntryKeepsSiblings(t *testing.T) {
	data := mp4AssembleUdta(
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
