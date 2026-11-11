package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// adtsStream builds a synthetic raw-AAC (ADTS) stream: `frames` AAC-LC frames at 44.1 kHz with the
// given channel configuration, each a 7-byte fixed header (no CRC) plus payloadPerFrame zero
// payload bytes. That is enough to drive detection and the verbatim-copy write path without an
// encoder.
func adtsStream(chanConfig, frames, payloadPerFrame int) []byte {
	const hdr = 7 // ADTS fixed header without CRC
	frameLen := hdr + payloadPerFrame
	mk := func() []byte {
		b := make([]byte, frameLen)
		b[0] = 0xFF                                             // syncword high
		b[1] = 0xF1                                             // sync low | MPEG-4 | layer 00 | no CRC
		b[2] = 0x50 | byte((chanConfig>>2)&1)                   // profile=LC(1), sfIndex=4(44100), chan hi bit
		b[3] = byte((chanConfig&3)<<6) | byte((frameLen>>11)&3) // chan lo bits | frame_length hi
		b[4] = byte((frameLen >> 3) & 0xFF)                     // frame_length mid
		b[5] = byte((frameLen & 7) << 5)                        // frame_length lo | buffer fullness
		return b                                                // b[6] and payload stay zero
	}
	var out []byte
	for range frames {
		out = append(out, mk()...)
	}
	return out
}

// An ADTS file keeps an empty tag ahead of leading bytes as an MP3 does.
func TestAACClearingTheLastFrameKeepsAHeaderAheadOfLeadingBytes(t *testing.T) {
	emptiedTagCase(t, wl.FormatAAC, adtsStream(2, 20, 200))
}

func TestAACBareDetectAndCreateTags(t *testing.T) {
	data := adtsStream(2, 50, 200) // stereo
	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatAAC {
		t.Fatalf("format = %v, want AAC", doc.Format())
	}
	if doc.Tags().Len() != 0 {
		t.Fatalf("bare ADTS should have no tags, got %d", doc.Tags().Len())
	}
	if tr := doc.Properties().First(); tr.Channels != 2 || tr.SampleRate != 44100 || tr.Codec != "AAC" || tr.CodecProfile != "AAC LC" {
		t.Errorf("track = %+v, want 2ch/44100/AAC (profile AAC LC)", tr)
	}

	plan, err := doc.Edit().Set(tag.Title, "From Scratch").Set(tag.Artist, "Synth").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("adding tags to a tagless file is not a no-op")
	}
	out := applyToBytes(t, data, plan)
	// A brand-new AAC tag is ID3v2.4 (the plan's default for raw AAC).
	if string(out[:3]) != "ID3" || out[3] != 4 {
		t.Errorf("new tag should be ID3v2.4, got %q ver %d", out[:3], out[3])
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "From Scratch" {
		t.Errorf("title = %q", re.Fields().Title)
	}
	if a := re.Fields().Artists; len(a) != 1 || a[0] != "Synth" {
		t.Errorf("artists = %v", a)
	}
}

func TestAACFrontID3Detection(t *testing.T) {
	data := append(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Tagged ADTS")), adtsStream(2, 20, 200)...)
	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatAAC {
		t.Fatalf("ID3-prefixed ADTS detected as %v, want AAC", doc.Format())
	}
	if doc.Fields().Title != "Tagged ADTS" {
		t.Errorf("title = %q", doc.Fields().Title)
	}
	// A tag edit preserves the ADTS bytes verbatim (essence stable).
	before := essenceOf(t, data)
	plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if after := essenceOf(t, out); !before.Equal(after) {
		t.Error("ADTS essence changed across a tag edit")
	}
}

// ADTS behind an ID3v2 tag and a gap reads as AAC from the first tag: every frame is counted from
// the first one, and the bytes before it are reported, are not legacy, are kept after the tag by an
// edit and are left out of the essence, whose digest is the bare stream's.
func TestAACBehindTagGap(t *testing.T) {
	lead := testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Tagged ADTS"))
	const frames = 20
	stream := adtsStream(2, frames, 200)
	essence := essenceOf(t, stream)
	for _, c := range []struct {
		name    string
		between []byte
	}{
		{"ten zero bytes", make([]byte, 10)},
		{"one byte", []byte{1}},
		{"two bytes", []byte{1, 2}},
		{"three bytes", []byte{1, 2, 3}},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := slices.Concat(lead, c.between, stream)
			doc := mustParseBytes(t, data)
			if doc.Format() != wl.FormatAAC {
				t.Fatalf("detected as %v, want AAC", doc.Format())
			}
			if got := doc.Properties().First().TotalSamples; got != frames*1024 {
				t.Errorf("TotalSamples = %d, want %d (every frame from the first one)", got, frames*1024)
			}
			if !hasWarning(doc, wl.WarnLeadingBytes) {
				t.Errorf("no leading-bytes warning; got %v", doc.Warnings())
			}
			if got := doc.Fields(); got.Title != "Tagged ADTS" || got.Album != "" {
				t.Errorf("title %q, album %q; want the first tag's title alone", got.Title, got.Album)
			}
			native := doc.Native().Describe()
			if n := len(native); n < 2 || native[n-2].Kind != "leading bytes" || native[n-2].Size != len(c.between) ||
				native[n-1].Kind != "ADTS audio" || native[n-1].Size != len(stream) {
				t.Errorf("native view = %+v, want %d leading bytes, then %d bytes of ADTS audio", native, len(c.between), len(stream))
			}
			if !essenceOf(t, data).Equal(essence) {
				t.Error("the digest is not the bare stream's")
			}
			if doc.HasOpaqueLegacyContent() {
				t.Error("the bytes before the first frame count as legacy content, though they go with the native tag")
			}
			plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, data, plan)
			if !bytes.HasSuffix(out, slices.Concat(c.between, stream)) {
				t.Error("the edit did not keep the bytes between the tag and the first frame")
			}
			if !essenceOf(t, out).Equal(essence) {
				t.Error("the essence changed across a tag edit")
			}
		})
	}
}

// Tags after an ADTS stream are legacy: an APE tag and stacked ID3v1 tags are read into the family
// view and reported, kept by an edit and removed by a legacy strip, which names the values only they
// held. The digest is the bare stream's throughout.
func TestAACTrailingTagsAreLegacy(t *testing.T) {
	stream := adtsStream(2, 20, 200)
	trailer := slices.Concat(apeTag(map[string]string{"Album": "APE Album"}), stackedV1())
	src := slices.Concat(stream, trailer)
	essence := essenceOf(t, stream)

	doc := mustParseBytes(t, src)
	if doc.Tags().Len() != 0 {
		t.Errorf("tags = %v, want none: the trailing tags are legacy", doc.Tags().Keys())
	}
	for _, code := range []wl.WarningCode{wl.WarnLegacyAPE, wl.WarnTrailingID3v1} {
		if !hasWarning(doc, code) {
			t.Errorf("no %s warning; got %v", code, doc.Warnings())
		}
	}
	if !hasFamily(doc, wl.FamilyAPEv2, tag.Album) || !hasFamily(doc, wl.FamilyID3v1, tag.Comment) {
		t.Errorf("the trailing tags are not in the family view: %+v", doc.Families())
	}
	if !essenceOf(t, src).Equal(essence) {
		t.Error("the trailing tags changed the digest")
	}

	plan, err := doc.Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, src, plan); !bytes.HasSuffix(out, slices.Concat(stream, trailer)) {
		t.Error("an edit did not keep the trailing tags after the stream")
	}

	plan, err = doc.Edit().Set(tag.Title, "Edited").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnLegacyStripDropped)
	if want := []tag.Key{tag.Album, tag.Comment}; !ok || !slices.Equal(w.Keys, want) {
		t.Errorf("strip warning keys = %v (present %v), want %v", w.Keys, ok, want)
	}
	stripped := applyToBytes(t, src, plan)
	legacy := func(f wl.FamilyValue) bool { return f.Legacy }
	if !bytes.HasSuffix(stripped, stream) || slices.ContainsFunc(mustParseBytes(t, stripped).Families(), legacy) {
		t.Error("the strip did not remove every trailing tag")
	}
	if !essenceOf(t, stripped).Equal(essence) {
		t.Error("the digest changed across the strip")
	}
}

// layer-bit split: an ADTS stream is AAC (layer 00, which MP3 frame decoding rejects), and an MPEG
// stream stays MP3.
func TestAACMP3MutualExclusivity(t *testing.T) {
	if doc := mustParseBytes(t, adtsStream(2, 8, 200)); doc.Format() != wl.FormatAAC {
		t.Errorf("ADTS stream detected as %v, want AAC", doc.Format())
	}
	if doc := mustParseFile(t, notagsMP3); doc.Format() != wl.FormatMP3 {
		t.Errorf("MP3 detected as %v, want MP3", doc.Format())
	}
}

// signature-only front-ID3 peek: a leading ID3 followed by non-ADTS bytes in a file named .aac
// stays MP3. Only a signature behind the tag may override the sniffed ID3; the extension may not.
func TestAACExtensionDoesNotOverrideSniff(t *testing.T) {
	data := append(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "x")), 0, 1, 2, 3, 4, 5, 6, 7) // no ADTS sync
	path := writeTempFile(t, "garbage.aac", data)
	doc, err := wl.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Format() != wl.FormatMP3 {
		t.Errorf("ID3 + non-ADTS named .aac = %v, want MP3 (extension must not override the sniff)", doc.Format())
	}
	if doc.Fields().Title != "x" {
		t.Errorf("front ID3 should still be read: title = %q", doc.Fields().Title)
	}
}

// The frame walk that ends an ADTS stream before its tags reads the frames in 64 KiB windows, so a
// long stream costs a few reads per window, not one per frame. Nothing else walks them: at 96 kHz,
// whose doubled rate has no index, no SBR probe runs, and of AAC Main frames, which abstain from its
// vote, the probe reads only the first few. Measured again past a tag the stream goes on after, the
// walk resumes where it stopped instead of starting over. It goes on past the ID3 tags between
// joined streams within the window it holds, however often they come, and ends at the first join
// past MaxElements.
func TestAACFrameWalkReadsInWindows(t *testing.T) {
	const frames = 20000
	cleared := func(bits byte, n int) []byte { // header bits cleared in the third byte of every frame
		s := adtsStream(2, n, 200)
		for i := 0; i < len(s); i += 207 {
			s[i+2] &^= bits
		}
		return s
	}
	second := cleared(0x3C, frames+10)
	unit := slices.Concat(adtsStream(2, 2, 0), []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 0}) // two frames, an empty tag
	units := 1 << 20 / len(unit)
	joined := slices.Concat(bytes.Repeat(unit, units), adtsStream(2, 2, 0))
	for _, c := range []struct {
		name   string
		stream []byte // before the ID3v1 tag
		frames int    // the frames counted
		limits wl.Limits
		tags   int // ID3 tags reported between joined streams
		junk   int // trailing bytes reported
	}{
		{"96 kHz", cleared(0x3C, frames), frames, wl.Limits{}, 0, 0},   // sampling-frequency index 0
		{"AAC Main", cleared(0xC0, frames), frames, wl.Limits{}, 0, 0}, // profile 0
		{"96 kHz, a tag before a second stream", slices.Concat(second[:frames*207], headedAPETag(t, "Title", "Coda"), second[frames*207:]),
			frames + 10, wl.Limits{}, 0, 0},
		{"two frames, then an empty ID3v2 tag, repeated", joined, 2*units + 2, wl.Limits{}, units, 0},
		// The walk ends at the tag of unit 1000, 14 bytes into it.
		{"joined more often than MaxElements", joined, 2 * 1001, wl.Limits{MaxElements: 1000}, 1000, len(joined) - (1000*len(unit) + 14)},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := slices.Concat(c.stream, testfile.ID3v1("V1", "", "", "", "", 255))
			read := &byteCounter{ReaderAtSized: wl.BytesSource(data)}
			src := &countingSource{ReaderAtSized: read}
			doc, err := wl.Parse(context.Background(), src, wl.WithLimits(c.limits))
			if err != nil {
				t.Fatal(err)
			}
			if got := doc.Properties().First().TotalSamples; got != uint64(c.frames)*1024 || !hasWarning(doc, wl.WarnTrailingID3v1) {
				t.Errorf("TotalSamples = %d, warnings %v; want %d frames and the ID3v1 tag after them", got, doc.Warnings(), c.frames)
			}
			windows := (len(c.stream) + 64<<10 - 1) / (64 << 10)
			if src.reads > windows+32 || read.n > 2*int64(len(data)) {
				t.Errorf("%d reads of %d bytes for %d windows of frames, want at most %d reads of %d bytes", src.reads, read.n, windows, windows+32, 2*len(data))
			}
			var tags, junk []string
			for _, w := range doc.Warnings() {
				switch {
				case w.Code != wl.WarnTrailingBytes:
				case strings.Contains(w.Message, "between joined"):
					tags = append(tags, w.Message)
				case strings.Contains(w.Message, "belong to no tag"):
					junk = append(junk, w.Message)
				}
			}
			if len(tags) != min(c.tags, 1) || c.tags > 0 && !strings.HasPrefix(tags[0], fmt.Sprintf("%d ID3 tag(s) between joined ADTS streams", c.tags)) {
				t.Errorf("joined-stream warnings %q, want one for %d tags", tags, c.tags)
			}
			if len(junk) != min(c.junk, 1) || c.junk > 0 && !strings.HasPrefix(junk[0], fmt.Sprintf("%d byte(s) after the ADTS stream", c.junk)) {
				t.Errorf("junk warnings %q, want one for %d bytes", junk, c.junk)
			}
		})
	}
}

// An APE tag that holds together wins over the audio the ADTS frames claim past its start: the audio
// ends at the tag, which is read and removed by a strip, and the frame that runs into it is reported
// truncated by the bytes it declares past the audio. That frame keeps its header, so the stripped file
// reads the junk after the tag as the rest of it, and the essence holds that junk before the strip too.
// Junk after the tag at least as long as the frame lacks completes it there: the stream goes on after
// the tag, and nothing is truncated.
func TestAACTagTheFramesRunIntoEndsTheAudio(t *testing.T) {
	big := headedAPETag(t, "Title", "Coda", "Comment", strings.Repeat("c", 600))
	cut := adtsStream(2, 21, 1000)[:20*1007+300] // the last frame lacks 707 bytes
	for _, c := range []struct {
		name   string
		audio  []byte // the frames up to the tag
		junk   int    // after the tag
		past   int    // the bytes the frame the tag cuts declares past it, 0 for none reported
		frames int    // the frames counted
	}{
		{"a frame ending in the junk after the tag", cut, 10, 707, 20},
		{"a frame declared past the end of the file", adtsStream(2, 21, 2000)[:20*2007+300], 10, 1707, 20},
		{"a frame one byte longer than the junk after the tag", cut, 706, 707, 20},
		{"a frame the junk after the tag completes", cut, 707, 0, 21},
		{"a frame the junk after the tag completes, with junk to spare", cut, 800, 0, 21},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := slices.Concat(c.audio, big, trailerJunk(c.junk))
			doc := mustParseBytes(t, data)
			if size := nativeSize(doc, "ADTS audio"); size != len(c.audio) || nativeSize(doc, "APEv2") != len(big) {
				t.Errorf("native view %+v; want %d bytes of ADTS audio, then the tag", doc.Native().Describe(), len(c.audio))
			}
			if !hasWarning(doc, wl.WarnLegacyAPE) || !hasFamily(doc, wl.FamilyAPEv2, tag.Title) {
				t.Errorf("the APE tag was not read: warnings %v", doc.Warnings())
			}
			tw, ok := docWarning(doc, wl.WarnTruncatedAudio)
			if want := fmt.Sprintf("the last ADTS frame, by %d byte(s), declares more audio than the file holds", c.past); ok != (c.past > 0) ||
				ok && !strings.HasPrefix(tw.Message, want) {
				t.Errorf("truncated-audio warning %q (present %v), want it to begin %q (or none for 0 bytes)", tw.Message, ok, want)
			}
			if got := doc.Properties().First().TotalSamples; got != uint64(c.frames)*1024 {
				t.Errorf("TotalSamples = %d, want %d frames'", got, c.frames)
			}
			stripped := slices.Concat(c.audio, trailerJunk(c.junk))
			essence := essenceOf(t, stripped)
			if !essenceOf(t, data).Equal(essence) {
				t.Error("the digest is not the one the frames and the junk after the tag give")
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
				t.Errorf("the strip left %d bytes with %d APETAGEX markers, or another digest; want the frames and the junk alone",
					len(w.b), bytes.Count(w.b, []byte("APETAGEX")))
			}
		})
	}
}

// eofAtEnd is a source whose read that reaches its end returns io.EOF with the bytes, as
// io.ReaderAt allows.
type eofAtEnd struct{ wl.ReaderAtSized }

func (s eofAtEnd) ReadAt(p []byte, off int64) (int, error) {
	n, err := s.ReaderAtSized.ReadAt(p, off)
	if err == nil && off+int64(n) == s.Size() {
		err = io.EOF
	}
	return n, err
}

// Joined ADTS streams read alike from a source whose read that reaches the end of the file returns
// io.EOF with its bytes: the walk goes on past the tags between them, the second stream audio either
// way.
func TestAACJoinedStreamsReadAlikeFromAnEOFAtEndSource(t *testing.T) {
	aac := readFixture(t, notagsAAC)
	v1 := testfile.ID3v1("V1", "", "", "", "", 255)
	for _, c := range []struct {
		name  string
		data  []byte
		audio int // the bytes of ADTS audio
	}{
		{"two ffmpeg-made files", slices.Concat(ffmpegTag(), aac, ffmpegTag(), aac), 2*len(aac) + len(ffmpegTag())},
		{"an ID3v1 tag between two streams", slices.Concat(aac, v1, aac), 2*len(aac) + len(v1)},
		{"two ffmpeg-made files, an ID3v1 tag after", slices.Concat(ffmpegTag(), aac, ffmpegTag(), aac, v1), 2*len(aac) + len(ffmpegTag())},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := mustParseBytes(t, c.data)
			got, err := wl.Parse(context.Background(), eofAtEnd{wl.BytesSource(c.data)})
			if err != nil {
				t.Fatal(err)
			}
			if size := nativeSize(got, "ADTS audio"); size != c.audio || !reflect.DeepEqual(got.Native().Describe(), want.Native().Describe()) {
				t.Errorf("%d bytes of ADTS audio, native view %+v; want %d, %+v", size, got.Native().Describe(), c.audio, want.Native().Describe())
			}
			if !reflect.DeepEqual(got.Properties(), want.Properties()) || !reflect.DeepEqual(got.Warnings(), want.Warnings()) {
				t.Errorf("properties %+v, warnings %v; want %+v, %v", got.Properties(), got.Warnings(), want.Properties(), want.Warnings())
			}
			digest, err := got.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(c.data)))
			if err != nil || !digest.Equal(essenceOf(t, c.data)) {
				t.Errorf("digest %v, %v; want the one a plain source gives", digest, err)
			}
		})
	}
}

// sparse is a source of size bytes, zero but where its pieces sit.
type sparse struct {
	size   int64
	pieces map[int64][]byte
}

func (s sparse) Size() int64 { return s.size }

func (s sparse) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	n := int(max(min(int64(len(p)), s.size-off), 0))
	clear(p[:n])
	for at, b := range s.pieces {
		if lo, hi := max(at, off), min(at+int64(len(b)), off+int64(n)); lo < hi {
			copy(p[lo-off:hi-off], b[lo-at:hi-at])
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Eight of the largest ID3v2 tags, more than 2 GiB, between two joined ADTS streams are passed by
// their headers: the walk goes on where the second stream starts, on 32-bit platforms too.
func TestAACJoinPastTwoGiBOfTags(t *testing.T) {
	frames := adtsStream(2, 2, 0)
	src := sparse{pieces: map[int64][]byte{0: frames}}
	at := int64(len(frames))
	for range 8 {
		src.pieces[at] = []byte{'I', 'D', '3', 4, 0, 0, 0x7F, 0x7F, 0x7F, 0x7F}
		at += 10 + 1<<28 - 1
	}
	src.pieces[at] = frames
	src.size = at + int64(len(frames))
	doc, err := wl.Parse(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Properties().First().TotalSamples; got != 4*1024 {
		t.Errorf("TotalSamples = %d, warnings %v; want the four frames on both sides of the tags", got, doc.Warnings())
	}
	for _, w := range doc.Warnings() {
		if strings.Contains(w.Message, "belong to no tag") {
			t.Errorf("junk reported: %q", w.Message)
		}
	}
}

// The ADTS frames end the audio. A last frame the end of the file or the tags cut short stays audio,
// as an MP3's does, while a bad header ends the stream there and leaves the rest as junk.
func TestAACStreamEnd(t *testing.T) {
	stream := adtsStream(2, 20, 200)
	cut := stream[:len(stream)-100]
	corrupt := slices.Clone(stream)
	corrupt[10*207] = 0xA5 // the eleventh frame loses its sync word
	for _, c := range []struct {
		name  string
		data  []byte
		audio []byte // what the essence holds
		junk  int    // trailing bytes reported, 0 for none
	}{
		{"last frame cut by the end of the file", cut, cut, 0},
		{"last frame cut by an ID3v1 tag", slices.Concat(cut, testfile.ID3v1("V1", "", "", "", "", 255)), cut, 0},
		{"bad header mid-stream", corrupt, corrupt[:10*207], len(corrupt) - 10*207},
		// A tag goes into the stream only when frames of the same stream follow it.
		{"a tag no frames follow", slices.Concat(stream, ffmpegTag(), trailerJunk(500)), stream, len(ffmpegTag()) + 500},
		{"a tag before frames of another stream", slices.Concat(stream, ffmpegTag(), adtsStream(1, 20, 200)), stream,
			len(ffmpegTag()) + len(adtsStream(1, 20, 200))},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, c.data)
			audio := -1
			for _, e := range doc.Native().Describe() {
				if e.Kind == "ADTS audio" {
					audio = e.Size
				}
			}
			w, junk := docWarning(doc, wl.WarnTrailingBytes)
			if audio != len(c.audio) || junk != (c.junk > 0) || junk && !strings.HasPrefix(w.Message, fmt.Sprintf("%d byte(s) ", c.junk)) {
				t.Errorf("ADTS audio of %d bytes, junk warning %q (present %v); want %d bytes of audio and %d of junk",
					audio, w.Message, junk, len(c.audio), c.junk)
			}
			if !essenceOf(t, c.data).Equal(essenceOf(t, c.audio)) {
				t.Error("the digest is not that of the audio alone")
			}
		})
	}
}

// A second ID3v2 tag chained behind an ADTS stream's front tag is a duplicate, as after MP3: first
// wins, the second is reported and out of the essence, and an edit drops it, warning about the
// value only it held, not the title the written tag holds too.
func TestAACDuplicateLeadingTag(t *testing.T) {
	stream := adtsStream(2, 20, 200)
	src := slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "First")),
		testfile.ID3v2(4, testfile.TextFrame(4, "TALB", "Second Album"), testfile.TextFrame(4, "TIT2", "First")), stream)
	doc := mustParseBytes(t, src)
	if doc.Format() != wl.FormatAAC || !hasWarning(doc, wl.WarnDuplicateTagBlock) || hasWarning(doc, wl.WarnLeadingBytes) {
		t.Fatalf("format %v, warnings %v; want AAC with duplicate-tag-block and no leading-bytes", doc.Format(), doc.Warnings())
	}
	if f := doc.Fields(); f.Title != "First" || f.Album != "" {
		t.Errorf("title %q, album %q; want the first tag's alone", f.Title, f.Album)
	}
	essence := essenceOf(t, stream)
	if !essenceOf(t, src).Equal(essence) {
		t.Error("the digest is not the bare stream's")
	}
	plan, err := doc.Edit().Set(tag.Artist, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Report().Warnings, func(w wl.Warning) bool {
		return w.Code == wl.WarnDuplicateTagBlockDropped && slices.Equal(w.Keys, []tag.Key{tag.Album})
	}) {
		t.Errorf("plan warnings %v, want duplicate-tag-block-dropped naming ALBUM", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.HasSuffix(out, stream) || bytes.Contains(out, []byte("Second Album")) || !essenceOf(t, out).Equal(essence) {
		t.Error("the edit did not drop the duplicate and keep the stream and its digest")
	}
}

// ADTS behind an ID3v2.5 front tag reads as after MP3: the tag is ignored and reported, the stream
// keeps its digest, and an edit writes an ID3v2.4 tag in its place.
func TestAACUnknownID3Version(t *testing.T) {
	stream := adtsStream(2, 20, 200)
	tagBytes := testfile.ID3v2(5, testfile.TextFrame(4, "TIT2", "Future"))
	src := slices.Concat(tagBytes, stream)
	doc := mustParseBytes(t, src)
	if doc.Format() != wl.FormatAAC || !hasWarning(doc, wl.WarnMalformedTagEntry) || !essenceOf(t, src).Equal(essenceOf(t, stream)) {
		t.Fatalf("format %v, warnings %v; want AAC, the tag reported, the bare stream's digest", doc.Format(), doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("plan warnings %v, want malformed-tag-entry-dropped", plan.Report().Warnings)
	}
	if out := applyToBytes(t, src, plan); out[3] != 4 || !bytes.HasSuffix(out, stream) {
		t.Errorf("output tag version %d; want an ID3v2.4 tag ahead of the stream", out[3])
	}
}
