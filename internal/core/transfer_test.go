package core

import (
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

// tinyGIF: minimal GIF89a (3x5); not on MP4 covr allowlist.
func tinyGIF() []byte {
	return append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0x77, 0x00, 0x00)
}

// tinyWebP: minimal WebP header; also outside MP4 allowlist.
func tinyWebP() []byte { return []byte("RIFF\x00\x00\x00\x00WEBP") }

// tinyPNG/tinyJPEG: allowlisted formats. Pictures need real bytes so sniff sets MIME.
func tinyPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
	}
}

func tinyJPEG() []byte {
	return []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x00, 0x05, 0x00, 0x03, 0x03, 0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01}
}

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

	items := ProjectTransfer(m, caps)
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
	items := ProjectTransfer(over, caps)
	if len(items) != 1 || items[0].Kind != TransferChapter || items[0].Disposition != Dropped {
		t.Fatalf("256 chapters vs limit 255 should drop, got %+v", items)
	}
	if items[0].Reason == "" {
		t.Error("an over-limit drop must carry a reason")
	}

	atLimit := &Media{Format: FormatFLAC, Chapters: make([]Chapter, 255)}
	if got := ProjectTransfer(atLimit, caps); got[0].Disposition != Carried {
		t.Errorf("255 chapters at the limit should carry, got %s", got[0].Disposition)
	}
}

// TestProjectTransferReadOnlyDropsEverything: read-only destination drops all items.
func TestProjectTransferReadOnlyDropsEverything(t *testing.T) {
	var ts tag.TagSet
	ts.Set("TITLE", "x")
	m := &Media{Format: FormatFLAC, Tags: ts, Pictures: []Picture{{}}}
	caps := NewCapabilities(FormatMatroska, true,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, Capability{}, AccessNone, nil)

	items := ProjectTransfer(m, caps)
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
		{Type: PicFrontCover, MIME: "image/jpeg", Data: tinyJPEG()},
		{Type: PicFrontCover, MIME: "image/gif", Data: tinyGIF()},
		{Type: PicFrontCover, MIME: "image/webp", Data: tinyWebP()},
	}}
	items := ProjectTransfer(m, caps)
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
		{Type: PicFrontCover, MIME: "image/gif", Data: tinyGIF()},
		{Type: PicBackCover, MIME: "image/gif", Data: tinyGIF()},
	}}
	got := ProjectTransfer(allGIF, caps)
	if len(got) != 1 || got[0].Disposition != Dropped || got[0].Count != 2 {
		t.Fatalf("all-unrepresentable items = %+v, want one Dropped of count 2", got)
	}
	if want := "MP4 cannot store image/gif"; got[0].Reason != want {
		t.Errorf("reason = %q, want %q (distinct MIMEs only)", got[0].Reason, want)
	}

	// No MIME restriction: single picture item.
	open := NewCapabilities(FormatFLAC, false, Capability{Write: AccessFull},
		Capability{Write: AccessFull}, Capability{Write: AccessNone}, AccessNone, nil)
	if n := len(ProjectTransfer(m, open)); n != 1 {
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
		{Type: PicFrontCover, MIME: "image/jpeg", Data: tinyJPEG()},
		{Type: PicBackCover, MIME: "image/jpeg", Data: tinyJPEG()},
	}}
	items := ProjectTransfer(m, caps)
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
		{Type: PicFrontCover, MIME: "image/jpeg", Data: tinyJPEG()},
		{Type: PicBackCover, MIME: "image/png", Data: tinyPNG()},
		{Type: PicFrontCover, MIME: "image/gif", Data: tinyGIF()},
	}}
	got := pictureDisps(ProjectTransfer(m2, caps))
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
	if c, l, _ := (TransferReport{Items: ProjectTransfer(reconstructable, startTitle)}).Counts(); c != 2 || l != 0 {
		t.Errorf("reconstructable-end set counts = (%d carried, %d lossy), want (2, 0): a gapless end must grade carried", c, l)
	}
	gapped := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, End: 5 * time.Second, Title: "A"}, // gapped end
		{Start: 10 * time.Second, Title: "B"},
	}}
	if c, l, _ := (TransferReport{Items: ProjectTransfer(gapped, startTitle)}).Counts(); c != 1 || l != 1 {
		t.Errorf("gapped-end set counts = (%d carried, %d lossy), want (1, 1): a gapped end must grade lossy", c, l)
	}

	mixed := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, End: 10 * time.Second, Title: "A"},
		{Start: 10 * time.Second, End: 15 * time.Second, Title: "B"}, // gapped (C at 20s)
		{Start: 20 * time.Second, Title: "C"},
	}}
	got := chapterItems(ProjectTransfer(mixed, startTitle))
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
	got2 := chapterItems(ProjectTransfer(m2, titleCap))
	if len(got2) != 2 || got2[0].Disposition != Carried || got2[1].Disposition != Lossy {
		t.Fatalf("title-cap chapter items = %+v, want [Carried, Lossy]", got2)
	}
	if got2[1].Reason != "chapter title is too long and was truncated" {
		t.Errorf("title-cap lossy reason = %q, want the truncation message", got2[1].Reason)
	}
}

// TestProjectTransferReasonUsesSniffedMIME: drop reason uses sniffed MIME, not stored label.
func TestProjectTransferReasonUsesSniffedMIME(t *testing.T) {
	gif := tinyGIF()
	pics := Capability{Write: AccessFull, PictureMIMEs: []string{"image/jpeg", "image/png", "image/bmp"}}
	caps := NewCapabilities(FormatMP4, false, Capability{Write: AccessFull}, pics,
		Capability{Write: AccessNone}, AccessNone, nil)

	for _, label := range []string{"image/jpeg", ""} {
		m := &Media{Format: FormatFLAC, Pictures: []Picture{{Type: PicFrontCover, MIME: label, Data: gif}}}
		var dropped *TransferItem
		items := ProjectTransfer(m, caps)
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
	jpeg := tinyJPEG()
	gif := tinyGIF()
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
		for _, it := range ProjectTransfer(&Media{Format: FormatMatroska, Chapters: chs}, caps) {
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
		for _, it := range ProjectTransfer(&Media{Format: FormatFLAC, SyncedLyrics: sls}, dst) {
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
	got := syncedItems(ProjectTransfer(m, lrc))
	if len(got) != 2 || got[0].Disposition != Carried || got[1].Disposition != Lossy {
		t.Fatalf("synced-lyrics items = %+v, want [Carried, Lossy]", got)
	}
	if got[0].Count != 1 || got[0].Reason != "" {
		t.Errorf("carried synced item = %+v, want count 1 with an empty reason", got[0])
	}
	if got[1].Count != 1 || got[1].Reason == "" {
		t.Errorf("lossy synced item = %+v, want count 1 with a metadata reason", got[1])
	}
	if c, l, d := (TransferReport{Items: ProjectTransfer(m, lrc)}).Counts(); c != 1 || l != 1 || d != 0 {
		t.Errorf("counts = (%d,%d,%d), want (1,1,0)", c, l, d)
	}

	clampOnly := NewCapabilities(FormatMP3, false, Capability{Write: AccessFull},
		Capability{Write: AccessNone}, Capability{Write: AccessNone}, AccessNone, nil).
		WithSyncedLyrics(Capability{Write: AccessFull, SyncedLyricsTimeMax: 100 * time.Second})
	m2 := &Media{Format: FormatMatroska, SyncedLyrics: []SyncedLyrics{
		{Lines: []SyncedLine{{Time: 0, Text: "ok"}}},
		{Lines: []SyncedLine{{Time: 200 * time.Second, Text: "late"}}},
	}}
	got2 := syncedItems(ProjectTransfer(m2, clampOnly))
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
	items := ProjectTransfer(m, NewCapabilities(FormatMP4, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, Capability{Write: AccessFull}, AccessNone, nil))
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
	for _, it := range ProjectTransfer(m, caps) {
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
	items := ProjectTransfer(m, caps)
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
	for _, it := range ProjectTransfer(one, caps) {
		if it.Kind == TransferPicture && it.Disposition == Dropped {
			t.Errorf("single-picture set produced a dropped item: %+v", it)
		}
	}

	// No picture write: one whole-set drop; slot partition must not add a second item.
	none := caps
	none.Pictures.Write = AccessNone
	var pictureItems []TransferItem
	for _, it := range ProjectTransfer(m, none) {
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
		for _, it := range ProjectTransfer(m, caps) {
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
