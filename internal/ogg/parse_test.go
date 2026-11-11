package ogg

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// nonAlignedOpus builds a non-standard Ogg Opus stream in which the first audio packet
// shares the OpusTags page, so the comment header does not end on a page boundary. No
// encoder produces this, but the reader must still account for that page's audio.
// Returns the stream and the audio payload bytes.
func nonAlignedOpus(t *testing.T) ([]byte, []byte) {
	t.Helper()
	const serial = 0x1234
	// OpusHead: version 1, 2 channels, pre-skip 0, input rate 48000, gain 0, family 0.
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xBB, 0x00, 0x00, 0, 0, 0}
	// OpusTags: empty vendor, zero comments.
	tags := []byte{'O', 'p', 'u', 's', 'T', 'a', 'g', 's', 0, 0, 0, 0, 0, 0, 0, 0}
	audio := []byte("AUDIOPKT!!") // 10 opaque audio bytes

	page0 := buildPage(flagBOS, 0, serial, 0, []byte{byte(len(head))}, head)
	// One page carrying both OpusTags and the audio packet (two lacing values),
	// granule 960, so the comment header ends mid-page.
	body1 := append(append([]byte{}, tags...), audio...)
	page1 := buildPage(0, 960, serial, 1, []byte{byte(len(tags)), byte(len(audio))}, body1)
	return append(page0, page1...), audio
}

func TestParseNonAlignedAudioEssence(t *testing.T) {
	stream, audio := nonAlignedOpus(t)
	media, err := parse(context.Background(), Codec{}, core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if media.Format != core.FormatOggOpus {
		t.Fatalf("format = %v", media.Format)
	}
	d := media.Native.(*doc)
	if d.clean {
		t.Error("stream sharing the comment page with audio must not be marked clean")
	}

	// The shared page's audio body must appear in the essence ranges; dropping it
	// yields zero ranges and a wrong digest.
	ranges := media.EssenceRanges()
	if len(ranges) != 1 {
		t.Fatalf("essence ranges = %d, want 1", len(ranges))
	}
	lo, hi := ranges[0][0], ranges[0][1]
	if hi-lo != int64(len(audio)) || string(stream[lo:hi]) != string(audio) {
		t.Errorf("essence range = stream[%d:%d] = %q, want %q", lo, hi, stream[lo:hi], audio)
	}
	// The final granule (960 samples at 48 kHz, minus zero pre-skip) gives a non-zero
	// duration.
	if media.Properties.First().Duration <= 0 {
		t.Error("duration should be derived from the shared page's granule")
	}
}

func TestPlanRefusesNonAlignedWrite(t *testing.T) {
	stream, _ := nonAlignedOpus(t)
	media, err := parse(context.Background(), Codec{}, core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	edited := media.Clone()
	edited.Tags.Set(tag.Title, "nope") // a real change, so it is not a no-op

	if _, err := NewOpus().Plan(context.Background(), media, edited, core.DefaultWriteOptions()); !errors.Is(err, waxerr.ErrUnalignedStream) {
		t.Errorf("non-page-aligned write should be refused with ErrUnalignedStream, got %v", err)
	}
	// The capabilities say so before an edit is planned.
	if caps := NewOpus().Capabilities(media, core.DefaultWriteOptions()); !caps.ReadOnly || !errors.Is(caps.ReadOnlyReason(), waxerr.ErrUnalignedStream) {
		t.Errorf("capabilities: read-only %v, reason %v; want read-only as unaligned", caps.ReadOnly, caps.ReadOnlyReason())
	}
}

// TestChapterCapabilityRepresentation pins Ogg's chapter capability: Vorbis and Opus
// store chapters as CHAPTERxxx comments with full read/write access.
func TestChapterCapabilityRepresentation(t *testing.T) {
	for _, c := range []Codec{NewVorbis(), NewOpus()} {
		caps := c.Capabilities(nil, core.DefaultWriteOptions())
		if got := caps.Chapters.Representation; got != "VorbisComment CHAPTERxxx" {
			t.Errorf("%v chapters Representation = %q, want %q", c.Format(), got, "VorbisComment CHAPTERxxx")
		}
		if caps.Chapters.Write != core.AccessFull || caps.Chapters.Read != core.AccessFull {
			t.Errorf("%v chapters access = read %v/write %v, want full/full", c.Format(), caps.Chapters.Read, caps.Chapters.Write)
		}
	}
}

// TestParseResyncsPastLeadingBytes: bytes ahead of the first page are skipped when a page whose
// checksum holds starts within the resync window, reported and kept by a rewrite. With no such
// page the bytes are no Ogg stream.
func TestParseResyncsPastLeadingBytes(t *testing.T) {
	stream := buildOpusStreamAt(t, 0, 2, 0)
	forged := slices.Clone(stream[:pageLen(t, stream)])
	forged[22] ^= 0xFF
	junk := func(n int) []byte { return bytes.Repeat([]byte{0xA5}, n) }
	parseBytes := func(b []byte) (*core.Media, error) {
		return parse(context.Background(), Codec{}, core.BytesSource(b), core.DefaultParseOptions())
	}

	for name, lead := range map[string][]byte{
		"a few bytes":                   junk(10),
		"a page with a broken checksum": slices.Concat(junk(3), forged, junk(3)),
		"the whole window":              junk(core.ResyncWindow),
	} {
		src := slices.Concat(lead, stream)
		m, err := parseBytes(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ws := core.WarningsWithCode(m.Warnings, core.WarnLeadingBytes); len(ws) != 1 || !strings.Contains(ws[0].Message, strconv.Itoa(len(lead))) {
			t.Errorf("%s: warnings = %v, want leading-bytes counting %d", name, m.Warnings, len(lead))
		}
		if v, _ := m.Tags.Get("TITLE"); len(v) != 1 || v[0] != "Song" {
			t.Errorf("%s: TITLE = %v", name, v)
		}
		if got := m.Native.Describe()[0]; got.Kind != "leading bytes" || got.Size != len(lead) {
			t.Errorf("%s: first native entry = %+v", name, got)
		}
		for editName, edit := range map[string]func(*core.Media){
			"tags":        func(e *core.Media) { e.Tags.Set("TITLE", "New") },
			"output gain": func(e *core.Media) { e.Properties.Tracks[0].OutputGain = -256 },
			"a spilling cover": func(e *core.Media) {
				e.Pictures = []core.Picture{{Type: 3, MIME: "image/png", Data: pattern(70000)}}
			},
		} {
			edited := m.Clone()
			edit(edited)
			plan, err := NewOpus().Plan(context.Background(), m, edited, core.DefaultWriteOptions())
			if err != nil {
				t.Fatalf("%s, %s: %v", name, editName, err)
			}
			out := renderPlan(t, src, plan)
			if !bytes.HasPrefix(out, lead) {
				t.Fatalf("%s, %s: the leading bytes do not open the rewritten file", name, editName)
			}
			for i, seq := range scanSeqs(t, out[len(lead):]) {
				if seq != uint32(i) {
					t.Fatalf("%s, %s: page %d has sequence number %d", name, editName, i, seq)
				}
			}
			got, err := parseBytes(out)
			if err != nil {
				t.Fatalf("%s, %s: re-parse: %v", name, editName, err)
			}
			if !slices.Equal(got.EssenceRanges(), plan.Result.EssenceRanges()) {
				t.Errorf("%s, %s: essence ranges %v, planned %v", name, editName, got.EssenceRanges(), plan.Result.EssenceRanges())
			}
		}
	}

	for name, b := range map[string][]byte{
		"a stream past the window": slices.Concat(junk(core.ResyncWindow+1), stream),
		"a broken checksum alone":  slices.Concat(junk(3), forged),
		"no page":                  junk(100),
	} {
		if _, err := parseBytes(b); !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "not an Ogg stream") {
			t.Errorf("%s: err = %v, want not an Ogg stream", name, err)
		}
	}
}

// TestWalkBoundsItsResyncs: the walk crosses a bounded number of gaps and tries a bounded number
// of candidates whose checksum does not hold; past either it stops, and what follows is trailing
// bytes.
func TestWalkBoundsItsResyncs(t *testing.T) {
	const serial = 0x4F4747
	audio := func(seq int) []byte {
		return buildPage(0, uint64(960*seq), serial, uint32(seq), []byte{1}, []byte{0x08})
	}
	header := buildOpusStreamAt(t, 0, 0, 0)
	forged := audio(9)
	forged[22] ^= 0xFF

	gapped := slices.Clone(header)
	for i := range maxGaps + 10 {
		gapped = append(append(gapped, '-'), audio(i+2)...)
	}
	forgeries := func(n int) []byte {
		out := slices.Concat(header, audio(2), []byte{'-'})
		for range n {
			out = append(out, forged...)
		}
		return append(out, audio(3)...)
	}

	for name, c := range map[string]struct {
		data  []byte
		pages int
		left  bool // the walk stopped short
	}{
		"the most gaps":      {gapped[:len(gapped)-10*(1+len(audio(0)))], maxGaps, false},
		"too many gaps":      {gapped, maxGaps, true},
		"the most forgeries": {forgeries(maxResyncMisses - 1), 2, false},
		"too many forgeries": {forgeries(maxResyncMisses), 1, true},
	} {
		m, err := parse(context.Background(), Codec{}, core.BytesSource(c.data), core.DefaultParseOptions())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := len(m.Native.(*doc).audioPages); got != c.pages {
			t.Errorf("%s: audio pages = %d, want %d", name, got, c.pages)
		}
		// What a read leaves behind is reported as unread, not as junk that belongs to nothing.
		if len(core.WarningsWithCode(m.Warnings, core.WarnTrailingBytes)) != 0 {
			t.Errorf("%s: warnings = %v, want no trailing-bytes", name, m.Warnings)
		}
		// A read that gave up leaves pages unread, so its essence stands for part of the audio.
		if err := m.Native.(*doc).RefuseEssence(); errors.Is(err, waxerr.ErrInvalidData) != c.left {
			t.Errorf("%s: essence refusal = %v, want one only when the walk stopped short", name, err)
		}
	}
}

// TestSkeletonAloneIsUnsupported: a file whose only streams are Skeletons holds nothing to read.
func TestSkeletonAloneIsUnsupported(t *testing.T) {
	fishead := append([]byte("fishead\x00"), make([]byte, 56)...)
	data := slices.Concat(
		buildPage(flagBOS, 0, 7, 0, []byte{byte(len(fishead))}, fishead),
		buildPage(0, 0, 7, 1, []byte{8}, []byte("fisbone\x00")),
	)
	_, err := parse(context.Background(), Codec{}, core.BytesSource(data), core.DefaultParseOptions())
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) || !strings.Contains(err.Error(), "Skeleton") {
		t.Errorf("err = %v, want unsupported format naming the Skeleton", err)
	}
}

// TestPageBytesShapedLikeATagStayAudio: what looks like an ID3v1 tag at the end of the file, but
// lies inside a page whose checksum holds, is that page's audio: no tag is read, and a legacy
// strip finds nothing to take.
func TestPageBytesShapedLikeATagStayAudio(t *testing.T) {
	const serial = 0x4F4747
	shaped := slices.Concat(make([]byte, 72), testfile.ID3v1("Title", "Artist", "", "", "", 12))
	stream := append(buildOpusStreamAt(t, 0, 1, 0), buildPage(0, 1920, serial, 3, []byte{byte(len(shaped))}, shaped)...)
	m, err := parse(context.Background(), Codec{}, core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if ws := core.WarningsWithCode(m.Warnings, core.WarnTrailingID3v1, core.WarnTruncatedAudio, core.WarnTrailingBytes); len(ws) != 0 {
		t.Errorf("warnings = %v, want none about a tail", ws)
	}
	if d := m.Native.(*doc); len(d.audioPages) != 2 || d.layout.HasTags() {
		t.Errorf("audio pages = %d, tail tags = %v; want both pages and no tag", len(d.audioPages), d.layout.HasTags())
	}
	opts := core.DefaultWriteOptions()
	opts.Legacy = core.LegacyStrip
	plan, err := NewOpus().Plan(context.Background(), m, m.Clone(), opts)
	if err != nil || !plan.NoOp {
		t.Errorf("a legacy strip: no-op = %v, err = %v; want nothing to strip", plan != nil && plan.NoOp, err)
	}

	// A tag behind that page cut short is a tag, though the page's length takes in some of
	// its bytes: the page's checksum no longer holds.
	cut := slices.Concat(stream[:len(stream)-60], shaped[72:])
	m, err = parse(context.Background(), Codec{}, core.BytesSource(cut), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(core.WarningsWithCode(m.Warnings, core.WarnTrailingID3v1)) != 1 || len(core.WarningsWithCode(m.Warnings, core.WarnTruncatedAudio)) != 1 {
		t.Errorf("warnings = %v, want a tag behind a truncated page", m.Warnings)
	}
}

// TestStreamsThatBeginTogetherAreBounded: the serials of the streams a file opens with are kept
// to tell their pages from a chained link's, so their number is bounded; a file that opens with
// more reads as chained on top of multiplexed, refused like either.
func TestStreamsThatBeginTogetherAreBounded(t *testing.T) {
	stream := buildOpusStreamAt(t, 0, 2, 0)
	page0 := pageLen(t, stream)
	withStreams := func(n int) *core.Media {
		data := slices.Clone(stream[:page0])
		for i := range n {
			data = append(data, buildPage(flagBOS, 0, uint32(1000+i), 0, []byte{4}, []byte("data"))...)
		}
		data = append(data, stream[page0:]...)
		for i := range n {
			data = append(data, buildPage(0, 0, uint32(1000+i), 1, []byte{4}, []byte("more"))...)
		}
		m, err := parse(context.Background(), Codec{}, core.BytesSource(data), core.DefaultParseOptions())
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	for _, c := range []struct {
		streams int
		chained bool
	}{{maxStreams - 1, false}, {maxStreams, true}} {
		m := withStreams(c.streams)
		if len(core.WarningsWithCode(m.Warnings, core.WarnMultiplexedStream)) != 1 {
			t.Errorf("%d streams beside the first: warnings = %v, want multiplexed-stream", c.streams, m.Warnings)
		}
		if got := len(core.WarningsWithCode(m.Warnings, core.WarnChainedStream)) == 1; got != c.chained {
			t.Errorf("%d streams beside the first: chained-stream reported = %v, want %v", c.streams, got, c.chained)
		}
		if v, _ := m.Tags.Get("TITLE"); len(v) != 1 || v[0] != "Song" {
			t.Errorf("%d streams beside the first: TITLE = %v", c.streams, v)
		}
	}
}

// TestPlanRefusesHeaderJunkPastTheResyncWindow: junk from among the header pages is written as
// one run, which a reader has to cross to reach the rebuilt pages. More of it than a reader
// skips makes the file read-only, whatever the edit.
func TestPlanRefusesHeaderJunkPastTheResyncWindow(t *testing.T) {
	const serial = 0x4F4747
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xBB, 0, 0, 0, 0, 0}
	page0, _ := paginateBOS(serial, 0, head)
	tags, n := paginate(serial, 1, [][]byte{opusTagsPacket("libopus 1.4", "TITLE=Song", "COMMENT="+strings.Repeat("c", 70000))})
	if n != 2 {
		t.Fatalf("setup: the comment header takes %d pages", n)
	}
	second := pageLen(t, tags)
	audio := buildPage(0, 960, serial, 3, []byte{1}, []byte{0x08})
	stream := func(junk int) []byte {
		j := bytes.Repeat([]byte{0xA5}, junk)
		return slices.Concat(page0, j, tags[:second], j, tags[second:], audio)
	}
	plan := func(src []byte, edit func(*core.Media)) (*core.WritePlan, error) {
		m, err := parse(context.Background(), Codec{}, core.BytesSource(src), core.DefaultParseOptions())
		if err != nil {
			t.Fatal(err)
		}
		edited := m.Clone()
		edit(edited)
		return NewOpus().Plan(context.Background(), m, edited, core.DefaultWriteOptions())
	}
	title := func(m *core.Media) { m.Tags.Set("TITLE", "New") }

	within := stream(core.ResyncWindow / 2)
	p, err := plan(within, title)
	if err != nil {
		t.Fatalf("two runs that fill the window: %v", err)
	}
	got, err := parse(context.Background(), Codec{}, core.BytesSource(renderPlan(t, within, p)), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("two runs that fill the window: re-parse: %v", err)
	}
	if got.AudioStart != p.Result.AudioStart || got.AudioEnd != p.Result.AudioEnd || !slices.Equal(got.EssenceRanges(), p.Result.EssenceRanges()) {
		t.Errorf("two runs that fill the window: audio reads at %d-%d, planned %d-%d", got.AudioStart, got.AudioEnd, p.Result.AudioStart, p.Result.AudioEnd)
	}

	over := stream(core.ResyncWindow/2 + 1)
	if _, err := plan(over, title); !errors.Is(err, waxerr.ErrUnalignedStream) || !strings.Contains(err.Error(), "between") {
		t.Errorf("two runs past the window: err = %v, want a refusal naming the bytes between the header pages", err)
	}
	if _, err := plan(over, func(m *core.Media) { m.Properties.Tracks[0].OutputGain = -256 }); !errors.Is(err, waxerr.ErrUnalignedStream) {
		t.Errorf("a gain edit past the window: err = %v, want the same refusal", err)
	}
	m, err := parse(context.Background(), Codec{}, core.BytesSource(over), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if caps := NewOpus().Capabilities(m, core.DefaultWriteOptions()); !caps.ReadOnly || !errors.Is(caps.ReadOnlyReason(), waxerr.ErrUnalignedStream) {
		t.Errorf("capabilities: read-only %v, reason %v", caps.ReadOnly, caps.ReadOnlyReason())
	}
}

// TestStrayAPEFooterReadsUnderALowLimit: an APE footer at the end of the file that places its tag
// back among the pages is no tag, and settling that reads none of the bytes it claims: the file
// parses under a limit below the claim, with the footer reported and kept as trailing bytes.
func TestStrayAPEFooterReadsUnderALowLimit(t *testing.T) {
	const serial, pages = 0x4F4747, 20
	data := buildOpusStreamAt(t, 0, 0, 0)
	for i := range pages {
		data = append(data, buildPage(0, uint64(960*(i+1)), serial, uint32(i+2), []byte{254}, append([]byte{0x08}, make([]byte, 253)...))...)
	}
	const claimed = 5000 // of the 5,640 bytes the audio pages hold
	data = append(data, slices.Concat([]byte("APETAGEX"), testfile.LE32(2000), testfile.LE32(claimed), testfile.LE32(1), make([]byte, 12))...)
	opts := core.DefaultParseOptions()
	opts.Limits.MaxAllocBytes = 4096
	m, err := parse(context.Background(), Codec{}, core.BytesSource(data), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(m.Native.(*doc).audioPages); got != pages {
		t.Errorf("audio pages = %d, want %d", got, pages)
	}
	if len(core.WarningsWithCode(m.Warnings, core.WarnMalformedTagEntry)) != 1 || len(core.WarningsWithCode(m.Warnings, core.WarnTrailingBytes)) != 1 {
		t.Errorf("warnings = %v, want the misplaced footer and the bytes it leaves", m.Warnings)
	}
}

// TestJunkBetweenPagesReadsUnderALowLimit: the search for a page behind junk and the check of
// the page it finds read through a small buffer, so a run of junk between pages longer than the
// allocation limit, and a page behind it longer than the limit, read as under the default.
func TestJunkBetweenPagesReadsUnderALowLimit(t *testing.T) {
	const serial = 0x4F4747
	big := append([]byte{0x08}, make([]byte, 8*255-1)...) // a page of 2 KiB
	lacing := append(bytes.Repeat([]byte{255}, 8), 0)
	opts := core.DefaultParseOptions()
	opts.Limits.MaxAllocBytes = 1024
	// A short run, runs that leave the capture pattern across two reads of the buffer, a run
	// over the limit, and one that fills the resync window.
	for _, junk := range []int{18, 1021, 1022, 1023, 1024, 1025, 3000, core.ResyncWindow} {
		stream := slices.Concat(
			buildOpusStreamAt(t, 0, 1, 0),
			bytes.Repeat([]byte{0xA5}, junk),
			buildPage(0, 1920, serial, 3, lacing, big),
		)
		m, err := parse(context.Background(), Codec{}, core.BytesSource(stream), opts)
		if err != nil {
			t.Errorf("%d bytes of junk: %v", junk, err)
			continue
		}
		if d := m.Native.(*doc); len(d.audioPages) != 2 || d.gapBytes != int64(junk) {
			t.Errorf("%d bytes of junk: audio pages = %d, gap = %d bytes", junk, len(d.audioPages), d.gapBytes)
		}
	}
}

// TestResyncTakesLittleMemory: detection asks every Ogg codec to look through its window, so the
// search works through a small buffer and copies neither the window nor a page.
func TestResyncTakesLittleMemory(t *testing.T) {
	window := slices.Concat(bytes.Repeat([]byte{0xA5}, 40000), buildOpusStreamAt(t, 0, 1, 0))
	if at, ok := NewOpus().Resync(window); !ok || at != 40000 {
		t.Fatalf("Resync = %d, %v, want the stream at 40000", at, ok)
	}
	var before, after runtime.MemStats
	const runs = 50
	runtime.ReadMemStats(&before)
	for range runs {
		NewVorbis().Resync(window)
	}
	runtime.ReadMemStats(&after)
	if per := (after.TotalAlloc - before.TotalAlloc) / runs; per > 16<<10 {
		t.Errorf("a search of a %d-byte window allocates %d bytes", len(window), per)
	}
}

// TestReadThatGaveUpKeepsStreamInfo: a read that stopped at its bounds has not seen the pages
// behind, so an Ogg FLAC keeps the sample count STREAMINFO declares and is not called truncated,
// and the file refuses writes.
func TestReadThatGaveUpKeepsStreamInfo(t *testing.T) {
	const serial, pages, declared = 0x57ac7557, maxGaps + 20, 4096 * (maxGaps + 21)
	stream := testfile.OggFLAC(testfile.CommentBlock("v", "TITLE=x"))
	binary.BigEndian.PutUint32(stream[pageFixedHdr+1+flacStreamInfoOff+14:], declared)
	testfile.OggSealPage(stream[:pageLen(t, stream)])
	for i := range pages {
		stream = append(append(stream, '-'), buildPage(0, uint64(4096*(i+2)), serial, uint32(i+3), []byte{5}, []byte("FRAME"))...)
	}
	m, err := parse(context.Background(), Codec{}, core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	d := m.Native.(*doc)
	if !d.gaveUp {
		t.Fatal("setup: the read went through")
	}
	if got := m.Properties.First().TotalSamples; got != declared || len(core.WarningsWithCode(m.Warnings, core.WarnTruncatedAudio)) != 0 {
		t.Errorf("total samples = %d, want STREAMINFO's %d and no truncated-audio (%v)", got, declared, m.Warnings)
	}
	if err := d.refuseWrite(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("refuseWrite = %v, want invalid data", err)
	}
}
