package waxlabel_test

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"
	"unicode/utf8"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// Synthetic AIFF / AIFF-C builders. The SSND chunk is silence; tests assert
// on metadata structure and round-trips, not on decoded audio. All sizes are
// big-endian, per IFF.

func aiffBE16(n int) []byte { return []byte{byte(n >> 8), byte(n)} }
func aiffBE32(n int) []byte { return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)} }

// aiffChunk wraps a chunk body in its 8-byte header, word-aligning with a pad
// byte when the body length is odd.
func aiffChunk(id string, body []byte) []byte {
	out := append([]byte(id), aiffBE32(len(body))...)
	out = append(out, body...)
	if len(body)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

// aiffFile assembles a FORM/AIFF (or AIFC) file from chunk bytes.
func aiffFile(formType string, chunks ...[]byte) []byte {
	body := []byte(formType)
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := append([]byte("FORM"), aiffBE32(len(body))...)
	return append(out, body...)
}

// aiffRate80 encodes an integer sample rate as an 80-bit IEEE 754
// extended-precision float (the form AIFF's COMM chunk stores the rate in).
func aiffRate80(rate uint32) []byte {
	b := make([]byte, 10)
	if rate == 0 {
		return b
	}
	mant := uint64(rate)
	exp := 16383 + 63
	for mant&(1<<63) == 0 {
		mant <<= 1
		exp--
	}
	b[0] = byte(exp >> 8)
	b[1] = byte(exp)
	binary.BigEndian.PutUint64(b[2:], mant)
	return b
}

// aiffCOMM builds a plain-AIFF 18-byte COMM chunk.
func aiffCOMM(channels, numFrames, sampleSize int, rate uint32) []byte {
	b := slices.Concat(aiffBE16(channels), aiffBE32(numFrames), aiffBE16(sampleSize), aiffRate80(rate))
	return aiffChunk("COMM", b)
}

// aiffCOMMC builds an AIFF-C COMM chunk: the 18 common bytes, a 4-byte
// compression type, and an empty pascal-string compression name.
func aiffCOMMC(channels, numFrames, sampleSize int, rate uint32, compType string) []byte {
	b := slices.Concat(aiffBE16(channels), aiffBE32(numFrames), aiffBE16(sampleSize), aiffRate80(rate))
	b = append(b, []byte(compType)...)
	b = append(b, 0, 0) // pascal string: length 0, plus a pad byte to even it out
	return aiffChunk("COMM", b)
}

// aiffSSND builds an SSND chunk: an 8-byte offset/blockSize sub-header followed by
// n silent sample-frame bytes.
func aiffSSND(n int) []byte {
	body := make([]byte, 8+n) // offset=0, blockSize=0, then samples
	return aiffChunk("SSND", body)
}

// aiffSSNDOffset builds an SSND chunk whose "offset" sub-header field is non-zero:
// `align` block-alignment bytes precede the `samples` sample frames. A reader must
// begin the sample-frame (essence) region after those alignment bytes. A declared
// offset larger than the body models a corrupt header.
func aiffSSNDOffset(offset int, align, samples []byte) []byte {
	body := slices.Concat(aiffBE32(offset), aiffBE32(0), align, samples)
	return aiffChunk("SSND", body)
}

// aiffText builds a native text chunk (NAME/AUTH/"(c) "/ANNO).
func aiffText(id, value string) []byte { return aiffChunk(id, []byte(value)) }

// aiffID3 wraps ID3v2 tag bytes (built with id3v2/textFrame from mp3_synth_test)
// in an uppercase "ID3 " chunk.
func aiffID3(tagBytes []byte) []byte { return aiffChunk("ID3 ", tagBytes) }

// stdCOMM is a common 18-byte COMM: stereo, 44100 Hz, 16-bit, 1000 frames.
func stdCOMM() []byte { return aiffCOMM(2, 1000, 16, 44100) }

func TestAIFFSynthParseNativeText(t *testing.T) {
	data := aiffFile("AIFF",
		aiffText("NAME", "Synth Title"),
		aiffText("AUTH", "Synth Author"),
		aiffText("(c) ", "ACME Records"),
		aiffText("ANNO", "first note"),
		aiffText("ANNO", "second note"),
		stdCOMM(),
		aiffSSND(400))

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
	data := aiffFile("AIFF", aiffText("NAME", "Old"), stdCOMM(), aiffSSND(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New").Set(tag.Artist, "Added").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	// A native-representable edit on a native-only file must not introduce an ID3
	// chunk.
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
	data := aiffFile("AIFF", aiffText("NAME", "T"), stdCOMM(), aiffSSND(400),
		aiffID3(id3v2(3, textFrame(3, "TIT2", "T"))))
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

	// The native view describes the chunk structure (COMM, SSND, the text chunk,
	// and the ID3 chunk).
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
	for _, tc := range []struct {
		comp string
		want string
	}{
		{"NONE", "PCM"},
		{"twos", "PCM"},
		{"sowt", "PCM (little-endian)"},
		{"fl32", "IEEE float"},
		{"fl64", "IEEE float64"},
		{"ulaw", "mu-law"},
		{"alaw", "A-law"},
		{"ima4", "IMA ADPCM"},
		{"XYZ!", "AIFF-C XYZ!"},             // unknown printable type passes through
		{"\x01\x02\x03\x04", "AIFF-C ????"}, // non-printable bytes are sanitized
	} {
		data := aiffFile("AIFC", aiffCOMMC(2, 100, 16, 44100, tc.comp), aiffSSND(64))
		got := mustParseBytes(t, data).Properties().First().Codec
		if got != tc.want {
			t.Errorf("compType %q -> codec %q, want %q", tc.comp, got, tc.want)
		}
	}
}

func TestAIFFMalformedSampleRateNoNaN(t *testing.T) {
	// A COMM whose 80-bit rate has a huge (non-0x7FFF) exponent and a zero mantissa
	// is mathematically 0, but a naive decode computes 0 * 2^huge = 0 * +Inf = NaN.
	// A NaN slips past decodeSampleRate's range check (every NaN comparison is false)
	// and casts to a platform-dependent uint32; the mant==0 guard must yield 0
	// (unknown rate) without a panic.
	rate := []byte{0x7F, 0xFE, 0, 0, 0, 0, 0, 0, 0, 0} // exp=0x7FFE, mantissa=0
	commBody := slices.Concat(aiffBE16(2), aiffBE32(100), aiffBE16(16), rate)
	data := aiffFile("AIFF", aiffText("NAME", "T"), aiffChunk("COMM", commBody), aiffSSND(64))

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
	// A corrupt COMM pairing a ~3 GHz rate with 65535 channels and 65535-bit samples:
	// the raw rate*channels*sampleSize product overflows int64 and wraps negative. The
	// staged MaxInt32 cap must keep Bitrate (and the other geometry) non-negative, and
	// the file must still parse without panic.
	commBody := slices.Concat(aiffBE16(0xFFFF), aiffBE32(100), aiffBE16(0xFFFF), aiffRate80(3_000_000_000))
	data := aiffFile("AIFF", aiffChunk("COMM", commBody), aiffSSND(64))
	tr := mustParseBytes(t, data).Properties().First()
	if tr.Bitrate < 0 {
		t.Errorf("hostile COMM produced negative Bitrate %d", tr.Bitrate)
	}
	if tr.SampleRate < 0 || tr.Channels < 0 || tr.BitsPerSample < 0 {
		t.Errorf("hostile COMM produced negative geometry: %+v", tr)
	}
}

func TestAIFFCorruptId3NotDuplicatedOnForcedRewrite(t *testing.T) {
	// A lone "ID3 " chunk whose body is not a valid ID3 tag leaves no authoritative
	// ID3. An edit that forces a new ID3 chunk (adding a picture) must drop the stale
	// chunk so the output carries exactly one ID3 chunk - not two, which a re-parse
	// would flag as a duplicate, disagreeing with the returned document.
	corrupt := aiffID3([]byte("corrupt-not-a-valid-tag")) // fails id3.ParseTag
	data := aiffFile("AIFF", aiffText("NAME", "T"), stdCOMM(), aiffSSND(400), corrupt)
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "T" { // native NAME is authoritative; the corrupt ID3 gave nothing
		t.Fatalf("title = %q", doc.Fields().Title)
	}
	plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
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
	// preserves the AIFC form type without disturbing the audio essence.
	data := aiffFile("AIFC",
		aiffChunk("FVER", []byte{0xA2, 0x80, 0x51, 0x40}),
		aiffText("NAME", "Float"),
		aiffCOMMC(2, 500, 32, 48000, "fl32"),
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
	data := aiffFile("AIFF",
		aiffText("NAME", "Native Title"),
		aiffText("AUTH", "Shared Artist"),
		stdCOMM(),
		aiffSSND(400),
		aiffID3(id3v2(4, textFrame(4, "TIT2", "ID3 Title"), textFrame(4, "TPE1", "Shared Artist"))))

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
	// ID3 carries Title; a "(c) " chunk carries a Copyright ID3 lacks. The
	// native-only value must merge into the canonical set and survive an unrelated
	// edit, not be silently dropped on rewrite.
	data := aiffFile("AIFF",
		aiffText("(c) ", "ACME Records"),
		stdCOMM(),
		aiffSSND(400),
		aiffID3(id3v2(3, textFrame(3, "TIT2", "T"))))

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
	data := aiffFile("AIFF",
		aiffText("NAME", "Only Native"),
		aiffText("AUTH", "Native Artist"),
		stdCOMM(), aiffSSND(400))
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
	data := aiffFile("AIFF", aiffText("NAME", "T"), stdCOMM(), aiffSSND(400))
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
	data := aiffFile("AIFF", aiffText("AUTH", "Solo"), stdCOMM(), aiffSSND(400))
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
	data := aiffFile("AIFF", stdCOMM(), aiffSSND(400))
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

func TestAIFFStripNativeConsolidatesToId3(t *testing.T) {
	data := aiffFile("AIFF", aiffText("NAME", "Keep"), aiffText("AUTH", "A"), stdCOMM(), aiffSSND(400))
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

func TestAIFFPreservesUnknownChunks(t *testing.T) {
	// An "FVER" chunk and a "MARK" chunk (neither modeled) must survive an edit
	// byte-for-byte and keep their order relative to SSND.
	fver := aiffChunk("FVER", []byte{0xA2, 0x80, 0x51, 0x40})
	mark := aiffChunk("MARK", []byte{0, 1, 2, 3})
	data := aiffFile("AIFF", fver, aiffText("NAME", "X"), stdCOMM(), aiffSSND(400), mark)

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
	// A 128-byte ID3v1-style tag appended after the FORM chunk (excluded from the
	// declared FORM size) must be preserved verbatim AND kept outside the recomputed
	// FORM size on rewrite.
	base := aiffFile("AIFF", aiffText("NAME", "T"), stdCOMM(), aiffSSND(400))
	trailer := make([]byte, 128)
	copy(trailer, "TAG")
	copy(trailer[3:], "Trailing Title")
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
	data := aiffFile("AIFF", stdCOMM(), aiffSSND(200),
		aiffChunk("id3 ", id3v2(4, textFrame(4, "TIT2", "Lower"))))
	if got := mustParseBytes(t, data).Fields().Title; got != "Lower" {
		t.Errorf("lowercase id3 chunk title = %q", got)
	}
}

func TestAIFFDuplicateId3ChunksDropped(t *testing.T) {
	data := aiffFile("AIFF", stdCOMM(), aiffSSND(400),
		aiffID3(id3v2(3, textFrame(3, "TIT2", "Primary"))),
		aiffID3(id3v2(3, textFrame(3, "TIT2", "Stale"))))
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
	// canonical model rather than passing through as an invalid-UTF-8 string.
	data := aiffFile("AIFF", aiffText("NAME", "caf\xe9"), stdCOMM(), aiffSSND(200))
	title := mustParseBytes(t, data).Fields().Title
	if title != "café" {
		t.Errorf("Latin-1 native value decoded to %q, want %q", title, "café")
	}
	if !utf8.ValidString(title) {
		t.Error("decoded title is not valid UTF-8")
	}
}

func TestAIFFClearAllRemovesNativeChunks(t *testing.T) {
	// Clearing the only tag drops the now-empty native chunk rather than leaving a
	// husk.
	data := aiffFile("AIFF", aiffText("NAME", "Gone"), stdCOMM(), aiffSSND(200))
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
	data := aiffFile("AIFF", stdCOMM(), aiffSSND(200),
		aiffID3(id3v2(3, textFrame(3, "TCON", "(17)"), textFrame(3, "TIT2", "T"))))
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
	data := aiffFile("AIFF", stdCOMM(), aiffSSND(400))
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
	if got.Fields().Title != "Fresh" || got.Fields().Comment != "note" {
		t.Errorf("bare edit: title=%q comment=%q", got.Fields().Title, got.Fields().Comment)
	}
}

// TestAIFFTruncatedSSNDWarns covers the truncation signal for AIFF: an SSND chunk
// declaring more bytes than the file holds is flagged; an intact file is not.
func TestAIFFTruncatedSSNDWarns(t *testing.T) {
	t.Run("declared overruns file", func(t *testing.T) {
		// The SSND header declares 100000 bytes but only the 8-byte sub-header plus a
		// little audio follow.
		ssndHdr := slices.Concat([]byte("SSND"), aiffBE32(100000))
		data := aiffFile("AIFF", stdCOMM(), slices.Concat(ssndHdr, make([]byte, 8+200)))
		doc := mustParseBytes(t, data)
		if !hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("expected truncated-audio warning; got %v", doc.Warnings())
		}
	})
	t.Run("intact file not flagged", func(t *testing.T) {
		data := aiffFile("AIFF", stdCOMM(), aiffSSND(400))
		if doc := mustParseBytes(t, data); hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("an intact AIFF must not be flagged truncated; got %v", doc.Warnings())
		}
	})
}
