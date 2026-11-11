package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Synthetic RIFF/WAVE builders. The data chunk is silence; tests assert on
// metadata structure and round-trips, not on decoded audio.

// wavID3 wraps ID3v2 tag bytes in an "id3 " chunk.
func wavID3(tagBytes []byte) []byte { return testfile.WAVChunk("id3 ", tagBytes) }

func TestWAVId3TakesPrecedenceOverInfo(t *testing.T) {
	// id3 and INFO disagree on the title; the id3 value wins and the INFO value
	// is surfaced as an unselected (conflicting) RIFF family entry.
	id3 := wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "ID3 Title")))
	info := testfile.WAVInfo([2]string{"INAM", "INFO Title"}, [2]string{"IART", "Shared Artist"})
	data := testfile.WAV(testfile.WAVFmtPCM(), info, id3, testfile.WAVData(800))

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "ID3 Title" {
		t.Errorf("id3 should win: title = %q", doc.Fields().Title)
	}
	conflict := false
	for _, f := range doc.Families() {
		if f.Family == wl.FamilyRIFF && f.Key == tag.Title && !f.Selected {
			conflict = true
		}
	}
	if !conflict {
		t.Errorf("expected an unselected RIFF Title family entry; families = %+v", doc.Families())
	}
	// A shared, agreeing value is not a conflict.
	for _, f := range doc.Families() {
		if f.Family == wl.FamilyRIFF && f.Key == tag.Artist && !f.Selected {
			t.Errorf("agreeing artist should not be flagged: %+v", f)
		}
	}
}

func TestWAVId3PlusInfoDisjointKeysPreserved(t *testing.T) {
	// id3 carries Title; INFO carries a Copyright that id3 lacks. The INFO-only
	// value must merge into the canonical set and survive an unrelated edit.
	id3 := wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T")))
	info := testfile.WAVInfo([2]string{"ICOP", "ACME Records"})
	data := testfile.WAV(testfile.WAVFmtPCM(), info, id3, testfile.WAVData(400))

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "T" {
		t.Errorf("title = %q", doc.Fields().Title)
	}
	if doc.Fields().Copyright != "ACME Records" {
		t.Errorf("INFO-only Copyright not merged into canonical set: %q", doc.Fields().Copyright)
	}

	plan, err := doc.Edit().Set(tag.Artist, "New Artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if got := re.Fields().Copyright; got != "ACME Records" {
		t.Errorf("INFO-only Copyright lost on rewrite: %q", got)
	}
	if re.Fields().Title != "T" {
		t.Errorf("title lost on rewrite: %q", re.Fields().Title)
	}
	if !slices.Equal(re.Fields().Artists, []string{"New Artist"}) {
		t.Errorf("artist = %v", re.Fields().Artists)
	}
}

func TestWAVInfoAuthoritativeWhenNoId3(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Only INFO"}, [2]string{"IPRT", "5"}), testfile.WAVData(400))
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "Only INFO" {
		t.Errorf("INFO title = %q", doc.Fields().Title)
	}
	if doc.Fields().TrackNumber != 5 {
		t.Errorf("INFO track = %d, want 5", doc.Fields().TrackNumber)
	}
	// INFO is authoritative, so its entries are selected.
	for _, f := range doc.Families() {
		if f.Family == wl.FamilyRIFF && !f.Selected {
			t.Errorf("authoritative INFO entry should be selected: %+v", f)
		}
	}
}

// checks the ITRK read alias. INFO-only files can read track numbers from ITRK, while newly written
// track numbers use IPRT so output stays deterministic.
func TestWAVITRKReadsTrackNumber(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Track via ITRK"}, [2]string{"ITRK", "7"}), testfile.WAVData(400))
	if got := mustParseBytes(t, data).Fields().TrackNumber; got != 7 {
		t.Fatalf("ITRK track = %d, want 7", got)
	}
	// Writing a fresh track number into an INFO file with no existing track item
	// must emit the chosen IPRT identifier, not ITRK.
	base := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Has title"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, base).Edit().Set(tag.TrackNumber, "4").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, base, plan)
	if !bytes.Contains(out, []byte("IPRT")) {
		t.Error("freshly written track number should emit IPRT")
	}
	if bytes.Contains(out, []byte("ITRK")) {
		t.Error("write must not emit ITRK; IPRT is the chosen identifier")
	}
	if got := mustParseBytes(t, out).Fields().TrackNumber; got != 4 {
		t.Errorf("round-trip track = %d, want 4", got)
	}
}

func TestWAVEditInfoOnlyStaysInfoOnly(t *testing.T) {
	// Editing an INFO-representable key on an INFO-only file updates INFO in place
	// and does not introduce an id3 chunk.
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Old"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("id3 ")) {
		t.Error("an INFO-representable edit should not create an id3 chunk")
	}
	if got := mustParseBytes(t, out).Fields().Title; got != "New" {
		t.Errorf("title = %q", got)
	}
}

func TestWAVNonInfoKeyPromotesToId3(t *testing.T) {
	// Composer has no INFO identifier, so it forces an id3 chunk; the INFO chunk
	// is kept and stays in sync for the representable keys.
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "T"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Composer, "Stravinsky").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("id3 ")) {
		t.Error("a non-INFO key should create an id3 chunk")
	}
	got := mustParseBytes(t, out)
	if !slices.Equal(got.Fields().Composers, []string{"Stravinsky"}) {
		t.Errorf("composer = %v", got.Fields().Composers)
	}
	if got.Fields().Title != "T" {
		t.Errorf("promoted title = %q, want T", got.Fields().Title)
	}
}

func TestWAVMultiValueForcesId3(t *testing.T) {
	// A multi-value artist cannot be stored in single-valued INFO, so it forces
	// the id3 chunk and round-trips fully there.
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"IART", "Solo"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "A", "B").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	got := mustParseBytes(t, out)
	if !slices.Equal(got.Fields().Artists, []string{"A", "B"}) {
		t.Errorf("multi-value artist = %v", got.Fields().Artists)
	}
}

func TestWAVStripInfoConsolidatesToId3(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Keep"}, [2]string{"ISFT", "Lavf"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Keep").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("stripping a present LIST/INFO is not a no-op")
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("LIST")) || bytes.Contains(out, []byte("INFO")) {
		t.Error("LIST/INFO should have been stripped")
	}
	if !bytes.Contains(out, []byte("id3 ")) {
		t.Error("tags should have been consolidated into an id3 chunk")
	}
	if got := mustParseBytes(t, out).Fields().Title; got != "Keep" {
		t.Errorf("title after strip = %q", got)
	}
}

// Under a strip policy a tagless WAV takes its tags in an id3 chunk alone: a LIST/INFO chunk
// written now is one the next strip would remove.
func TestWAVStripOnATaglessFileWritesId3Only(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(400))
	for name, opt := range map[string]wl.WriteOption{"strip": wl.WithLegacyPolicy(wl.LegacyStrip), "minimal": wl.Minimal} {
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "T").Prepare(opt)
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if bytes.Contains(out, []byte("LIST")) || !bytes.Contains(out, []byte("id3 ")) {
			t.Errorf("%s: want an id3 chunk and no LIST/INFO; operations %v", name, plan.Report().Operations)
		}
		re := mustParseBytes(t, out)
		if re.Fields().Title != "T" {
			t.Errorf("%s: title = %q", name, re.Fields().Title)
		}
		if again, err := re.Edit().Prepare(opt); err != nil || !again.IsNoOp() {
			t.Errorf("%s: a second strip is not a no-op (err %v)", name, err)
		}
	}
}

func TestWAVStripInfoConsolidatesToId3WithExistingId3(t *testing.T) {
	// Source shape: an id3 chunk holds TITLE, while LIST/INFO holds keys not present in id3 (ARTIST,
	// COPYRIGHT). When --legacy strip removes INFO, those native-only values must be emitted into id3.
	id3Chunk := wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Original Title")))
	info := testfile.WAVInfo([2]string{"IART", "Native Artist"}, [2]string{"ICOP", "Native Copyright"})
	data := testfile.WAV(testfile.WAVFmtPCM(), info, id3Chunk, testfile.WAVData(400))

	t.Run("unchanged native-only keys survive", func(t *testing.T) {
		// The edit touches only TITLE; ARTIST and COPYRIGHT compare unchanged against the merged
		// base yet must still enter the rebuilt id3 chunk.
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New Title").
			Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if bytes.Contains(out, []byte("LIST")) || bytes.Contains(out, []byte("INFO")) {
			t.Error("LIST/INFO should have been stripped")
		}
		if !bytes.Contains(out, []byte("id3 ")) {
			t.Error("tags should have been consolidated into an id3 chunk")
		}
		re := mustParseBytes(t, out).Fields()
		if re.Title != "New Title" {
			t.Errorf("title after strip = %q", re.Title)
		}
		if !slices.Contains(re.Artists, "Native Artist") {
			t.Errorf("native-only ARTIST dropped on strip: artists = %v", re.Artists)
		}
		if re.Copyright != "Native Copyright" {
			t.Errorf("native-only COPYRIGHT dropped on strip: %q", re.Copyright)
		}
	})

	t.Run("changed native-only key survives (control)", func(t *testing.T) {
		// Changing ARTIST uses the dirty-key path; untouched COPYRIGHT must also migrate.
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "Changed Artist").
			Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		re := mustParseBytes(t, out).Fields()
		if !slices.Contains(re.Artists, "Changed Artist") {
			t.Errorf("changed native-only ARTIST lost on strip: artists = %v", re.Artists)
		}
		if re.Copyright != "Native Copyright" {
			t.Errorf("unchanged native-only COPYRIGHT dropped: %q", re.Copyright)
		}
		if re.Title != "Original Title" {
			t.Errorf("id3-only TITLE lost on strip: %q", re.Title)
		}
	})
}

func TestWAVPreservesUnknownChunks(t *testing.T) {
	// A "bext" chunk and a "cue " chunk (neither modeled) must survive an edit
	// byte-for-byte and keep their order relative to data.
	bext := testfile.WAVChunk("bext", []byte("broadcast-extension-payload!!"))
	cue := testfile.WAVChunk("cue ", []byte{1, 2, 3, 4})
	data := testfile.WAV(testfile.WAVFmtPCM(), bext, testfile.WAVInfo([2]string{"INAM", "X"}), testfile.WAVData(400), cue)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Y").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, bext) {
		t.Error("bext chunk was not preserved verbatim")
	}
	if !bytes.Contains(out, cue) {
		t.Error("trailing cue chunk was not preserved verbatim")
	}
	if mustParseBytes(t, out).Fields().Title != "Y" {
		t.Error("edit did not apply")
	}
}

func TestWAVAppendedDataKeptOutsideRiffSize(t *testing.T) {
	// A 128-byte ID3v1-style tag appended after the RIFF chunk (excluded from the
	// declared RIFF size) must be preserved verbatim AND kept outside the recomputed
	// RIFF size on rewrite, so a strict RIFF reader does not misparse it as a chunk.
	base := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "T"}), testfile.WAVData(400))
	id3v1 := testfile.ID3v1("Trailing Title", "", "", "", "", 0)
	data := append(slices.Clone(base), id3v1...)

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "T" {
		t.Errorf("title = %q (appended tag should not disturb the INFO read)", doc.Fields().Title)
	}

	plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.HasSuffix(out, id3v1) {
		t.Error("appended out-of-RIFF tag was not preserved verbatim")
	}
	riffSize := int(binary.LittleEndian.Uint32(out[4:8]))
	if 8+riffSize != len(out)-len(id3v1) {
		t.Errorf("RIFF size %d should exclude the %d-byte appended tag: 8+size=%d, want %d",
			riffSize, len(id3v1), 8+riffSize, len(out)-len(id3v1))
	}
	if mustParseBytes(t, out).Fields().Title != "Edited" {
		t.Error("edit did not apply")
	}
}

// rf64File assembles an RF64 (or BW64) file: the 64-bit header, a leading ds64 chunk carrying the
// container and data sizes, then the given chunks. The data chunk's own size field is rewritten
// to the 0xFFFFFFFF marker so the file exercises the ds64 resolution path.
func rf64File(magic string, sampleCount uint64, chunks ...[]byte) []byte {
	body := []byte("WAVE")
	var dataSize uint64
	for _, c := range chunks {
		if string(c[0:4]) == "data" {
			dataSize = uint64(binary.LittleEndian.Uint32(c[4:8]))
			c = slices.Clone(c)
			copy(c[4:8], []byte{0xFF, 0xFF, 0xFF, 0xFF})
		}
		body = append(body, c...)
	}
	ds := make([]byte, 28)
	binary.LittleEndian.PutUint64(ds[8:16], dataSize)
	binary.LittleEndian.PutUint64(ds[16:24], sampleCount)
	body = slices.Concat(body[:4], testfile.WAVChunk("ds64", ds), body[4:])
	binary.LittleEndian.PutUint64(body[12:20], uint64(len(body))) // ds64.riffSize

	out := append([]byte(magic), []byte{0xFF, 0xFF, 0xFF, 0xFF}...)
	return append(out, body...)
}

// checks the 64-bit RIFF extension end to end: the ds64 chunk resolves the marked sizes on read,
// and a metadata edit keeps the RF64 form with a regenerated ds64 rather than downgrading to plain
// RIFF (which would truncate the sizes the extension exists to carry).
func TestWAVRF64RoundTrip(t *testing.T) {
	for _, magic := range []string{"RF64", "BW64"} {
		t.Run(magic, func(t *testing.T) {
			src := rf64File(magic, 16, testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Before"}), testfile.WAVData(64))
			doc := mustParseBytes(t, src)
			if doc.Format() != wl.FormatWAV {
				t.Fatalf("format = %v, want WAV", doc.Format())
			}
			if got := doc.Fields().Title; got != "Before" {
				t.Fatalf("title = %q, want Before", got)
			}
			if tr := doc.Properties().First(); tr.SampleRate != 44100 || tr.Channels != 2 {
				t.Errorf("track = %+v, want 44100 Hz stereo", tr)
			}

			plan, err := doc.Edit().Set(tag.Title, "After").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, src, plan)

			if string(out[0:4]) != magic {
				t.Errorf("output header = %q, want the %s form preserved", out[0:4], magic)
			}
			if got := binary.LittleEndian.Uint32(out[4:8]); got != 0xFFFFFFFF {
				t.Errorf("output container size field = %#x, want the RF64 marker", got)
			}
			if string(out[12:16]) != "ds64" {
				t.Fatalf("ds64 is not the first chunk: %q", out[12:16])
			}
			if got, want := binary.LittleEndian.Uint64(out[20:28]), uint64(len(out)-8); got != want {
				t.Errorf("ds64.riffSize = %d, want %d", got, want)
			}
			if got := binary.LittleEndian.Uint64(out[28:36]); got != 64 {
				t.Errorf("ds64.dataSize = %d, want 64", got)
			}
			if got := binary.LittleEndian.Uint64(out[36:44]); got != 16 {
				t.Errorf("ds64.sampleCount = %d, want the source value 16 carried through", got)
			}
			re := mustParseBytes(t, out)
			if re.Fields().Title != "After" {
				t.Errorf("title after edit = %q", re.Fields().Title)
			}
			if ws := re.Warnings(); len(ws) != 0 {
				t.Errorf("unexpected warnings after an RF64 rewrite: %v", ws)
			}
		})
	}
}

// same edit against an independently authored RF64 file (ffmpeg -rf64 always), so the ds64 reading
// is checked against another writer's bytes, not only the shape this package synthesizes.
func TestWAVRF64FixtureRoundTrip(t *testing.T) {
	src := readFixture(t, sampleRF64)
	doc := mustParseBytes(t, src)
	if got := doc.Fields().Title; got != "Sample Title" {
		t.Fatalf("title = %q, want Sample Title", got)
	}
	before := essenceOf(t, src)

	plan, err := doc.Edit().Set(tag.Album, "Edited Album").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)

	if string(out[0:4]) != "RF64" {
		t.Errorf("output header = %q, want RF64 preserved", out[0:4])
	}
	if got, want := binary.LittleEndian.Uint64(out[20:28]), uint64(len(out)-8); got != want {
		t.Errorf("ds64.riffSize = %d, want %d", got, want)
	}
	if after := essenceOf(t, out); !before.Equal(after) {
		t.Error("audio essence changed across an RF64 tag edit")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Album != "Edited Album" {
		t.Errorf("album after edit = %q", re.Fields().Album)
	}
	if re.Properties().First().Duration != doc.Properties().First().Duration {
		t.Error("duration changed across the rewrite")
	}
}

// ds64 chunk is mandatory, and a file claiming the 64-bit form without one has no way to report its
// real sizes. That is corrupt input (exit 4), not an unsupported format.
func TestWAVRF64WithoutDS64Rejected(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(64))
	copy(data[0:4], "RF64")
	if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Fatalf("RF64 without ds64: err = %v, want ErrInvalidData", err)
	}
}

func TestWAVId3ChunkUppercaseVariantRead(t *testing.T) {
	// Some tools write the chunk id as "ID3 " (uppercase). It must read as a tag.
	chunk := testfile.WAVChunk("ID3 ", testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Upper")))
	data := testfile.WAV(testfile.WAVFmtPCM(), chunk, testfile.WAVData(200))
	if got := mustParseBytes(t, data).Fields().Title; got != "Upper" {
		t.Errorf("uppercase ID3 chunk title = %q", got)
	}
}

func TestWAVDuplicateInfoChunksDropped(t *testing.T) {
	// Two LIST/INFO chunks: the first is authoritative, a warning is raised, and a
	// rewrite drops the stale duplicate so the output is single and consistent.
	data := testfile.WAV(testfile.WAVFmtPCM(),
		testfile.WAVInfo([2]string{"INAM", "First"}),
		testfile.WAVInfo([2]string{"INAM", "Second"}),
		testfile.WAVData(400))

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "First" {
		t.Errorf("first INFO should be authoritative: title = %q", doc.Fields().Title)
	}
	if !hasWarning(doc, wl.WarnDuplicateTagBlock) {
		t.Errorf("expected a duplicate-tag-block warning; got %v", doc.Warnings())
	}
	foundLint := false
	for _, fi := range doc.Lint() {
		if fi.Code == "duplicate-tag-block" {
			foundLint = true
		}
	}
	if !foundLint {
		t.Error("expected a duplicate-tag-block lint finding")
	}

	plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if n := bytes.Count(out, []byte("INFO")); n != 1 {
		t.Errorf("expected exactly one INFO list after rewrite, found %d", n)
	}
	if bytes.Contains(out, []byte("Second")) {
		t.Error("stale duplicate INFO value survived the rewrite")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Edited" {
		t.Errorf("title after rewrite = %q", re.Fields().Title)
	}
	if hasWarning(re, wl.WarnDuplicateTagBlock) {
		t.Error("the duplicate should be gone after a rewrite")
	}
}

func TestWAVDuplicateId3ChunksDropped(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(),
		wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Primary"))),
		wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Stale"))),
		testfile.WAVData(400))
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "Primary" {
		t.Errorf("first id3 should be authoritative: title = %q", doc.Fields().Title)
	}
	if !hasWarning(doc, wl.WarnDuplicateTagBlock) {
		t.Errorf("expected a duplicate-tag-block warning; got %v", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("Stale")) {
		t.Error("stale duplicate id3 chunk survived the rewrite")
	}
	if mustParseBytes(t, out).Fields().Title != "Edited" {
		t.Error("edit did not apply")
	}
}

func TestWAVCorruptId3NotDuplicatedOnForcedRewrite(t *testing.T) {
	// A lone "id3 " chunk whose body fails to parse leaves no authoritative id3. An edit forcing a new
	// id3 chunk (a non-INFO key) must drop the stale chunk so the output carries exactly one id3 chunk,
	// not two (which a re-parse would flag as a duplicate).
	corrupt := testfile.WAVChunk("id3 ", []byte("corrupt-not-a-valid-tag")) // fails id3.ParseTag
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "T"}), testfile.WAVData(400), corrupt)
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "T" { // INFO authoritative; the corrupt id3 gave nothing
		t.Fatalf("title = %q", doc.Fields().Title)
	}
	plan, err := doc.Edit().Set(tag.Composer, "Stravinsky").Prepare() // non-INFO key -> forces id3
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if n := bytes.Count(out, []byte("id3 ")); n != 1 {
		t.Errorf("expected exactly one id3 chunk in output, found %d", n)
	}
	re := mustParseBytes(t, out)
	if !slices.Equal(re.Fields().Composers, []string{"Stravinsky"}) {
		t.Errorf("composer = %v", re.Fields().Composers)
	}
	if hasWarning(re, wl.WarnDuplicateTagBlock) {
		t.Error("re-parse of the output should not see a duplicate id3 block")
	}
}

func TestWAVLatin1InfoValueDecodes(t *testing.T) {
	// A legacy Latin-1 INFO value (0xE9 == 'é') must decode to valid UTF-8 in the
	// canonical model rather than passing through as an invalid-UTF-8 string.
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "caf\xe9"}), testfile.WAVData(200))
	title := mustParseBytes(t, data).Fields().Title
	if title != "café" {
		t.Errorf("Latin-1 INFO decoded to %q, want %q", title, "café")
	}
	if !utf8.ValidString(title) {
		t.Error("decoded title is not valid UTF-8")
	}
}

func TestWAVByteRateInEssenceDigest(t *testing.T) {
	// Two files identical except for the fmt byteRate, chosen to share their low 16
	// bits (176400 and 176400+65536). A truncated 16-bit digest config would
	// collide; the full uint32 must distinguish them.
	fmtWith := func(byteRate int) []byte {
		b := slices.Concat(testfile.LE16(1), testfile.LE16(2), testfile.LE32(44100), testfile.LE32(byteRate), testfile.LE16(4), testfile.LE16(16))
		return testfile.WAVChunk("fmt ", b)
	}
	d1 := testfile.WAV(fmtWith(176400), testfile.WAVData(400))
	d2 := testfile.WAV(fmtWith(176400+65536), testfile.WAVData(400))
	if essenceOf(t, d1).Equal(essenceOf(t, d2)) {
		t.Error("essence digest ignored the high bits of byteRate (truncation regression)")
	}
}

func TestWAVClearAllRemovesInfoChunk(t *testing.T) {
	// Clearing the only tag drops the now-empty INFO chunk rather than leaving an empty husk.
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Gone"}), testfile.WAVData(200))
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if got := mustParseBytes(t, out); got.Tags().Len() != 0 {
		t.Errorf("expected no tags after clear, got %d", got.Tags().Len())
	}
}

// cross-format truncation signal: a data chunk that declares more bytes than the file holds is
// flagged, while the streaming "size unknown" sentinel a piped capture carries is not.
func TestWAVTruncatedDataChunkWarns(t *testing.T) {
	t.Run("declared overruns file", func(t *testing.T) {
		// The data header declares 100000 bytes but only 200 follow.
		dataHdr := slices.Concat([]byte("data"), testfile.LE32(100000))
		data := testfile.WAV(testfile.WAVFmtPCM(), slices.Concat(dataHdr, make([]byte, 200)))
		doc := mustParseBytes(t, data)
		if !hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("expected truncated-audio warning; got %v", doc.Warnings())
		}
		// A truncated file still has some essence, so it must not also read as no-audio.
		if hasWarning(doc, wl.WarnNoAudioFrames) {
			t.Errorf("a partly-present data chunk should not warn no-audio; got %v", doc.Warnings())
		}
	})
	t.Run("streaming sentinel not flagged", func(t *testing.T) {
		// 0xFFFFFFFF means "size unknown"; the audio is whatever follows, not a truncation. (A 0 size is
		// the other sentinel; it reads as no-audio instead.) testfile.LE32(-1) yields the same four 0xFF bytes on
		// every int width, where int(^uint32(0)) overflows a 32-bit int at compile time.
		dataHdr := slices.Concat([]byte("data"), testfile.LE32(-1))
		data := testfile.WAV(testfile.WAVFmtPCM(), slices.Concat(dataHdr, make([]byte, 400)))
		doc := mustParseBytes(t, data)
		if hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("a streaming-sentinel data size must not be flagged truncated; got %v", doc.Warnings())
		}
		// Not truncation, but the clamp takes the rest of the file as audio, so a LIST/INFO sitting after
		// the data chunk is swallowed and the file reads as untagged.
		if !hasWarning(doc, wl.WarnUnknownChunkSize) {
			t.Errorf("the size-unknown sentinel left no signal at all; got %v", doc.Warnings())
		}
		// Info severity: a non-seekable writer emits the sentinel legitimately, so a piped
		// capture must still lint clean.
		found := false
		for _, f := range doc.Lint() {
			if f.Code != "unknown-chunk-size" {
				continue
			}
			found = true
			if f.Severity != wl.LintInfo {
				t.Errorf("unknown-chunk-size severity = %v, want info", f.Severity)
			}
		}
		if !found {
			t.Errorf("lint did not promote the warning: %v", doc.Lint())
		}
	})
	t.Run("intact file not flagged", func(t *testing.T) {
		data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(400))
		if doc := mustParseBytes(t, data); hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("an intact WAV must not be flagged truncated; got %v", doc.Warnings())
		}
	})
	t.Run("zero essence reports only no-audio", func(t *testing.T) {
		// The data header declares 100000 bytes but the file ends at the header, so
		// zero essence survives. no-audio subsumes truncated for the nothing-at-all
		// case: the file must report no-audio and not also truncated-audio.
		dataHdr := slices.Concat([]byte("data"), testfile.LE32(100000))
		data := testfile.WAV(testfile.WAVFmtPCM(), dataHdr)
		doc := mustParseBytes(t, data)
		if !hasWarning(doc, wl.WarnNoAudioFrames) {
			t.Errorf("a zero-essence file should report no-audio; got %v", doc.Warnings())
		}
		if hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("no-audio subsumes truncated for zero essence; got %v", doc.Warnings())
		}
	})
}

// wavWithRiffSize rebuilds a WAV with an arbitrary declared RIFF size, leaving every chunk
// byte untouched. It is how the malformed-size cases below differ from a correct file.
func wavWithRiffSize(data []byte, declared uint32) []byte {
	out := slices.Clone(data)
	binary.LittleEndian.PutUint32(out[4:8], declared)
	return out
}

// a declared RIFF size that is in range but far too small must not be trusted as the walk boundary,
// or data and every LIST/id3 chunk past it go unseen: the file reads as no-audio with the rest
// reported as trailing bytes.
func TestWAVTooSmallRiffSizeRecovers(t *testing.T) {
	full := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Recovered"}), testfile.WAVData(400))
	// 30 bytes covers "WAVE" plus the fmt chunk and stops before the INFO list.
	data := wavWithRiffSize(full, 30)

	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Recovered" {
		t.Errorf("TITLE = %q, want Recovered (the INFO list past the declared size)", got)
	}
	if hasWarning(doc, wl.WarnNoAudioFrames) {
		t.Errorf("the data chunk is present past the declared size; no-audio must not fire: %v", doc.Warnings())
	}
	if hasWarning(doc, wl.WarnTrailingBytes) {
		t.Errorf("the recovered chunks are in-container, not trailing bytes: %v", doc.Warnings())
	}
	if !hasWarning(doc, wl.WarnDistrustedBlockSize) {
		t.Errorf("distrusting the declared size must be reported: %v", doc.Warnings())
	}
	if d := doc.Properties().Duration(); d <= 0 {
		t.Errorf("duration = %v, want the recovered data chunk's", d)
	}
}

// a tagger that appended a LIST without updating the RIFF size leaves it outside the container; the
// read must still find it, or a rewrite emits a second LIST beside the stranded one.
func TestWAVShortRiffSizeStrandedTagRecovered(t *testing.T) {
	full := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(400), testfile.WAVInfo([2]string{"INAM", "Stranded"}))
	// A size covering everything but the trailing LIST, as if it predated the append.
	data := wavWithRiffSize(full, uint32(len(full)-8-len(testfile.WAVInfo([2]string{"INAM", "Stranded"}))))

	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Stranded" {
		t.Errorf("TITLE = %q, want Stranded (the LIST past the declared size)", got)
	}
	plan, err := doc.Edit().Set(tag.Artist, "Added").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if n := bytes.Count(out, []byte("LIST")); n != 1 {
		t.Errorf("output holds %d LIST chunks, want 1", n)
	}
	re := mustParseBytes(t, out)
	if got := re.Fields().Title; got != "Stranded" {
		t.Errorf("TITLE after rewrite = %q, want Stranded", got)
	}
}

// other half of the rule: when the wide re-walk finds no audio, the narrow result stands.
func TestWAVTruncatedNoRecovery(t *testing.T) {
	base := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Tagged"}))
	data := append(slices.Clone(base), bytes.Repeat([]byte{0xAB}, 64)...)

	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Tagged" {
		t.Errorf("TITLE = %q, want Tagged", got)
	}
	if !hasWarning(doc, wl.WarnNoAudioFrames) {
		t.Errorf("a WAV with no data chunk must still warn no-audio: %v", doc.Warnings())
	}
	if !hasWarning(doc, wl.WarnTrailingBytes) {
		t.Errorf("bytes past the container must still report as trailing: %v", doc.Warnings())
	}
	if hasWarning(doc, wl.WarnDistrustedBlockSize) {
		t.Errorf("nothing was recovered, so the declared size was not distrusted: %v", doc.Warnings())
	}
}

// the recovery retry must not disturb the sizes the existing fallback already handles.
func TestWAVMalformedRiffSizesUnchanged(t *testing.T) {
	full := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Intact"}), testfile.WAVData(400))
	declared := binary.LittleEndian.Uint32(full[4:8])
	cases := map[string]uint32{
		"zero":      0,
		"all ones":  0xFFFFFFFF,
		"one short": declared - 1,
		"one long":  declared + 1,
		"oversized": declared + 4096,
		"exact":     declared,
	}
	for name, sz := range cases {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, wavWithRiffSize(full, sz))
			if got := doc.Fields().Title; got != "Intact" {
				t.Errorf("TITLE = %q, want Intact", got)
			}
			if hasWarning(doc, wl.WarnNoAudioFrames) {
				t.Errorf("the data chunk is present; no-audio must not fire: %v", doc.Warnings())
			}
		})
	}
}

// correct declared size with out-of-container bytes after it keeps reporting them as trailing.
func TestWAVAppendedBytesStillTrailing(t *testing.T) {
	base := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(400))
	data := append(slices.Clone(base), bytes.Repeat([]byte{0xCD}, 40)...)
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnTrailingBytes) {
		t.Errorf("appended out-of-container bytes must report as trailing: %v", doc.Warnings())
	}
	if hasWarning(doc, wl.WarnDistrustedBlockSize) {
		t.Errorf("a correct declared size must not be distrusted: %v", doc.Warnings())
	}
}

// wavInfoNoPad builds a LIST/INFO chunk whose items carry no word-alignment pad byte after an
// odd-size value.
func wavInfoNoPad(pairs ...[2]string) []byte {
	body := []byte("INFO")
	for _, p := range pairs {
		val := append([]byte(p[1]), 0)
		body = append(body, []byte(p[0])...)
		body = append(body, testfile.LE32(len(val))...)
		body = append(body, val...)
	}
	return testfile.WAVChunk("LIST", body)
}

// wavInfoWithTail builds a well-formed LIST/INFO chunk with a region appended that no item
// walk can read: too short to hold another item header, and not the clean end of the list.
func wavInfoWithTail(tail []byte, pairs ...[2]string) []byte {
	body := []byte("INFO")
	for _, p := range pairs {
		val := append([]byte(p[1]), 0)
		body = append(body, []byte(p[0])...)
		body = append(body, testfile.LE32(len(val))...)
		body = append(body, val...)
		if len(val)&1 == 1 {
			body = append(body, 0)
		}
	}
	return testfile.WAVChunk("LIST", append(body, tail...))
}

// unpaddedInfoPairs is the malformed shape: four items whose first is odd-size and
// unpadded, so every item after it lands one byte off for a reader that steps over the
// word-alignment byte unconditionally.
var unpaddedInfoPairs = [][2]string{
	{"INAM", "Song"}, {"IART", "Band"}, {"IPRD", "Album"}, {"ICMT", "Note"},
}

// the walk must not desynchronize on the missing pad byte: stopping there with no warning would
// destroy everything past that point on the next rewrite, because rebuildInfo re-renders the chunk
// from the items alone.
func TestWAVUnpaddedInfoReadsEveryItem(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"riff", testfile.WAV(testfile.WAVFmtPCM(), wavInfoNoPad(unpaddedInfoPairs...), testfile.WAVData(400))},
		{"rf64", rf64File("RF64", 100, testfile.WAVFmtPCM(), wavInfoNoPad(unpaddedInfoPairs...), testfile.WAVData(400))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParseBytes(t, tc.data)
			f := doc.Fields()
			if f.Title != "Song" || f.Album != "Album" {
				t.Errorf("title = %q, album = %q, want Song / Album", f.Title, f.Album)
			}
			if len(f.Artists) != 1 || f.Artists[0] != "Band" {
				t.Errorf("artists = %v, want [Band]", f.Artists)
			}
			if v, _ := doc.Get(tag.Comment); len(v) != 1 || v[0] != "Note" {
				t.Errorf("COMMENT = %v, want [Note]", v)
			}
			if !hasWarning(doc, wl.WarnMalformedTagEntry) {
				t.Errorf("the missing pad byte went unreported: %v", doc.Warnings())
			}
			if !lintHasCode(doc, "malformed-tag-entry") {
				t.Errorf("lint did not promote the warning: %v", doc.Lint())
			}
			// An unrelated edit re-renders the list padded, so the values past the desync survive
			// and the file stops reporting the condition.
			plan, err := doc.Edit().Set(tag.Genre, "Rock").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			re := mustParseBytes(t, applyToBytes(t, tc.data, plan))
			if re.Fields().Album != "Album" {
				t.Errorf("IPRD did not survive the edit: album = %q", re.Fields().Album)
			}
			if v, _ := re.Get(tag.Comment); len(v) != 1 || v[0] != "Note" {
				t.Errorf("ICMT did not survive the edit: COMMENT = %v", v)
			}
			if hasWarning(re, wl.WarnMalformedTagEntry) {
				t.Errorf("the rewritten file still reports the condition it fixed: %v", re.Warnings())
			}
		})
	}
}

// control: a properly padded list must not gain the warning, or every ordinary WAV would report
// one.
func TestWAVWellFormedInfoStaysQuiet(t *testing.T) {
	doc := mustParseBytes(t, testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo(unpaddedInfoPairs...), testfile.WAVData(400)))
	if hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("a well-formed LIST/INFO must stay quiet: %v", doc.Warnings())
	}
}

// region the item walk cannot read has nowhere to go in a rebuilt chunk, so the read warning is not
// enough; the write must say the bytes are gone.
func TestWAVUnreadableInfoTailReportedOnWrite(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(),
		wavInfoWithTail([]byte{0x01, 0x02, 0x03}, [2]string{"INAM", "Song"}), testfile.WAVData(400))
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Fatalf("the unreadable tail went unreported on read: %v", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Artist, "Band").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarn(plan.Report().Warnings, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("the write did not report the dropped region: %v", plan.Report().Warnings)
	}
}

// LIST whose declared size runs past EOF was cut by the walker, not by the writer, so naming an
// unread tail would double-report a condition oversized-chunk already states.
func TestWAVClampedOversizeInfoReportsOversizedOnly(t *testing.T) {
	list := testfile.WAVInfo([2]string{"INAM", "Song"})
	over := slices.Clone(list)
	copy(over[4:8], testfile.LE32(1<<20)) // declare a megabyte the file does not hold
	doc := mustParseBytes(t, testfile.WAV(testfile.WAVFmtPCM(), over, testfile.WAVData(400)))
	if !hasWarning(doc, wl.WarnOversizedChunk) {
		t.Fatalf("the clamped LIST was not reported: %v", doc.Warnings())
	}
	if hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("a clamped chunk must not also report an unreadable tail: %v", doc.Warnings())
	}
}

// documents the split the read suppression must not leak into: the read message is silenced where
// another code already names the condition, but what a rewrite destroys is reported whatever put it
// there.
func TestWAVWriteLossIsNotGatedOnHowTheRegionArose(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(),
		wavInfoWithTail([]byte{0x01, 0x02, 0x03}, [2]string{"INAM", "Song"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "Band").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarn(plan.Report().Warnings, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("the write did not report what it destroys: %v", plan.Report().Warnings)
	}
}

// LIST the walker clamped because it declared the size-unknown value is reported as
// unknown-chunk-size, so naming an unreadable tail as well would both duplicate the diagnosis and
// make set --strict refuse a file whose only defect is the streaming sentinel.
func TestWAVSentinelInfoDoesNotDoubleReport(t *testing.T) {
	list := wavInfoWithTail([]byte{0x01, 0x02, 0x03}, [2]string{"INAM", "Song"})
	copy(list[4:8], testfile.LE32(-1)) // the 0xFFFFFFFF size-unknown value
	doc := mustParseBytes(t, testfile.WAV(testfile.WAVFmtPCM(), list, testfile.WAVData(400)))
	if !hasWarning(doc, wl.WarnUnknownChunkSize) {
		t.Fatalf("the sentinel went unreported: %v", doc.Warnings())
	}
	if hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("a clamped chunk must not also report an unreadable tail: %v", doc.Warnings())
	}
}

// renderInfo writes an item's value up to its first NUL plus one terminator, so bytes the item
// declared past it die on the next rewrite.
func TestWAVBytesPastTheTerminatorReportedAndRefused(t *testing.T) {
	item := slices.Concat([]byte("INAM"), testfile.LE32(16), []byte("Song\x00HIDDENDATA\x00"))
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVChunk("LIST", append([]byte("INFO"), item...)), testfile.WAVData(400))
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "Song" {
		t.Errorf("title = %q, want Song", doc.Fields().Title)
	}
	if !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Fatalf("the bytes past the terminator went unreported: %v", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Artist, "Band").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarn(plan.Report().Warnings, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("the write did not report the dropped bytes: %v", plan.Report().Warnings)
	}
}

// false-positive control: a writer that rounds a LIST body up to a four-byte boundary leaves zeros
// the item model does not carry but a rewrite re-creates, so warning about them would fail --strict
// on an ordinary file.
func TestWAVListAlignmentZerosStayQuiet(t *testing.T) {
	list := wavInfoWithTail(make([]byte, 8), [2]string{"INAM", "Song"})
	doc := mustParseBytes(t, testfile.WAV(testfile.WAVFmtPCM(), list, testfile.WAVData(400)))
	if hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("alignment zeros are not a loss: %v", doc.Warnings())
	}
	// They are not read as an item either: a run of zeros has no printable identifier, and
	// re-emitting one would write a non-conformant item back out.
	for _, e := range doc.Native().Describe() {
		if strings.HasPrefix(e.Kind, "  \x00") {
			t.Errorf("a run of zeros was read as an INFO item: %q", e.Kind)
		}
	}
}

// inside an RF64/BW64 container the 32-bit 0xFFFFFFFF is always the ds64 marker, so a chunk ds64
// does not resolve keeps its own size and clamps like any other overrun. Reading it as the
// streaming sentinel would exempt a truncated RF64 from truncated-audio.
func TestRF64UnresolvedMarkerIsNotTheStreamingSentinel(t *testing.T) {
	src := rf64File("RF64", 16, testfile.WAVFmtPCM(), testfile.WAVData(64))
	// Blank the ds64 data size so the override declines for the data chunk, leaving its
	// on-disk 0xFFFFFFFF unresolved.
	i := bytes.Index(src, []byte("ds64"))
	if i < 0 {
		t.Fatal("no ds64 chunk")
	}
	binary.LittleEndian.PutUint64(src[i+8+8:i+8+16], 1<<40) // a data size the file cannot hold
	doc := mustParseBytes(t, src)
	if hasWarning(doc, wl.WarnUnknownChunkSize) {
		t.Errorf("an RF64 marker is not the streaming sentinel: %v", doc.Warnings())
	}
	if !hasWarning(doc, wl.WarnTruncatedAudio) {
		t.Errorf("a data chunk declaring more than the file holds is truncation: %v", doc.Warnings())
	}
}

// id3 chunk wins the projection, but the INFO item it disagrees with is the file's own data; an
// edit that does not name the key leaves it byte for byte, warns nothing, and keeps the conflict
// visible in the families.
func TestWAVConflictingInfoValueSurvivesUnrelatedEdit(t *testing.T) {
	id3 := wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Id3 Title")))
	info := testfile.WAVInfo([2]string{"INAM", "Riff Title"})
	data := testfile.WAV(testfile.WAVFmtPCM(), info, id3, testfile.WAVData(400))

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "Z").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnValueDropped || w.Code == wl.WarnNativeValueReduced {
			t.Errorf("unrelated edit must not report a loss: %v", w)
		}
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("Riff Title")) {
		t.Fatal("INFO title was overwritten by the id3 value")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Id3 Title" || re.Fields().Album != "Z" {
		t.Errorf("title/album = %q/%q", re.Fields().Title, re.Fields().Album)
	}
	conflict := false
	for _, f := range re.Families() {
		if f.Family == wl.FamilyRIFF && f.Key == tag.Title && !f.Selected && f.Values[0] == "Riff Title" {
			conflict = true
		}
	}
	if !conflict {
		t.Errorf("the RIFF title conflict should still be reported: %+v", re.Families())
	}
}

// IPRT and ITRK both read as TRACKNUMBER; an unrelated edit keeps both, and a track edit updates
// both so ffmpeg (which reads ITRK) and IPRT readers agree.
func TestWAVAlternateTrackIdentifierSurvives(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"IPRT", "7"}, [2]string{"ITRK", "9"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "Z").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("ITRK")) || !bytes.Contains(out, []byte("IPRT")) {
		t.Fatal("an unrelated edit dropped a track identifier")
	}
	if got := mustParseBytes(t, out).Fields().TrackNumber; got != 7 {
		t.Errorf("track = %d, want 7 (first identifier wins)", got)
	}

	plan, err = mustParseBytes(t, out).Edit().Set(tag.TrackNumber, "3").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out = applyToBytes(t, out, plan)
	items := infoItemsOf(t, out)
	if items["IPRT"] != "3" || items["ITRK"] != "3" {
		t.Errorf("after a track edit IPRT=%q ITRK=%q, want both 3", items["IPRT"], items["ITRK"])
	}
}

// two INAM and two ICMT items are the file's own shape. An unrelated edit keeps all four without
// spilling them into a new id3 chunk; a title edit collapses the title to one item and leaves the
// comments alone.
func TestWAVDuplicateInfoItemsSurvive(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"ICMT", "c1"}, [2]string{"ICMT", "c2"}, [2]string{"INAM", "t1"}, [2]string{"INAM", "t2"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "Z").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("id3 ")) {
		t.Error("an unrelated edit must not create an id3 chunk to hold INFO's own duplicates")
	}
	for _, v := range []string{"c1", "c2", "t1", "t2"} {
		if !bytes.Contains(out, []byte(v)) {
			t.Errorf("duplicate item %q lost", v)
		}
	}
	plan, err = mustParseBytes(t, out).Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out = applyToBytes(t, out, plan)
	if bytes.Count(out, []byte("INAM")) != 1 || !bytes.Contains(out, []byte("c2")) {
		t.Errorf("title edit should leave one INAM and both comments; got %d INAM", bytes.Count(out, []byte("INAM")))
	}
}

// setting a key to the value the projection already holds is how a user resolves a conflicting INFO
// item, so it is a write that re-renders the item and reports what it did.
func TestWAVExplicitSetResolvesInfoConflict(t *testing.T) {
	id3 := wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Id3 Title")))
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Riff Title"}), id3, testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Id3 Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("resolving a conflict must not be a no-op")
	}
	if !slices.Contains(plan.Report().Operations, "LIST/INFO conflict resolved (TITLE)") {
		t.Errorf("operations = %v", plan.Report().Operations)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("Riff Title")) {
		t.Error("the conflicting INFO value should have been replaced")
	}
	for _, f := range mustParseBytes(t, out).Families() {
		if !f.Selected {
			t.Errorf("no conflict should remain: %+v", f)
		}
	}
}

func TestWAVTrackTotalEditRewritesPairItem(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"IPRT", "4/9"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.TrackTotal, "12").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if got := infoItemsOf(t, applyToBytes(t, data, plan))["IPRT"]; got != "4/12" {
		t.Errorf("IPRT = %q, want 4/12", got)
	}
}

// infoItemsOf returns the first value of each INFO identifier in a WAV's LIST/INFO chunk.
func infoItemsOf(t *testing.T, wav []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	i := 12
	for i+8 <= len(wav) {
		size := int(binary.LittleEndian.Uint32(wav[i+4:]))
		if string(wav[i:i+4]) == "LIST" && i+12 <= len(wav) && string(wav[i+8:i+12]) == "INFO" {
			break
		}
		i += 8 + size + size&1
	}
	if i+8 > len(wav) {
		return out
	}
	size := int(binary.LittleEndian.Uint32(wav[i+4:]))
	body := wav[i+12 : i+8+size]
	for len(body) >= 8 {
		id := string(body[:4])
		n := int(binary.LittleEndian.Uint32(body[4:]))
		val := body[8 : 8+n]
		if _, dup := out[id]; !dup {
			out[id] = string(bytes.TrimRight(val, "\x00"))
		}
		body = body[8+n+n&1:]
	}
	return out
}

// number and total share one IPRT item, so an edit naming only the number still has to judge
// whether the total can be kept.
func TestWAVTrackTotalSurvivesNumberOnlyEdit(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"IPRT", "4/9"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.TrackNumber).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if got, ok := re.Tags().Get(tag.TrackTotal); !ok || got[0] != "9" {
		t.Errorf("TRACKTOTAL = %v (ok=%v), want [9] carried into the id3 chunk", got, ok)
	}
}

// non-numeric number cannot compose "A1/9", so neither container can hold the total and the write
// must say so.
func TestWAVUnrepresentableTrackTotalStillWarns(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"IPRT", "4/9"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.TrackNumber, "A1").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarn(plan.Report().Warnings, wl.WarnValueDropped) {
		t.Errorf("dropping TRACKTOTAL must warn: %v", plan.Report().Warnings)
	}
}

// CLI turns the stamp strip on for any edit naming ENCODER, so the writer's gate has to read the
// same signal.
func TestWAVSetEncoderToTheStoredStampKeepsIt(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"ISFT", "Lavf62.3.100"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Encoder, "Lavf62.3.100").
		Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if got, ok := mustParseBytes(t, out).Tags().Get(tag.Encoder); !ok || got[0] != "Lavf62.3.100" {
		t.Errorf("ENCODER = %v (ok=%v), want the value the edit set", got, ok)
	}
}

func TestWAVStripEncoderStampWithoutAnEncoderEditStillStrips(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"ISFT", "Lavf62.3.100"}, [2]string{"INAM", "T"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "Z").Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, data, plan); bytes.Contains(out, []byte("Lavf62.3.100")) {
		t.Error("an inherited stamp the edit did not author should still be stripped")
	}
}

// --legacy strip deletes the LIST chunk, so a plan that also claimed to have resolved a conflict
// inside it would describe a write that never happened.
func TestWAVStrippedInfoReportsNoConflictResolution(t *testing.T) {
	id3 := wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Id3 Title")))
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Riff Title"}), id3, testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Id3 Title").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range plan.Report().Operations {
		if strings.Contains(op, "conflict resolved") {
			t.Errorf("a stripped LIST/INFO cannot have a conflict resolved in it: %v", plan.Report().Operations)
		}
	}
}

// --legacy strip moves the LIST/INFO items into the id3 chunk, but a value the id3 chunk disagrees
// with has no frame to move into, and neither has an item no canonical key holds.
func TestWAVLegacyStripWarnsAboutDestroyedInfoValues(t *testing.T) {
	id3 := wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Id3 Title")))
	file := func(items ...[2]string) []byte {
		return testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo(items...), id3, testfile.WAVData(400))
	}
	data := file([2]string{"INAM", "Riff Title"}, [2]string{"IKEY", "Alice"})
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "Z").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(applyToBytes(t, data, plan), []byte("Riff Title")) {
		t.Fatal("the strip was expected to destroy the unselected INAM value")
	}
	w, ok := warningFor(plan, wl.WarnLegacyStripDropped)
	if !ok {
		t.Fatalf("the strip destroyed a value and reported nothing: %v", plan.Report().Warnings)
	}
	if !slices.Equal(w.Keys, []tag.Key{tag.Title}) {
		t.Errorf("warning keys = %v, want TITLE", w.Keys)
	}
	for _, want := range []string{"TITLE", "IKEY"} {
		if !strings.Contains(w.Message, want) {
			t.Errorf("the warning should name %s, got %q", want, w.Message)
		}
	}

	// An edit that writes the value the item held loses nothing: the id3 chunk takes it.
	data = file([2]string{"INAM", "Riff Title"})
	rescue, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Riff Title").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(rescue, wl.WarnLegacyStripDropped); ok {
		t.Errorf("an edit that writes the INFO value must not report it lost: %v", rescue.Report().Warnings)
	}
}

// An id3 chunk whose frames an edit leaves alone is copied whole, padding and identifier included,
// and reports no rewrite.
func TestWAVUnchangedId3ChunkIsCopiedWhole(t *testing.T) {
	id3 := testfile.WAVChunk("ID3 ", testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Id3 Title"), make([]byte, 64)))
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Riff Title"}), id3, testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Id3 Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, data, plan); !bytes.Contains(out, id3) || !bytes.Contains(out, []byte("INAM")) {
		t.Error("the id3 chunk was not copied whole beside the rewritten LIST/INFO")
	}
	if ops := plan.Report().Operations; slices.Contains(ops, "id3 chunk rewrite") || !slices.Contains(ops, "LIST/INFO rewrite") {
		t.Errorf("operations %v, want the LIST/INFO rewrite and no id3 rewrite", ops)
	}
}

// A rewritten id3 chunk keeps its identifier, and its size while the tag had padding the new frames
// fit in; a tag without padding gains none. No padding option changes that.
func TestWAVRewrittenId3ChunkKeepsItsIdAndPadding(t *testing.T) {
	padded := testfile.WAVChunk("ID3 ", testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T"), make([]byte, 64)))
	bare := testfile.WAVChunk("ID3 ", testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Title")))
	for _, c := range []struct {
		name  string
		chunk []byte
		title string
		want  int // the rewritten chunk's body length
		opts  []wl.WriteOption
	}{
		{"padding absorbs the growth", padded, "A Longer Title", len(padded) - 8, nil},
		{"the frames outgrow the padding", padded, strings.Repeat("x", 200), 10 + 10 + 1 + 200, nil},
		{"no padding to keep", bare, "T", 10 + 10 + 1 + 1, nil},
		// The padding is not the caller's to set, as an Opus comment packet's is not.
		{"the caller asks for none", padded, "Title", len(padded) - 8, []wl.WriteOption{wl.WithPadding(wl.PaddingPolicy{})}},
		{"the minimal preset", padded, "Title", len(padded) - 8, []wl.WriteOption{wl.Minimal}},
		{"the caller asks for more", padded, "Title", len(padded) - 8, []wl.WriteOption{wl.WithPadding(wl.PaddingPolicy{Target: 4096, Min: 4096, ReuseInPlace: true})}},
	} {
		data := testfile.WAV(testfile.WAVFmtPCM(), c.chunk, testfile.WAVData(400))
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, c.title).Prepare(c.opts...)
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		_, chunk, found := bytes.Cut(out, []byte("ID3 "))
		if !found || bytes.Contains(out, []byte("id3 ")) {
			t.Fatalf("%s: the chunk did not keep its identifier", c.name)
		}
		if got := int(binary.LittleEndian.Uint32(chunk)); got != c.want {
			t.Errorf("%s: chunk body is %d bytes, want %d", c.name, got, c.want)
		}
		if got := mustParseBytes(t, out).Fields().Title; got != c.title {
			t.Errorf("%s: title %q", c.name, got)
		}
	}
}

// ITCH is ffmpeg's encoded_by; both it and IENG have canonical keys and read, copy and write like
// the other INFO items.
func TestWAVTechnicianAndEngineerItemsProject(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"ITCH", "Tech"}, [2]string{"IENG", "Eng"}), testfile.WAVData(400))
	doc := mustParseBytes(t, data)
	if v, _ := doc.Get(tag.EncodedBy); !slices.Equal(v, []string{"Tech"}) {
		t.Errorf("ENCODEDBY = %v", v)
	}
	if v, _ := doc.Get(tag.Engineer); !slices.Equal(v, []string{"Eng"}) {
		t.Errorf("ENGINEER = %v", v)
	}
	plan, err := doc.Edit().Set(tag.EncodedBy, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("id3 ")) || infoItemsOf(t, out)["ITCH"] != "New" {
		t.Errorf("ENCODEDBY should write to ITCH without an id3 chunk; ITCH=%q", infoItemsOf(t, out)["ITCH"])
	}
}

// A WAV id3 chunk a rewrite drops, a duplicate or a stale one a fresh chunk replaces, is graded like
// a duplicate front tag: the warning names a frame the canonical view does not carry, and a chunk
// that does not read at all, so --strict refuses instead of losing it unsaid.
func TestWAVDroppedId3ChunkNamesWhatItLoses(t *testing.T) {
	front := wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Primary")))
	popm := wavID3(testfile.ID3v2(3, testfile.ID3Frame(3, "POPM", []byte("me@example.com\x00\xC8"))))
	unreadable := wavID3(testfile.ID3v2(5, testfile.TextFrame(4, "TALB", "Future")))
	for _, c := range []struct {
		name   string
		chunks [][]byte
		want   string
	}{
		{"a duplicate holding a POPM frame", [][]byte{front, popm}, `1 "POPM" frame(s)`},
		{"an unreadable duplicate", [][]byte{front, unreadable}, "1 unreadable id3 chunk(s)"},
		{"a duplicate with bytes behind its tag", [][]byte{front, wavID3(slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Primary")), []byte("JUNKJUNK")))}, "8 unreadable byte(s)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := testfile.WAV(slices.Concat([][]byte{testfile.WAVFmtPCM()}, c.chunks, [][]byte{testfile.WAVData(400)})...)
			plan, err := mustParseBytes(t, data).Edit().Set(tag.Composer, "Stravinsky").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if msg := dupDroppedNames(plan); !strings.Contains(msg, c.want) {
				t.Errorf("dropped warning %q, want it to name %s", msg, c.want)
			}
		})
	}
}

// An id3 chunk holding more frames than the element cap refuses the parse wherever it sits, as a
// chained front tag over the cap does in an MP3; behind the kept chunk it is not taken for an
// unreadable duplicate the next edit drops.
func TestWAVId3ChunkOverTheElementCapRefuses(t *testing.T) {
	front := wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Primary")))
	var frames [][]byte
	for range 20 {
		frames = append(frames, testfile.TextFrame(3, "TALB", "Album"))
	}
	big := wavID3(testfile.ID3v2(3, frames...))
	for name, chunks := range map[string][][]byte{"ahead of the kept chunk": {big, front}, "behind the kept chunk": {front, big}} {
		data := testfile.WAV(slices.Concat([][]byte{testfile.WAVFmtPCM()}, chunks, [][]byte{testfile.WAVData(400)})...)
		_, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxElements: 8}))
		if !errors.Is(err, waxerr.ErrSizeTooLarge) {
			t.Errorf("%s: err = %v, want ErrSizeTooLarge", name, err)
		}
	}
}

// A lone WAV id3 chunk that does not parse is reported on read, kept by an edit that writes no id3
// chunk, and when a fresh one replaces it the drop is warned as an unreadable chunk's, so --strict
// refuses. An empty id3 chunk ahead of a populated one yields to it, and an edit drops the empty one
// without a word.
func TestWAVUnreadableAndEmptyId3Chunks(t *testing.T) {
	unreadable := wavID3(testfile.ID3v2(5, testfile.TextFrame(4, "TALB", "Future")))
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "T"}), unreadable, testfile.WAVData(400))
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("warnings %v, want the unreadable id3 chunk reported", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Composer, "Stravinsky").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnMalformedTagEntryDropped) || dupDroppedNames(plan) != "" {
		t.Errorf("plan warnings %v, want malformed-tag-entry-dropped and no duplicate drop", plan.Report().Warnings)
	}
	empty := wavID3(testfile.ID3v2(3))
	data = testfile.WAV(testfile.WAVFmtPCM(), empty, wavID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Kept"))), testfile.WAVData(400))
	doc = mustParseBytes(t, data)
	if doc.Fields().Title != "Kept" {
		t.Errorf("title %q, want the populated chunk's", doc.Fields().Title)
	}
	plan, err = doc.Edit().Set(tag.Composer, "Stravinsky").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if msg := dupDroppedNames(plan); msg != "" {
		t.Errorf("dropped warning %q, want none for an empty chunk", msg)
	}
}
