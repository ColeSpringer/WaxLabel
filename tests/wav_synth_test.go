package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"unicode/utf8"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Synthetic RIFF/WAVE builders. The data chunk is silence; tests assert on
// metadata structure and round-trips, not on decoded audio.

func wavLE16(n int) []byte { return []byte{byte(n), byte(n >> 8)} }
func wavLE32(n int) []byte { return []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)} }

// wavChunk wraps a chunk body in its 8-byte header, word-aligning with a pad
// byte when the body length is odd.
func wavChunk(id string, body []byte) []byte {
	out := append([]byte(id), wavLE32(len(body))...)
	out = append(out, body...)
	if len(body)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

// wavFile assembles a RIFF/WAVE file from chunk bytes.
func wavFile(chunks ...[]byte) []byte {
	body := []byte("WAVE")
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := append([]byte("RIFF"), wavLE32(len(body))...)
	return append(out, body...)
}

// wavFmtPCM is a 16-byte PCM "fmt " chunk: 44100 Hz, stereo, 16-bit.
func wavFmtPCM() []byte {
	b := slices.Concat(wavLE16(1), wavLE16(2), wavLE32(44100), wavLE32(176400), wavLE16(4), wavLE16(16))
	return wavChunk("fmt ", b)
}

// wavData is a data chunk of n silent bytes.
func wavData(n int) []byte { return wavChunk("data", make([]byte, n)) }

// wavInfo builds a LIST/INFO chunk from ordered 4CC/value pairs.
func wavInfo(pairs ...[2]string) []byte {
	body := []byte("INFO")
	for _, p := range pairs {
		val := append([]byte(p[1]), 0)
		body = append(body, []byte(p[0])...)
		body = append(body, wavLE32(len(val))...)
		body = append(body, val...)
		if len(val)&1 == 1 {
			body = append(body, 0)
		}
	}
	return wavChunk("LIST", body)
}

// wavID3 wraps ID3v2 tag bytes (built with id3v2/textFrame from mp3_synth_test)
// in an "id3 " chunk.
func wavID3(tagBytes []byte) []byte { return wavChunk("id3 ", tagBytes) }

func TestWAVId3TakesPrecedenceOverInfo(t *testing.T) {
	// id3 and INFO disagree on the title; the id3 value wins and the INFO value
	// is surfaced as an unselected (conflicting) RIFF family entry.
	id3 := wavID3(id3v2(3, textFrame(3, "TIT2", "ID3 Title")))
	info := wavInfo([2]string{"INAM", "INFO Title"}, [2]string{"IART", "Shared Artist"})
	data := wavFile(wavFmtPCM(), info, id3, wavData(800))

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
	// value must merge into the canonical set and survive an unrelated edit, not be
	// silently destroyed on rewrite (regression: it was dropped).
	id3 := wavID3(id3v2(3, textFrame(3, "TIT2", "T")))
	info := wavInfo([2]string{"ICOP", "ACME Records"})
	data := wavFile(wavFmtPCM(), info, id3, wavData(400))

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
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Only INFO"}, [2]string{"IPRT", "5"}), wavData(400))
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

// TestWAVITRKReadsTrackNumber checks the ITRK read alias. INFO-only files can read
// track numbers from ITRK, while newly written track numbers use IPRT so output stays
// deterministic.
func TestWAVITRKReadsTrackNumber(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Track via ITRK"}, [2]string{"ITRK", "7"}), wavData(400))
	if got := mustParseBytes(t, data).Fields().TrackNumber; got != 7 {
		t.Fatalf("ITRK track = %d, want 7", got)
	}
	// Writing a fresh track number into an INFO file with no existing track item
	// must emit the chosen IPRT identifier, not ITRK.
	base := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Has title"}), wavData(400))
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
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Old"}), wavData(400))
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
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "T"}), wavData(400))
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
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"IART", "Solo"}), wavData(400))
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
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Keep"}, [2]string{"ISFT", "Lavf"}), wavData(400))
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

func TestWAVStripInfoConsolidatesToId3WithExistingId3(t *testing.T) {
	// Source shape: an id3 chunk holds TITLE, while LIST/INFO holds keys not present
	// in id3 (ARTIST, COPYRIGHT). When --legacy strip removes INFO, those native-only
	// values must be emitted into id3. The regression seeded the ID3 diff base from
	// the merged projection, so untouched native-only keys were omitted.
	id3Chunk := wavID3(id3v2(3, textFrame(3, "TIT2", "Original Title")))
	info := wavInfo([2]string{"IART", "Native Artist"}, [2]string{"ICOP", "Native Copyright"})
	data := wavFile(wavFmtPCM(), info, id3Chunk, wavData(400))

	t.Run("unchanged native-only keys survive", func(t *testing.T) {
		// The edit touches only TITLE. Before the fix, ARTIST and COPYRIGHT were
		// compared against the merged base, treated as unchanged, and omitted from
		// the rebuilt id3 chunk.
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
		// Changing ARTIST already used the dirty-key path. Keep that coverage and
		// confirm that untouched COPYRIGHT also migrates.
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
	bext := wavChunk("bext", []byte("broadcast-extension-payload!!"))
	cue := wavChunk("cue ", []byte{1, 2, 3, 4})
	data := wavFile(wavFmtPCM(), bext, wavInfo([2]string{"INAM", "X"}), wavData(400), cue)

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
	base := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "T"}), wavData(400))
	id3v1 := make([]byte, 128)
	copy(id3v1, "TAG")
	copy(id3v1[3:], "Trailing Title")
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

// rf64File assembles an RF64 (or BW64) file: the 64-bit header, a leading ds64
// chunk carrying the real container and data sizes, then the given chunks. The
// data chunk's own size field is rewritten to the 0xFFFFFFFF marker so the file
// exercises the ds64 resolution path rather than the plain 32-bit one.
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
	body = slices.Concat(body[:4], wavChunk("ds64", ds), body[4:])
	binary.LittleEndian.PutUint64(body[12:20], uint64(len(body))) // ds64.riffSize

	out := append([]byte(magic), []byte{0xFF, 0xFF, 0xFF, 0xFF}...)
	return append(out, body...)
}

// TestWAVRF64RoundTrip checks the 64-bit RIFF extension end to end: the ds64 chunk
// resolves the marked sizes on read, and a metadata edit keeps the RF64 form with a
// regenerated ds64 rather than downgrading to plain RIFF (which would truncate the
// sizes the extension exists to carry).
func TestWAVRF64RoundTrip(t *testing.T) {
	for _, magic := range []string{"RF64", "BW64"} {
		t.Run(magic, func(t *testing.T) {
			src := rf64File(magic, 16, wavFmtPCM(), wavInfo([2]string{"INAM", "Before"}), wavData(64))
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

// TestWAVRF64FixtureRoundTrip runs the same edit against an independently authored
// RF64 file (ffmpeg -rf64 always), so the ds64 reading is checked against a real
// writer's bytes and not only against the shape this package synthesizes.
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

// TestWAVRF64WithoutDS64Rejected: the ds64 chunk is mandatory, and a file claiming
// the 64-bit form without one has no way to report its real sizes. That is corrupt
// input (exit 4), not an unsupported format.
func TestWAVRF64WithoutDS64Rejected(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavData(64))
	copy(data[0:4], "RF64")
	if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Fatalf("RF64 without ds64: err = %v, want ErrInvalidData", err)
	}
}

func TestWAVId3ChunkUppercaseVariantRead(t *testing.T) {
	// Some tools write the chunk id as "ID3 " (uppercase). It must read as a tag.
	chunk := wavChunk("ID3 ", id3v2(4, textFrame(4, "TIT2", "Upper")))
	data := wavFile(wavFmtPCM(), chunk, wavData(200))
	if got := mustParseBytes(t, data).Fields().Title; got != "Upper" {
		t.Errorf("uppercase ID3 chunk title = %q", got)
	}
}

func TestWAVDuplicateInfoChunksDropped(t *testing.T) {
	// Two LIST/INFO chunks: the first is authoritative, a warning is raised, and a
	// rewrite drops the stale duplicate so the output is single and consistent.
	data := wavFile(wavFmtPCM(),
		wavInfo([2]string{"INAM", "First"}),
		wavInfo([2]string{"INAM", "Second"}),
		wavData(400))

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
	data := wavFile(wavFmtPCM(),
		wavID3(id3v2(3, textFrame(3, "TIT2", "Primary"))),
		wavID3(id3v2(3, textFrame(3, "TIT2", "Stale"))),
		wavData(400))
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
	// A lone "id3 " chunk whose body fails to parse leaves no authoritative id3. An
	// edit forcing a new id3 chunk (a non-INFO key) must drop the stale chunk so the
	// output carries exactly one id3 chunk, not two (which a re-parse would flag as a
	// duplicate, disagreeing with the returned document).
	corrupt := wavChunk("id3 ", []byte("corrupt-not-a-valid-tag")) // fails id3.ParseTag
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "T"}), wavData(400), corrupt)
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
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "caf\xe9"}), wavData(200))
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
		b := slices.Concat(wavLE16(1), wavLE16(2), wavLE32(44100), wavLE32(byteRate), wavLE16(4), wavLE16(16))
		return wavChunk("fmt ", b)
	}
	d1 := wavFile(fmtWith(176400), wavData(400))
	d2 := wavFile(fmtWith(176400+65536), wavData(400))
	if essenceOf(t, d1).Equal(essenceOf(t, d2)) {
		t.Error("essence digest ignored the high bits of byteRate (truncation regression)")
	}
}

func TestWAVClearAllRemovesInfoChunk(t *testing.T) {
	// Clearing the only tag drops the now-empty INFO chunk rather than leaving an
	// empty husk.
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Gone"}), wavData(200))
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if got := mustParseBytes(t, out); got.Tags().Len() != 0 {
		t.Errorf("expected no tags after clear, got %d", got.Tags().Len())
	}
}

// TestWAVTruncatedDataChunkWarns covers the cross-format truncation signal: a data
// chunk that declares more bytes than the file holds is flagged, while the
// streaming "size unknown" sentinel a real piped capture carries is not.
func TestWAVTruncatedDataChunkWarns(t *testing.T) {
	t.Run("declared overruns file", func(t *testing.T) {
		// The data header declares 100000 bytes but only 200 follow.
		dataHdr := slices.Concat([]byte("data"), wavLE32(100000))
		data := wavFile(wavFmtPCM(), slices.Concat(dataHdr, make([]byte, 200)))
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
		// 0xFFFFFFFF means "size unknown" - the audio is whatever follows, not a
		// truncation. (A 0 size is the other sentinel; it reads as no-audio instead.)
		// wavLE32(-1) yields the same four 0xFF bytes on every int width, where
		// int(^uint32(0)) overflows a 32-bit int at compile time.
		dataHdr := slices.Concat([]byte("data"), wavLE32(-1))
		data := wavFile(wavFmtPCM(), slices.Concat(dataHdr, make([]byte, 400)))
		doc := mustParseBytes(t, data)
		if hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("a streaming-sentinel data size must not be flagged truncated; got %v", doc.Warnings())
		}
	})
	t.Run("intact file not flagged", func(t *testing.T) {
		data := wavFile(wavFmtPCM(), wavData(400))
		if doc := mustParseBytes(t, data); hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("an intact WAV must not be flagged truncated; got %v", doc.Warnings())
		}
	})
	t.Run("zero essence reports only no-audio", func(t *testing.T) {
		// The data header declares 100000 bytes but the file ends at the header, so
		// zero essence survives. no-audio subsumes truncated for the nothing-at-all
		// case: the file must report no-audio and not also truncated-audio.
		dataHdr := slices.Concat([]byte("data"), wavLE32(100000))
		data := wavFile(wavFmtPCM(), dataHdr)
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

// TestWAVTooSmallRiffSizeRecovers: a declared RIFF size that is in range but far too small
// used to be trusted as the walk boundary, so data and every LIST/id3 chunk past it were
// never seen: the file read as no-audio with the rest reported as trailing bytes. The walk
// must retry against the file size and adopt that result, which recovers the whole file.
func TestWAVTooSmallRiffSizeRecovers(t *testing.T) {
	full := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Recovered"}), wavData(400))
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

// TestWAVShortRiffSizeStrandedTagRecovered: a tagger that appended a LIST without updating
// the RIFF size leaves it outside the container, invisible to the read - and a rewrite then
// emits a second LIST beside the stranded one, so the file carries two.
func TestWAVShortRiffSizeStrandedTagRecovered(t *testing.T) {
	full := wavFile(wavFmtPCM(), wavData(400), wavInfo([2]string{"INAM", "Stranded"}))
	// A size covering everything but the trailing LIST, as if it predated the append.
	data := wavWithRiffSize(full, uint32(len(full)-8-len(wavInfo([2]string{"INAM", "Stranded"}))))

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

// TestWAVTruncatedNoRecovery is the other half of the rule: when the wide re-walk finds no
// audio, the narrow result stands. A genuinely tag-only file with appended junk must keep its
// honest no-audio plus trailing-bytes verdict rather than having the junk parsed as chunks.
func TestWAVTruncatedNoRecovery(t *testing.T) {
	base := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Tagged"}))
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

// TestWAVMalformedRiffSizesUnchanged guards the sizes the existing fallback already handled,
// so the recovery retry does not disturb them.
func TestWAVMalformedRiffSizesUnchanged(t *testing.T) {
	full := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Intact"}), wavData(400))
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

// TestWAVAppendedBytesStillTrailing: a correct declared size with genuine out-of-container
// bytes after it keeps reporting them as trailing. The recovery only runs when no data chunk
// was found, so this file never reaches it.
func TestWAVAppendedBytesStillTrailing(t *testing.T) {
	base := wavFile(wavFmtPCM(), wavData(400))
	data := append(slices.Clone(base), bytes.Repeat([]byte{0xCD}, 40)...)
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnTrailingBytes) {
		t.Errorf("appended out-of-container bytes must report as trailing: %v", doc.Warnings())
	}
	if hasWarning(doc, wl.WarnDistrustedBlockSize) {
		t.Errorf("a correct declared size must not be distrusted: %v", doc.Warnings())
	}
}
