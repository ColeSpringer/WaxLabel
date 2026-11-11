package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Synthetic AIFF / AIFF-C builders. The SSND chunk is silence; tests assert on metadata structure
// and round-trips, not on decoded audio.

// aiffSSND builds an SSND chunk: an 8-byte offset/blockSize sub-header followed by
// n silent sample-frame bytes.
func aiffSSND(n int) []byte {
	body := make([]byte, 8+n) // offset=0, blockSize=0, then samples
	return testfile.AIFFChunk("SSND", body)
}

// aiffText builds a native text chunk (NAME/AUTH/"(c) "/ANNO).
func aiffText(id, value string) []byte { return testfile.AIFFChunk(id, []byte(value)) }

// aiffID3 wraps ID3v2 tag bytes in an uppercase "ID3 " chunk.
func aiffID3(tagBytes []byte) []byte { return testfile.AIFFChunk("ID3 ", tagBytes) }

// stdCOMM is a common 18-byte COMM: stereo, 44100 Hz, 16-bit, 1000 frames.
func stdCOMM() []byte { return testfile.COMM(2, 1000, 16, 44100) }

// stdSSND holds every frame stdCOMM declares, 4 bytes each, so a file built from the
// pair is one whose two chunks agree: shorter sound data would read as COMM overstating.
func stdSSND() []byte { return aiffSSND(1000 * 4) }

// sampleDuration is the time n sample frames play for at rate Hz.
func sampleDuration(n uint64, rate int) time.Duration {
	return time.Duration(float64(n) / float64(rate) * float64(time.Second))
}

// withinDuration reports whether two durations agree to within tol.
func withinDuration(a, b, tol time.Duration) bool { d := a - b; return d <= tol && d >= -tol }

func TestAIFFSynthParseNativeText(t *testing.T) {
	data := testfile.AIFF("AIFF",
		aiffText("NAME", "Synth Title"),
		aiffText("AUTH", "Synth Author"),
		aiffText("(c) ", "ACME Records"),
		aiffText("ANNO", "first note"),
		aiffText("ANNO", "second note"),
		stdCOMM(),
		aiffSSND(1000*4))

	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatAIFF {
		t.Fatalf("format = %v, want AIFF", doc.Format())
	}
	f := doc.Fields()
	if f.Title != "Synth Title" {
		t.Errorf("title = %q", f.Title)
	}
	if len(f.Artists) != 1 || f.Artists[0] != "Synth Author" {
		t.Errorf("artists = %v", f.Artists)
	}
	if f.Copyright != "ACME Records" {
		t.Errorf("copyright = %q", f.Copyright)
	}
	// Two ANNO chunks become two Comment values.
	cs := doc.Tags()
	vals, _ := cs.Get(tag.Comment)
	if !slices.Equal(vals, []string{"first note", "second note"}) {
		t.Errorf("comments = %v, want two ANNO values", vals)
	}
	tr := doc.Properties().First()
	if tr.SampleRate != 44100 || tr.Channels != 2 || tr.BitsPerSample != 16 {
		t.Errorf("geometry = %+v", tr)
	}
	if tr.TotalSamples != 1000 {
		t.Errorf("total samples = %d, want 1000", tr.TotalSamples)
	}
	if tr.Codec != "PCM" {
		t.Errorf("codec = %q", tr.Codec)
	}
}

func TestAIFFSynthRoundTripNativeText(t *testing.T) {
	data := testfile.AIFF("AIFF", aiffText("NAME", "Old"), stdCOMM(), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New").Set(tag.Artist, "Added").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	// A native-representable edit on a native-only file must not introduce an ID3 chunk.
	if bytes.Contains(out, []byte("ID3 ")) {
		t.Error("a native-representable edit should not create an ID3 chunk")
	}
	got := mustParseBytes(t, out)
	if got.Fields().Title != "New" {
		t.Errorf("title = %q", got.Fields().Title)
	}
	if !slices.Equal(got.Fields().Artists, []string{"Added"}) {
		t.Errorf("artists = %v", got.Fields().Artists)
	}
}

func TestAIFFCapabilitiesAndNative(t *testing.T) {
	data := testfile.AIFF("AIFF", aiffText("NAME", "T"), stdCOMM(), stdSSND(),
		aiffID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T"))))
	doc := mustParseBytes(t, data)

	caps := doc.Capabilities()
	if caps.Format != wl.FormatAIFF || caps.ReadOnly {
		t.Errorf("AIFF caps = %+v, want writable AIFF", caps)
	}
	if caps.Field(tag.Title).Write != wl.AccessFull {
		t.Error("AIFF should fully support writing Title (via ID3)")
	}
	if caps.Pictures.Write != wl.AccessFull {
		t.Error("AIFF should fully support writing pictures (via ID3)")
	}

	// The native view describes the chunk structure (COMM, SSND, the text chunk, and the ID3 chunk).
	entries := doc.Native().Describe()
	if len(entries) == 0 {
		t.Fatal("Native().Describe() should describe chunks")
	}
	kinds := map[string]bool{}
	for _, e := range entries {
		kinds[e.Kind] = true
	}
	for _, want := range []string{"COMM", "SSND", "NAME", "ID3 chunk"} {
		if !kinds[want] {
			t.Errorf("Describe() missing a %q entry; got %v", want, kinds)
		}
	}
	if doc.Native().Format() != wl.FormatAIFF {
		t.Errorf("Native().Format() = %v", doc.Native().Format())
	}
}

func TestAIFFCCodecNames(t *testing.T) {
	// The compression type reads as the same codec and profile it does from a .mov, the
	// fourcc kept as the profile whatever its case.
	for _, tc := range []struct {
		comp        string
		want        string
		wantProfile string
	}{
		{"NONE", "PCM", "NONE"},
		{"twos", "PCM", "twos"},
		{"sowt", "PCM", "sowt"},
		{"raw ", "PCM", "raw "},
		{"in24", "PCM", "in24"},
		{"fl32", "IEEE float", "fl32"},
		{"FL32", "IEEE float", "FL32"},
		{"fl64", "IEEE float64", "fl64"},
		{"ulaw", "mu-law", "ulaw"},
		{"alaw", "A-law", "alaw"},
		{"ima4", "IMA ADPCM", "ima4"},
		{"IMA4", "IMA ADPCM", "IMA4"},
		{".mp3", "MP3", ".mp3"},
		// A COMM too short to carry a type is the AIFF-C default, PCM, with no spelling to report.
		{"\x00\x00\x00\x00", "PCM", ""},
		// QTFF's Windows-codec spelling: "ms" then the WAVE format tag, named through the
		// table WAV and WMA read that tag with.
		{"ms\x00\x11", "IMA ADPCM", ""},
		{"ms\x00\x55", "MP3", ""},
		{"MAC3", "MAC3", ""},             // the fourcc is the name
		{"mac3", "MAC3", "mac3"},         // folded to Apple's spelling, as ffmpeg reads it, the raw one kept
		{"XYZ!", "XYZ!", ""},             // unknown printable type passes through bare
		{"\x01\x02\x03\x04", "????", ""}, // non-printable bytes are sanitized
	} {
		data := testfile.AIFF("AIFC", testfile.COMMC(2, 100, 16, 44100, tc.comp), aiffSSND(64))
		tr := mustParseBytes(t, data).Properties().First()
		if tr.Codec != tc.want || tr.CodecProfile != tc.wantProfile {
			t.Errorf("compType %q -> codec %q / profile %q, want %q / %q",
				tc.comp, tr.Codec, tr.CodecProfile, tc.want, tc.wantProfile)
		}
	}
}

func TestAIFFMalformedSampleRateNoNaN(t *testing.T) {
	// Huge exponent + zero mantissa is 0; naive decode yields NaN. mant==0 must report rate 0, no panic.
	rate := []byte{0x7F, 0xFE, 0, 0, 0, 0, 0, 0, 0, 0} // exp=0x7FFE, mantissa=0
	commBody := slices.Concat(testfile.BE16(2), testfile.BE32(100), testfile.BE16(16), rate)
	data := testfile.AIFF("AIFF", aiffText("NAME", "T"), testfile.AIFFChunk("COMM", commBody), aiffSSND(64))

	doc := mustParseBytes(t, data)
	if got := doc.Properties().First().SampleRate; got != 0 {
		t.Errorf("malformed 80-bit rate decoded to %d, want 0", got)
	}
	// The file still round-trips (no panic, edit applies).
	plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if got := mustParseBytes(t, applyToBytes(t, data, plan)).Fields().Title; got != "Edited" {
		t.Errorf("title after edit = %q", got)
	}
}

func TestAIFFHostileCOMMBitrateNotNegative(t *testing.T) {
	// Hostile COMM geometry overflows int64 bitrate; cap must keep Bitrate non-negative (MaxInt32).
	commBody := slices.Concat(testfile.BE16(0xFFFF), testfile.BE32(100), testfile.BE16(0xFFFF), testfile.Rate80(3_000_000_000))
	data := testfile.AIFF("AIFF", testfile.AIFFChunk("COMM", commBody), aiffSSND(64))
	tr := mustParseBytes(t, data).Properties().First()
	if tr.Bitrate != math.MaxInt32 {
		t.Errorf("hostile COMM bitrate = %d, want saturation at %d", tr.Bitrate, math.MaxInt32)
	}
	// Packetized path: clamp must run after frames-per-packet division, not before (else MaxInt32/64).
	ima4 := testfile.AIFF("AIFC", testfile.COMMC(0xFFFF, 100, 4, 3_000_000_000, "ima4"), aiffSSND(34*0xFFFF))
	if got := mustParseBytes(t, ima4).Properties().First().Bitrate; got != math.MaxInt32 {
		t.Errorf("hostile ima4 COMM bitrate = %d, want saturation at %d", got, math.MaxInt32)
	}
	if tr.SampleRate < 0 || tr.Channels < 0 || tr.BitsPerSample < 0 {
		t.Errorf("hostile COMM produced negative geometry: %+v", tr)
	}
}

func TestAIFFCorruptId3NotDuplicatedOnForcedRewrite(t *testing.T) {
	// A lone "ID3 " chunk whose body is not a valid ID3 tag leaves no authoritative ID3. An edit that
	// forces a new ID3 chunk (adding a picture) must drop the stale chunk, or a re-parse flags a
	// duplicate.
	corrupt := aiffID3([]byte("corrupt-not-a-valid-tag")) // fails id3.ParseTag
	data := testfile.AIFF("AIFF", aiffText("NAME", "T"), stdCOMM(), stdSSND(), corrupt)
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "T" { // native NAME is authoritative; the corrupt ID3 gave nothing
		t.Fatalf("title = %q", doc.Fields().Title)
	}
	plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if n := bytes.Count(out, []byte("ID3 ")); n != 1 {
		t.Errorf("expected exactly one ID3 chunk in output, found %d", n)
	}
	re := mustParseBytes(t, out)
	if len(re.Pictures()) != 1 {
		t.Errorf("picture not written: %d pictures", len(re.Pictures()))
	}
	if re.Fields().Title != "T" {
		t.Errorf("native title lost: %q", re.Fields().Title)
	}
	if hasWarning(re, wl.WarnDuplicateTagBlock) {
		t.Error("re-parse of the output should not see a duplicate ID3 block")
	}
}

func TestAIFFSynthAIFCFloatCodec(t *testing.T) {
	// A synthetic AIFF-C with an "fl32" compression type: the form type is AIFC, the
	// 24-byte COMM decodes the compression type to a codec name, and an edit
	// preserves the AIFC form type and the audio essence.
	data := testfile.AIFF("AIFC",
		testfile.AIFFChunk("FVER", []byte{0xA2, 0x80, 0x51, 0x40}),
		aiffText("NAME", "Float"),
		testfile.COMMC(2, 500, 32, 48000, "fl32"),
		aiffSSND(800))

	doc := mustParseBytes(t, data)
	if doc.Properties().Container != "AIFC" {
		t.Errorf("container = %q, want AIFC", doc.Properties().Container)
	}
	tr := doc.Properties().First()
	if tr.Codec != "IEEE float" {
		t.Errorf("codec = %q, want IEEE float for fl32", tr.Codec)
	}
	if tr.SampleRate != 48000 || tr.BitsPerSample != 32 {
		t.Errorf("geometry = %+v, want 48000 Hz / 32-bit", tr)
	}
	before := essenceOf(t, data)
	plan, err := doc.Edit().Set(tag.Title, "Edited Float").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if string(out[8:12]) != "AIFC" {
		t.Errorf("form type after edit = %q, want AIFC", out[8:12])
	}
	if after := essenceOf(t, out); !before.Equal(after) {
		t.Error("essence changed across an AIFF-C tag edit")
	}
	if mustParseBytes(t, out).Fields().Title != "Edited Float" {
		t.Error("AIFF-C edit did not apply")
	}
}

func TestAIFFId3TakesPrecedenceOverNative(t *testing.T) {
	// ID3 and a NAME chunk disagree on the title; the ID3 value wins and the native
	// value is surfaced as an unselected (conflicting) AIFF family entry.
	data := testfile.AIFF("AIFF",
		aiffText("NAME", "Native Title"),
		aiffText("AUTH", "Shared Artist"),
		stdCOMM(),
		stdSSND(),
		aiffID3(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "ID3 Title"), testfile.TextFrame(4, "TPE1", "Shared Artist"))))

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "ID3 Title" {
		t.Errorf("id3 should win: title = %q", doc.Fields().Title)
	}
	conflict := false
	for _, f := range doc.Families() {
		if f.Family == wl.FamilyAIFF && f.Key == tag.Title && !f.Selected {
			conflict = true
		}
	}
	if !conflict {
		t.Errorf("expected an unselected AIFF Title family entry; families = %+v", doc.Families())
	}
	// A shared, agreeing value is not a conflict.
	for _, f := range doc.Families() {
		if f.Family == wl.FamilyAIFF && f.Key == tag.Artist && !f.Selected {
			t.Errorf("agreeing artist should not be flagged: %+v", f)
		}
	}
}

func TestAIFFId3PlusNativeDisjointKeysPreserved(t *testing.T) {
	// ID3 carries Title; a "(c) " chunk carries a Copyright ID3 lacks. The native-only value must merge
	// into the canonical set and survive an unrelated edit.
	data := testfile.AIFF("AIFF",
		aiffText("(c) ", "ACME Records"),
		stdCOMM(),
		stdSSND(),
		aiffID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T"))))

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "T" {
		t.Errorf("title = %q", doc.Fields().Title)
	}
	if doc.Fields().Copyright != "ACME Records" {
		t.Errorf("native-only Copyright not merged into canonical set: %q", doc.Fields().Copyright)
	}

	plan, err := doc.Edit().Set(tag.Artist, "New Artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if got := re.Fields().Copyright; got != "ACME Records" {
		t.Errorf("native-only Copyright lost on rewrite: %q", got)
	}
	if re.Fields().Title != "T" {
		t.Errorf("title lost on rewrite: %q", re.Fields().Title)
	}
	if !slices.Equal(re.Fields().Artists, []string{"New Artist"}) {
		t.Errorf("artist = %v", re.Fields().Artists)
	}
}

func TestAIFFNativeAuthoritativeWhenNoId3(t *testing.T) {
	data := testfile.AIFF("AIFF",
		aiffText("NAME", "Only Native"),
		aiffText("AUTH", "Native Artist"),
		stdCOMM(), stdSSND())
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "Only Native" {
		t.Errorf("native title = %q", doc.Fields().Title)
	}
	if doc.Fields().Artists[0] != "Native Artist" {
		t.Errorf("native artist = %v", doc.Fields().Artists)
	}
	for _, f := range doc.Families() {
		if f.Family == wl.FamilyAIFF && !f.Selected {
			t.Errorf("authoritative native entry should be selected: %+v", f)
		}
	}
}

func TestAIFFNonNativeKeyPromotesToId3(t *testing.T) {
	// Composer has no native chunk, so it forces an ID3 chunk; the native chunks
	// are kept and stay in sync for the representable keys.
	data := testfile.AIFF("AIFF", aiffText("NAME", "T"), stdCOMM(), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Composer, "Stravinsky").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("ID3 ")) {
		t.Error("a non-native key should create an ID3 chunk")
	}
	got := mustParseBytes(t, out)
	if !slices.Equal(got.Fields().Composers, []string{"Stravinsky"}) {
		t.Errorf("composer = %v", got.Fields().Composers)
	}
	if got.Fields().Title != "T" {
		t.Errorf("promoted title = %q, want T", got.Fields().Title)
	}
}

func TestAIFFMultiValueArtistForcesId3(t *testing.T) {
	// A multi-value artist cannot be stored in the single-valued AUTH chunk, so it
	// forces the ID3 chunk and round-trips fully there.
	data := testfile.AIFF("AIFF", aiffText("AUTH", "Solo"), stdCOMM(), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "A", "B").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("ID3 ")) {
		t.Error("a multi-value artist should create an ID3 chunk")
	}
	got := mustParseBytes(t, out)
	if !slices.Equal(got.Fields().Artists, []string{"A", "B"}) {
		t.Errorf("multi-value artist = %v", got.Fields().Artists)
	}
}

func TestAIFFMultiCommentStaysNative(t *testing.T) {
	// Comment is the one multi-valued native slot (repeated ANNO), so two comments
	// do not force an ID3 chunk and round-trip as two ANNO chunks.
	data := testfile.AIFF("AIFF", stdCOMM(), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, "one", "two").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("ID3 ")) {
		t.Error("multi-value Comment should stay in native ANNO chunks, not force ID3")
	}
	if n := bytes.Count(out, []byte("ANNO")); n != 2 {
		t.Errorf("expected two ANNO chunks, found %d", n)
	}
	got := mustParseBytes(t, out)
	vals, _ := got.Tags().Get(tag.Comment)
	if !slices.Equal(vals, []string{"one", "two"}) {
		t.Errorf("comments = %v", vals)
	}
}

// repeated ANNO comments are treated as a valid multi-valued COMMENT field, not as a lint or dump
// conflict.
func TestAIFFMultiCommentNotFlaggedConflict(t *testing.T) {
	data := testfile.AIFF("AIFF", stdCOMM(), stdSSND())
	out := applyToBytes(t, data, mustPlan(t, mustParseBytes(t, data).Edit().Set(tag.Comment, "one", "two")))
	doc := mustParseBytes(t, out)
	if vals, _ := doc.Tags().Get(tag.Comment); !slices.Equal(vals, []string{"one", "two"}) {
		t.Fatalf("comments = %v, want [one two]", vals)
	}
	for _, f := range doc.Lint() {
		if f.Code == "single-valued-multi" && f.Key == tag.Comment {
			t.Errorf("multi-value COMMENT must not be flagged single-valued-multi; got %v", f)
		}
	}
	// Dump's "(conflict)" marker gates on the same predicate.
	if tag.Comment.SingleValuedMulti(2) {
		t.Error("Comment.SingleValuedMulti(2) = true; dump would still mark (conflict)")
	}
}

func TestAIFFStripNativeConsolidatesToId3(t *testing.T) {
	data := testfile.AIFF("AIFF", aiffText("NAME", "Keep"), aiffText("AUTH", "A"), stdCOMM(), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Keep").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("stripping present native chunks is not a no-op")
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("NAME")) || bytes.Contains(out, []byte("AUTH")) {
		t.Error("native text chunks should have been stripped")
	}
	if !bytes.Contains(out, []byte("ID3 ")) {
		t.Error("tags should have been consolidated into an ID3 chunk")
	}
	got := mustParseBytes(t, out)
	if got.Fields().Title != "Keep" || got.Fields().Artists[0] != "A" {
		t.Errorf("title/artist after strip = %q / %v", got.Fields().Title, got.Fields().Artists)
	}
}

// Under a strip policy a tagless AIFF takes its tags in an ID3 chunk alone: a native text chunk
// written now is one the next strip would remove.
func TestAIFFStripOnATaglessFileWritesId3Only(t *testing.T) {
	data := testfile.AIFF("AIFF", stdCOMM(), stdSSND())
	for name, opt := range map[string]wl.WriteOption{"strip": wl.WithLegacyPolicy(wl.LegacyStrip), "minimal": wl.Minimal} {
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "T").Prepare(opt)
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if bytes.Contains(out, []byte("NAME")) || !bytes.Contains(out, []byte("ID3 ")) {
			t.Errorf("%s: want an ID3 chunk and no NAME; operations %v", name, plan.Report().Operations)
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

func TestAIFFStripNativeConsolidatesToId3WithExistingId3(t *testing.T) {
	// An ID3 chunk holds TITLE; native text chunks hold keys ID3 lacks (AUTH -> Artist, "(c) " ->
	// Copyright). When --legacy strip removes the native chunks, those values must move into ID3.
	id3Chunk := aiffID3(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Original Title")))
	data := testfile.AIFF("AIFF", aiffText("AUTH", "Native Artist"), aiffText("(c) ", "Native Copyright"),
		id3Chunk, stdCOMM(), stdSSND())

	t.Run("unchanged native-only keys survive", func(t *testing.T) {
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New Title").
			Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if bytes.Contains(out, []byte("AUTH")) || bytes.Contains(out, []byte("(c) ")) {
			t.Error("native text chunks should have been stripped")
		}
		if !bytes.Contains(out, []byte("ID3 ")) {
			t.Error("tags should have been consolidated into an ID3 chunk")
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

func TestAIFFPreservesUnknownChunks(t *testing.T) {
	// An "FVER" chunk and a "MARK" chunk (neither modeled) must survive an edit
	// byte-for-byte and keep their order relative to SSND.
	fver := testfile.AIFFChunk("FVER", []byte{0xA2, 0x80, 0x51, 0x40})
	mark := testfile.AIFFChunk("MARK", []byte{0, 1, 2, 3})
	data := testfile.AIFF("AIFF", fver, aiffText("NAME", "X"), stdCOMM(), stdSSND(), mark)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Y").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, fver) {
		t.Error("FVER chunk was not preserved verbatim")
	}
	if !bytes.Contains(out, mark) {
		t.Error("trailing MARK chunk was not preserved verbatim")
	}
	if mustParseBytes(t, out).Fields().Title != "Y" {
		t.Error("edit did not apply")
	}
}

func TestAIFFAppendedDataKeptOutsideFormSize(t *testing.T) {
	// A 128-byte ID3v1-style tag appended after the FORM chunk (excluded from the declared FORM size)
	// must be preserved verbatim AND kept outside the recomputed FORM size on rewrite.
	base := testfile.AIFF("AIFF", aiffText("NAME", "T"), stdCOMM(), stdSSND())
	trailer := testfile.ID3v1("Trailing Title", "", "", "", "", 0)
	data := append(slices.Clone(base), trailer...)

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "T" {
		t.Errorf("title = %q (appended tag should not disturb the read)", doc.Fields().Title)
	}
	plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.HasSuffix(out, trailer) {
		t.Error("appended out-of-FORM tag was not preserved verbatim")
	}
	formSize := int(binary.BigEndian.Uint32(out[4:8]))
	if 8+formSize != len(out)-len(trailer) {
		t.Errorf("FORM size %d should exclude the %d-byte appended tag: 8+size=%d, want %d",
			formSize, len(trailer), 8+formSize, len(out)-len(trailer))
	}
	if mustParseBytes(t, out).Fields().Title != "Edited" {
		t.Error("edit did not apply")
	}
}

func TestAIFFId3ChunkLowercaseVariantRead(t *testing.T) {
	// Some tools write the chunk id as lowercase "id3 ". It must read as a tag.
	data := testfile.AIFF("AIFF", stdCOMM(), stdSSND(),
		testfile.AIFFChunk("id3 ", testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Lower"))))
	if got := mustParseBytes(t, data).Fields().Title; got != "Lower" {
		t.Errorf("lowercase id3 chunk title = %q", got)
	}
}

func TestAIFFDuplicateId3ChunksDropped(t *testing.T) {
	data := testfile.AIFF("AIFF", stdCOMM(), stdSSND(),
		aiffID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Primary"))),
		aiffID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Stale"))))
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
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Edited" {
		t.Error("edit did not apply")
	}
	if hasWarning(re, wl.WarnDuplicateTagBlock) {
		t.Error("the duplicate should be gone after a rewrite")
	}
}

func TestAIFFLatin1NativeValueDecodes(t *testing.T) {
	// A legacy Latin-1 native value (0xE9 == 'é') must decode to valid UTF-8 in the
	// canonical model.
	data := testfile.AIFF("AIFF", aiffText("NAME", "caf\xe9"), stdCOMM(), stdSSND())
	title := mustParseBytes(t, data).Fields().Title
	if title != "café" {
		t.Errorf("Latin-1 native value decoded to %q, want %q", title, "café")
	}
	if !utf8.ValidString(title) {
		t.Error("decoded title is not valid UTF-8")
	}
}

func TestAIFFClearAllRemovesNativeChunks(t *testing.T) {
	// Clearing the only tag drops the now-empty native chunk.
	data := testfile.AIFF("AIFF", aiffText("NAME", "Gone"), stdCOMM(), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("NAME")) {
		t.Error("empty NAME chunk should have been dropped")
	}
	if got := mustParseBytes(t, out); got.Tags().Len() != 0 {
		t.Errorf("expected no tags after clear, got %d", got.Tags().Len())
	}
}

func TestAIFFNumericGenreFromId3(t *testing.T) {
	data := testfile.AIFF("AIFF", stdCOMM(), stdSSND(),
		aiffID3(testfile.ID3v2(3, testfile.TextFrame(3, "TCON", "(17)"), testfile.TextFrame(3, "TIT2", "T"))))
	doc := mustParseBytes(t, data)
	if g := doc.Fields().Genres; len(g) != 1 || g[0] != "Rock" {
		t.Errorf("numeric genre (17) -> %v, want [Rock]", g)
	}
	if !hasWarning(doc, wl.WarnNumericGenre) {
		t.Errorf("expected a numeric-genre warning; got %v", doc.Warnings())
	}
}

func TestAIFFBareFileEditsGoNative(t *testing.T) {
	// A bare AIFF (no tag containers) receiving a native-representable edit writes
	// native text chunks, matching ffmpeg's own default, with no ID3 chunk.
	data := testfile.AIFF("AIFF", stdCOMM(), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Fresh").Set(tag.Comment, "note").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("ID3 ")) {
		t.Error("a bare-file native-representable edit should not create an ID3 chunk")
	}
	if !bytes.Contains(out, []byte("NAME")) || !bytes.Contains(out, []byte("ANNO")) {
		t.Error("expected created NAME and ANNO chunks")
	}
	got := mustParseBytes(t, out)
	if c := got.Fields().Comment; got.Fields().Title != "Fresh" || !slices.Equal(c, []string{"note"}) {
		t.Errorf("bare edit: title=%q comment=%v", got.Fields().Title, c)
	}
}

// truncation signal for AIFF: an SSND chunk declaring more bytes than the file holds is flagged; an
// intact file is not.
func TestAIFFTruncatedSSNDWarns(t *testing.T) {
	t.Run("declared overruns file", func(t *testing.T) {
		// The SSND header declares 100000 bytes but only the 8-byte sub-header plus a little audio follow.
		ssndHdr := slices.Concat([]byte("SSND"), testfile.BE32(100000))
		data := testfile.AIFF("AIFF", stdCOMM(), slices.Concat(ssndHdr, make([]byte, 8+200)))
		doc := mustParseBytes(t, data)
		if !hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("expected truncated-audio warning; got %v", doc.Warnings())
		}
	})
	t.Run("intact file not flagged", func(t *testing.T) {
		data := testfile.AIFF("AIFF", stdCOMM(), stdSSND())
		if doc := mustParseBytes(t, data); hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("an intact AIFF must not be flagged truncated; got %v", doc.Warnings())
		}
	})
}

// aiffWithFormSize rebuilds an AIFF with an arbitrary declared FORM size, leaving every
// chunk byte untouched.
func aiffWithFormSize(data []byte, declared uint32) []byte {
	out := slices.Clone(data)
	binary.BigEndian.PutUint32(out[4:8], declared)
	return out
}

// A FORM size shorter than the file must not hide the chunks past it: a tagger that appended a
// NAME chunk without updating the header would lose those tags, and a rewrite would emit a second
// NAME beside the stranded copy. AIFF and WAV share iff.WalkChunksRecovering.
func TestAIFFShortFormSizeRecovered(t *testing.T) {
	name := aiffText("NAME", "Stranded")
	full := testfile.AIFF("AIFF", testfile.COMM(2, 100, 16, 44100), aiffSSND(400), name)
	data := aiffWithFormSize(full, uint32(len(full)-8-len(name)))

	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "Stranded" {
		t.Errorf("TITLE = %q, want Stranded (the NAME chunk past the declared size)", got)
	}
	if !hasWarning(doc, wl.WarnDistrustedBlockSize) {
		t.Errorf("distrusting the declared size must be reported: %v", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Artist, "Added").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if n := bytes.Count(out, []byte("NAME")); n != 1 {
		t.Errorf("output holds %d NAME chunks, want 1", n)
	}
	if got := mustParseBytes(t, out).Fields().Title; got != "Stranded" {
		t.Errorf("TITLE after rewrite = %q, want Stranded", got)
	}
}

func TestAIFFAppendedBytesStillTrailing(t *testing.T) {
	base := testfile.AIFF("AIFF", testfile.COMM(2, 100, 16, 44100), aiffSSND(400))
	data := append(slices.Clone(base), bytes.Repeat([]byte{0xCD}, 40)...)
	doc := mustParseBytes(t, data)
	if hasWarning(doc, wl.WarnDistrustedBlockSize) {
		t.Errorf("appended junk must not be adopted as chunks: %v", doc.Warnings())
	}
	if got := doc.Properties().Duration(); got <= 0 {
		t.Errorf("duration = %v, want the audio's", got)
	}
}

// The shared chunk walker exempts a 0xFFFFFFFF size-unknown chunk from the truncation and
// oversized signals and reports the unknown size instead, on AIFF as on WAV.
func TestAIFFSentinelSSNDSizeReported(t *testing.T) {
	ssnd := aiffSSND(400)
	copy(ssnd[4:8], []byte{0xFF, 0xFF, 0xFF, 0xFF})
	doc := mustParseBytes(t, testfile.AIFF("AIFF", testfile.COMM(2, 100, 16, 44100), ssnd))
	if !hasWarning(doc, wl.WarnUnknownChunkSize) {
		t.Errorf("the size-unknown sentinel left no signal at all; got %v", doc.Warnings())
	}
	if hasWarning(doc, wl.WarnTruncatedAudio) {
		t.Errorf("a size-unknown SSND must not be flagged truncated; got %v", doc.Warnings())
	}
	// The control: an ordinary AIFF must not gain the warning.
	clean := mustParseBytes(t, testfile.AIFF("AIFF", testfile.COMM(2, 100, 16, 44100), aiffSSND(400)))
	if hasWarning(clean, wl.WarnUnknownChunkSize) {
		t.Errorf("an ordinary AIFF must stay quiet: %v", clean.Warnings())
	}
}

func TestAIFFConflictingTextChunkSurvivesUnrelatedEdit(t *testing.T) {
	data := testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "Aiff Title"), aiffID3(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Id3 Title"))), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "Z").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("Aiff Title")) {
		t.Fatal("NAME chunk was overwritten by the ID3 value")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Id3 Title" {
		t.Errorf("title = %q", re.Fields().Title)
	}
	conflict := false
	for _, f := range re.Families() {
		if f.Family == wl.FamilyAIFF && f.Key == tag.Title && !f.Selected {
			conflict = true
		}
	}
	if !conflict {
		t.Errorf("the AIFF title conflict should still be reported: %+v", re.Families())
	}

	plan, err = re.Edit().Set(tag.Title, "Id3 Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() || !slices.Contains(plan.Report().Operations, "native text chunk conflict resolved (TITLE)") {
		t.Errorf("explicit set should resolve the conflict: noop=%v ops=%v", plan.IsNoOp(), plan.Report().Operations)
	}
	if bytes.Contains(applyToBytes(t, out, plan), []byte("Aiff Title")) {
		t.Error("the conflicting NAME value should have been replaced")
	}
}

func TestAIFFDuplicateNameChunksSurviveUnrelatedEdit(t *testing.T) {
	data := testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "one"), aiffText("NAME", "two"), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Count(out, []byte("NAME")) != 2 || bytes.Contains(out, []byte("ID3 ")) {
		t.Errorf("NAME count = %d, ID3 present = %v", bytes.Count(out, []byte("NAME")), bytes.Contains(out, []byte("ID3 ")))
	}
}

// untouched-chunk rule: a file whose NAME chunk disagrees with its ID3 chunk is in its steady
// state, so an edit that names nothing must not rewrite it. Without the gate the writer would
// restamp the ID3 value over NAME on every pass.
func TestAIFFEmptyEditOnConflictingChunksIsNoOp(t *testing.T) {
	data := testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "Aiff Title"),
		aiffID3(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Id3 Title"))), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("an empty edit on a conflicting file must be a no-op; operations: %v", plan.Report().Operations)
	}
}

// --legacy strip is the one AIFF path that destroys a native value: a value the projection did not
// select has no canonical key to carry it into the ID3 chunk. doc.go's contract says such a loss
// must be reported.
func TestAIFFLegacyStripWarnsAboutDestroyedTextValues(t *testing.T) {
	data := testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "Aiff Title"),
		aiffID3(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Id3 Title"))), stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "Z").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(applyToBytes(t, data, plan), []byte("Aiff Title")) {
		t.Fatal("the strip was expected to destroy the unselected NAME value")
	}
	w, ok := warningFor(plan, wl.WarnLegacyStripDropped)
	if !ok {
		t.Fatalf("the strip destroyed a value and reported nothing: %v", plan.Report().Warnings)
	}
	if !strings.Contains(w.Message, "TITLE") || !slices.Equal(w.Keys, []tag.Key{tag.Title}) {
		t.Errorf("the warning should name the key that was lost, got %q keyed %v", w.Message, w.Keys)
	}

	// The control: an edit that writes the value the chunk held loses nothing, because the
	// ID3 chunk the strip consolidates into now carries it.
	rescue, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Aiff Title").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(rescue, wl.WarnLegacyStripDropped); ok {
		t.Errorf("an edit that writes the native value must not report it lost: %v", rescue.Report().Warnings)
	}
}

// A native text chunk an edit does not touch is copied where it stands, whole: one behind the
// sound chunk stays there, and the NUL a value ends with stays with it.
func TestAIFFUntouchedTextChunksStayInPlace(t *testing.T) {
	name, anno := aiffText("NAME", "T"), testfile.AIFFChunk("ANNO", []byte("hello world\x00"))
	data := testfile.AIFF("AIFF", stdCOMM(), name, stdSSND(), anno)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "Z").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	ssnd := bytes.Index(out, []byte("SSND"))
	if i := bytes.Index(out, name); i < 0 || i > ssnd {
		t.Errorf("NAME at %d, want it whole ahead of SSND at %d", i, ssnd)
	}
	if i := bytes.Index(out, anno); i < ssnd {
		t.Errorf("ANNO at %d, want all 12 bytes of it behind SSND at %d", i, ssnd)
	}
	if ops := plan.Report().Operations; slices.Contains(ops, "native text chunk rewrite") {
		t.Errorf("operations %v, want no native rewrite", ops)
	}
	if got := mustParseBytes(t, out).Fields(); got.Title != "T" || got.Album != "Z" {
		t.Errorf("title %q album %q after the edit", got.Title, got.Album)
	}
}

// Bytes behind the NUL that ends a text chunk's value are reported, survive an edit of another
// key, and die, warned, with an edit, a clear or a strip of the chunk that holds them.
func TestAIFFBytesPastTheTerminator(t *testing.T) {
	name := testfile.AIFFChunk("NAME", []byte("Hello\x00World!"))
	data := testfile.AIFF("AIFF", stdCOMM(), name, stdSSND())
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "Hello" || !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("title %q, warnings %v; want Hello and the hidden bytes reported", doc.Fields().Title, doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Composer, "Stravinsky").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if planWarns(t, plan, wl.WarnMalformedTagEntryDropped) || !bytes.Contains(applyToBytes(t, data, plan), name) {
		t.Errorf("an edit of another key must keep the chunk whole and silent: %v", plan.Report().Warnings)
	}
	for what, edit := range map[string]func(*wl.Editor) *wl.Editor{
		"an edit":  func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "New") },
		"a clear":  func(e *wl.Editor) *wl.Editor { return e.Clear(tag.Title) },
		"a resave": func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "Hello") },
	} {
		plan, err := edit(doc.Edit()).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if want := what != "a resave"; planWarns(t, plan, wl.WarnMalformedTagEntryDropped) != want {
			t.Errorf("%s of the chunk: dropped warning present = %v, want %v", what, !want, want)
		}
	}
	strip, err := doc.Edit().Set(tag.Composer, "Stravinsky").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, strip, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("a strip destroys the hidden bytes and must say so: %v", strip.Report().Warnings)
	}
	// Zeros behind the NUL are padding a rewrite loses nothing by dropping.
	padded := mustParseBytes(t, testfile.AIFF("AIFF", stdCOMM(), testfile.AIFFChunk("NAME", []byte("Hello\x00\x00\x00")), stdSSND()))
	if hasWarning(padded, wl.WarnMalformedTagEntry) {
		t.Errorf("NUL padding reported: %v", padded.Warnings())
	}
}

// A comment edit that leaves the ANNO chunks' values as they are changes nothing: each chunk stays
// where it stands with the bytes behind its NUL, and saving the same comments again is a no-op.
func TestAIFFCommentsSetToWhatTheChunksHoldIsANoOp(t *testing.T) {
	x, y := testfile.AIFFChunk("ANNO", []byte("x\x00junk")), aiffText("ANNO", "y")
	data := testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "T"), x, stdSSND(), y)
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.Comment, "x", "y").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() || planWarns(t, plan, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("operations %v, warnings %v; want a no-op that loses nothing", plan.Report().Operations, plan.Report().Warnings)
	}

	// One value changed: its chunk alone is re-rendered, where it stands.
	plan, err = doc.Edit().Set(tag.Comment, "x", "z").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if i := bytes.Index(out, aiffText("ANNO", "z")); !bytes.Contains(out, x) || i < bytes.Index(out, []byte("SSND")) {
		t.Errorf("want the first ANNO whole and the changed one still behind SSND (found at %d)", i)
	}
	re := mustParseBytes(t, out)
	if again, err := re.Edit().Set(tag.Comment, "x", "z").Prepare(); err != nil || !again.IsNoOp() {
		t.Errorf("saving the same comments again is not a no-op (err %v)", err)
	}

	// A third value follows the chunks the file holds; a dropped one takes its chunk with it.
	grown, err := doc.Edit().Set(tag.Comment, "x", "y", "w").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, data, grown); !bytes.Contains(out, x) || !bytes.Contains(out, slices.Concat(y, aiffText("ANNO", "w"))) {
		t.Error("want both ANNO chunks whole and the new one right behind the second")
	}
	shrunk, err := doc.Edit().Set(tag.Comment, "x").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, data, shrunk); !bytes.Contains(out, x) || bytes.Contains(out, y) {
		t.Error("want the first ANNO whole and the second gone")
	}
}

// A text chunk an edit creates goes ahead of the sound, behind the last text chunk there, so a
// reader that stops at SSND finds it; chunks behind the sound stay where they are.
func TestAIFFCreatedTextChunkGoesAheadOfTheSound(t *testing.T) {
	name, anno, auth := aiffText("NAME", "T"), aiffText("ANNO", "c"), aiffText("AUTH", "Someone")
	for what, c := range map[string]struct{ data, want []byte }{
		"a text chunk on each side": {testfile.AIFF("AIFF", stdCOMM(), name, stdSSND(), anno), slices.Concat(name, auth, stdSSND(), anno)},
		"text chunks behind only":   {testfile.AIFF("AIFF", stdCOMM(), stdSSND(), anno), slices.Concat(auth, stdSSND(), anno)},
	} {
		plan, err := mustParseBytes(t, c.data).Edit().Set(tag.Artist, "Someone").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if out := applyToBytes(t, c.data, plan); !bytes.Contains(out, c.want) {
			t.Errorf("%s: AUTH is not ahead of SSND behind the text chunks there; operations %v", what, plan.Report().Operations)
		}
	}
}

// Setting a key to the value the ID3 chunk already gives it is how a caller asks for the native
// chunk too: the chunk is created and the ID3 chunk, whose frames do not change, is copied whole.
func TestAIFFSameValueSetCreatesTheNativeChunk(t *testing.T) {
	id3 := aiffID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T"), testfile.TextFrame(3, "TPE1", "A"), make([]byte, 64)))
	data := testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "T"), id3, stdSSND())
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, aiffText("AUTH", "A")) {
		t.Error("an explicit set of the projected value did not create AUTH")
	}
	if !bytes.Contains(out, id3) {
		t.Error("the ID3 chunk, whose frames did not change, was not copied whole with its padding")
	}
	if ops := plan.Report().Operations; !slices.Equal(ops, []string{"native text chunk rewrite"}) {
		t.Errorf("operations %v, want the native rewrite alone", ops)
	}
}

// A rewritten ID3 chunk keeps its identifier, and its size while the tag had padding the new frames
// fit in; a tag without padding gains none. No padding option changes that.
func TestAIFFRewrittenId3ChunkKeepsItsIdAndPadding(t *testing.T) {
	padded := testfile.AIFFChunk("id3 ", testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T"), make([]byte, 64)))
	bare := testfile.AIFFChunk("id3 ", testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Title")))
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
		data := testfile.AIFF("AIFF", stdCOMM(), c.chunk, stdSSND())
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, c.title).Prepare(c.opts...)
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		_, chunk, found := bytes.Cut(out, []byte("id3 "))
		if !found || bytes.Contains(out, []byte("ID3 ")) {
			t.Fatalf("%s: the chunk did not keep its identifier", c.name)
		}
		if got := int(binary.BigEndian.Uint32(chunk)); got != c.want {
			t.Errorf("%s: chunk body is %d bytes, want %d", c.name, got, c.want)
		}
		if got := mustParseBytes(t, out).Fields().Title; got != c.title {
			t.Errorf("%s: title %q", c.name, got)
		}
	}
}

// An AIFF ID3 chunk a rewrite drops is graded as a WAV one is: a frame the canonical view does not
// carry, or a chunk that does not read, is named, so --strict refuses.
func TestAIFFDroppedId3ChunkNamesWhatItLoses(t *testing.T) {
	front := aiffID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Primary")))
	popm := aiffID3(testfile.ID3v2(3, testfile.ID3Frame(3, "POPM", []byte("me@example.com\x00\xC8"))))
	unreadable := aiffID3(testfile.ID3v2(5, testfile.TextFrame(4, "TALB", "Future")))
	for _, c := range []struct {
		name   string
		chunks [][]byte
		want   string
	}{
		{"a duplicate holding a POPM frame", [][]byte{front, popm}, `1 "POPM" frame(s)`},
		{"an unreadable duplicate", [][]byte{front, unreadable}, "1 unreadable ID3 chunk(s)"},
		{"a duplicate with bytes behind its tag", [][]byte{front, aiffID3(slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Primary")), []byte("JUNKJUNK")))}, "8 unreadable byte(s)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := testfile.AIFF("AIFF", slices.Concat([][]byte{stdCOMM(), stdSSND()}, c.chunks)...)
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

// An AIFF ID3 chunk over the element cap refuses the parse wherever it sits, as a WAV one does.
func TestAIFFId3ChunkOverTheElementCapRefuses(t *testing.T) {
	front := aiffID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Primary")))
	var frames [][]byte
	for range 20 {
		frames = append(frames, testfile.TextFrame(3, "TALB", "Album"))
	}
	big := aiffID3(testfile.ID3v2(3, frames...))
	for name, chunks := range map[string][][]byte{"ahead of the kept chunk": {big, front}, "behind the kept chunk": {front, big}} {
		data := testfile.AIFF("AIFF", slices.Concat([][]byte{stdCOMM(), stdSSND()}, chunks)...)
		_, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxElements: 8}))
		if !errors.Is(err, waxerr.ErrSizeTooLarge) {
			t.Errorf("%s: err = %v, want ErrSizeTooLarge", name, err)
		}
	}
}

// An AIFF ID3 chunk that does not parse, alone, is reported and its replacement warned as a WAV one
// is; an empty ID3 chunk ahead of a populated one yields to it.
func TestAIFFUnreadableAndEmptyId3Chunks(t *testing.T) {
	unreadable := aiffID3(testfile.ID3v2(5, testfile.TextFrame(4, "TALB", "Future")))
	data := testfile.AIFF("AIFF", stdCOMM(), stdSSND(), unreadable)
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("warnings %v, want the unreadable ID3 chunk reported", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Composer, "Stravinsky").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnMalformedTagEntryDropped) || dupDroppedNames(plan) != "" {
		t.Errorf("plan warnings %v, want malformed-tag-entry-dropped and no duplicate drop", plan.Report().Warnings)
	}
	data = testfile.AIFF("AIFF", stdCOMM(), stdSSND(), aiffID3(testfile.ID3v2(3)),
		aiffID3(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Kept"))))
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
