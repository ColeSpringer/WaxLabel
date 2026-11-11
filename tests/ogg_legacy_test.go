package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// oggLegacy is an Ogg file as a tagger that knows only ID3 leaves it.
type oggLegacy struct {
	name       string
	lead, tail []byte
	codes      []wl.WarningCode
}

func oggLegacyCases() []oggLegacy {
	v2 := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Sample Title"), testfile.TextFrame(3, "TCOM", "Legacy Composer"))
	v1 := testfile.ID3v1("Sample Title", "", "", "", "Legacy note", 255)
	return []oggLegacy{
		{"a leading ID3v2 tag", v2, nil, []wl.WarningCode{wl.WarnStrayLeadingID3}},
		{"a leading ID3v2 tag and bytes behind it", slices.Concat(v2, []byte{1, 2, 3}), nil,
			[]wl.WarningCode{wl.WarnStrayLeadingID3, wl.WarnLeadingBytes}},
		{"a trailing ID3v1 tag", nil, v1, []wl.WarningCode{wl.WarnTrailingID3v1}},
		{"both", v2, v1, []wl.WarningCode{wl.WarnStrayLeadingID3, wl.WarnTrailingID3v1}},
	}
}

// An Ogg file behind an ID3v2 tag or ahead of an ID3v1 tag reads as the stream it is: the tags are
// reported, shown as legacy families and out of the essence, and an edit keeps them.
func TestOggLegacyTagsReadAndPreserved(t *testing.T) {
	for _, f := range oggFixtures {
		clean := readFixture(t, f)
		for _, c := range oggLegacyCases() {
			t.Run(f+"/"+c.name, func(t *testing.T) {
				data := slices.Concat(c.lead, clean, c.tail)
				doc := mustParseBytes(t, data)
				if doc.Format() != mustParseBytes(t, clean).Format() {
					t.Fatalf("format = %v", doc.Format())
				}
				for _, code := range c.codes {
					if !hasWarning(doc, code) {
						t.Errorf("warnings = %v, want %v", doc.Warnings(), code)
					}
				}
				if hasWarning(doc, wl.WarnTrailingBytes) || hasWarning(doc, wl.WarnChainedStream) {
					t.Errorf("warnings = %v, want the legacy tags alone", doc.Warnings())
				}
				if c.lead != nil && !hasFamily(doc, wl.FamilyID3v2, tag.Composer) {
					t.Errorf("no ID3v2 family entry for COMPOSER: %v", doc.Families())
				}
				if c.tail != nil && !hasFamily(doc, wl.FamilyID3v1, tag.Comment) {
					t.Errorf("no ID3v1 family entry for COMMENT: %v", doc.Families())
				}
				if _, ok := doc.Get(tag.Composer); ok {
					t.Error("a legacy tag's value reads as a canonical one")
				}
				if got, want := essenceOf(t, data), essenceOf(t, clean); !got.Equal(want) {
					t.Errorf("essence = %s, want the clean file's %s", got, want)
				}

				plan, err := doc.Edit().Set(tag.Title, "Kept").Prepare()
				if err != nil {
					t.Fatal(err)
				}
				out := applyToBytes(t, data, plan)
				if !bytes.HasPrefix(out, c.lead) || !bytes.HasSuffix(out, c.tail) {
					t.Error("the edit did not keep the legacy tags where they were")
				}
				cleanPlan, err := mustParseBytes(t, clean).Edit().Set(tag.Title, "Kept").Prepare()
				if err != nil {
					t.Fatal(err)
				}
				if want := slices.Concat(c.lead, applyToBytes(t, clean, cleanPlan), c.tail); !bytes.Equal(out, want) {
					t.Error("the stream between the legacy tags differs from the clean file's rewrite")
				}
			})
		}
	}
}

// A legacy strip removes the tags, with the bytes behind a leading one, and leaves the stream as it
// was: the clean file, byte for byte. The plan names what only the legacy tags held.
func TestOggLegacyStrip(t *testing.T) {
	for _, f := range oggFixtures {
		clean := readFixture(t, f)
		for _, c := range oggLegacyCases() {
			t.Run(f+"/"+c.name, func(t *testing.T) {
				data := slices.Concat(c.lead, clean, c.tail)
				plan, err := mustParseBytes(t, data).Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
				if err != nil {
					t.Fatal(err)
				}
				if plan.Report().NoOp {
					t.Fatal("a strip of legacy tags planned no change")
				}
				ws := plan.Report().Warnings
				i := slices.IndexFunc(ws, func(w wl.Warning) bool { return w.Code == wl.WarnLegacyStripDropped })
				if i < 0 {
					t.Fatalf("warnings = %v, want legacy-strip-dropped", ws)
				}
				for key, held := range map[tag.Key]bool{tag.Composer: c.lead != nil, tag.Comment: c.tail != nil, tag.Title: false} {
					if got := slices.Contains(ws[i].Keys, key); got != held {
						t.Errorf("legacy-strip-dropped names %s = %v, want %v (%v)", key, got, held, ws[i].Keys)
					}
				}
				ops := strings.Join(plan.Report().Operations, "; ")
				if c.lead != nil && !strings.Contains(ops, "leading ID3v2 strip") || c.tail != nil && !strings.Contains(ops, "trailing ID3v1 strip") {
					t.Errorf("operations = %q", ops)
				}
				if out := applyToBytes(t, data, plan); !bytes.Equal(out, clean) {
					t.Errorf("the stripped file is %d bytes and differs from the clean file's %d", len(out), len(clean))
				}
			})
		}
	}
}

// A legacy strip keeps bytes that are no tag: junk ahead of an ID3v1 tag stays behind the stream.
func TestOggLegacyStripKeepsJunk(t *testing.T) {
	clean := readFixture(t, sampleOgg)
	data := slices.Concat(clean, junk, testfile.ID3v1("T", "", "", "", "", 255))
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnTrailingBytes) || !hasWarning(doc, wl.WarnTrailingID3v1) {
		t.Fatalf("warnings = %v, want the junk and the tag", doc.Warnings())
	}
	plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, data, plan); !bytes.Equal(out, slices.Concat(clean, junk)) {
		t.Error("the stripped file is not the stream and the junk")
	}
}

// A file ending inside a page ahead of an ID3v1 tag: the cut page runs up to the tag, and is
// neither read nor reported as junk.
func TestOggTruncatedPageAheadOfATag(t *testing.T) {
	clean := readFixture(t, sampleOgg)
	v1 := testfile.ID3v1("T", "", "", "", "", 255)
	data := slices.Concat(clean[:len(clean)-100], v1)
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnTruncatedAudio) || !hasWarning(doc, wl.WarnTrailingID3v1) || hasWarning(doc, wl.WarnTrailingBytes) {
		t.Errorf("warnings = %v, want truncated-audio and trailing-id3v1 alone", doc.Warnings())
	}
	if got, want := essenceOf(t, data), essenceOf(t, clean[:len(clean)-100]); !got.Equal(want) {
		t.Errorf("essence = %s, want that of the file without the tag, %s", got, want)
	}
	plan, err := doc.Edit().Set(tag.Title, "Cut").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if pages := oggPages(clean); !bytes.HasSuffix(out, clean[pages[len(pages)-1].off:len(clean)-100]) {
		t.Error("the cut page does not end the stripped file")
	}
}

// A stream that begins beside a Skeleton stream is read: the file is reported as multiplexed and
// refuses writes, and its audio essence is the stream's own.
func TestOggSkeletonMultiplexedReadOnly(t *testing.T) {
	fishead := slices.Concat([]byte("fishead\x00"), testfile.LE16(3), testfile.LE16(0), make([]byte, 52))
	for _, f := range oggFixtures {
		clean := readFixture(t, f)
		pages := oggPages(clean)
		// The Skeleton's pages around the stream's own: its BOS page first, a page among the
		// header pages, and its last page at the end.
		data := slices.Concat(
			testfile.OggPage(2, 0, 7, 0, fishead),
			clean[:pages[1].off],
			testfile.OggPage(0, 0, 7, 1, []byte("fisbone\x00")),
			clean[pages[1].off:],
			testfile.OggPage(4, 0, 7, 2, nil),
		)
		doc := mustParseBytes(t, data)
		want := mustParseBytes(t, clean)
		if doc.Format() != want.Format() || doc.Fields().Title != "Sample Title" {
			t.Errorf("%s: format %v, title %q", f, doc.Format(), doc.Fields().Title)
		}
		if !hasWarning(doc, wl.WarnMultiplexedStream) || hasWarning(doc, wl.WarnChainedStream) {
			t.Errorf("%s: warnings = %v, want multiplexed-stream alone", f, doc.Warnings())
		}
		if got := doc.Properties().First(); got.Duration != want.Properties().First().Duration {
			t.Errorf("%s: duration = %v, want %v", f, got.Duration, want.Properties().First().Duration)
		}
		if got, want := essenceOf(t, data), essenceOf(t, clean); !got.Equal(want) {
			t.Errorf("%s: essence = %s, want the stream's own %s", f, got, want)
		}
		if _, err := doc.Edit().Set(tag.Title, "nope").Prepare(); !errors.Is(err, waxerr.ErrChainedStream) {
			t.Errorf("%s: an edit: err = %v, want ErrChainedStream", f, err)
		}
		if caps := doc.Capabilities(); !caps.ReadOnly {
			t.Errorf("%s: a multiplexed file reports writable", f)
		}
	}

	// A Skeleton with no stream WaxLabel reads beside it is no file of a format it knows.
	alone := slices.Concat(testfile.OggPage(2, 0, 7, 0, fishead), testfile.OggPage(4, 0, 7, 1, nil))
	if _, err := wl.Parse(context.Background(), wl.BytesSource(alone)); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("a Skeleton alone: err = %v, want unsupported format", err)
	}
}

// Whatever legacy tags follow an Ogg stream are read as after a FLAC stream: an APE tag, a Lyrics3
// block and stacked ID3v1 tags each reported, the APE and ID3v1 values in the family view, and one
// strip removes them all.
func TestOggLegacyTailOfEveryKind(t *testing.T) {
	clean := readFixture(t, sampleOpus)
	data := slices.Concat(clean, apeTag(map[string]string{"Composer": "APE Composer"}),
		testfile.Lyrics3v2([2]string{"LYR", "words"}), testfile.ID3v1("One", "", "", "", "", 255), testfile.ID3v1("Two", "", "", "", "", 255))
	doc := mustParseBytes(t, data)
	for _, code := range []wl.WarningCode{wl.WarnLegacyAPE, wl.WarnTrailingLyrics3, wl.WarnTrailingID3v1} {
		if !hasWarning(doc, code) {
			t.Errorf("warnings = %v, want %v", doc.Warnings(), code)
		}
	}
	if hasWarning(doc, wl.WarnTrailingBytes) || !hasFamily(doc, wl.FamilyAPEv2, tag.Composer) || !hasFamily(doc, wl.FamilyID3v1, tag.Title) {
		t.Errorf("warnings = %v, families = %v", doc.Warnings(), doc.Families())
	}
	if !doc.HasOpaqueLegacyContent() {
		t.Error("the Lyrics3 block is not counted as content a strip cannot prove redundant")
	}
	if got, want := essenceOf(t, data), essenceOf(t, clean); !got.Equal(want) {
		t.Errorf("essence = %s, want the clean file's %s", got, want)
	}
	plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, data, plan); !bytes.Equal(out, clean) {
		t.Error("the stripped file is not the clean stream")
	}
}
