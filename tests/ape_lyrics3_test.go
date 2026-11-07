package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// apeTagStart is where the APE tag whose footer ends b starts, its header record included, or -1
// when no footer ends b.
func apeTagStart(b []byte) int {
	n := len(b)
	if n < 32 || string(b[n-32:n-24]) != "APETAGEX" {
		return -1
	}
	start := n - int(binary.LittleEndian.Uint32(b[n-20:n-16]))
	if binary.LittleEndian.Uint32(b[n-12:n-8])&(1<<31) != 0 {
		start -= 32
	}
	return start
}

// headedAPETag renders an APEv2 tag with a header record holding the text items kv lists as key,
// value pairs.
func headedAPETag(t *testing.T, kv ...string) []byte {
	t.Helper()
	var items []ape.Item
	for i := 0; i+1 < len(kv); i += 2 {
		items = append(items, ape.Item{Key: kv[i], Value: kv[i+1]})
	}
	b, err := ape.Render(items, 2000, true)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// trailerJunk is n bytes that open no tag, Lyrics3 block, WavPack block or Musepack packet.
func trailerJunk(n int) []byte { return bytes.Repeat([]byte{0xA5}, n) }

// docWarning returns the document's first warning with the given code, and whether it has one.
func docWarning(doc *wl.Document, code wl.WarningCode) (wl.Warning, bool) {
	for _, w := range doc.Warnings() {
		if w.Code == code {
			return w, true
		}
	}
	return wl.Warning{}, false
}

// An APE tag ahead of a Lyrics3 block and an ID3v1 tag is read, and the Lyrics3 block is reported,
// never read. An edit leaves one APE tag, its header and footer, with the Lyrics3 block and the
// ID3v1 tag after it byte for byte.
func TestAPETagAheadOfLyrics3(t *testing.T) {
	lyrics := testfile.Lyrics3v2([2]string{"IND", "00"}, [2]string{"LYR", "[00:01]first line"})
	v1 := testfile.ID3v1("V1 Title", "", "", "", "", 255)
	// notags.wv ends in a tag holding the encoder stamp, which the tag after it, the one readers
	// find, repeats.
	src := slices.Concat(readFixture(t, notagsWV), headedAPETag(t, "Title", "APE Title", "encoder", "Lavf62.3.100"), lyrics, v1)

	doc := mustParseBytes(t, src)
	if got := doc.Fields().Title; got != "APE Title" {
		t.Errorf("title = %q, want the APE tag's", got)
	}
	for _, code := range []wl.WarningCode{wl.WarnTrailingLyrics3, wl.WarnTrailingID3v1} {
		if !hasWarning(doc, code) {
			t.Errorf("no %s warning; got %v", code, doc.Warnings())
		}
	}

	plan, err := doc.Edit().Set(tag.Album, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if n := bytes.Count(out, []byte("APETAGEX")); n != 2 {
		t.Errorf("the edited file holds %d APETAGEX records, want the one tag's header and footer", n)
	}
	legacy := slices.Concat(lyrics, v1)
	if !bytes.HasSuffix(out, legacy) || apeTagStart(out[:len(out)-len(legacy)]) < 0 {
		t.Error("the edit did not leave the APE tag followed by the Lyrics3 block and the ID3v1 tag")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "APE Title" || re.Fields().Album != "Edited" {
		t.Errorf("after the edit: title %q, album %q; want APE Title and Edited", re.Fields().Title, re.Fields().Album)
	}
	if !hasWarning(re, wl.WarnTrailingLyrics3) {
		t.Error("the edited file no longer reports its Lyrics3 block")
	}
}

// Junk after the APE tag is reported and kept, and a rewrite moves it ahead of the tag, which ends the
// file again so readers find it. The essence ends with the stream, so the junk never changes the
// digest.
func TestJunkAfterTheTagMovesAheadOfIt(t *testing.T) {
	for _, path := range []string{sampleWV, sampleAPE, chaptersMPC} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			clean := readFixture(t, path)
			stuck := trailerJunk(300)
			src := slices.Concat(clean, stuck)
			want := mustParseBytes(t, clean)
			essence := essenceOf(t, clean)

			doc := mustParseBytes(t, src)
			if !doc.Tags().Equal(want.Tags()) {
				t.Errorf("tags = %v, want the clean file's %v", doc.Tags().Keys(), want.Tags().Keys())
			}
			if !hasWarning(doc, wl.WarnTrailingBytes) {
				t.Errorf("no trailing-bytes warning; got %v", doc.Warnings())
			}
			if !essenceOf(t, src).Equal(essence) {
				t.Error("the junk changed the digest")
			}

			plan, err := doc.Edit().Set(tag.Album, "Edited").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, src, plan)
			if start := apeTagStart(out); start < len(stuck) || !bytes.Equal(out[start-len(stuck):start], stuck) {
				t.Error("the junk does not sit right ahead of the rewritten tag at the end of the file")
			}
			re := mustParseBytes(t, out)
			if re.Fields().Album != "Edited" || !hasWarning(re, wl.WarnTrailingBytes) {
				t.Errorf("after the edit: album %q, warnings %v; want Edited and trailing-bytes", re.Fields().Album, re.Warnings())
			}
			if !essenceOf(t, out).Equal(essence) {
				t.Error("the digest changed across the edit")
			}
		})
	}
}

// A Lyrics3 block is legacy content nothing reads, so nothing proves it redundant: lint --fix keeps
// it with the ID3v1 tag after it, which the store makes redundant, and an explicit strip warns that it
// drops them both. So after a WavPack stream, whose store is the APE tag, after MP3 and ADTS, whose
// store is the front ID3v2 tag, and after FLAC, whose store is its Vorbis comments.
func TestLyrics3KeptByLintFix(t *testing.T) {
	lyrics := testfile.Lyrics3v1("la la la")
	v1 := testfile.ID3v1("Sample Title", "", "", "", "", 255) // the store holds the same title
	front := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Sample Title"))
	for _, c := range []struct {
		name string
		lead []byte
	}{
		{"sample.wv", readFixture(t, sampleWV)},
		{"notags.mp3", slices.Concat(front, readFixture(t, notagsMP3))},
		{"notags.aac", slices.Concat(front, readFixture(t, notagsAAC))},
		{"flac", testfile.TaggedFLAC("test", "TITLE=Sample Title")},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := slices.Concat(c.lead, lyrics, v1)
			doc := mustParseBytes(t, src)
			if !doc.HasOpaqueLegacyContent() || !lintHasCode(doc, "legacy-opaque-content") {
				t.Fatalf("the Lyrics3 block is not opaque legacy content; lint = %v", doc.Lint())
			}
			legacy := slices.Concat(lyrics, v1)
			if out := applyLintFix(t, src); !bytes.HasSuffix(out, legacy) {
				t.Error("lint --fix dropped the Lyrics3 block or the ID3v1 tag")
			}

			plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
			if err != nil {
				t.Fatal(err)
			}
			if !planHasWarning(plan, wl.WarnLegacyStripDropped) {
				t.Error("a strip of the Lyrics3 block does not warn legacy-strip-dropped")
			}
			stripped := applyToBytes(t, src, plan)
			re := mustParseBytes(t, stripped)
			if bytes.Contains(stripped, []byte("LYRICSBEGIN")) || hasWarning(re, wl.WarnTrailingID3v1) {
				t.Error("the strip kept the Lyrics3 block or the ID3v1 tag")
			}
			if re.Fields().Title != "Sample Title" {
				t.Errorf("title after the strip = %q, want the store's", re.Fields().Title)
			}
		})
	}
}

// Readers find the APE tag nearest the end of the file, so a second tag ahead of it is a reported
// duplicate. A no-op keeps both; a rewrite drops the duplicate, naming the values only it held.
func TestDuplicateAPETagDroppedByARewrite(t *testing.T) {
	clean := readFixture(t, sampleWV)
	start := apeTagStart(clean)
	blocks, store := clean[:start], clean[start:]
	essence := essenceOf(t, clean)

	src := slices.Concat(blocks, headedAPETag(t, "Composer", "Only Here"), store)
	doc := mustParseBytes(t, src)
	if !doc.Tags().Equal(mustParseBytes(t, clean).Tags()) {
		t.Errorf("tags = %v, want the tag nearest the end of the file", doc.Tags().Keys())
	}
	if !hasWarning(doc, wl.WarnDuplicateTagBlock) || !lintHasCode(doc, "duplicate-tag-block") {
		t.Errorf("the duplicate is not reported; warnings %v", doc.Warnings())
	}
	if !essenceOf(t, src).Equal(essence) {
		t.Error("the duplicate changed the digest")
	}

	noop, err := doc.Edit().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !noop.IsNoOp() || !bytes.Equal(applyToBytes(t, src, noop), src) {
		t.Error("a no-op did not keep the file byte for byte")
	}

	plan, err := doc.Edit().Set(tag.Album, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnDuplicateTagBlockDropped)
	if !ok || !slices.Equal(w.Keys, []tag.Key{tag.Composer}) {
		t.Errorf("drop warning = %+v (present %v), want one naming COMPOSER", w, ok)
	}
	out := applyToBytes(t, src, plan)
	re := mustParseBytes(t, out)
	if bytes.Contains(out, []byte("Only Here")) || hasWarning(re, wl.WarnDuplicateTagBlock) {
		t.Error("the rewrite kept the duplicate")
	}
	if re.Fields().Album != "Edited" || !essenceOf(t, out).Equal(essence) {
		t.Errorf("after the rewrite: album %q, digest changed %v", re.Fields().Album, !essenceOf(t, out).Equal(essence))
	}

	// A duplicate holding nothing the tag lacks goes without a warning.
	redundant := slices.Concat(blocks, headedAPETag(t, "Title", "Sample Title"), store)
	plan, err = mustParseBytes(t, redundant).Edit().Set(tag.Album, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnDuplicateTagBlockDropped); ok {
		t.Errorf("a redundant duplicate warned: %v", w)
	}
}

// Without an audio end (SV7 Musepack, Monkey's Audio before 3.98) nothing bounds the audio, so junk
// after the tag stays in the essence: it is reported so, a rewrite moves it ahead of the tag, and the
// digest, the one of the stream with the junk right after it, holds across an edit and a strip.
func TestUnknownAudioEndKeepsJunkInTheEssence(t *testing.T) {
	for _, c := range []struct {
		name   string
		stream []byte
	}{
		{"sv7.mpc", mpcSV7(100, 0)},
		{"pre-3.98.ape", slices.Concat(legacyAPEHeader(3970, 2000, 0, 2, 44100, 1, 1000), make([]byte, 512))},
	} {
		t.Run(c.name, func(t *testing.T) {
			stuck := trailerJunk(300)
			src := slices.Concat(c.stream, headedAPETag(t, "Title", "T"), stuck, testfile.ID3v1("V1", "", "", "", "", 255))
			essence := essenceOf(t, slices.Concat(c.stream, stuck))

			doc := mustParseBytes(t, src)
			if doc.Fields().Title != "T" {
				t.Errorf("title = %q, want the APE tag's", doc.Fields().Title)
			}
			w, ok := docWarning(doc, wl.WarnTrailingBytes)
			if !ok || !strings.Contains(w.Message, "stay in the audio essence") {
				t.Errorf("junk warning = %q (present %v), want it to say the bytes stay in the essence", w.Message, ok)
			}
			if !essenceOf(t, src).Equal(essence) {
				t.Error("the digest is not the stream's with the junk after it")
			}

			plan, err := doc.Edit().Set(tag.Album, "Edited").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, src, plan)
			if !bytes.Contains(out, slices.Concat(c.stream, stuck)) {
				t.Error("the junk does not follow the stream after the edit")
			}
			if !essenceOf(t, out).Equal(essence) {
				t.Error("the digest changed across the edit")
			}

			plan, err = doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
			if err != nil {
				t.Fatal(err)
			}
			if !essenceOf(t, applyToBytes(t, src, plan)).Equal(essence) {
				t.Error("the digest changed across a strip")
			}
		})
	}
}

// An audio end the file contradicts is not trusted: a WavPack block running past the start of the
// tags, a Monkey's Audio descriptor that declares more bytes than the file holds or ends inside its
// own header, or an SV8 Musepack stream with no SE packet leave the end unknown, so junk after the
// tag stays in the essence.
func TestDisagreeingAudioEndIsUnknown(t *testing.T) {
	// The second block declares a MiB, running past every tag after it.
	overrun := wvBlock(1|wvRate44k|wvFinal, 4410, make([]byte, 16))
	binary.LittleEndian.PutUint32(overrun[4:8], 1<<20)
	wv := slices.Concat(wvBlock(1|wvRate44k, 4410, make([]byte, 16)), overrun)
	pastTheFile := descriptorAPEFile(52, 512)
	binary.LittleEndian.PutUint32(pastTheFile[24:28], 512+1<<20)
	shortOfHeader := descriptorAPEFile(52, 512)
	binary.LittleEndian.PutUint32(shortOfHeader[12:16], 0) // ends where the descriptor does
	binary.LittleEndian.PutUint32(shortOfHeader[24:28], 0)
	noSE := testfile.MPCStream(44100, 0, 2, testfile.MPCAudio(64))
	for _, c := range []struct {
		name   string
		stream []byte
	}{
		{"wavpack block past the tags", wv},
		{"monkey's audio frames past the file", pastTheFile},
		{"monkey's audio end inside the header", shortOfHeader},
		{"musepack without SE", noSE},
	} {
		t.Run(c.name, func(t *testing.T) {
			stuck := trailerJunk(300)
			src := slices.Concat(c.stream, headedAPETag(t, "Title", "T"), stuck, testfile.ID3v1("V1", "", "", "", "", 255))
			doc := mustParseBytes(t, src)
			if doc.Fields().Title != "T" {
				t.Errorf("title = %q, want the APE tag's", doc.Fields().Title)
			}
			w, ok := docWarning(doc, wl.WarnTrailingBytes)
			if !ok || !strings.Contains(w.Message, "stay in the audio essence") {
				t.Errorf("junk warning = %q (present %v), want it to say the bytes stay in the essence", w.Message, ok)
			}
			if !essenceOf(t, src).Equal(essenceOf(t, slices.Concat(c.stream, stuck))) {
				t.Error("the digest is not the stream's with the junk after it")
			}
		})
	}
}

// The native view lists every region after the stream in file order, and a rewrite's report names
// what it does to each: the duplicate it drops, the junk it moves ahead of the tag, and the Lyrics3
// block and stacked ID3v1 tags a strip removes.
func TestAPETrailerNativeViewAndOperations(t *testing.T) {
	clean := readFixture(t, sampleWV)
	start := apeTagStart(clean)
	src := slices.Concat(clean[:start], headedAPETag(t, "Composer", "Dup"), clean[start:], trailerJunk(300),
		testfile.Lyrics3v2([2]string{"LYR", "words"}), stackedV1())

	doc := mustParseBytes(t, src)
	var kinds []string
	notes := map[string]string{}
	for _, e := range doc.Native().Describe() {
		if !strings.HasPrefix(e.Kind, " ") {
			kinds = append(kinds, e.Kind)
			notes[e.Kind] += e.Note + ";"
		}
	}
	if want := []string{"WavPack blocks", "APEv2", "APEv2", "trailing bytes", "Lyrics3v2", "ID3v1"}; !slices.Equal(kinds, want) {
		t.Errorf("native view = %v, want %v", kinds, want)
	}
	if !strings.Contains(notes["APEv2"], "duplicate") || !strings.Contains(notes["ID3v1"], "2 stacked tags") {
		t.Errorf("native notes = %v, want the duplicate and the stacked count named", notes)
	}

	plan, err := doc.Edit().Set(tag.Album, "Edited").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	ops := plan.Report().Operations
	for _, want := range []string{"duplicate APE tag drop", "trailing bytes moved ahead of the APE tag (300 bytes)",
		"trailing Lyrics3v2 strip", "trailing ID3v1 strip (2 stacked tags)"} {
		if !slices.Contains(ops, want) {
			t.Errorf("operations %q do not include %q", ops, want)
		}
	}
}

// The walks to the end of a WavPack or SV8 Musepack stream read at most MaxElements headers. Past the
// cap the end is unknown, with no warning of its own: a longer stream of tiny blocks or packets costs
// no more reads, and junk after the tag stays in the essence.
func TestStreamEndWalksStopAtTheElementLimit(t *testing.T) {
	const capped = 100
	for _, c := range []struct {
		name, subject string
		stream        func(n int) []byte
	}{
		{"wavpack blocks", "after the WavPack stream", func(n int) []byte { return bytes.Repeat(wvBlock(1|wvRate44k, 4410, nil), n) }},
		{"musepack packets", "after the Musepack stream", func(n int) []byte {
			return testfile.MPCStream(44100*60, 0, 2, bytes.Repeat(testfile.MPCAudio(0), n), testfile.MPCEnd())
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			reads := func(n int) int {
				src := &countingSource{ReaderAtSized: wl.BytesSource(c.stream(n))}
				if _, err := wl.Parse(context.Background(), src, wl.WithLimits(wl.Limits{MaxElements: capped})); err != nil {
					t.Fatal(err)
				}
				return src.reads
			}
			if short, long := reads(10*capped), reads(100*capped); long != short {
				t.Errorf("%d headers cost %d reads, %d cost %d; past the cap the length should not matter", 100*capped, long, 10*capped, short)
			}
			src := slices.Concat(c.stream(2*capped), headedAPETag(t, "Title", "T"), trailerJunk(300))
			whole, err := wl.Parse(context.Background(), wl.BytesSource(src))
			if err != nil {
				t.Fatal(err)
			}
			if w, ok := docWarning(whole, wl.WarnTrailingBytes); !ok || !strings.Contains(w.Message, c.subject) {
				t.Errorf("under the default limit the junk warning = %q, want it %s", w.Message, c.subject)
			}
			cut, err := wl.Parse(context.Background(), wl.BytesSource(src), wl.WithLimits(wl.Limits{MaxElements: capped}))
			if err != nil {
				t.Fatal(err)
			}
			if w, ok := docWarning(cut, wl.WarnTrailingBytes); !ok || !strings.Contains(w.Message, "stay in the audio essence") {
				t.Errorf("past the cap the junk warning = %q, want it to stay in the essence", w.Message)
			}
		})
	}
	// The WavPack walk past its cap raises nothing else.
	src := slices.Concat(bytes.Repeat(wvBlock(1|wvRate44k, 4410, nil), 2*capped), headedAPETag(t, "Title", "T"), trailerJunk(300))
	whole, cut := mustParseBytes(t, src), mustParseLimited(t, src, wl.Limits{MaxElements: capped})
	if got, want := warningCodes(cut), warningCodes(whole); !slices.Equal(got, want) {
		t.Errorf("warnings past the cap = %v, want the default limit's %v", got, want)
	}
}

func mustParseLimited(t *testing.T, data []byte, limits wl.Limits) *wl.Document {
	t.Helper()
	doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func warningCodes(doc *wl.Document) []wl.WarningCode {
	var out []wl.WarningCode
	for _, w := range doc.Warnings() {
		out = append(out, w.Code)
	}
	return out
}
