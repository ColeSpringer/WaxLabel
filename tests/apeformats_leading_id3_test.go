package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// leadingTitleID3 is a 30-byte ID3v2.3 tag holding one title, the stray front tag some taggers
// write ahead of an APEv2-native stream.
func leadingTitleID3() []byte {
	return testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Old Title"))
}

// A WavPack or Monkey's Audio stream behind a leading ID3v2 reads as the plain file does, the tag
// kept as legacy: an edit preserves it byte for byte and a legacy strip removes it, the essence
// unchanged throughout.
func TestAPEFormatsReadAndPreserveLeadingID3(t *testing.T) {
	lead := leadingTitleID3()
	for _, path := range []string{sampleWV, sampleAPE} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			plain := readFixture(t, path)
			src := slices.Concat(lead, plain)
			want := mustParseBytes(t, plain)
			essence := essenceOf(t, plain)

			doc := mustParseBytes(t, src)
			if doc.Format() != want.Format() {
				t.Fatalf("format = %v, want %v found past the leading ID3v2", doc.Format(), want.Format())
			}
			if !hasWarning(doc, wl.WarnStrayLeadingID3) {
				t.Error("no stray-leading-id3 warning")
			}
			if !doc.Tags().Equal(want.Tags()) {
				t.Errorf("tags = %v, want the APEv2 tags %v", doc.Tags().Keys(), want.Tags().Keys())
			}
			if !reflect.DeepEqual(doc.Properties(), want.Properties()) {
				t.Errorf("properties = %+v, want the plain file's %+v", doc.Properties(), want.Properties())
			}
			legacyTitle := wl.FamilyValue{Key: tag.Title, Family: wl.FamilyID3v2, Scope: wl.ScopeTrack,
				Values: []string{"Old Title"}, Legacy: true}
			if got, fams := doc.Families(), append(want.Families(), legacyTitle); !reflect.DeepEqual(got, fams) {
				t.Errorf("families = %+v, want the plain file's plus the leading title as legacy: %+v", got, fams)
			}
			if !essenceOf(t, src).Equal(essence) {
				t.Error("the essence differs from the plain file's")
			}

			plan, err := doc.Edit().Set(tag.Album, "Edited").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			edited := applyToBytes(t, src, plan)
			if !bytes.HasPrefix(edited, lead) {
				t.Error("an edit did not keep the leading ID3v2 byte for byte")
			}
			if got := mustParseBytes(t, edited).Fields().Album; got != "Edited" {
				t.Errorf("album after the edit = %q, want Edited", got)
			}
			if !essenceOf(t, edited).Equal(essence) {
				t.Error("the essence changed across an edit")
			}

			plan, err = mustParseBytes(t, edited).Edit().Set(tag.Album, "Stripped").
				Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
			if err != nil {
				t.Fatal(err)
			}
			stripped := applyToBytes(t, edited, plan)
			re := mustParseBytes(t, stripped)
			if bytes.HasPrefix(stripped, []byte("ID3")) || hasWarning(re, wl.WarnStrayLeadingID3) {
				t.Error("a legacy strip kept the leading ID3v2")
			}
			if re.Format() != want.Format() || re.Fields().Album != "Stripped" {
				t.Errorf("after the strip: format %v, album %q; want %v and Stripped", re.Format(), re.Fields().Album, want.Format())
			}
			if !essenceOf(t, stripped).Equal(essence) {
				t.Error("the essence changed across a legacy strip")
			}
			// The strip removes the leading tag and nothing else.
			plan, err = want.Edit().Set(tag.Album, "Stripped").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(stripped, applyToBytes(t, plain, plan)) {
				t.Error("the stripped file differs from the plain file given the same edit")
			}
		})
	}
}

// A leading ID3v2 holding a picture is content a legacy strip cannot prove redundant: lint --fix
// keeps the tag, and an explicit strip warns that it drops it.
func TestAPEFormatsLeadingID3PictureIsOpaque(t *testing.T) {
	lead := testfile.ID3v2(3, testfile.FrontCover("image/png", testfile.PNG()))
	for _, path := range []string{sampleWV, sampleAPE} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src := slices.Concat(lead, readFixture(t, path))
			doc := mustParseBytes(t, src)
			if !doc.HasOpaqueLegacyContent() {
				t.Fatal("the leading picture does not mark opaque legacy content")
			}
			if !bytes.HasPrefix(applyLintFix(t, src), lead) {
				t.Error("lint --fix stripped a leading ID3v2 holding a picture")
			}
			plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
			if err != nil {
				t.Fatal(err)
			}
			if !planHasWarning(plan, wl.WarnLegacyStripDropped) {
				t.Error("a legacy strip of the leading picture does not warn legacy-strip-dropped")
			}
		})
	}
}

// Behind a leading tag the WavPack blocks read from the first one: its declared size is checked
// against the bytes after it, the rate comes from its sub-blocks and the channels add up over the
// sample group, as in the plain stream.
func TestWavPackBlocksBehindLeadingID3(t *testing.T) {
	lead := leadingTitleID3()
	cut := wvBlock(1|wvRate44k|wvFinal, 4410, make([]byte, 26))
	_, err := wl.Parse(context.Background(), wl.BytesSource(slices.Concat(lead, cut[:len(cut)-10])))
	if !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "remain in the file") {
		t.Errorf("a first block cut 10 bytes short: err = %v, want ErrInvalidData saying it runs past the file", err)
	}
	for _, c := range []struct {
		name     string
		data     []byte
		rate, ch int
	}{
		{"sub-block rate", wvBlock(1|wvRateUnknown|wvFinal, 8000, wvSubBlock(0x27, []byte{0x40, 0x1F, 0x00})), 8000, 2},
		{"three channels", slices.Concat(wvBlock(1|wvRate44k, 4410, nil), wvBlock(1|wvRate44k|wvMono|wvFinal, 4410, nil)), 44100, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := mustParseBytes(t, c.data).Properties()
			if tr := want.First(); tr.SampleRate != c.rate || tr.Channels != c.ch {
				t.Fatalf("plain stream reads %d Hz, %d channels; want %d Hz, %d channels", tr.SampleRate, tr.Channels, c.rate, c.ch)
			}
			if got := mustParseBytes(t, slices.Concat(lead, c.data)).Properties(); !reflect.DeepEqual(got, want) {
				t.Errorf("behind a leading tag: properties = %+v, want %+v", got, want)
			}
		})
	}
}

// Behind a leading tag a Monkey's Audio header is read where the descriptor declares it from the
// marker, inside the leading read or past it, and one that would run into the trailing tag or past
// the end of the file is still refused.
func TestMonkeysAudioHeaderBehindLeadingID3(t *testing.T) {
	lead := leadingTitleID3()
	for _, descLen := range []uint32{64, 5000} {
		t.Run(fmt.Sprint(descLen), func(t *testing.T) {
			plain := slices.Concat(descriptorAPEFile(descLen, 512), apeTagText(map[string]string{"Title": "T"}))
			want := mustParseBytes(t, plain).Properties()
			if got := mustParseBytes(t, slices.Concat(lead, plain)).Properties(); !reflect.DeepEqual(got, want) {
				t.Errorf("behind a leading tag: properties = %+v, want %+v", got, want)
			}
		})
	}
	at64 := descriptorAPEFile(64, 0) // header at 64..88 past the marker
	for _, c := range []struct {
		name, cause string
		data        []byte
	}{
		{"into the tag", "into the trailing tag", slices.Concat(lead, at64[:80], apeTagText(nil))},
		{"past the end", "past the end of the file", slices.Concat(lead, descriptorAPEFile(200, 0)[:200])},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := wl.Parse(context.Background(), wl.BytesSource(c.data))
			if !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), c.cause) {
				t.Errorf("err = %v, want ErrInvalidData saying the header runs %s", err, c.cause)
			}
		})
	}
}

// The trailing-tag floor is measured from the stream marker: a tag footer inside the first WavPack
// block, or inside the documented Monkey's Audio header, is not a tag behind a leading ID3v2 either.
// The leading tag is longer than the footer's tag, so a floor measured from the file start would
// take it.
func TestAPEFormatsTagFloorBehindLeadingID3(t *testing.T) {
	lead := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", strings.Repeat("x", 109))) // 130 bytes
	inHeader := descriptorAPEFile(52, 0)                                               // header at 52..76 past the marker
	copy(inHeader[44:], apeTagText(nil))                                               // a footer-only tag over the last 32 bytes
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"wavpack block", wvBlock(1|wvRate44k|wvFinal, 4410, slices.Concat(make([]byte, 64), apeTagText(map[string]string{"Title": "Inside"})))},
		{"monkey's audio header", inHeader},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := mustParseBytes(t, c.data)
			if want.Tags().Len() != 0 {
				t.Fatalf("the plain stream reads tags %v, want none", want.Tags().Keys())
			}
			got := mustParseBytes(t, slices.Concat(lead, c.data))
			if got.Tags().Len() != 0 || !reflect.DeepEqual(got.Properties(), want.Properties()) {
				t.Errorf("behind a leading tag: tags %v, properties %+v; want no tags and %+v",
					got.Tags().Keys(), got.Properties(), want.Properties())
			}
		})
	}
}

// The native view lists a leading ID3v2 ahead of the plain file's entries, spelled alike in every
// format that keeps one, so the audio run is sized without it.
func TestNativeViewSizesAudioWithoutLeadingID3(t *testing.T) {
	lead := leadingTitleID3()
	entry := wl.NativeEntry{Kind: "ID3v2", Size: len(lead), Note: "leading, legacy, preserved"}
	for _, path := range []string{sampleFLAC, sampleWV, sampleAPE, chaptersMPC} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			plain := readFixture(t, path)
			want := mustParseBytes(t, plain).Native().Describe()
			got := mustParseBytes(t, slices.Concat(lead, plain)).Native().Describe()
			if len(got) == 0 || got[0] != entry {
				t.Fatalf("native view = %+v, want %+v first", got, entry)
			}
			if !slices.Equal(got[1:], want) {
				t.Errorf("native view past the leading tag = %+v, want the plain file's %+v", got[1:], want)
			}
		})
	}
}

// leadingGap sits between the leading tag and the stream in the gap tests: bytes no sniffer claims.
var leadingGap = []byte{1, 2, 3, 4, 5, 6, 7}

// A FLAC, Musepack, WavPack or Monkey's Audio stream behind a leading ID3v2 and a gap reads as the
// plain file does. The gap is reported and goes with the tag: kept by an edit, dropped by a legacy
// strip. Nothing proves it redundant, so a lint fix keeps both and a strip warns that it drops it.
func TestLeadingID3GapKeptWithTheTag(t *testing.T) {
	lead := leadingTitleID3()
	for _, path := range []string{sampleFLAC, chaptersMPC, sampleWV, sampleAPE} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			plain := readFixture(t, path)
			src := slices.Concat(lead, leadingGap, plain)
			want := mustParseBytes(t, plain)
			essence := essenceOf(t, plain)

			doc := mustParseBytes(t, src)
			if doc.Format() != want.Format() {
				t.Fatalf("format = %v, want %v found past the leading ID3v2 and the gap", doc.Format(), want.Format())
			}
			for _, code := range []wl.WarningCode{wl.WarnStrayLeadingID3, wl.WarnLeadingBytes} {
				if !hasWarning(doc, code) {
					t.Errorf("no %s warning; got %v", code, doc.Warnings())
				}
			}
			if !doc.Tags().Equal(want.Tags()) || !reflect.DeepEqual(doc.Properties(), want.Properties()) {
				t.Errorf("tags %v, properties %+v; want the plain file's %v, %+v",
					doc.Tags().Keys(), doc.Properties(), want.Tags().Keys(), want.Properties())
			}
			if !essenceOf(t, src).Equal(essence) {
				t.Error("the essence differs from the plain file's")
			}
			if !doc.HasOpaqueLegacyContent() {
				t.Error("the gap is not reported as content a strip cannot prove redundant")
			}
			native := doc.Native().Describe()
			if len(native) < 2 || native[0] != (wl.NativeEntry{Kind: "ID3v2", Size: len(lead), Note: "leading, legacy, preserved"}) ||
				native[1].Kind != "leading bytes" || native[1].Size != len(leadingGap) ||
				!slices.Equal(native[2:], want.Native().Describe()) {
				t.Errorf("native view = %+v, want the %d-byte tag, the %d leading bytes, then the plain file's %+v",
					native, len(lead), len(leadingGap), want.Native().Describe())
			}

			plan, err := doc.Edit().Set(tag.Album, "Edited").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			edited := applyToBytes(t, src, plan)
			if !bytes.HasPrefix(edited, slices.Concat(lead, leadingGap)) {
				t.Error("an edit did not keep the leading tag and the gap byte for byte")
			}
			if got := mustParseBytes(t, edited).Fields().Album; got != "Edited" {
				t.Errorf("album after the edit = %q, want Edited", got)
			}
			if !essenceOf(t, edited).Equal(essence) {
				t.Error("the essence changed across an edit")
			}
			if !bytes.HasPrefix(applyLintFix(t, src), slices.Concat(lead, leadingGap)) {
				t.Error("lint --fix dropped the leading tag and the gap")
			}

			plan, err = mustParseBytes(t, edited).Edit().Set(tag.Album, "Stripped").
				Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
			if err != nil {
				t.Fatal(err)
			}
			if !planHasWarning(plan, wl.WarnLegacyStripDropped) {
				t.Error("a legacy strip of the gap does not warn legacy-strip-dropped")
			}
			stripped := applyToBytes(t, edited, plan)
			re := mustParseBytes(t, stripped)
			if re.Format() != want.Format() || re.Fields().Album != "Stripped" {
				t.Errorf("after the strip: format %v, album %q; want %v and Stripped", re.Format(), re.Fields().Album, want.Format())
			}
			if hasWarning(re, wl.WarnStrayLeadingID3) || hasWarning(re, wl.WarnLeadingBytes) {
				t.Errorf("a legacy strip kept the tag or the gap; warnings %v", re.Warnings())
			}
			if !essenceOf(t, stripped).Equal(essence) {
				t.Error("the essence changed across a legacy strip")
			}
			// The strip removes the tag and the gap and nothing else.
			plan, err = want.Edit().Set(tag.Album, "Stripped").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(stripped, applyToBytes(t, plain, plan)) {
				t.Error("the stripped file differs from the plain file given the same edit")
			}
		})
	}
}

// The stream marker is found behind a second ID3v2 tag larger than the resync window: detection and
// the parser skip chained tags by their headers, and the second tag is leading bytes kept with the
// first.
func TestStreamBehindALargeSecondTag(t *testing.T) {
	lead := leadingTitleID3()
	second := testfile.ID3v2(3, testfile.ID3Frame(3, "PRIV", make([]byte, core.ResyncWindow+1024)))
	for _, path := range []string{sampleFLAC, chaptersMPC, sampleWV, sampleAPE} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			plain := readFixture(t, path)
			src := slices.Concat(lead, second, plain)
			want := mustParseBytes(t, plain)
			doc := mustParseBytes(t, src)
			if doc.Format() != want.Format() {
				t.Fatalf("format = %v, want %v found past both tags", doc.Format(), want.Format())
			}
			if !hasWarning(doc, wl.WarnLeadingBytes) || !reflect.DeepEqual(doc.Properties(), want.Properties()) {
				t.Errorf("warnings %v, properties %+v; want leading-bytes and the plain file's %+v",
					doc.Warnings(), doc.Properties(), want.Properties())
			}
			if !essenceOf(t, src).Equal(essenceOf(t, plain)) {
				t.Error("the essence differs from the plain file's")
			}
		})
	}
}
