package core

import (
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

// tinyGIF returns a minimal recognized GIF89a header (3x5), a cover format the MP4 covr
// allowlist does not include.
func tinyGIF() []byte {
	return append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0x77, 0x00, 0x00)
}

// TestProjectTransferDispositions exercises all three dispositions in one pass,
// including the Lossy path. Shipping codecs write a field Full or None, so the
// test uses a synthetic capability set for the partial-write case.
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

	// carried sums the field unit (TITLE) and the 2-chapter set's Count: 1 + 2 = 3.
	carried, lossy, dropped := (TransferReport{Items: items}).Counts()
	if carried != 3 || lossy != 1 || dropped != 1 {
		t.Errorf("counts = (%d,%d,%d), want (3,1,1)", carried, lossy, dropped)
	}
}

// TestProjectTransferMaxItems checks that a set exceeding the destination's hard
// MaxItems cap is reported Dropped - the destination would reject the whole set at
// write time, so reporting it carried would break the report==write invariant.
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

// TestProjectTransferReadOnlyDropsEverything checks that a read-only destination
// drops every item regardless of the per-field write level.
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

// TestProjectTransferSplitsUnrepresentableCovers checks a destination that stores only
// certain cover MIME types: unsupported covers become a separate Dropped item, while the
// representable subset is graded as usual.
func TestProjectTransferSplitsUnrepresentableCovers(t *testing.T) {
	pics := Capability{
		Write: AccessFull, PictureLoss: PictureLossRoleAndDescription,
		PictureMIMEs: []string{"image/jpeg", "image/png", "image/bmp"},
	}
	caps := NewCapabilities(FormatMP4, false, Capability{Write: AccessFull}, pics,
		Capability{Write: AccessNone}, AccessNone, nil)

	// Mixed: one representable JPEG, two unrepresentable (GIF, WebP).
	m := &Media{Format: FormatFLAC, Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/jpeg"},
		{Type: PicFrontCover, MIME: "image/gif"},
		{Type: PicFrontCover, MIME: "image/webp"},
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

	// All-unrepresentable: only a Dropped item, no carried picture item, and the reason
	// lists each distinct MIME once.
	allGIF := &Media{Format: FormatFLAC, Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/gif"},
		{Type: PicBackCover, MIME: "image/gif"},
	}}
	got := ProjectTransfer(allGIF, caps)
	if len(got) != 1 || got[0].Disposition != Dropped || got[0].Count != 2 {
		t.Fatalf("all-unrepresentable items = %+v, want one Dropped of count 2", got)
	}
	if want := "MP4 cannot store image/gif"; got[0].Reason != want {
		t.Errorf("reason = %q, want %q (distinct MIMEs only)", got[0].Reason, want)
	}

	// No PictureMIMEs restriction: the set stays a single item, byte-identical to the
	// pre-split behavior (a format that stores any cover it accepts).
	open := NewCapabilities(FormatFLAC, false, Capability{Write: AccessFull},
		Capability{Write: AccessFull}, Capability{Write: AccessNone}, AccessNone, nil)
	if n := len(ProjectTransfer(m, open)); n != 1 {
		t.Errorf("unrestricted destination produced %d picture items, want 1", n)
	}
}

// TestProjectTransferSplitsPicturesByMetadataLoss checks that when a destination stores image bytes
// losslessly but drops role and description (MP4's covr), a mixed set is partitioned per picture
// rather than flipped whole. A front cover with no description round-trips (Carried) while a back
// cover loses its role (Lossy), so front plus back reports 1 carried, 1 lossy instead of the
// 0 carried, 2 lossy an earlier version reported. The item order is pinned
// carried-then-lossy-then-dropped for stable output.
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

	// Front + back cover, both representable, no descriptions: the front round-trips, the back
	// loses its role. Exactly one carried, one lossy, carried first with an empty reason.
	m := &Media{Format: FormatFLAC, Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/jpeg"},
		{Type: PicBackCover, MIME: "image/jpeg"},
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

	// Adding an unrepresentable cover locks the full three-item order: carried, then lossy, then
	// the dropped-MIME item.
	m2 := &Media{Format: FormatFLAC, Pictures: []Picture{
		{Type: PicFrontCover, MIME: "image/jpeg"}, // carried
		{Type: PicBackCover, MIME: "image/png"},   // lossy: role dropped
		{Type: PicFrontCover, MIME: "image/gif"},  // dropped: unrepresentable MIME
	}}
	got := pictureDisps(ProjectTransfer(m2, caps))
	if len(got) != 3 || got[0] != Carried || got[1] != Lossy || got[2] != Dropped {
		t.Errorf("picture item order = %v, want [Carried Lossy Dropped]", got)
	}
}

// TestProjectTransferSplitsChaptersByMetadataLoss checks that a chapter set copied to a start+title
// destination is partitioned per chapter rather than flipped whole (the chapter analogue of the
// picture split). It asserts the reconstructable-end boundary explicitly, since that is the whole
// subtlety of the per-index predicate: a chapter whose interior end reaches the next start
// reconstructs and carries, while a gapped end is a real loss. The title-byte-cap axis splits the
// same way.
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
	// Start+title destination (FLAC/Ogg model): a gapless interior end reconstructs from the next
	// start, a gapped one cannot, and an open last chapter has no end to lose.
	startTitle := NewCapabilities(FormatFLAC, false, Capability{Write: AccessFull},
		Capability{Write: AccessNone},
		Capability{Write: AccessFull, ChapterLoss: ChapterLossStartTitleOnly}, AccessNone, nil)

	// The reconstructable-end boundary, isolated: two 2-chapter sets differing ONLY in whether the
	// first chapter's end reaches the second's start. The second chapter is open, so it carries in
	// both; only the first's grade flips.
	reconstructable := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, End: 10 * time.Second, Title: "A"}, // end == B.Start -> reconstructable, carried
		{Start: 10 * time.Second, Title: "B"},         // open last -> carried
	}}
	if c, l, _ := (TransferReport{Items: ProjectTransfer(reconstructable, startTitle)}).Counts(); c != 2 || l != 0 {
		t.Errorf("reconstructable-end set counts = (%d carried, %d lossy), want (2, 0): a gapless end must grade carried", c, l)
	}
	gapped := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, End: 5 * time.Second, Title: "A"}, // end 5s != B.Start 10s -> gapped, lossy
		{Start: 10 * time.Second, Title: "B"},        // open last -> carried
	}}
	if c, l, _ := (TransferReport{Items: ProjectTransfer(gapped, startTitle)}).Counts(); c != 1 || l != 1 {
		t.Errorf("gapped-end set counts = (%d carried, %d lossy), want (1, 1): a gapped end must grade lossy", c, l)
	}

	// A mixed set: reconstructable end (carried), gapped interior end (lossy), open last (carried) ->
	// 1 lossy, N-1 carried, carried item first with an empty reason and the lossy item a metadata reason.
	mixed := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, End: 10 * time.Second, Title: "A"},                // reaches B -> carried
		{Start: 10 * time.Second, End: 15 * time.Second, Title: "B"}, // gapped (C at 20) -> lossy
		{Start: 20 * time.Second, Title: "C"},                        // open last -> carried
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

	// Title-byte-cap axis, no metadata loss: only the over-long title is lossy, with the truncation
	// reason (not the metadata reason), the rest carried.
	titleCap := NewCapabilities(FormatMP4, false, Capability{Write: AccessFull},
		Capability{Write: AccessNone},
		Capability{Write: AccessFull, ChapterTitleByteMax: 3}, AccessNone, nil)
	m2 := &Media{Format: FormatMatroska, Chapters: []Chapter{
		{Start: 0, Title: "ok"},                     // within the 3-byte cap -> carried
		{Start: time.Second, Title: "way too long"}, // exceeds it -> lossy
	}}
	got2 := chapterItems(ProjectTransfer(m2, titleCap))
	if len(got2) != 2 || got2[0].Disposition != Carried || got2[1].Disposition != Lossy {
		t.Fatalf("title-cap chapter items = %+v, want [Carried, Lossy]", got2)
	}
	if got2[1].Reason != "chapter title is too long and was truncated" {
		t.Errorf("title-cap lossy reason = %q, want the truncation message", got2[1].Reason)
	}
}

// TestProjectTransferReasonUsesSniffedMIME checks that a dropped cover's reason names the
// sniffed type used to reject it, not a wrong or empty stored label. A GIF mislabeled as
// JPEG, and an unlabeled GIF, both read "cannot store image/gif".
func TestProjectTransferReasonUsesSniffedMIME(t *testing.T) {
	gif := tinyGIF()
	pics := Capability{Write: AccessFull, PictureMIMEs: []string{"image/jpeg", "image/png", "image/bmp"}}
	caps := NewCapabilities(FormatMP4, false, Capability{Write: AccessFull}, pics,
		Capability{Write: AccessNone}, AccessNone, nil)

	for _, label := range []string{"image/jpeg", ""} { // a wrong label, then an empty one
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

// TestRepresentableUsesSniffedMIME guards the per-image split against a false drop:
// Representable must compare the MIME an authoritative sniff settles on (what AddPicture
// stores), not the raw container label. A storable JPEG carried under a non-canonical
// alias or odd casing is representable; a GIF mislabeled as JPEG is not (the bytes win);
// a label-only picture (no bytes) falls back to the stored label.
func TestRepresentableUsesSniffedMIME(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x00, 0x05, 0x00, 0x03, 0x03, 0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01}
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
	// Label-only (no recognizable bytes): the stored label decides.
	if Representable(mp4, Picture{MIME: "image/gif"}) {
		t.Error("a label-only image/gif (no bytes) must not be representable")
	}
	if !Representable(mp4, Picture{MIME: "image/png"}) {
		t.Error("a label-only image/png (no bytes) should be representable")
	}
}

// TestChaptersLoseMetadata checks the start+title-only loss predicate. Metadata the
// destination drops flags a loss - including any per-chapter language, since these stores
// hold no language field (uniform or varying alike). ChapterLossNone never flags a loss.
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
		{"gapped-end", []Chapter{{End: sec(3)}, {Start: sec(5)}}, true},      // gap inference cannot recover this
		{"contiguous-end", []Chapter{{End: sec(5)}, {Start: sec(5)}}, false}, // End == next Start, inferred
		{"last-end", []Chapter{{}, {Start: sec(5), End: sec(9)}}, true},      // last End always lost
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

// TestChaptersLoseMetadataInteriorEnds checks the MP4 QuickTime loss predicate. It
// matches ChapterLossStartTitleOnly except the final chapter's explicit end is kept
// (the text track stores it), so only an interior gapped end is a loss.
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
		{"gapped-interior-end", []Chapter{{End: sec(3)}, {Start: sec(5)}}, true},                     // interior gap cannot be inferred
		{"contiguous-end", []Chapter{{End: sec(5)}, {Start: sec(5)}}, false},                         // End == next Start, inferred
		{"last-end-kept", []Chapter{{}, {Start: sec(5), End: sec(9)}}, false},                        // the text track stores the final end
		{"last-end-plus-interior-gap", []Chapter{{End: sec(3)}, {Start: sec(5), End: sec(9)}}, true}, // interior gap still lost
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ChaptersLoseMetadata(c.chs, ChapterLossInteriorEndsLangFlags); got != c.want {
				t.Errorf("ChaptersLoseMetadata(InteriorEnds) = %v, want %v", got, c.want)
			}
		})
	}
}

// TestChaptersLoseMetadataLangFlags checks the ID3 CHAP loss predicate. Start, end, and
// title all survive, so gapped and last-chapter ends are not a loss. CHAP has no language
// or visibility fields, so any language or Matroska visibility flag is a loss.
func TestChaptersLoseMetadataLangFlags(t *testing.T) {
	sec := func(s int) time.Duration { return time.Duration(s) * time.Second }
	cases := []struct {
		name string
		chs  []Chapter
		want bool
	}{
		{"plain", []Chapter{{Start: 0, Title: "A"}, {Start: sec(5), Title: "B"}}, false},
		{"gapped-end-kept", []Chapter{{End: sec(3)}, {Start: sec(5)}}, false},                 // CHAP stores ends
		{"last-end-kept", []Chapter{{}, {Start: sec(5), End: sec(9)}}, false},                 // CHAP stores ends
		{"uniform-iso", []Chapter{{Language: "eng"}, {Start: sec(5), Language: "eng"}}, true}, // no language field at all
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

// TestProjectTransferChapterGrading checks that a start+title-only destination marks
// chapter sets Lossy only when they carry metadata the destination drops. Plain
// chapters copy as Carried, and a lossless destination carries metadata-bearing
// chapters as well.
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

	// ID3 CHAP keeps ends but stores no per-chapter language. A Matroska source whose
	// chapters carry language, even uniformly, copies as Lossy; plain chapters carry.
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

// TestProjectTransferSyncedLyricsTimestampClamp checks the upgrade: a synced-lyric line
// past the destination's SyncedLyricsTimeMax grades Lossy (the write clamps it), while a set
// within the ceiling carries. The destination stores the language too (no metadata loss), so
// the timestamp clamp is the only thing that can make it lossy.
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
	// A line exactly at the ceiling round-trips (strictly-greater clamp), so it carries.
	atMax := []SyncedLyrics{{Lines: []SyncedLine{{Time: 100 * time.Second, Text: "edge"}}}}
	if it := syncedItem(atMax); it.Disposition != Carried {
		t.Errorf("a line exactly at the ceiling = %s, want Carried (clamp is strictly greater)", it.Disposition)
	}
}

// TestProjectTransferSplitsSyncedLyricsByMetadataLoss checks that a synced-lyrics set is partitioned
// per set, like pictures and chapters. An LRC destination drops each set's language and descriptor,
// so a set carrying one is lossy while a plain set carries: two sets, one plain and one with a
// descriptor, report 1 carried, 1 lossy rather than the whole set flipped. The timestamp-clamp axis
// splits the same way, taking the clamp reason when no set lost metadata.
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
	// LRC destination: drops each set's per-set language/descriptor, with a timestamp ceiling too.
	lrc := NewCapabilities(FormatFLAC, false, Capability{Write: AccessFull},
		Capability{Write: AccessNone}, Capability{Write: AccessNone}, AccessNone, nil).
		WithSyncedLyrics(Capability{Write: AccessFull, SyncedLyricsLoss: SyncedLyricsLossLanguage, SyncedLyricsTimeMax: 100 * time.Second})

	// One plain set (carried) beside one carrying a descriptor (lossy metadata).
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

	// Timestamp-clamp axis, no metadata loss: only the over-ceiling set is lossy, with the clamp
	// reason (not the metadata reason).
	clampOnly := NewCapabilities(FormatMP3, false, Capability{Write: AccessFull},
		Capability{Write: AccessNone}, Capability{Write: AccessNone}, AccessNone, nil).
		WithSyncedLyrics(Capability{Write: AccessFull, SyncedLyricsTimeMax: 100 * time.Second})
	m2 := &Media{Format: FormatMatroska, SyncedLyrics: []SyncedLyrics{
		{Lines: []SyncedLine{{Time: 0, Text: "ok"}}},                   // within -> carried
		{Lines: []SyncedLine{{Time: 200 * time.Second, Text: "late"}}}, // over -> lossy
	}}
	got2 := syncedItems(ProjectTransfer(m2, clampOnly))
	if len(got2) != 2 || got2[0].Disposition != Carried || got2[1].Disposition != Lossy {
		t.Fatalf("clamp-axis synced items = %+v, want [Carried, Lossy]", got2)
	}
	if got2[1].Reason != "a synced-lyric timestamp is too large and was clamped" {
		t.Errorf("clamp-axis lossy reason = %q, want the clamp message", got2[1].Reason)
	}
}

// TestProjectTransferEmptyMetadata: a source with no canonical metadata yields no
// items (and so is trivially lossless).
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

// TestProjectTransferTrimsNumericDateValues checks that transfer grading sees the value
// the writer stores. Numeric and date fields are trimmed before value-level predicates
// run, while unrelated fields keep surrounding whitespace.
func TestProjectTransferTrimsNumericDateValues(t *testing.T) {
	// A drop predicate that fires only for surrounding whitespace proves whether trimming
	// happened before grading.
	dropsIfPadded := func(v string) bool { return v != strings.TrimSpace(v) }
	padSensitive := WithValueDrop(Capability{Read: AccessFull, Write: AccessFull}, dropsIfPadded)
	caps := NewCapabilities(FormatMP3, false,
		Capability{Read: AccessFull, Write: AccessFull}, Capability{}, Capability{}, AccessNone,
		map[tag.Key]Capability{tag.RecordingDate: padSensitive, tag.Title: padSensitive})

	m := &Media{Tags: tag.NewTagSet()}
	m.Tags.Set(tag.RecordingDate, " 2021 ") // a date field: trimmed before grading
	m.Tags.Set(tag.Title, " padded ")       // a non-date field: not trimmed

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
