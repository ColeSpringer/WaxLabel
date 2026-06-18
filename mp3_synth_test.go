package waxlabel_test

import (
	"bytes"
	"context"
	"os"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// Synthetic ID3 builders. The audio body is borrowed from the tagless
// fixture so the synthesized files are real, decodable MP3s.

func mp3Audio(t *testing.T) []byte {
	t.Helper()
	return readFixture(t, notagsMP3)
}

func syncsafe(n int) []byte {
	return []byte{byte(n>>21) & 0x7f, byte(n>>14) & 0x7f, byte(n>>7) & 0x7f, byte(n) & 0x7f}
}

// id3v2 wraps frame bytes in an ID3v2 header of the given minor version.
func id3v2(version byte, frames ...[]byte) []byte {
	body := slices.Concat(frames...)
	out := append([]byte{'I', 'D', '3', version, 0, 0}, syncsafe(len(body))...)
	return append(out, body...)
}

// textFrame23 builds a Latin-1 text frame for v2.3/v2.4 (plain 4-byte size is
// valid for both small frames since the value is well under 128 per byte here we
// keep it tiny, but use sync-safe for v2.4 correctness).
func textFrame(version byte, id, text string) []byte {
	body := append([]byte{0}, text...) // encoding 0 = Latin-1
	var sz []byte
	if version >= 4 {
		sz = syncsafe(len(body))
	} else {
		sz = []byte{byte(len(body) >> 24), byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	}
	out := append([]byte(id), sz...)
	out = append(out, 0, 0) // flags
	return append(out, body...)
}

// textFrame22 builds a v2.2 (3-char ID, 3-byte size, no flags) text frame.
func textFrame22(id, text string) []byte {
	body := append([]byte{0}, text...)
	out := append([]byte(id), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	return append(out, body...)
}

func id3v1(title, artist string, genre byte) []byte {
	b := make([]byte, 128)
	copy(b[0:3], "TAG")
	copy(b[3:33], title)
	copy(b[33:63], artist)
	b[127] = genre
	return b
}

func TestMP3NumericGenreRead(t *testing.T) {
	data := append(id3v2(3, textFrame(3, "TCON", "(17)"), textFrame(3, "TIT2", "T")), mp3Audio(t)...)
	doc := mustParseBytes(t, data)
	if g := doc.Fields().Genres; len(g) != 1 || g[0] != "Rock" {
		t.Errorf("numeric genre (17) -> %v, want [Rock]", g)
	}
	if !hasWarning(doc, wl.WarnNumericGenre) {
		t.Error("expected a numeric-genre warning")
	}
}

func TestMP3MultipleNumericGenres(t *testing.T) {
	// An ID3v2.3 two-genre TCON "(51)(39)" must resolve to both names.
	data := append(id3v2(3, textFrame(3, "TCON", "(51)(39)"), textFrame(3, "TIT2", "T")), mp3Audio(t)...)
	g := mustParseBytes(t, data).Fields().Genres
	if len(g) != 2 || g[0] != "Techno-Industrial" || g[1] != "Noise" {
		t.Errorf("(51)(39) -> %v, want [Techno-Industrial Noise]", g)
	}
}

func TestMP3NumericGenreWrite(t *testing.T) {
	data := append(id3v2(4, textFrame(4, "TIT2", "T")), mp3Audio(t)...)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Genre, "Rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	// The written TCON is numeric, but reading resolves it back to the name.
	if g := mustParseBytes(t, out).Fields().Genres; len(g) != 1 || g[0] != "Rock" {
		t.Errorf("genre round-trip = %v", g)
	}
	// Prove the on-disk TCON is the numeric reference "17", not the name "Rock":
	// look for the exact v2.4 frame (header + sync-safe size 3 + flags + Latin-1
	// encoding byte + "17"). A substring match on "17" alone would pass spuriously.
	wantFrame := append([]byte("TCON"), 0, 0, 0, 3, 0, 0, 0)
	wantFrame = append(wantFrame, "17"...)
	if !bytes.Contains(out, wantFrame) {
		t.Error("expected a numeric TCON='17' frame in the output")
	}
	if bytes.Contains(out, []byte("Rock")) {
		t.Error("output should not contain the genre name when numeric is requested")
	}
}

func TestMP3V22EndToEnd(t *testing.T) {
	data := append(id3v2(2, textFrame22("TT2", "V22 Title"), textFrame22("TP1", "V22 Artist")), mp3Audio(t)...)
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "V22 Title" || len(doc.Fields().Artists) != 1 || doc.Fields().Artists[0] != "V22 Artist" {
		t.Fatalf("v2.2 read: title=%q artists=%v", doc.Fields().Title, doc.Fields().Artists)
	}
	// Editing modernises the tag to v2.3.
	plan, err := doc.Edit().Set(tag.Album, "V22 Album").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if out[3] != 3 {
		t.Errorf("v2.2 should be written back as v2.3, got version byte %d", out[3])
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "V22 Title" || re.Fields().Album != "V22 Album" {
		t.Errorf("after upgrade: title=%q album=%q", re.Fields().Title, re.Fields().Album)
	}
}

func TestMP3VersionPreserved(t *testing.T) {
	for _, c := range []struct {
		path string
		want byte
	}{{sampleMP3, 3}, {sampleMP324, 4}} {
		src := readFixture(t, c.path)
		plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "V").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, src, plan)
		if out[3] != c.want {
			t.Errorf("%s: wrote ID3v2.%d, want v2.%d", c.path, out[3], c.want)
		}
	}
}

func TestMP3LegacyConflictSurfaced(t *testing.T) {
	data := id3v2(3, textFrame(3, "TIT2", "V2 Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, id3v1("V1 Different Title", "", 255)...)

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "V2 Title" {
		t.Errorf("ID3v2 should win: title = %q", doc.Fields().Title)
	}
	// The ID3v1 disagreement is surfaced as an unselected family entry / lint.
	conflict := false
	for _, f := range doc.Families() {
		if f.Family == wl.FamilyID3v1 && f.Key == tag.Title && !f.Selected {
			conflict = true
		}
	}
	if !conflict {
		t.Errorf("expected an unselected ID3v1 Title family entry; families = %+v", doc.Families())
	}
	foundLint := false
	for _, fi := range doc.Lint() {
		if fi.Code == "conflicting-families" {
			foundLint = true
		}
	}
	if !foundLint {
		t.Error("expected a conflicting-families lint finding")
	}
}

func TestMP3APELegacyView(t *testing.T) {
	data := append(slices.Clone(mp3Audio(t)), apeTag(map[string]string{"Title": "APE Title"})...)
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnLegacyAPE) {
		t.Error("expected a legacy-APE warning")
	}
	found := false
	for _, f := range doc.Families() {
		if f.Family == wl.FamilyAPEv2 && f.Key == tag.Title {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an APEv2 Title family entry; families = %+v", doc.Families())
	}
	// A tag edit preserves the APE bytes verbatim.
	plan, _ := doc.Edit().Set(tag.Artist, "X").Prepare()
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("APE Title")) {
		t.Error("APE tag bytes were not preserved across an edit")
	}
}

// TestMP3PostWriteRetainsLegacyFamilies confirms the document returned from a
// write surfaces the preserved trailing ID3v1 in its family view, matching a
// fresh parse of the output (not just the new ID3v2).
func TestMP3PostWriteRetainsLegacyFamilies(t *testing.T) {
	data := id3v2(3, textFrame(3, "TIT2", "V2 Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, id3v1("V1 Different", "", 255)...)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	outDoc, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}

	hasV1Title := func(d *wl.Document) bool {
		for _, f := range d.Families() {
			if f.Family == wl.FamilyID3v1 && f.Key == tag.Title {
				return true
			}
		}
		return false
	}
	if !hasV1Title(outDoc) {
		t.Error("post-write document dropped the preserved ID3v1 family entry")
	}
	if !hasV1Title(mustParseBytes(t, w.b)) {
		t.Error("a fresh parse of the output should carry the ID3v1 family entry")
	}
}

// apeTag builds a minimal footer-only APEv2 tag (mirrors the ape package test).
func apeTag(items map[string]string) []byte {
	var body []byte
	count := 0
	for k, v := range items {
		var hdr [8]byte
		put32le(hdr[0:4], len(v))
		put32le(hdr[4:8], 0)
		body = append(body, hdr[:]...)
		body = append(body, []byte(k)...)
		body = append(body, 0)
		body = append(body, []byte(v)...)
		count++
	}
	foot := make([]byte, 32)
	copy(foot[0:8], "APETAGEX")
	put32le(foot[8:12], 2000)
	put32le(foot[12:16], len(body)+32)
	put32le(foot[16:20], count)
	return append(body, foot...)
}

func put32le(b []byte, v int) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func TestMP3StripLegacy(t *testing.T) {
	data := id3v2(3, textFrame(3, "TIT2", "Keep"))
	data = append(data, mp3Audio(t)...)
	data = append(data, id3v1("Old V1", "", 255)...)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Keep").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("stripping a present legacy tag is not a no-op")
	}
	out := applyToBytes(t, data, plan)
	if bytes.HasSuffix(out, id3v1("Old V1", "", 255)) {
		t.Error("ID3v1 should have been stripped")
	}
	if mustParseBytes(t, out).Fields().Title != "Keep" {
		t.Error("ID3v2 title should survive the strip")
	}
}

func TestMP3CoverRoundTrip(t *testing.T) {
	src := readFixture(t, sampleMP3)
	before := essenceOf(t, src)

	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if after := essenceOf(t, out); !before.Equal(after) {
		t.Error("essence changed when adding a cover")
	}
	got := mustParseBytes(t, out)
	if len(got.Pictures()) != 1 || got.Pictures()[0].Type != wl.PicFrontCover {
		t.Fatalf("pictures = %+v", got.Pictures())
	}
	if got.Pictures()[0].MIME != "image/png" {
		t.Errorf("MIME = %q, want image/png", got.Pictures()[0].MIME)
	}
	// Remove it again.
	plan2, _ := got.Edit().ClearPictures().Prepare()
	if n := len(mustParseBytes(t, applyToBytes(t, out, plan2)).Pictures()); n != 0 {
		t.Errorf("ClearPictures left %d pictures", n)
	}
}

func TestMP3NoOpWritesNothing(t *testing.T) {
	path := copyToTemp(t, sampleMP3)
	before, _ := os.ReadFile(path)
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, doc.Fields().Title).Prepare() // same value
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Fatal("re-setting the same title should be a no-op")
	}
	_, res, err := plan.Execute(context.Background(), wl.SaveBack())
	if err != nil {
		t.Fatal(err)
	}
	if res.Committed {
		t.Error("a no-op SaveBack must not write")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("no-op SaveBack changed the file")
	}
}

func TestMP3V23MultiValueWarns(t *testing.T) {
	src := readFixture(t, sampleMP3) // v2.3
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Artist, "A", "B", "C").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnID3MultiValue {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected an id3-multi-value warning in the report; got %v", plan.Report().Warnings)
	}
	// The values still round-trip for our own reader.
	got := mustParseBytes(t, applyToBytes(t, src, plan)).Fields().Artists
	if !slices.Equal(got, []string{"A", "B", "C"}) {
		t.Errorf("multi-value artists round-trip = %v", got)
	}
}
