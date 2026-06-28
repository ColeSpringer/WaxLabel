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

func TestWAVRF64Rejected(t *testing.T) {
	// RF64/BW64 (the >4 GiB extension) is out of scope and must fail loudly. Use a
	// .wav file path so detection routes to the WAV codec by extension and its own
	// RF64 branch runs - a bare byte source would instead fail generically at
	// detection (Sniff rejects RF64 and there is no extension to fall back to).
	data := wavFile(wavFmtPCM(), wavData(64))
	copy(data[0:4], "RF64")
	path := writeTempFile(t, "x.wav", data)
	_, err := wl.ParseFile(context.Background(), path)
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Fatalf("RF64 parse error = %v, want ErrUnsupportedFormat", err)
	}
	if !strings.Contains(err.Error(), "RF64") {
		t.Errorf("expected the codec's explicit RF64 message, got %v", err)
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
		dataHdr := slices.Concat([]byte("data"), wavLE32(int(^uint32(0))))
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
