package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// failWriter errors after limit bytes (full disk mid-write).
type failWriter struct {
	limit, written int
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.written+len(p) > w.limit {
		n := w.limit - w.written
		w.written = w.limit
		return n, errors.New("simulated write failure")
	}
	w.written += len(p)
	return len(p), nil
}

func TestWriteToFailingWriterPropagatesError(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	src, _ := os.ReadFile(sampleFLAC)
	plan, err := doc.Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = plan.Execute(context.Background(), wl.WriteTo(&failWriter{limit: 100}, wl.BytesSource(src)))
	if err == nil {
		t.Fatal("expected an error from a failing writer")
	}
}

func TestContextCancellationStopsExecute(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	before, _ := os.ReadFile(path)
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("cancelled SaveBack modified the file")
	}
}

// SaveBack is atomic (no temp litter); a no-op writes nothing.
func TestSaveBackLeavesNoTempLitter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.flac")
	data, _ := os.ReadFile(sampleFLAC)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	doc := mustParseFile(t, path)
	plan, _ := doc.Edit().Set(tag.Title, "Committed").Prepare()
	if _, res, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil || !res.Committed {
		t.Fatalf("SaveBack: err=%v committed=%v", err, res.Committed)
	}
	assertNoTempFiles(t, dir)

	doc2 := mustParseFile(t, path)
	plan2, _ := doc2.Edit().Prepare()
	if _, res, err := plan2.Execute(context.Background(), wl.SaveBack()); err != nil || res.Committed {
		t.Fatalf("no-op SaveBack: err=%v committed=%v (want committed=false)", err, res.Committed)
	}
	assertNoTempFiles(t, dir)
}

func TestSaveAsFileToBadDirFailsCleanly(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	plan, _ := doc.Edit().Set(tag.Title, "X").Prepare()

	bad := filepath.Join(t.TempDir(), "does-not-exist", "out.flac")
	_, _, err := plan.Execute(context.Background(), wl.SaveAsFile(bad))
	if err == nil {
		t.Fatal("expected error writing into a nonexistent directory")
	}
	if _, statErr := os.Stat(bad); statErr == nil {
		t.Error("a partial output file was left behind")
	}
}

// mtime updates by default; WithPreserveModTime keeps it, including pre-epoch.
func TestModTimePolicy(t *testing.T) {
	past := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("default updates mtime", func(t *testing.T) {
		path := copyToTemp(t, sampleFLAC)
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
		doc := mustParseFile(t, path)
		plan, _ := doc.Edit().Set(tag.Title, "a").Prepare()
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		if got := statMod(t, path); got.Equal(past) {
			t.Error("default SaveBack should have updated mtime, but it kept the old one")
		}
	})

	t.Run("WithPreserveModTime keeps mtime", func(t *testing.T) {
		path := copyToTemp(t, sampleFLAC)
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
		doc := mustParseFile(t, path)
		plan, _ := doc.Edit().Set(tag.Title, "b").Prepare(wl.WithPreserveModTime())
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		if got := statMod(t, path); !got.Equal(past) {
			t.Errorf("WithPreserveModTime: mtime = %v, want %v", got, past)
		}
	})

	// The epoch is Unix nanosecond 0, which must not read as "no mtime".
	t.Run("WithPreserveModTime keeps the exact epoch", func(t *testing.T) {
		epoch := time.Unix(0, 0).UTC()
		path := copyToTemp(t, sampleFLAC)
		if err := os.Chtimes(path, epoch, epoch); err != nil {
			t.Fatal(err)
		}
		doc := mustParseFile(t, path)
		plan, _ := doc.Edit().Set(tag.Title, "e").Prepare(wl.WithPreserveModTime())
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		if got := statMod(t, path); !got.Equal(epoch) {
			t.Errorf("WithPreserveModTime: mtime = %v, want %v", got, epoch)
		}
	})

	// Pre-1970 mtime is negative Unix ns; a >0 guard must not drop WithPreserveModTime.
	t.Run("WithPreserveModTime keeps a pre-epoch mtime", func(t *testing.T) {
		ancient := time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC)
		path := copyToTemp(t, sampleFLAC)
		if err := os.Chtimes(path, ancient, ancient); err != nil {
			t.Skipf("filesystem rejected a pre-epoch mtime: %v", err)
		}
		if got := statMod(t, path); !got.Equal(ancient) {
			t.Skipf("filesystem did not store a pre-epoch mtime (kept %v)", got)
		}
		doc := mustParseFile(t, path)
		plan, _ := doc.Edit().Set(tag.Title, "c").Prepare(wl.WithPreserveModTime())
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		if got := statMod(t, path); !got.Equal(ancient) {
			t.Errorf("WithPreserveModTime: mtime = %v, want %v", got, ancient)
		}
	})
}

func statMod(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().UTC()
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".waxlabel-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// A device error in any read a parse makes is the parse's error, keeping its cause and never called
// invalid data, wherever the reader of those bytes tolerates a short read or an absent structure:
// each read that touches the byte fails in turn, and then every one of them.
func TestParseReadFaultsKeepTheirCause(t *testing.T) {
	fixture := func(name string) []byte { return readFixture(t, filepath.Join("..", "testdata", name)) }
	inside := func(file, part []byte, at int) int64 { return int64(bytes.Index(file, part) + at) }

	fmtChunk, data := testfile.WAVFmtPCM(), testfile.WAVData(64)
	tagA := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "First"))
	tagB := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Second"))
	dupInfo := testfile.WAVInfo([2]string{"INAM", "Second"})
	wavDupInfo := testfile.WAV(fmtChunk, testfile.WAVInfo([2]string{"INAM", "First"}), dupInfo, data)
	wavDupID3 := testfile.WAV(fmtChunk, testfile.WAVChunk("id3 ", tagA), testfile.WAVChunk("id3 ", tagB), data)
	ssnd := testfile.SSNDOffset(0, nil, make([]byte, 64))
	aiffDupID3 := testfile.AIFF("AIFF", testfile.COMM(2, 16, 16, 44100), testfile.AIFFChunk("ID3 ", tagA), testfile.AIFFChunk("ID3 ", tagB), ssnd)
	wvRate := wvBlock(1|wvRateUnknown|wvFinal, 8000, wvSubBlock(0x27, []byte{0x40, 0x1F, 0x00}))
	block := testfile.El(testfile.IDSimpleBlock, make([]byte, 4))
	cluster := concat(testfile.IDBytes(testfile.IDCluster), []byte{0xFF}, testfile.Uint(testfile.IDTimestamp, 0), block)
	streamed := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, cluster,
		testfile.El(testfile.IDTags, testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "After")))))
	f32 := testfile.El(testfile.IDDuration, binary.BigEndian.AppendUint32(nil, math.Float32bits(1000)))
	mkvF32 := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, testfile.El(testfile.IDInfo, f32), testfile.AudioCluster(0)))
	mdta := testfile.MP4MdtaFile([]string{"title"}, []string{"Keys Title"})
	saio := mp4SaioFile(t, 0, false)
	emptyMeta := testfile.MP4Assemble()
	co64 := mp4QTChaptersCo64(func(samples int) uint64 { return uint64(samples) })
	mpcV1 := slices.Concat(fixture("chapters.mpc"), testfile.ID3v1("V1", "", "", "", "", 255))
	wv, ape, mp3 := fixture("sample.wv"), fixture("sample.ape"), fixture("sample.mp3")

	for _, c := range []struct {
		name string
		data []byte
		at   int64 // a byte such a reader reads
	}{
		{"Musepack stream header", fixture("chapters.mpc"), 1500},
		{"Monkey's Audio header", fixture("sample.ape"), 2000},
		{"Ogg header page body", fixture("sample.ogg"), 1500},
		{"Ogg segment table", fixture("sample.opus"), 14874},
		{"AIFF FORM header", fixture("notags.aiff"), 0},
		{"AIFC sound data offset", fixture("sample.aifc"), 105},
		{"AIFF duplicate ID3 chunk", aiffDupID3, inside(aiffDupID3, testfile.AIFFChunk("ID3 ", tagB), 20)},
		{"ASF object header", fixture("notags.wma"), 0},
		{"ASF Data Object header", fixture("lossless24.wma"), 4990},
		{"FLAC marker behind the ID3v2 probe", fixture("notags.flac"), 0},
		{"ID3v2 probe before ADTS", fixture("notags.aac"), 0},
		{"Matroska DocType", fixture("chapters.mka"), 31},
		{"Matroska SeekHead", fixture("chapters.mka"), 64},
		{"Matroska Info", fixture("chapters.mka"), 225},
		{"Matroska duration", fixture("chapters.mka"), 307},
		{"Matroska track type", fixture("chapters.mka"), 365},
		{"Matroska sampling frequency", fixture("chapters.mka"), 380},
		{"Matroska edition", fixture("chapters.mka"), 455},
		{"Matroska chapter start", fixture("chapters.mka"), 499},
		{"Matroska chapter end", fixture("chapters.mka"), 504},
		{"Matroska tag", fixture("chapters.mka"), 579},
		{"Matroska targets", fixture("chapters.mka"), 642},
		{"Matroska element ID", fixture("chapters.mka"), 3135},
		{"Matroska element size", fixture("chapters.mka"), 3143},
		{"Matroska timestamp scale", fixture("notags.mka"), 228},
		{"Matroska attachment", fixture("sample.mka"), 454},
		{"Matroska Cues", fixture("sample.mka"), 13431},
		{"Matroska track number", fixture("sample.webm"), 291},
		{"Matroska bit depth", fixture("sample.webm"), 358},
		{"Matroska SimpleTag", fixture("sample.webm"), 414},
		{"Matroska unknown-size cluster", streamed, inside(streamed, block, 0)},
		{"MP4 ftyp brand", fixture("notags.m4a"), 8},
		{"MP4 mvhd", fixture("sample.m4a"), 9065},
		{"MP4 mvhd timing", fixture("notags.m4a"), 9043},
		{"MP4 tkhd", fixture("notags.m4a"), 9159},
		{"MP4 edts", fixture("notags.m4a"), 9251},
		{"MP4 mdhd", fixture("notags.m4a"), 9295},
		{"MP4 hdlr", fixture("notags.m4a"), 9327},
		{"MP4 stsd", fixture("notags.m4a"), 9440},
		{"MP4 meta version", fixture("notags.m4a"), 9888},
		{"MP4 udta", fixture("notags.m4a"), 9900},
		{"MP4 chapter title", fixture("sample_chapters.m4b"), 44},
		{"MP4 chapter track", fixture("sample_chapters.m4b"), 19795},
		{"MP4 chapter tref", fixture("sample_chapters.m4b"), 20014},
		{"MP4 chapter edts", fixture("sample_chapters.m4b"), 22103},
		{"MP4 stts", fixture("sample_chapters.m4b"), 22430},
		{"MP4 stsc", fixture("sample_chapters.m4b"), 22454},
		{"MP4 stsz", fixture("sample_chapters.m4b"), 22482},
		{"MP4 offset table", fixture("sample_chapters.m4b"), 22514},
		{"MP4 chpl", fixture("sample_chapters.m4b"), 22710},
		{"QuickTime udta text", fixture("mp3.mov"), 5133},
		{"WavPack block header", fixture("sample.wv"), 10},
		{"WavPack sample-rate sub-block", wvRate, 33},
		{"WAV RIFF header", fixture("notags.wav"), 0},
		{"RF64 ds64", fixture("sample-rf64.wav"), 15},
		{"WAV duplicate LIST INFO", wavDupInfo, inside(wavDupInfo, dupInfo, 16)},
		{"WAV duplicate id3 chunk", wavDupID3, inside(wavDupID3, testfile.WAVChunk("id3 ", tagB), 20)},
		{"MP4 chapter title text", fixture("sample_chapters.m4b"), 46},
		{"MP4 keys", mdta, inside(mdta, []byte("keys"), 12)},
		{"MP4 saio", saio, inside(saio, []byte("saio"), 8)},
		{"MP4 chapter co64", co64, inside(co64, []byte("co64"), 12)},
		{"MP4 childless meta", emptyMeta, inside(emptyMeta, []byte("meta"), 4)},
		{"Matroska 4-byte float", mkvF32, inside(mkvF32, f32, len(f32)-2)},
		{"Musepack trailing ID3v1", mpcV1, int64(len(mpcV1) - 60)},
		{"WavPack trailing APE tag", wv, int64(len(wv) - 40)},
		{"Monkey's Audio trailing APE tag", ape, int64(len(ape) - 40)},
		{"ADTS leading ID3v2", fixture("sample.aac"), 100},
		{"MP3 trailing ID3v1", mp3, int64(len(mp3) - 60)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := wl.Parse(context.Background(), wl.BytesSource(c.data)); err != nil {
				t.Fatalf("the file with no fault: %v", err)
			}
			for nth := 0; ; nth++ {
				pick, which := testfile.Covering(c.at), "every read"
				if nth > 0 {
					pick, which = testfile.Nth(nth, pick), fmt.Sprintf("read %d", nth)
				}
				src := &testfile.Faulty{Src: wl.BytesSource(c.data), Fails: pick}
				_, err := wl.Parse(context.Background(), src)
				if src.Failed == 0 {
					if nth == 0 {
						t.Fatalf("no read touches byte %d", c.at)
					}
					break
				}
				if !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
					t.Errorf("%s touching byte %d failing: err = %v, want the read's own failure", which, c.at, err)
				}
			}
		})
	}
}

// The metadata fingerprint a save checks is read by the parse, so a device error in bytes only it
// reads, past the window detection sniffs in a chunk the WAV reader skips, fails the parse instead
// of leaving the file with no fingerprint and the save with no check of it.
func TestParseFailsOnAFingerprintReadFault(t *testing.T) {
	skipped := testfile.WAVChunk("junk", bytes.Repeat([]byte{0x5A}, 4096))
	file := testfile.WAV(testfile.WAVFmtPCM(), skipped, testfile.WAVData(64))
	at := int64(bytes.Index(file, skipped) + 2048)
	if _, err := wl.Parse(context.Background(), wl.BytesSource(file)); err != nil {
		t.Fatal(err)
	}
	src := &testfile.Faulty{Src: wl.BytesSource(file), Fails: testfile.Covering(at)}
	_, err := wl.Parse(context.Background(), src)
	if src.Failed == 0 || !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("%d failed reads, err = %v; want the fingerprint read's own failure", src.Failed, err)
	}
}

// A source that serves fewer bytes than its size fails the parse as invalid data even where only the
// metadata fingerprint reads the missing bytes, a trailing chunk the WAV reader skips: the parse
// fails closed instead of leaving a save with no fingerprint to check.
func TestParseFailsOnAShortFingerprintRead(t *testing.T) {
	skipped := testfile.WAVChunk("junk", bytes.Repeat([]byte{0x5A}, 4096))
	file := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(64), skipped)
	cut := bytes.Index(file, skipped) + 2048
	_, err := wl.Parse(context.Background(), cutShort{file[:cut], int64(len(file))})
	if !errors.Is(err, waxerr.ErrInvalidData) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want invalid data from the short read", err)
	}
}
