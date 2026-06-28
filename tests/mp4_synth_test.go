package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Synthetic MP4/iTunes builders. The mdat payload is filler bytes; tests
// assert on the atom structure, tag round-trips, and chunk-offset fixups, not on
// decoded audio.

func mp4be32(n int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(n))
	return b
}

// mp4Atom wraps a body in an atom header: [size][name][body].
func mp4Atom(name string, body []byte) []byte {
	return slices.Concat(mp4be32(8+len(body)), []byte(name), body)
}

// mp4Data builds a "data" sub-atom: [size]["data"][version=0][type:24][locale=0][value].
func mp4Data(typ uint32, value []byte) []byte {
	tf := make([]byte, 4)
	binary.BigEndian.PutUint32(tf, typ&0x00FFFFFF)
	return mp4Atom("data", slices.Concat(tf, []byte{0, 0, 0, 0}, value))
}

// mp4Text builds a text item: one UTF-8 data atom.
func mp4Text(name, value string) []byte {
	return mp4Atom(name, mp4Data(1, []byte(value)))
}

// mp4Freeform builds a "----" com.apple.iTunes freeform item.
func mp4Freeform(meanName, name, value string) []byte {
	mean := mp4Atom("mean", append([]byte{0, 0, 0, 0}, meanName...))
	nm := mp4Atom("name", append([]byte{0, 0, 0, 0}, name...))
	return mp4Atom("----", slices.Concat(mean, nm, mp4Data(1, []byte(value))))
}

// mp4Pair builds a trkn/disk number/total atom (8-byte value with trailing pad).
func mp4Pair(name string, num, total int) []byte {
	v := make([]byte, 8)
	binary.BigEndian.PutUint16(v[2:4], uint16(num))
	binary.BigEndian.PutUint16(v[4:6], uint16(total))
	return mp4Atom(name, mp4Data(0, v))
}

func mp4Meta(children ...[]byte) []byte {
	return mp4Atom("meta", append([]byte{0, 0, 0, 0}, slices.Concat(children...)...))
}

// mp4MetaBare builds a QuickTime-style meta with no FullBox version/flags prefix
// (its children start immediately).
func mp4MetaBare(children ...[]byte) []byte {
	return mp4Atom("meta", slices.Concat(children...))
}

func mp4Ilst(items ...[]byte) []byte { return mp4Atom("ilst", slices.Concat(items...)) }

func mp4HdlrMdir() []byte {
	return mp4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("mdirappl"), make([]byte, 9)))
}

func mp4HdlrSoun() []byte {
	return mp4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("soun"), make([]byte, 12)))
}

func mp4Mdhd() []byte {
	body := slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), mp4be32(44100), mp4be32(44100))
	return mp4Atom("mdhd", body)
}

// mp4StsdAudio builds an stsd with a single mp4a AudioSampleEntry: stereo, 16-bit,
// 44100 Hz.
func mp4StsdAudio() []byte {
	entryBody := slices.Concat(
		make([]byte, 6), []byte{0, 1}, // reserved + data_ref_index
		make([]byte, 8),                                 // reserved
		[]byte{0, 2}, []byte{0, 16}, []byte{0, 0, 0, 0}, // channels, sample_size, predefined+reserved
		[]byte{0xAC, 0x44, 0, 0}, // sample_rate 44100 << 16
	)
	entry := mp4Atom("mp4a", entryBody)
	return mp4Atom("stsd", slices.Concat([]byte{0, 0, 0, 0}, mp4be32(1), entry))
}

func mp4Stco(offset uint32) []byte {
	return mp4Atom("stco", slices.Concat([]byte{0, 0, 0, 0}, mp4be32(1), mp4be32(int(offset))))
}

// mp4SounTrak builds a minimal audio (soun) trak with the given track id and a
// single-entry stco at stcoOff. Unlike mp4AudioTrakChap it carries no tref, so it
// models a second, independent audio track in a multi-track file.
func mp4SounTrak(trackID int, stcoOff uint32) []byte {
	tkhd := mp4Atom("tkhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), mp4be32(trackID), make([]byte, 4)))
	stbl := mp4Atom("stbl", slices.Concat(mp4StsdAudio(), mp4Stco(stcoOff)))
	minf := mp4Atom("minf", stbl)
	mdia := mp4Atom("mdia", slices.Concat(mp4HdlrSoun(), mp4Mdhd(), minf))
	return mp4Atom("trak", slices.Concat(tkhd, mdia))
}

// mp4Moov assembles the movie box: one audio trak (with a one-entry stco) and the
// given udta. The stco entry points at stcoOff.
func mp4Moov(udta []byte, stcoOff uint32) []byte {
	stbl := mp4Atom("stbl", slices.Concat(mp4StsdAudio(), mp4Stco(stcoOff)))
	minf := mp4Atom("minf", stbl)
	mdia := mp4Atom("mdia", slices.Concat(mp4HdlrSoun(), mp4Mdhd(), minf))
	trak := mp4Atom("trak", mdia)
	return mp4Atom("moov", slices.Concat(trak, udta))
}

func mp4Ftyp() []byte {
	return mp4Atom("ftyp", slices.Concat([]byte("M4A "), mp4be32(0), []byte("M4A mp42isom")))
}

// mp4AssembleUdta builds ftyp + moov(trak + udta(udtaKids)) + mdat, patching the
// stco entry to point at the mdat payload.
func mp4AssembleUdta(udtaKids ...[]byte) []byte {
	mdatPayload := bytes.Repeat([]byte{0xA7}, 120)
	build := func(stcoOff uint32) []byte {
		udta := mp4Atom("udta", slices.Concat(udtaKids...))
		return slices.Concat(mp4Ftyp(), mp4Moov(udta, stcoOff), mp4Atom("mdat", mdatPayload))
	}
	tmp := build(0)
	j := bytes.Index(tmp, []byte("mdat"))
	return build(uint32(j + 4)) // stco points at the mdat payload (after its 4-byte name)
}

// mp4Assemble builds a file whose udta holds a single meta box with metaKids
// (hdlr, ilst, and optionally an adjacent free atom).
func mp4Assemble(metaKids ...[]byte) []byte {
	return mp4AssembleUdta(mp4Meta(metaKids...))
}

// mp4Tagged builds a standard tagged file: meta = hdlr + ilst(items).
func mp4Tagged(items ...[]byte) []byte {
	return mp4Assemble(mp4HdlrMdir(), mp4Ilst(items...))
}

// mp4Index returns the mdat payload offset and the stco's first entry value, the
// two numbers an offset fixup must keep equal.
func mp4Index(t *testing.T, data []byte) (mdatPayload int64, stcoEntry int64) {
	t.Helper()
	j := bytes.Index(data, []byte("mdat"))
	if j < 0 {
		t.Fatal("no mdat atom in output")
	}
	s := bytes.Index(data, []byte("stco"))
	if s < 0 {
		t.Fatal("no stco atom in output")
	}
	entry := int64(binary.BigEndian.Uint32(data[s+12 : s+16])) // name+verflags(4)+count(4)
	return int64(j + 4), entry
}

func TestMP4ParseBasicTags(t *testing.T) {
	data := mp4Tagged(
		mp4Text("\xa9nam", "Synth Title"),
		mp4Text("\xa9ART", "Synth Artist"),
		mp4Text("\xa9alb", "Synth Album"),
		mp4Pair("trkn", 3, 12),
	)
	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatMP4 {
		t.Fatalf("format = %v, want MP4", doc.Format())
	}
	f := doc.Fields()
	if f.Title != "Synth Title" {
		t.Errorf("Title = %q", f.Title)
	}
	if len(f.Artists) != 1 || f.Artists[0] != "Synth Artist" {
		t.Errorf("Artists = %v", f.Artists)
	}
	if f.Album != "Synth Album" {
		t.Errorf("Album = %q", f.Album)
	}
	if f.TrackNumber != 3 || f.TrackTotal != 12 {
		t.Errorf("track = %d/%d, want 3/12", f.TrackNumber, f.TrackTotal)
	}
	if tr := doc.Properties().First(); tr.Codec != "AAC" || tr.CodecProfile != "mp4a" {
		t.Errorf("codec = %q (profile %q), want AAC (profile mp4a)", tr.Codec, tr.CodecProfile)
	}
	if got := doc.Properties().First().SampleRate; got != 44100 {
		t.Errorf("sample rate = %d, want 44100", got)
	}
}

func TestMP4RewriteInPlaceKeepsOffsets(t *testing.T) {
	// A free atom adjacent to ilst gives slack, so editing the title to a value
	// that fits reuses the region: the mdat must not move (stco unchanged) and the
	// file size must stay the same.
	free := mp4Atom("free", make([]byte, 64))
	data := mp4Assemble(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "Original Title")), free)

	mdatBefore, stcoBefore := mp4Index(t, data)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if len(out) != len(data) {
		t.Errorf("in-place edit changed file size %d -> %d", len(data), len(out))
	}
	mdatAfter, stcoAfter := mp4Index(t, out)
	if stcoAfter != stcoBefore {
		t.Errorf("stco entry moved on in-place edit: %d -> %d", stcoBefore, stcoAfter)
	}
	if mdatAfter != mdatBefore {
		t.Errorf("mdat moved on in-place edit: %d -> %d", mdatBefore, mdatAfter)
	}
	if stcoAfter != mdatAfter {
		t.Errorf("stco entry %d does not point at mdat payload %d", stcoAfter, mdatAfter)
	}
	if got := mustParseBytes(t, out).Fields().Title; got != "New" {
		t.Errorf("title after edit = %q", got)
	}
}

func TestMP4GrowShiftsAllOffsets(t *testing.T) {
	// No free atom: a longer title forces the metadata to grow, the mdat to move,
	// and the stco entry to follow it. The audio bytes must survive verbatim at the
	// new offset, and the stco must point exactly at them.
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	mdatBefore, _ := mp4Index(t, data)

	longTitle := "A Substantially Longer Title That Cannot Fit In Place At All Here"
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, longTitle).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	mdatAfter, stcoAfter := mp4Index(t, out)
	if mdatAfter <= mdatBefore {
		t.Errorf("mdat should have moved later: %d -> %d", mdatBefore, mdatAfter)
	}
	if stcoAfter != mdatAfter {
		t.Errorf("stco entry %d must point at the moved mdat payload %d", stcoAfter, mdatAfter)
	}
	// The audio payload (120 bytes of 0xA7) must be intact at the new offset.
	if !bytes.Equal(out[mdatAfter:mdatAfter+120], bytes.Repeat([]byte{0xA7}, 120)) {
		t.Error("audio payload not preserved verbatim at the new mdat offset")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != longTitle {
		t.Errorf("title after grow = %q", re.Fields().Title)
	}
}

func TestMP4EssenceStableAcrossEdit(t *testing.T) {
	// The audio-essence digest hashes mdat content (not offsets), so it must be
	// identical before and after a metadata-growing edit.
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	before := essenceOf(t, data)
	plan, err := mustParseBytes(t, data).Edit().
		Set(tag.Title, "A Much Longer Title Forcing The Metadata To Grow Substantially").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if after := essenceOf(t, out); !after.Equal(before) {
		t.Error("audio essence digest changed across a metadata edit")
	}
}

func TestMP4PictureRoundTrip(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "Art"))
	plan, err := mustParseBytes(t, data).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	re := mustParseBytes(t, out)
	pics := re.Pictures()
	if len(pics) != 1 {
		t.Fatalf("expected 1 picture, got %d", len(pics))
	}
	if pics[0].MIME != "image/png" {
		t.Errorf("picture MIME = %q, want image/png", pics[0].MIME)
	}
	if !bytes.Equal(pics[0].Data, tinyPNG()) {
		t.Error("picture data not preserved")
	}
}

func TestMP4FreeformMusicBrainzRoundTrip(t *testing.T) {
	mbid := "11111111-2222-3333-4444-555555555555"
	data := mp4Tagged(mp4Freeform("com.apple.iTunes", "MusicBrainz Album Id", mbid))
	doc := mustParseBytes(t, data)
	if got := doc.Fields().MusicBrainz.ReleaseID; got != mbid {
		t.Errorf("MB release id = %q, want %q", got, mbid)
	}
	// A custom canonical key round-trips through a com.apple.iTunes freeform too.
	plan, err := doc.Edit().Set(tag.Key("MY_CUSTOM"), "custom-value").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if vs, ok := re.Get(tag.Key("MY_CUSTOM")); !ok || vs[0] != "custom-value" {
		t.Errorf("custom freeform key = %v (ok=%v)", vs, ok)
	}
	if re.Fields().MusicBrainz.ReleaseID != mbid {
		t.Error("MusicBrainz id lost on rewrite")
	}
}

func TestMP4PreservesUnknownAndForeignItems(t *testing.T) {
	// An unknown four-cc atom and a foreign-mean freeform are not canonically
	// owned; both must survive a tag edit byte-for-byte.
	unknown := mp4Atom("xxxx", mp4Data(1, []byte("opaque")))
	foreign := mp4Freeform("com.example.tool", "PrivateThing", "secret")
	data := mp4Tagged(mp4Text("\xa9nam", "T"), unknown, foreign)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Changed Title Longer").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("opaque")) {
		t.Error("unknown atom payload not preserved")
	}
	if !bytes.Contains(out, []byte("com.example.tool")) || !bytes.Contains(out, []byte("secret")) {
		t.Error("foreign-mean freeform not preserved")
	}
	if mustParseBytes(t, out).Fields().Title != "Changed Title Longer" {
		t.Error("edit did not apply")
	}
}

func TestMP4CraftedDataAtomSizeNoPanic(t *testing.T) {
	// An ilst item whose second "data" sub-atom declares a size near 2^31 after a
	// valid 16-byte one: on a 32-bit platform `pos+size` would overflow a signed
	// int, slip past the bounds check, and panic the slice. The codec must reject
	// the item (preserve it) without panicking on any architecture.
	emptyData := mp4Data(1, nil)                                  // a valid 16-byte data atom
	fakeHdr := slices.Concat(mp4be32(0x7FFFFFFF), []byte("data")) // size ~2^31, no body
	item := mp4Atom("\xa9nam", slices.Concat(emptyData, fakeHdr))
	data := mp4Tagged(item)

	doc := mustParseBytes(t, data) // must not panic
	if doc.Fields().Title != "" {
		t.Errorf("a malformed data atom should not project a title, got %q", doc.Fields().Title)
	}
}

func TestMP4BareMetaQuickTime(t *testing.T) {
	// QuickTime authors a bare meta (no FullBox version/flags prefix). The codec
	// must detect this and still find the ilst - otherwise the 4-byte skip
	// misaligns child parsing and the file reads as untagged (silent data loss).
	bare := mp4MetaBare(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "Bare Meta Title")))
	data := mp4AssembleUdta(bare)

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "Bare Meta Title" {
		t.Fatalf("bare-meta title = %q, want Bare Meta Title (silently read as untagged?)", doc.Fields().Title)
	}
	// An edit round-trips and the output re-reads correctly.
	plan, err := doc.Edit().Set(tag.Title, "Edited Bare").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if got := mustParseBytes(t, applyToBytes(t, data, plan)).Fields().Title; got != "Edited Bare" {
		t.Errorf("edited bare-meta title = %q", got)
	}
}

// TestMP4UdtaZeroTerminatorCreatesTag covers a udta that ends in a 32-bit-zero
// QuickTime terminator (which parse tolerates) and has no ilst: creating a tag
// must insert the new meta after the last real child, dropping the terminator -
// not after it, which would shift the meta out of alignment and corrupt every
// following atom on re-parse. (The degenerate 1-byte-zero-body form of this is a
// FuzzParse seed; this asserts the real-file scenario keeps the existing child.)
func TestMP4UdtaZeroTerminatorCreatesTag(t *testing.T) {
	child := mp4Atom("Xtra", []byte("XTRADATA")) // a real opaque udta child (preserved verbatim)
	data := mp4AssembleUdta(child, mp4be32(0))   //...then a 32-bit-zero QuickTime terminator
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "" {
		t.Fatalf("setup: unexpected title %q", doc.Fields().Title)
	}
	plan, err := doc.Edit().Set(tag.Title, "Added").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	re := mustParseBytes(t, out) // must re-parse cleanly: no misaligned atoms
	if re.Fields().Title != "Added" {
		t.Errorf("title after round-trip = %q, want Added", re.Fields().Title)
	}
	if !bytes.Contains(out, []byte("XTRADATA")) {
		t.Error("the preserved udta child atom was dropped by the tag insert")
	}
}

func TestMP4MultiMdatTrailingMoovFingerprinted(t *testing.T) {
	// Two mdats with the moov (tags) after the last one: the change-detection
	// fingerprint must hash the trailing moov, so two files differing only in a
	// tag (same size) get different fingerprints. Before the fix, an AudioRanges
	// essence skipped the tail and these collided.
	build := func(title string) []byte {
		udta := mp4Atom("udta", mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", title))))
		moov := mp4Moov(udta, 100)
		mdat1 := mp4Atom("mdat", bytes.Repeat([]byte{0x11}, 48))
		mdat2 := mp4Atom("mdat", bytes.Repeat([]byte{0x22}, 48))
		return slices.Concat(mp4Ftyp(), mdat1, mdat2, moov)
	}
	a, b := build("AAA"), build("BBB")
	if len(a) != len(b) {
		t.Fatalf("setup: expected equal length, got %d vs %d", len(a), len(b))
	}
	ida := mustParseBytes(t, a).Identity()
	idb := mustParseBytes(t, b).Identity()
	if !ida.HasFinger || !idb.HasFinger {
		t.Fatal("expected a structural fingerprint")
	}
	if ida.Fingerprint == idb.Fingerprint {
		t.Error("a tag after the last mdat was not covered by the fingerprint")
	}
}

func TestMP4FragmentedRejected(t *testing.T) {
	// A top-level moof marks a fragmented file, which is out of scope and must fail
	// loudly rather than corrupt offset tables.
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	data = append(data, mp4Atom("moof", make([]byte, 16))...)
	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Fatalf("fragmented parse error = %v, want ErrUnsupportedFormat", err)
	}
}

func TestMP4MalformedContainerTilingRejected(t *testing.T) {
	// A nested container's children must exactly tile it. Two fuzz-found shapes
	// broke the round-trip (parse clamped/ignored the defect, then a create/insert
	// rewrite copied the original bytes verbatim and emitted un-reparseable output),
	// so both are now rejected at parse. A truncated *top-level* final atom stays
	// tolerated - that recovery path is exercised by the real fixtures.
	ftyp := mp4Atom("ftyp", []byte("M4A \x00\x00\x00\x00isom"))
	cases := map[string][]byte{
		// trak inside moov declares 1000 bytes but only 16 are present (overrun).
		"nested overrun": mp4Atom("moov", slices.Concat(mp4be32(1000), []byte("trak"), make([]byte, 8))),
		// moov body is a 1-byte ragged tail that cannot be a complete atom.
		"ragged tail": mp4Atom("moov", []byte("0")),
	}
	for name, moov := range cases {
		file := slices.Concat(ftyp, moov)
		if _, err := wl.Parse(context.Background(), wl.BytesSource(file)); !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("%s: parse error = %v, want ErrInvalidData", name, err)
		}
	}
}

func TestMP4QuickTimeUdtaTerminatorAccepted(t *testing.T) {
	// QuickTime terminates a udta user-data list with a 32-bit zero. The
	// nested-tiling rejection must treat an all-zero remainder as benign so these
	// Apple-authored files still read (regression guard for that check).
	udta := mp4Atom("udta", append(mp4Text("\xa9nam", "X"), 0, 0, 0, 0))
	file := slices.Concat(mp4Atom("ftyp", []byte("M4A \x00\x00\x00\x00isom")), mp4Atom("moov", udta))
	if _, err := wl.Parse(context.Background(), wl.BytesSource(file)); err != nil {
		t.Fatalf("a udta with a QuickTime zero terminator should parse, got %v", err)
	}
}

func TestMP4PaddingFloorGrowsRegion(t *testing.T) {
	// --padding N is a floor, honored on the in-place reuse path too.
	// MP4 reuses the existing ilst+free region when the new content fits; without
	// the floor wired into that path, a large --padding over a small region was
	// silently ignored. Seed a 50 KB region, then a tiny edit under a 200 KB floor
	// must grow rather than reuse the smaller region.
	data := mp4Tagged(mp4Text("\xa9nam", "T"))

	// Set a different title so the seed actually rewrites (creating the 50 KB region);
	// setting it to its current value would be a no-op and leave the region untouched.
	seedPlan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Seeded").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 50000, Max: 1 << 20, ReuseInPlace: true}))
	if err != nil {
		t.Fatal(err)
	}
	seeded := applyToBytes(t, data, seedPlan)

	// A tiny edit that fits the 50 KB region, but a 200 KB floor: must grow.
	floorPlan, err := mustParseBytes(t, seeded).Edit().Set(tag.Title, "Hi").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 200000, Min: 200000, ReuseInPlace: true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := floorPlan.Report().PaddingAfter; got < 200000 {
		t.Errorf("PaddingAfter under a 200 KB floor = %d, want >= 200000 (reuse path ignored Min)", got)
	}

	// A region already past a small floor reuses in place (Min only floors a grow).
	reusePlan, err := mustParseBytes(t, seeded).Edit().Set(tag.Title, "Hi").
		Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 1000, Min: 1000, ReuseInPlace: true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := reusePlan.Report().PaddingAfter; got < 40000 {
		t.Errorf("PaddingAfter on reuse = %d, want the ~50 KB region reused, not shrunk", got)
	}
}

func TestMP4NoOpWritesVerbatim(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "Same"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Same").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Fatal("setting a tag to its current value should be a no-op")
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Equal(out, data) {
		t.Error("no-op write should reproduce the input byte-for-byte")
	}
}

func TestMP4CreateTagsOnUntaggedFile(t *testing.T) {
	// A file with no udta/meta/ilst: editing must create the whole tag path, shift
	// the mdat, and fix the stco - and read back correctly.
	mdatPayload := bytes.Repeat([]byte{0xA7}, 120)
	build := func(stcoOff uint32) []byte {
		return slices.Concat(mp4Ftyp(), mp4Moov(nil, stcoOff), mp4Atom("mdat", mdatPayload))
	}
	tmp := build(0)
	data := build(uint32(bytes.Index(tmp, []byte("mdat")) + 4))

	doc := mustParseBytes(t, data)
	if doc.Tags().Len() != 0 {
		t.Fatalf("expected an untagged file, got %d tags", doc.Tags().Len())
	}
	plan, err := doc.Edit().Set(tag.Title, "Created").Set(tag.Artist, "Maker").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	mdatAfter, stcoAfter := mp4Index(t, out)
	if stcoAfter != mdatAfter {
		t.Errorf("stco entry %d must point at mdat payload %d after creating tags", stcoAfter, mdatAfter)
	}
	if !bytes.Equal(out[mdatAfter:mdatAfter+120], mdatPayload) {
		t.Error("audio payload not preserved when creating tags")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Created" || len(re.Fields().Artists) != 1 || re.Fields().Artists[0] != "Maker" {
		t.Errorf("created tags read back wrong: title=%q artists=%v", re.Fields().Title, re.Fields().Artists)
	}
}

func TestMP4CreateMetaInExistingUdta(t *testing.T) {
	// A file with a udta (holding only a chpl) but no meta/ilst: editing must
	// create meta+ilst inside the existing udta, preserve the chpl, fix the stco,
	// and read back - exercising the create-in-udta path and its result rebuild.
	chpl := mp4Atom("chpl", slices.Concat([]byte{1, 0, 0, 0}, make([]byte, 4), []byte{1},
		make([]byte, 8), []byte{3}, []byte("One")))
	data := mp4AssembleUdta(chpl)

	doc := mustParseBytes(t, data)
	if doc.Tags().Len() != 0 {
		t.Fatalf("expected no tags, got %d", doc.Tags().Len())
	}
	plan, err := doc.Edit().Set(tag.Title, "Made In Udta").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	mdatAfter, stcoAfter := mp4Index(t, out)
	if stcoAfter != mdatAfter {
		t.Errorf("stco entry %d must point at mdat payload %d", stcoAfter, mdatAfter)
	}
	if !bytes.Contains(out, []byte("One")) {
		t.Error("existing chpl lost when creating meta in udta")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Made In Udta" {
		t.Errorf("created title = %q", re.Fields().Title)
	}
	// The result document must be re-editable without reparse (offsets reconstructed).
	plan2, err := re.Edit().Set(tag.Artist, "Second Pass").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, out, plan2)
	if got := mustParseBytes(t, out2).Fields().Artists; len(got) != 1 || got[0] != "Second Pass" {
		t.Errorf("second-pass artist = %v", got)
	}
}

func TestMP4VerifyEssenceOnGrow(t *testing.T) {
	// A faststart file (moov before mdat) grown so the mdat moves: WithVerifyEssence
	// re-hashes the written output's mdat and compares it to the source, so it fails
	// loudly if buildResult shifted the essence range wrong. The save must succeed.
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	path := writeTempFile(t, "verify.m4a", data)
	dst := path + ".out.m4a"

	plan, err := mustParseFile(t, path).Edit().
		Set(tag.Title, "A Long Enough Title To Force The Metadata Region To Grow And Move mdat").
		Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := plan.Execute(context.Background(), wl.SaveAsFile(dst))
	if err != nil {
		t.Fatalf("verified save failed (essence-range bug?): %v", err)
	}
	if res.Fields().Title == "" {
		t.Error("result document missing title")
	}
}

func TestMP4InheritedEncoderWarned(t *testing.T) {
	// ffmpeg stamps "Lavf..." into the \xa9too encoder atom on transcoded files; it
	// must surface as an inherited-encoder warning, the MP4 analogue of ID3 TSSE.
	data := mp4Tagged(mp4Text("\xa9nam", "T"), mp4Text("\xa9too", "Lavf61.7.100"))
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Errorf("expected an inherited-encoder warning; got %v", doc.Warnings())
	}
}

func TestMP4CompilationAndDiscRoundTrip(t *testing.T) {
	// cpil (boolean) and disk (a no-trailing pair) round-trip through the codec.
	data := mp4Tagged(
		mp4Atom("cpil", mp4Data(21, []byte{1})),
		mp4Atom("disk", mp4Data(0, []byte{0, 0, 0, 1, 0, 2})), // disc 1 of 2 (6-byte value)
	)
	doc := mustParseBytes(t, data)
	if !doc.Fields().Compilation {
		t.Error("cpil did not read as Compilation=true")
	}
	if doc.Fields().DiscNumber != 1 || doc.Fields().DiscTotal != 2 {
		t.Errorf("disk = %d/%d, want 1/2", doc.Fields().DiscNumber, doc.Fields().DiscTotal)
	}
	// Edit something else and confirm cpil/disk survive the rewrite.
	plan, err := doc.Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if !re.Fields().Compilation || re.Fields().DiscNumber != 1 || re.Fields().DiscTotal != 2 {
		t.Errorf("cpil/disk lost on rewrite: comp=%v disc=%d/%d",
			re.Fields().Compilation, re.Fields().DiscNumber, re.Fields().DiscTotal)
	}
}

func TestMP4SlashAndSpacedTrackNumber(t *testing.T) {
	// A track number set directly as a slash-combined "5/9" (or with stray spaces)
	// must still write a valid trkn rather than being dropped to zero.
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	plan, err := mustParseBytes(t, data).Edit().
		Set(tag.TrackNumber, "5/9").
		Set(tag.DiscNumber, " 2 ").
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if re.Fields().TrackNumber != 5 || re.Fields().TrackTotal != 9 {
		t.Errorf("slash track = %d/%d, want 5/9", re.Fields().TrackNumber, re.Fields().TrackTotal)
	}
	if re.Fields().DiscNumber != 2 {
		t.Errorf("spaced disc number = %d, want 2", re.Fields().DiscNumber)
	}
}

func TestMP4NativeViewAndCapabilities(t *testing.T) {
	chpl := mp4Atom("chpl", slices.Concat([]byte{1, 0, 0, 0}, make([]byte, 4), []byte{1},
		make([]byte, 8), []byte{5}, []byte("Intro")))
	data := mp4AssembleUdta(mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))), chpl)

	doc := mustParseBytes(t, data)
	kinds := map[string]bool{}
	for _, e := range doc.Native().Describe() {
		kinds[e.Kind] = true
	}
	for _, want := range []string{"ftyp", "moov", "mdat", "moov.udta.meta.ilst", "moov.udta.chpl"} {
		if !kinds[want] {
			t.Errorf("native view missing %q; got %v", want, kinds)
		}
	}

	caps := doc.Capabilities()
	if caps.Format != wl.FormatMP4 || caps.ReadOnly {
		t.Errorf("MP4 caps = %+v, want writable MP4", caps)
	}
	if caps.Field(tag.Title).Write != wl.AccessFull {
		t.Error("MP4 should fully support writing Title")
	}
	// Picture write is Full: the image set carries byte-for-byte. MP4 drops a picture's
	// role/description, but that per-picture loss is surfaced by the plan warning, not
	// the coarse transfer level (which would mislabel a lossless front-cover copy).
	if caps.Pictures.Write != wl.AccessFull {
		t.Error("MP4 picture write should be AccessFull (image bytes carry losslessly)")
	}
}

func TestMP4ChplPreservedThroughEdit(t *testing.T) {
	// A Nero chapter list (moov.udta.chpl) is not editable but must survive a tag
	// edit byte-for-byte and stay reported in the native view.
	chplPayload := slices.Concat([]byte{1, 0, 0, 0}, make([]byte, 4), []byte{1},
		make([]byte, 8), []byte{12}, []byte("Chapter One!"))
	chpl := mp4Atom("chpl", chplPayload)
	data := mp4AssembleUdta(mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))), chpl)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "A Longer Title Forcing A Grow Here").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("Chapter One!")) {
		t.Error("chpl chapter data was not preserved through a tag edit")
	}
	hasChpl := false
	for _, e := range mustParseBytes(t, out).Native().Describe() {
		if e.Kind == "moov.udta.chpl" {
			hasChpl = true
		}
	}
	if !hasChpl {
		t.Error("chpl missing from the native view after an edit")
	}
}

func TestMP4GenreConflictSurfaced(t *testing.T) {
	// A legacy numeric gnre and a text \xa9gen that disagree both project to Genre;
	// the family view must flag the conflict (an unselected entry).
	gnre := mp4Atom("gnre", mp4Data(0, []byte{0, 18})) // numeric -> a name
	data := mp4Tagged(gnre, mp4Text("\xa9gen", "Synthpop"))
	doc := mustParseBytes(t, data)
	conflict := false
	for _, f := range doc.Families() {
		if f.Key == tag.Genre && !f.Selected {
			conflict = true
		}
	}
	if !conflict {
		t.Errorf("expected an unselected Genre family entry; families = %+v", doc.Families())
	}
}

func TestMP4GnreResolvedToName(t *testing.T) {
	// The legacy numeric "gnre" atom (1-based) resolves to a genre name and warns.
	gnre := mp4Atom("gnre", mp4Data(0, []byte{0, 18})) // 18 -> index 17 -> "Rock" era list
	data := mp4Tagged(gnre)
	doc := mustParseBytes(t, data)
	if len(doc.Fields().Genres) != 1 || doc.Fields().Genres[0] == "" {
		t.Errorf("gnre did not resolve to a genre name: %v", doc.Fields().Genres)
	}
	if !hasWarning(doc, wl.WarnNumericGenre) {
		t.Errorf("expected a numeric-genre warning; got %v", doc.Warnings())
	}
}

// TestMP4TruncatedMdatWarns covers the truncation signal for MP4: an mdat atom
// whose declared size runs past EOF is flagged, while an intact file is not.
func TestMP4TruncatedMdatWarns(t *testing.T) {
	t.Run("declared overruns file", func(t *testing.T) {
		data := mp4Tagged(mp4Text("\xa9nam", "Title"))
		j := bytes.Index(data, []byte("mdat"))
		if j < 4 {
			t.Fatal("no mdat atom in synthetic file")
		}
		// Inflate the mdat atom's declared size so it overruns EOF (a truncated
		// download): the payload bytes are unchanged, only the size header lies.
		binary.BigEndian.PutUint32(data[j-4:j], binary.BigEndian.Uint32(data[j-4:j])+5000)
		doc := mustParseBytes(t, data)
		if !hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("expected truncated-audio warning; got %v", doc.Warnings())
		}
	})
	t.Run("intact file not flagged", func(t *testing.T) {
		data := mp4Tagged(mp4Text("\xa9nam", "Title"))
		if doc := mustParseBytes(t, data); hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("an intact MP4 must not be flagged truncated; got %v", doc.Warnings())
		}
	})
	t.Run("64-bit mdat near 2^63 does not overflow detection", func(t *testing.T) {
		// A 64-bit mdat declaring ~2^63 bytes: forming offset+declaredSize overflows
		// int64 to negative and silently suppressed the warning. Detecting at the walk's
		// clamp (a comparison, no addition) is overflow-safe, so it is still flagged.
		mdat64 := func(payload []byte) []byte {
			b := append([]byte{0, 0, 0, 1}, []byte("mdat")...) // size==1 => 64-bit size follows
			sz := make([]byte, 8)
			binary.BigEndian.PutUint64(sz, 0x7FFFFFFFFFFFFFFF) // max int64; offset+size overflows
			return append(append(b, sz...), payload...)
		}
		// Point the chunk-offset table at the mdat payload (past the 16-byte 64-bit
		// header) so the mdat is real essence, not an unreferenced range the digest trim
		// would drop. The moov's byte length is independent of the offset value it carries,
		// so measuring it with a placeholder gives the final payload offset.
		udta := mp4Atom("udta", nil)
		ftyp := mp4Ftyp()
		chunkOff := uint32(len(ftyp) + len(mp4Moov(udta, 0)) + 16)
		data := slices.Concat(ftyp, mp4Moov(udta, chunkOff), mdat64(bytes.Repeat([]byte{0xA7}, 64)))
		doc := mustParseBytes(t, data)
		if doc.Format() != wl.FormatMP4 {
			t.Fatalf("format = %v, want MP4", doc.Format())
		}
		if !hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("expected truncated-audio on a 64-bit overflowing mdat; got %v", doc.Warnings())
		}
	})
}
