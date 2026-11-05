package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Synthetic MP4/iTunes builders. The mdat payload is filler bytes; tests assert on the atom
// structure, tag round-trips, and chunk-offset fixups, not on decoded audio.

// mp4Freeform builds a "----" com.apple.iTunes freeform item.
func mp4Freeform(meanName, name, value string) []byte {
	mean := testfile.MP4Atom("mean", append([]byte{0, 0, 0, 0}, meanName...))
	nm := testfile.MP4Atom("name", append([]byte{0, 0, 0, 0}, name...))
	return testfile.MP4Atom("----", slices.Concat(mean, nm, testfile.MP4Data(1, []byte(value))))
}

// mp4Pair builds a trkn/disk number/total atom (8-byte value with trailing pad).
func mp4Pair(name string, num, total int) []byte {
	v := make([]byte, 8)
	binary.BigEndian.PutUint16(v[2:4], uint16(num))
	binary.BigEndian.PutUint16(v[4:6], uint16(total))
	return testfile.MP4Atom(name, testfile.MP4Data(0, v))
}

// mp4MetaBare builds a QuickTime-style meta with no FullBox version/flags prefix
// (its children start immediately).
func mp4MetaBare(children ...[]byte) []byte {
	return testfile.MP4Atom("meta", slices.Concat(children...))
}

// mp4HdlrMdta builds the metadata handler an ffmpeg "+use_metadata_tags" file carries: the
// ilst items under it are keyed by index into a sibling "keys" box, not by four-cc name.
func mp4HdlrMdta() []byte {
	return testfile.MP4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("mdta"), make([]byte, 12)))
}

// mp4Keys builds a "keys" box indexing the given key names under the "mdta" namespace. Entry i is
// ilst index i+1.
func mp4Keys(names ...string) []byte {
	body := slices.Concat([]byte{0, 0, 0, 0}, testfile.BE32(len(names)))
	for _, n := range names {
		body = slices.Concat(body, testfile.BE32(8+len(n)), []byte("mdta"), []byte(n))
	}
	return testfile.MP4Atom("keys", body)
}

// mp4KeyItem builds an mdta ilst item keyed by a 1-based keys index: its four-byte atom
// name is the index itself.
func mp4KeyItem(index int, value string) []byte {
	return slices.Concat(testfile.BE32(8+len(testfile.MP4Data(1, []byte(value)))), testfile.BE32(index), testfile.MP4Data(1, []byte(value)))
}

// mp4KeyItemData builds an mdta ilst item from pre-rendered data sub-atoms (cover art, say).
func mp4KeyItemData(index int, data []byte) []byte {
	return slices.Concat(testfile.BE32(8+len(data)), testfile.BE32(index), data)
}

// mp4QTTextEntry builds one [uint16 size][uint16 language]<text> entry of a classic QuickTime udta
// text atom.
func mp4QTTextEntry(lang uint16, text string) []byte {
	h := make([]byte, 4)
	binary.BigEndian.PutUint16(h[0:2], uint16(len(text)))
	binary.BigEndian.PutUint16(h[2:4], lang)
	return append(h, text...)
}

// mp4UdtaText builds a classic QuickTime udta-level text atom from its entries.
func mp4UdtaText(name string, entries ...[]byte) []byte {
	return testfile.MP4Atom(name, slices.Concat(entries...))
}

// mp4StsdEntryV1 builds a QuickTime version 1 sound sample entry: the fixed v0 fields followed by
// the four samples- and bytes-per-packet words that push the extension boxes 16 bytes further out.
func mp4StsdEntryV1(fourcc string, channels, sampleSize, rate int, children ...[]byte) []byte {
	rateField := 0
	if rate > 0 && rate <= 0xFFFF {
		rateField = rate << 16
	}
	return testfile.MP4Atom(fourcc, slices.Concat(
		make([]byte, 6), []byte{0, 1}, // reserved + data_ref_index
		testfile.BE16(1), testfile.BE16(0), testfile.BE32(0), // version 1, revision, vendor
		testfile.BE16(channels), testfile.BE16(sampleSize), []byte{0, 0, 0, 0}, // predefined + reserved
		testfile.BE32(rateField),
		testfile.BE32(1024), testfile.BE32(0), testfile.BE32(0), testfile.BE32(2), // samples/bytes per packet, frame, sample
		slices.Concat(children...),
	))
}

// mp4StsdEntryV2 builds a QuickTime version 2 sound sample entry with no format-specific
// flags, the shape every caller but the float-lpcm test wants.
func mp4StsdEntryV2(fourcc string, rate float64, channels, bits int, children ...[]byte) []byte {
	return mp4StsdEntryV2Flags(fourcc, rate, channels, bits, 0, children...)
}

// mp4StsdEntryV2Flags builds a QuickTime version 2 sound sample entry: the fixed v0 fields carry
// their required constants and the real geometry sits in the v2 struct, whose formatSpecificFlags
// word is the only place a float lpcm stream declares itself (bit 0 is kAudioFormatFlagIsFloat).
func mp4StsdEntryV2Flags(fourcc string, rate float64, channels, bits, flags int, children ...[]byte) []byte {
	rateBits := make([]byte, 8)
	binary.BigEndian.PutUint64(rateBits, math.Float64bits(rate))
	return testfile.MP4Atom(fourcc, slices.Concat(
		make([]byte, 6), []byte{0, 1}, // reserved + data_ref_index
		testfile.BE16(2), testfile.BE16(0), testfile.BE32(0), // version 2, revision, vendor
		testfile.BE16(3), testfile.BE16(16), testfile.BE16(0xFFFE), testfile.BE16(0), // the required v2 constants
		testfile.BE32(1<<16), // always 65536
		testfile.BE32(72),    // sizeOfStructOnly
		rateBits,             // audioSampleRate
		testfile.BE32(channels),
		testfile.BE32(0x7F000000),
		testfile.BE32(bits),  // constBitsPerChannel
		testfile.BE32(flags), // formatSpecificFlags
		testfile.BE32(0),     // constBytesPerAudioPacket
		testfile.BE32(0),     // constLPCMFramesPerAudioPacket
		slices.Concat(children...),
	))
}

// mp4PcmC builds the ISOBMFF PCM configuration box an ipcm or fpcm entry carries: a
// FullBox holding a format_flags byte (0 = big-endian) and the PCM sample size.
func mp4PcmC(size byte) []byte {
	return testfile.MP4Atom("pcmC", []byte{0, 0, 0, 0, 0, size})
}

// mp4Esds builds the esds box an AAC sample entry carries, wrapping the AudioSpecificConfig in the
// ES / DecoderConfig / DecoderSpecificInfo descriptor nest with objectTypeIndication 0x40 (MPEG-4
// audio). Descriptor lengths use the four-byte 80 80 80 xx form ffmpeg writes.
func mp4Esds(asc []byte) []byte {
	return testfile.MP4EsdsRaw(0x40, asc, mp4DescrLong)
}

// mp4DescrLong encodes a descriptor with the padded four-byte expandable length ffmpeg
// writes: three continuation bytes then the low seven bits.
func mp4DescrLong(tag byte, body []byte) []byte {
	n := len(body)
	return slices.Concat([]byte{tag, byte(n>>21) | 0x80, byte(n>>14) | 0x80, byte(n>>7) | 0x80, byte(n) & 0x7F}, body)
}

// mp4Wave builds the QuickTime wave wrapper a version 1 or 2 sound entry nests its codec
// configuration in, terminated by the 8-byte zero atom QuickTime appends.
func mp4Wave(children ...[]byte) []byte {
	return testfile.MP4Atom("wave", slices.Concat(slices.Concat(children...), make([]byte, 8)))
}

// mp4StreamInfo builds a 34-byte FLAC STREAMINFO body.
func mp4StreamInfo(rate, channels, depth, minBlock, maxBlock int, total uint64) []byte {
	body := make([]byte, 34)
	binary.BigEndian.PutUint16(body[0:2], uint16(minBlock))
	binary.BigEndian.PutUint16(body[2:4], uint16(maxBlock))
	body[10] = byte(rate >> 12)
	body[11] = byte(rate >> 4)
	body[12] = byte(rate<<4) | byte(channels-1)<<1 | byte((depth-1)>>4)
	body[13] = byte((depth-1)&0x0F)<<4 | byte(total>>32)&0x0F
	binary.BigEndian.PutUint32(body[14:18], uint32(total))
	return body
}

// mp4DfLa builds the dfLa box a FLAC-in-MP4 sample entry carries, holding one STREAMINFO
// metadata block.
func mp4DfLa(streamInfo []byte) []byte {
	hdr := []byte{0x80, byte(len(streamInfo) >> 16), byte(len(streamInfo) >> 8), byte(len(streamInfo))}
	return testfile.MP4Atom("dfLa", slices.Concat([]byte{0, 0, 0, 0}, hdr, streamInfo))
}

// mp4Moov assembles the movie box: one audio trak (with a one-entry stco) and the
// given udta. The stco entry points at stcoOff.
func mp4Moov(udta []byte, stcoOff uint32) []byte {
	return mp4MoovExtra(udta, stcoOff, nil, nil)
}

// mp4MoovExtra is mp4Moov with two extension points: stblExtra is appended to the audio track's
// stbl (beside the stco, where a saio belongs), and moovExtra to the moov itself (an mvex).
func mp4MoovExtra(udta []byte, stcoOff uint32, stblExtra, moovExtra []byte) []byte {
	return testfile.MP4MoovStsd(udta, stcoOff, stblExtra, moovExtra, testfile.MP4StsdAudio(), 44100)
}

// mp4Mvex builds an mvex declaring one trex: the forward declaration a muxer emits when fragments
// MAY follow.
func mp4Mvex() []byte {
	trex := testfile.MP4Atom("trex", slices.Concat([]byte{0, 0, 0, 0}, testfile.BE32(1), make([]byte, 16)))
	return testfile.MP4Atom("mvex", trex)
}

// mp4Saio builds a saio sample-auxiliary-offset box (ISO/IEC 14496-12 8.7.13): version 0
// gives 32-bit entries and version 1 gives 64-bit, and auxType sets flags&1, which
// inserts the aux_info_type + aux_info_type_parameter pair ahead of the entry count.
func mp4Saio(version uint8, auxType bool, offsets ...uint32) []byte {
	var flags byte
	if auxType {
		flags = 1
	}
	body := []byte{version, 0, 0, flags}
	if auxType {
		body = append(body, []byte("cenc")...)   // aux_info_type
		body = append(body, testfile.BE32(0)...) // aux_info_type_parameter
	}
	body = append(body, testfile.BE32(len(offsets))...)
	for _, o := range offsets {
		if version == 1 {
			b := make([]byte, 8)
			binary.BigEndian.PutUint64(b, uint64(o))
			body = append(body, b...)
			continue
		}
		body = append(body, testfile.BE32(int(o))...)
	}
	return testfile.MP4Atom("saio", body)
}

// mp4BoxPayload returns the payload of the first real box with the given four-cc: a name match
// whose preceding 4-byte size field describes a box that fits the file.
func mp4BoxPayload(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	for i := 0; i < len(data); {
		j := bytes.Index(data[i:], []byte(name))
		if j < 0 {
			break
		}
		at := i + j
		if at >= 4 {
			size := int(binary.BigEndian.Uint32(data[at-4 : at]))
			if size >= 8 && at-4+size <= len(data) {
				return data[at+4 : at-4+size]
			}
		}
		i = at + 1
	}
	t.Fatalf("no %s box in output", name)
	return nil
}

// mp4SaioEntries decodes the offsets of the first saio in data and reports the entry
// width it used, so a test can pin that a version 1 table stayed 64-bit across a rewrite.
func mp4SaioEntries(t *testing.T, data []byte) (offsets []uint64, width int) {
	t.Helper()
	body := mp4BoxPayload(t, data, "saio")
	if len(body) < 8 {
		t.Fatalf("saio payload is %d bytes, too short to decode", len(body))
	}
	prefix := 0
	if body[3]&1 != 0 {
		prefix = 8
	}
	width = 4
	if body[0] == 1 {
		width = 8
	}
	if len(body) < 8+prefix {
		t.Fatalf("saio payload is %d bytes, too short for its %d-byte aux-type prefix", len(body), prefix)
	}
	count := int(binary.BigEndian.Uint32(body[4+prefix : 8+prefix]))
	if len(body) < 8+prefix+count*width {
		t.Fatalf("saio declares %d entries but its payload is only %d bytes", count, len(body))
	}
	for i := range count {
		off := 8 + prefix + i*width
		if width == 8 {
			offsets = append(offsets, binary.BigEndian.Uint64(body[off:off+8]))
			continue
		}
		offsets = append(offsets, uint64(binary.BigEndian.Uint32(body[off:off+4])))
	}
	return offsets, width
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
	data := testfile.MP4Tagged(
		testfile.MP4Text("\xa9nam", "Synth Title"),
		testfile.MP4Text("\xa9ART", "Synth Artist"),
		testfile.MP4Text("\xa9alb", "Synth Album"),
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
	// No esds on the entry, so there is no object type to name and the four-cc stands as the profile.
	if tr := doc.Properties().First(); tr.Codec != "AAC" || tr.CodecProfile != "mp4a" {
		t.Errorf("codec = %q (profile %q), want AAC (profile mp4a)", tr.Codec, tr.CodecProfile)
	}
	if got := doc.Properties().First().SampleRate; got != 44100 {
		t.Errorf("sample rate = %d, want 44100", got)
	}
}

func TestMP4RewriteInPlaceKeepsOffsets(t *testing.T) {
	// A free atom adjacent to ilst gives slack, so editing the title to a value that fits reuses the
	// region: the mdat must not move (stco unchanged) and the file size must stay the same.
	free := testfile.MP4Atom("free", make([]byte, 64))
	data := testfile.MP4Assemble(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Original Title")), free)

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

func TestMP4RewriteReusesSkipPadding(t *testing.T) {
	// A "skip" atom is spec padding like "free" (the native view marks both). An edit that grows
	// the ilst past its original size but still fits within the adjacent skip slack must reuse the
	// region in place: the mdat must not move (stco unchanged) and the file size must stay the same.
	skip := testfile.MP4Atom("skip", make([]byte, 64))
	data := testfile.MP4Assemble(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "T")), skip)

	mdatBefore, stcoBefore := mp4Index(t, data)
	// Longer than "T" (so the ilst grows past its original extent) but well within the 64-byte
	// skip body, so a codec that reuses skip padding keeps everything in place.
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "A Longer Title That Fits In Skip").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if len(out) != len(data) {
		t.Errorf("grow-into-skip edit changed file size %d -> %d (skip padding not reused)", len(data), len(out))
	}
	mdatAfter, stcoAfter := mp4Index(t, out)
	if stcoAfter != stcoBefore {
		t.Errorf("stco entry moved on a skip-reuse edit: %d -> %d", stcoBefore, stcoAfter)
	}
	if mdatAfter != mdatBefore {
		t.Errorf("mdat moved on a skip-reuse edit: %d -> %d", mdatBefore, mdatAfter)
	}
	if got := mustParseBytes(t, out).Fields().Title; got != "A Longer Title That Fits In Skip" {
		t.Errorf("title after edit = %q", got)
	}
}

func TestMP4GrowShiftsAllOffsets(t *testing.T) {
	// No free atom: a longer title forces the metadata to grow, the mdat to move,
	// and the stco entry to follow it. The audio bytes must survive verbatim at the
	// new offset, and the stco must point exactly at them.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
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
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
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
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "Art"))
	plan, err := mustParseBytes(t, data).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
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
	if !bytes.Equal(pics[0].Data, testfile.PNG()) {
		t.Error("picture data not preserved")
	}
}

func TestMP4FreeformMusicBrainzRoundTrip(t *testing.T) {
	mbid := "11111111-2222-3333-4444-555555555555"
	data := testfile.MP4Tagged(mp4Freeform("com.apple.iTunes", "MusicBrainz Album Id", mbid))
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
	unknown := testfile.MP4Atom("xxxx", testfile.MP4Data(1, []byte("opaque")))
	foreign := mp4Freeform("com.example.tool", "PrivateThing", "secret")
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), unknown, foreign)

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
	// An ilst item whose second "data" sub-atom declares a size near 2^31 after a valid 16-byte one: on
	// a 32-bit platform `pos+size` would overflow a signed int, slip past the bounds check, and panic
	// the slice. The codec must reject the item (preserve it) without panicking on any architecture.
	emptyData := testfile.MP4Data(1, nil)                               // a valid 16-byte data atom
	fakeHdr := slices.Concat(testfile.BE32(0x7FFFFFFF), []byte("data")) // size ~2^31, no body
	item := testfile.MP4Atom("\xa9nam", slices.Concat(emptyData, fakeHdr))
	data := testfile.MP4Tagged(item)

	doc := mustParseBytes(t, data) // must not panic
	if doc.Fields().Title != "" {
		t.Errorf("a malformed data atom should not project a title, got %q", doc.Fields().Title)
	}
}

func TestMP4BareMetaQuickTime(t *testing.T) {
	// QuickTime authors a bare meta (no FullBox version/flags prefix). The codec must detect this
	// and still find the ilst; otherwise the 4-byte skip misaligns child parsing and the file reads
	// as untagged.
	bare := mp4MetaBare(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Bare Meta Title")))
	data := testfile.MP4AssembleUdta(bare)

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

// A udta that ends in a 32-bit-zero QuickTime terminator (which parse tolerates) and has no
// ilst: creating a tag must insert the new meta after the last real child and drop the
// terminator. Inserting after it would misalign the meta and corrupt every following atom.
func TestMP4UdtaZeroTerminatorCreatesTag(t *testing.T) {
	child := testfile.MP4Atom("Xtra", []byte("XTRADATA"))     // a real opaque udta child (preserved verbatim)
	data := testfile.MP4AssembleUdta(child, testfile.BE32(0)) //...then a 32-bit-zero QuickTime terminator
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
	// Two mdats with the moov (tags) after the last one: the change-detection fingerprint must
	// hash the trailing moov, so two files differing only in a tag (same size) get different
	// fingerprints.
	build := func(title string) []byte {
		udta := testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", title))))
		moov := mp4Moov(udta, 100)
		mdat1 := testfile.MP4Atom("mdat", bytes.Repeat([]byte{0x11}, 48))
		mdat2 := testfile.MP4Atom("mdat", bytes.Repeat([]byte{0x22}, 48))
		return slices.Concat(testfile.MP4Ftyp(), mdat1, mdat2, moov)
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

func TestMP4MalformedTextAtomNotDuplicatedOnEdit(t *testing.T) {
	// A known atom whose content does not decode cleanly (here an invalid-UTF-8 \xa9nam) is preserved
	// verbatim, but buildItems still re-emits the fresh canonical atom of the same type.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "\xff\xfe mojibake")) // invalid UTF-8: preserved, not owned
	if got := mustParseBytes(t, data).Fields().Title; got != "" {
		t.Fatalf("setup: a malformed \\xa9nam should not project a title, got %q", got)
	}
	before := essenceOf(t, data)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Fresh Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if got := bytes.Count(out, []byte("\xa9nam")); got != 1 {
		t.Errorf("output carries %d \\xa9nam atoms, want exactly 1 (malformed duplicate not dropped)", got)
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Fresh Title" {
		t.Errorf("title read back = %q, want the fresh value (stale mojibake preserved instead?)", re.Fields().Title)
	}
	if !essenceOf(t, out).Equal(before) {
		t.Error("audio essence changed across the edit")
	}
}

func TestMP4MalformedCoverNotDuplicatedOnPictureEdit(t *testing.T) {
	// A covr whose data atoms do not parse is preserved verbatim; changing the picture set
	// re-emits a fresh covr. The malformed duplicate must be dropped so a conformant single covr
	// remains, holding the new image.
	badCovr := testfile.MP4Atom("covr", []byte("not a data atom"))
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), badCovr)
	if got := mustParseBytes(t, data).Pictures(); len(got) != 0 {
		t.Fatalf("setup: a malformed covr should project no picture, got %d", len(got))
	}

	plan, err := mustParseBytes(t, data).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if got := bytes.Count(out, []byte("covr")); got != 1 {
		t.Errorf("output carries %d covr atoms, want exactly 1 (malformed duplicate not dropped)", got)
	}
	re := mustParseBytes(t, out)
	pics := re.Pictures()
	if len(pics) != 1 || pics[0].MIME != "image/png" || !bytes.Equal(pics[0].Data, testfile.PNG()) {
		t.Errorf("picture after edit = %+v, want the single fresh PNG", pics)
	}
}

func TestMP4ForeignFreeformSurvivesCanonicalFreeformEdit(t *testing.T) {
	// A com.apple.iTunes freeform with a mixed-case internal name (iTunNORM) is preserved
	// verbatim. An edit that authors a custom canonical key emits its own "----" atom, so
	// de-duplication must key freeforms by mean+name, not the shared "----" fourcc.
	foreign := mp4Freeform("com.apple.iTunes", "iTunNORM", "00000123 00000456")
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), foreign)

	plan, err := mustParseBytes(t, data).Edit().Set(tag.Key("MY_CUSTOM"), "custom-value").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if !bytes.Contains(out, []byte("iTunNORM")) {
		t.Error("foreign iTunNORM freeform dropped by the canonical-freeform edit")
	}
	re := mustParseBytes(t, out)
	if vs, ok := re.Get(tag.Key("MY_CUSTOM")); !ok || len(vs) != 1 || vs[0] != "custom-value" {
		t.Errorf("custom freeform key = %v (ok=%v), want [custom-value]", vs, ok)
	}
}

func TestMP4FragmentedReadNotWritten(t *testing.T) {
	// A top-level moof marks a fragmented file.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	data = append(data, testfile.MP4Atom("moof", make([]byte, 16))...)
	doc := mustParseBytes(t, data)
	if got := doc.Fields().Title; got != "T" {
		t.Errorf("Title = %q, want T", got)
	}
	if !hasWarning(doc, wl.WarnFragmented) {
		t.Errorf("expected a fragmented warning; got %v", doc.Warnings())
	}
	if _, err := doc.Edit().Set(tag.Title, "Edited").Prepare(); !errors.Is(err, waxerr.ErrFragmented) {
		t.Fatalf("fragmented write error = %v, want ErrFragmented", err)
	}
}

func TestMP4MalformedContainerTilingRejected(t *testing.T) {
	// A nested container's children must exactly tile it. Both shapes are rejected at parse: a
	// rewrite that copied the defect verbatim would emit un-reparseable output.
	ftyp := testfile.MP4Atom("ftyp", []byte("M4A \x00\x00\x00\x00isom"))
	cases := map[string][]byte{
		// trak inside moov declares 1000 bytes but only 16 are present (overrun).
		"nested overrun": testfile.MP4Atom("moov", slices.Concat(testfile.BE32(1000), []byte("trak"), make([]byte, 8))),
		// moov body is a 1-byte ragged tail that cannot be a complete atom.
		"ragged tail": testfile.MP4Atom("moov", []byte("0")),
	}
	for name, moov := range cases {
		file := slices.Concat(ftyp, moov)
		if _, err := wl.Parse(context.Background(), wl.BytesSource(file)); !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("%s: parse error = %v, want ErrInvalidData", name, err)
		}
	}
}

func TestMP4QuickTimeUdtaTerminatorAccepted(t *testing.T) {
	// QuickTime terminates a udta user-data list with a 32-bit zero. The nested-tiling
	// rejection must treat an all-zero remainder as benign so these Apple-authored files
	// still read.
	udta := testfile.MP4Atom("udta", append(testfile.MP4Text("\xa9nam", "X"), 0, 0, 0, 0))
	file := slices.Concat(testfile.MP4Atom("ftyp", []byte("M4A \x00\x00\x00\x00isom")), testfile.MP4Atom("moov", udta))
	if _, err := wl.Parse(context.Background(), wl.BytesSource(file)); err != nil {
		t.Fatalf("a udta with a QuickTime zero terminator should parse, got %v", err)
	}
}

func TestMP4PaddingFloorGrowsRegion(t *testing.T) {
	// --padding N is a floor, honored on the in-place reuse path too. MP4 reuses the existing
	// ilst+free region when the new content fits, so without the floor on that path a large
	// --padding over a small region would be ignored.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))

	// Set a different title so the seed rewrites (creating the 50 KB region); setting it to
	// its current value would be a no-op.
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
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "Same"))
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
	// A file with no udta/meta/ilst: editing must create the whole tag path, shift the mdat, and fix
	// the stco, and read back correctly.
	mdatPayload := bytes.Repeat([]byte{0xA7}, 120)
	build := func(stcoOff uint32) []byte {
		return slices.Concat(testfile.MP4Ftyp(), mp4Moov(nil, stcoOff), testfile.MP4Atom("mdat", mdatPayload))
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
	// A file with a udta (holding only a chpl) but no meta/ilst: editing must create meta+ilst
	// inside the existing udta, preserve the chpl, fix the stco, and read back.
	chpl := testfile.MP4Atom("chpl", slices.Concat([]byte{1, 0, 0, 0}, make([]byte, 4), []byte{1},
		make([]byte, 8), []byte{3}, []byte("One")))
	data := testfile.MP4AssembleUdta(chpl)

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
	// The written file takes a second edit.
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
	// re-hashes the written output's mdat and compares it to the source, so it fails if
	// buildResult shifted the essence range wrong. The save must succeed.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
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
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), testfile.MP4Text("\xa9too", "Lavf61.7.100"))
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Errorf("expected an inherited-encoder warning; got %v", doc.Warnings())
	}
}

func TestMP4CompilationAndDiscRoundTrip(t *testing.T) {
	// cpil (boolean) and disk (a no-trailing pair) round-trip through the codec.
	data := testfile.MP4Tagged(
		testfile.MP4Atom("cpil", testfile.MP4Data(21, []byte{1})),
		testfile.MP4Atom("disk", testfile.MP4Data(0, []byte{0, 0, 0, 1, 0, 2})), // disc 1 of 2 (6-byte value)
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
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
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
	chpl := testfile.MP4Atom("chpl", slices.Concat([]byte{1, 0, 0, 0}, make([]byte, 4), []byte{1},
		make([]byte, 8), []byte{5}, []byte("Intro")))
	data := testfile.MP4AssembleUdta(testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "T"))), chpl)

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
	chpl := testfile.MP4Atom("chpl", chplPayload)
	data := testfile.MP4AssembleUdta(testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "T"))), chpl)

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
	gnre := testfile.MP4Atom("gnre", testfile.MP4Data(0, []byte{0, 18})) // numeric -> a name
	data := testfile.MP4Tagged(gnre, testfile.MP4Text("\xa9gen", "Synthpop"))
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
	gnre := testfile.MP4Atom("gnre", testfile.MP4Data(0, []byte{0, 18})) // 18 -> index 17 -> "Rock" era list
	data := testfile.MP4Tagged(gnre)
	doc := mustParseBytes(t, data)
	if len(doc.Fields().Genres) != 1 || doc.Fields().Genres[0] == "" {
		t.Errorf("gnre did not resolve to a genre name: %v", doc.Fields().Genres)
	}
	if !hasWarning(doc, wl.WarnNumericGenre) {
		t.Errorf("expected a numeric-genre warning; got %v", doc.Warnings())
	}
}

// truncation signal for MP4: an mdat atom whose declared size runs past EOF is flagged, while an
// intact file is not.
func TestMP4TruncatedMdatWarns(t *testing.T) {
	t.Run("declared overruns file", func(t *testing.T) {
		data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "Title"))
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
		data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "Title"))
		if doc := mustParseBytes(t, data); hasWarning(doc, wl.WarnTruncatedAudio) {
			t.Errorf("an intact MP4 must not be flagged truncated; got %v", doc.Warnings())
		}
	})
	t.Run("64-bit mdat near 2^63 does not overflow detection", func(t *testing.T) {
		// A 64-bit mdat declaring ~2^63 bytes: forming offset+declaredSize overflows
		// int64 to negative and would suppress the warning. Detecting at the walk's
		// clamp (a comparison, no addition) is overflow-safe, so it is still flagged.
		mdat64 := func(payload []byte) []byte {
			b := append([]byte{0, 0, 0, 1}, []byte("mdat")...) // size==1 => 64-bit size follows
			sz := make([]byte, 8)
			binary.BigEndian.PutUint64(sz, 0x7FFFFFFFFFFFFFFF) // max int64; offset+size overflows
			return append(append(b, sz...), payload...)
		}
		// Point the chunk-offset table at the mdat payload (past the 16-byte 64-bit header) so the mdat is
		// real essence, not an unreferenced range the digest trim would drop.
		udta := testfile.MP4Atom("udta", nil)
		ftyp := testfile.MP4Ftyp()
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

// Detection looks past a leading free box, so such a file must parse and keep that box across
// a write.
func TestMP4LeadingFreeAtomParsesAndSurvives(t *testing.T) {
	free := testfile.MP4Atom("free", []byte{0, 0, 0, 0, 0, 0, 0, 0})
	data := testfile.MP4AssembleLeading(free, nil, nil, testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "Song"))))
	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatMP4 {
		t.Fatalf("format = %v, want MP4", doc.Format())
	}
	if doc.Fields().Title != "Song" {
		t.Errorf("title = %q, want Song", doc.Fields().Title)
	}
	plan, err := doc.Edit().Set(tag.Artist, "Band").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.HasPrefix(out, free) {
		t.Errorf("the leading free atom did not survive the write")
	}
	re := mustParseBytes(t, out)
	if len(re.Fields().Artists) != 1 || re.Fields().Artists[0] != "Band" {
		t.Errorf("artists = %v, want [Band]", re.Fields().Artists)
	}
}
