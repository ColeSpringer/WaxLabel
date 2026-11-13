package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

func TestWithLimitsBoundsAllocation(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	// A limit smaller than the STREAMINFO body must be refused, not allocated.
	_, err := wl.Parse(context.Background(), wl.BytesSource(src),
		wl.WithLimits(wl.Limits{MaxAllocBytes: 20, MaxDepth: 64}))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("err = %v, want ErrSizeTooLarge", err)
	}
}

// Under any allocation limit a fixture either refuses as declared-size-too-large or reads what it
// reads under the default limit: a structure the limit cuts short is never read as absent, cut to
// fit, or reported as invalid data. At 5 bytes not even a fixed-size header fits, so every fixture
// refuses.
func TestSmallLimitsRefuseEveryFixture(t *testing.T) {
	const dir = "../testdata"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		ref, err := wl.ParseFile(ctx, path)
		if err != nil {
			t.Fatalf("%s at the default limit: %v", e.Name(), err)
		}
		for _, limit := range []int64{5, 20, 64, 256, 1024, 4096, 64 << 10} {
			doc, err := wl.ParseFile(ctx, path, wl.WithLimits(wl.Limits{MaxAllocBytes: limit}))
			refused := errors.Is(err, waxerr.ErrSizeTooLarge) && !errors.Is(err, waxerr.ErrInvalidData)
			switch {
			case limit == 5 && !refused:
				t.Errorf("%s at limit %d: err = %v, want declared size too large", e.Name(), limit, err)
				continue
			case err != nil && !refused:
				t.Errorf("%s at limit %d: err = %v, want nil or declared size too large", e.Name(), limit, err)
				continue
			case err != nil:
				continue
			}
			if drift := limitDrift(ref, doc); len(drift) > 0 {
				t.Errorf("%s at limit %d reads differently from the default limit: %v (properties %+v, want %+v)",
					e.Name(), limit, drift, doc.Properties(), ref.Properties())
			}
		}
	}
}

// limitDrift names what got reads differently from ref: tags, pictures, chapters, synced lyrics,
// families, properties, and each warning code got gains (+) or loses (-).
func limitDrift(ref, got *wl.Document) []string {
	var drift []string
	add := func(differs bool, what string) {
		if differs {
			drift = append(drift, what)
		}
	}
	add(!ref.Tags().Equal(got.Tags()), "tags")
	add(!core.EqualPictures(ref.Pictures(), got.Pictures()), "pictures")
	add(!core.EqualChapters(ref.Chapters(), got.Chapters()), "chapters")
	add(!core.EqualSyncedLyrics(ref.SyncedLyrics(), got.SyncedLyrics()), "synced lyrics")
	add(!reflect.DeepEqual(ref.Families(), got.Families()), "families")
	add(!reflect.DeepEqual(ref.Properties(), got.Properties()), "properties")
	codes := map[string]int{}
	for _, w := range ref.Warnings() {
		codes[w.Code.String()]--
	}
	for _, w := range got.Warnings() {
		codes[w.Code.String()]++
	}
	for _, c := range slices.Sorted(maps.Keys(codes)) {
		for n := codes[c]; n != 0; {
			if n > 0 {
				drift, n = append(drift, "warning +"+c), n-1
			} else {
				drift, n = append(drift, "warning -"+c), n+1
			}
		}
	}
	return drift
}

// A tag or header over the allocation limit is refused as declared-size-too-large: never read as
// absent, cut to fit, or reported as invalid data.
func TestLimitRefusesOversizedStructure(t *testing.T) {
	cover := append(testfile.PNG(), make([]byte, 100<<10)...)
	mp3 := readFixture(t, sampleMP3)
	plan, err := mustParseBytes(t, mp3).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: cover}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	mp3Cover := applyToBytes(t, mp3, plan)
	apeTag, err := ape.Render([]ape.Item{{Key: "Comment", Value: strings.Repeat("x", 100<<10)}}, 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	bigID3 := testfile.ID3v2(3, testfile.FrontCover("image/png", cover))
	smallID3 := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T"))
	flacLead := slices.Concat(bigID3, readFixture(t, sampleFLAC))
	// Rate index 15 sends the WavPack parser to the first block's sub-blocks for the rate.
	wv := readFixture(t, sampleWV)
	wv[26], wv[27] = wv[26]|0x80, wv[27]|0x07
	mkaCodec := testfile.Matroska("matroska", "", testfile.El(testfile.IDTracks, testfile.El(testfile.IDTrackEntry,
		testfile.Uint(testfile.IDTrackType, 2), testfile.El(testfile.IDCodecID, []byte(strings.Repeat("A", 2048))))))
	// Each value fits the limit; the Tag group a rewrite copies verbatim does not.
	mkaGroup := testfile.Matroska("matroska", "", testfile.El(testfile.IDTags, testfile.TagGroup(0,
		testfile.SimpleTag("ARTIST", strings.Repeat("a", 600)), testfile.SimpleTag("ALBUM", strings.Repeat("b", 600)))))
	chpl := []byte{1, 0, 0, 0, 0, 0, 0, 0, 40} // version 1, flags, reserved, 40 chapters
	var starts []int
	var titles []string
	for i := range 40 {
		chpl = append(binary.BigEndian.AppendUint64(chpl, uint64(i)*10_000_000), 200)
		chpl = append(chpl, strings.Repeat("c", 200)...)
	}
	for i := range 200 {
		starts, titles = append(starts, i*1000), append(titles, fmt.Sprint("Chapter ", i+1))
	}
	for _, c := range []struct {
		name  string
		data  []byte
		limit int64
	}{
		{"mp3 cover", mp3Cover, 64 << 10},
		{"mp3 tag header", mp3Cover, 5},
		{"mp3 trailing apev2", slices.Concat(mp3, apeTag), 64 << 10},
		{"ape header window", readFixture(t, sampleAPE), 1024},
		{"mpc header window", readFixture(t, chaptersMPC), 4096},
		{"wavpack sample-rate sub-blocks", wv, 4096},
		{"flac leading id3", flacLead, 64 << 10},
		{"flac leading id3 header", flacLead, 5},
		{"wavpack leading id3", slices.Concat(bigID3, readFixture(t, sampleWV)), 64 << 10},
		{"ape leading id3", slices.Concat(bigID3, readFixture(t, sampleAPE)), 64 << 10},
		{"sniff window behind a leading id3", slices.Concat(smallID3, readFixture(t, sampleWV)), 512},
		{"resync window behind a leading id3", slices.Concat(smallID3, []byte{1, 2, 3}, readFixture(t, sampleWV)), 32 << 10},
		{"flac trailing id3v1", slices.Concat(testfile.FLAC(), testfile.ID3v1("T", "", "", "", "", 0)), 64},
		{"wma header object", readFixture(t, sampleWMA), 256},
		{"matroska codec id", mkaCodec, 1024},
		{"matroska tag group", mkaGroup, 1024},
		{"mp4 chapter list", testfile.MP4AssembleUdta(testfile.MP4Atom("chpl", chpl)), 4096},
		{"mp4 chapter track", testfile.MP4QTFile(starts, titles), 1024},
		{"mp4 chapter title", testfile.MP4QTFile([]int{0, 5000}, []string{"Intro", strings.Repeat("t", 60000)}), 32 << 10},
		{"wav duplicate id3 chunk", testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(4),
			testfile.WAVChunk("id3 ", smallID3), testfile.WAVChunk("id3 ", bigID3)), 1024},
		{"aiff duplicate id3 chunk", testfile.AIFF("AIFF", testfile.COMM(2, 1, 16, 44100), testfile.SSNDOffset(0, nil, make([]byte, 4)),
			testfile.AIFFChunk("ID3 ", smallID3), testfile.AIFFChunk("ID3 ", bigID3)), 1024},
	} {
		_, err := wl.Parse(context.Background(), wl.BytesSource(c.data), wl.WithLimits(wl.Limits{MaxAllocBytes: c.limit}))
		if !errors.Is(err, waxerr.ErrSizeTooLarge) || errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("%s at limit %d: err = %v, want declared size too large", c.name, c.limit, err)
		}
		if _, err := wl.Parse(context.Background(), wl.BytesSource(c.data)); err != nil {
			t.Errorf("%s at the default limit: %v", c.name, err)
		}
	}
}

// withCoverItem returns data with a binary Cover Art (Front) item holding cover, laid out by
// apeTagBinaryItem, added to the APE tag that ends data, or followed by a tag holding only that item
// when data ends in none. The tag's records count the item and its bytes.
func withCoverItem(data, cover []byte) []byte {
	tagBytes := apeTagBinaryItem("Cover Art (Front)", cover)
	start := apeTagStart(data)
	if start < 0 {
		return slices.Concat(data, tagBytes)
	}
	item := tagBytes[:len(tagBytes)-32]
	n := len(data)
	out := slices.Concat(data[:n-32], item, data[n-32:])
	size := int(binary.LittleEndian.Uint32(data[n-20:n-16])) + len(item)
	count := int(binary.LittleEndian.Uint32(data[n-16:n-12])) + 1
	records := [][]byte{out[len(out)-32:]}
	if binary.LittleEndian.Uint32(data[n-12:n-8])&(1<<31) != 0 {
		records = append(records, out[start:start+32])
	}
	for _, r := range records {
		put32le(r[12:16], size)
		put32le(r[16:20], count)
	}
	return out
}

// An APE tag over the allocation limit after a WavPack, Monkey's Audio or Musepack stream is refused,
// never read as absent; under the default limit the whole tag reads, the 100 KB cover with the rest.
func TestAPEFormatsTagOverTheLimit(t *testing.T) {
	cover := slices.Concat([]byte("cover.png\x00"), testfile.PNG(), make([]byte, 100<<10))
	for _, path := range []string{sampleWV, sampleAPE, chaptersMPC} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			plain := readFixture(t, path)
			data := withCoverItem(plain, cover)
			_, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: 64 << 10}))
			if !errors.Is(err, waxerr.ErrSizeTooLarge) || errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("at a 64 KiB limit: err = %v, want declared size too large", err)
			}
			doc := mustParseBytes(t, data)
			want := mustParseBytes(t, plain)
			if !doc.Tags().Equal(want.Tags()) {
				t.Errorf("tags = %v, want the plain file's %v", doc.Tags().Keys(), want.Tags().Keys())
			}
			if pics := doc.Pictures(); len(pics) != 1 || pics[0].Type != wl.PicFrontCover || len(pics[0].Data) != len(cover)-len("cover.png\x00") {
				t.Errorf("pictures = %d, want the one front cover", len(pics))
			}
		})
	}
}

// A Musepack SV8 chapter packet over the allocation limit is the one structure a parse skips
// instead of refusing: that chapter is left out and reported, and the chapters around it read.
func TestMusepackChapterOverTheLimitSkipped(t *testing.T) {
	big := testfile.MPCChapter(1152, testfile.MPCChapterTag([2]string{"Comment", strings.Repeat("x", 100<<10)}))
	data := testfile.MPCStream(44100, 0, 2, testfile.MPCAudio(8),
		testfile.MPCTitled(0, "before"), big, testfile.MPCTitled(2304, "after"), testfile.MPCEnd())
	doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: 64 << 10}))
	if err != nil {
		t.Fatalf("at a 64 KiB limit: %v", err)
	}
	if chs := doc.Chapters(); len(chs) != 2 || chs[0].Title != "before" || chs[1].Title != "after" {
		t.Errorf("chapters = %+v, want the two around the skipped one", chs)
	}
	warned := false
	for _, w := range doc.Warnings() {
		warned = warned || w.Code == wl.WarnMalformedTagEntry && strings.Contains(w.Message, "chapter packet 2 is larger than the allocation limit")
	}
	if !warned {
		t.Errorf("warnings = %v, want the skipped packet reported", doc.Warnings())
	}
	if n := len(mustParseBytes(t, data).Chapters()); n != 3 {
		t.Errorf("at the default limit: %d chapters, want 3", n)
	}
}

// A WAV or AIFF metadata chunk is read whole against the allocation limit alone: one past 64 MiB
// is refused under a 64 MiB limit, and under the default limit an item past 64 MiB is read.
func TestMetadataChunkReadWholeOrRefused(t *testing.T) {
	const big = 64<<20 + 2 // past the 64 MiB the parsers used to stop at; even, so no pad byte
	riff := func(id string, bodyPrefix []byte, bodyLen int) []byte {
		pre := slices.Concat(testfile.WAVFmtPCM(), testfile.WAVData(4))
		return slices.Concat([]byte("RIFF"), testfile.LE32(4+len(pre)+8+bodyLen), []byte("WAVE"), pre,
			[]byte(id), testfile.LE32(bodyLen), bodyPrefix)
	}
	form := func(id string, bodyPrefix []byte, bodyLen int) []byte {
		pre := slices.Concat(testfile.COMM(2, 1, 16, 44100), testfile.SSNDOffset(0, nil, make([]byte, 4)))
		return slices.Concat([]byte("FORM"), testfile.BE32(4+len(pre)+8+bodyLen), []byte("AIFF"), pre,
			[]byte(id), testfile.BE32(bodyLen), bodyPrefix)
	}
	// An INFO item, or an ID3v2 PRIV frame, of big zero bytes, then the title past it.
	info := slices.Concat([]byte("INFO"), []byte("IZZZ"), testfile.LE32(big))
	inam := slices.Concat([]byte("INAM"), testfile.LE32(6), []byte("After\x00"))
	tit2 := testfile.TextFrame(3, "TIT2", "After")
	id3 := slices.Concat([]byte{'I', 'D', '3', 3, 0, 0}, testfile.Syncsafe(10+big+len(tit2)),
		[]byte("PRIV"), testfile.BE32(big), []byte{0, 0})
	id3Len := len(id3) + big + len(tit2)
	for _, c := range []struct {
		name  string
		src   zeroRun
		title string
	}{
		{"wav LIST/INFO chunk", zeroRun{riff("LIST", info, len(info)+big+len(inam)), inam, big}, "After"},
		{"wav id3 chunk", zeroRun{riff("id3 ", id3, id3Len), tit2, big}, "After"},
		{"aiff ID3 chunk", zeroRun{form("ID3 ", id3, id3Len), tit2, big}, "After"},
		{"aiff text chunk", zeroRun{form("NAME", []byte("Name"), 4+big), nil, big}, "Name"},
	} {
		_, err := wl.Parse(context.Background(), c.src, wl.WithLimits(wl.Limits{MaxAllocBytes: 64 << 20}))
		if !errors.Is(err, waxerr.ErrSizeTooLarge) {
			t.Errorf("%s at a 64 MiB limit: err = %v, want declared size too large", c.name, err)
		}
		doc, err := wl.Parse(context.Background(), c.src)
		if err != nil {
			t.Errorf("%s at the default limit: %v", c.name, err)
			continue
		}
		if got := doc.Fields().Title; got != c.title {
			t.Errorf("%s at the default limit: title = %q, want %q", c.name, got, c.title)
		}
	}
}

// zeroRun is a source of head, n zero bytes, then tail, that never holds the zeros, so a chunk past
// 64 MiB costs only what the parser reads.
type zeroRun struct {
	head, tail []byte
	n          int
}

func (z zeroRun) Size() int64 { return int64(len(z.head) + z.n + len(z.tail)) }

func (z zeroRun) ReadAt(p []byte, off int64) (int, error) {
	head, zeros := int64(len(z.head)), int64(len(z.head)+z.n)
	n := 0
	for n < len(p) && off >= 0 && off < z.Size() {
		var c int
		switch {
		case off < head:
			c = copy(p[n:], z.head[off:])
		case off < zeros:
			c = int(min(int64(len(p)-n), zeros-off))
			clear(p[n : n+c])
		default:
			c = copy(p[n:], z.tail[off-zeros:])
		}
		n += c
		off += int64(c)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// A file shorter than a tag header or footer holds no tag: a read past its end is not a refusal.
func TestShortFileHoldsNoTag(t *testing.T) {
	frame := append([]byte{0xFF, 0xFB, 0x90, 0x64}, make([]byte, 16)...)
	for _, limit := range []int64{int64(len(frame)), 0} {
		doc, err := wl.Parse(context.Background(), wl.BytesSource(frame), wl.WithLimits(wl.Limits{MaxAllocBytes: limit}))
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if doc.Format() != wl.FormatMP3 || len(doc.Tags().Keys()) != 0 {
			t.Errorf("limit %d: format %s with %d tags, want MP3 with none", limit, doc.Format(), len(doc.Tags().Keys()))
		}
	}
}

// partially-specified Limits keeps the default bound for any zero field rather than reading zero as
// "reject everything" (a literal zero MaxAllocBytes would refuse the STREAMINFO allocation).
func TestWithLimitsZeroFieldUsesDefault(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	// Only MaxDepth is set; MaxAllocBytes is zero and must fall back to the default.
	if _, err := wl.Parse(context.Background(), wl.BytesSource(src),
		wl.WithLimits(wl.Limits{MaxDepth: 64})); err != nil {
		t.Errorf("partial Limits{MaxDepth:64} should parse with the default MaxAllocBytes, got: %v", err)
	}
	// Symmetrically, only MaxAllocBytes set keeps the default MaxDepth.
	if _, err := wl.Parse(context.Background(), wl.BytesSource(src),
		wl.WithLimits(wl.Limits{MaxAllocBytes: 1 << 20})); err != nil {
		t.Errorf("partial Limits{MaxAllocBytes:1MiB} should parse with the default MaxDepth, got: %v", err)
	}
}

// A write from a Document whose parse a limit cut short commits and keeps what that parse left
// unread: Musepack chapters past the packet walk's or the chapter run's cap through a title edit,
// and an APEv2 cover past the item cap through a no-op copy.
func TestWriteFromCappedParseCommits(t *testing.T) {
	ctx := context.Background()
	audio := testfile.MPCAudio(64)
	viaSeekTable := testfile.MPCStream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable(),
		testfile.MPCTitled(0, "a"), testfile.MPCTitled(1152, "b"), testfile.MPCTitled(2304, "c"), testfile.MPCEnd())
	apeTag, err := ape.Render([]ape.Item{{Key: "Title", Value: "T"}, {Key: "Artist", Value: "A"}, {Key: "Album", Value: "B"},
		{Key: "Cover Art (Front)", Data: append([]byte("cover.png\x00"), testfile.PNG()...), Flags: 2}}, 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, title string // title is what the edit sets; empty for a no-op
		data        []byte
	}{
		{"chapters past the walk cap", "Capped", testfile.MPCChaptered()},
		{"chapters past the run cap", "Capped", viaSeekTable},
		{"cover past the item cap", "", slices.Concat(readFixture(t, notagsAPE), apeTag)},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, err := wl.Parse(ctx, wl.BytesSource(c.data), wl.WithLimits(wl.Limits{MaxElements: 2}))
			if err != nil {
				t.Fatal(err)
			}
			if !hasWarning(doc, wl.WarnElementCap) {
				t.Fatal("setup: the element cap should cut the parse short")
			}
			ed := doc.Edit()
			if c.title != "" {
				ed.Set(tag.Title, c.title)
			}
			plan, err := ed.Prepare()
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if _, res, err := plan.Execute(ctx, wl.WriteTo(&out, wl.BytesSource(c.data))); err != nil || !res.Committed {
				t.Fatalf("Execute = (committed %v, err %v), want it written", res.Committed, err)
			}
			src, got := mustParseBytes(t, c.data), mustParseBytes(t, out.Bytes())
			if !slices.Equal(got.Chapters(), src.Chapters()) || len(got.Pictures()) != len(src.Pictures()) {
				t.Errorf("output holds %d chapters and %d pictures, want the source's %d and %d",
					len(got.Chapters()), len(got.Pictures()), len(src.Chapters()), len(src.Pictures()))
			}
			if c.title == "" && !bytes.Equal(out.Bytes(), c.data) {
				t.Error("the no-op copy changed the bytes")
			}
			if c.title != "" && got.Fields().Title != c.title {
				t.Errorf("title = %q, want %q", got.Fields().Title, c.title)
			}
		})
	}
}

// withTrailingID3v1 appends a 128-byte ID3v1 tag to FLAC bytes.
func withTrailingID3v1(src []byte) []byte {
	return slices.Concat(src, testfile.ID3v1("Legacy Title", "", "", "", "", 0))
}

// withLeadingID3v2 prepends a minimal empty ID3v2 tag (10-byte header, size 0).
func withLeadingID3v2(src []byte) []byte { return append(testfile.ID3v2(4), src...) }

func TestLegacyTrailingID3v1(t *testing.T) {
	ctx := context.Background()
	src := withTrailingID3v1(readFixture(t, sampleFLAC))

	doc, err := wl.Parse(ctx, wl.BytesSource(src))
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(doc, wl.WarnTrailingID3v1) {
		t.Error("expected a trailing-ID3v1 warning")
	}

	// Default policy preserves it: a no-op write keeps the bytes.
	t.Run("preserve", func(t *testing.T) {
		plan, _ := doc.Edit().Prepare()
		out := applyToBytes(t, src, plan)
		if !bytes.HasSuffix(out, src[len(src)-128:]) {
			t.Error("default policy should preserve trailing ID3v1")
		}
	})

	// Strip policy removes it.
	t.Run("strip", func(t *testing.T) {
		plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
		if err != nil {
			t.Fatal(err)
		}
		if plan.IsNoOp() {
			t.Fatal("stripping a present legacy tag is not a no-op")
		}
		out := applyToBytes(t, src, plan)
		if bytes.Contains(out[len(out)-128:], []byte("TAG")) {
			t.Error("strip policy should remove trailing ID3v1")
		}
		// Audio and tags still intact.
		re := mustParseBytes(t, out)
		if re.Fields().Title != "Original Title" {
			t.Errorf("tags lost after strip: %q", re.Fields().Title)
		}
	})
}

func TestLegacyLeadingID3v2(t *testing.T) {
	ctx := context.Background()
	src := withLeadingID3v2(readFixture(t, sampleFLAC))
	// Detection looks past the leading ID3v2 to the fLaC marker; a real file lets the strip below
	// save back.
	path := writeTempFile(t, "lead.flac", src)

	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(doc, wl.WarnStrayLeadingID3) {
		t.Error("expected a stray-leading-ID3 warning")
	}
	if doc.Fields().Title != "Original Title" {
		t.Errorf("Title behind leading ID3 = %q", doc.Fields().Title)
	}

	plan, _ := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	out := readFixture(t, path)
	if !bytes.HasPrefix(out, []byte("fLaC")) {
		t.Error("strip policy should remove the leading ID3v2 (output should start with fLaC)")
	}
}

func TestWithPaddingAndReport(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().Set(tag.Title, "Padded").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 1000, Max: 1 << 20}))
	if err != nil {
		t.Fatal(err)
	}
	rep := plan.Report()
	if rep.PaddingAfter != 1000 {
		t.Errorf("PaddingAfter = %d, want 1000", rep.PaddingAfter)
	}
	if rep.NoOp || len(rep.Operations) == 0 {
		t.Errorf("report should describe a non-no-op write: %+v", rep)
	}
	if rep.BytesBefore == 0 || rep.BytesAfter == 0 {
		t.Errorf("report sizes unset: %+v", rep)
	}
}

func TestPaddingFloorWiring(t *testing.T) {
	src := readFixture(t, sampleFLAC)

	// A floor (Min == Target) grows the region: an edit that would fit the fixture's small existing
	// padding must reserve at least Min instead of reusing the smaller leftover. Min gates the
	// reuse branch.
	floorPlan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Floor").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 200000, Min: 200000, ReuseInPlace: true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := floorPlan.Report().PaddingAfter; got < 200000 {
		t.Fatalf("PaddingAfter under a floor = %d, want >= 200000", got)
	}
	grown := applyToBytes(t, src, floorPlan)

	// A region already past the floor reuses in place: re-editing the grown file under a small floor
	// keeps the large region (Min only floors a grow, it never forces a shrink toward Target).
	reusePlan, err := mustParseBytes(t, grown).Edit().Set(tag.Title, "Reuse").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 1000, Min: 1000, ReuseInPlace: true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := reusePlan.Report().PaddingAfter; got < 100000 {
		t.Errorf("PaddingAfter on reuse = %d, want the large region reused (not shrunk to 1000)", got)
	}
}

func TestMinimalPresetStripsAndShrinks(t *testing.T) {
	src := withTrailingID3v1(readFixture(t, sampleFLAC))
	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().Set(tag.Title, "Min").Prepare(wl.Minimal)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Report().PaddingAfter; got != 0 {
		t.Errorf("Minimal preset PaddingAfter = %d, want 0", got)
	}
	out := applyToBytes(t, src, plan)
	if bytes.Contains(out[len(out)-128:], []byte("TAG")) {
		t.Error("Minimal preset should strip trailing ID3v1")
	}
}

func hasWarning(doc *wl.Document, code wl.WarningCode) bool {
	for _, w := range doc.Warnings() {
		if w.Code == code {
			return true
		}
	}
	return false
}
