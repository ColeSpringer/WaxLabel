package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// tinyWebP: minimal WebP header, outside the MP4 allowlist.
func tinyWebP() []byte { return []byte("RIFF\x00\x00\x00\x00WEBP") }

// TestProjectTransferDispositions: Carried, Lossy, and Dropped in one pass. Synthetic caps for partial write.
func TestProjectTransferDispositions(t *testing.T) {
	var ts tag.TagSet
	ts.Set("TITLE", "x")
	ts.Set("ARTIST", "y")
	m := &Media{
		Format:   FormatFLAC,
		Tags:     ts,
		Pictures: []Picture{{}},
		Chapters: []Chapter{{}, {}},
	}
	caps := NewCapabilities(FormatMP4, false,
		Capability{Write: AccessFull},                              // generic field
		Capability{Write: AccessNone, Representation: "no covers"}, // pictures
		Capability{Write: AccessFull},                              // chapters
		AccessNone,                                                 // padding
		map[tag.Key]Capability{
			"ARTIST": {Write: AccessPartial, Fidelity: "ASCII only"},
		},
	)

	items := ProjectTransferOnto(m, nil, caps).Items
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4 (2 fields, pictures, chapters)", len(items))
	}
	want := []struct {
		kind   TransferKind
		key    tag.Key
		count  int
		disp   Disposition
		reason string
	}{
		{TransferField, "TITLE", 1, Carried, ""},
		{TransferField, "ARTIST", 1, Lossy, "ASCII only"},
		{TransferPicture, "", 1, Dropped, "destination format does not store pictures"},
		{TransferChapter, "", 2, Carried, ""},
	}
	for i, w := range want {
		it := items[i]
		if it.Kind != w.kind || it.Key != w.key || it.Count != w.count ||
			it.Disposition != w.disp || it.Reason != w.reason {
			t.Errorf("item %d = %+v, want %+v", i, it, w)
		}
	}

	// Counts: TITLE (1) + chapters (2) = 3 carried.
	carried, lossy, dropped := (TransferReport{Items: items}).Counts()
	if carried != 3 || lossy != 1 || dropped != 1 {
		t.Errorf("counts = (%d,%d,%d), want (3,1,1)", carried, lossy, dropped)
	}
}

// TestProjectTransferMaxItems: over MaxItems drops the whole set (report==write invariant).
func TestProjectTransferMaxItems(t *testing.T) {
	caps := NewCapabilities(FormatMP4, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull},
		Capability{Write: AccessFull, MaxItems: 255}, AccessNone, nil)

	over := &Media{Format: FormatFLAC, Chapters: make([]Chapter, 256)}
	items := ProjectTransferOnto(over, nil, caps).Items
	if len(items) != 1 || items[0].Kind != TransferChapter || items[0].Disposition != Dropped {
		t.Fatalf("256 chapters vs limit 255 should drop, got %+v", items)
	}
	if items[0].Reason == "" {
		t.Error("an over-limit drop must carry a reason")
	}

	atLimit := &Media{Format: FormatFLAC, Chapters: make([]Chapter, 255)}
	if got := ProjectTransferOnto(atLimit, nil, caps).Items; got[0].Disposition != Carried {
		t.Errorf("255 chapters at the limit should carry, got %s", got[0].Disposition)
	}

	// A set over the limit is dropped whole, and none of it is written.
	caps.Pictures.MaxItems, caps.SyncedLyrics = 1, Capability{Write: AccessFull, MaxItems: 1}
	over.Pictures = []Picture{{Type: PicFrontCover, Data: testfile.PNG()}, {Type: PicBackCover, Data: testfile.PNG()}}
	over.SyncedLyrics = make([]SyncedLyrics, 2)
	p := ProjectTransferOnto(over, nil, caps)
	for _, it := range p.Items {
		if it.Disposition != Dropped {
			t.Errorf("%s over its limit: %s", it.Kind, it.Disposition)
		}
	}
	if p.SetPictures || len(p.Pictures) != 0 || p.SetChapters || p.SetSyncedLyrics || len(p.SyncedLyrics) != 0 {
		t.Errorf("sets over their limits are written: %+v", p)
	}
}

// TestProjectTransferReadOnlyDropsEverything: read-only destination drops all items.
func TestProjectTransferReadOnlyDropsEverything(t *testing.T) {
	var ts tag.TagSet
	ts.Set("TITLE", "x")
	m := &Media{Format: FormatFLAC, Tags: ts, Pictures: []Picture{{}}}
	caps := NewCapabilities(FormatMatroska, true,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, Capability{}, AccessNone, nil)

	items := ProjectTransferOnto(m, nil, caps).Items
	for _, it := range items {
		if it.Disposition != Dropped || it.Reason != "destination is read-only" {
			t.Errorf("item %+v: want dropped/read-only", it)
		}
	}
	if r := (TransferReport{Items: items}); r.Lossless() {
		t.Error("a read-only destination cannot be lossless")
	}
}

// TestProjectTransferSplitsUnrepresentableCovers: MIME allowlist splits representable vs dropped items.
func TestProjectTransferSplitsUnrepresentableCovers(t *testing.T) {
	pics := Capability{
		Write: AccessFull, PictureLoss: PictureLossRoleAndDescription,
		PictureMIMEs: []string{"image/jpeg", "image/png", "image/bmp"},
	}
	caps := NewCapabilities(FormatMP4, false, Capability{Write: AccessFull}, pics,
		Capability{Write: AccessNone}, AccessNone, nil)

	m := &Media{Format: FormatFLAC, Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/jpeg", Data: testfile.JPEG()},
		{Type: PicFrontCover, MIME: "image/gif", Data: testfile.GIF()},
		{Type: PicFrontCover, MIME: "image/webp", Data: tinyWebP()},
	}}
	items := ProjectTransferOnto(m, nil, caps).Items
	var carried, dropped *TransferItem
	for i := range items {
		if items[i].Kind != TransferPicture {
			continue
		}
		if items[i].Disposition == Dropped {
			dropped = &items[i]
		} else {
			carried = &items[i]
		}
	}
	if carried == nil || carried.Disposition != Carried || carried.Count != 1 {
		t.Fatalf("carried picture item = %+v, want one Carried JPEG", carried)
	}
	if dropped == nil || dropped.Count != 2 {
		t.Fatalf("dropped picture item = %+v, want count 2", dropped)
	}
	if want := "MP4 cannot store image/gif, image/webp"; dropped.Reason != want {
		t.Errorf("dropped reason = %q, want %q", dropped.Reason, want)
	}
	if c, l, d := (TransferReport{Items: items}).Counts(); c != 1 || l != 0 || d != 2 {
		t.Errorf("counts = (%d,%d,%d), want (1,0,2)", c, l, d)
	}

	// All unrepresentable: one Dropped item; reason lists each MIME once.
	allGIF := &Media{Format: FormatFLAC, Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/gif", Data: testfile.GIF()},
		{Type: PicBackCover, MIME: "image/gif", Data: testfile.GIF()},
	}}
	got := ProjectTransferOnto(allGIF, nil, caps).Items
	if len(got) != 1 || got[0].Disposition != Dropped || got[0].Count != 2 {
		t.Fatalf("all-unrepresentable items = %+v, want one Dropped of count 2", got)
	}
	if want := "MP4 cannot store image/gif"; got[0].Reason != want {
		t.Errorf("reason = %q, want %q (distinct MIMEs only)", got[0].Reason, want)
	}

	// No MIME restriction: single picture item.
	open := NewCapabilities(FormatFLAC, false, Capability{Write: AccessFull},
		Capability{Write: AccessFull}, Capability{Write: AccessNone}, AccessNone, nil)
	if n := len(ProjectTransferOnto(m, nil, open).Items); n != 1 {
		t.Errorf("unrestricted destination produced %d picture items, want 1", n)
	}
}

// TestProjectTransferSplitsPicturesByMetadataLoss: per-picture split when covr drops role/description.
// Order: carried, lossy, dropped.
func TestProjectTransferSplitsPicturesByMetadataLoss(t *testing.T) {
	pics := Capability{
		Write: AccessFull, PictureLoss: PictureLossRoleAndDescription,
		PictureMIMEs: []string{"image/jpeg", "image/png", "image/bmp"},
	}
	caps := NewCapabilities(FormatMP4, false, Capability{Write: AccessFull}, pics,
		Capability{Write: AccessNone}, AccessNone, nil)

	pictureDisps := func(items []TransferItem) []Disposition {
		var out []Disposition
		for _, it := range items {
			if it.Kind == TransferPicture {
				out = append(out, it.Disposition)
			}
		}
		return out
	}

	m := &Media{Format: FormatFLAC, Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/jpeg", Data: testfile.JPEG()},
		{Type: PicBackCover, MIME: "image/jpeg", Data: testfile.JPEG()},
	}}
	items := ProjectTransferOnto(m, nil, caps).Items
	disps := pictureDisps(items)
	if len(disps) != 2 || disps[0] != Carried || disps[1] != Lossy {
		t.Fatalf("picture dispositions = %v, want [Carried Lossy]", disps)
	}
	for _, it := range items {
		if it.Kind == TransferPicture && it.Disposition == Carried {
			if it.Count != 1 || it.Reason != "" {
				t.Errorf("carried picture item = %+v, want count 1 with empty reason", it)
			}
		}
		if it.Kind == TransferPicture && it.Disposition == Lossy && it.Count != 1 {
			t.Errorf("lossy picture item = %+v, want count 1", it)
		}
	}
	if c, l, d := (TransferReport{Items: items}).Counts(); c != 1 || l != 1 || d != 0 {
		t.Errorf("counts = (%d,%d,%d), want (1,1,0)", c, l, d)
	}

	m2 := &Media{Format: FormatFLAC, Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/jpeg", Data: testfile.JPEG()},
		{Type: PicBackCover, MIME: "image/png", Data: testfile.PNG()},
		{Type: PicFrontCover, MIME: "image/gif", Data: testfile.GIF()},
	}}
	got := pictureDisps(ProjectTransferOnto(m2, nil, caps).Items)
	if len(got) != 3 || got[0] != Carried || got[1] != Lossy || got[2] != Dropped {
		t.Errorf("picture item order = %v, want [Carried Lossy Dropped]", got)
	}
}

// TestProjectTransferSplitsChaptersByMetadataLoss: per-chapter split for start+title stores.
// Gapless interior end reconstructs (carried); gapped end is lossy. Title cap splits the same way.
func TestProjectTransferSplitsChaptersByMetadataLoss(t *testing.T) {
	chapterItems := func(items []TransferItem) []TransferItem {
		var out []TransferItem
		for _, it := range items {
			if it.Kind == TransferChapter {
				out = append(out, it)
			}
		}
		return out
	}
	startTitle := NewCapabilities(FormatFLAC, false, Capability{Write: AccessFull},
		Capability{Write: AccessNone},
		Capability{Write: AccessFull, ChapterLoss: ChapterLossStartTitleOnly}, AccessNone, nil)

	reconstructable := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, End: 10 * time.Second, Title: "A"}, // end == next start
		{Start: 10 * time.Second, Title: "B"},
	}}
	if c, l, _ := (TransferReport{Items: ProjectTransferOnto(reconstructable, nil, startTitle).Items}).Counts(); c != 2 || l != 0 {
		t.Errorf("reconstructable-end set counts = (%d carried, %d lossy), want (2, 0): a gapless end must grade carried", c, l)
	}
	gapped := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, End: 5 * time.Second, Title: "A"}, // gapped end
		{Start: 10 * time.Second, Title: "B"},
	}}
	if c, l, _ := (TransferReport{Items: ProjectTransferOnto(gapped, nil, startTitle).Items}).Counts(); c != 1 || l != 1 {
		t.Errorf("gapped-end set counts = (%d carried, %d lossy), want (1, 1): a gapped end must grade lossy", c, l)
	}

	mixed := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, End: 10 * time.Second, Title: "A"},
		{Start: 10 * time.Second, End: 15 * time.Second, Title: "B"}, // gapped (C at 20s)
		{Start: 20 * time.Second, Title: "C"},
	}}
	got := chapterItems(ProjectTransferOnto(mixed, nil, startTitle).Items)
	if len(got) != 2 || got[0].Disposition != Carried || got[1].Disposition != Lossy {
		t.Fatalf("mixed chapter items = %+v, want [Carried, Lossy]", got)
	}
	if got[0].Count != 2 || got[0].Reason != "" {
		t.Errorf("carried chapter item = %+v, want count 2 with an empty reason", got[0])
	}
	if got[1].Count != 1 || got[1].Reason == "" {
		t.Errorf("lossy chapter item = %+v, want count 1 with a metadata reason", got[1])
	}

	titleCap := NewCapabilities(FormatMP4, false, Capability{Write: AccessFull},
		Capability{Write: AccessNone},
		Capability{Write: AccessFull, ChapterTitleByteMax: 3}, AccessNone, nil)
	m2 := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, Title: "ok"},
		{Start: time.Second, Title: "way too long"},
	}}
	got2 := chapterItems(ProjectTransferOnto(m2, nil, titleCap).Items)
	if len(got2) != 2 || got2[0].Disposition != Carried || got2[1].Disposition != Lossy {
		t.Fatalf("title-cap chapter items = %+v, want [Carried, Lossy]", got2)
	}
	if got2[1].Reason != "chapter title is too long and was truncated" {
		t.Errorf("title-cap lossy reason = %q, want the truncation message", got2[1].Reason)
	}
}

// TestProjectTransferReasonUsesSniffedMIME: drop reason uses sniffed MIME, not stored label.
func TestProjectTransferReasonUsesSniffedMIME(t *testing.T) {
	gif := testfile.GIF()
	pics := Capability{Write: AccessFull, PictureMIMEs: []string{"image/jpeg", "image/png", "image/bmp"}}
	caps := NewCapabilities(FormatMP4, false, Capability{Write: AccessFull}, pics,
		Capability{Write: AccessNone}, AccessNone, nil)

	for _, label := range []string{"image/jpeg", ""} {
		m := &Media{Format: FormatFLAC, Pictures: []Picture{{Type: PicFrontCover, MIME: label, Data: gif}}}
		var dropped *TransferItem
		items := ProjectTransferOnto(m, nil, caps).Items
		for i := range items {
			if items[i].Kind == TransferPicture && items[i].Disposition == Dropped {
				dropped = &items[i]
			}
		}
		if dropped == nil {
			t.Fatalf("label %q: expected the GIF cover dropped", label)
		}
		if want := "MP4 cannot store image/gif"; dropped.Reason != want {
			t.Errorf("label %q: reason = %q, want %q (sniffed type, not the stored label)", label, dropped.Reason, want)
		}
	}
}

// TestRepresentableUsesSniffedMIME: Representable uses sniffed MIME. Bytes beat label; empty payload is not representable.
func TestRepresentableUsesSniffedMIME(t *testing.T) {
	jpeg := testfile.JPEG()
	gif := testfile.GIF()
	mp4 := Capability{Write: AccessFull, PictureMIMEs: []string{"image/jpeg", "image/png", "image/bmp"}}

	if !Representable(mp4, Picture{MIME: "image/jpg", Data: jpeg}) {
		t.Error("a JPEG labeled image/jpg should be representable (the sniff canonicalizes to image/jpeg)")
	}
	if !Representable(mp4, Picture{MIME: "IMAGE/JPEG", Data: jpeg}) {
		t.Error("a JPEG labeled IMAGE/JPEG should be representable")
	}
	if Representable(mp4, Picture{MIME: "image/jpeg", Data: gif}) {
		t.Error("a GIF mislabeled image/jpeg must not be representable (the bytes win over the label)")
	}
	if Representable(mp4, Picture{MIME: "image/gif"}) {
		t.Error("a label-only image/gif (no bytes) must not be representable")
	}
	if Representable(mp4, Picture{MIME: "image/png"}) {
		t.Error("a label-only image/png (no bytes) must not be representable either: an empty payload is not a PNG")
	}
}

// TestChaptersLoseMetadata: ChapterLossStartTitleOnly flags dropped metadata (incl. any language).
func TestChaptersLoseMetadata(t *testing.T) {
	sec := func(s int) time.Duration { return time.Duration(s) * time.Second }
	cases := []struct {
		name string
		chs  []Chapter
		want bool
	}{
		{"plain", []Chapter{{Start: 0, Title: "A"}, {Start: sec(5), Title: "B"}}, false},
		{"uniform-ietf", []Chapter{{Start: 0, LanguageIETF: "en-US"}, {Start: sec(5), LanguageIETF: "en-US"}}, true},
		{"uniform-iso+ietf", []Chapter{{LanguageIETF: "en-US", Language: "eng"}, {Start: sec(5), LanguageIETF: "en-US", Language: "eng"}}, true},
		{"varying-iso", []Chapter{{Language: "fre"}, {Start: sec(5), Language: "ger"}}, true},
		{"varying-ietf", []Chapter{{LanguageIETF: "fr-FR"}, {Start: sec(5), LanguageIETF: "de-DE"}}, true},
		{"hidden", []Chapter{{Hidden: true}}, true},
		{"disabled", []Chapter{{Disabled: true}}, true},
		{"gapped-end", []Chapter{{End: sec(3)}, {Start: sec(5)}}, true},
		{"contiguous-end", []Chapter{{End: sec(5)}, {Start: sec(5)}}, false},
		{"last-end", []Chapter{{}, {Start: sec(5), End: sec(9)}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ChaptersLoseMetadata(c.chs, ChapterLossStartTitleOnly); got != c.want {
				t.Errorf("ChaptersLoseMetadata = %v, want %v", got, c.want)
			}
			if ChaptersLoseMetadata(c.chs, ChapterLossNone) {
				t.Error("ChapterLossNone reported a metadata loss")
			}
		})
	}
}

// TestChaptersLoseMetadataInteriorEnds: MP4 keeps final end; only interior gapped end is lossy.
func TestChaptersLoseMetadataInteriorEnds(t *testing.T) {
	sec := func(s int) time.Duration { return time.Duration(s) * time.Second }
	cases := []struct {
		name string
		chs  []Chapter
		want bool
	}{
		{"plain", []Chapter{{Start: 0, Title: "A"}, {Start: sec(5), Title: "B"}}, false},
		{"uniform-ietf", []Chapter{{LanguageIETF: "en-US"}, {Start: sec(5), LanguageIETF: "en-US"}}, true},
		{"varying-iso", []Chapter{{Language: "fre"}, {Start: sec(5), Language: "ger"}}, true},
		{"hidden", []Chapter{{Hidden: true}}, true},
		{"disabled", []Chapter{{Disabled: true}}, true},
		{"gapped-interior-end", []Chapter{{End: sec(3)}, {Start: sec(5)}}, true},
		{"contiguous-end", []Chapter{{End: sec(5)}, {Start: sec(5)}}, false},
		{"last-end-kept", []Chapter{{}, {Start: sec(5), End: sec(9)}}, false},
		{"last-end-plus-interior-gap", []Chapter{{End: sec(3)}, {Start: sec(5), End: sec(9)}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ChaptersLoseMetadata(c.chs, ChapterLossInteriorEndsLangFlags); got != c.want {
				t.Errorf("ChaptersLoseMetadata(InteriorEnds) = %v, want %v", got, c.want)
			}
		})
	}
}

// TestChaptersLoseMetadataLangFlags: CHAP keeps ends; language and visibility flags are lossy.
func TestChaptersLoseMetadataLangFlags(t *testing.T) {
	sec := func(s int) time.Duration { return time.Duration(s) * time.Second }
	cases := []struct {
		name string
		chs  []Chapter
		want bool
	}{
		{"plain", []Chapter{{Start: 0, Title: "A"}, {Start: sec(5), Title: "B"}}, false},
		{"gapped-end-kept", []Chapter{{End: sec(3)}, {Start: sec(5)}}, false},
		{"last-end-kept", []Chapter{{}, {Start: sec(5), End: sec(9)}}, false},
		{"uniform-iso", []Chapter{{Language: "eng"}, {Start: sec(5), Language: "eng"}}, true},
		{"uniform-ietf", []Chapter{{LanguageIETF: "en-US"}, {Start: sec(5), LanguageIETF: "en-US"}}, true},
		{"hidden", []Chapter{{Hidden: true}}, true},
		{"disabled", []Chapter{{Disabled: true}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ChaptersLoseMetadata(c.chs, ChapterLossLangFlags); got != c.want {
				t.Errorf("ChaptersLoseMetadata(LangFlags) = %v, want %v", got, c.want)
			}
		})
	}
}

// TestProjectTransferChapterGrading: start+title destination marks Lossy only when metadata is dropped.
func TestProjectTransferChapterGrading(t *testing.T) {
	sec := func(s int) time.Duration { return time.Duration(s) * time.Second }
	startTitleOnly := Capability{Write: AccessFull, ChapterLoss: ChapterLossStartTitleOnly, Fidelity: "start and title only"}
	mp4 := NewCapabilities(FormatMP4, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, startTitleOnly, AccessNone, nil)

	chapterItem := func(caps Capabilities, chs []Chapter) TransferItem {
		for _, it := range ProjectTransferOnto(&Media{Format: FormatMatroska, Chapters: chs}, nil, caps).Items {
			if it.Kind == TransferChapter {
				return it
			}
		}
		t.Fatal("no chapter item")
		return TransferItem{}
	}

	lossy := []Chapter{{End: sec(3), Title: "A", Language: "fre", Hidden: true}, {Start: sec(5), Title: "B", Language: "ger"}}
	if it := chapterItem(mp4, lossy); it.Disposition != Lossy || it.Reason != "start and title only" {
		t.Errorf("metadata-bearing chapters = %s/%q, want Lossy with the fidelity reason", it.Disposition, it.Reason)
	}
	mp4UniformLang := []Chapter{{Title: "A", LanguageIETF: "en-US"}, {Start: sec(5), Title: "B", LanguageIETF: "en-US"}}
	if it := chapterItem(mp4, mp4UniformLang); it.Disposition != Lossy || it.Reason != "start and title only" {
		t.Errorf("uniform-language chapters = %s/%q, want Lossy (MP4 stores no per-chapter language)", it.Disposition, it.Reason)
	}
	plainMP4 := []Chapter{{Title: "A"}, {Start: sec(5), Title: "B"}}
	if it := chapterItem(mp4, plainMP4); it.Disposition != Carried {
		t.Errorf("plain language-free chapters = %s, want Carried", it.Disposition)
	}
	lossless := NewCapabilities(FormatMatroska, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, Capability{Write: AccessFull}, AccessNone, nil)
	if it := chapterItem(lossless, lossy); it.Disposition != Carried {
		t.Errorf("Matroska->Matroska chapters = %s, want Carried", it.Disposition)
	}

	langFlags := Capability{Write: AccessFull, ChapterLoss: ChapterLossLangFlags, Fidelity: "language and flags dropped"}
	mp3 := NewCapabilities(FormatMP3, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, langFlags, AccessPartial, nil)
	uniformLang := []Chapter{{Title: "A", LanguageIETF: "en-US"}, {Start: sec(5), Title: "B", LanguageIETF: "en-US"}}
	if it := chapterItem(mp3, uniformLang); it.Disposition != Lossy {
		t.Errorf("Matroska->MP3 uniform-language chapters = %s, want Lossy (CHAP has no language field)", it.Disposition)
	}
	plain := []Chapter{{Title: "A", End: sec(3)}, {Start: sec(5), Title: "B"}}
	if it := chapterItem(mp3, plain); it.Disposition != Carried {
		t.Errorf("Matroska->MP3 plain chapters = %s, want Carried (CHAP keeps ends)", it.Disposition)
	}
}

// TestProjectTransferSyncedLyricsTimestampClamp: line past SyncedLyricsTimeMax is Lossy (write clamps).
func TestProjectTransferSyncedLyricsTimestampClamp(t *testing.T) {
	dst := NewCapabilities(FormatMP3, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, Capability{Write: AccessFull}, AccessPartial, nil).
		WithSyncedLyrics(Capability{Write: AccessFull, SyncedLyricsTimeMax: 100 * time.Second})

	syncedItem := func(sls []SyncedLyrics) TransferItem {
		for _, it := range ProjectTransferOnto(&Media{Format: FormatFLAC, SyncedLyrics: sls}, nil, dst).Items {
			if it.Kind == TransferSyncedLyric {
				return it
			}
		}
		t.Fatal("no synced-lyrics item")
		return TransferItem{}
	}

	over := []SyncedLyrics{{Lines: []SyncedLine{{Time: 0, Text: "a"}, {Time: 200 * time.Second, Text: "b"}}}}
	if it := syncedItem(over); it.Disposition != Lossy || it.Reason == "" {
		t.Errorf("a line past the timestamp ceiling = %s/%q, want Lossy with a reason", it.Disposition, it.Reason)
	}
	within := []SyncedLyrics{{Lines: []SyncedLine{{Time: 0, Text: "a"}, {Time: 50 * time.Second, Text: "b"}}}}
	if it := syncedItem(within); it.Disposition != Carried {
		t.Errorf("a set within the ceiling = %s, want Carried", it.Disposition)
	}
	atMax := []SyncedLyrics{{Lines: []SyncedLine{{Time: 100 * time.Second, Text: "edge"}}}} // clamp is strictly greater
	if it := syncedItem(atMax); it.Disposition != Carried {
		t.Errorf("a line exactly at the ceiling = %s, want Carried (clamp is strictly greater)", it.Disposition)
	}
}

// TestProjectTransferSplitsSyncedLyricsByMetadataLoss: per-set split for LRC (language/descriptor) and timestamp clamp.
func TestProjectTransferSplitsSyncedLyricsByMetadataLoss(t *testing.T) {
	syncedItems := func(items []TransferItem) []TransferItem {
		var out []TransferItem
		for _, it := range items {
			if it.Kind == TransferSyncedLyric {
				out = append(out, it)
			}
		}
		return out
	}
	lrc := NewCapabilities(FormatFLAC, false, Capability{Write: AccessFull},
		Capability{Write: AccessNone}, Capability{Write: AccessNone}, AccessNone, nil).
		WithSyncedLyrics(Capability{Write: AccessFull, SyncedLyricsLoss: SyncedLyricsLossLanguage, SyncedLyricsTimeMax: 100 * time.Second})

	m := &Media{Format: FormatMatroska, SyncedLyrics: []SyncedLyrics{
		{Lines: []SyncedLine{{Time: 0, Text: "plain"}}},
		{Description: "chorus", Lines: []SyncedLine{{Time: time.Second, Text: "meta"}}},
	}}
	got := syncedItems(ProjectTransferOnto(m, nil, lrc).Items)
	if len(got) != 2 || got[0].Disposition != Carried || got[1].Disposition != Lossy {
		t.Fatalf("synced-lyrics items = %+v, want [Carried, Lossy]", got)
	}
	if got[0].Count != 1 || got[0].Reason != "" {
		t.Errorf("carried synced item = %+v, want count 1 with an empty reason", got[0])
	}
	if got[1].Count != 1 || got[1].Reason == "" {
		t.Errorf("lossy synced item = %+v, want count 1 with a metadata reason", got[1])
	}
	if c, l, d := (TransferReport{Items: ProjectTransferOnto(m, nil, lrc).Items}).Counts(); c != 1 || l != 1 || d != 0 {
		t.Errorf("counts = (%d,%d,%d), want (1,1,0)", c, l, d)
	}

	clampOnly := NewCapabilities(FormatMP3, false, Capability{Write: AccessFull},
		Capability{Write: AccessNone}, Capability{Write: AccessNone}, AccessNone, nil).
		WithSyncedLyrics(Capability{Write: AccessFull, SyncedLyricsTimeMax: 100 * time.Second})
	m2 := &Media{Format: FormatMatroska, SyncedLyrics: []SyncedLyrics{
		{Lines: []SyncedLine{{Time: 0, Text: "ok"}}},
		{Lines: []SyncedLine{{Time: 200 * time.Second, Text: "late"}}},
	}}
	got2 := syncedItems(ProjectTransferOnto(m2, nil, clampOnly).Items)
	if len(got2) != 2 || got2[0].Disposition != Carried || got2[1].Disposition != Lossy {
		t.Fatalf("clamp-axis synced items = %+v, want [Carried, Lossy]", got2)
	}
	if got2[1].Reason != "a synced-lyric timestamp is too large and was clamped" {
		t.Errorf("clamp-axis lossy reason = %q, want the clamp message", got2[1].Reason)
	}
}

// TestProjectTransferEmptyMetadata: empty source yields no items.
func TestProjectTransferEmptyMetadata(t *testing.T) {
	m := &Media{Format: FormatFLAC}
	items := ProjectTransferOnto(m, nil, NewCapabilities(FormatMP4, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, Capability{Write: AccessFull}, AccessNone, nil)).Items
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
	if !(TransferReport{Items: items}).Lossless() {
		t.Error("empty transfer should be lossless")
	}
}

// TestProjectTransferTrimsNumericDateValues: date/numeric fields trimmed before grading.
func TestProjectTransferTrimsNumericDateValues(t *testing.T) {
	dropsIfPadded := func(v string) bool { return v != strings.TrimSpace(v) }
	padSensitive := WithValueDrop(Capability{Read: AccessFull, Write: AccessFull}, dropsIfPadded)
	caps := NewCapabilities(FormatMP3, false,
		Capability{Read: AccessFull, Write: AccessFull}, Capability{}, Capability{}, AccessNone,
		map[tag.Key]Capability{tag.RecordingDate: padSensitive, tag.Title: padSensitive})

	m := &Media{Tags: tag.NewTagSet()}
	m.Tags.Set(tag.RecordingDate, " 2021 ")
	m.Tags.Set(tag.Title, " padded ")

	var rec, title TransferItem
	for _, it := range ProjectTransferOnto(m, nil, caps).Items {
		switch it.Key {
		case tag.RecordingDate:
			rec = it
		case tag.Title:
			title = it
		}
	}
	if rec.Disposition == Dropped {
		t.Errorf("RECORDINGDATE graded %s; expected a padded date to be trimmed to its stored form before grading", rec.Disposition)
	}
	if title.Disposition != Dropped {
		t.Errorf("TITLE graded %s; a non-numeric/non-date value should not be trimmed (the predicate should fire)", title.Disposition)
	}
}

// TestProjectTransferPictureSlotPartition: APE slot hook drops unslotted pictures; kept ones still split carried/lossy.
func TestProjectTransferPictureSlotPartition(t *testing.T) {
	keepFirst := func(ps []Picture, _ []bool) []int {
		if len(ps) == 0 {
			return nil
		}
		return []int{0}
	}
	pics := WithPictureSlots(Capability{Write: AccessFull, PictureLoss: PictureLossRoleAndDescription},
		keepFirst, "one slot only")
	caps := NewCapabilities(FormatWavPack, false, Capability{Write: AccessFull}, pics,
		Capability{Write: AccessNone}, AccessNone, nil)

	m := &Media{Format: FormatFLAC, Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/jpeg"},
		{Type: PicArtist, MIME: "image/jpeg"},
	}}
	items := ProjectTransferOnto(m, nil, caps).Items
	var disps []Disposition
	for _, it := range items {
		if it.Kind == TransferPicture {
			disps = append(disps, it.Disposition)
			if it.Disposition == Dropped {
				if it.Count != 1 || it.Reason != "one slot only" {
					t.Errorf("dropped item = %+v, want count 1 with the slot reason", it)
				}
			}
		}
	}
	if len(disps) != 2 || disps[0] != Carried || disps[1] != Dropped {
		t.Fatalf("picture dispositions = %v, want [Carried Dropped]", disps)
	}
	if c, l, d := (TransferReport{Items: items}).Counts(); c != 1 || l != 0 || d != 1 {
		t.Errorf("counts = (%d,%d,%d), want (1,0,1)", c, l, d)
	}

	one := &Media{Format: FormatFLAC, Pictures: []Picture{{Type: PicFrontCover, MIME: "image/jpeg"}}}
	for _, it := range ProjectTransferOnto(one, nil, caps).Items {
		if it.Kind == TransferPicture && it.Disposition == Dropped {
			t.Errorf("single-picture set produced a dropped item: %+v", it)
		}
	}

	// No picture write: one whole-set drop; slot partition must not add a second item.
	none := caps
	none.Pictures.Write = AccessNone
	var pictureItems []TransferItem
	for _, it := range ProjectTransferOnto(m, nil, none).Items {
		if it.Kind == TransferPicture {
			pictureItems = append(pictureItems, it)
		}
	}
	if len(pictureItems) != 1 || pictureItems[0].Count != 2 || pictureItems[0].Reason == "one slot only" {
		t.Errorf("no-picture destination items = %+v, want one whole-set drop with the capability reason", pictureItems)
	}

	if _, _, ok := PartitionPictureSlotsEdited(Capability{Write: AccessFull}, m.Pictures, nil); ok {
		t.Error("a capability without slots claimed to have them")
	}
	keptIdx, reason, ok := PartitionPictureSlotsEdited(caps.Pictures, m.Pictures, []bool{false, true})
	if !ok || reason != "one slot only" || len(keptIdx) != 1 {
		t.Errorf("PartitionPictureSlotsEdited = %v,%q,%v, want the hook's selection and reason", keptIdx, reason, ok)
	}
}

// TestProjectTransferGradesChaptersAsGiven: grading uses chapters as given; OpenRunToEOFEnd is caller's job.
func TestProjectTransferGradesChaptersAsGiven(t *testing.T) {
	caps := NewCapabilities(FormatMP4, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull},
		Capability{Write: AccessFull, ChapterLoss: ChapterLossStartTitleOnly, Fidelity: "start and title only"},
		AccessNone, nil)

	chapterItem := func(chs []Chapter) TransferItem {
		m := &Media{
			Format:     FormatMatroska,
			Properties: Properties{Tracks: []AudioTrack{{Duration: 10 * time.Second}}},
			Chapters:   chs,
		}
		for _, it := range ProjectTransferOnto(m, nil, caps).Items {
			if it.Kind == TransferChapter {
				return it
			}
		}
		t.Fatal("no chapter item")
		return TransferItem{}
	}

	literal := []Chapter{{Title: "A", End: 10 * time.Second}}
	if it := chapterItem(literal); it.Disposition != Lossy {
		t.Errorf("literal run-to-EOF end = %s, want Lossy", it.Disposition)
	}
	opened := OpenRunToEOFEnd(literal, 10*time.Second)
	if it := chapterItem(opened); it.Disposition != Carried {
		t.Errorf("pre-opened end = %s, want Carried", it.Disposition)
	}
}

// Two formats hold one value when the strings agree, when an MP4 side stores the key as a number
// and the numbers agree, or when the key is a boolean and the words mean the same.
func TestTransferValuesEqual(t *testing.T) {
	for _, c := range []struct {
		key    tag.Key
		a, b   []string
		fa, fb Format
		want   bool
	}{
		{tag.Title, []string{"A"}, []string{"A"}, FormatFLAC, FormatMP3, true},
		{tag.Title, []string{"A"}, []string{"B"}, FormatFLAC, FormatMP3, false},
		{tag.Title, []string{"A", "B"}, []string{"A"}, FormatFLAC, FormatMP3, false},
		{tag.TrackNumber, []string{"03"}, []string{"3"}, FormatFLAC, FormatMP4, true},
		{tag.TrackNumber, []string{"03"}, []string{"3"}, FormatMP4, FormatFLAC, true},
		{tag.TrackNumber, []string{"03"}, []string{"3"}, FormatFLAC, FormatMP3, false},
		{tag.Title, []string{"03"}, []string{"3"}, FormatFLAC, FormatMP4, false},
		{tag.Title, nil, nil, FormatFLAC, FormatMP4, true},
		{tag.Compilation, []string{"yes"}, []string{"1"}, FormatFLAC, FormatMP3, true},
		{tag.Compilation, []string{" No "}, []string{"0"}, FormatFLAC, FormatFLAC, true},
		{tag.Compilation, []string{"yes"}, []string{"0"}, FormatFLAC, FormatMP3, false},
		{tag.Compilation, []string{"maybe"}, []string{"1"}, FormatFLAC, FormatMP3, false},
		{tag.Compilation, []string{"1", "0"}, []string{"1"}, FormatFLAC, FormatMP3, false},
		{tag.Title, []string{"yes"}, []string{"1"}, FormatFLAC, FormatMP3, false},
	} {
		if got := TransferValuesEqual(c.key, c.a, c.b, c.fa, c.fb); got != c.want {
			t.Errorf("TransferValuesEqual(%s, %q, %q, %s, %s) = %v, want %v", c.key, c.a, c.b, c.fa, c.fb, got, c.want)
		}
	}
}

func tagsOf(kv ...string) tag.TagSet {
	ts := tag.NewTagSet()
	for i := 0; i+1 < len(kv); i += 2 {
		ts.Add(tag.Key(kv[i]), kv[i+1])
	}
	return ts
}

// A field graded from the capability table is held to what the plan leaves in the destination.
func TestReconcileTransferFields(t *testing.T) {
	for _, c := range []struct {
		name                string
		grade               Disposition
		src, before, result tag.TagSet
		want                Disposition
		reason              string
	}{
		{"stored as given", Carried, tagsOf("TITLE", "A"), tagsOf(), tagsOf("TITLE", "A"), Carried, ""},
		{"first value only", Carried, tagsOf("TITLE", "A", "TITLE", "B"), tagsOf(), tagsOf("TITLE", "A"), Lossy, "only the first of 2 values is stored"},
		{"fewer values", Carried, tagsOf("TITLE", "A", "TITLE", "B", "TITLE", "C"), tagsOf(), tagsOf("TITLE", "A", "TITLE", "B"), Lossy, "only the first 2 of 3 values are stored"},
		{"not stored", Carried, tagsOf("TITLE", "A"), tagsOf(), tagsOf(), Dropped, "the destination does not store this value"},
		{"destination keeps its own", Carried, tagsOf("TITLE", "A"), tagsOf("TITLE", "B"), tagsOf("TITLE", "B"), Dropped, "the destination does not store this value and keeps the one it had"},
		{"stored under another key", Carried, tagsOf("TITLE", "A"), tagsOf(), tagsOf("ALBUM", "A"), Lossy, "stored as ALBUM"},
		{"another key the source holds too", Carried, tagsOf("TITLE", "A", "ALBUM", "A"), tagsOf(), tagsOf("ALBUM", "A"), Dropped, "the destination does not store this value"},
		{"another key the destination held", Carried, tagsOf("TITLE", "A"), tagsOf("ALBUM", "A"), tagsOf("ALBUM", "A"), Dropped, "the destination does not store this value"},
		{"joined", Carried, tagsOf("TITLE", "A", "TITLE", "B"), tagsOf(), tagsOf("TITLE", "A / B"), Lossy, `the values are stored joined as "A / B"`},
		{"one other value for several", Carried, tagsOf("TITLE", "3/12", "TITLE", "4"), tagsOf(), tagsOf("TITLE", "3"), Lossy, `stored as "3"`},
		{"respelled", Carried, tagsOf("TITLE", "A"), tagsOf(), tagsOf("TITLE", "a"), Lossy, `stored as "a"`},
		{"respelled values", Carried, tagsOf("TITLE", "A", "TITLE", "B"), tagsOf(), tagsOf("TITLE", "a", "TITLE", "b"), Lossy, `stored as "a", "b"`},
		{"lossy and stored", Lossy, tagsOf("TITLE", "A"), tagsOf(), tagsOf("TITLE", "a"), Lossy, "graded"},
		{"lossy and stored as given", Lossy, tagsOf("TITLE", "A"), tagsOf(), tagsOf("TITLE", "A"), Lossy, "graded"},
		{"not stored, and the held value gone", Carried, tagsOf("TITLE", "A"), tagsOf("TITLE", "B"), tagsOf(), Dropped, "the destination does not store this value, and loses the one it held"},
		{"stored under a key of the destination's own, in place of its value", Carried, tagsOf("TITLE", "A"), tagsOf("ALBUM", "B"), tagsOf("ALBUM", "A"), Dropped, "the destination stores it as ALBUM, which already holds another value"},
		{"lossy and stored in place of the destination's own", Lossy, tagsOf("TITLE", "A"), tagsOf("ALBUM", "B"), tagsOf("ALBUM", "A"), Dropped, "the destination stores it as ALBUM, which already holds another value"},
		{"lossy and found where an earlier copy stored it", Lossy, tagsOf("TITLE", "A"), tagsOf("ALBUM", "A"), tagsOf("ALBUM", "A"), Lossy, "graded"},
		{"read back beside a value of the destination's own key", Carried, tagsOf("TITLE", "A"), tagsOf("ALBUM", "B"), tagsOf("ALBUM", "B", "ALBUM", "A"), Dropped, "the destination stores it as ALBUM, which already holds another value"},
		{"lossy and read back beside the destination's own", Lossy, tagsOf("TITLE", "A"), tagsOf("ALBUM", "B"), tagsOf("ALBUM", "B", "ALBUM", "A"), Dropped, "the destination stores it as ALBUM, which already holds another value"},
		{"beside a key the destination changed for another reason", Carried, tagsOf("TITLE", "A"), tagsOf("ALBUM", "B"), tagsOf("ALBUM", "C"), Dropped, "the destination does not store this value"},
		{"beside values of a key the source carries too", Carried, tagsOf("TITLE", "A", "ALBUM", "B"), tagsOf("ALBUM", "C"), tagsOf("ALBUM", "B", "ALBUM", "A"), Dropped, "the destination does not store this value"},
		{"beside values the destination held so all along", Carried, tagsOf("TITLE", "A"), tagsOf("ALBUM", "B", "ALBUM", "A"), tagsOf("ALBUM", "B", "ALBUM", "A"), Dropped, "the destination does not store this value"},
		{"beside another value under a key the destination did not hold", Carried, tagsOf("TITLE", "A"), tagsOf(), tagsOf("ALBUM", "B", "ALBUM", "A"), Dropped, "the destination does not store this value"},
		{"lossy and not stored", Lossy, tagsOf("TITLE", "A"), tagsOf(), tagsOf(), Dropped, "the destination does not store this value"},
		{"lossy and stored under another key", Lossy, tagsOf("TITLE", "A"), tagsOf(), tagsOf("ALBUM", "A"), Lossy, "graded"},
		{"dropped stays", Dropped, tagsOf("TITLE", "A"), tagsOf(), tagsOf("TITLE", "A"), Dropped, "graded"},
		{"dropped beside the value the destination holds", Dropped, tagsOf("TITLE", "A"), tagsOf("TITLE", "B"), tagsOf("TITLE", "B"), Dropped, "graded"},
		{"dropped, and the held value gone with another field's write", Dropped, tagsOf("TITLE", "A"), tagsOf("TITLE", "B"), tagsOf(), Dropped, "graded; the destination loses the value it held"},
		{"excluded stays", Excluded, tagsOf("TITLE", "A"), tagsOf(), tagsOf(), Excluded, "graded"},
	} {
		items := []TransferItem{{Kind: TransferField, Key: tag.Title, Count: 1, Disposition: c.grade}}
		if c.grade != Carried {
			items[0].Reason = "graded"
		}
		got := ReconcileTransfer(TransferProjection{Items: items},
			&Media{Format: FormatFLAC, Tags: c.src}, &Media{Format: FormatMP3, Tags: c.before}, &Media{Format: FormatMP3, Tags: c.result})
		if len(got) != 1 || got[0].Disposition != c.want || got[0].Reason != c.reason {
			t.Errorf("%s: got %+v, want %s %q", c.name, got, c.want, c.reason)
		}
		if items[0].Disposition != c.grade {
			t.Errorf("%s: the items given were changed", c.name)
		}
	}
	// A drop found in one result is said again of the next: what became of the held value is
	// read off the result at hand, whatever was said of the one before.
	src, held := &Media{Tags: tagsOf("TITLE", "A")}, &Media{Tags: tagsOf("TITLE", "B")}
	for _, said := range []string{lostHeldReason, keptHeldReason, "graded" + lostHeldSuffix} {
		p := TransferProjection{Items: []TransferItem{{Kind: TransferField, Key: tag.Title, Count: 1, Disposition: Dropped, Reason: said}}}
		kept, lost := keptHeldReason, lostHeldReason
		if strings.HasPrefix(said, "graded") {
			kept, lost = "graded", "graded"+lostHeldSuffix
		}
		if got := ReconcileTransfer(p, src, held, held); got[0].Reason != kept {
			t.Errorf("said %q, the value still held: %q, want %q", said, got[0].Reason, kept)
		}
		if got := ReconcileTransfer(p, src, held, &Media{Tags: tagsOf()}); got[0].Reason != lost {
			t.Errorf("said %q, the value gone: %q, want %q", said, got[0].Reason, lost)
		}
	}
}

// A trimmable key is compared in the form it is written, and an MP4 number by its value.
func TestReconcileTransferComparesTheWrittenForm(t *testing.T) {
	items := []TransferItem{
		{Kind: TransferField, Key: tag.TrackNumber, Count: 1},
		{Kind: TransferField, Key: tag.MediaType, Count: 1},
	}
	got := ReconcileTransfer(TransferProjection{Items: items},
		&Media{Format: FormatFLAC, Tags: tagsOf("TRACKNUMBER", "03", "MEDIATYPE", " 2 ")},
		&Media{Format: FormatMP4, Tags: tagsOf()},
		&Media{Format: FormatMP4, Tags: tagsOf("TRACKNUMBER", "3", "MEDIATYPE", "2")})
	for _, it := range got {
		if it.Disposition != Carried {
			t.Errorf("%s: %s (%s), want carried", it.Key, it.Disposition, it.Reason)
		}
	}
}

// A value the destination holds as the source does, spaces a write would trim included, is the
// source's value: nothing was written and nothing is lost.
func TestReconcileTransferKeepsAValueHeldUntrimmed(t *testing.T) {
	held := &Media{Format: FormatFLAC, Tags: tagsOf("TRACKNUMBER", " 3 ")}
	items := []TransferItem{{Kind: TransferField, Key: tag.TrackNumber, Count: 1}}
	if got := ReconcileTransfer(TransferProjection{Items: items}, held, held, held); !slices.Equal(got, items) {
		t.Errorf("got %+v, want it carried", got)
	}
}

// Each picture is held to its own fate in the result: one graded carried that the destination
// holds altered is lossy, and one it does not hold at all is dropped.
func TestReconcileTransferPictures(t *testing.T) {
	png, jpeg := testfile.PNG(), testfile.JPEG()
	src := &Media{Tags: tag.NewTagSet(), Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/png", Description: "front", Data: png},
		{Type: PicBackCover, MIME: "image/jpeg", Data: jpeg},
		{Type: PicArtist, MIME: "image/png", Data: append(testfile.PNG(), 1)},
	}}
	blank := &Media{Tags: tag.NewTagSet()}
	p := ProjectTransferOnto(src, nil, writableCaps(FormatFLAC))
	if want := []TransferItem{{Kind: TransferPicture, Count: 3, Disposition: Carried}}; !slices.Equal(p.Items, want) {
		t.Fatalf("projected %+v", p.Items)
	}
	result := &Media{Tags: tag.NewTagSet(), Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/png", Data: png},
		{Type: PicBackCover, MIME: "image/jpeg", Data: jpeg},
	}}
	const altered = "stored with another type, description or format than the source gives"
	got := ReconcileTransfer(p, src, blank, result)
	want := []TransferItem{
		{Kind: TransferPicture, Count: 1, Disposition: Carried},
		{Kind: TransferPicture, Count: 1, Disposition: Lossy, Reason: altered},
		{Kind: TransferPicture, Count: 1, Disposition: Dropped, Reason: "the destination does not store this picture"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	// What the grade already says is left as graded.
	graded := TransferProjection{
		Items: []TransferItem{
			{Kind: TransferPicture, Count: 1, Disposition: Carried},
			{Kind: TransferPicture, Count: 1, Disposition: Lossy, Reason: "graded lossy"},
			{Kind: TransferPicture, Count: 1, Disposition: Dropped, Reason: "graded dropped"},
		},
		pictures: []Disposition{Lossy, Carried, Dropped},
	}
	if got := ReconcileTransfer(graded, src, blank, result); !slices.Equal(got, graded.Items) {
		t.Errorf("a grade the result bears out changed: %+v", got)
	}
	// A picture graded lossy that reads back as given does not stand in for one graded carried
	// that the writer changed: the first here is altered, the second exact.
	crossed := graded
	crossed.pictures = []Disposition{Carried, Lossy, Dropped}
	want = []TransferItem{
		{Kind: TransferPicture, Count: 1, Disposition: Lossy, Reason: "graded lossy"},
		{Kind: TransferPicture, Count: 1, Disposition: Lossy, Reason: altered},
		{Kind: TransferPicture, Count: 1, Disposition: Dropped, Reason: "graded dropped"},
	}
	if got := ReconcileTransfer(crossed, src, blank, result); !slices.Equal(got, want) {
		t.Errorf("grades crossed with fates:\n got %+v\nwant %+v", got, want)
	}
	// One stored picture is one source picture's, however many the source holds alike.
	twins := &Media{Tags: tag.NewTagSet(), Pictures: []Picture{src.Pictures[1], src.Pictures[1]}}
	got = ReconcileTransfer(ProjectTransferOnto(twins, nil, writableCaps(FormatFLAC)), twins, blank, result)
	want = []TransferItem{
		{Kind: TransferPicture, Count: 1, Disposition: Carried},
		{Kind: TransferPicture, Count: 1, Disposition: Dropped, Reason: "the destination does not store this picture"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("two pictures alike, one stored:\n got %+v\nwant %+v", got, want)
	}
	// A store of uniquely named items leaves the second of three out: the grade of each
	// picture is its own, and the two it holds are found as given.
	slots := writableCaps(FormatFLAC)
	slots.Pictures = WithPictureSlots(slots.Pictures, func([]Picture, []bool) []int { return []int{0, 2} }, "one picture to a name")
	held := &Media{Tags: tag.NewTagSet(), Pictures: []Picture{src.Pictures[0], src.Pictures[2]}}
	want = []TransferItem{
		{Kind: TransferPicture, Count: 2, Disposition: Carried},
		{Kind: TransferPicture, Count: 1, Disposition: Dropped, Reason: "one picture to a name"},
	}
	if got := ReconcileTransfer(ProjectTransferOnto(src, nil, slots), src, blank, held); !slices.Equal(got, want) {
		t.Errorf("the second of three left out of a slot store:\n got %+v\nwant %+v", got, want)
	}
	// Of two pictures alike, such a store drops the first: the one it holds is the second's, and
	// the dropped one takes no place in the result.
	second := writableCaps(FormatFLAC)
	second.Pictures = WithPictureSlots(second.Pictures, func([]Picture, []bool) []int { return []int{1} }, "one picture to a name")
	want = []TransferItem{
		{Kind: TransferPicture, Count: 1, Disposition: Carried},
		{Kind: TransferPicture, Count: 1, Disposition: Dropped, Reason: "one picture to a name"},
	}
	if got := ReconcileTransfer(ProjectTransferOnto(twins, nil, second), twins, blank, result); !slices.Equal(got, want) {
		t.Errorf("two pictures alike, the first left out of a slot store:\n got %+v\nwant %+v", got, want)
	}
	// Pictures are compared as a read settles them, not as their blocks declare them.
	declared := &Media{Tags: tag.NewTagSet(), Pictures: []Picture{{Type: PicFrontCover, MIME: "image/jpg", Width: 9, Data: jpeg}}}
	settled := &Media{Tags: tag.NewTagSet(), Pictures: []Picture{{Type: PicFrontCover, MIME: "", Data: jpeg}}}
	carried := TransferProjection{Items: []TransferItem{{Kind: TransferPicture, Count: 1, Disposition: Carried}}, pictures: []Disposition{Carried}}
	if got := ReconcileTransfer(carried, declared, blank, settled); !slices.Equal(got, carried.Items) {
		t.Errorf("a loosely declared picture: %+v, want it carried", got)
	}
}

// Each chapter is held to its own fate: the one stored at its start under its title, carried
// where the rest of it reads back too.
func TestReconcileTransferChapters(t *testing.T) {
	const altered = "stored with another time, title, language or flag than the source gives"
	const absent = "the destination does not store this chapter"
	list := func(chs ...Chapter) *Media { return &Media{Tags: tag.NewTagSet(), Chapters: chs} }
	a, b, c := Chapter{Start: 1500 * time.Microsecond, Title: "A"}, Chapter{Start: 2 * time.Second, Title: "B"}, Chapter{Start: 3 * time.Second, Title: "C"}
	src := list(a, b, c)
	p := ProjectTransferOnto(src, nil, writableCaps(FormatFLAC))
	carried := func(n int) TransferItem { return TransferItem{Kind: TransferChapter, Count: n, Disposition: Carried} }
	if !slices.Equal(p.Items, []TransferItem{carried(3)}) {
		t.Fatalf("projected %+v", p.Items)
	}
	with := func(ch Chapter, change func(*Chapter)) Chapter { change(&ch); return ch }
	for _, tc := range []struct {
		name   string
		result *Media
		want   []TransferItem
	}{
		{"as given", list(a, b, c), []TransferItem{carried(3)}},
		{"a start moved", list(with(a, func(ch *Chapter) { ch.Start = time.Millisecond }), b, c),
			[]TransferItem{carried(2), {Kind: TransferChapter, Count: 1, Disposition: Lossy, Reason: altered}}},
		{"a title cut", list(a, b, with(c, func(ch *Chapter) { ch.Title = "Cut" })),
			[]TransferItem{carried(2), {Kind: TransferChapter, Count: 1, Disposition: Lossy, Reason: altered}}},
		// One chapter left out costs the ones after it nothing.
		{"the first left out", list(b, c), []TransferItem{carried(2), {Kind: TransferChapter, Count: 1, Disposition: Dropped, Reason: absent}}},
		{"the middle left out", list(a, c), []TransferItem{carried(2), {Kind: TransferChapter, Count: 1, Disposition: Dropped, Reason: absent}}},
		{"none stored", list(), []TransferItem{{Kind: TransferChapter, Count: 3, Disposition: Dropped, Reason: absent}}},
		// An end the list rebuilds, here the next start, is no change. Another one is.
		{"an end filled to the next start", list(with(a, func(ch *Chapter) { ch.End = b.Start }), b, c), []TransferItem{carried(3)}},
		{"an end short of the next start", list(with(a, func(ch *Chapter) { ch.End = time.Second }), b, c),
			[]TransferItem{carried(2), {Kind: TransferChapter, Count: 1, Disposition: Lossy, Reason: altered}}},
		{"a language added", list(a, with(b, func(ch *Chapter) { ch.Language = "eng" }), c),
			[]TransferItem{carried(2), {Kind: TransferChapter, Count: 1, Disposition: Lossy, Reason: altered}}},
		{"a start and a title changed, and one left out", list(a, with(b, func(ch *Chapter) { ch.Start, ch.Title = time.Second, "X" })),
			[]TransferItem{carried(1), {Kind: TransferChapter, Count: 1, Disposition: Lossy, Reason: altered}, {Kind: TransferChapter, Count: 1, Disposition: Dropped, Reason: absent}}},
	} {
		if got := ReconcileTransfer(p, src, list(), tc.result); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
	// One stored chapter is one source chapter's, however many the source holds alike.
	twins := list(b, b)
	want := []TransferItem{carried(1), {Kind: TransferChapter, Count: 1, Disposition: Dropped, Reason: absent}}
	if got := ReconcileTransfer(ProjectTransferOnto(twins, nil, writableCaps(FormatFLAC)), twins, list(), list(b)); !slices.Equal(got, want) {
		t.Errorf("two chapters alike, one stored:\n got %+v\nwant %+v", got, want)
	}
	// What the grade already says is left as graded: the chapter graded lossy is the altered one.
	graded := TransferProjection{
		Items:    []TransferItem{carried(2), {Kind: TransferChapter, Count: 1, Disposition: Lossy, Reason: "graded"}},
		chapters: []Disposition{Lossy, Carried, Carried},
	}
	moved := list(with(a, func(ch *Chapter) { ch.Start = time.Millisecond }), b, c)
	if got := ReconcileTransfer(graded, src, list(), moved); !slices.Equal(got, graded.Items) {
		t.Errorf("a grade the result bears out changed: %+v", got)
	}
	dropped := TransferProjection{
		Items:    []TransferItem{{Kind: TransferChapter, Count: 3, Disposition: Dropped, Reason: "graded"}},
		chapters: []Disposition{Dropped, Dropped, Dropped},
	}
	if got := ReconcileTransfer(dropped, src, list(), list()); !slices.Equal(got, dropped.Items) {
		t.Errorf("a dropped list changed: %+v", got)
	}
	// A final end at or past the end of the destination's audio is one a read fills in.
	timed := func(d time.Duration, chs ...Chapter) *Media {
		m := list(chs...)
		m.Properties.Tracks = []AudioTrack{{Duration: d}}
		return m
	}
	filled := timed(10*time.Second, a, b, with(c, func(ch *Chapter) { ch.End = 10 * time.Second }))
	if got := ReconcileTransfer(p, src, timed(10*time.Second), filled); !slices.Equal(got, []TransferItem{carried(3)}) {
		t.Errorf("a final end filled to the end of the audio: %+v, want all carried", got)
	}
	// An end stored as given is carried, though it lies past the end of the destination's
	// audio and short of the source's, where only one of the two files would rebuild it.
	ended := timed(time.Minute, a, b, with(c, func(ch *Chapter) { ch.End = 20 * time.Second }))
	if got := ReconcileTransfer(ProjectTransferOnto(ended, nil, writableCaps(FormatFLAC)), ended, timed(10*time.Second), ended); !slices.Equal(got, []TransferItem{carried(3)}) {
		t.Errorf("a final end stored as given: %+v, want all carried", got)
	}
}

// Each synced lyrics set is held to its own fate: the one stored with its lines, carried where
// its language and description read back too.
func TestReconcileTransferSyncedLyrics(t *testing.T) {
	const altered = "stored with other lines, language or description than the source gives"
	set := func(language string, texts ...string) SyncedLyrics {
		sl := SyncedLyrics{Language: language}
		for i, s := range texts {
			sl.Lines = append(sl.Lines, SyncedLine{Time: time.Duration(i) * time.Second, Text: s})
		}
		return sl
	}
	sets := func(sls ...SyncedLyrics) *Media { return &Media{Tags: tag.NewTagSet(), SyncedLyrics: sls} }
	item := func(n int, d Disposition, reason string) TransferItem {
		return TransferItem{Kind: TransferSyncedLyric, Count: n, Disposition: d, Reason: reason}
	}
	src := sets(set("", "a", "b"), set("", "c"))
	p := ProjectTransferOnto(src, nil, writableCaps(FormatMP3))
	for _, tc := range []struct {
		name   string
		result *Media
		want   []TransferItem
	}{
		{"as given", sets(set("", "a", "b"), set("", "c")), []TransferItem{item(2, Carried, "")}},
		{"lines cut", sets(set("", "a"), set("", "c")), []TransferItem{item(1, Carried, ""), item(1, Lossy, altered)}},
		{"a language changed", sets(set("", "a", "b"), set("eng", "c")), []TransferItem{item(1, Carried, ""), item(1, Lossy, altered)}},
		{"the first left out", sets(set("", "c")), []TransferItem{item(1, Carried, ""), item(1, Dropped, "the destination does not store this synced lyrics set")}},
	} {
		if got := ReconcileTransfer(p, src, sets(), tc.result); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
	// A set the projection leaves out takes no place in the result: the set after it is compared
	// with the one stored, not with nothing.
	src = sets(set("", "bad\x00"), set("", "good"))
	p = ProjectTransferOnto(src, nil, writableCaps(FormatMP3))
	want := []TransferItem{item(1, Carried, ""), item(1, Dropped, "a synced-lyrics line contains a NUL byte")}
	if !slices.Equal(p.Items, want) || len(p.SyncedLyrics) != 1 {
		t.Fatalf("projected %+v, writing %d sets", p.Items, len(p.SyncedLyrics))
	}
	if got := ReconcileTransfer(p, src, sets(), sets(set("", "good"))); !slices.Equal(got, want) {
		t.Errorf("an unwritable set ahead of a stored one:\n got %+v\nwant %+v", got, want)
	}
	// A set stored in another form is found by its lines: with the set ahead of it left out, it
	// keeps the grade it was given and that one is dropped.
	graded := TransferProjection{
		Items:  []TransferItem{item(1, Carried, ""), item(1, Lossy, "graded")},
		lyrics: []Disposition{Carried, Lossy},
	}
	src = sets(set("", "first"), set("english", "second"))
	want = []TransferItem{item(1, Lossy, "graded"), item(1, Dropped, "the destination does not store this synced lyrics set")}
	if got := ReconcileTransfer(graded, src, sets(), sets(set("eng", "second"))); !slices.Equal(got, want) {
		t.Errorf("a set in another form after one left out:\n got %+v\nwant %+v", got, want)
	}
}

// A format that stores pictures or synced lyrics only in part grades each one lossy, and the
// reconcile keeps that grade for those the result holds.
func TestReconcileTransferKeepsAPartialStoresGrade(t *testing.T) {
	partial := Capability{Read: AccessFull, Write: AccessPartial, Fidelity: "stored in part"}
	caps := writableCaps(FormatFLAC).WithSyncedLyrics(partial)
	caps.Pictures = partial
	line := func(text string) SyncedLyrics { return SyncedLyrics{Lines: []SyncedLine{{Text: text}}} }
	src := &Media{
		Tags:         tag.NewTagSet(),
		Pictures:     []Picture{{Type: PicFrontCover, MIME: "image/png", Data: testfile.PNG()}, {Type: PicBackCover, MIME: "image/jpeg", Data: testfile.JPEG()}},
		SyncedLyrics: []SyncedLyrics{line("a"), line("b")},
	}
	blank := &Media{Tags: tag.NewTagSet()}
	p := ProjectTransferOnto(src, nil, caps)
	want := []TransferItem{
		{Kind: TransferPicture, Count: 2, Disposition: Lossy, Reason: "stored in part"},
		{Kind: TransferSyncedLyric, Count: 2, Disposition: Lossy, Reason: "stored in part"},
	}
	if !slices.Equal(p.Items, want) {
		t.Fatalf("projected %+v", p.Items)
	}
	if got := ReconcileTransfer(p, src, blank, src); !slices.Equal(got, want) {
		t.Errorf("all stored: %+v, want the grades as given", got)
	}
	one := &Media{Tags: tag.NewTagSet(), Pictures: src.Pictures[1:], SyncedLyrics: src.SyncedLyrics[:1]}
	want = []TransferItem{
		{Kind: TransferPicture, Count: 1, Disposition: Lossy, Reason: "stored in part"},
		{Kind: TransferPicture, Count: 1, Disposition: Dropped, Reason: "the destination does not store this picture"},
		{Kind: TransferSyncedLyric, Count: 1, Disposition: Lossy, Reason: "stored in part"},
		{Kind: TransferSyncedLyric, Count: 1, Disposition: Dropped, Reason: "the destination does not store this synced lyrics set"},
	}
	if got := ReconcileTransfer(p, src, blank, one); !slices.Equal(got, want) {
		t.Errorf("one of each stored:\n got %+v\nwant %+v", got, want)
	}
}

// A set left out for its text does not count against the sets a format holds, and a language
// the store keeps in another form is lossy.
func TestProjectSyncedLyricsGradesEachSet(t *testing.T) {
	set := func(language, text string) SyncedLyrics {
		return SyncedLyrics{Language: language, Lines: []SyncedLine{{Time: time.Second, Text: text}}}
	}
	item := func(n int, d Disposition, reason string) TransferItem {
		return TransferItem{Kind: TransferSyncedLyric, Count: n, Disposition: d, Reason: reason}
	}
	const nul = "a synced-lyrics line contains a NUL byte"
	one := writableCaps(FormatFLAC).WithSyncedLyrics(Capability{Read: AccessFull, Write: AccessFull, MaxItems: 1})
	lower := writableCaps(FormatMP3).WithSyncedLyrics(WithStoredLanguage(Capability{Read: AccessFull, Write: AccessFull, SyncedLyricsTimeMax: time.Minute}, strings.ToLower))
	late := SyncedLyrics{Lines: []SyncedLine{{Time: time.Hour, Text: "late"}}}
	textOnly := writableCaps(FormatFLAC).WithSyncedLyrics(WithStoredLanguage(
		Capability{Read: AccessFull, Write: AccessFull, Fidelity: "timed text only", SyncedLyricsLoss: SyncedLyricsLossLanguage}, strings.ToLower))
	for _, tc := range []struct {
		name    string
		caps    Capabilities
		sets    []SyncedLyrics
		want    []TransferItem
		written int
	}{
		{"an unwritable set beside one, under a limit of one", one, []SyncedLyrics{set("", "bad\x00"), set("", "good")},
			[]TransferItem{item(1, Carried, ""), item(1, Dropped, nul)}, 1},
		{"two good sets under a limit of one", one, []SyncedLyrics{set("", "a"), set("", "b")},
			[]TransferItem{item(2, Dropped, "exceeds the destination limit of 1")}, 0},
		{"two good sets and an unwritable one under a limit of one", one, []SyncedLyrics{set("", "a"), set("", "bad\x00"), set("", "b")},
			[]TransferItem{item(2, Dropped, "exceeds the destination limit of 1"), item(1, Dropped, nul)}, 0},
		{"a language stored in another form", lower, []SyncedLyrics{set("ENG", "a"), set("deu", "b")},
			[]TransferItem{item(1, Carried, ""), item(1, Lossy, `language "ENG" reads back as "eng"`)}, 2},
		{"a clamped time beside a language stored in another form", lower, []SyncedLyrics{late, set("ENG", "a")},
			[]TransferItem{item(2, Lossy, `language "ENG" reads back as "eng"`)}, 2},
		{"a language a store drops whole", textOnly, []SyncedLyrics{set("ENG", "a")},
			[]TransferItem{item(1, Lossy, "timed text only")}, 1},
		{"a clamped time alone", lower, []SyncedLyrics{late},
			[]TransferItem{item(1, Lossy, "a synced-lyric timestamp is too large and was clamped")}, 1},
		// A destination that takes no set gives its own reason for every one.
		{"a format with no store", writableCaps(FormatMP4).WithSyncedLyrics(Capability{}), []SyncedLyrics{set("", "bad\x00"), set("", "good")},
			[]TransferItem{item(2, Dropped, "destination format does not store synced lyrics")}, 0},
	} {
		p := ProjectTransferOnto(&Media{Tags: tag.NewTagSet(), SyncedLyrics: tc.sets}, nil, tc.caps)
		if !slices.Equal(p.Items, tc.want) || len(p.SyncedLyrics) != tc.written || p.SetSyncedLyrics != (tc.written > 0) {
			t.Errorf("%s: writing %d sets (set %v)\n got %+v\nwant %+v", tc.name, len(p.SyncedLyrics), p.SetSyncedLyrics, p.Items, tc.want)
		}
	}
}

// A size declared for a picture whose bytes give none (a link, an image type nothing here reads)
// lives only where the format has a field for it.
func TestProjectTransferGradesDeclaredGeometry(t *testing.T) {
	link := Picture{Type: PicFrontCover, MIME: LinkMIME, Width: 1, Height: 1, Depth: 24, Data: []byte("http://example.com/c.jpg")}
	sized := Picture{Type: PicFrontCover, MIME: "image/png", Data: testfile.PNG()}
	sized.SniffAuthoritative()
	bare := Picture{Type: PicBackCover, MIME: LinkMIME, Data: []byte("http://example.com/d.jpg")}
	src := &Media{Tags: tag.NewTagSet(), Pictures: []Picture{link, sized, bare}}
	pics := Capability{Read: AccessFull, Write: AccessFull}

	got := ProjectTransferOnto(src, nil, NewCapabilities(FormatMP3, false, Capability{}, pics, Capability{}, AccessNone, nil)).Items
	want := []TransferItem{
		{Kind: TransferPicture, Count: 2, Disposition: Carried},
		{Kind: TransferPicture, Count: 1, Disposition: Lossy, Reason: "the size declared for a picture whose bytes give none is not stored"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("no geometry field: got %+v\nwant %+v", got, want)
	}

	pics.PictureGeometry = true
	got = ProjectTransferOnto(src, nil, NewCapabilities(FormatFLAC, false, Capability{}, pics, Capability{}, AccessNone, nil)).Items
	if want := []TransferItem{{Kind: TransferPicture, Count: 3, Disposition: Carried}}; !slices.Equal(got, want) {
		t.Errorf("geometry field: got %+v\nwant %+v", got, want)
	}

	if stored := WithoutDeclaredGeometry([]Picture{link, sized, bare}); stored[0].Width != 0 || stored[0].Height != 0 || stored[0].Depth != 0 ||
		stored[1].Width != sized.Width || !slices.Equal(stored[2].Data, bare.Data) || link.Width != 1 {
		t.Errorf("WithoutDeclaredGeometry = %+v", stored)
	}
	same := []Picture{sized, bare}
	if stored := WithoutDeclaredGeometry(same); &stored[0] != &same[0] {
		t.Error("a list with no declared size is returned as given")
	}

	// A block that states other than its bytes give (a type spelled another way, a wrong size, no
	// size) is read from its bytes by every format, so nothing of it is lost anywhere.
	loose := []Picture{
		{Type: PicFrontCover, MIME: "image/x-png", Width: 600, Height: 600, Depth: 8, Data: testfile.PNG()},
		{Type: PicBackCover, MIME: "", Data: testfile.JPEG()},
	}
	for _, geometry := range []bool{false, true} {
		pics.PictureGeometry = geometry
		got = ProjectTransferOnto(&Media{Tags: tag.NewTagSet(), Pictures: loose}, nil, NewCapabilities(FormatMP3, false, Capability{}, pics, Capability{}, AccessNone, nil)).Items
		if want := []TransferItem{{Kind: TransferPicture, Count: 2, Disposition: Carried}}; !slices.Equal(got, want) {
			t.Errorf("loosely declared pictures (size fields %v): got %+v", geometry, got)
		}
	}
	if loose[0].Width != 600 || loose[1].MIME != "" {
		t.Error("the source's pictures were changed")
	}

	// A type the bytes name without a size (AVIF) is in the same place: the size is the file's word.
	avif := Picture{Type: PicFrontCover, MIME: "image/avif", Width: 1920, Height: 1080, Depth: 24,
		Data: []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf")}
	if got := avif.EffectiveMIME(); got != "image/avif" {
		t.Fatalf("setup: the AVIF bytes sniff as %q", got)
	}
	pics.PictureGeometry = false
	got = ProjectTransferOnto(&Media{Tags: tag.NewTagSet(), Pictures: []Picture{avif}}, nil, NewCapabilities(FormatMP3, false, Capability{}, pics, Capability{}, AccessNone, nil)).Items
	if want := []TransferItem{{Kind: TransferPicture, Count: 1, Disposition: Lossy, Reason: "the size declared for a picture whose bytes give none is not stored"}}; !slices.Equal(got, want) {
		t.Errorf("an AVIF with a declared size: got %+v", got)
	}
	if stored := WithoutDeclaredGeometry([]Picture{avif}); stored[0].Width != 0 || stored[0].Depth != 0 || stored[0].MIME != "image/avif" {
		t.Errorf("WithoutDeclaredGeometry(avif) = %+v", stored[0])
	}
}

func writableCaps(f Format) Capabilities {
	full := Capability{Read: AccessFull, Write: AccessFull}
	return NewCapabilities(f, false, full, full, full, AccessNone, nil).WithSyncedLyrics(full)
}

// Text no format stores as given (a NUL, invalid UTF-8) is graded before the write: a field or a
// chapter list holding it is dropped, a lyric set too, and a picture is carried without the
// description.
func TestProjectTransferDropsUnwritableText(t *testing.T) {
	line := func(s string) SyncedLyrics { return SyncedLyrics{Lines: []SyncedLine{{Text: s}}} }
	src := &Media{
		Tags:         tagsOf("TITLE", "abc\x00", "ARTIST", "A", "ALBUM", "b\xffd"),
		Pictures:     []Picture{{Type: PicFrontCover, MIME: "image/png", Description: "a\x00b", Data: testfile.PNG()}},
		Chapters:     []Chapter{{Title: "One"}, {Start: time.Second, Title: "Two\x00"}},
		SyncedLyrics: []SyncedLyrics{line("fine"), line("bad\x00")},
	}
	src.Pictures[0].SniffAuthoritative()
	p := ProjectTransferOnto(src, nil, writableCaps(FormatFLAC))
	want := []TransferItem{
		{Kind: TransferField, Key: tag.Title, Count: 1, Disposition: Dropped, Reason: "a value contains a NUL byte"},
		{Kind: TransferField, Key: tag.Artist, Count: 1, Disposition: Carried},
		{Kind: TransferField, Key: tag.Album, Count: 1, Disposition: Dropped, Reason: "a value contains invalid UTF-8"},
		{Kind: TransferPicture, Count: 1, Disposition: Lossy, Reason: "a picture description contains a NUL byte and is not stored"},
		{Kind: TransferChapter, Count: 2, Disposition: Dropped, Reason: "a chapter title contains a NUL byte"},
		{Kind: TransferSyncedLyric, Count: 1, Disposition: Carried},
		{Kind: TransferSyncedLyric, Count: 1, Disposition: Dropped, Reason: "a synced-lyrics line contains a NUL byte"},
	}
	if !slices.Equal(p.Items, want) {
		t.Errorf("items:\n got %+v\nwant %+v", p.Items, want)
	}
	if !slices.Equal(p.Keys, []tag.Key{tag.Artist}) {
		t.Errorf("Keys = %v, want ARTIST alone", p.Keys)
	}
	if !p.SetPictures || len(p.Pictures) != 1 || p.Pictures[0].Description != "" || src.Pictures[0].Description == "" {
		t.Errorf("pictures written = %+v (set %v), want the picture without its description and the source's untouched", p.Pictures, p.SetPictures)
	}
	if p.SetChapters {
		t.Error("a dropped chapter list is not written")
	}
	if !p.SetSyncedLyrics || len(p.SyncedLyrics) != 1 || p.SyncedLyrics[0].Lines[0].Text != "fine" {
		t.Errorf("synced lyrics written = %+v (set %v), want the writable set", p.SyncedLyrics, p.SetSyncedLyrics)
	}

	// With no set left to write, the destination's lyrics are left alone, not replaced by none.
	src.SyncedLyrics = []SyncedLyrics{line("\x00leading"), line("bad\x00")}
	if p := ProjectTransferOnto(src, nil, writableCaps(FormatFLAC)); p.SetSyncedLyrics || len(p.SyncedLyrics) != 0 {
		t.Errorf("every set unwritable: set %v, %d resolved, want nothing written", p.SetSyncedLyrics, len(p.SyncedLyrics))
	}
	if got := WritableTextReason("\x00leading"); got != "contains a NUL byte" {
		t.Errorf("a NUL in the first byte: %q", got)
	}
}

// A chapter time finer than the destination's unit is cut short by the writer, so it is lossy.
func TestProjectTransferGradesChapterTimeUnit(t *testing.T) {
	src := &Media{Tags: tag.NewTagSet(), Chapters: []Chapter{
		{Start: 0, Title: "A"},
		{Start: 181400 * time.Microsecond, Title: "B"},
		{Start: 2 * time.Second, End: 2500*time.Millisecond + 50, Title: "C"},
	}}
	caps := writableCaps(FormatMP3)
	caps.Chapters.ChapterTimeUnit = time.Millisecond
	got := ProjectTransferOnto(src, nil, caps).Items
	want := []TransferItem{
		{Kind: TransferChapter, Count: 1, Disposition: Carried},
		{Kind: TransferChapter, Count: 2, Disposition: Lossy, Reason: "a chapter time finer than the 1ms the format counts in is not stored exactly"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	// Lost metadata is the reason given where a chapter loses that too.
	lang := *src
	lang.Chapters = slices.Clone(src.Chapters)
	lang.Chapters[1].Language = "eng"
	caps.Chapters.ChapterLoss, caps.Chapters.Fidelity = ChapterLossLangFlags, "no language"
	if got := ProjectTransferOnto(&lang, nil, caps).Items; len(got) != 2 || got[1].Count != 2 || got[1].Reason != "no language" {
		t.Errorf("a time cut beside lost metadata: got %+v, want the metadata reason", got)
	}
	caps.Chapters.ChapterTimeMax = time.Second
	if got := ProjectTransferOnto(&lang, nil, caps).Items; len(got) != 2 || got[1].Reason != "no language" {
		t.Errorf("a clamped time beside lost metadata: got %+v, want the metadata reason", got)
	}
	caps.Chapters.ChapterLoss, caps.Chapters.Fidelity, caps.Chapters.ChapterTimeMax = ChapterLossNone, "", 0

	// A store that keeps chapters in part is still written.
	partial := caps
	partial.Chapters.Write = AccessPartial
	if p := ProjectTransferOnto(src, nil, partial); !p.SetChapters || len(p.Items) != 1 || p.Items[0].Disposition != Lossy {
		t.Errorf("a partial chapter store: set %v, items %+v", p.SetChapters, p.Items)
	}

	// A time past the largest the format's field holds is clamped, which is the reason given
	// ahead of a cut one.
	caps.Chapters.ChapterTimeMax = 2 * time.Second
	want[1].Reason = "a chapter time is past the largest the format stores and is clamped"
	if got := ProjectTransferOnto(src, nil, caps).Items; !slices.Equal(got, want) {
		t.Errorf("a time past the format's largest: got %+v\nwant %+v", got, want)
	}
	caps.Chapters.ChapterTimeMax = 3 * time.Second
	want[1].Reason = "a chapter time finer than the 1ms the format counts in is not stored exactly"
	if got := ProjectTransferOnto(src, nil, caps).Items; !slices.Equal(got, want) {
		t.Errorf("no time past the format's largest: got %+v\nwant %+v", got, want)
	}
	caps.Chapters.ChapterTimeMax = 0

	caps.Chapters.ChapterTimeUnit = time.Nanosecond
	if got := ProjectTransferOnto(src, nil, caps).Items; !slices.Equal(got, []TransferItem{{Kind: TransferChapter, Count: 3, Disposition: Carried}}) {
		t.Errorf("nanosecond unit: got %+v", got)
	}
	caps.Chapters.ChapterTimeUnit = 0
	if got := ProjectTransferOnto(src, nil, caps).Items; !slices.Equal(got, []TransferItem{{Kind: TransferChapter, Count: 3, Disposition: Carried}}) {
		t.Errorf("no unit: got %+v", got)
	}
}

// What the destination already holds as the source gives it is carried and not written again,
// whatever the format can store: a copy onto the same metadata changes nothing.
func TestProjectTransferOntoHeldItems(t *testing.T) {
	sl := SyncedLyrics{Lines: []SyncedLine{{Text: "x"}}}
	pic := Picture{Type: PicFrontCover, MIME: "image/png", Data: testfile.PNG()}
	src := &Media{
		Tags:         tagsOf("TITLE", "T\x00", "ARTIST", "A", "ALBUM", "Al", "TRACKNUMBER", " 3 "),
		Comments:     []TextItem{{Text: "c", Description: "d"}},
		Pictures:     []Picture{pic},
		Chapters:     []Chapter{{Start: 1500 * time.Microsecond, Title: "One"}},
		SyncedLyrics: []SyncedLyrics{sl},
	}
	src.Tags.Set(tag.Comment, "c")
	dst := &Media{
		Tags:         tagsOf("TITLE", "T\x00", "ARTIST", "A", "ALBUM", "Other", "TRACKNUMBER", "3", "COMMENT", "c"),
		Comments:     []TextItem{{Text: "c", Description: "d"}},
		Pictures:     []Picture{pic},
		Chapters:     []Chapter{{Start: 1500 * time.Microsecond, Title: "One"}},
		SyncedLyrics: []SyncedLyrics{sl},
	}
	readOnly := NewCapabilities(FormatWMA, true, Capability{}, Capability{}, Capability{}, AccessNone, nil)
	p := ProjectTransferOnto(src, dst, readOnly)
	want := []TransferItem{
		{Kind: TransferField, Key: tag.Title, Count: 1, Disposition: Carried},
		{Kind: TransferField, Key: tag.Artist, Count: 1, Disposition: Carried},
		{Kind: TransferField, Key: tag.Album, Count: 1, Disposition: Dropped, Reason: "destination is read-only"},
		{Kind: TransferField, Key: tag.TrackNumber, Count: 1, Disposition: Carried},
		{Kind: TransferField, Key: tag.Comment, Count: 1, Disposition: Carried},
		{Kind: TransferPicture, Count: 1, Disposition: Carried},
		{Kind: TransferChapter, Count: 1, Disposition: Carried},
		{Kind: TransferSyncedLyric, Count: 1, Disposition: Carried},
	}
	if !slices.Equal(p.Items, want) {
		t.Errorf("items:\n got %+v\nwant %+v", p.Items, want)
	}
	if len(p.Keys) != 0 || p.SetPictures || p.SetChapters || p.SetSyncedLyrics {
		t.Errorf("held items are written again: %+v", p)
	}

	// A comment the destination holds under another description is not held as given.
	dst.Comments = []TextItem{{Text: "c"}}
	caps := writableCaps(FormatMP3)
	if p := ProjectTransferOnto(src, dst, caps); !slices.Contains(p.Keys, tag.Comment) {
		t.Errorf("Keys = %v, want COMMENT written for its description", p.Keys)
	}
	// Nor a lyric held in another language.
	src.Tags.Set(tag.Lyrics, "la")
	dst.Tags.Set(tag.Lyrics, "la")
	src.Lyrics, dst.Lyrics = []TextItem{{Text: "la", Language: "deu"}}, []TextItem{{Text: "la", Language: "deu"}}
	if p := ProjectTransferOnto(src, dst, caps); slices.Contains(p.Keys, tag.Lyrics) {
		t.Errorf("Keys = %v, want LYRICS left alone: the destination holds the lyric as given", p.Keys)
	}
	dst.Lyrics = []TextItem{{Text: "la", Language: "eng"}}
	if p := ProjectTransferOnto(src, dst, caps); !slices.Contains(p.Keys, tag.Lyrics) {
		t.Errorf("Keys = %v, want LYRICS written for its language", p.Keys)
	}
	// Pictures the destination holds beside another are replaced, so the set is written.
	dst.Pictures = []Picture{pic, {Type: PicBackCover, MIME: "image/png", Data: testfile.PNG()}}
	if p := ProjectTransferOnto(src, dst, caps); !p.SetPictures || len(p.Pictures) != 1 {
		t.Errorf("pictures: set %v, %d given, want the source's one written", p.SetPictures, len(p.Pictures))
	}
	// Pictures are held as a read settles them: a block declaring its type another way is the same picture.
	src.Pictures = []Picture{{Type: PicFrontCover, MIME: "image/x-png", Width: 3, Data: testfile.PNG()}}
	dst.Pictures = []Picture{{Type: PicFrontCover, MIME: "", Data: testfile.PNG()}}
	if p := ProjectTransferOnto(src, dst, caps); p.SetPictures || len(p.Items) == 0 || p.Items[len(p.Items)-3].Disposition != Carried {
		t.Errorf("a picture held under another declaration: set %v, items %+v", p.SetPictures, p.Items)
	}
	src.Pictures = []Picture{pic}
	// A destination holding just the pictures that can be stored has nothing to take: the GIF is
	// dropped and the set left as it is.
	gifOnly := NewCapabilities(FormatMP4, false, Capability{}, Capability{Read: AccessFull, Write: AccessFull, PictureMIMEs: []string{"image/png"}}, Capability{}, AccessNone, nil)
	src.Pictures = []Picture{pic, {Type: PicBackCover, MIME: "image/gif", Data: testfile.GIF()}}
	dst.Pictures = []Picture{pic}
	if p := ProjectTransferOnto(src, dst, gifOnly); p.SetPictures || len(p.Pictures) != 1 {
		t.Errorf("pictures the destination holds: set %v, %d resolved, want the one picture and no write", p.SetPictures, len(p.Pictures))
	}
}

// A picture or a value past what the destination's store holds is dropped before the write, and
// the rest of the transfer goes on.
func TestProjectTransferDropsWhatIsTooLarge(t *testing.T) {
	big := Picture{Type: PicFrontCover, MIME: "image/png", Data: append(testfile.PNG(), make([]byte, 200)...)}
	small := Picture{Type: PicBackCover, MIME: "image/png", Data: testfile.PNG()}
	src := &Media{
		Tags:     tagsOf("TITLE", "T", "COMMENT", strings.Repeat("x", 300), "ALBUM", "a", "ALBUM", strings.Repeat("y", 101)),
		Pictures: []Picture{big, small},
	}
	caps := writableCaps(FormatFLAC).WithValueSizeLimit(100, "a test block holds")
	caps.Pictures = WithPictureSizeLimit(caps.Pictures, func(p Picture) string {
		return SizeLimitReason("the picture", int64(len(p.Data)), 100, "a test block holds")
	})
	p := ProjectTransferOnto(src, nil, caps)
	want := []TransferItem{
		{Kind: TransferField, Key: tag.Title, Count: 1, Disposition: Carried},
		{Kind: TransferField, Key: tag.Comment, Count: 1, Disposition: Dropped, Reason: "a value is 300 B, over the 100 B a test block holds"},
		{Kind: TransferField, Key: tag.Album, Count: 2, Disposition: Dropped, Reason: "a value is 101 B, over the 100 B a test block holds"},
		{Kind: TransferPicture, Count: 1, Disposition: Carried},
		{Kind: TransferPicture, Count: 1, Disposition: Dropped, Reason: fmt.Sprintf("the picture is %d B, over the 100 B a test block holds", len(big.Data))},
	}
	if !slices.Equal(p.Items, want) {
		t.Errorf("items:\n got %+v\nwant %+v", p.Items, want)
	}
	if !slices.Equal(p.Keys, []tag.Key{tag.Title}) || len(p.Pictures) != 1 || p.Pictures[0].Type != PicBackCover {
		t.Errorf("written: keys %v, %d pictures", p.Keys, len(p.Pictures))
	}
	// Of several pictures past the limit, the reason given is the first one's.
	bigger := Picture{Type: PicArtist, MIME: "image/png", Data: append(testfile.PNG(), make([]byte, 400)...)}
	if kept, reason := PartitionPictureSize(caps.Pictures, []Picture{small, big, bigger}); !slices.Equal(kept, []int{0}) ||
		reason != fmt.Sprintf("the picture is %d B, over the 100 B a test block holds", len(big.Data)) {
		t.Errorf("two pictures past the limit: kept %v, reason %q", kept, reason)
	}
	// A value at the limit fits, and a format with no limit takes any.
	if got := SizeLimitReason("a value", 100, 100, "a test block holds"); got != "" {
		t.Errorf("a value at the limit: %q", got)
	}
	if p := ProjectTransferOnto(src, nil, writableCaps(FormatFLAC)); len(p.Keys) != 3 || len(p.Pictures) != 2 {
		t.Errorf("no limit: keys %v, %d pictures", p.Keys, len(p.Pictures))
	}
	// The same size in both units prints the byte counts.
	if got := SizeLimitReason("a value", 16<<20+1, 16<<20, "a block holds"); got != "a value is 16.0 MiB (16777217 bytes), over the 16.0 MiB (16777216 bytes) a block holds" {
		t.Errorf("close sizes: %q", got)
	}
}

// A dropped link picture is named for what it is, not by the marker its MIME field holds.
func TestUnrepresentableReasonNamesLinkPictures(t *testing.T) {
	for _, c := range []struct {
		mimes []string
		want  string
	}{
		{[]string{LinkMIME}, "MP4 cannot store a linked (URL) picture"},
		{[]string{"image/gif", LinkMIME, "image/gif", LinkMIME}, "MP4 cannot store image/gif, a linked (URL) picture"},
		{[]string{"image/gif"}, "MP4 cannot store image/gif"},
	} {
		if got := UnrepresentableReason(FormatMP4, c.mimes); got != c.want {
			t.Errorf("UnrepresentableReason(%q) = %q, want %q", c.mimes, got, c.want)
		}
	}
}

// A comment or lyric is carried only with the description and language the source gives: one cut
// to a format's field, or left behind, is lossy though the text is there. A language the source
// does not state is the destination's to fill.
func TestReconcileTransferComparesQualifiers(t *testing.T) {
	media := func(items ...TextItem) *Media {
		m := &Media{Format: FormatMatroska, Tags: tag.NewTagSet(), Comments: items}
		m.Tags.Set(tag.Comment, TextsOf(items)...)
		return m
	}
	carried := []TransferItem{{Kind: TransferField, Key: tag.Comment, Count: 1}}
	for _, c := range []struct {
		name      string
		src, held TextItem
		want      Disposition
		reason    string
	}{
		{"as given", TextItem{Text: "c", Description: "d", Language: "deu"}, TextItem{Text: "c", Description: "d", Language: "deu"}, Carried, ""},
		{"language cut", TextItem{Text: "c", Language: "en-us"}, TextItem{Text: "c", Language: "en-"}, Lossy, `language "en-us" reads back as "en-"`},
		{"description left behind", TextItem{Text: "c", Description: "d"}, TextItem{Text: "c"}, Lossy, `description "d" reads back as ""`},
		{"language filled in", TextItem{Text: "c"}, TextItem{Text: "c", Language: "eng"}, Carried, ""},
		{"language in another case", TextItem{Text: "c", Language: "ENG"}, TextItem{Text: "c", Language: "eng"}, Carried, ""},
	} {
		got := ReconcileTransfer(TransferProjection{Items: carried}, media(c.src), media(), media(c.held))
		if len(got) != 1 || got[0].Disposition != c.want || got[0].Reason != c.reason {
			t.Errorf("%s: %+v, want %s %q", c.name, got, c.want, c.reason)
		}
	}
	// A lyric is held to its qualifiers as a comment is.
	lyric := func(it TextItem) *Media {
		m := &Media{Format: FormatMatroska, Tags: tagsOf("LYRICS", it.Text), Lyrics: []TextItem{it}}
		return m
	}
	got := ReconcileTransfer(TransferProjection{Items: []TransferItem{{Kind: TransferField, Key: tag.Lyrics, Count: 1}}},
		lyric(TextItem{Text: "la", Language: "en-us"}), media(), lyric(TextItem{Text: "la", Language: "en-"}))
	if len(got) != 1 || got[0].Disposition != Lossy || got[0].Reason != `language "en-us" reads back as "en-"` {
		t.Errorf("a lyric's cut language: %+v", got)
	}
	// A grade that already says the qualifier is lost keeps its own reason, and is not lifted.
	lossy := []TransferItem{{Kind: TransferField, Key: tag.Comment, Count: 1, Disposition: Lossy, Reason: "graded"}}
	if got := ReconcileTransfer(TransferProjection{Items: lossy}, media(TextItem{Text: "c", Description: "d"}), media(), media(TextItem{Text: "c"})); !slices.Equal(got, lossy) {
		t.Errorf("a graded qualifier loss: %+v", got)
	}
}

// Rendered grading grades each field by what the writer's own rendering reads back as, leaves out
// a field that reads back as nothing and renders the rest again. It renders what the transfer
// writes: no field the table drops, and the pictures, chapters and synced lyrics it brings.
func TestRenderedFields(t *testing.T) {
	var renders [][]tag.Key
	var brought [][3]int
	// The writer: keeps one TITLE, stores Y as Z, has no place for X or V, nor for T beside an N,
	// reads an X written beside a BPM back as a second BPM value, and a U as an ARTIST in capitals.
	render := func(written *Media, fields tag.TagSet) *Media {
		renders = append(renders, fields.Keys())
		brought = append(brought, [3]int{len(written.Pictures), len(written.Chapters), len(written.SyncedLyrics)})
		out := &Media{Format: FormatMP3, Tags: tag.NewTagSet()}
		for k, vals := range fields.All() {
			switch k {
			case "X", "V":
			case "T":
				if !fields.Has("N") {
					out.Tags.Set(k, vals...)
				}
			case "U":
				out.Tags.Add(tag.Artist, strings.ToUpper(vals[0]))
			case "Y":
				out.Tags.Set("Z", vals...)
			case "W":
				out.Tags.Add(tag.Artist, vals...)
			case "P", "Q":
				out.Tags.Add("R", vals...)
			case tag.Title:
				out.Tags.Set(k, vals[0])
			case tag.BPM:
				out.Tags.Set(k, vals...)
				if x, ok := fields.Get("X"); ok {
					out.Tags.Add(k, x...)
				}
			default:
				out.Tags.Set(k, vals...)
			}
		}
		return out
	}
	reason := func(k tag.Key, d Disposition, fields tag.TagSet) string {
		if vals, _ := fields.Get(k); k == "X" && d == Dropped && len(vals) == 1 {
			return "no place for X"
		}
		return ""
	}
	bad := SyncedLyrics{Lines: []SyncedLine{{Text: "bad\x00"}}}
	src := &Media{Format: FormatFLAC,
		Tags: tagsOf("TITLE", "A", "TITLE", "B", "ARTIST", "R", "X", "x", "Y", "y", "BPM", "120",
			"ENCODER", "e", "ALBUM", "a\x00b", "W", "w", "P", "p", "Q", "q", "V", "y", "N", "n", "T", "t", "U", "u", "COMMENT", strings.Repeat("c", 200), "GENRE", "g"),
		Pictures:     []Picture{{Type: PicFrontCover, MIME: "image/png", Data: testfile.PNG()}},
		Chapters:     []Chapter{{Title: "one"}, {Start: time.Second, Title: "two"}},
		SyncedLyrics: []SyncedLyrics{bad, {Lines: []SyncedLine{{Text: "good"}}}},
	}
	caps := writableCaps(FormatMP3).WithValueSizeLimit(100, "a test frame holds").
		WithRenderedFields(RenderedFields{Render: render, Reason: reason})
	caps.Pictures = Capability{Read: AccessFull}
	// The destination holds the GENRE: it is not written, and is in the rendering as the file's.
	held := &Media{Format: FormatMP3, Tags: tagsOf("GENRE", "g")}
	p := ProjectTransferOnto(src, held, caps)
	for _, c := range []struct {
		key    tag.Key
		want   Disposition
		reason string
	}{
		{tag.Title, Lossy, "only the first of 2 values is stored"},
		{tag.Artist, Carried, ""},
		{"X", Dropped, "no place for X"},
		{"Y", Lossy, "stored as Z"},
		{tag.BPM, Carried, ""},
		{"W", Dropped, "the format reads it back as ARTIST, which the source holds too"},
		// Two fields the writer stores under one key the source does not hold: neither reads back.
		{"P", Dropped, "the destination does not store this value"},
		{"Q", Dropped, "the destination does not store this value"},
		// The value Y is stored as, under a key with no place: where Y went is not where V did.
		{"V", Dropped, "the destination does not store this value"},
		// Stored by itself and not beside N: its own key is no other place it went.
		{"T", Dropped, "the destination does not store this value"},
		{"N", Carried, ""},
		// Another key holding another value is not where the field went.
		{"U", Dropped, "the destination does not store this value"},
		{tag.Genre, Carried, ""},
	} {
		at := slices.IndexFunc(p.Items, func(it TransferItem) bool { return it.Key == c.key })
		if it := p.Items[at]; it.Disposition != c.want || it.Reason != c.reason {
			t.Errorf("%s: %s %q, want %s %q", c.key, it.Disposition, it.Reason, c.want, c.reason)
		}
	}
	if want := []tag.Key{tag.Title, tag.Artist, "Y", tag.BPM, "N"}; !slices.Equal(p.Keys, want) {
		t.Errorf("written keys %v, want %v", p.Keys, want)
	}
	// Rendered whole, then each of the eight fields that read back under no key of their own by
	// itself, then without the seven dropped. A field the table drops was never in it: own audio,
	// text no format stores, a value past the limit.
	first, last := renders[0], renders[len(renders)-1]
	if len(renders) != 10 || !slices.Contains(first, "X") || !slices.Contains(first, tag.Genre) || slices.Contains(last, "X") || slices.Contains(last, "W") {
		t.Errorf("renderings: %v", renders)
	}
	for _, k := range []tag.Key{tag.Encoder, tag.Album, tag.Comment} {
		if slices.ContainsFunc(renders, func(keys []tag.Key) bool { return slices.Contains(keys, k) }) {
			t.Errorf("%s, which the table drops, was rendered: %v", k, renders)
		}
	}
	// Beside the fields went what the transfer writes of the rest: no picture the destination
	// cannot write, the chapters, and the one synced lyrics set of two that can be stored.
	for i, b := range brought {
		if b != [3]int{0, 2, 1} {
			t.Errorf("rendering %d was given %d pictures, %d chapters, %d synced lyrics sets, want 0, 2, 1", i, b[0], b[1], b[2])
		}
	}
	// Nothing to ask the writer about, nothing rendered.
	renders = nil
	ProjectTransferOnto(&Media{Format: FormatFLAC, Tags: tagsOf("GENRE", "g", "ENCODER", "e")}, held, caps)
	if len(renders) != 0 {
		t.Errorf("a transfer writing no field rendered %d times", len(renders))
	}
}

// A classifier that speaks for a field can only take away: its dropped stands over a capability
// that says lossy, and where the capability says lossy its reason is the one given, whatever
// the classifier found. A lossy grade is the table's word on the stored form (a genre kept as a
// numeric reference reads back the same and is still not stored as given).
func TestClassifierOverCapability(t *testing.T) {
	partial := Capability{Read: AccessFull, Write: AccessPartial, Fidelity: "the table's reason"}
	src := &Media{Tags: tagsOf("TITLE", "T")}
	for _, c := range []struct {
		verdict Disposition
		spoken  bool
		want    Disposition
		reason  string
	}{
		{Carried, true, Lossy, "the table's reason"},
		{Carried, false, Lossy, "the table's reason"},
		{Lossy, true, Lossy, "the table's reason"},
		{Dropped, true, Dropped, "the writer's reason"},
	} {
		caps := NewCapabilities(FormatMP3, false, partial, Capability{}, Capability{}, AccessNone, nil).
			WithFieldClassifier(func(tag.Key, []string, *Media) (Disposition, string, bool) {
				return c.verdict, "the writer's reason", c.spoken
			})
		got := ProjectTransferOnto(src, nil, caps).Items
		if len(got) != 1 || got[0].Disposition != c.want || got[0].Reason != c.reason {
			t.Errorf("classifier %s (spoken %v): %+v, want %s %q", c.verdict, c.spoken, got, c.want, c.reason)
		}
	}
}

// What a plan's result drops is left unwritten: each field the destination stores nothing of, and
// the pictures, chapters or synced lyrics where it stores none of them. Their grades stand.
func TestWithoutDropped(t *testing.T) {
	field := func(k tag.Key, d Disposition, reason string) TransferItem {
		return TransferItem{Kind: TransferField, Key: k, Count: 1, Disposition: d, Reason: reason}
	}
	set := func(kind TransferKind, n int, d Disposition, reason string) TransferItem {
		return TransferItem{Kind: kind, Count: n, Disposition: d, Reason: reason}
	}
	p := TransferProjection{
		Items: []TransferItem{
			field(tag.TrackNumber, Carried, ""), field(tag.Title, Carried, ""), field(tag.Album, Lossy, "graded"),
			field(tag.Artist, Dropped, "graded"), field(tag.Genre, Carried, ""),
			set(TransferPicture, 2, Carried, ""), set(TransferChapter, 2, Carried, ""), set(TransferSyncedLyric, 1, Carried, ""),
		},
		Keys:     []tag.Key{tag.TrackNumber, tag.Title, tag.Album},
		Pictures: []Picture{{}, {}}, SetPictures: true, pictures: []Disposition{Carried, Carried},
		SetChapters: true, chapters: []Disposition{Carried, Carried},
		SyncedLyrics: []SyncedLyrics{{}}, SetSyncedLyrics: true, lyrics: []Disposition{Carried},
	}
	if got, ok := p.WithoutDropped(p.Items); ok || !slices.Equal(got.Keys, p.Keys) {
		t.Errorf("a report that drops nothing written: ok %v, keys %v", ok, got.Keys)
	}
	// GENRE is held by the destination and not written, so its drop is nothing to unwrite.
	reconciled := []TransferItem{
		field(tag.TrackNumber, Dropped, lostHeldReason), field(tag.Title, Carried, ""), field(tag.Album, Dropped, "mixed in"),
		field(tag.Artist, Dropped, "graded"), field(tag.Genre, Dropped, lostHeldReason),
		set(TransferPicture, 1, Carried, ""), set(TransferPicture, 1, Dropped, "one gone"),
		set(TransferChapter, 2, Dropped, "none stored"), set(TransferSyncedLyric, 1, Lossy, "altered"),
	}
	got, ok := p.WithoutDropped(reconciled)
	if !ok {
		t.Fatal("a report dropping written fields and a written list unwrites nothing")
	}
	want := []TransferItem{
		field(tag.TrackNumber, Dropped, lostHeldReason), field(tag.Title, Carried, ""), field(tag.Album, Dropped, "mixed in"),
		field(tag.Artist, Dropped, "graded"), field(tag.Genre, Carried, ""),
		set(TransferPicture, 2, Carried, ""), set(TransferChapter, 2, Dropped, "none stored"), set(TransferSyncedLyric, 1, Carried, ""),
	}
	if !slices.Equal(got.Items, want) {
		t.Errorf("items:\n got %+v\nwant %+v", got.Items, want)
	}
	if !slices.Equal(got.Keys, []tag.Key{tag.Title}) || !got.SetPictures || len(got.Pictures) != 2 || got.SetChapters || !got.SetSyncedLyrics {
		t.Errorf("writes: keys %v, pictures %v, chapters %v, synced lyrics %v", got.Keys, got.SetPictures, got.SetChapters, got.SetSyncedLyrics)
	}
	if !slices.Equal(p.Keys, []tag.Key{tag.TrackNumber, tag.Title, tag.Album}) || !p.SetChapters {
		t.Error("the projection given was changed")
	}
	// The grades of what was left out stand through the next reconcile, whatever the result,
	// which says that the destination now keeps the TRACKNUMBER it held.
	held := &Media{Tags: tagsOf("TRACKNUMBER", "9", "TITLE", "T"), Chapters: []Chapter{{Title: "own"}}}
	src := &Media{Tags: tagsOf("TRACKNUMBER", "1", "TITLE", "T", "ALBUM", "A", "ARTIST", "R", "GENRE", "G"), Chapters: []Chapter{{Title: "a"}, {Start: time.Second, Title: "b"}}}
	again := ReconcileTransfer(got, src, held, held)
	if again[0] != field(tag.TrackNumber, Dropped, keptHeldReason) || again[2] != field(tag.Album, Dropped, "mixed in") {
		t.Errorf("after the next reconcile: %+v, %+v", again[0], again[2])
	}
	if at := slices.IndexFunc(again, func(it TransferItem) bool { return it.Kind == TransferChapter }); again[at] != set(TransferChapter, 2, Dropped, "none stored") {
		t.Errorf("chapters after the next reconcile: %+v", again[at])
	}
	// What the projection does not write is nothing to unwrite, whatever the report says of it.
	idle := p
	idle.Keys, idle.SetPictures, idle.SetChapters, idle.SetSyncedLyrics = nil, false, false, false
	if _, ok := idle.WithoutDropped([]TransferItem{
		field(tag.Title, Dropped, "t"), set(TransferPicture, 2, Dropped, "p"), set(TransferChapter, 2, Dropped, "c"), set(TransferSyncedLyric, 1, Dropped, "l"),
	}); ok {
		t.Error("a projection that writes nothing has something to unwrite")
	}
	// Each of the three sets is unwritten where none of it is stored.
	none := []TransferItem{
		set(TransferPicture, 2, Dropped, "p"), set(TransferChapter, 2, Carried, ""), set(TransferSyncedLyric, 1, Dropped, "l"),
	}
	got, ok = p.WithoutDropped(none)
	if !ok || got.SetPictures || got.Pictures != nil || !got.SetChapters || got.SetSyncedLyrics || got.SyncedLyrics != nil {
		t.Errorf("pictures and synced lyrics dropped whole: ok %v, %+v", ok, got)
	}
	if at := slices.IndexFunc(got.Items, func(it TransferItem) bool { return it.Kind == TransferPicture }); got.Items[at] != none[0] || got.Items[at+1].Kind != TransferChapter {
		t.Errorf("items after unwriting pictures: %+v", got.Items)
	}
	// Their grades stand through the next reconcile: the source's pictures and sets are not
	// looked for again in a result that was never given them.
	src.Pictures = []Picture{{Type: PicFrontCover, Data: testfile.PNG()}, {Type: PicBackCover, Data: testfile.JPEG()}}
	src.SyncedLyrics = []SyncedLyrics{{Lines: []SyncedLine{{Text: "la"}}}}
	again = ReconcileTransfer(got, src, held, held)
	sets := slices.DeleteFunc(slices.Clone(again), func(it TransferItem) bool { return it.Kind == TransferField || it.Kind == TransferChapter })
	if want := []TransferItem{none[0], none[2]}; !slices.Equal(sets, want) {
		t.Errorf("after the next reconcile:\n got %+v\nwant %+v", sets, want)
	}
}

// A carry keeps of each item what the source's format stores and takes the rest from the
// destination's item of the same text; a language that is only the source writer's default says
// nothing, where the caller names it as filler.
func TestCarryTextItems(t *testing.T) {
	dst := []TextItem{{Text: "hello", Description: "kept", Language: "deu"}, {Text: "other", Language: "fra"}}
	for _, c := range []struct {
		name   string
		src    TextItem
		loss   TextItemLoss
		filler string
		want   TextItem
	}{
		{"a source with both fields gives both", TextItem{Text: "hello", Description: "d", Language: "ita"}, TextLossNone, "", TextItem{Text: "hello", Description: "d", Language: "ita"}},
		{"a source with both fields and neither set clears both", TextItem{Text: "hello"}, TextLossNone, "", TextItem{Text: "hello"}},
		{"a source with no description field leaves the destination's", TextItem{Text: "hello", Language: "ita"}, TextLossDescription, "", TextItem{Text: "hello", Description: "kept", Language: "ita"}},
		{"a source with neither field leaves both", TextItem{Text: "hello"}, TextLossDescriptionAndLanguage, "", TextItem{Text: "hello", Description: "kept", Language: "deu"}},
		{"a filler language leaves the destination's", TextItem{Text: "hello", Language: "eng"}, TextLossNone, "eng", TextItem{Text: "hello", Language: "deu"}},
		{"a filler language with no such item is none", TextItem{Text: "new", Language: "eng"}, TextLossNone, "eng", TextItem{Text: "new"}},
		{"another language is given beside a filler", TextItem{Text: "hello", Language: "ita"}, TextLossNone, "eng", TextItem{Text: "hello", Language: "ita"}},
		{"no language is given beside a filler", TextItem{Text: "hello"}, TextLossNone, "eng", TextItem{Text: "hello"}},
		{"with no filler named the default language is a language", TextItem{Text: "hello", Language: "eng"}, TextLossNone, "", TextItem{Text: "hello", Language: "eng"}},
	} {
		if got := CarryTextItems([]TextItem{c.src}, dst, c.loss, c.filler); len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	// Items of one text pair with the destination's in order.
	twice := CarryTextItems([]TextItem{{Text: "a"}, {Text: "a"}}, []TextItem{{Text: "a", Language: "deu"}, {Text: "a", Language: "fra"}}, TextLossDescriptionAndLanguage, "")
	if len(twice) != 2 || twice[0].Language != "deu" || twice[1].Language != "fra" {
		t.Errorf("two items of one text: %+v", twice)
	}
}
