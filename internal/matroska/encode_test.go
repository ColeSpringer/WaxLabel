package matroska

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// TestRawCRCOverlongSizeVINT verifies that a CRC-32 element whose 4-byte size is
// written with an overlong VINT (0x40 0x04 rather than the canonical 0x84) is
// still recognized. rawCRC decodes the size VINT and accepts any width whose
// value is 4; recomputeCRC then rewrites the value as crc32(content).
func TestRawCRCOverlongSizeVINT(t *testing.T) {
	content := []byte{0x11, 0x22, 0x33, 0x44, 0x55} // the master's body, after the CRC element
	// CRC id (0xBF) + overlong 2-byte size VINT encoding 4 (0x40 0x04) + 4 value bytes.
	raw := append([]byte{idCRC32 & 0xFF, 0x40, 0x04, 0, 0, 0, 0}, content...)

	spot := rawCRC(raw, 0)
	if spot == nil {
		t.Fatal("rawCRC returned nil for an overlong (2-byte) size VINT; the CRC would be left stale")
	}
	if spot.valOff != 3 || spot.contentStart != 7 {
		t.Errorf("crcSpot = {valOff:%d, contentStart:%d}, want {3, 7} (2-byte VINT width accounted for)",
			spot.valOff, spot.contentStart)
	}

	recomputeCRC(raw, spot)
	want := crc32.ChecksumIEEE(raw[spot.contentStart:])
	got := binary.LittleEndian.Uint32(raw[spot.valOff : spot.valOff+4])
	if got != want {
		t.Errorf("recomputed CRC = %#08x, want crc32(content) = %#08x", got, want)
	}
	if raw[0] != byte(idCRC32) {
		t.Errorf("CRC element no longer leads its master: first byte %#x", raw[0])
	}
}

// TestEncodeRoundTrip checks the EBML encoder is the inverse of the reader: a
// nested, CRC-guarded master element re-decodes with the CRC matching its content.
func TestEncodeRoundTrip(t *testing.T) {
	st := encElement(idSimpleTag, append(stringElement(idTagName, "ARTIST"), stringElement(idTagString, "Me")...))
	tags := masterElement(idTags, encElement(idTag, st), true)

	src := core.BytesSource(tags)
	limit := int64(1 << 20)
	root, ok := readElement(src, 0, src.Size(), limit)
	if !ok || root.id != idTags {
		t.Fatalf("readElement root: ok=%v id=%#x", ok, root.id)
	}
	first, _ := readElement(src, root.dataStart, root.dataEnd, limit)
	if first.id != idCRC32 {
		t.Fatalf("first child id=%#x, want CRC-32", first.id)
	}
	stored, _ := bits.ReadSlice(src, first.dataStart, 4, limit)
	content, _ := bits.ReadSlice(src, first.dataEnd, root.dataEnd-first.dataEnd, limit)
	if !bytes.Equal(stored, crcElement(content)[2:]) {
		t.Errorf("CRC mismatch: stored=% x want=% x", stored, crcElement(content)[2:])
	}
}

// TestVINTBoundaries pins the minimal-width VINT and fixed-width encodings,
// including the all-ones "unknown size" exclusion.
func TestVINTBoundaries(t *testing.T) {
	for _, c := range []struct {
		n     uint64
		width int
	}{{0, 1}, {126, 1}, {127, 2}, {128, 2}, {16382, 2}, {16383, 3}} {
		if w := vintWidth(c.n); w != c.width {
			t.Errorf("vintWidth(%d)=%d want %d", c.n, w, c.width)
		}
	}
	if _, ok := sizeVINTWidthOK(127, 1); ok {
		t.Error("127 must not fit a 1-byte VINT (it is the unknown-size form)")
	}
	if b, ok := sizeVINTWidthOK(100, 1); !ok || !bytes.Equal(b, []byte{0xE4}) {
		t.Errorf("sizeVINTWidthOK(100,1)=% x ok=%v", b, ok)
	}
	if got := uintDataWidth(254, 1); !bytes.Equal(got, []byte{0xFE}) {
		t.Errorf("uintDataWidth(254,1)=% x", got)
	}
	if uintDataWidth(256, 1) != nil {
		t.Error("uintDataWidth(256,1) must not fit one byte")
	}
}

// TestVoidOfTotal confirms voidOfTotal renders a Void of exactly the requested
// total length across the size-VINT width boundaries.
func TestVoidOfTotal(t *testing.T) {
	for _, total := range []int64{2, 3, 10, 129, 130, 16500} {
		b := voidOfTotal(total)
		if int64(len(b)) != total {
			t.Errorf("voidOfTotal(%d) len = %d", total, len(b))
		}
		if b[0] != byte(idVoid) {
			t.Errorf("voidOfTotal(%d) id = %#x", total, b[0])
		}
		el, ok := readElement(core.BytesSource(b), 0, int64(len(b)), 1<<20)
		if !ok || el.id != idVoid || el.dataEnd != int64(len(b)) {
			t.Errorf("voidOfTotal(%d) does not re-parse to a single Void: %+v ok=%v", total, el, ok)
		}
	}
}

// TestAttachedFileUID confirms a written cover carries the mandatory FileUID, that
// it is non-zero, and that it is random (distinct across renders) - a collision
// across 64 bits is negligible.
func TestAttachedFileUID(t *testing.T) {
	pic := core.Picture{Type: core.PicFrontCover, MIME: "image/png", Data: []byte("cover-bytes")}
	b0, _ := attachedFileBytes(pic)
	b1, _ := attachedFileBytes(pic)

	if uidOf(t, b0) == 0 || uidOf(t, b1) == 0 {
		t.Error("FileUID must be non-zero")
	}
	if uidOf(t, b0) == uidOf(t, b1) {
		t.Error("FileUID should be random (two renders collided)")
	}
	for i := 0; i < 100; i++ {
		if fileUID() == 0 {
			t.Fatal("fileUID must never return zero")
		}
	}
}

// TestCheckIndexCaptured: an edit is refused when a SeekHead/Cues element exists
// but its structure was not captured (a read/over-limit failure), since copying it
// verbatim while other elements move would corrupt its offsets.
func TestCheckIndexCaptured(t *testing.T) {
	if err := checkIndexCaptured(&writeBase{children: []l1elem{{id: idSeekHead}}}); err == nil {
		t.Error("uncaptured SeekHead should be refused")
	}
	if err := checkIndexCaptured(&writeBase{children: []l1elem{{id: idCues}}}); err == nil {
		t.Error("uncaptured Cues should be refused")
	}
	ok := &writeBase{children: []l1elem{{id: idSeekHead}, {id: idCues}}, seek: &seekHead{}, cues: &cuesIndex{}}
	if err := checkIndexCaptured(ok); err != nil {
		t.Errorf("captured index elements should pass: %v", err)
	}
	if err := checkIndexCaptured(&writeBase{children: []l1elem{{id: idTracks}}}); err != nil {
		t.Errorf("a file with no index elements should pass: %v", err)
	}
}

// TestCheckPreservable: an edit is refused when an element the writer must copy
// verbatim could not be captured (raw==nil from an over-limit size), rather than
// silently dropping it.
func TestCheckPreservable(t *testing.T) {
	// A non-album group whose bytes weren't captured. It carries no edited key, so
	// it takes the verbatim path and needs its whole-element raw.
	d := &doc{groups: []tagGroup{
		{scope: core.ScopeAlbum, raw: []byte{1}},
		{scope: core.ScopeTrack, trackUID: true, raw: nil},
	}}
	if err := checkPreservable(d, changes{simple: true}, nil); err == nil {
		t.Error("uncaptured tag group should be refused")
	}
	// A non-image attachment whose bytes weren't captured.
	d2 := &doc{attachments: []attachment{{image: false, raw: nil}}}
	if err := checkPreservable(d2, changes{pictures: true}, nil); err == nil {
		t.Error("uncaptured attachment should be refused")
	}
	// All captured -> fine.
	ok := &doc{
		groups:      []tagGroup{{scope: core.ScopeAlbum, raw: []byte{1}}},
		attachments: []attachment{{image: true}},
	}
	if err := checkPreservable(ok, changes{simple: true, pictures: true}, nil); err != nil {
		t.Errorf("fully-captured doc should pass: %v", err)
	}

	// A track group carrying an edited key (ENCODER) is re-rendered, not preserved:
	// its surviving SimpleTag's raw must have been captured, even though the group's
	// whole-element raw is nil (a large group whose dropped key shrank it under the
	// limit). A surviving tag with no raw is refused.
	ek := map[tag.Key]bool{tag.Encoder: true}
	rerender := &doc{groups: []tagGroup{
		{scope: core.ScopeAlbum, raw: []byte{1}},
		{scope: core.ScopeTrack, trackUID: true, raw: nil, targetsRaw: []byte{1}, tags: []simpleTag{
			{name: "ENCODER", raw: []byte{1}}, // dropped (edited), raw not required
			{name: "TITLE", raw: nil},         // kept, but its raw wasn't captured
		}},
	}}
	if err := checkPreservable(rerender, changes{simple: true}, ek); err == nil {
		t.Error("re-rendered group with an uncaptured surviving tag should be refused")
	}
	// Same group, but the scope-narrowing Targets bytes weren't captured: the
	// rebuild would lose the track scope, so it is refused.
	noTargets := &doc{groups: []tagGroup{
		{scope: core.ScopeAlbum, raw: []byte{1}},
		{scope: core.ScopeTrack, trackUID: true, raw: nil, targetsRaw: nil, tags: []simpleTag{
			{name: "ENCODER", raw: []byte{1}}, // dropped
			{name: "TITLE", raw: []byte{1}},   // kept
		}},
	}}
	if err := checkPreservable(noTargets, changes{simple: true}, ek); err == nil {
		t.Error("re-rendered scope-narrowing group with uncaptured Targets should be refused")
	}
	// A re-rendered group where every SimpleTag is dropped needs neither the
	// surviving-tag raws nor the Targets - it disappears entirely.
	emptied := &doc{groups: []tagGroup{
		{scope: core.ScopeAlbum, raw: []byte{1}},
		{scope: core.ScopeTrack, trackUID: true, raw: nil, targetsRaw: nil, tags: []simpleTag{
			{name: "ENCODER", raw: nil}, // dropped (edited); its raw is irrelevant
		}},
	}}
	if err := checkPreservable(emptied, changes{simple: true}, ek); err != nil {
		t.Errorf("a fully-emptied re-rendered group should pass: %v", err)
	}
}

// uidOf parses an AttachedFile element and returns its FileUID value (0 if absent).
func uidOf(t *testing.T, attached []byte) uint64 {
	t.Helper()
	src := core.BytesSource(attached)
	limit := int64(1 << 20)
	root, ok := readElement(src, 0, src.Size(), limit)
	if !ok || root.id != idAttached {
		t.Fatalf("not an AttachedFile: ok=%v id=%#x", ok, root.id)
	}
	var uid uint64
	_ = eachChild(src, root.dataStart, root.dataEnd, bits.NewDepth(8), limit, func(c element) error {
		if c.id == idFileUID {
			uid = readUint(src, c, limit)
		}
		return nil
	})
	return uid
}

// TestMatroskaNameRoundTrip is the write-mapping invariant: every spec name a
// canonical key writes to reads back to that same canonical key.
func TestMatroskaNameRoundTrip(t *testing.T) {
	keys := []tag.Key{
		tag.Artist, tag.Album, tag.AlbumArtist, tag.Composer, tag.Genre, tag.Comment,
		tag.TrackNumber, tag.TrackTotal, tag.DiscNumber, tag.DiscTotal,
		tag.RecordingDate, tag.ReleaseDate, tag.OriginalDate,
		tag.Encoder, tag.EncodedBy, tag.CatalogNumber, tag.Remixer, tag.Label, tag.Grouping,
		tag.MBReleaseID, tag.ReplayGainTrackGain,
	}
	for _, k := range keys {
		name := mapping.MatroskaTagName(k)
		got, ok := mapping.MatroskaTagKey(name)
		if !ok || got != k {
			t.Errorf("%s -> %q -> %s (ok=%v): not a round trip", k, name, got, ok)
		}
	}
}
