package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// The mixed-case names Picard writes as an ID3 TXXX description and an MP4 freeform name.
const (
	picardCountry = "MusicBrainz Album Release Country"
	picardStatus  = "MusicBrainz Album Status"
	picardType    = "MusicBrainz Album Type"
)

// TestReleaseDetailRoundTrip proves the three release-detail keys survive a real
// set -> write -> reparse across the main storage mechanisms: a Vorbis comment (FLAC), ID3
// TXXX user frames (MP3), MP4 com.apple.iTunes freeforms (M4A), the embedded-ID3 chunk WAV
// and AIFF carry, and Matroska SimpleTags (MKA, identity names). RELEASETYPE holds two
// values to exercise the multivalued projection.
func TestReleaseDetailRoundTrip(t *testing.T) {
	details := []struct {
		key  tag.Key
		vals []string
		get  func(*wl.Document) []string
	}{
		{tag.ReleaseCountry, []string{"GB"}, func(d *wl.Document) []string {
			return []string{d.Fields().ReleaseCountry}
		}},
		{tag.ReleaseStatus, []string{"official"}, func(d *wl.Document) []string {
			return []string{d.Fields().ReleaseStatus}
		}},
		{tag.ReleaseType, []string{"album", "compilation"}, func(d *wl.Document) []string {
			return d.Fields().ReleaseTypes
		}},
	}

	for _, f := range []string{sampleFLAC, sampleMP3, sampleMP4, sampleWAV, sampleAIFF, sampleMKA} {
		src := readFixture(t, f)
		ed := mustParseBytes(t, src).Edit()
		for _, d := range details {
			ed = ed.Set(d.key, d.vals...)
		}
		plan, err := ed.Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		out := applyToBytes(t, src, plan)

		re := mustParseBytes(t, out)
		for _, d := range details {
			if got, _ := re.Tags().Get(d.key); !slices.Equal(got, d.vals) {
				t.Errorf("%s: %s = %v, want %v", f, d.key, got, d.vals)
			}
			if got := d.get(re); !slices.Equal(got, d.vals) {
				t.Errorf("%s: Fields() for %s = %v, want %v", f, d.key, got, d.vals)
			}
		}
	}
}

// TestReleaseDetailMP4PicardAtoms reads the atoms Picard actually writes. Before the mapping
// entries existed decodeFreeform missed the mixed-case names and left the items unowned:
// preserved on write, but invisible to Get, Fields, dump, diff, and copy.
func TestReleaseDetailMP4PicardAtoms(t *testing.T) {
	data := mp4Tagged(
		mp4Freeform("com.apple.iTunes", picardCountry, "GB"),
		mp4Freeform("com.apple.iTunes", picardStatus, "official"),
		mp4Freeform("com.apple.iTunes", picardType, "album"),
	)
	f := mustParseBytes(t, data).Fields()
	if f.ReleaseCountry != "GB" {
		t.Errorf("ReleaseCountry = %q, want GB", f.ReleaseCountry)
	}
	if f.ReleaseStatus != "official" {
		t.Errorf("ReleaseStatus = %q, want official", f.ReleaseStatus)
	}
	if want := []string{"album"}; !slices.Equal(f.ReleaseTypes, want) {
		t.Errorf("ReleaseTypes = %v, want %v", f.ReleaseTypes, want)
	}
}

// TestReleaseDetailPicardWriteSpelling checks the write spelling at the byte level on the two
// formats that rename these keys. A canonical name in the output would not read back in Picard.
func TestReleaseDetailPicardWriteSpelling(t *testing.T) {
	for _, f := range []string{sampleMP3, sampleMP4} {
		src := readFixture(t, f)
		plan, err := mustParseBytes(t, src).Edit().
			Set(tag.ReleaseCountry, "GB").
			Set(tag.ReleaseStatus, "official").
			Set(tag.ReleaseType, "album").
			Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		out := applyToBytes(t, src, plan)

		for _, name := range []string{picardCountry, picardStatus, picardType} {
			if !bytes.Contains(out, []byte(name)) {
				t.Errorf("%s: expected the Picard name %q in the written output", f, name)
			}
		}
		for _, canon := range []string{"RELEASECOUNTRY", "RELEASESTATUS", "RELEASETYPE"} {
			if bytes.Contains(out, []byte(canon)) {
				t.Errorf("%s: the canonical name %q leaked into the output", f, canon)
			}
		}
	}
}

// TestReleaseDetailID3LegacyMigration covers a pre-existing TXXX:RELEASECOUNTRY frame. It
// reads onto the canonical key, and an edit that touches the key drops it as a stale
// representation while writing the Picard-spelled frame. Same mechanism as TXXX:LYRICIST.
func TestReleaseDetailID3LegacyMigration(t *testing.T) {
	data := append(id3v2(3, txxxFrame(3, "RELEASECOUNTRY", "US")), mp3Audio(t)...)

	doc := mustParseBytes(t, data)
	if got := doc.Fields().ReleaseCountry; got != "US" {
		t.Fatalf("legacy TXXX:RELEASECOUNTRY read as %q, want US", got)
	}

	plan, err := doc.Edit().Set(tag.ReleaseCountry, "GB").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if n := bytes.Count(out, []byte("TXXX")); n != 1 {
		t.Errorf("output carries %d TXXX frames, want 1 (stale representation not dropped)", n)
	}
	if !bytes.Contains(out, []byte(picardCountry)) {
		t.Errorf("expected the Picard description %q in the output", picardCountry)
	}
	if bytes.Contains(out, []byte("RELEASECOUNTRY")) {
		t.Error("the legacy TXXX:RELEASECOUNTRY description survived the edit")
	}
	if got, _ := mustParseBytes(t, out).Tags().Get(tag.ReleaseCountry); !slices.Equal(got, []string{"GB"}) {
		t.Errorf("RELEASECOUNTRY after migration = %v, want [GB]", got)
	}
}

// TestReleaseDetailMP4LegacyMigration covers an uppercase RELEASECOUNTRY atom, owned through
// decodeFreeform's valid-key fallback. It must keep reading and rebuild under the Picard name.
func TestReleaseDetailMP4LegacyMigration(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "T"), mp4Freeform("com.apple.iTunes", "RELEASECOUNTRY", "US"))

	doc := mustParseBytes(t, data)
	if got := doc.Fields().ReleaseCountry; got != "US" {
		t.Fatalf("uppercase RELEASECOUNTRY atom read as %q, want US", got)
	}

	plan, err := doc.Edit().Set(tag.ReleaseCountry, "GB").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if !bytes.Contains(out, []byte(picardCountry)) {
		t.Errorf("expected the Picard freeform name %q in the output", picardCountry)
	}
	if bytes.Contains(out, []byte("RELEASECOUNTRY")) {
		t.Error("the legacy uppercase atom name survived the edit")
	}
	if got, _ := mustParseBytes(t, out).Tags().Get(tag.ReleaseCountry); !slices.Equal(got, []string{"GB"}) {
		t.Errorf("RELEASECOUNTRY after migration = %v, want [GB]", got)
	}
}

// TestReleaseDetailDualRepresentationID3 covers a file holding both TXXX:RELEASECOUNTRY and
// the Picard spelling. They were two keys before this change and are one now, so both values
// project, lint reports the redundancy, and repair is scoped to an edit that touches the key.
func TestReleaseDetailDualRepresentationID3(t *testing.T) {
	build := func(canonVal, picardVal string) []byte {
		return append(id3v2(3,
			txxxFrame(3, "RELEASECOUNTRY", canonVal),
			txxxFrame(3, picardCountry, picardVal),
		), mp3Audio(t)...)
	}

	// The description folds into the source token, so the two frames are distinct
	// contributions rather than a dedupe.
	data := build("US", "GB")
	doc := mustParseBytes(t, data)
	if got, _ := doc.Tags().Get(tag.ReleaseCountry); len(got) != 2 {
		t.Fatalf("RELEASECOUNTRY = %v, want both frames to project", got)
	}
	// The key is Known() now, so the redundancy is lint's business. A custom key was exempt.
	if !hasLintCode(doc, "single-valued-multi") {
		t.Errorf("expected a single-valued-multi finding; got %v", doc.Lint())
	}

	// Distinct sources with distinct values is a conflict (unselected). Two frames carrying
	// the same value stay selected, and dump marks them "(duplicate)" instead.
	if selected, ok := familySelected(doc, tag.ReleaseCountry); !ok || selected {
		t.Errorf("distinct values: family Selected = %v (found=%v), want false", selected, ok)
	}
	same := mustParseBytes(t, build("GB", "GB"))
	if selected, ok := familySelected(same, tag.ReleaseCountry); !ok || !selected {
		t.Errorf("identical values: family Selected = %v (found=%v), want true", selected, ok)
	}

	plan, err := doc.Edit().Set(tag.ReleaseCountry, "FR").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	touched := applyToBytes(t, data, plan)
	if n := bytes.Count(touched, []byte("TXXX")); n != 1 {
		t.Errorf("after an edit that sets the key: %d TXXX frames, want 1", n)
	}
	if !bytes.Contains(touched, []byte(picardCountry)) || bytes.Contains(touched, []byte("RELEASECOUNTRY")) {
		t.Error("the surviving frame should be the Picard-spelled one")
	}

	// An unrelated edit copies both frames verbatim, so the duplication persists. This
	// asymmetry with MP4 (which always coalesces) is easy to get wrong later, so pin it.
	unrelatedPlan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Untouched").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	unrelated := applyToBytes(t, data, unrelatedPlan)
	if !bytes.Contains(unrelated, []byte("RELEASECOUNTRY")) || !bytes.Contains(unrelated, []byte(picardCountry)) {
		t.Error("an unrelated edit must copy both TXXX frames verbatim")
	}
}

// TestReleaseDetailDualRepresentationMP4 is the MP4 arm: buildItems rebuilds every owned item
// from the tag set, so any write coalesces the two atoms into one that carries both values.
func TestReleaseDetailDualRepresentationMP4(t *testing.T) {
	data := mp4Tagged(
		mp4Text("\xa9nam", "T"),
		mp4Freeform("com.apple.iTunes", "RELEASECOUNTRY", "US"),
		mp4Freeform("com.apple.iTunes", picardCountry, "GB"),
	)

	doc := mustParseBytes(t, data)
	if got, _ := doc.Tags().Get(tag.ReleaseCountry); len(got) != 2 {
		t.Fatalf("RELEASECOUNTRY = %v, want both atoms to project", got)
	}
	if !hasLintCode(doc, "single-valued-multi") {
		t.Errorf("expected a single-valued-multi finding; got %v", doc.Lint())
	}

	// An edit to an unrelated key still coalesces, unlike ID3.
	plan, err := doc.Edit().Set(tag.Title, "Changed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if bytes.Contains(out, []byte("RELEASECOUNTRY")) {
		t.Error("the uppercase atom name should be gone after a rebuild")
	}
	if n := bytes.Count(out, []byte(picardCountry)); n != 1 {
		t.Errorf("output carries %d %q atoms, want 1", n, picardCountry)
	}
	if got, _ := mustParseBytes(t, out).Tags().Get(tag.ReleaseCountry); !slices.Equal(got, []string{"US", "GB"}) {
		t.Errorf("coalesced values = %v, want [US GB] (MP4 merges, never drops)", got)
	}
}

// TestReleaseDetailMatroskaCountryUntouched pins that a bare COUNTRY SimpleTag stays a
// custom key. The Matroska spec defines COUNTRY as a nesting qualifier scoping sibling tags
// to a country, not as this release's country, so folding it would misread the file and
// subject its free-text value to the two-letter malformed-country lint. A file carrying one
// must keep lint-clean and keep its own key.
func TestReleaseDetailMatroskaCountryUntouched(t *testing.T) {
	src := readFixture(t, notagsMKA)
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Key("COUNTRY"), "United States").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, src, plan))

	if got, ok := re.Tags().Get(tag.Key("COUNTRY")); !ok || !slices.Equal(got, []string{"United States"}) {
		t.Errorf("COUNTRY = %v (ok=%v), want it kept as its own custom key", got, ok)
	}
	if _, ok := re.Tags().Get(tag.ReleaseCountry); ok {
		t.Error("a bare COUNTRY SimpleTag must not project onto RELEASECOUNTRY")
	}
	if hasLintCode(re, "malformed-country") {
		t.Errorf("a free-text COUNTRY value must not draw the country validator; lint = %v", re.Lint())
	}
}

// TestReleaseDetailMatroskaRoundTrip writes the three keys under their identity names and
// reads them back, including the underscored spellings folding on read.
func TestReleaseDetailMatroskaRoundTrip(t *testing.T) {
	src := readFixture(t, sampleMKA)
	plan, err := mustParseBytes(t, src).Edit().Set(tag.ReleaseCountry, "GB").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, []byte("RELEASECOUNTRY")) {
		t.Error("the write must emit the identity RELEASECOUNTRY name")
	}
	if got, _ := mustParseBytes(t, out).Tags().Get(tag.ReleaseCountry); !slices.Equal(got, []string{"GB"}) {
		t.Errorf("RELEASECOUNTRY = %v, want [GB]", got)
	}

	// The underscored APE spelling folds on read, so the same string means the same key on
	// Matroska as it does on Vorbis.
	aliased, err := mustParseBytes(t, src).Edit().Set(tag.Key("MUSICBRAINZ_ALBUMSTATUS"), "official").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := mustParseBytes(t, applyToBytes(t, src, aliased)).Tags().Get(tag.ReleaseStatus); !slices.Equal(got, []string{"official"}) {
		t.Errorf("RELEASESTATUS = %v, want [official]", got)
	}
}

// TestReleaseDetailTypeNeverSlashSplit pins that a slash-joined RELEASETYPE stays one value.
// Picard's v2.3 writer joins values with a separator, so a v2.3 file presents one value where
// v2.4 or FLAC presents two. Splitting here would invent values the file does not carry.
func TestReleaseDetailTypeNeverSlashSplit(t *testing.T) {
	data := append(id3v2(3, txxxFrame(3, picardType, "album/compilation")), mp3Audio(t)...)
	got := mustParseBytes(t, data).Fields().ReleaseTypes
	if want := []string{"album/compilation"}; !slices.Equal(got, want) {
		t.Errorf("ReleaseTypes = %v, want %v (the slash must not be split)", got, want)
	}
}

// TestReleaseDetailDualTaggedMP3Families covers an MP3 carrying an ID3v2 tag and a trailing
// APEv2 one. The APE item used to resolve to a key the ID3 side never held, so the row was
// always selected; now both resolve to RELEASESTATUS and a disagreement surfaces in dump.
func TestReleaseDetailDualTaggedMP3Families(t *testing.T) {
	build := func(apeVal string) []byte {
		data := append(id3v2(3, txxxFrame(3, picardStatus, "official")), mp3Audio(t)...)
		return append(data, apeTag(map[string]string{"MUSICBRAINZ_ALBUMSTATUS": apeVal})...)
	}

	for _, c := range []struct {
		apeVal   string
		selected bool
	}{
		{"bootleg", false}, // a genuine disagreement, previously invisible
		{"official", true},
	} {
		doc := mustParseBytes(t, build(c.apeVal))
		found := false
		for _, f := range doc.Families() {
			if f.Family != wl.FamilyAPEv2 || f.Key != tag.ReleaseStatus {
				continue
			}
			found = true
			if f.Selected != c.selected {
				t.Errorf("APE RELEASESTATUS=%q: Selected = %v, want %v", c.apeVal, f.Selected, c.selected)
			}
		}
		if !found {
			t.Errorf("APE RELEASESTATUS=%q: no APEv2 family entry; families = %+v", c.apeVal, doc.Families())
		}
	}
}

// hasLintCode reports whether the document's lint findings include the given code.
func hasLintCode(d *wl.Document, code string) bool {
	for _, f := range d.Lint() {
		if f.Code == code {
			return true
		}
	}
	return false
}

// familySelected returns the Selected flag of the first family entry for key, and whether
// such an entry exists at all.
func familySelected(d *wl.Document, key tag.Key) (selected, found bool) {
	for _, f := range d.Families() {
		if f.Key == key {
			return f.Selected, true
		}
	}
	return false, false
}
