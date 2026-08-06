package waxlabel_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// A saio (sample auxiliary information offsets, used by CENC) holds absolute file
// offsets into an mdat, so a growing rewrite must shift it exactly as it shifts the
// chunk-offset tables. It is kept in its own slice because an aux offset is not a media
// chunk: merging it would move the essence trim and change the audio digest.

// longTitle grows the ilst past any free padding, forcing the offset-fixup path.
const longTitle = "A substantially longer title that grows the ilst region past its padding"

// mp4SaioFile builds a tagged file with a saio in the audio track's stbl whose single
// offset points 16 bytes into the mdat payload - past the ilst region, so a growing edit
// must shift it. The two-pass build resolves that offset against the final layout (the
// offset value is fixed-width, so it does not disturb the layout it is measured from).
func mp4SaioFile(t *testing.T, version uint8, auxType bool) []byte {
	t.Helper()
	build := func(auxOff uint32) []byte {
		return mp4AssembleExtra(mp4Saio(version, auxType, auxOff), nil,
			mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))))
	}
	tmp := build(0)
	j := bytes.Index(tmp, []byte("mdat"))
	if j < 0 {
		t.Fatal("setup: no mdat in the synthesized file")
	}
	return build(uint32(j + 4 + 16))
}

func TestMP4SaioOffsetsShifted(t *testing.T) {
	// The full 2x2 matrix pins both the entryPrefix arithmetic (flags&1 inserts an
	// 8-byte aux_info_type pair ahead of the entry count) and the per-atom width
	// derivation (the version byte, not the caller, decides 32- vs 64-bit).
	cases := []struct {
		name      string
		version   uint8
		auxType   bool
		wantWidth int
	}{
		{"v0 no aux type", 0, false, 4},
		{"v0 with aux type", 0, true, 4},
		{"v1 no aux type", 1, false, 8},
		{"v1 with aux type", 1, true, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := mp4SaioFile(t, tc.version, tc.auxType)
			before, widthBefore := mp4SaioEntries(t, data)
			if len(before) != 1 {
				t.Fatalf("setup: parsed %d saio entries, want 1", len(before))
			}
			if widthBefore != tc.wantWidth {
				t.Fatalf("setup: saio entry width = %d, want %d", widthBefore, tc.wantWidth)
			}

			plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, longTitle).Prepare()
			if err != nil {
				t.Fatalf("growing edit: %v", err)
			}
			out := applyToBytes(t, data, plan)
			delta := int64(len(out) - len(data))
			if delta <= 0 {
				t.Fatalf("setup: the edit did not grow the file (delta %d)", delta)
			}

			after, widthAfter := mp4SaioEntries(t, out)
			if widthAfter != tc.wantWidth {
				t.Errorf("saio entry width after the edit = %d, want %d (the rewrite must not change it)", widthAfter, tc.wantWidth)
			}
			if len(after) != len(before) {
				t.Fatalf("saio entry count = %d, want %d", len(after), len(before))
			}
			for i := range after {
				if want := before[i] + uint64(delta); after[i] != want {
					t.Errorf("saio entry %d = %d, want %d (shifted by the %d-byte metadata delta)", i, after[i], want, delta)
				}
			}
			// The chunk offsets must still track the mdat too, so the aux patch did not
			// land on top of them.
			if mdat, stco := mp4Index(t, out); mdat != stco {
				t.Errorf("stco entry = %d, want the mdat payload offset %d", stco, mdat)
			}
		})
	}
}

// mp4UnknownSaioFile builds a tagged file carrying a saio whose version this codec does
// not recognize, so its entry width is unknown.
func mp4UnknownSaioFile() []byte {
	return mp4AssembleExtra(mp4Saio(2, false, 0x1000), nil,
		mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))))
}

func TestMP4MalformedSaioStillReads(t *testing.T) {
	// A saio the codec cannot decode must not fail the parse: the movie box and tag list
	// do not depend on it, and such a file read fine before saio was collected at all. It
	// degrades to a write refusal, so dump/lint/verify keep working. A broken stco/co64 is
	// deliberately different - that one leaves the media itself unaddressable.
	cases := map[string][]byte{
		"unknown version":   mp4Saio(2, false, 0x1000),
		"absurd entrycount": mp4Atom("saio", slices.Concat([]byte{0, 0, 0, 0}, mp4be32(1<<30))),
		"short body":        mp4Atom("saio", []byte{0, 0, 0, 0}),
		"truncated entries": mp4Atom("saio", slices.Concat([]byte{0, 0, 0, 1}, mp4be32(4), mp4be32(1))),
	}
	for name, saio := range cases {
		t.Run(name, func(t *testing.T) {
			data := mp4AssembleExtra(saio, nil, mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))))
			doc := mustParseBytes(t, data)
			if got := doc.Fields().Title; got != "T" {
				t.Errorf("Title = %q, want T", got)
			}
			// Reads work; only the rewrite is refused.
			if _, err := doc.Edit().Set(tag.Title, longTitle).Prepare(); err == nil {
				t.Error("expected the write to be refused")
			}
			if !doc.Capabilities().ReadOnly {
				t.Error("a file with an undecodable saio should report ReadOnly")
			}
		})
	}
}

func TestMP4SaioUnknownVersionRefused(t *testing.T) {
	// An unrecognized saio version leaves the entry width unknown. Guessing 32-bit would
	// write 4-byte entries over 8-byte data, so the write is refused; the file still reads.
	data := mp4UnknownSaioFile()
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "T" {
		t.Fatalf("Title = %q, want T (an unknown saio version must still read)", got)
	}
	_, err := doc.Edit().Set(tag.Title, longTitle).Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Fatalf("write error = %v, want ErrUnsupportedFormat", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("saio")) {
		t.Errorf("error should name the saio box: %v", err)
	}
}

// mp4SaioAheadOfAudio builds a tagged file whose audio chunk sits auxLead bytes into the
// mdat payload, optionally with a saio pointing at the payload start - ahead of that
// chunk. A saio treated as a chunk offset would drag the essence trim back to the payload
// start, which is the auxLead == 0 shape.
func mp4SaioAheadOfAudio(auxLead int, withSaio bool) []byte {
	mdatPayload := bytes.Repeat([]byte{0xA7}, 120)
	build := func(stcoOff, auxOff uint32) []byte {
		var stblExtra []byte
		if withSaio {
			stblExtra = mp4Saio(0, false, auxOff)
		}
		udta := mp4Atom("udta", mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))))
		return slices.Concat(mp4Ftyp(), mp4MoovExtra(udta, stcoOff, stblExtra, nil), mp4Atom("mdat", mdatPayload))
	}
	tmp := build(0, 0)
	payload := bytes.Index(tmp, []byte("mdat")) + 4
	return build(uint32(payload+auxLead), uint32(payload))
}

func TestMP4SaioDoesNotAffectEssenceDigest(t *testing.T) {
	// Guards why auxTables is a separate slice: nonChapterTables and
	// firstNonChapterChunk treat every offTables entry as a chunk locating media, so a
	// merged saio sitting ahead of the first audio chunk would silently widen the essence
	// extent. The digest covers exactly the trimmed extent, so it is the operative signal;
	// the two files differ in size, so absolute offsets could not be compared anyway.
	const auxLead = 32
	withSaio := essenceOf(t, mp4SaioAheadOfAudio(auxLead, true))
	without := essenceOf(t, mp4SaioAheadOfAudio(auxLead, false))
	if !withSaio.Equal(without) {
		t.Error("a saio ahead of the first audio chunk changed the essence digest")
	}
	// The control: a file whose audio genuinely starts at the payload start - the extent a
	// merged saio would have produced - must hash differently. Without this the equality
	// above could hold for the wrong reason (a digest blind to the trim).
	if untrimmed := essenceOf(t, mp4SaioAheadOfAudio(0, false)); withSaio.Equal(untrimmed) {
		t.Error("the digest is insensitive to the essence trim, so the check above proves nothing")
	}
}

// mp4QTFileSaioTrailingMdat builds a QuickTime-chapter MP4 whose trailing mdat holds the
// chapter chunk at its payload start and saio-referenced aux data after it, with the
// audio in an earlier mdat. No non-chapter CHUNK lands in the trailing mdat, so only the
// aux check can stop the chapter rewrite from reclaiming (deleting) it.
func mp4QTFileSaioTrailingMdat(startsMS []int, titles []string, aux []byte) []byte {
	audio := bytes.Repeat([]byte{0xA7}, 120)
	samples := mp4SamplesBlob(titles)
	build := func(audioStco, textStco, auxOff uint32) []byte {
		kids := slices.Concat(
			mp4AudioTrakChap(2, audioStco, mp4Saio(0, false, auxOff)),
			mp4TextTrak(2, 1000, startsMS, titles, textStco),
		)
		moov := mp4Atom("moov", kids)
		return slices.Concat(mp4Ftyp(), moov,
			mp4Atom("mdat", audio),
			mp4Atom("mdat", slices.Concat(samples, aux)))
	}
	tmp := build(0, 0, 0)
	audioOff := bytes.Index(tmp, []byte("mdat")) + 4
	textOff := bytes.Index(tmp[audioOff:], []byte("mdat")) + audioOff + 4
	return build(uint32(audioOff), uint32(textOff), uint32(textOff+len(samples)))
}

func TestMP4SaioBlocksChapterMdatReclaim(t *testing.T) {
	// Splitting auxTables out of nonChapterTables is right for the essence trim but
	// would silently remove aux data from the reclaim guard, so the guard carries its own
	// aux check. Without it this trailing mdat is deleted and every saio offset just
	// patched points at nothing.
	aux := bytes.Repeat([]byte{0xBB}, 48)
	data := mp4QTFileSaioTrailingMdat([]int{0, 5000}, []string{"A", "B"}, aux)
	if !bytes.Contains(data, aux) {
		t.Fatal("setup: aux data missing from the fixture")
	}

	plan, err := mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "X"},
		wl.Chapter{Start: 3 * time.Second, Title: "Y"},
	).Prepare()
	if err != nil {
		t.Fatalf("chapter edit: %v", err)
	}
	out := applyToBytes(t, data, plan)

	if !bytes.Contains(out, aux) {
		t.Fatal("the chapter rewrite reclaimed an mdat holding sample auxiliary data")
	}
	// The surviving saio offset must still resolve to the aux data it names.
	offsets, _ := mp4SaioEntries(t, out)
	if len(offsets) != 1 {
		t.Fatalf("saio entries = %d, want 1", len(offsets))
	}
	if got := int(offsets[0]); got+len(aux) > len(out) || !bytes.Equal(out[got:got+len(aux)], aux) {
		t.Errorf("saio offset %d no longer points at the aux data", got)
	}
	if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 || chs[0].Title != "X" {
		t.Errorf("chapters after edit = %+v, want [X Y]", chs)
	}
}

func TestMP4SaioInChapterTrackNotPatched(t *testing.T) {
	// A chapter rewrite replaces the chapter track wholesale. A saio inside it must be
	// skipped by the aux fixup, exactly as that track's stco already is: patching it
	// would emit an edit inside the replaced region and fail assemble's overlap guard.
	build := func(audioStco, textStco uint32) []byte {
		kids := slices.Concat(
			mp4AudioTrakChap(2, audioStco),
			mp4TextTrak(2, 1000, []int{0, 5000}, []string{"A", "B"}, textStco, mp4Saio(0, false, textStco)),
		)
		audio := bytes.Repeat([]byte{0xA7}, 120)
		mdat := mp4Atom("mdat", slices.Concat(audio, mp4SamplesBlob([]string{"A", "B"})))
		return slices.Concat(mp4Ftyp(), mp4Atom("moov", kids), mdat)
	}
	tmp := build(0, 0)
	audioOff := bytes.Index(tmp, []byte("mdat")) + 4
	data := build(uint32(audioOff), uint32(audioOff+120))

	plan, err := mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "X"},
		wl.Chapter{Start: 3 * time.Second, Title: "Y"},
	).Prepare()
	if err != nil {
		t.Fatalf("a saio inside the chapter track must not break the rewrite: %v", err)
	}
	out := applyToBytes(t, data, plan)
	if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 || chs[0].Title != "X" {
		t.Errorf("chapters after edit = %+v, want [X Y]", chs)
	}
}
