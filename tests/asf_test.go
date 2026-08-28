package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"time"
	"unicode/utf16"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

const (
	sampleWMA = "../testdata/sample.wma"
	notagsWMA = "../testdata/notags.wma"
)

// ASF synthesis. ffmpeg writes WMA, but not the WM/Picture descriptor or the
// Metadata Library records, so those shapes are built here rather than shipped as
// binary fixtures.

// asfUTF16 encodes a NUL-terminated UTF-16LE string, the only text encoding ASF uses.
func asfUTF16(s string) []byte {
	b := make([]byte, 0, len(s)*2+2)
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return append(b, 0, 0)
}

// asfObject wraps a body in the 16-byte GUID and 64-bit size every ASF object carries.
func asfObject(guidHex string, body []byte) []byte {
	g, err := hexBytes(guidHex)
	if err != nil {
		panic(err)
	}
	out := append(slices.Clone(g), make([]byte, 8)...)
	binary.LittleEndian.PutUint64(out[16:24], uint64(len(body)+24))
	return append(out, body...)
}

func hexBytes(s string) ([]byte, error) {
	b := make([]byte, len(s)/2)
	for i := range b {
		var v int
		for j := range 2 {
			c := s[i*2+j]
			switch {
			case c >= '0' && c <= '9':
				v = v<<4 | int(c-'0')
			case c >= 'a' && c <= 'f':
				v = v<<4 | int(c-'a'+10)
			default:
				return nil, errors.New("bad hex")
			}
		}
		b[i] = byte(v)
	}
	return b, nil
}

// The on-disk GUID byte sequences, in the mixed-endian layout ASF stores them in.
const (
	guidHeaderHex      = "3026b2758e66cf11a6d900aa0062ce6c"
	guidFilePropsHex   = "a1dcab8c47a9cf118ee400c00c205365"
	guidStreamPropsHex = "9107dcb7b7a9cf118ee600c00c205365"
	guidHeaderExtHex   = "b503bf5f2ea9cf118ee300c00c205365"
	guidContentDescHex = "3326b2758e66cf11a6d900aa0062ce6c"
	guidExtContentHex  = "40a4d0d207e3d21197f000a0c95ea850"
	guidMetadataHex    = "eacbf8c5af5b77488467aa8c44fa4cca"
	guidAudioMediaHex  = "409e69f84d5bcf11a8fd00805f5c442b"
	guidNoErrCorrHex   = "00000000000000000000000000000000"
)

// asfFileProperties builds a File Properties object with the given play duration and
// preroll, both of which a reader must combine to report a track length.
func asfFileProperties(play time.Duration, prerollMS uint64) []byte {
	b := make([]byte, 80)
	binary.LittleEndian.PutUint64(b[40:48], uint64(play/(100*time.Nanosecond)))
	binary.LittleEndian.PutUint64(b[56:64], prerollMS)
	binary.LittleEndian.PutUint32(b[76:80], 128000)
	return asfObject(guidFilePropsHex, b)
}

// asfStreamProperties builds an audio Stream Properties object carrying a WAVEFORMATEX.
func asfStreamProperties(formatTag uint16, channels uint16, rate uint32, bits uint16) []byte {
	w := make([]byte, 18)
	binary.LittleEndian.PutUint16(w[0:2], formatTag)
	binary.LittleEndian.PutUint16(w[2:4], channels)
	binary.LittleEndian.PutUint32(w[4:8], rate)
	binary.LittleEndian.PutUint32(w[8:12], uint32(rate)*uint32(channels)*uint32(bits)/8)
	binary.LittleEndian.PutUint16(w[12:14], channels*bits/8)
	binary.LittleEndian.PutUint16(w[14:16], bits)

	g, _ := hexBytes(guidAudioMediaHex)
	e, _ := hexBytes(guidNoErrCorrHex)
	b := append(slices.Clone(g), e...)
	b = append(b, make([]byte, 8)...) // time offset
	b = binary.LittleEndian.AppendUint32(b, uint32(len(w)))
	b = binary.LittleEndian.AppendUint32(b, 0) // error correction data length
	b = binary.LittleEndian.AppendUint16(b, 1) // flags: stream number 1
	b = binary.LittleEndian.AppendUint32(b, 0) // reserved
	return asfObject(guidStreamPropsHex, append(b, w...))
}

// asfContentDescription builds the five-field Content Description object.
func asfContentDescription(title, author, copyright, description, rating string) []byte {
	fields := [][]byte{asfUTF16(title), asfUTF16(author), asfUTF16(copyright), asfUTF16(description), asfUTF16(rating)}
	var b []byte
	for _, f := range fields {
		b = binary.LittleEndian.AppendUint16(b, uint16(len(f)))
	}
	for _, f := range fields {
		b = append(b, f...)
	}
	return asfObject(guidContentDescHex, b)
}

// asfDescriptor is one Extended Content Description entry.
type asfDescriptor struct {
	name      string
	valueType uint16
	value     []byte
}

func asfExtContentDescription(ds ...asfDescriptor) []byte {
	b := binary.LittleEndian.AppendUint16(nil, uint16(len(ds)))
	for _, d := range ds {
		n := asfUTF16(d.name)
		b = binary.LittleEndian.AppendUint16(b, uint16(len(n)))
		b = append(b, n...)
		b = binary.LittleEndian.AppendUint16(b, d.valueType)
		b = binary.LittleEndian.AppendUint16(b, uint16(len(d.value)))
		b = append(b, d.value...)
	}
	return asfObject(guidExtContentHex, b)
}

// asfHeaderExtension nests a Metadata object inside a Header Extension, the only
// place the Metadata and Metadata Library records can appear.
func asfHeaderExtension(ds ...asfDescriptor) []byte {
	recs := binary.LittleEndian.AppendUint16(nil, uint16(len(ds)))
	for _, d := range ds {
		n := asfUTF16(d.name)
		recs = binary.LittleEndian.AppendUint16(recs, 0) // reserved / language index
		recs = binary.LittleEndian.AppendUint16(recs, 1) // stream number
		recs = binary.LittleEndian.AppendUint16(recs, uint16(len(n)))
		recs = binary.LittleEndian.AppendUint16(recs, d.valueType)
		recs = binary.LittleEndian.AppendUint32(recs, uint32(len(d.value)))
		recs = append(recs, n...)
		recs = append(recs, d.value...)
	}
	meta := asfObject(guidMetadataHex, recs)

	g, _ := hexBytes(guidNoErrCorrHex)
	b := append(slices.Clone(g), 6, 0) // reserved GUID + reserved word
	b = binary.LittleEndian.AppendUint32(b, uint32(len(meta)))
	return asfObject(guidHeaderExtHex, append(b, meta...))
}

// asfFile assembles a whole ASF file: the Header Object wrapping the given child
// objects, then a stub data region.
func asfFile(children ...[]byte) []byte {
	var body []byte
	for _, c := range children {
		body = append(body, c...)
	}
	g, _ := hexBytes(guidHeaderHex)
	head := append(slices.Clone(g), make([]byte, 14)...)
	binary.LittleEndian.PutUint64(head[16:24], uint64(30+len(body)))
	binary.LittleEndian.PutUint32(head[24:28], uint32(len(children)))
	head[28], head[29] = 0x01, 0x02
	return append(append(head, body...), make([]byte, 256)...)
}

// asfPictureValue builds a WM/Picture descriptor value: the type byte, the image
// length, a UTF-16LE MIME and description, then the bytes.
func asfPictureValue(picType byte, mime, desc string, data []byte) []byte {
	b := []byte{picType}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	b = append(b, asfUTF16(mime)...)
	b = append(b, asfUTF16(desc)...)
	return append(b, data...)
}

func TestWMAParse(t *testing.T) {
	doc := mustParseFile(t, sampleWMA)
	if doc.Format() != wl.FormatWMA {
		t.Fatalf("format = %v, want WMA", doc.Format())
	}
	tr := doc.Properties().First()
	if tr.Codec != "WMA v2" || tr.SampleRate != 44100 || tr.Channels != 2 {
		t.Errorf("track = %+v, want WMA v2 44100 Hz stereo", tr)
	}
	if tr.Duration <= 0 {
		t.Errorf("duration = %v, want > 0", tr.Duration)
	}
	if got := doc.Fields().Title; got != "Sample Title" {
		t.Errorf("title = %q", got)
	}
	if got := doc.Fields().RecordingDate; got != "2021" {
		t.Errorf("RecordingDate = %q, want 2021", got)
	}
}

// TestWMAWriteRefused pins the read-only contract: the refusal is the format
// sentinel (exit 3), not the unsupported-tag one, which is documented as "a tag
// exists that this version cannot model" and would misdescribe WMA.
func TestWMAWriteRefused(t *testing.T) {
	doc := mustParseFile(t, sampleWMA)
	if !doc.Capabilities().ReadOnly {
		t.Error("Capabilities().ReadOnly should be true for WMA")
	}
	_, err := doc.Edit().Set(tag.Title, "Nope").Prepare()
	if err == nil {
		t.Fatal("preparing an edit to a WMA file should fail")
	}
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("err = %v, want ErrUnsupportedFormat", err)
	}
}

// TestWMAUnchangedCopyStillWorks: the refusal sits after the no-op fast path, so
// copying a WMA verbatim - which changes nothing - still produces a whole file.
func TestWMAUnchangedCopyStillWorks(t *testing.T) {
	src := readFixture(t, sampleWMA)
	plan, err := mustParseBytes(t, src).Edit().Prepare()
	if err != nil {
		t.Fatalf("an unedited prepare must not be refused: %v", err)
	}
	if !plan.IsNoOp() {
		t.Error("an unedited WMA plan should be a no-op")
	}
	if out := applyToBytes(t, src, plan); !bytes.Equal(out, src) {
		t.Errorf("verbatim copy differs from the source (%d bytes vs %d)", len(out), len(src))
	}
}

// TestWMAContentDescriptionAndDescriptors covers both tag objects at once, including
// the numeric descriptor types and the slash-pair split every codec shares.
func TestWMAContentDescriptionAndDescriptors(t *testing.T) {
	data := asfFile(
		asfFileProperties(3*time.Second, 500),
		asfStreamProperties(0x0161, 2, 44100, 16),
		asfContentDescription("The Title", "The Artist", "The Copyright", "The Comment", "5 stars"),
		asfExtContentDescription(
			asfDescriptor{"WM/AlbumTitle", 0, asfUTF16("The Album")},
			asfDescriptor{"WM/TrackNumber", 0, asfUTF16("4/9")},
			asfDescriptor{"WM/BeatsPerMinute", 3, binary.LittleEndian.AppendUint32(nil, 128)},
			asfDescriptor{"WM/Year", 5, binary.LittleEndian.AppendUint16(nil, 1999)},
			asfDescriptor{"AspectRatioX", 3, binary.LittleEndian.AppendUint32(nil, 1)},
		),
	)
	doc := mustParseBytes(t, data)
	f := doc.Fields()
	if f.Title != "The Title" || f.Artists[0] != "The Artist" || f.Album != "The Album" {
		t.Errorf("fields = %+v", f)
	}
	if got, _ := doc.Get(tag.Comment); len(got) != 1 || got[0] != "The Comment" {
		t.Errorf("COMMENT = %v, want the Description field", got)
	}
	if got, _ := doc.Get(tag.TrackNumber); !slices.Equal(got, []string{"4"}) {
		t.Errorf("TRACKNUMBER = %v, want the number half of 4/9", got)
	}
	if got, _ := doc.Get(tag.TrackTotal); !slices.Equal(got, []string{"9"}) {
		t.Errorf("TRACKTOTAL = %v, want 9", got)
	}
	if got, _ := doc.Get(tag.BPM); !slices.Equal(got, []string{"128"}) {
		t.Errorf("BPM = %v, want the DWORD rendered as decimal", got)
	}
	if got := doc.Fields().RecordingDate; got != "1999" {
		t.Errorf("RecordingDate = %q, want the WORD rendered as decimal", got)
	}
	// Stream bookkeeping is not metadata about the work and must not appear as a field.
	if _, ok := doc.Get(tag.Key("ASPECTRATIOX")); ok {
		t.Error("AspectRatioX leaked into the canonical set")
	}
	// Play duration includes the preroll, so the reported length is the difference.
	if got := doc.Properties().First().Duration; got != 2500*time.Millisecond {
		t.Errorf("duration = %v, want 2.5s (3s play minus 500ms preroll)", got)
	}
}

// TestWMADuplicateValueFolded: the ffmpeg family writes the Content Description
// fields and repeats them as descriptors. That is one value with two spellings, not a
// multi-valued field.
func TestWMADuplicateValueFolded(t *testing.T) {
	data := asfFile(
		asfStreamProperties(0x0161, 2, 44100, 16),
		asfContentDescription("Same", "", "", "", ""),
		asfExtContentDescription(asfDescriptor{"title", 0, asfUTF16("Same")}),
	)
	if got, _ := mustParseBytes(t, data).Get(tag.Title); !slices.Equal(got, []string{"Same"}) {
		t.Errorf("TITLE = %v, want one value", got)
	}

	// A genuine disagreement still contributes both, so the conflict stays visible.
	data2 := asfFile(
		asfStreamProperties(0x0161, 2, 44100, 16),
		asfContentDescription("First", "", "", "", ""),
		asfExtContentDescription(asfDescriptor{"title", 0, asfUTF16("Second")}),
	)
	if got, _ := mustParseBytes(t, data2).Get(tag.Title); len(got) != 2 {
		t.Errorf("TITLE = %v, want both disagreeing values surfaced", got)
	}
}

// TestWMAPicture covers the WM/Picture descriptor, including its role and
// description - which ASF stores and most other picture conventions do not.
func TestWMAPicture(t *testing.T) {
	png := tinyPNG()
	data := asfFile(
		asfStreamProperties(0x0161, 2, 44100, 16),
		asfExtContentDescription(
			asfDescriptor{"WM/Picture", 1, asfPictureValue(3, "image/png", "Front art", png)},
			asfDescriptor{"WM/Picture", 1, asfPictureValue(4, "image/png", "", png)},
		),
	)
	pics := mustParseBytes(t, data).Pictures()
	if len(pics) != 2 {
		t.Fatalf("pictures = %d, want 2", len(pics))
	}
	if pics[0].Type != wl.PicFrontCover || pics[0].MIME != "image/png" || pics[0].Description != "Front art" {
		t.Errorf("first picture = %+v", pics[0])
	}
	if !bytes.Equal(pics[0].Data, png) {
		t.Error("the decoded image bytes do not match what was stored")
	}
	if pics[1].Type != wl.PicBackCover {
		t.Errorf("second picture type = %v, want back cover", pics[1].Type)
	}
}

// TestWMAPictureInMetadataLibrary: a large cover is written into the Header
// Extension's Metadata objects rather than the Extended Content Description, whose
// value length is only 16 bits.
func TestWMAPictureInMetadataObject(t *testing.T) {
	png := tinyPNG()
	data := asfFile(
		asfStreamProperties(0x0161, 2, 44100, 16),
		asfHeaderExtension(
			asfDescriptor{"WM/Picture", 1, asfPictureValue(3, "image/png", "", png)},
			asfDescriptor{"WM/AlbumTitle", 0, asfUTF16("Nested Album")},
		),
	)
	doc := mustParseBytes(t, data)
	if pics := doc.Pictures(); len(pics) != 1 || pics[0].Type != wl.PicFrontCover || !bytes.Equal(pics[0].Data, png) {
		t.Fatalf("pictures = %+v, want one front cover with the stored bytes", doc.Pictures())
	}
	if got := doc.Fields().Album; got != "Nested Album" {
		t.Errorf("album = %q, want the Metadata record read", got)
	}
}

// TestWMAMalformedPictureWarns: a bad cover is surfaced, not silently dropped.
func TestWMAMalformedPictureWarns(t *testing.T) {
	data := asfFile(
		asfStreamProperties(0x0161, 2, 44100, 16),
		asfExtContentDescription(asfDescriptor{"WM/Picture", 1, []byte{3, 0, 0, 0, 0}}),
	)
	doc := mustParseBytes(t, data)
	if len(doc.Pictures()) != 0 {
		t.Errorf("pictures = %+v, want none decoded", doc.Pictures())
	}
	if !hasWarning(doc, wl.WarnInvalidPicture) {
		t.Error("expected an invalid-picture warning")
	}
}

// TestWMACodecVariantsAllRead: WMA is a family, and Pro/Lossless/Voice differ only in
// the decoder they need. A metadata reader must read all of them rather than refusing
// a variant by name.
func TestWMACodecVariantsAllRead(t *testing.T) {
	for _, c := range []struct {
		tag  uint16
		name string
	}{
		{0x0160, "WMA v1"},
		{0x0161, "WMA v2"},
		{0x0162, "WMA Pro"},
		{0x0163, "WMA Lossless"},
		{0x000A, "WMA Voice"},
	} {
		data := asfFile(asfStreamProperties(c.tag, 2, 44100, 16), asfContentDescription("T", "", "", "", ""))
		doc := mustParseBytes(t, data)
		if got := doc.Properties().First().Codec; got != c.name {
			t.Errorf("format tag %#04x -> codec %q, want %q", c.tag, got, c.name)
		}
		if got := doc.Fields().Title; got != "T" {
			t.Errorf("format tag %#04x: title = %q", c.tag, got)
		}
	}
}

func TestWMATruncatedHeaderRejected(t *testing.T) {
	g, _ := hexBytes(guidHeaderHex)
	_, err := wl.Parse(context.Background(), wl.BytesSource(append(g, 0, 0, 0)))
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want ErrInvalidData", err)
	}
}
