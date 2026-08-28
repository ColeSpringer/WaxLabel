package waxlabel_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
)

// The Ogg FLAC mapping is the one Ogg codec whose cover art is a native FLAC
// PICTURE block rather than a METADATA_BLOCK_PICTURE comment, and the one whose
// identification packet carries a count of the header packets that follow. Both
// are synthesized here rather than shipped as binary fixtures.

// oggCRC is the Ogg page checksum: CRC-32 with polynomial 0x04c11db7, no input or
// output reflection, zero init, and no final XOR. The test carries its own copy so
// a synthesized page is checked against the spec, not against the writer's helper.
func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, c := range b {
		crc ^= uint32(c) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// oggPage lays one packet out as a single Ogg page. Every packet these tests build
// is short enough for one page, so no continuation handling is needed.
func oggPage(flags byte, granule uint64, serial, seq uint32, pkt []byte) []byte {
	var lacing []byte
	n := len(pkt)
	for n >= 255 {
		lacing = append(lacing, 255)
		n -= 255
	}
	lacing = append(lacing, byte(n))

	page := make([]byte, 27+len(lacing)+len(pkt))
	copy(page, "OggS")
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], serial)
	binary.LittleEndian.PutUint32(page[18:22], seq)
	page[26] = byte(len(lacing))
	copy(page[27:], lacing)
	copy(page[27+len(lacing):], pkt)
	binary.LittleEndian.PutUint32(page[22:26], oggCRC(page))
	return page
}

// flacBlockPacket frames one FLAC metadata block as an Ogg FLAC header packet.
func flacBlockPacket(code byte, last bool, body []byte) []byte {
	h := []byte{code, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	if last {
		h[0] |= 0x80
	}
	return append(h, body...)
}

// synthOggFLAC builds a minimal Ogg FLAC stream: the identification packet (the
// mapping prologue plus STREAMINFO), one header packet per supplied block, and a
// single audio page. The blocks are given as {code, body} pairs in order; the
// last-block flag is set on the final one.
func synthOggFLAC(blocks ...[]byte) []byte {
	const serial = 0x57ac7557
	streamInfo := make([]byte, 34)
	streamInfo[0], streamInfo[1] = 0x10, 0x00 // min block 4096
	streamInfo[2], streamInfo[3] = 0x10, 0x00 // max block 4096
	streamInfo[10] = 0x0A                     // sample rate 44100, ...
	streamInfo[11] = 0xC4
	streamInfo[12] = 0x40 | (1 << 1) // rate low nibble | (channels-1)<<1
	streamInfo[13] = 15 << 4         // (bps-1)&0xf << 4

	id := []byte("\x7FFLAC\x01\x00")
	id = binary.BigEndian.AppendUint16(id, uint16(len(blocks)))
	id = append(id, "fLaC"...)
	id = append(id, flacBlockPacket(0, false, streamInfo)...)

	out := oggPage(0x02, 0, serial, 0, id) // BOS
	seq := uint32(1)
	for i, b := range blocks {
		out = append(out, oggPage(0, 0, serial, seq, flacBlockPacket(b[0], i == len(blocks)-1, b[1:]))...)
		seq++
	}
	return append(out, oggPage(0, 4096, serial, seq, []byte("AUDIO-FRAME-BYTES"))...)
}

// declaredHeaderPackets reads the identification packet's count of header packets
// following it. A metadata rewrite that adds or drops a block must keep it honest.
func declaredHeaderPackets(t *testing.T, data []byte) int {
	t.Helper()
	if len(data) < 27 {
		t.Fatal("truncated Ogg stream")
	}
	body := 27 + int(data[26])
	if len(data) < body+9 {
		t.Fatal("truncated identification packet")
	}
	return int(binary.BigEndian.Uint16(data[body+7 : body+9]))
}

func TestOggFLACPreservesUnknownBlocks(t *testing.T) {
	marker := []byte("APPLICATION-PRESERVE-ME-0xABCDEF")
	app := append(append([]byte{2}, "WAXX"...), marker...)
	comment := append([]byte{4}, vorbis.RenderCommentList("test", []vorbis.Comment{{Name: "TITLE", Value: "Keep"}})...)
	data := synthOggFLAC(app, comment)

	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatOggFLAC {
		t.Fatalf("format = %v, want Ogg FLAC", doc.Format())
	}
	if doc.Fields().Title != "Keep" {
		t.Fatalf("setup: Title = %q", doc.Fields().Title)
	}
	plan, err := doc.Edit().Set(tag.Title, "Changed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if !bytes.Contains(out, marker) {
		t.Error("APPLICATION block bytes were not preserved through a tag edit")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Changed" {
		t.Errorf("Title = %q, want Changed", re.Fields().Title)
	}
	if got := declaredHeaderPackets(t, out); got != 2 {
		t.Errorf("declared header packets = %d, want 2", got)
	}
	foundApp := false
	for _, e := range re.Native().Describe() {
		if e.Kind == "APPLICATION" {
			foundApp = true
		}
	}
	if !foundApp {
		t.Error("APPLICATION block missing from native view after edit")
	}
}

// TestOggFLACPictureIsNativeBlock pins the mapping's one divergence from Vorbis and
// Opus: a cover is written as a FLAC PICTURE block, not a base64
// METADATA_BLOCK_PICTURE comment, and the identification packet's header-packet
// count follows the block it added.
func TestOggFLACPictureIsNativeBlock(t *testing.T) {
	src := readFixture(t, notagsOggFLAC)
	before := declaredHeaderPackets(t, src)

	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)

	if bytes.Contains(out, []byte("METADATA_BLOCK_PICTURE")) {
		t.Error("cover was stored as a comment; Ogg FLAC must use a native PICTURE block")
	}
	if got := declaredHeaderPackets(t, out); got != before+1 {
		t.Errorf("declared header packets = %d, want %d", got, before+1)
	}
	re := mustParseBytes(t, out)
	if pics := re.Pictures(); len(pics) != 1 || pics[0].Type != wl.PicFrontCover || !bytes.Equal(pics[0].Data, tinyPNG()) {
		t.Fatalf("expected one front cover with the stored bytes, got %+v", pics)
	}
	seenPicture := false
	for _, e := range re.Native().Describe() {
		if e.Kind == "PICTURE" {
			seenPicture = true
		}
	}
	if !seenPicture {
		t.Error("no PICTURE block in the native view after adding a cover")
	}

	// And back out again: the count must shrink with the block.
	plan2, err := re.Edit().ClearPictures().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, out, plan2)
	if got := declaredHeaderPackets(t, out2); got != before {
		t.Errorf("after clearing pictures, declared header packets = %d, want %d", got, before)
	}
	if pics := mustParseBytes(t, out2).Pictures(); len(pics) != 0 {
		t.Errorf("ClearPictures left %d pictures", len(pics))
	}
}

// TestOggFLACCommentPictureMaterialized covers the cross-form file: some encoders
// put a METADATA_BLOCK_PICTURE comment in an Ogg FLAC stream. It must read as a
// cover, and a tag-only edit (which re-renders the comment block and so strips the
// entry) must materialize it as a native PICTURE block rather than drop it.
func TestOggFLACCommentPictureMaterialized(t *testing.T) {
	pic := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}
	comment := append([]byte{4}, vorbis.RenderCommentList("test", []vorbis.Comment{
		{Name: "TITLE", Value: "Keep"},
		{Name: vorbis.PictureComment, Value: commentPictureValue(pic)},
	})...)
	data := synthOggFLAC(comment)

	doc := mustParseBytes(t, data)
	if len(doc.Pictures()) != 1 {
		t.Fatalf("setup: expected the comment cover to be read, got %+v", doc.Pictures())
	}
	plan, err := doc.Edit().Set(tag.Title, "Changed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	re := mustParseBytes(t, out)
	if len(re.Pictures()) != 1 {
		t.Fatalf("cover lost across a tag-only edit: %+v", re.Pictures())
	}
	if bytes.Contains(out, []byte("METADATA_BLOCK_PICTURE")) {
		t.Error("the stale picture comment survived; it must be replaced by a native block")
	}
	if got := declaredHeaderPackets(t, out); got != 2 {
		t.Errorf("declared header packets = %d, want 2 (comment + materialized picture)", got)
	}
}

// TestOggFLACWithoutCommentBlock exercises the create-a-comment-block path: the
// mapping requires one, but a stream missing it is read rather than refused.
func TestOggFLACWithoutCommentBlock(t *testing.T) {
	padding := append([]byte{1}, make([]byte, 8)...)
	data := synthOggFLAC(padding)

	doc := mustParseBytes(t, data)
	if doc.Tags().Len() != 0 {
		t.Fatalf("setup: expected no tags, got %d", doc.Tags().Len())
	}
	plan, err := doc.Edit().Set(tag.Title, "Created").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	re := mustParseBytes(t, out)
	if re.Fields().Title != "Created" {
		t.Errorf("Title = %q, want Created", re.Fields().Title)
	}
	// The PADDING block is dropped (Ogg re-paginates the header region on every
	// rewrite, so padding buys nothing), leaving just the new comment block.
	if got := declaredHeaderPackets(t, out); got != 1 {
		t.Errorf("declared header packets = %d, want 1", got)
	}
}
