package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// Synthetic ID3 builders. The audio body is borrowed from the tagless
// fixture so the synthesized files are real, decodable MP3s.

func mp3Audio(t *testing.T) []byte {
	t.Helper()
	return readFixture(t, notagsMP3)
}

// txxxFrame builds a TXXX user frame: encoding byte, description, NUL, value.
func txxxFrame(version byte, desc, value string) []byte {
	return testfile.ID3Frame(version, "TXXX", slices.Concat([]byte{0}, []byte(desc), []byte{0}, []byte(value)))
}

// frame22 builds a v2.2 frame: 3-char ID, 3-byte size, no flags.
func frame22(id string, body []byte) []byte {
	out := append([]byte(id), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	return append(out, body...)
}

// textFrame22 builds a v2.2 Latin-1 text frame.
func textFrame22(id, text string) []byte {
	return frame22(id, append([]byte{0}, text...))
}

// mp3XingFrameVariant builds one MPEG-1 Layer 3 frame (128 kbps, 44.1 kHz, 417 bytes) carrying a
// Xing header with a frame-count field. When crc is set the frame's protection bit is cleared and
// the Xing sits 2 bytes later, behind the optional MPEG CRC; mono shrinks the side-information
// block that precedes it.
func mp3XingFrameVariant(mono, crc bool, declared uint32) []byte {
	const frameLen = 417
	f := make([]byte, frameLen)
	f[0] = 0xFF
	f[1] = 0xFB // MPEG-1 Layer 3, protection bit set (no CRC)
	if crc {
		f[1] = 0xFA // protection bit cleared: a 2-byte CRC follows the header
	}
	f[2] = 0x90 // 128 kbps, 44.1 kHz, no padding
	if mono {
		f[3] = 0xC0 // channel mode 3 = mono (stereo side info is larger)
	}
	side := 32 // MPEG-1 stereo
	if mono {
		side = 17
	}
	off := 4 + side // 4-byte header + side info
	if crc {
		off += 2 // skip the CRC
	}
	copy(f[off:off+4], "Xing")
	f[off+7] = 1 // flags low byte: frame-count present
	binary.BigEndian.PutUint32(f[off+8:off+12], declared)
	return f
}

// mp3VBRVariantFile pairs a Xing frame with a bare second frame so parseMPEG's two-frame consensus
// accepts the stream.
func mp3VBRVariantFile(mono, crc bool, declared uint32) []byte {
	f0 := mp3XingFrameVariant(mono, crc, declared)
	f1 := make([]byte, len(f0))
	copy(f1[:4], f0[:4]) // same header so the next-frame check agrees
	return append(f0, f1...)
}

func TestMP3VBRXingOffsetProbing(t *testing.T) {
	// The Xing header sits after an optional 2-byte MPEG CRC and a channel-dependent side-info
	// block. readVBR must probe both the CRC-less and CRC-present offsets, for mono and stereo, or
	// a CRC-protected VBR file falls back to the CBR estimate and reports the wrong duration.
	const samplesPerFrame = 1152 // MPEG-1 Layer 3
	declared := uint32(2)
	for _, c := range []struct {
		name      string
		mono, crc bool
	}{
		{"stereo behind CRC", false, true},
		{"stereo without CRC", false, false},
		{"mono behind CRC", true, true},
		{"mono without CRC", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, mp3VBRVariantFile(c.mono, c.crc, declared))
			if doc.Format() != wl.FormatMP3 {
				t.Fatalf("format = %v, want MP3", doc.Format())
			}
			if got := doc.Properties().Tracks[0].TotalSamples; got != uint64(declared)*samplesPerFrame {
				t.Errorf("TotalSamples = %d, want %d (Xing frame count not read)", got, uint64(declared)*samplesPerFrame)
			}
		})
	}
}

func TestMP3NumericGenreRead(t *testing.T) {
	data := append(testfile.ID3v2(3, testfile.TextFrame(3, "TCON", "(17)"), testfile.TextFrame(3, "TIT2", "T")), mp3Audio(t)...)
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
	data := append(testfile.ID3v2(3, testfile.TextFrame(3, "TCON", "(51)(39)"), testfile.TextFrame(3, "TIT2", "T")), mp3Audio(t)...)
	g := mustParseBytes(t, data).Fields().Genres
	if len(g) != 2 || g[0] != "Techno-Industrial" || g[1] != "Noise" {
		t.Errorf("(51)(39) -> %v, want [Techno-Industrial Noise]", g)
	}
}

func TestMP3NumericGenreWrite(t *testing.T) {
	data := append(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "T")), mp3Audio(t)...)
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
	data := append(testfile.ID3v2(2, textFrame22("TT2", "V22 Title"), textFrame22("TP1", "V22 Artist")), mp3Audio(t)...)
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
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "V2 Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, testfile.ID3v1("V1 Different Title", "", "", "", "", 255)...)

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

// An edit preserves the trailing ID3v1 and the family entry its title projects.
func TestMP3EditKeepsID3v1Family(t *testing.T) {
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "V2 Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, testfile.ID3v1("V1 Different", "", "", "", "", 255)...)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(mustParseBytes(t, applyToBytes(t, data, plan)).Families(), func(f wl.FamilyValue) bool {
		return f.Family == wl.FamilyID3v1 && f.Key == tag.Title
	}) {
		t.Error("the written file lost the ID3v1 title family entry")
	}
}

// apeTag builds a minimal footer-only APEv2 tag (mirrors the ape package test).
func apeTag(items map[string]string) []byte { return apeTagCount(items, len(items)) }

// apeTagCount is apeTag with an explicitly chosen footer item-count field, so a test can
// overstate it to exercise the decoded-item cap while the raw tag bytes stay preserved.
func apeTagCount(items map[string]string, footerCount int) []byte {
	var body []byte
	for k, v := range items {
		var hdr [8]byte
		put32le(hdr[0:4], len(v))
		put32le(hdr[4:8], 0)
		body = append(body, hdr[:]...)
		body = append(body, []byte(k)...)
		body = append(body, 0)
		body = append(body, []byte(v)...)
	}
	foot := make([]byte, 32)
	copy(foot[0:8], "APETAGEX")
	put32le(foot[8:12], 2000)
	put32le(foot[12:16], len(body)+32)
	put32le(foot[16:20], footerCount)
	return append(body, foot...)
}

// APE footer can overstate its item count without corrupting the read. The decoded item list is
// capped, the raw APE bytes remain available for preservation, and the authoritative ID3v2 tag
// still reads.
func TestMP3APEOverstatedItemCountPreserved(t *testing.T) {
	ape := apeTagCount(map[string]string{"Title": "APE Title"}, 1_000_000)
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "V2 Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, ape...)

	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "V2 Title" {
		t.Errorf("authoritative id3v2 title = %q, want V2 Title", got)
	}
	if !hasWarning(doc, wl.WarnLegacyAPE) {
		t.Error("expected a legacy-APE warning despite the overstated count")
	}
	// The raw APE region round-trips byte-identical across an edit.
	plan, err := doc.Edit().Set(tag.Artist, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, data, plan); !bytes.Contains(out, ape) {
		t.Error("the raw APE region was not preserved verbatim under an overstated item count")
	}
}

func put32le(b []byte, v int) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func TestMP3StripLegacy(t *testing.T) {
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Keep"))
	data = append(data, mp3Audio(t)...)
	data = append(data, testfile.ID3v1("Old V1", "", "", "", "", 255)...)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Keep").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("stripping a present legacy tag is not a no-op")
	}
	out := applyToBytes(t, data, plan)
	if bytes.HasSuffix(out, testfile.ID3v1("Old V1", "", "", "", "", 255)) {
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
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
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
	// The values still round-trip for this reader.
	got := mustParseBytes(t, applyToBytes(t, src, plan)).Fields().Artists
	if !slices.Equal(got, []string{"A", "B", "C"}) {
		t.Errorf("multi-value artists round-trip = %v", got)
	}
}

// mp3XingFrame builds an MPEG-1 Layer III stereo frame header (128 kbps, 44100 Hz) followed by a
// Xing header declaring frameCount frames.
func mp3XingFrame(frameCount uint32) []byte {
	b := []byte{0xFF, 0xFB, 0x90, 0x00} // MPEG-1 L3, 128 kbps, 44100 Hz, stereo
	b = append(b, make([]byte, 32)...)  // side information (MPEG-1 stereo)
	b = append(b, []byte("Xing")...)
	b = append(b, 0, 0, 0, 1) // flags: frame-count present
	fc := make([]byte, 4)
	binary.BigEndian.PutUint32(fc, frameCount)
	return append(b, fc...)
}

// mp3VBRStream builds `frames` valid consecutive MPEG-1 Layer III frames (128 kbps, 44100 Hz,
// stereo, 417 bytes each, zero-filled after the header), with a Xing header in the first frame
// declaring `declared` frames.
func mp3VBRStream(frames int, declared uint32) []byte {
	const frameLen = 417
	out := make([]byte, 0, frames*frameLen)
	for i := range frames {
		f := make([]byte, frameLen)
		f[0], f[1], f[2], f[3] = 0xFF, 0xFB, 0x90, 0x00
		if i == 0 {
			copy(f[36:40], "Xing") // after the 32-byte MPEG-1-stereo side information
			f[43] = 1              // flags: frame-count present
			binary.BigEndian.PutUint32(f[44:48], declared)
		}
		out = append(out, f...)
	}
	return out
}

// A VBR MP3 whose Xing header survives but whose frames are mostly gone (a head -c truncation):
// the declared frame count implies minutes of audio while only ~48 bytes are present, so the
// average bitrate collapses below the MPEG floor and truncated-audio fires.
func TestMP3TruncatedAfterXingWarns(t *testing.T) {
	t.Run("frames missing after Xing", func(t *testing.T) {
		data := append(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "X")), mp3XingFrame(10000)...)
		doc := mustParseBytes(t, data)
		if doc.Format() != wl.FormatMP3 {
			t.Fatalf("format = %v, want MP3", doc.Format())
		}
		if !hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("expected truncated-audio warning; got %v", doc.Warnings())
		}
	})
	t.Run("extreme truncation collapses bitrate to zero", func(t *testing.T) {
		// A multi-minute declared duration with only the ~48-byte header present drives the integer average
		// bitrate to exactly 0; the warning must still fire (the signal is "< 8000", not "> 0 && < 8000").
		data := append(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "X")), mp3XingFrame(50000)...)
		if doc := mustParseBytes(t, data); !hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("expected truncated-audio warning on an extreme truncation; got %v", doc.Warnings())
		}
	})
	t.Run("intact VBR stream not flagged", func(t *testing.T) {
		// Two full frames whose Xing count matches the frames present: the average
		// bitrate is ~128 kbps, so the "< 8000" branch runs and no warning fires. The
		// codec check rules out the vacuous case, where a stub that fails parseMPEG
		// never reaches the check.
		data := append(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "X")), mp3VBRStream(2, 2)...)
		doc := mustParseBytes(t, data)
		if got := doc.Properties().First().Codec; got != "MP3" {
			t.Fatalf("setup: codec = %q, want MP3 (parseMPEG must succeed for this test to mean anything)", got)
		}
		if hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("an intact VBR stream must not be flagged truncated; got %v", doc.Warnings())
		}
	})
}

// MPEG audio behind an ID3v2 tag and a gap stays MP3 with the plain stream's geometry: no other
// codec confirms a stream there, not even a lone ADTS header inside the gap, a free-format header
// there does not stand in for the confirmed stream behind it, and frames no second frame confirms,
// free-format ones or a lone frame at the end of the file or before the tags there, are found as in
// a file without the tag, those tags read as there. The gap is reported, is not legacy, and is left
// out of the essence, whose digest is the plain stream's, and an edit keeps it after the tag.
func TestMP3BehindID3WithGapStaysMP3(t *testing.T) {
	notags := mp3Audio(t)
	free := bytes.Repeat(slices.Concat([]byte{0xFF, 0xFB, 0x00, 0x00}, make([]byte, 1040)), 200) // bitrate index 0
	lead := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Gap"))
	for _, c := range []struct {
		name       string
		gap, audio []byte
	}{
		{"ten zero bytes", make([]byte, 10), notags},
		{"three bytes", []byte{1, 2, 3}, notags},
		{"a lone ADTS header", slices.Concat([]byte{1, 2, 3}, adtsStream(2, 1, 0)), notags},
		{"a free-format header", slices.Concat([]byte{1, 2, 3}, free[:4], []byte{5, 6, 7}), notags},
		{"free-format frames", []byte{1, 2, 3}, free},
		{"a single frame", []byte{1, 2, 3}, mp3Frames(1)},
		{"a single frame and an ID3v1 tag", []byte{1, 2, 3}, slices.Concat(mp3Frames(1), testfile.ID3v1("V1", "", "", "", "", 255))},
		{"a single frame and an APE tag", []byte{1, 2, 3}, slices.Concat(mp3Frames(1), apeTag(map[string]string{"Title": "APE"}))},
	} {
		t.Run(c.name, func(t *testing.T) {
			plain := mustParseBytes(t, c.audio)
			want := plain.Properties().First()
			if want.SampleRate == 0 {
				t.Fatalf("setup: the plain stream reads no audio: %+v", want)
			}
			essence := essenceOf(t, c.audio)
			src := slices.Concat(lead, c.gap, c.audio)
			doc := mustParseBytes(t, src)
			if doc.Format() != wl.FormatMP3 {
				t.Fatalf("detected as %v, want MP3", doc.Format())
			}
			got := doc.Properties().First()
			if hasWarning(doc, wl.WarnNoAudioFrames) || got.Codec != want.Codec || got.SampleRate != want.SampleRate ||
				got.Channels != want.Channels || got.TotalSamples != want.TotalSamples {
				t.Errorf("track = %+v, warnings %v; want the plain stream's %+v", got, doc.Warnings(), want)
			}
			if codes := slices.DeleteFunc(warningCodes(doc), func(code wl.WarningCode) bool { return code == wl.WarnLeadingBytes }); !slices.Equal(codes, warningCodes(plain)) {
				t.Errorf("warnings %v besides the gap's; want the plain stream's %v", codes, warningCodes(plain))
			}
			if !hasWarning(doc, wl.WarnLeadingBytes) || !essenceOf(t, src).Equal(essence) {
				t.Errorf("warnings %v; want the gap reported and the plain stream's digest", doc.Warnings())
			}
			if doc.HasOpaqueLegacyContent() {
				t.Error("the gap counts as legacy content, though it goes with the native tag")
			}
			plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, src, plan)
			if !bytes.HasSuffix(out, slices.Concat(c.gap, c.audio)) || !essenceOf(t, out).Equal(essence) {
				t.Error("the edit did not keep the gap ahead of the frames, or changed the digest")
			}
		})
	}
}

// coverWithFrameHeader is a cover item's value, a file name and a JPEG whose comment segment opens
// FF FE 00 10: a free-format MPEG-1 Layer I header, which the parser's own scan accepts alone.
func coverWithFrameHeader() []byte {
	jpeg := testfile.JPEG()
	return slices.Concat([]byte("cover.jpg\x00"), jpeg[:2], []byte{0xFF, 0xFE, 0x00, 0x10}, []byte("made by camera"), jpeg[2:])
}

// Behind an ID3v2 tag and a gap with no frame a second one confirms, frames are looked for only up
// to the tags at the end of the file, and within 64 KiB of the tag: bytes inside those tags that open
// like a frame header, a JPEG cover's comment marker or an ID3v1 title opening FF FB, are no stream,
// so a file without frames stays no-audio with its tags read and refuses a write and a strip, while a
// lone frame before the tags is the stream, the tags read after it and kept by an edit. A lone frame
// before an APE tag that junk keeps from the end of the file is no stream either, as the parse reads
// on through that tag, and no gap is reported before it.
func TestMP3FramesNotLookedForInsideTrailingTags(t *testing.T) {
	lead := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Gap"))
	gap := []byte{1, 2, 3}
	cover := coverWithFrameHeader()
	headed, err := ape.Render([]ape.Item{{Key: "Title", Value: "APE title"}, {Key: "Cover Art (Front)", Data: cover, Flags: 2}}, 2000, true)
	if err != nil {
		t.Fatal(err)
	}
	v1 := testfile.ID3v1("V1 title", "V1 artist", "", "2001", "", 12)
	const (
		leading   = wl.WarnLeadingBytes
		trailing  = wl.WarnTrailingBytes
		id3v1     = wl.WarnTrailingID3v1
		legacyAPE = wl.WarnLegacyAPE
		noAudio   = wl.WarnNoAudioFrames
	)
	for _, c := range []struct {
		name     string
		data     []byte
		audio    bool
		warns    []wl.WarningCode
		titles   []wl.Family // the legacy families holding a title
		opaque   bool
		trailing []byte // the tags after the stream, which an edit keeps
	}{
		{"a JPEG cover in an APE tag", slices.Concat(lead, gap, apeTagBinaryItem("Cover Art (Front)", cover)), false,
			[]wl.WarningCode{legacyAPE, noAudio}, nil, true, nil},
		{"a JPEG cover and a title in an APE tag, then an ID3v1 tag", slices.Concat(lead, gap, headed, v1), false,
			[]wl.WarningCode{id3v1, legacyAPE, noAudio}, []wl.Family{wl.FamilyAPEv2, wl.FamilyID3v1}, true, nil},
		{"an ID3v1 title opening like a frame header", slices.Concat(lead, gap, testfile.ID3v1("\xff\xfbAB title", "", "", "", "", 12)), false,
			[]wl.WarningCode{id3v1, noAudio}, []wl.Family{wl.FamilyID3v1}, false, nil},
		{"a frame, then that APE tag and an ID3v1 tag", slices.Concat(lead, gap, mp3Frames(1), headed, v1), true,
			[]wl.WarningCode{leading, id3v1, legacyAPE}, []wl.Family{wl.FamilyAPEv2, wl.FamilyID3v1}, true, slices.Concat(headed, v1)},
		{"a frame more than 64 KiB past the tag", slices.Concat(lead, trailerJunk(70<<10), mp3Frames(1), v1), false,
			[]wl.WarningCode{id3v1, noAudio}, []wl.Family{wl.FamilyID3v1}, false, nil},
		{"a frame, then an APE tag, junk and an ID3v1 tag", slices.Concat(lead, gap, mp3Frames(1), apeTag(map[string]string{"Title": "APE title"}), trailerJunk(100), v1), false,
			[]wl.WarningCode{trailing, id3v1, legacyAPE, noAudio}, []wl.Family{wl.FamilyAPEv2, wl.FamilyID3v1}, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, c.data)
			if got := warningCodes(doc); !slices.Equal(got, c.warns) {
				t.Errorf("warnings %v, want the codes %v", doc.Warnings(), c.warns)
			}
			for _, f := range c.titles {
				if !hasFamily(doc, f, tag.Title) {
					t.Errorf("no %v title in the family view %+v", f, doc.Families())
				}
			}
			if doc.HasOpaqueLegacyContent() != c.opaque {
				t.Errorf("opaque legacy content = %v, want %v", doc.HasOpaqueLegacyContent(), c.opaque)
			}
			plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
			if !c.audio {
				if slices.ContainsFunc(doc.Native().Describe(), func(e wl.NativeEntry) bool { return e.Kind == "leading bytes" }) {
					t.Errorf("a file without audio lists leading bytes: %+v", doc.Native().Describe())
				}
				_, stripErr := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
				if err == nil || stripErr == nil {
					t.Errorf("a file without frames accepted an edit (%v) or a strip (%v)", err, stripErr)
				}
				return
			}
			if tr := doc.Properties().First(); tr.Codec != "MP3" || tr.Bitrate != 128000 {
				t.Errorf("track %+v, want MP3 at 128 kbps", tr)
			}
			if err != nil {
				t.Fatal(err)
			}
			if out := applyToBytes(t, c.data, plan); !bytes.HasSuffix(out, c.trailing) {
				t.Error("the edit did not keep the tags after the frame")
			}
		})
	}
}

// An MP3 behind its ID3v2 tag and a second tag longer than a resync window has audio: the stream
// starts behind every chained tag, and the second tag is a duplicate, out of the essence.
func TestMP3BehindALargeSecondTag(t *testing.T) {
	audio := mp3Audio(t)
	second := testfile.ID3v2(4, txxxFrame(4, "Filler", strings.Repeat("x", 100<<10)))
	src := slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Front")), second, audio)
	doc := mustParseBytes(t, src)
	if hasWarning(doc, wl.WarnNoAudioFrames) || !hasWarning(doc, wl.WarnDuplicateTagBlock) || hasWarning(doc, wl.WarnLeadingBytes) {
		t.Errorf("warnings %v; want audio, the second tag reported as a duplicate", doc.Warnings())
	}
	if want := mustParseBytes(t, audio); !reflect.DeepEqual(doc.Properties(), want.Properties()) {
		t.Errorf("properties %+v, want the plain stream's %+v", doc.Properties(), want.Properties())
	}
	if !essenceOf(t, src).Equal(essenceOf(t, audio)) {
		t.Error("the digest differs from the plain stream's")
	}
}

// Two MP3 streams one after the other, the first with an Info header counting only its own bytes,
// read as one stream: the count ends where frames of the same stream go on, so the frames decide
// where the audio ends, and the second stream is not junk.
func TestMP3ConcatenatedStreamsStayAudio(t *testing.T) {
	audio := mp3Audio(t)
	doc := mustParseBytes(t, slices.Concat(audio, audio, testfile.ID3v1("V1", "", "", "", "", 255)))
	size := -1
	for _, e := range doc.Native().Describe() {
		if e.Kind == "MPEG audio" {
			size = e.Size
		}
	}
	if size != 2*len(audio) || hasWarning(doc, wl.WarnTrailingBytes) {
		t.Errorf("MPEG audio of %d bytes, warnings %v; want both streams, %d bytes, and no junk", size, doc.Warnings(), 2*len(audio))
	}
}

// ffmpegTag is the ID3v2.4 tag ffmpeg writes ahead of an MP3 it makes, an encoder frame alone.
func ffmpegTag() []byte { return testfile.ID3v2(4, testfile.TextFrame(4, "TSSE", "Lavf61.7.100")) }

// A VBR header's byte count a byte short of the frames, or a byte past them, ends no frame and counts
// none the file lacks, so the frames place the end: the file reads whole, nothing junk or cut.
func TestMP3ByteCountOffByAByte(t *testing.T) {
	audio := mp3Audio(t)
	if string(audio[21:25]) != "Info" {
		t.Fatal("setup: notags.mp3 should carry an Info header 21 bytes in")
	}
	const count = 21 + 12 // past the Info tag, its flags and its frame count
	for _, delta := range []int{-1, 1} {
		data := slices.Clone(audio)
		binary.BigEndian.PutUint32(data[count:], binary.BigEndian.Uint32(data[count:])+uint32(delta))
		doc := mustParseBytes(t, data)
		if ws := doc.Warnings(); len(ws) != 0 {
			t.Errorf("count off by %d: warnings %v, want none", delta, ws)
		}
		if es := doc.Native().Describe(); len(es) != 1 || es[0].Size != len(data) {
			t.Errorf("count off by %d: native entries %+v, want the whole file as audio", delta, es)
		}
	}
}

// A VBR header whose byte count ends where the same stream goes on, as a stream joined after it does,
// counts only part of the stream, so it gives no duration either: the stream reads as one without the
// header does, its duration estimated from the bytes at the first frame's bitrate, however the tags
// sit between them, the joined file's own tag included. That is no truncation.
func TestMP3JoinedStreamGivesNoHeaderDuration(t *testing.T) {
	audio := mp3Audio(t)
	if string(audio[21:25]) != "Info" {
		t.Fatal("setup: notags.mp3 should carry an Info header 21 bytes in")
	}
	blank := func(b []byte, at int) []byte {
		b = slices.Clone(b)
		copy(b[at:at+4], make([]byte, 4))
		return b
	}
	ape := headedAPETag(t, "Title", "Coda")
	lead := ffmpegTag()
	for _, c := range []struct {
		name       string
		data       []byte
		headerless []byte // the same bytes with the first stream's Info header blanked
	}{
		{"two streams", slices.Concat(audio, audio), blank(slices.Concat(audio, audio), 21)},
		{"two streams, a tag between", slices.Concat(audio, ape, audio, testfile.ID3v1("V1", "", "", "", "", 255)),
			blank(slices.Concat(audio, audio), 21)},
		{"two ffmpeg-made files", slices.Concat(lead, audio, lead, audio),
			blank(slices.Concat(lead, audio, lead, audio), len(lead)+21)},
	} {
		doc := mustParseBytes(t, c.data)
		if got, want := doc.Properties(), mustParseBytes(t, c.headerless).Properties(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: properties %+v, want the header-less stream's %+v", c.name, got, want)
		}
		if hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("%s: a joined stream reads as truncated", c.name)
		}
	}
	if one := mustParseBytes(t, audio).Properties().First(); one.TotalSamples == 0 ||
		one.Duration >= mustParseBytes(t, slices.Concat(audio, audio)).Properties().First().Duration {
		t.Errorf("one stream: %+v, want the Info header's duration, shorter than the joined stream's", one)
	}
}

// A VBR byte count that runs past the tags by a frame or more is a truncation: the header still gives
// the duration it declares, and the file is reported truncated by the audio bytes the header declares
// past the audio it holds. The cut frame, however few of its bytes are left, stays audio; junk after
// the last whole frame does not, and counts toward the cut.
func TestMP3ByteCountPastTheTagsIsTruncation(t *testing.T) {
	audio := mp3Audio(t)
	full := mustParseBytes(t, audio).Properties().First()
	v1 := testfile.ID3v1("V1", "", "", "", "", 255)
	starts := mpegFrameStarts(audio)
	cut := audio[:starts[len(starts)-5]] // five frames cut off, at a frame boundary
	for _, c := range []struct {
		name  string
		data  []byte
		audio int // the bytes of MPEG audio
		junk  int // trailing bytes reported
	}{
		{"cut to a tenth", audio[:len(audio)/10], len(audio) / 10, 0},
		{"cut to half", audio[:len(audio)/2], len(audio) / 2, 0},
		{"cut by one frame's worth, tagged", slices.Concat(audio[:len(audio)-417], v1), len(audio) - 417, 0},
		{"cut to a tenth, an APE tag after", slices.Concat(audio[:len(audio)/10], apeTag(map[string]string{"Title": "T"})), len(audio) / 10, 0},
		{"cut on a frame, junk after", slices.Concat(cut, trailerJunk(10)), len(cut), 10},
		{"cut on a frame, a tag and junk after", slices.Concat(cut, headedAPETag(t, "Title", "Coda"), trailerJunk(10)), len(cut), 10},
	} {
		doc := mustParseBytes(t, c.data)
		got := doc.Properties().First()
		if got.Duration != full.Duration || got.TotalSamples != full.TotalSamples {
			t.Errorf("%s: %v, %d samples; want the declared %v, %d samples", c.name, got.Duration, got.TotalSamples, full.Duration, full.TotalSamples)
		}
		w, ok := docWarning(doc, wl.WarnTruncatedAudio)
		if want := fmt.Sprintf("by %d byte(s), declares more audio than the file holds", len(audio)-c.audio); !ok || !strings.Contains(w.Message, want) {
			t.Errorf("%s: truncated-audio warning %q (present %v), want it to say the header, %s", c.name, w.Message, ok, want)
		}
		junk, found := docWarning(doc, wl.WarnTrailingBytes)
		if size := nativeSize(doc, "MPEG audio"); size != c.audio || found != (c.junk > 0) ||
			found && !strings.HasPrefix(junk.Message, fmt.Sprintf("%d byte(s) after the MPEG stream belong to no tag", c.junk)) {
			t.Errorf("%s: %d bytes of MPEG audio, warnings %v; want %d, the cut frame among them, and %d junk bytes", c.name, size, doc.Warnings(), c.audio, c.junk)
		}
	}
	if hasWarning(mustParseBytes(t, audio), wl.WarnTruncatedAudio) {
		t.Error("the whole stream reads as truncated")
	}
	// A byte count the stream fills says it is whole, whatever its frame count claims.
	whole := mp3VBRStream(2, 10000)
	whole[43] = 3 // frame count and byte count present
	binary.BigEndian.PutUint32(whole[48:52], uint32(len(whole)))
	if doc := mustParseBytes(t, whole); hasWarning(doc, wl.WarnTruncatedAudio) {
		t.Errorf("a stream its byte count covers reads as truncated: %v", doc.Warnings())
	}
	// A byte count shorter than its own frame counts nothing, so the frame count's average bitrate
	// still shows the truncation, as with no byte count at all.
	short := mp3VBRStream(2, 10000)
	short[43] = 3
	binary.BigEndian.PutUint32(short[48:52], 100)
	if doc := mustParseBytes(t, short); !hasWarning(doc, wl.WarnTruncatedAudio) {
		t.Errorf("a byte count shorter than a frame hid the truncation: %v", doc.Warnings())
	}
}

// An MP3 truncated on a frame boundary, then an APE tag with less junk around it than the cut: the
// tag is found, read and removed by a legacy strip, which keeps the junk out of the essence and the
// truncation reported as before, by the bytes cut from the frames, and the digest is the frames'
// throughout. As much junk as the cut or more would hold the byte count's end and read as audio.
func TestMP3TagAfterATruncationIsFound(t *testing.T) {
	audio := mp3Audio(t)
	starts := mpegFrameStarts(audio)
	cut := audio[:starts[len(starts)-5]] // five frames cut off, at a frame boundary
	synth := mp3VBRStream(300, 300)
	synth[43] = 3 // frame count and byte count present
	binary.BigEndian.PutUint32(synth[48:52], uint32(len(synth)))
	headed, footed := headedAPETag(t, "Title", "Coda"), apeTag(map[string]string{"Title": "Coda"})
	for _, c := range []struct {
		name          string
		audio         []byte
		declared      int    // the bytes the VBR header counts
		before, after []byte // the junk around the tag
		tag           []byte
		native        []string
	}{
		{"notags.mp3 cut by five frames, a tag, junk", cut, len(audio), nil, trailerJunk(10), headed,
			[]string{"MPEG audio", "APEv2", "trailing bytes"}},
		{"notags.mp3 cut by five frames, junk, a footer-only tag, junk", cut, len(audio), trailerJunk(10), trailerJunk(10), footed,
			[]string{"MPEG audio", "trailing bytes", "APEv2", "trailing bytes"}},
		{"200 frames of 300, a tag, junk", synth[:200*417], len(synth), nil, trailerJunk(10), headed,
			[]string{"MPEG audio", "APEv2", "trailing bytes"}},
		{"200 frames of 300, junk, a footer-only tag, junk", synth[:200*417], len(synth), trailerJunk(300), trailerJunk(10), footed,
			[]string{"MPEG audio", "trailing bytes", "APEv2", "trailing bytes"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := slices.Concat(c.audio, c.before, c.tag, c.after)
			doc := mustParseBytes(t, data)
			var native []string
			for _, e := range doc.Native().Describe() {
				if !strings.HasPrefix(e.Kind, " ") {
					native = append(native, e.Kind)
				}
			}
			if !slices.Equal(native, c.native) || nativeSize(doc, "MPEG audio") != len(c.audio) {
				t.Errorf("native view %q with %d bytes of MPEG audio; want %q with %d", native, nativeSize(doc, "MPEG audio"), c.native, len(c.audio))
			}
			if !hasWarning(doc, wl.WarnLegacyAPE) || !hasFamily(doc, wl.FamilyAPEv2, tag.Title) {
				t.Errorf("the APE tag was not read: warnings %v", doc.Warnings())
			}
			truncated, ok := docWarning(doc, wl.WarnTruncatedAudio)
			if want := fmt.Sprintf("by %d byte(s),", c.declared-len(c.audio)); !ok || !strings.Contains(truncated.Message, want) {
				t.Errorf("truncated-audio warning %q (present %v), want it %s", truncated.Message, ok, want)
			}
			essence := essenceOf(t, c.audio)
			if !essenceOf(t, data).Equal(essence) {
				t.Error("the digest is not the frames'")
			}
			plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip), wl.WithVerifyEssence())
			if err != nil {
				t.Fatal(err)
			}
			var w writerTo
			if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data))); err != nil {
				t.Fatalf("strip under the essence check: %v", err)
			}
			if !bytes.Equal(w.b, slices.Concat(c.audio, c.before, c.after)) {
				t.Errorf("the strip left %d bytes with %d APETAGEX markers; want the frames and the junk alone", len(w.b), bytes.Count(w.b, []byte("APETAGEX")))
			}
			re := mustParseBytes(t, w.b)
			if got, _ := docWarning(re, wl.WarnTruncatedAudio); got.Message != truncated.Message ||
				!reflect.DeepEqual(re.Properties(), doc.Properties()) || !essenceOf(t, w.b).Equal(essence) {
				t.Errorf("after the strip: %q, %+v; want %q, %+v and the same digest", got.Message, re.Properties(), truncated.Message, doc.Properties())
			}
		})
	}
}

// An APE tag that holds together wins over the audio the frames or the VBR header's byte count claim
// past its start: the audio ends at the tag, which is read and removed by a strip. The claim is
// reported truncated by the bytes it runs past the audio unless the stream goes on after the tag:
// junk after the tag at least as long as a cut frame's rest completes that frame once a strip cuts
// the tag out, so the junk up to its end reads as the stream going on, and a byte count is then
// truncated only by what it claims past that. A byte count ending in that junk, where no frame ends,
// places nothing, so the junk stays junk. A cut frame that keeps its header and nothing that
// completes it leaves the stripped file's end unknown, so the junk after the tag stays in the
// essence, before the strip as after it.
func TestMP3TagTheClaimRunsIntoEndsTheAudio(t *testing.T) {
	audio := mp3Audio(t)
	starts := mpegFrameStarts(audio)
	cut := starts[len(starts)-5] // five frames from the end
	synth := mp3VBRStream(300, 300)
	synth[43] = 3 // frame count and byte count present
	binary.BigEndian.PutUint32(synth[48:52], uint32(len(synth)))
	short := headedAPETag(t, "Title", "Coda")                                      // shorter than the cut frame's rest
	big := headedAPETag(t, "Title", "Coda", "Comment", strings.Repeat("c", 600))   // longer than the cut frame's rest
	huge := headedAPETag(t, "Title", "Coda", "Comment", strings.Repeat("c", 2000)) // longer than the cut
	tagged := []string{"MPEG audio", "APEv2", "trailing bytes"}
	goesOn := []string{"MPEG audio", "APEv2", "MPEG audio"}
	for _, c := range []struct {
		name    string
		audio   []byte // the stream up to the tag
		tag     []byte
		junk    int // after the tag
		native  []string
		kept    bool   // the junk after the tag stays in the essence
		warning string // how the truncated-audio warning begins, empty for none
	}{
		{"a cut keeping the frame's header, then a tag", synth[:200*417+300], big, 10, tagged, true, "the VBR header, by 41400 byte(s),"},
		{"a cut keeping the frame's header, then a tag shorter than its rest", synth[:200*417+300], short, 10, tagged, true, "the VBR header, by 41400 byte(s),"},
		{"a cut keeping the frame's header, then a tag and junk completing the frame", synth[:200*417+300], big, 1000,
			append(goesOn, "trailing bytes"), true, "the VBR header, by 41283 byte(s),"},
		{"notags.mp3 cut inside a frame, then a tag", audio[:cut+100], big, 10, tagged, true, fmt.Sprintf("the VBR header, by %d byte(s),", len(audio)-cut-100)},
		{"a byte count ending inside a longer tag", audio[:cut], huge, 10, tagged, false, fmt.Sprintf("the VBR header, by %d byte(s),", len(audio)-cut)},
		{"a byte count ending in the junk after a longer tag", audio[:cut], huge, 2000, tagged, false, ""},
		{"a stream with no VBR header cut keeping the frame's header, then a tag", mp3Frames(300)[:200*417+300], big, 10, tagged, true,
			"the last MPEG frame, by 117 byte(s),"},
		{"a stream with no VBR header cut keeping the frame's header, then a tag shorter than its rest", mp3Frames(300)[:200*417+300], short, 10,
			tagged, true, "the last MPEG frame, by 117 byte(s),"},
		{"a stream with no VBR header cut keeping the frame's header, then a tag and junk completing the frame", mp3Frames(300)[:200*417+300], big, 117,
			goesOn, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := slices.Concat(c.audio, c.tag, trailerJunk(c.junk))
			doc := mustParseBytes(t, data)
			var native []string
			for _, e := range doc.Native().Describe() {
				if !strings.HasPrefix(e.Kind, " ") {
					native = append(native, e.Kind)
				}
			}
			if !slices.Equal(native, c.native) || nativeSize(doc, "MPEG audio") != len(c.audio) {
				t.Errorf("native view %q with %d bytes of MPEG audio; want %q with %d", native, nativeSize(doc, "MPEG audio"), c.native, len(c.audio))
			}
			if !hasWarning(doc, wl.WarnLegacyAPE) || !hasFamily(doc, wl.FamilyAPEv2, tag.Title) {
				t.Errorf("the APE tag was not read: warnings %v", doc.Warnings())
			}
			if tw, ok := docWarning(doc, wl.WarnTruncatedAudio); ok != (c.warning != "") || !strings.HasPrefix(tw.Message, c.warning) {
				t.Errorf("truncated-audio warning %q (present %v), want it to begin %q (none when empty)", tw.Message, ok, c.warning)
			}
			stripped := slices.Concat(c.audio, trailerJunk(c.junk))
			essence := essenceOf(t, c.audio)
			if c.kept {
				essence = essenceOf(t, stripped)
			}
			if !essenceOf(t, data).Equal(essence) {
				t.Error("the digest is not the one the audio and the junk it keeps give")
			}
			plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip), wl.WithVerifyEssence())
			if err != nil {
				t.Fatal(err)
			}
			var w writerTo
			if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data))); err != nil {
				t.Fatalf("strip under the essence check: %v", err)
			}
			if !bytes.Equal(w.b, stripped) || !essenceOf(t, w.b).Equal(essence) {
				t.Errorf("the strip left %d bytes with %d APETAGEX markers, or another digest; want the stream and the junk alone",
					len(w.b), bytes.Count(w.b, []byte("APETAGEX")))
			}
		})
	}
}

// A VBR byte count that a tag found before the tags at the end interrupts, no frames following the
// tag, stays truncated by what it claims past the audio as a strip leaves the file, whatever the
// junk after the tag adds to the audio: a frame it completes, or fewer bytes than a header, which
// stay with the frame they follow. The amount reads the same before and after the strip.
func TestMP3TruncationWithNoFramesAfterTheTag(t *testing.T) {
	audio := mp3Audio(t)
	starts := mpegFrameStarts(audio)
	five, one := starts[len(starts)-5], starts[len(starts)-1]
	short := headedAPETag(t, "Title", "Coda")
	big := headedAPETag(t, "Title", "Coda", "Comment", strings.Repeat("c", 600))
	huge := headedAPETag(t, "Title", "Coda", "Comment", strings.Repeat("c", 2000))
	footed := apeTag(map[string]string{"Title": "Coda", "Comment": strings.Repeat("c", 2000)})
	v1 := testfile.ID3v1("V1", "", "", "", "", 255)
	synth := mp3VBRStream(300, 300)
	synth[43] = 3                                             // frame count and byte count present
	binary.BigEndian.PutUint32(synth[48:52], uint32(203*417)) // two frames past the one the tag cuts
	whole := mp3VBRStream(300, 300)
	whole[43] = 3
	binary.BigEndian.PutUint32(whole[48:52], uint32(len(whole))) // the whole stream
	type shape struct {
		name   string
		audio  []byte // the stream up to the tag
		tag    []byte
		junk   int
		tail   []byte // tags peeled from the end
		amount int    // the bytes the count claims past the audio
	}
	shapes := []shape{{"one frame cut, then a tag and a junk byte", audio[:one], big, 1, nil, len(audio) - one - 1}}
	for j := 1; j <= 3; j++ {
		for _, tg := range []struct {
			name string
			b    []byte
		}{{"a tag", huge}, {"a footer-only tag", footed}} {
			for _, tl := range []struct {
				name string
				b    []byte
			}{{"", nil}, {", an ID3v1 tag after", v1}} {
				shapes = append(shapes, shape{fmt.Sprintf("five frames cut, then %s and %d junk byte(s)%s", tg.name, j, tl.name),
					audio[:five], tg.b, j, tl.b, len(audio) - five - j})
			}
		}
	}
	for _, j := range []int{109, 500, 944} { // the frame the tag cuts lacks 109 bytes
		shapes = append(shapes, shape{fmt.Sprintf("cut inside a frame, then a tag and %d junk bytes completing it", j),
			audio[:five+100], huge, j, nil, len(audio) - starts[len(starts)-4]})
	}
	for _, j := range []int{117, 500, 835} { // the frame the tag cuts lacks 117 bytes
		shapes = append(shapes, shape{fmt.Sprintf("a count of 203 frames cut inside the 201st, then a tag and %d junk bytes", j),
			synth[:200*417+300], huge, j, nil, 2 * 417})
	}
	// A count that runs past the tags, and one whose junk completes the frame the tag cuts and adds a
	// byte, which stays with that frame.
	shapes = append(shapes, shape{"five frames cut, then a short tag and 2 junk bytes", audio[:five], short, 2, nil, len(audio) - five - 2},
		shape{"a count of the whole stream cut inside the 201st frame, then a tag and 118 junk bytes", whole[:200*417+300], big, 118, nil,
			len(whole) - 201*417 - 1})
	for _, c := range shapes {
		t.Run(c.name, func(t *testing.T) {
			data := slices.Concat(c.audio, c.tag, trailerJunk(c.junk), c.tail)
			doc := mustParseBytes(t, data)
			want := fmt.Sprintf("the VBR header, by %d byte(s),", c.amount)
			if tw, ok := docWarning(doc, wl.WarnTruncatedAudio); !ok || !strings.HasPrefix(tw.Message, want) {
				t.Errorf("truncated-audio warning %q (present %v), want it to begin %q", tw.Message, ok, want)
			}
			plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip), wl.WithVerifyEssence())
			if err != nil {
				t.Fatal(err)
			}
			var w writerTo
			if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data))); err != nil {
				t.Fatalf("strip under the essence check: %v", err)
			}
			re := mustParseBytes(t, w.b)
			if tw, ok := docWarning(re, wl.WarnTruncatedAudio); !ok || !strings.HasPrefix(tw.Message, want) || bytes.Contains(w.b, []byte("APETAGEX")) {
				t.Errorf("after the strip: truncated-audio warning %q (present %v), want it to begin %q, the tag gone", tw.Message, ok, want)
			}
			if !essenceOf(t, w.b).Equal(essenceOf(t, data)) {
				t.Error("the strip changed the digest")
			}
		})
	}
}

// A VBR header's byte count that covers an APE tag between two runs of frames counts the tag's bytes
// as stream when the frame chain, the tag cut out, goes on from the first run into the second and to
// the end of the file: the count the tag interrupts is then no truncation, and only a count that runs
// past the file is, by the bytes it runs past it. The tag is read and removed by a strip. A second run
// whose last frame is cut leaves the end unknown without the tag, and the count is then measured to
// the tag.
func TestMP3ByteCountOverATagBetweenRuns(t *testing.T) {
	big := headedAPETag(t, "Title", "Coda", "Comment", strings.Repeat("c", 600))
	runs := mp3VBRStream(80, 80)
	runs[43] = 3 // frame count and byte count present
	for _, c := range []struct {
		name    string
		after   int    // the frames of the second run
		count   int    // the bytes the header counts
		warning string // how the truncated-audio warning begins, empty for none
	}{
		{"a count covering both runs and the tag", 40, 80*417 + len(big), ""},
		{"a count covering both runs alone", 40, 80 * 417, ""},
		{"a count one frame past the file", 40, 81*417 + len(big), "the VBR header, by 417 byte(s),"},
		{"a count covering a run, the tag and the one frame after it", 1, 41*417 + len(big), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := slices.Concat(runs[:40*417], big, runs[40*417:(40+c.after)*417])
			binary.BigEndian.PutUint32(data[48:52], uint32(c.count))
			doc := mustParseBytes(t, data)
			var native []string
			for _, e := range doc.Native().Describe() {
				if !strings.HasPrefix(e.Kind, " ") {
					native = append(native, e.Kind)
				}
			}
			if want := []string{"MPEG audio", "APEv2", "MPEG audio"}; !slices.Equal(native, want) || !hasFamily(doc, wl.FamilyAPEv2, tag.Title) {
				t.Errorf("native view %q, APE title read %v; want %q and the tag read", native, hasFamily(doc, wl.FamilyAPEv2, tag.Title), want)
			}
			if tw, ok := docWarning(doc, wl.WarnTruncatedAudio); ok != (c.warning != "") || !strings.HasPrefix(tw.Message, c.warning) {
				t.Errorf("truncated-audio warning %q (present %v), want it to begin %q (none when empty)", tw.Message, ok, c.warning)
			}
			stripped := slices.Concat(data[:40*417], data[40*417+len(big):])
			plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip), wl.WithVerifyEssence())
			if err != nil {
				t.Fatal(err)
			}
			var w writerTo
			if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data))); err != nil {
				t.Fatalf("strip under the essence check: %v", err)
			}
			if !bytes.Equal(w.b, stripped) || !essenceOf(t, data).Equal(essenceOf(t, stripped)) {
				t.Errorf("the strip left %d bytes, or another digest; want the two runs alone", len(w.b))
			}
		})
	}
}

// mpegFrameStarts lists where the frames of an MPEG-1 Layer III stream start, from its first byte to
// the first position that opens no frame.
func mpegFrameStarts(b []byte) []int {
	kbps := [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	rates := [4]int{44100, 48000, 32000}
	var starts []int
	for p := 0; p+4 <= len(b) && b[p] == 0xFF && b[p+1]&0xFE == 0xFA; {
		n := 0
		if rate := rates[b[p+2]>>2&3]; rate > 0 {
			n = 144*kbps[b[p+2]>>4]*1000/rate + int(b[p+2]>>1&1)
		}
		if n == 0 {
			break
		}
		starts = append(starts, p)
		p += n
	}
	return starts
}

// nativeSize is the size of the first native-view entry of the given kind, or -1.
func nativeSize(doc *wl.Document, kind string) int {
	for _, e := range doc.Native().Describe() {
		if e.Kind == kind {
			return e.Size
		}
	}
	return -1
}

// mp3Frames is n MPEG-1 Layer III frames at 128 kbps and 44.1 kHz with no VBR header, their bodies
// zero.
func mp3Frames(n int) []byte {
	return bytes.Repeat(slices.Concat([]byte{0xFF, 0xFB, 0x90, 0x00}, make([]byte, 413)), n)
}

// byteCounter counts the bytes a parse reads from its source.
type byteCounter struct {
	wl.ReaderAtSized
	n int64
}

func (c *byteCounter) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.ReaderAtSized.ReadAt(p, off)
	c.n += int64(n)
	return n, err
}

// An MP3 without a VBR header whose last frame the tags cut keeps that frame as audio, and the
// parse reads a bounded number of bytes, however long the stream: no walk of the frames.
func TestMP3CutLastFrameStaysAudio(t *testing.T) {
	stream := slices.Concat(mp3Frames(10000), mp3Frames(1)[:200])
	src := &byteCounter{ReaderAtSized: wl.BytesSource(slices.Concat(stream, testfile.ID3v1("V1", "", "", "", "", 255)))}
	doc, err := wl.Parse(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if hasWarning(doc, wl.WarnTrailingBytes) || !hasWarning(doc, wl.WarnTrailingID3v1) {
		t.Errorf("warnings %v; want the ID3v1 tag and no trailing bytes", doc.Warnings())
	}
	if bound := int64(4 * 64 << 10); src.n > bound {
		t.Errorf("read %d bytes of a %d-byte file, want at most %d", src.n, src.Size(), bound)
	}
	got, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(src.ReaderAtSized))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(essenceOf(t, stream)) {
		t.Error("the digest is not the stream's, cut frame included")
	}
}

// Junk after an APE tag behind a stream whose end is unknown stays in the essence, so stripping the
// tags, which leaves the junk right after the frames, keeps the digest.
func TestMP3JunkAfterATagWithAnUnknownEnd(t *testing.T) {
	stream := slices.Concat(mp3Frames(40), trailerJunk(70000)) // no frame chain before the tags
	src := slices.Concat(stream, apeTag(map[string]string{"Title": "APE Title"}), trailerJunk(300),
		testfile.ID3v1("V1", "", "", "", "", 255))
	doc := mustParseBytes(t, src)
	w, ok := docWarning(doc, wl.WarnTrailingBytes)
	if !ok || !strings.Contains(w.Message, "stay in the audio essence") || !hasWarning(doc, wl.WarnLegacyAPE) {
		t.Errorf("warnings %v; want the APE tag and the junk after it, kept in the essence", doc.Warnings())
	}
	essence := essenceOf(t, slices.Concat(stream, trailerJunk(300)))
	if !essenceOf(t, src).Equal(essence) {
		t.Error("the digest is not the frames' and the junk's")
	}
	plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	stripped := applyToBytes(t, src, plan)
	if !bytes.Equal(stripped, slices.Concat(stream, trailerJunk(300))) || !essenceOf(t, stripped).Equal(essence) {
		t.Error("the strip did not leave the frames and the junk, or changed the digest")
	}
}

// An MP3 selected by a leading ID3v2 tag but carrying non-MPEG bytes after it reports
// WarnNoAudioFrames once: the non-empty essence range triggers the MP3 warning path, and the
// root zero-essence path does not fire.
func TestMP3NonAudioWarnsNoAudio(t *testing.T) {
	t.Parallel()
	data := append(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "x")), []byte("this is text, not audio\n")...)
	path := writeTempFile(t, "fake.mp3", data)
	doc := mustParseFile(t, path)
	if doc.Format() != wl.FormatMP3 {
		t.Fatalf("format = %v, want MP3 (the leading ID3 tag must route it to the MP3 codec)", doc.Format())
	}
	if !hasWarning(doc, wl.WarnNoAudioFrames) {
		t.Errorf("a non-MPEG .mp3 should warn no-audio; got %v", doc.Warnings())
	}
	n := 0
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnNoAudioFrames {
			n++
		}
	}
	if n != 1 {
		t.Errorf("no-audio warnings = %d, want exactly 1 (the mp3 and root paths must not both fire); got %v", n, doc.Warnings())
	}
}

// An MP3 whose ID3v2 tag holds a frame with a lowercase ID reads the frames after it and reports the
// frame; an edit keeps it byte for byte, and ffmpeg still reads all four frames after the edit.
func TestMP3NonStandardFrameIDKeepsLaterFrames(t *testing.T) {
	low := testfile.ID3Frame(3, "Tlow", []byte("\x00odd"))
	src := slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Title"), low,
		testfile.TextFrame(3, "TPE1", "Artist"), testfile.TextFrame(3, "TALB", "Album")), mp3Audio(t))
	doc := mustParseBytes(t, src)
	if f := doc.Fields(); f.Title != "Title" || !slices.Equal(f.Artists, []string{"Artist"}) || f.Album != "Album" {
		t.Errorf("fields %+v, want the three standard frames", f)
	}
	if !slices.ContainsFunc(doc.Warnings(), func(w wl.Warning) bool {
		return w.Code == wl.WarnMalformedTagEntry && strings.Contains(w.Message, "Tlow")
	}) {
		t.Errorf("warnings %v, want malformed-tag-entry naming Tlow", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Comment, "Note").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, low) {
		t.Error("the edit did not keep the Tlow frame byte for byte")
	}
	if f := mustParseBytes(t, out).Fields(); !slices.Equal(f.Artists, []string{"Artist"}) || f.Album != "Album" {
		t.Errorf("after the edit fields %+v, want ARTIST and ALBUM kept", f)
	}
	requireTool(t, "ffprobe")
	got := ffprobeFormatTags(t, writeTempFile(t, "edited.mp3", out))
	for k, want := range map[string]string{"title": "Title", "artist": "Artist", "album": "Album", "Tlow": "odd"} {
		if got[k] != want {
			t.Errorf("ffprobe %s = %q, want %q (tags %v)", k, got[k], want, got)
		}
	}
}

// A frame ID with a control byte ends the frame walk: the frames after it are reported unreadable,
// not taken for padding, and an edit says it does not carry them.
func TestMP3ControlByteFrameIDIsAnUnreadableTail(t *testing.T) {
	src := slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Title"),
		testfile.TextFrame(3, "T\x01LW", "junk"), testfile.TextFrame(3, "TPE1", "Artist")), mp3Audio(t))
	doc := mustParseBytes(t, src)
	if !hasWarning(doc, wl.WarnMalformedTagEntry) || doc.Padding() != 0 {
		t.Errorf("warnings %v, padding %d; want malformed-tag-entry and no padding", doc.Warnings(), doc.Padding())
	}
	plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Report().Warnings, func(w wl.Warning) bool { return w.Code == wl.WarnMalformedTagEntryDropped }) {
		t.Errorf("plan warnings %v, want malformed-tag-entry-dropped", plan.Report().Warnings)
	}
}

// png16 is a real 16x16 PNG that ffmpeg can decode.
func png16(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A chapter's sub-frames are sized as the writer sized the rest: plain in a tag whose frame sizes are
// plain, so a long chapter title reads whole, and they are sized the same way in a tag otherwise
// sync-safe. A rewrite writes them sync-safe, as it writes the frames around them.
func TestMP3PlainSizeChapterTitleReadsWhole(t *testing.T) {
	title := strings.Repeat("T", 200)
	plain := func(id string, body []byte) []byte {
		return slices.Concat([]byte(id), testfile.BE32(len(body)), []byte{0, 0}, body)
	}
	unused := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	syncSafe := func(id string, body []byte) []byte { return testfile.ID3Frame(4, id, body) }
	chap := func(frame, sub func(string, []byte) []byte) []byte {
		return frame("CHAP", slices.Concat([]byte("ch0\x00"), testfile.BE32(0), testfile.BE32(1000), unused, unused,
			sub("TIT2", slices.Concat([]byte{3}, []byte(title)))))
	}
	for name, frames := range map[string][][]byte{
		"a plain-size tag": {plain("TIT2", []byte("\x03Plain")), chap(plain, plain), plain("CTOC", []byte("toc\x00\x03\x01ch0\x00"))},
		"a sync-safe tag":  {syncSafe("TIT2", []byte("\x03Safe")), chap(syncSafe, plain), syncSafe("CTOC", []byte("toc\x00\x03\x01ch0\x00"))},
		"a conformant tag": {syncSafe("TIT2", []byte("\x03Safe")), chap(syncSafe, syncSafe), syncSafe("CTOC", []byte("toc\x00\x03\x01ch0\x00"))},
		// The title's sync-safe size read plain (329) lands on a frame ID inside the TXXX behind it.
		"a conformant tag with a decoy": {syncSafe("TIT2", []byte("\x03Safe")), syncSafe("CHAP", slices.Concat([]byte("ch0\x00"),
			testfile.BE32(0), testfile.BE32(1000), unused, unused, syncSafe("TIT2", slices.Concat([]byte{3}, []byte(title))),
			syncSafe("TXXX", slices.Concat([]byte("\x03x\x00"), bytes.Repeat([]byte("v"), 115), []byte("TIT3\x00\x00\x00\x05\x00\x00"),
				bytes.Repeat([]byte("w"), 40))))), syncSafe("CTOC", []byte("toc\x00\x03\x01ch0\x00"))},
	} {
		t.Run(name, func(t *testing.T) {
			src := slices.Concat(testfile.ID3v2(4, append(frames, make([]byte, 64))...), mp3Audio(t))
			doc := mustParseBytes(t, src)
			if chs := doc.Chapters(); len(chs) != 1 || chs[0].Title != title {
				t.Fatalf("chapters %+v, want one titled with all 200 characters", chs)
			}
			plan, err := doc.Edit().Set(tag.Album, "Album").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, src, plan)
			if chs := mustParseBytes(t, out).Chapters(); len(chs) != 1 || chs[0].Title != title {
				t.Errorf("after the edit chapters %+v, want the title kept whole", chs)
			}
			if !bytes.Contains(out, []byte("TIT2\x00\x00\x01\x49")) || bytes.Contains(out, []byte("TIT2\x00\x00\x00\xC9")) {
				t.Errorf("the rewritten chapter's title sub-frame is not sized sync-safe")
			}
		})
	}
}

// An MP3 whose ID3v2.4 frame sizes are plain integers, as iTunes writes them, reads every frame and
// its cover with only a native-view note, and an unrelated edit keeps the cover, which ffmpeg then
// decodes.
func TestMP3PlainSizeV24TagKeepsTheCover(t *testing.T) {
	img := png16(t)
	head := slices.Concat([]byte{0}, []byte("image/png\x00"), []byte{3})
	apic := slices.Concat(head, bytes.Repeat([]byte("d"), 201-len(head)-1-len(img)), []byte{0}, img)
	plainAPIC := slices.Concat([]byte("APIC"), testfile.BE32(len(apic)), []byte{0, 0}, apic)
	src := slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Title"), plainAPIC,
		testfile.TextFrame(4, "TPE1", "Artist"), make([]byte, 64)), mp3Audio(t))
	doc := mustParseBytes(t, src)
	if pics := doc.Pictures(); len(pics) != 1 || !bytes.Equal(pics[0].Data, img) || doc.Fields().Title != "Title" ||
		!slices.Equal(doc.Fields().Artists, []string{"Artist"}) || doc.Padding() != 64 {
		t.Errorf("pictures %d, fields %+v, padding %d; want the cover, both frames and 64 bytes of padding",
			len(doc.Pictures()), doc.Fields(), doc.Padding())
	}
	if hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("warnings %v; plain sizes are a native note, not a read warning", doc.Warnings())
	}
	if note := nativeNote(doc, "ID3v2.4"); !strings.Contains(note, "non-sync-safe frame sizes") {
		t.Errorf("native note %q, want it to name the plain frame sizes", note)
	}
	plan, err := doc.Edit().Set(tag.Album, "Album").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	re := mustParseBytes(t, out)
	if pics := re.Pictures(); len(pics) != 1 || !bytes.Equal(pics[0].Data, img) {
		t.Fatalf("after the edit pictures %d, want the cover kept", len(pics))
	}
	if note := nativeNote(re, "ID3v2.4"); strings.Contains(note, "non-sync-safe") {
		t.Errorf("the rewritten tag still notes plain sizes: %q", note)
	}
	requireTool(t, "ffprobe")
	width, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width",
		"-of", "csv=p=0", writeTempFile(t, "edited.mp3", out)).Output()
	if err != nil || strings.TrimSpace(string(width)) != "16" {
		t.Errorf("ffprobe cover width %q (%v), want 16", width, err)
	}
}

// nativeNote is the note of the first native-view entry of the given kind.
func nativeNote(doc *wl.Document, kind string) string {
	for _, e := range doc.Native().Describe() {
		if e.Kind == kind {
			return e.Note
		}
	}
	return ""
}

// Custom keys spelled like ID3 frame IDs are stored as TXXX frames and read back as themselves; the
// edit leaves the file's own frames alone. ffmpeg reads the TXXX descriptions through its ID3 name
// table, so TCON reaches it as the genre and TBPM under its own name.
func TestMP3FrameIDSpelledCustomKeysRoundTrip(t *testing.T) {
	src := slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Title")), mp3Audio(t))
	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().Set("TCON", "Rock").Set("TBPM", "128").Set("TIT2", "Custom").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	re := mustParseBytes(t, out)
	for key, want := range map[tag.Key]string{"TCON": "Rock", "TBPM": "128", "TIT2": "Custom", tag.Title: "Title"} {
		if v, _ := re.Get(key); !slices.Equal(v, []string{want}) {
			t.Errorf("%s = %v, want [%s]", key, v, want)
		}
	}
	if re.Fields().BPM != "" || len(re.Fields().Genres) != 0 {
		t.Errorf("BPM %q, genres %v; the custom keys must not read back as the canonical ones", re.Fields().BPM, re.Fields().Genres)
	}
	requireTool(t, "ffprobe")
	got := ffprobeFormatTags(t, writeTempFile(t, "custom.mp3", out))
	if got["genre"] != "Rock" || got["TBPM"] != "128" {
		t.Errorf("ffprobe tags %v, want genre=Rock and TBPM=128", got)
	}
}

// A second ID3v2 tag chained behind an MP3's front tag is a duplicate: the first wins and the second
// is reported, kept out of the essence and not counted as leading bytes. An edit drops it, warning
// that it held a value no other tag does; the output reads as the edit says, with the clean digest.
func TestMP3DuplicateLeadingTag(t *testing.T) {
	audio := mp3Audio(t)
	first := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "First"))
	second := testfile.ID3v2(4, testfile.TextFrame(4, "TALB", "Second Album"))
	src := slices.Concat(first, second, audio)
	doc := mustParseBytes(t, src)
	if !hasWarning(doc, wl.WarnDuplicateTagBlock) || hasWarning(doc, wl.WarnLeadingBytes) {
		t.Errorf("warnings %v, want duplicate-tag-block and no leading-bytes", doc.Warnings())
	}
	if f := doc.Fields(); f.Title != "First" || f.Album != "" {
		t.Errorf("title %q, album %q; want the first tag's alone", f.Title, f.Album)
	}
	if got := nativeSize(doc, "MPEG audio"); got != len(audio) || nativeNote(doc, "ID3v2.4") != "duplicate, dropped on rewrite" {
		t.Errorf("MPEG audio %d bytes, duplicate note %q; want %d and the duplicate marked", got, nativeNote(doc, "ID3v2.4"), len(audio))
	}
	clean := essenceOf(t, audio)
	if !essenceOf(t, src).Equal(clean) {
		t.Error("the digest is not the clean file's")
	}
	plan, err := doc.Edit().Clear(tag.Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Report().Warnings, func(w wl.Warning) bool {
		return w.Code == wl.WarnDuplicateTagBlockDropped && slices.Equal(w.Keys, []tag.Key{tag.Album})
	}) {
		t.Errorf("plan warnings %v, want duplicate-tag-block-dropped naming ALBUM", plan.Report().Warnings)
	}
	if !slices.Contains(plan.Report().Operations, "duplicate ID3v2 drop") {
		t.Errorf("operations %v, want the duplicate drop", plan.Report().Operations)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Equal(out, audio) {
		t.Errorf("output is %d bytes, want the %d bytes of the bare stream", len(out), len(audio))
	}
	re := mustParseBytes(t, out)
	if hasWarning(re, wl.WarnDuplicateTagBlock) || !essenceOf(t, out).Equal(clean) {
		t.Errorf("after the edit warnings %v; want no duplicate and the clean digest", re.Warnings())
	}
}

// Editing one half of a number pair re-renders the frame that holds both halves, so a total kept
// in a TXXX frame (ffmpeg passes a FLAC's TOTALTRACKS through as one) is not left beside it as a
// second copy, and neither is a credit an involved-people edit re-renders; a total the re-rendered
// frame cannot hold, a second and different one, stays where it is.
func TestMP3NumberEditKeepsOneTotal(t *testing.T) {
	trck := func(v string) []byte { return testfile.TextFrame(3, "TRCK", v) }
	for _, c := range []struct {
		name   string
		frames [][]byte
		key    tag.Key
		value  string
		check  tag.Key
		want   []string
	}{
		{"TOTALTRACKS", [][]byte{trck("3"), txxxFrame(3, "TOTALTRACKS", "12")}, tag.TrackNumber, "4", tag.TrackTotal, []string{"12"}},
		{"TRACKTOTAL", [][]byte{trck("3"), txxxFrame(3, "TRACKTOTAL", "12")}, tag.TrackNumber, "4", tag.TrackTotal, []string{"12"}},
		{"TOTALDISCS", [][]byte{testfile.TextFrame(3, "TPOS", "1"), txxxFrame(3, "TOTALDISCS", "2")}, tag.DiscNumber, "2", tag.DiscTotal, []string{"2"}},
		{"a total ahead of the number", [][]byte{txxxFrame(3, "TOTALTRACKS", "12"), trck("3")}, tag.TrackNumber, "4", tag.TrackTotal, []string{"12"}},
		{"a credit", [][]byte{testfile.ID3Frame(3, "IPLS", []byte("\x00producer\x00Pro")), txxxFrame(3, "ENGINEER", "Eng")}, tag.Producer, "Other", tag.Engineer, []string{"Eng"}},
		{"a conflicting total", [][]byte{trck("3/10"), txxxFrame(3, "TOTALTRACKS", "12")}, tag.TrackNumber, "4", tag.TrackTotal, []string{"10", "12"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := slices.Concat(testfile.ID3v2(3, c.frames...), mp3Audio(t))
			plan, err := mustParseBytes(t, src).Edit().Set(c.key, c.value).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			after := mustParseBytes(t, applyPlan(t, plan, src))
			if got, _ := after.Tags().Get(c.check); !slices.Equal(got, c.want) {
				t.Errorf("%s after the edit = %q, want %q", c.check, got, c.want)
			}
			if got, _ := after.Tags().Get(c.key); !slices.Equal(got, []string{c.value}) {
				t.Errorf("%s after the edit = %q, want [%q]", c.key, got, c.value)
			}
		})
	}
}

// TXXX descriptions spelled as aliases read as their canonical keys, as ffmpeg leaves a FLAC's own
// field names: TOTALTRACKS is the track total, which a clear removes and a set keeps in one home,
// TXXX:USLT is the lyrics, and ALBUM ARTIST the album artist, whose edits reach every home it has.
func TestMP3TXXXAliasSpellingsFold(t *testing.T) {
	audio := mp3Audio(t)
	src := slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TRCK", "3"), txxxFrame(4, "TOTALTRACKS", "12"),
		txxxFrame(4, "USLT", "la la"), testfile.TextFrame(4, "TPE2", "Real AA"), txxxFrame(4, "ALBUM ARTIST", "Alias AA")), audio)
	doc := mustParseBytes(t, src)
	if f := doc.Fields(); f.TrackNumber != 3 || f.TrackTotal != 12 || f.Lyrics != "la la" {
		t.Errorf("track %d/%d, lyrics %q; want 3/12 and the TXXX:USLT lyrics", f.TrackNumber, f.TrackTotal, f.Lyrics)
	}
	if v, _ := doc.Get(tag.AlbumArtist); !slices.Equal(v, []string{"Real AA", "Alias AA"}) {
		t.Errorf("ALBUMARTIST = %v, want both spellings' values", v)
	}
	for _, c := range []struct {
		name  string
		edit  func(*wl.Editor) *wl.Editor
		check func(t *testing.T, out []byte, re *wl.Document)
	}{
		{"clear the total", func(e *wl.Editor) *wl.Editor { return e.Clear(tag.TrackTotal) }, func(t *testing.T, out []byte, re *wl.Document) {
			if bytes.Contains(out, []byte("TOTALTRACKS")) || re.Fields().TrackTotal != 0 || re.Fields().TrackNumber != 3 {
				t.Errorf("total %d, track %d, TOTALTRACKS kept %v; want the total gone and the track kept",
					re.Fields().TrackTotal, re.Fields().TrackNumber, bytes.Contains(out, []byte("TOTALTRACKS")))
			}
		}},
		{"set the total", func(e *wl.Editor) *wl.Editor { return e.Set(tag.TrackTotal, "20") }, func(t *testing.T, out []byte, re *wl.Document) {
			if v, _ := re.Get(tag.TrackTotal); !slices.Equal(v, []string{"20"}) || bytes.Contains(out, []byte("TOTALTRACKS")) {
				t.Errorf("TRACKTOTAL = %v, want [20] in one home", v)
			}
		}},
		{"clear the album artist", func(e *wl.Editor) *wl.Editor { return e.Clear("ALBUM ARTIST") }, func(t *testing.T, out []byte, re *wl.Document) {
			if _, has := re.Get(tag.AlbumArtist); has || bytes.Contains(out, []byte("ALBUM ARTIST")) || bytes.Contains(out, []byte("TPE2")) {
				t.Errorf("ALBUMARTIST still has a home after the clear")
			}
		}},
		{"set the album artist", func(e *wl.Editor) *wl.Editor { return e.Set("ALBUM ARTIST", "New AA") }, func(t *testing.T, out []byte, re *wl.Document) {
			if v, _ := re.Get(tag.AlbumArtist); !slices.Equal(v, []string{"New AA"}) || bytes.Contains(out, []byte("ALBUM ARTIST")) {
				t.Errorf("ALBUMARTIST = %v, want [New AA] in TPE2 alone", v)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan, err := c.edit(doc.Edit()).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, src, plan)
			c.check(t, out, mustParseBytes(t, out))
		})
	}
}

// What ffmpeg makes of a FLAC reads back: field names it passes through as TXXX descriptions read as
// the canonical keys they spell, and its TXXX:USLT for the lyrics as LYRICS.
func TestMP3FFmpegTXXXSpellingsRead(t *testing.T) {
	requireTool(t, "ffmpeg")
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		meta []string
		want map[tag.Key]string
	}{
		{"aliases", []string{"TOTALTRACKS=12", "ALBUM ARTIST=Alias AA", "UNSYNCEDLYRICS=un sync"},
			map[tag.Key]string{tag.TrackTotal: "12", tag.AlbumArtist: "Alias AA", tag.Lyrics: "un sync"}},
		{"lyrics", []string{"LYRICS=la la"}, map[tag.Key]string{tag.Lyrics: "la la"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := []string{"-i", "../testdata/notags.flac"}
			for _, m := range c.meta {
				args = append(args, "-metadata", m)
			}
			flac := ffmpegEncode(t, dir+"/"+c.name+".flac", args...)
			mp3 := ffmpegEncode(t, dir+"/"+c.name+".mp3", "-i", flac, "-c:a", "libmp3lame", "-b:a", "64k")
			doc := mustParseFile(t, mp3)
			for k, want := range c.want {
				if v, _ := doc.Get(k); !slices.Equal(v, []string{want}) {
					t.Errorf("%s = %v, want [%s] (tags %v)", k, v, want, doc.Tags().Keys())
				}
			}
		})
	}
}

// An ID3v2.2 PIC link (format "-->") reads as a URL link, not a broken JPEG, and the v2.3 tag an
// unrelated edit writes keeps it a link.
func TestMP3V22PICLink(t *testing.T) {
	pic := frame22("PIC", slices.Concat([]byte{0}, []byte("-->"), []byte{3}, []byte("\x00"), []byte("http://example.com/c.jpg")))
	src := slices.Concat(testfile.ID3v2(2, textFrame22("TT2", "Linked"), pic), mp3Audio(t))
	doc := mustParseBytes(t, src)
	if pics := doc.Pictures(); len(pics) != 1 || pics[0].MIME != wl.LinkMIME || string(pics[0].Data) != "http://example.com/c.jpg" {
		t.Fatalf("pictures %+v, want the link", pics)
	}
	if hasWarning(doc, wl.WarnInvalidPicture) || slices.ContainsFunc(doc.Lint(), func(f wl.Finding) bool { return f.Code == "invalid-picture" }) {
		t.Errorf("warnings %v, lint %v; want no invalid-picture", doc.Warnings(), doc.Lint())
	}
	plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, []byte("\x00-->\x00\x03")) {
		t.Error("the rewritten APIC does not declare the --> link MIME")
	}
	if pics := mustParseBytes(t, out).Pictures(); len(pics) != 1 || pics[0].MIME != wl.LinkMIME {
		t.Errorf("after the edit pictures %+v, want the link", pics)
	}
}

// An ID3v1 tag holding what it can of the ID3v2 values, a title cut at 30 bytes and the year of a
// full date, agrees with them: no conflicting-families, and lint --fix may strip it. A different
// value still conflicts. ffmpeg's own ID3v1 writes the same cut.
func TestMP3ID3v1TruncationIsNotAConflict(t *testing.T) {
	long := "A Title Far Longer Than Thirty Characters"
	v2 := testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", long), testfile.TextFrame(4, "TDRC", "2001-05-06"))
	lintOf := func(src []byte) []wl.Finding { return mustParseBytes(t, src).Lint() }
	findings := lintOf(slices.Concat(v2, mp3Audio(t), testfile.ID3v1(long[:30], "", "", "2001", "", 255)))
	if slices.ContainsFunc(findings, func(f wl.Finding) bool { return f.Code == "conflicting-families" }) ||
		!slices.ContainsFunc(findings, func(f wl.Finding) bool { return f.Code == "trailing-id3v1" && f.Fixable }) {
		t.Errorf("findings %+v, want no conflict and a fixable trailing-id3v1", findings)
	}
	live := testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Song (Live)"))
	if !slices.ContainsFunc(lintOf(slices.Concat(live, mp3Audio(t), testfile.ID3v1("Song", "", "", "", "", 255))),
		func(f wl.Finding) bool { return f.Code == "conflicting-families" && f.Key == tag.Title }) {
		t.Error("Song against Song (Live) no longer conflicts")
	}
	requireTool(t, "ffmpeg")
	out := ffmpegEncode(t, t.TempDir()+"/v1.mp3", "-i", "../testdata/notags.flac", "-metadata", "title="+long,
		"-metadata", "date=2001-05-06", "-write_id3v1", "1", "-c:a", "libmp3lame", "-b:a", "64k")
	doc := mustParseFile(t, out)
	if !hasWarning(doc, wl.WarnTrailingID3v1) || slices.ContainsFunc(doc.Lint(), func(f wl.Finding) bool { return f.Code == "conflicting-families" }) {
		t.Errorf("ffmpeg's ID3v1: warnings %v, lint %+v; want the tag found and no conflict", doc.Warnings(), doc.Lint())
	}
}

// An MP3 whose front tag is ID3v2.5, a version no specification defines, reads: the tag is ignored
// and reported, the audio starts behind it with the clean file's digest, and an edit replaces it
// with a tag at the format's default version, saying the old one is not carried.
func TestMP3UnknownID3Version(t *testing.T) {
	audio := mp3Audio(t)
	tagBytes := testfile.ID3v2(5, testfile.TextFrame(4, "TIT2", "Future"))
	src := slices.Concat(tagBytes, audio)
	doc := mustParseBytes(t, src)
	if !hasWarning(doc, wl.WarnMalformedTagEntry) || doc.Fields().Title != "" {
		t.Errorf("warnings %v, title %q; want the tag ignored and reported", doc.Warnings(), doc.Fields().Title)
	}
	if nativeSize(doc, "ID3v2.5") != len(tagBytes) || nativeSize(doc, "MPEG audio") != len(audio) || !essenceOf(t, src).Equal(essenceOf(t, audio)) {
		t.Errorf("native %+v; want the tag's %d bytes, then the audio with the clean digest", doc.Native().Describe(), len(tagBytes))
	}
	plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("plan warnings %v, want malformed-tag-entry-dropped", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	if out[3] != 3 || mustParseBytes(t, out).Fields().Album != "X" || !bytes.HasSuffix(out, audio) {
		t.Errorf("output tag version %d; want an ID3v2.3 tag holding the album ahead of the audio", out[3])
	}
}

// An ignored front tag is no tag a rewrite updates: when it writes a tag the operations say one is
// created and the warning says the ignored tag is replaced, and when it writes none (all that held
// values was an ID3v1 tag a strip removes) the operations say the ID3v2 tag is removed and so does
// the warning. A tag declaring more bytes than the file holds is ignored the same way.
func TestMP3IgnoredFrontTagReplacedOrRemoved(t *testing.T) {
	audio := mp3Audio(t)
	pastEnd := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Cut"))
	copy(pastEnd[6:10], []byte{0x04, 0x00, 0x00, 0x00}) // 8 MiB declared
	for name, front := range map[string][]byte{
		"an unknown version": testfile.ID3v2(5, testfile.TextFrame(4, "TIT2", "Future")),
		"a tag past the end": pastEnd,
	} {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, slices.Concat(front, audio))
			plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			ops, msg := plan.Report().Operations, malformedDroppedMessage(plan)
			if !slices.Contains(ops, "ID3v2.3 tag creation") || !strings.Contains(msg, "is replaced by") {
				t.Errorf("writing a tag: operations %v, warning %q; want a tag creation and the ignored tag replaced", ops, msg)
			}
			stripped := slices.Concat(front, audio, testfile.ID3v1("V1 Title", "", "", "2001", "", 255))
			plan, err = mustParseBytes(t, stripped).Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
			if err != nil {
				t.Fatal(err)
			}
			ops, msg = plan.Report().Operations, malformedDroppedMessage(plan)
			if !slices.Contains(ops, "ID3v2 removal") || slices.ContainsFunc(ops, func(op string) bool { return strings.Contains(op, "creation") }) ||
				!strings.Contains(msg, "is removed") {
				t.Errorf("writing no tag: operations %v, warning %q; want the ID3v2 tag's removal and the ignored tag removed", ops, msg)
			}
		})
	}
	// A tag that reads no frame, only bytes it cannot read, goes whole with them when a strip leaves
	// it nothing to write.
	junk := testfile.ID3v2(3, []byte("\x01\x02\x03\x04junk"))
	plan, err := mustParseBytes(t, slices.Concat(junk, audio, testfile.ID3v1("V1 Title", "", "", "", "", 255))).Edit().
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if msg := malformedDroppedMessage(plan); !strings.Contains(msg, "are removed with the tag") {
		t.Errorf("an unreadable tag stripped: warning %q, want its bytes removed with the tag", msg)
	}
}

// malformedDroppedMessage is the message of plan's malformed-tag-entry-dropped warning, or "".
func malformedDroppedMessage(plan *wl.Plan) string {
	if w, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); ok {
		return w.Message
	}
	return ""
}

// Frames in a group or with status flags set, a read-only one included, keep those bytes through an
// unrelated edit, in ID3v2.3 and ID3v2.4, and still read; an edit of such a frame is not blocked.
func TestMP3FrameGroupAndStatusKept(t *testing.T) {
	for _, c := range []struct {
		version       byte
		group, status byte
	}{{3, 0x20, 0xE0}, {4, 0x40, 0x70}} {
		grouped := testfile.ID3FrameFlags(c.version, "TPE1", [2]byte{0, c.group}, []byte("\x07\x00Grouped"))
		flagged := testfile.ID3FrameFlags(c.version, "TALB", [2]byte{c.status, 0}, []byte("\x00Flagged"))
		src := slices.Concat(testfile.ID3v2(c.version, testfile.TextFrame(c.version, "TIT2", "Title"), grouped, flagged), mp3Audio(t))
		doc := mustParseBytes(t, src)
		plan, err := doc.Edit().Set(tag.Title, "New Title").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, src, plan)
		if !bytes.Contains(out, grouped) || !bytes.Contains(out, flagged) {
			t.Errorf("v2.%d: the edit did not keep the grouped and flagged frames byte for byte", c.version)
		}
		re := mustParseBytes(t, out)
		if f := re.Fields(); f.Title != "New Title" || !slices.Equal(f.Artists, []string{"Grouped"}) || f.Album != "Flagged" {
			t.Errorf("v2.%d: fields %+v after the edit", c.version, f)
		}
		plan, err = re.Edit().Set(tag.Album, "Edited").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if got := mustParseBytes(t, applyToBytes(t, out, plan)).Fields().Album; got != "Edited" {
			t.Errorf("v2.%d: the read-only frame's edit read back %q", c.version, got)
		}
	}
}

// A GENRE with whitespace around it reads the same from an MP3's TCON as from a FLAC's GENRE.
func TestMP3GenreWhitespaceMatchesFLAC(t *testing.T) {
	const genre = "  padded  "
	flac := testfile.TaggedFLAC("ref", "GENRE="+genre)
	mp3 := slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TCON", genre)), mp3Audio(t))
	want, _ := mustParseBytes(t, flac).Get(tag.Genre)
	if got, _ := mustParseBytes(t, mp3).Get(tag.Genre); !slices.Equal(got, want) || !slices.Equal(want, []string{genre}) {
		t.Errorf("MP3 GENRE %q, FLAC %q; want both %q", got, want, genre)
	}
}

// An MP3 whose front tag declares more bytes than the file holds reads its audio behind the tag: the
// tag is an unreadable region up to the first frame, reported, out of the essence, whose digest is
// the clean file's, and an edit replaces the whole region, saying it is not carried.
func TestMP3FrontTagPastTheEnd(t *testing.T) {
	audio := mp3Audio(t)
	tagBytes := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Cut"), testfile.TextFrame(3, "TALB", "Lost"))
	copy(tagBytes[6:10], testfile.Syncsafe(len(audio)+1000))
	src := slices.Concat(tagBytes, audio)
	doc := mustParseBytes(t, src)
	if !hasWarning(doc, wl.WarnMalformedTagEntry) || hasWarning(doc, wl.WarnLeadingBytes) || hasWarning(doc, wl.WarnNoAudioFrames) {
		t.Errorf("warnings %v; want the tag reported and the audio found", doc.Warnings())
	}
	if nativeSize(doc, "ID3v2.3") != len(tagBytes) || nativeSize(doc, "MPEG audio") != len(audio) || !essenceOf(t, src).Equal(essenceOf(t, audio)) {
		t.Errorf("native %+v; want the tag's %d bytes, then the audio with the clean digest", doc.Native().Describe(), len(tagBytes))
	}
	plan, err := doc.Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("plan warnings %v, want malformed-tag-entry-dropped", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	if bytes.Contains(out, []byte("Lost")) || !bytes.HasSuffix(out, audio) || mustParseBytes(t, out).Fields().Title != "New" {
		t.Error("the edit did not replace the unreadable tag with the new one ahead of the audio")
	}
}

// Nine ID3v2 tags chained ahead of an MP3: the chain read stops at eight, the front tag and seven
// duplicates, and the ninth is no stream start but bytes before the first frame. The digest is the
// clean file's before an edit and after it, when the ninth becomes the chain's duplicate.
func TestMP3NineChainedTags(t *testing.T) {
	audio := mp3Audio(t)
	var parts [][]byte
	for i := range 9 {
		parts = append(parts, testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", fmt.Sprintf("Tag %d", i))))
	}
	src := slices.Concat(append(parts, audio)...)
	clean := essenceOf(t, audio)
	doc := mustParseBytes(t, src)
	if !essenceOf(t, src).Equal(clean) || !hasWarning(doc, wl.WarnLeadingBytes) || nativeSize(doc, "MPEG audio") != len(audio) {
		t.Errorf("warnings %v, native %+v; want the ninth tag before the first frame and the clean digest", doc.Warnings(), doc.Native().Describe())
	}
	plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, src, plan); !essenceOf(t, out).Equal(clean) {
		t.Error("the edit changed the digest")
	}
}

// dupDroppedNames is the duplicate-tag-block-dropped message of plan, or "" when it has none.
func dupDroppedNames(plan *wl.Plan) string {
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnDuplicateTagBlockDropped {
			return w.Message
		}
	}
	return ""
}

// A chapter edit rewrites the chapter frames from the flat chapter list, so it names what that list
// cannot hold: a chapter's image, a second title or unreadable bytes, a table of contents' title, and
// a nested table. Plain chapters under one table lose nothing, and --strict lets their edit through.
func TestMP3ChapterEditNamesDroppedChapterContent(t *testing.T) {
	unused := []byte{0xFF, 0xFF, 0xFF, 0xFF} // no byte offsets
	chap := func(sub ...[]byte) []byte {
		return testfile.ID3Frame(3, "CHAP", slices.Concat([]byte("ch0\x00"), testfile.BE32(0), testfile.BE32(1000),
			unused, unused, testfile.TextFrame(3, "TIT2", "One"), slices.Concat(sub...)))
	}
	ctoc := func(id string, flags byte, child string, sub ...[]byte) []byte {
		return testfile.ID3Frame(3, "CTOC", slices.Concat([]byte(id+"\x00"), []byte{flags, 1}, []byte(child+"\x00"), slices.Concat(sub...)))
	}
	const topOrdered, ordered = 3, 1
	toc := ctoc("toc", topOrdered, "ch0")
	for _, c := range []struct {
		name   string
		frames [][]byte
		lossy  bool
	}{
		{"plain chapters under one table", [][]byte{chap(), toc}, false},
		{"a lone table not flagged top-level", [][]byte{chap(), ctoc("toc", ordered, "ch0")}, false},
		{"a chapter's image", [][]byte{chap(testfile.FrontCover("image/png", testfile.PNG())), toc}, true},
		{"a chapter's second title", [][]byte{chap(testfile.TextFrame(3, "TIT2", "Two")), toc}, true},
		{"a chapter's unreadable sub-frame", [][]byte{chap(testfile.OverrunFrame("APIC")), toc}, true},
		{"a table of contents' title", [][]byte{chap(), ctoc("toc", topOrdered, "ch0", testfile.TextFrame(3, "TIT2", "Contents"))}, true},
		{"a nested table", [][]byte{chap(), ctoc("toc", topOrdered, "part"), ctoc("part", ordered, "ch0")}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := slices.Concat(testfile.ID3v2(3, slices.Concat([][]byte{testfile.TextFrame(3, "TIT2", "Front")}, c.frames)...), mp3Audio(t))
			doc := mustParseBytes(t, src)
			if len(doc.Chapters()) != 1 {
				t.Fatalf("setup: %d chapters, want 1", len(doc.Chapters()))
			}
			plan, err := doc.Edit().SetChapters(wl.Chapter{Start: 0, End: time.Second, Title: "Renamed"}).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if got := planWarns(t, plan, wl.WarnChapterMetadataDropped); got != c.lossy {
				t.Errorf("chapter-metadata-dropped warned %v, want %v: %v", got, c.lossy, plan.Report().Warnings)
			}
		})
	}
}

// A duplicate ID3v2 tag a rewrite drops loses more than values: the frames the canonical view does not
// carry (a rating, a URL, private data), a picture the written tag lacks, and, when the duplicate does
// not read at all, everything in it. The warning names each, so --strict refuses; a picture the
// written tag holds too is not a loss.
func TestMP3DroppedDuplicateNamesWhatItLoses(t *testing.T) {
	front := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Front"))
	popm := testfile.ID3Frame(3, "POPM", []byte("me@example.com\x00\xC8"))
	wxxx := testfile.ID3Frame(3, "WXXX", []byte("\x00\x00http://example.com"))
	priv := testfile.ID3Frame(3, "PRIV", []byte("owner\x00data"))
	cover := testfile.FrontCover("image/png", testfile.PNG())
	unknown := testfile.ID3v2(5, testfile.TextFrame(4, "TALB", "Future"))
	compressed := testfile.ID3v2(2, frame22("TT2", []byte("\x00Compressed")))
	compressed[5] = 0x40
	credit := testfile.ID3Frame(3, "TIPL", []byte("\x00photography\x00Some One"))
	chap := func(sub ...[]byte) []byte {
		unused := []byte{0xFF, 0xFF, 0xFF, 0xFF} // no byte offsets
		return testfile.ID3Frame(3, "CHAP", slices.Concat([]byte("ch0\x00"), testfile.BE32(0), testfile.BE32(1000),
			unused, unused, testfile.TextFrame(3, "TIT2", "One"), slices.Concat(sub...)))
	}
	chapterFront := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Front"), chap())
	for _, c := range []struct {
		name   string
		front  []byte
		second []byte
		want   []string // in the dropped warning; none means no warning
	}{
		{"unmodeled frames", front, testfile.ID3v2(3, popm, wxxx, priv), []string{"POPM", "WXXX", "PRIV"}},
		{"an unknown version", front, unknown, []string{"unreadable ID3v2.5 tag"}},
		{"a compressed v2.2 tag", front, compressed, []string{"unreadable ID3v2.2 tag"}},
		{"an unreadable tail", front, testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Front"), testfile.OverrunFrame("TALB")),
			[]string{"unreadable byte(s)"}},
		{"the written tag's own picture", testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Front"), cover),
			testfile.ID3v2(3, cover), nil},
		{"a picture the written tag lacks", front, testfile.ID3v2(3, cover), []string{"1 picture(s)"}},
		{"a credit the canonical view does not model", front, testfile.ID3v2(3, credit), []string{"TIPL"}},
		{"a chapter's sub-frame", chapterFront, testfile.ID3v2(3, chap(wxxx)), []string{"CHAP"}},
		{"a chapter's second title", chapterFront, testfile.ID3v2(3, chap(testfile.TextFrame(3, "TIT2", "Two"))), []string{"CHAP"}},
		{"a chapter's unreadable sub-frame", chapterFront, testfile.ID3v2(3, chap(testfile.OverrunFrame("APIC"))), []string{"CHAP"}},
		{"a table of contents' title", chapterFront, testfile.ID3v2(3, chap(), testfile.ID3Frame(3, "CTOC",
			slices.Concat([]byte("toc\x00"), []byte{3, 1}, []byte("ch0\x00"), testfile.TextFrame(3, "TIT2", "Contents")))), []string{"CTOC"}},
		{"a lone table not flagged top-level", chapterFront, testfile.ID3v2(3, chap(), testfile.ID3Frame(3, "CTOC",
			slices.Concat([]byte("toc\x00"), []byte{1, 1}, []byte("ch0\x00")))), nil},
		{"a nested table of contents", chapterFront, testfile.ID3v2(3, chap(), testfile.ID3Frame(3, "CTOC",
			slices.Concat([]byte("toc\x00"), []byte{3, 1}, []byte("part\x00"))), testfile.ID3Frame(3, "CTOC",
			slices.Concat([]byte("part\x00"), []byte{1, 1}, []byte("ch0\x00")))), []string{`1 "CTOC" frame(s)`}},
		{"two frames of one kind", front, testfile.ID3v2(3, popm, testfile.ID3Frame(3, "POPM", []byte("you@example.com\x00\x40"))),
			[]string{`2 "POPM" frame(s)`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := slices.Concat(c.front, c.second, mp3Audio(t))
			doc := mustParseBytes(t, src)
			if doc.Fields().Title != "Front" || !hasWarning(doc, wl.WarnDuplicateTagBlock) {
				t.Fatalf("title %q, warnings %v; want the front tag read and the second reported", doc.Fields().Title, doc.Warnings())
			}
			plan, err := doc.Edit().Set(tag.Artist, "X").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			msg := dupDroppedNames(plan)
			if (msg != "") != (len(c.want) > 0) {
				t.Fatalf("dropped warning %q, want one naming %v", msg, c.want)
			}
			for _, w := range c.want {
				if !strings.Contains(msg, w) {
					t.Errorf("dropped warning %q does not name %s", msg, w)
				}
			}
		})
	}
}

// Of the tags a file opens with, the first that holds frames is the one read: an empty one ahead of
// it, or one that cannot be read, is a duplicate, and an edit keeps the real tag's values, dropping
// an empty duplicate without a word.
func TestMP3FirstTagHoldingFramesWins(t *testing.T) {
	tagged := readFixture(t, sampleMP324)
	for _, c := range []struct {
		name   string
		ahead  []byte
		silent bool // dropping the tag ahead loses nothing
	}{
		{"an empty tag", readFixture(t, "../testdata/empty.mp3"), true},
		{"an unknown version", testfile.ID3v2(5, testfile.TextFrame(4, "TIT2", "Future")), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := slices.Concat(c.ahead, tagged)
			doc := mustParseBytes(t, src)
			want := mustParseBytes(t, tagged).Tags()
			if !doc.Tags().Equal(want) || !hasWarning(doc, wl.WarnDuplicateTagBlock) {
				t.Fatalf("tags %v, warnings %v; want the second tag's eight values and the duplicate reported", doc.Tags().Keys(), doc.Warnings())
			}
			plan, err := doc.Edit().Set(tag.Comment, "Note").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if got := dupDroppedNames(plan) == ""; got != c.silent {
				t.Errorf("dropped warning %q, want one only when the tag ahead held something", dupDroppedNames(plan))
			}
			re := mustParseBytes(t, applyToBytes(t, src, plan))
			for _, k := range want.Keys() {
				if v, _ := re.Get(k); len(v) == 0 {
					t.Errorf("the edit lost %s", k)
				}
			}
		})
	}
}

// A tag written twice, its copy a duplicate, loses nothing when an edit drops the copy: the value the
// edit replaces or clears, the picture, chapter or synced lyrics it removes, and a credit list it
// re-renders were the file's own, so none is named and --strict lets the edit through. That holds
// for a chapter the read opens up, one starting past the end with its end at its start.
func TestMP3DuplicateOfTheSameTagLosesNothing(t *testing.T) {
	chapAt := func(id string, start, end int) []byte {
		return testfile.ID3Frame(3, "CHAP", slices.Concat([]byte(id+"\x00"), testfile.BE32(start), testfile.BE32(end),
			[]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, testfile.TextFrame(3, "TIT2", id)))
	}
	chap := slices.Concat(chapAt("ch0", 0, 1000), chapAt("ch1", 60000, 60000))
	sylt := testfile.ID3Frame(3, "SYLT", slices.Concat([]byte{0}, []byte("eng"), []byte{2, 1, 0}, []byte("line\x00"), testfile.BE32(1000)))
	ipls := testfile.ID3Frame(3, "IPLS", []byte("\x00producer\x00Pro\x00mastering\x00Mas"))
	tagBytes := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Title"), testfile.TextFrame(3, "TPE1", "Artist"),
		testfile.FrontCover("image/png", testfile.PNG()), chap, sylt, ipls)
	src := slices.Concat(tagBytes, tagBytes, mp3Audio(t))
	doc := mustParseBytes(t, src)
	if chs := doc.Chapters(); len(chs) != 2 || chs[1].End != 0 || len(doc.SyncedLyrics()) != 1 {
		t.Fatalf("setup: chapters %+v, %d synced lyric sets; want two, the last opened up, and one set", chs, len(doc.SyncedLyrics()))
	}
	for name, edit := range map[string]func(*wl.Editor) *wl.Editor{
		"replace the title":       func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "New") },
		"clear the artist":        func(e *wl.Editor) *wl.Editor { return e.Clear(tag.Artist) },
		"remove the cover":        func(e *wl.Editor) *wl.Editor { return e.ClearPictures() },
		"clear the chapters":      func(e *wl.Editor) *wl.Editor { return e.ClearChapters() },
		"clear the synced lyrics": func(e *wl.Editor) *wl.Editor { return e.ClearSyncedLyrics() },
		"replace the producer":    func(e *wl.Editor) *wl.Editor { return e.Set(tag.Producer, "Other") },
	} {
		plan, err := edit(doc.Edit()).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if msg := dupDroppedNames(plan); msg != "" {
			t.Errorf("%s: dropped warning %q, want none", name, msg)
		}
	}
}

// A padding-only edit of a tag whose padding holds junk reports the padding it had, none, and says
// the unreadable bytes are not carried, instead of counting them as padding.
func TestMP3PaddingEditOfJunkPadding(t *testing.T) {
	pad := make([]byte, 1000)
	copy(pad[996:], "JUNK")
	src := slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Title"), pad), mp3Audio(t))
	plan, err := mustParseBytes(t, src).Edit().Prepare(wl.WithPadding(wl.PaddingPolicy{}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Report().Operations, "padding 0 -> 0") || !planWarns(t, plan, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("operations %v, warnings %v; want padding 0 -> 0 and the junk reported dropped", plan.Report().Operations, plan.Report().Warnings)
	}
}
