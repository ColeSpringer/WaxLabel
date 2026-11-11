package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	wl "github.com/colespringer/waxlabel" // also registers the codecs (populates core's registry)
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
)

// detect runs detection over data as Parse does, looking past leading ID3v2 tags; FormatUnknown
// stands for unsupported.
func detect(t *testing.T, data []byte) core.Format {
	t.Helper()
	c, ok, err := core.DetectLeading(core.BytesSource(data), id3.TagSize)
	if err != nil {
		t.Fatalf("detection over bytes in memory: %v", err)
	}
	if !ok {
		return core.FormatUnknown
	}
	return c.Format()
}

// cutShort is a source that declares size bytes but holds only data, as a file cut short under a
// parse does: its reads past data end early with io.EOF.
type cutShort struct {
	data []byte
	size int64
}

func (s cutShort) Size() int64 { return s.size }

func (s cutShort) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Detection reads only inside the file, so a read there that fails is returned in its class, not
// taken for an unrecognized format: a device error keeps its cause, in the sniff window, in the window
// after a leading tag and in the resync window behind a gap, and a short read is invalid data. A file
// whose first bytes match no format is still unrecognized, with no error.
func TestDetectionReadFailures(t *testing.T) {
	flac := readFixture(t, sampleFLAC)
	tag := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", strings.Repeat("x", 2000)))
	behind := slices.Concat(tag, flac)
	gap := slices.Concat(tag, make([]byte, 3000), flac) // nothing sniffs right after the tag
	for _, c := range []struct {
		name    string
		src     core.ReaderAtSized
		cause   error
		invalid bool
	}{
		{"a device error in the sniff window", &testfile.Faulty{Src: wl.BytesSource(flac), Fails: testfile.Covering(100)}, syscall.EIO, false},
		{"a short read in the sniff window", cutShort{flac[:100], int64(len(flac))}, io.ErrUnexpectedEOF, true},
		{"a device error in the window after a leading tag", &testfile.Faulty{Src: wl.BytesSource(behind), Fails: testfile.Covering(int64(len(tag) + 100))}, syscall.EIO, false},
		{"a device error in the resync window", &testfile.Faulty{Src: wl.BytesSource(gap), Fails: testfile.Covering(int64(len(tag) + 2000))}, syscall.EIO, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := core.DetectLeading(c.src, id3.TagSize)
			if !errors.Is(err, c.cause) || errors.Is(err, waxerr.ErrInvalidData) != c.invalid {
				t.Errorf("detection: err = %v, want %v, invalid data %v", err, c.cause, c.invalid)
			}
			_, err = wl.Parse(context.Background(), c.src)
			if !errors.Is(err, c.cause) || errors.Is(err, waxerr.ErrInvalidData) != c.invalid {
				t.Errorf("Parse: err = %v, want %v, invalid data %v", err, c.cause, c.invalid)
			}
		})
	}
	// A failed read is the window's failure even where reading the bytes again would succeed.
	once := &testfile.Faulty{Src: wl.BytesSource(behind), Fails: testfile.Nth(1, testfile.Covering(int64(len(tag)+100)))}
	if _, _, err := core.DetectLeading(once, id3.TagSize); !errors.Is(err, syscall.EIO) {
		t.Errorf("a device error once in the window after a leading tag: err = %v, want it", err)
	}
	if c, ok, err := core.DetectLeading(core.BytesSource([]byte("matches no format at all")), id3.TagSize); ok || err != nil {
		t.Errorf("an unrecognized file: %v, %v, %v; want no codec and no error", c, ok, err)
	}
}

// every valid testdata fixture is recognized from its leading bytes, as detection only sniffs the
// content. A failure names the fixture that would read as unsupported.
func TestContentDetectionCoversFixtures(t *testing.T) {
	dir := "../testdata"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		seen++
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			header := make([]byte, core.SniffWindow)
			n, _ := f.Read(header)
			if _, ok := core.Detect(header[:n]); !ok {
				t.Errorf("Detect does not recognize %s from its leading bytes, so it would read as unsupported (exit 3)", name)
			}
		})
	}
	if seen == 0 {
		t.Fatal("no fixtures found; content detection was not exercised")
	}
}

// The parsers that read past a leading ID3v2 tag are MP3, FLAC, raw AAC, WavPack, Monkey's Audio,
// Musepack and the three Ogg mappings. Other inner signatures past ID3 are reported unsupported.
func TestSkipsLeadingID3Set(t *testing.T) {
	want := map[core.Format]bool{
		core.FormatMP3: true, core.FormatFLAC: true, core.FormatAAC: true,
		core.FormatWavPack: true, core.FormatMonkeysAudio: true, core.FormatMusepack: true,
		core.FormatOggVorbis: true, core.FormatOggOpus: true, core.FormatOggFLAC: true,
		core.FormatMP4: false, core.FormatMatroska: false, core.FormatWAV: false,
		core.FormatAIFF: false,
	}
	for f, w := range want {
		c, ok := core.ForFormat(f)
		if !ok {
			t.Fatalf("no codec registered for %s", f)
		}
		if got := c.SkipsLeadingID3(); got != w {
			t.Errorf("%s.SkipsLeadingID3() = %v, want %v", f, got, w)
		}
	}
}

// Detection skips up to MaxLeadingTags chained ID3v2 tags. When the bytes after them match no
// sniffer, the nearest stream that a codec skipping leading ID3 confirms within the resync window
// wins, else the file stays MP3; a format that does not skip leading ID3 stays unsupported behind
// one. A candidate confirms only by its second frame, or by the check after its marker, inside the
// window.
func TestDetectsPastLeadingTagsAndGaps(t *testing.T) {
	lead := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T"))
	tags := func(n int) []byte { return bytes.Repeat(lead, n) }
	gap := []byte{1, 2, 3}
	zeros := func(n int) []byte { return make([]byte, n) }
	big := testfile.ID3v2(3, testfile.ID3Frame(3, "PRIV", make([]byte, core.ResyncWindow)))
	past := slices.Clone(lead)
	past[6] = 0x7f // declares far more bytes than the file holds
	flac := readFixture(t, sampleFLAC)
	adts := adtsStream(2, 4, 200) // 207-byte frames
	badWV := wvBlock(1|wvRate44k|wvFinal, 4410, make([]byte, 16))
	binary.LittleEndian.PutUint16(badWV[8:10], 0x0300)
	for _, c := range []struct {
		name string
		data []byte
		want core.Format
	}{
		{"one tag", slices.Concat(lead, flac), core.FormatFLAC},
		{"two tags", slices.Concat(tags(2), flac), core.FormatFLAC},
		{"the most chained tags", slices.Concat(tags(core.MaxLeadingTags), flac), core.FormatFLAC},
		{"one tag too many", slices.Concat(tags(core.MaxLeadingTags+1), flac), core.FormatMP3},
		{"a second tag past the resync window", slices.Concat(lead, big, flac), core.FormatFLAC},
		{"a second tag past the resync window, then a gap", slices.Concat(lead, big, gap, flac), core.FormatFLAC},
		{"a second tag running past the end", slices.Concat(lead, past, flac), core.FormatMP3},
		{"two tags then Ogg", slices.Concat(tags(2), readFixture(t, sampleOgg)), core.FormatOggVorbis},
		{"two tags then MP4", slices.Concat(tags(2), readFixture(t, sampleMP4)), core.FormatUnknown},

		{"gap then FLAC", slices.Concat(lead, gap, flac), core.FormatFLAC},
		{"gap then ADTS", slices.Concat(lead, gap, adts), core.FormatAAC},
		{"gap then WavPack", slices.Concat(lead, gap, readFixture(t, sampleWV)), core.FormatWavPack},
		{"gap then Monkey's Audio", slices.Concat(lead, gap, readFixture(t, sampleAPE)), core.FormatMonkeysAudio},
		{"gap then Musepack SV8", slices.Concat(lead, gap, readFixture(t, chaptersMPC)), core.FormatMusepack},
		{"gap then Musepack SV7", slices.Concat(lead, gap, mpcSV7(100, 0)), core.FormatMusepack},
		{"gap then Ogg Vorbis", slices.Concat(lead, gap, readFixture(t, sampleOgg)), core.FormatOggVorbis},
		{"gap then Ogg Opus", slices.Concat(lead, gap, readFixture(t, sampleOpus)), core.FormatOggOpus},
		{"gap then Ogg FLAC", slices.Concat(lead, gap, readFixture(t, sampleOggFLAC)), core.FormatOggFLAC},
		{"gap then an Ogg page with a broken checksum", slices.Concat(lead, gap, brokenChecksum(readFixture(t, sampleOgg)), adts), core.FormatAAC},
		{"gap then nothing confirmed", slices.Concat(lead, gap, []byte("plain text, not audio")), core.FormatMP3},

		{"FLAC nearer than ADTS", slices.Concat(lead, gap, []byte("fLaC\x00\x00\x00\x22"), adts), core.FormatFLAC},
		{"ADTS nearer than FLAC", slices.Concat(lead, gap, adts, flac), core.FormatAAC},
		{"MPEG frames nearer than ADTS", slices.Concat(lead, gap, mp3VBRStream(2, 2), adts), core.FormatMP3},
		{"a lone MPEG header", slices.Concat(lead, gap, []byte{0xFF, 0xFB, 0x90, 0x00}, adts), core.FormatAAC},
		{"two ADTS headers that disagree", slices.Concat(lead, gap, adtsStream(1, 1, 200), adtsStream(2, 1, 200), flac), core.FormatFLAC},
		{"fLaC without a STREAMINFO header", slices.Concat(lead, gap, []byte("fLaC\x04\x00\x00\x22"), adts), core.FormatAAC},
		{"wvpk with an unsupported version", slices.Concat(lead, gap, badWV, adts), core.FormatAAC},
		{"MAC with a version before 3.80", slices.Concat(lead, gap, legacyAPEHeader(3700, 2000, 0, 2, 44100, 1, 100), adts), core.FormatAAC},
		{"MPCK without a packet", slices.Concat(lead, gap, []byte("MPCKsh\x05\x00\x00"), adts), core.FormatAAC},
		{"MP+ with an unknown version", slices.Concat(lead, gap, []byte("MP+\x08"), adts), core.FormatAAC},

		{"FLAC block header ending at the window", slices.Concat(lead, zeros(core.ResyncWindow-8), flac), core.FormatFLAC},
		{"FLAC block header crossing the window", slices.Concat(lead, zeros(core.ResyncWindow-7), flac), core.FormatMP3},
		{"second ADTS header ending at the window", slices.Concat(lead, zeros(core.ResyncWindow-214), adts), core.FormatAAC},
		{"second ADTS header crossing the window", slices.Concat(lead, zeros(core.ResyncWindow-213), adts), core.FormatMP3},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := detect(t, c.data); got != c.want {
				t.Errorf("detected %v, want %v", got, c.want)
			}
		})
	}
}

// With no tag ahead of them, bytes no codec knows are skipped only for a stream whose first unit
// carries a checksum, an Ogg page: within the resync window, and only when the checksum holds.
// Another stream behind such bytes stays unidentified.
func TestDetectsOggBehindBytesWithNoTag(t *testing.T) {
	junk := func(n int) []byte { return bytes.Repeat([]byte{0xA5}, n) }
	ogg := readFixture(t, sampleOgg)
	firstPage := oggPages(ogg)[0].len
	for _, c := range []struct {
		name string
		data []byte
		want core.Format
	}{
		{"Ogg Vorbis", slices.Concat(junk(10), ogg), core.FormatOggVorbis},
		{"Ogg Opus", slices.Concat(junk(10), readFixture(t, sampleOpus)), core.FormatOggOpus},
		{"Ogg FLAC", slices.Concat(junk(10), readFixture(t, sampleOggFLAC)), core.FormatOggFLAC},
		{"a first page ending at the window", slices.Concat(junk(core.ResyncWindow-firstPage), ogg), core.FormatOggVorbis},
		{"a first page crossing the window", slices.Concat(junk(core.ResyncWindow-firstPage+1), ogg), core.FormatUnknown},
		{"a first page with a broken checksum", slices.Concat(junk(10), brokenChecksum(ogg)), core.FormatUnknown},
		{"FLAC", slices.Concat(junk(10), readFixture(t, sampleFLAC)), core.FormatUnknown},
		{"MPEG frames", slices.Concat(junk(10), mp3VBRStream(2, 2)), core.FormatUnknown},
		{"nothing", junk(1000), core.FormatUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := detect(t, c.data); got != c.want {
				t.Errorf("detected %v, want %v", got, c.want)
			}
		})
	}
}

// brokenChecksum returns a copy of an Ogg stream whose first page no longer matches its checksum.
func brokenChecksum(stream []byte) []byte {
	out := slices.Clone(stream)
	out[22] ^= 0xFF
	return out
}

// An Ogg stream is recognized by its codec signature opening the packet that begins the stream on its
// first page, or on the next page when the first begins a Skeleton stream; the signature anywhere
// else in the sniff window does not count.
func TestOggSniffWalksPageHeaders(t *testing.T) {
	page := testfile.OggPage
	vorbisID := slices.Concat([]byte("\x01vorbis"), make([]byte, 4), []byte{2}, testfile.LE32(44100), make([]byte, 12), []byte{0xB8, 1})
	opusHead := slices.Concat([]byte("OpusHead"), []byte{1, 2}, testfile.LE16(312), testfile.LE32(48000), make([]byte, 3))
	opusTags := slices.Concat([]byte("OpusTags"), testfile.LE32(9), []byte("x\x01vorbisx"), testfile.LE32(0))
	fishead := slices.Concat([]byte("fishead\x00"), testfile.LE16(3), testfile.LE16(0), make([]byte, 52))
	for _, c := range []struct {
		name string
		data []byte
		want core.Format
	}{
		{"Vorbis", page(2, 0, 1, 0, vorbisID), core.FormatOggVorbis},
		{"Skeleton then Vorbis", slices.Concat(page(2, 0, 7, 0, fishead), page(2, 0, 1, 0, vorbisID)), core.FormatOggVorbis},
		{"Skeleton then Opus", slices.Concat(page(2, 0, 7, 0, fishead), page(2, 0, 1, 0, opusHead)), core.FormatOggOpus},
		{"Opus whose comments hold the Vorbis signature", slices.Concat(page(2, 0, 1, 0, opusHead), page(0, 0, 1, 1, opusTags)), core.FormatOggOpus},
		{"Skeleton alone", page(2, 0, 7, 0, fishead), core.FormatUnknown},
		{"signature not opening the packet", page(2, 0, 1, 0, append([]byte{0}, vorbisID...)), core.FormatUnknown},
		{"first page not beginning a stream", page(0, 0, 1, 0, vorbisID), core.FormatUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := detect(t, c.data); got != c.want {
				t.Errorf("detected %v, want %v", got, c.want)
			}
		})
	}
}

// A file whose writer emitted a free/skip/wide box before "ftyp" is still detected as MP4:
// walkAtoms is generic over top-level atoms and only moov is required.
func TestDetectsMP4BehindLeadingFreeBox(t *testing.T) {
	ftyp := []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00M4A mp42isom")
	box := func(name string, n int) []byte { return testfile.MP4Atom(name, make([]byte, n-8)) }
	for _, tc := range []struct {
		name string
		data []byte
		want bool
	}{
		{"bare ftyp", ftyp, true},
		{"free then ftyp", append(box("free", 8), ftyp...), true},
		{"skip then ftyp", append(box("skip", 16), ftyp...), true},
		{"wide then ftyp", append(box("wide", 8), ftyp...), true},
		{"two boxes then ftyp", slices.Concat(box("free", 8), box("skip", 8), ftyp), true},
		{"skip of a few hundred bytes then ftyp", append(box("skip", 300), ftyp...), true},
		{"free filling the sniff window", append(box("free", core.SniffWindow-8), ftyp...), true},
		// A leading box whose end leaves no room for the ftyp header inside the sniff window hides
		// it, and an unrecognized leading box is not stepped over at all.
		{"free past the sniff window", append(box("free", core.SniffWindow), ftyp...), false},
		{"unknown leading box", append(box("junk", 8), ftyp...), false},
		// A size below 8 is the "extends to EOF" / "64-bit size follows" form; neither
		// names a next box inside the window.
		{"free with a to-EOF size", append([]byte("\x00\x00\x00\x00free"), ftyp...), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := detect(t, tc.data)
			if (got != core.FormatUnknown) != tc.want {
				t.Fatalf("detected %v, want MP4: %v", got, tc.want)
			}
			if tc.want && got != core.FormatMP4 {
				t.Errorf("format = %v, want MP4", got)
			}
		})
	}
}
