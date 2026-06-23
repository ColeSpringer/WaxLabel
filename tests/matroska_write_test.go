package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os/exec"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// saveMatroska runs an editor to bytes via WriteTo and returns the output and the
// post-write document.
func saveMatroska(t *testing.T, src []byte, e *wl.Editor) ([]byte, *wl.Document) {
	t.Helper()
	plan, err := e.Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	var w writerTo
	outDoc, res, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Committed {
		t.Fatal("write not committed")
	}
	return w.b, outDoc
}

// essenceUnchanged asserts the audio essence (cluster region) is bit-identical
// across an edit - the preservation invariant.
func essenceUnchanged(t *testing.T, src, out []byte) {
	t.Helper()
	ctx := context.Background()
	in := mustParseBytes(t, src)
	got := mustParseBytes(t, out)
	d1, err := in.HashAudioEssence(ctx, wl.WithHashSource(wl.BytesSource(src)))
	if err != nil {
		t.Fatalf("hash in: %v", err)
	}
	d2, err := got.HashAudioEssence(ctx, wl.WithHashSource(wl.BytesSource(out)))
	if err != nil {
		t.Fatalf("hash out: %v", err)
	}
	if !d1.Equal(d2) {
		t.Errorf("audio essence changed across edit: %s vs %s", d1, d2)
	}
}

// TestMatroskaWriteTitle edits Segment.Info.Title and confirms the new title
// reads back, the other tags survive, and the clusters are untouched.
func TestMatroskaWriteTitle(t *testing.T) {
	src := readFixture(t, sampleMKA)
	out, outDoc := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Title, "New MKA Title"))

	if got := outDoc.Fields().Title; got != "New MKA Title" {
		t.Errorf("returned doc Title = %q", got)
	}
	re := mustParseBytes(t, out)
	f := re.Fields()
	if f.Title != "New MKA Title" {
		t.Errorf("reparsed Title = %q, want New MKA Title", f.Title)
	}
	if f.Album != "Sample Album" || len(f.Artists) != 1 || f.Artists[0] != "Sample Artist" {
		t.Errorf("other tags not preserved: Album=%q Artists=%v", f.Album, f.Artists)
	}
	if f.TrackNumber != 2 || f.TrackTotal != 10 {
		t.Errorf("track numbering lost: %d/%d", f.TrackNumber, f.TrackTotal)
	}
	// A small title edit is absorbed into the reserved Void: the clusters do not
	// move and the file size is unchanged.
	if len(out) != len(src) {
		t.Errorf("absorbed edit changed size %d -> %d (expected the Void to absorb it)", len(src), len(out))
	}
	essenceUnchanged(t, src, out)
}

// TestMatroskaWriteTag changes an existing SimpleTag and adds a new one.
func TestMatroskaWriteTag(t *testing.T) {
	src := readFixture(t, sampleMKA)
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().
		Set(tag.Artist, "Changed Artist").
		Set(tag.Key("CUSTOM_FIELD"), "custom value"))

	f := mustParseBytes(t, out).Fields()
	if len(f.Artists) != 1 || f.Artists[0] != "Changed Artist" {
		t.Errorf("Artist = %v, want Changed Artist", f.Artists)
	}
	if f.Album != "Sample Album" {
		t.Errorf("Album not preserved: %q", f.Album)
	}
	if v, _ := mustParseBytes(t, out).Get(tag.Key("CUSTOM_FIELD")); len(v) != 1 || v[0] != "custom value" {
		t.Errorf("custom field = %v, want [custom value]", v)
	}
	// Adding a new tag overflows the reserved Void, so the tail shifts and the file
	// grows - exercising the shift path (with the Cues/SeekHead position fixups).
	if len(out) <= len(src) {
		t.Errorf("expected the add-tag edit to grow the file via the shift path (%d -> %d)", len(src), len(out))
	}
	essenceUnchanged(t, src, out)
}

// TestMatroskaWriteSpecNames confirms canonical keys are written under the
// Matroska-spec SimpleTag names players expect (ALBUM_ARTIST, DATE_RECORDED) and
// still round-trip to the same canonical keys.
func TestMatroskaWriteSpecNames(t *testing.T) {
	src := readFixture(t, sampleMKA)
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().
		Set(tag.AlbumArtist, "VA").Set(tag.RecordingDate, "1999"))
	f := mustParseBytes(t, out).Fields()
	if f.AlbumArtist != "VA" {
		t.Errorf("AlbumArtist = %q", f.AlbumArtist)
	}
	if f.RecordingDate != "1999" {
		t.Errorf("RecordingDate = %q", f.RecordingDate)
	}
	// The on-wire SimpleTag names must be the spec forms.
	if !bytes.Contains(out, []byte("ALBUM_ARTIST")) {
		t.Error("expected ALBUM_ARTIST SimpleTag name on the wire")
	}
	if !bytes.Contains(out, []byte("DATE_RECORDED")) {
		t.Error("expected DATE_RECORDED SimpleTag name on the wire")
	}
}

// TestMatroskaWriteNoOp confirms a same-value edit is a no-op.
func TestMatroskaWriteNoOp(t *testing.T) {
	src := readFixture(t, sampleMKA)
	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().Set(tag.Title, doc.Fields().Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Report().NoOp {
		t.Error("same-value Title edit should be a no-op")
	}
}

// TestMatroskaWriteCover replaces the cover art of an .mka and reads it back
// (the fixture already carries a cover, so this also exercises clear+add).
func TestMatroskaWriteCover(t *testing.T) {
	src := readFixture(t, sampleMKA)
	out, outDoc := saveMatroska(t, src, mustParseBytes(t, src).Edit().
		ClearPictures().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}))
	if len(outDoc.Pictures()) == 0 {
		t.Fatal("returned doc has no pictures")
	}
	re := mustParseBytes(t, out)
	pics := re.Pictures()
	if len(pics) != 1 || pics[0].Type != wl.PicFrontCover {
		t.Fatalf("reparsed pictures = %+v", pics)
	}
	if !bytes.Equal(pics[0].Data, tinyPNG()) {
		t.Error("cover bytes not preserved")
	}
	essenceUnchanged(t, src, out)
}

// A minimal EBML walker to validate CRC-32 integrity of written output.

// vlen returns the byte length a VINT occupies from its first byte.
func vlen(b byte) int {
	for i := range 8 {
		if b&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 0
}

// readVint reads a VINT at off, returning its value (marker stripped when
// keepMarker is false), the bytes consumed, and ok.
func readVint(b []byte, off int, keepMarker bool) (uint64, int, bool) {
	if off >= len(b) {
		return 0, 0, false
	}
	n := vlen(b[off])
	if n == 0 || off+n > len(b) {
		return 0, 0, false
	}
	var v uint64
	if keepMarker {
		for i := range n {
			v = v<<8 | uint64(b[off+i])
		}
		return v, n, true
	}
	v = uint64(b[off] &^ (0x80 >> (n - 1)))
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[off+i])
	}
	return v, n, true
}

// checkCRCs walks every master element in [start,end) and, when its first child
// is a CRC-32 (0xBF), verifies the stored little-endian value equals the IEEE
// CRC-32 of the element's content after the CRC - the exact integrity check a
// strict Matroska reader (mkvmerge) performs.
func checkCRCs(t *testing.T, b []byte, start, end, depth int) {
	if depth > 12 {
		return
	}
	off := start
	for off < end {
		id, idn, ok := readVint(b, off, true)
		if !ok {
			return
		}
		size, szn, ok := readVint(b, off+idn, false)
		if !ok {
			return
		}
		ds := off + idn + szn
		de := ds + int(size)
		if de > end || de < ds {
			return
		}
		// Masters that may carry a CRC and nested masters: Segment, SeekHead, Info,
		// Tracks, Tags, Tag, Attachments, Cues, Chapters, EditionEntry.
		switch id {
		case 0x18538067, 0x114D9B74, 0x1549A966, 0x1654AE6B, 0x1254C367, 0x7373, 0x1941A469, 0x1C53BB6B, 0x1043A770, 0x45B9:
			if ds+6 <= de && b[ds] == 0xBF && b[ds+1] == 0x84 {
				stored := binary.LittleEndian.Uint32(b[ds+2 : ds+6])
				if got := crc32.ChecksumIEEE(b[ds+6 : de]); got != stored {
					t.Errorf("CRC mismatch in element %#x: stored %08x, computed %08x", id, stored, got)
				}
			}
			checkCRCs(t, b, ds, de, depth+1)
		}
		off = de
	}
}

// TestMatroskaWriteCRCsValid edits Tags, Info.Title, and the cover (touching the
// Tags/Info/SeekHead/Cues CRC-32s across both the absorb and shift paths) and
// verifies every CRC-32 in the output is recomputed correctly.
func TestMatroskaWriteCRCsValid(t *testing.T) {
	src := readFixture(t, sampleMKA)
	for _, e := range []*wl.Editor{
		mustParseBytes(t, src).Edit().Set(tag.Title, "T2"),                                  // absorb
		mustParseBytes(t, src).Edit().Set(tag.Artist, "A Much Longer Artist Forcing Shift"), // shift
		mustParseBytes(t, src).Edit().Set(tag.Title, "T3").Set(tag.Key("X"), "y"),           // both
	} {
		out, _ := saveMatroska(t, src, e)
		checkCRCs(t, out, 0, len(out), 0)
	}
}

// TestMatroskaDifferentialFFprobe writes tags (a small absorbed edit and a large
// shifting one) and confirms ffprobe - the authority - reads them back and still
// sees a valid FLAC audio stream, proving the rewrite kept the container sound.
func TestMatroskaDifferentialFFprobe(t *testing.T) {
	requireTool(t, "ffprobe")
	path := copyToTemp(t, sampleMKA)
	plan, err := mustParseFile(t, path).Edit().
		Set(tag.Title, "Differential Title").
		Set(tag.Artist, "Differential Artist That Is Quite Long To Force A Tail Shift").
		Set(tag.Album, "Differential Album").
		Set(tag.Key("CUSTOM_TAG"), "custom-value").
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_entries", "format_tags:stream=codec_name", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var probe struct {
		Streams []struct {
			Codec string `json:"codec_name"`
		} `json:"streams"`
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("parse ffprobe json: %v\n%s", err, out)
	}
	// The segment title is a container-level tag; the rest are SimpleTags.
	for k, want := range map[string]string{
		"title": "Differential Title", "ARTIST": "Differential Artist That Is Quite Long To Force A Tail Shift",
		"ALBUM": "Differential Album", "CUSTOM_TAG": "custom-value",
	} {
		if got := lookupCI(probe.Format.Tags, k); got != want {
			t.Errorf("ffprobe tag %q = %q, want %q (all: %v)", k, got, want, probe.Format.Tags)
		}
	}
	if len(probe.Streams) == 0 || probe.Streams[0].Codec != "flac" {
		t.Errorf("audio stream not intact: %+v", probe.Streams)
	}
}

// TestMatroskaDifferentialRemux confirms ffmpeg accepts our edited output for a
// stream-copy remux (a strict structural validation of the rewrite).
func TestMatroskaDifferentialRemux(t *testing.T) {
	requireTool(t, "ffmpeg")
	path := copyToTemp(t, sampleMKA)
	plan, err := mustParseFile(t, path).Edit().Set(tag.Album, "Remux Album").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-i", path, "-c", "copy", "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg remux rejected our output: %v\n%s", err, out)
	}
}

// idSeekHead mirrors the codec constant for the synth tests below.
const idSeekHead = 0x114D9B74

// countSegChildren counts the Segment's direct children with the given ID.
func countSegChildren(t *testing.T, data []byte, wantID uint64) int {
	t.Helper()
	// Walk top-level to find the Segment, then count its children.
	off := 0
	for off < len(data) {
		id, idn, ok := readVint(data, off, true)
		if !ok {
			return 0
		}
		size, szn, ok := readVint(data, off+idn, false)
		if !ok {
			return 0
		}
		ds := off + idn + szn
		de := ds + int(size)
		if de > len(data) || de < ds {
			de = len(data)
		}
		if id != 0x18538067 { // not the Segment
			off = de
			continue
		}
		n, o := 0, ds
		for o < de {
			cid, cidn, ok := readVint(data, o, true)
			if !ok {
				break
			}
			csize, cszn, ok := readVint(data, o+cidn, false)
			if !ok {
				break
			}
			if cid == wantID {
				n++
			}
			cde := o + cidn + cszn + int(csize)
			if cde <= o || cde > de {
				break
			}
			o = cde
		}
		return n
	}
	return 0
}

// TestMatroskaWriteCrossScopeNoDuplicate: editing a file with a track-scoped tag
// must not duplicate that tag into album scope (finding: every save re-emitting
// the whole flattened set bloats the file and risks a spurious conflict).
func TestMatroskaWriteCrossScopeNoDuplicate(t *testing.T) {
	tags := mkEl(idTags, concat(
		mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA"))),
		mkEl(idTag, concat(
			mkEl(idTargets, concat(mkUint(idTgtTypeVal, 30), mkUint(idTagTrackUID, 7))),
			mkSimple("PART_NUMBER", "2/10"))),
	))
	data := buildMatroska("matroska", "T", tags)

	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Album, "X"))
	if n := bytes.Count(out, []byte("PART_NUMBER")); n != 1 {
		t.Errorf("PART_NUMBER written %d times, want 1 (no cross-scope duplication)", n)
	}
	re := mustParseBytes(t, out)
	f := re.Fields()
	if f.TrackNumber != 2 || f.TrackTotal != 10 || f.Album != "X" || f.Artists[0] != "AA" {
		t.Errorf("round-trip wrong: %d/%d album=%q artist=%v", f.TrackNumber, f.TrackTotal, f.Album, f.Artists)
	}
	if hasWarning(re, wl.WarnConflictingFamilies) {
		t.Error("spurious cross-scope conflict warning after edit")
	}
}

// mkCRC prepends a CRC-32 element over content (the mkvmerge master convention),
// so a synthesized Tag master parses as CRC-bearing and its re-render must
// recompute the checksum rather than copy the stale one.
func mkCRC(content []byte) []byte {
	sum := crc32.ChecksumIEEE(content)
	crc := []byte{idCRC32, 0x84, byte(sum), byte(sum >> 8), byte(sum >> 16), byte(sum >> 24)}
	return concat(crc, content)
}

// multiScopeTags builds a Tags element with ENCODER at both album scope and a
// track-scoped group, plus the given extra track-scope SimpleTags. The track
// group is optionally CRC-guarded so the re-render path's CRC recompute is
// exercised. This is the transcoded-file shape the report's finding #1 hinges on
// (muxer ENCODER at album + codec ENCODER at track).
func multiScopeTags(crc bool, trackExtra ...[]byte) []byte {
	album := mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)),
		mkSimple("ARTIST", "AA"), mkSimple("ENCODER", "album-enc")))
	trackContent := concat(mkEl(idTargets, concat(mkUint(idTgtTypeVal, 50), mkUint(idTagTrackUID, 7))),
		mkSimple("ENCODER", "track-enc"))
	trackContent = concat(trackContent, concat(trackExtra...))
	if crc {
		trackContent = mkCRC(trackContent)
	}
	return mkEl(idTags, concat(album, mkEl(idTag, trackContent)))
}

// TestMatroskaWriteMultiScopeClear: clearing a key carried at album *and* track
// scope removes it from every scope (finding #1), leaves unrelated scoped tags
// intact, and dissolves the spurious cross-scope conflict the two copies produced.
func TestMatroskaWriteMultiScopeClear(t *testing.T) {
	data := buildMatroska("matroska", "T", multiScopeTags(false, mkSimple("PART_NUMBER", "2/10")))

	// The fresh file projects ENCODER to two conflicting values across scopes.
	if v, _ := mustParseBytes(t, data).Get(tag.Encoder); len(v) != 2 {
		t.Fatalf("setup: want 2 ENCODER values across scopes, got %v", v)
	}

	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Clear(tag.Encoder))

	if n := bytes.Count(out, []byte("ENCODER")); n != 0 {
		t.Errorf("ENCODER survived %d times after clear, want 0 (cleared at every scope)", n)
	}
	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Encoder); len(v) != 0 {
		t.Errorf("ENCODER reads back %v after clear, want empty", v)
	}
	// The other tags at each scope survive (album ARTIST, track PART_NUMBER).
	f := re.Fields()
	if len(f.Artists) != 1 || f.Artists[0] != "AA" || f.TrackNumber != 2 || f.TrackTotal != 10 {
		t.Errorf("clear dropped an unrelated tag: artist=%v %d/%d", f.Artists, f.TrackNumber, f.TrackTotal)
	}
	if hasWarning(re, wl.WarnConflictingFamilies) {
		t.Error("conflicting-families warning should be gone after clearing ENCODER everywhere")
	}
}

// TestMatroskaWriteMultiScopeSet: setting a key carried at two scopes replaces it
// with the single new value at album scope (not one new + one stale).
func TestMatroskaWriteMultiScopeSet(t *testing.T) {
	data := buildMatroska("matroska", "T", multiScopeTags(false))

	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Encoder, "OnlyEnc"))

	if n := bytes.Count(out, []byte("ENCODER")); n != 1 {
		t.Errorf("ENCODER written %d times after set, want 1 (replaced, not doubled)", n)
	}
	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Encoder); len(v) != 1 || v[0] != "OnlyEnc" {
		t.Errorf("ENCODER = %v, want [OnlyEnc]", v)
	}
	if hasWarning(re, wl.WarnConflictingFamilies) {
		t.Error("conflicting-families warning should be gone after a single-value set")
	}
}

// TestMatroskaWriteMultiScopeCRCRecompute: a CRC-bearing track group that must be
// re-rendered (to drop the edited key) has its CRC recomputed over the new content
// - the old verbatim fast path never touched a non-album group's CRC.
func TestMatroskaWriteMultiScopeCRCRecompute(t *testing.T) {
	data := buildMatroska("matroska", "T", multiScopeTags(true, mkSimple("COMPOSER", "TrackComposer")))

	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Clear(tag.Encoder))

	// Every CRC in the output, including the re-rendered track group's, must verify.
	checkCRCs(t, out, 0, len(out), 0)
	if n := bytes.Count(out, []byte("ENCODER")); n != 0 {
		t.Errorf("ENCODER survived CRC-group re-render %d times, want 0", n)
	}
	if v, _ := mustParseBytes(t, out).Get(tag.Composer); len(v) != 1 || v[0] != "TrackComposer" {
		t.Errorf("track-scoped COMPOSER lost in CRC re-render: %v", v)
	}
}

// TestMatroskaWriteMultiScopeRerenderRoundTrips: after a multi-scope clear
// re-renders a track group, the returned document is re-editable in place - a
// second, unrelated edit preserves the surviving track tag and does not resurrect
// the cleared key (the re-rendered group carries its new bytes, not the stale ones).
func TestMatroskaWriteMultiScopeRerenderRoundTrips(t *testing.T) {
	data := buildMatroska("matroska", "T", multiScopeTags(false, mkSimple("COMPOSER", "TrackComposer")))

	out1, doc1 := saveMatroska(t, data, mustParseBytes(t, data).Edit().Clear(tag.Encoder))
	// Re-edit the returned document in memory (no re-parse) and save again.
	out2, _ := saveMatroska(t, out1, doc1.Edit().Set(tag.Album, "NewAlbum"))

	re := mustParseBytes(t, out2)
	if v, _ := re.Get(tag.Encoder); len(v) != 0 {
		t.Errorf("cleared ENCODER resurrected on re-edit: %v", v)
	}
	if v, _ := re.Get(tag.Composer); len(v) != 1 || v[0] != "TrackComposer" {
		t.Errorf("surviving track COMPOSER lost on re-edit: %v", v)
	}
	if re.Fields().Album != "NewAlbum" {
		t.Errorf("second edit not applied: Album = %q", re.Fields().Album)
	}
}

// TestMatroskaWriteCrossScopeSplitValuePreserved (finding #1): a key split across
// scopes with different values (ENCODER album=album-enc + track=track-enc, the
// transcoded-file shape) must keep BOTH values when an unrelated tag is edited. The
// album group is rebuilt; the value-aware sync re-emits only the album-scope value
// the track group does not carry, instead of dropping the album value wholesale.
func TestMatroskaWriteCrossScopeSplitValuePreserved(t *testing.T) {
	data := buildMatroska("matroska", "T", multiScopeTags(false))
	if v, _ := mustParseBytes(t, data).Get(tag.Encoder); len(v) != 2 {
		t.Fatalf("setup: want 2 ENCODER values across scopes, got %v", v)
	}

	// Edit an unrelated key (ENCODER itself is untouched): the album group rebuilds.
	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Artist, "NewArtist"))

	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Encoder); len(v) != 2 {
		t.Errorf("ENCODER = %v after unrelated edit, want both album+track values preserved", v)
	}
	// One ENCODER per scope: neither dropped nor duplicated.
	if n := bytes.Count(out, []byte("ENCODER")); n != 2 {
		t.Errorf("ENCODER written %d times, want 2 (one per scope)", n)
	}
	if f := re.Fields(); len(f.Artists) != 1 || f.Artists[0] != "NewArtist" {
		t.Errorf("edit not applied: artists=%v", f.Artists)
	}

	// A second, unrelated save must not drift the layout (re-drop or duplicate).
	out2, _ := saveMatroska(t, out, re.Edit().Set(tag.Album, "Alb"))
	if n := bytes.Count(out2, []byte("ENCODER")); n != 2 {
		t.Errorf("ENCODER written %d times after a second save, want 2 (idempotent)", n)
	}
	if v, _ := mustParseBytes(t, out2).Get(tag.Encoder); len(v) != 2 {
		t.Errorf("ENCODER = %v after a second save, want 2 preserved", v)
	}
}

// TestMatroskaWriteCrossScopeSplitNumberNoDuplicate (finding #1): a track-scoped
// slash number (PART_NUMBER=3/12) projects to TrackNumber=3 AND TrackTotal=12. When
// an unrelated tag is edited, neither must reappear at album scope - the covered set
// projects through the same slash split (not a bare key lookup), so TrackTotal is
// recognized as already carried and not split out to an album-scope TOTAL_PARTS.
func TestMatroskaWriteCrossScopeSplitNumberNoDuplicate(t *testing.T) {
	tags := mkEl(idTags, concat(
		mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA"))),
		mkEl(idTag, concat(
			mkEl(idTargets, concat(mkUint(idTgtTypeVal, 30), mkUint(idTagTrackUID, 7))),
			mkSimple("PART_NUMBER", "3/12"))),
	))
	data := buildMatroska("matroska", "T", tags)

	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Album, "X"))

	if n := bytes.Count(out, []byte("PART_NUMBER")); n != 1 {
		t.Errorf("PART_NUMBER written %d times, want 1 (no cross-scope duplication)", n)
	}
	// The TrackTotal half must not be split out to a separate album-scope SimpleTag.
	if n := bytes.Count(out, []byte("TOTAL_PARTS")); n != 0 {
		t.Errorf("TrackTotal re-emitted at album scope %d times, want 0", n)
	}
	re := mustParseBytes(t, out)
	if f := re.Fields(); f.TrackNumber != 3 || f.TrackTotal != 12 || f.Album != "X" {
		t.Errorf("round-trip wrong: %d/%d album=%q", f.TrackNumber, f.TrackTotal, f.Album)
	}
}

// TestMatroskaWriteSplitNumberComponentEdit (finding #1 regression guard): editing
// ONE half of a track-scoped slash number (PART_NUMBER=3/12, projecting TrackNumber=3
// AND TrackTotal=12) must keep the other half. Editing TrackNumber re-emits both at
// album scope (the slash tag is dropped); editing TrackTotal does the same - neither
// half is silently lost, and no stale cross-scope conflict is left behind.
func TestMatroskaWriteSplitNumberComponentEdit(t *testing.T) {
	build := func() []byte {
		return buildMatroska("matroska", "T", mkEl(idTags, concat(
			mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA"))),
			mkEl(idTag, concat(
				mkEl(idTargets, concat(mkUint(idTgtTypeVal, 30), mkUint(idTagTrackUID, 7))),
				mkSimple("PART_NUMBER", "3/12"))),
		)))
	}

	t.Run("edit TrackNumber keeps TrackTotal", func(t *testing.T) {
		data := build()
		out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.TrackNumber, "5"))
		re := mustParseBytes(t, out)
		if f := re.Fields(); f.TrackNumber != 5 || f.TrackTotal != 12 {
			t.Errorf("Set(TrackNumber,5): got %d/%d, want 5/12 (TrackTotal must survive)", f.TrackNumber, f.TrackTotal)
		}
		if hasWarning(re, wl.WarnConflictingFamilies) {
			t.Error("spurious cross-scope conflict after editing TrackNumber")
		}
	})

	t.Run("edit TrackTotal keeps TrackNumber and does not conflict", func(t *testing.T) {
		data := build()
		out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.TrackTotal, "20"))
		re := mustParseBytes(t, out)
		if f := re.Fields(); f.TrackNumber != 3 || f.TrackTotal != 20 {
			t.Errorf("Set(TrackTotal,20): got %d/%d, want 3/20 (TrackNumber must survive, total updated)", f.TrackNumber, f.TrackTotal)
		}
		if v, _ := re.Get(tag.TrackTotal); len(v) != 1 {
			t.Errorf("TrackTotal = %v after edit, want exactly [20] (no stale cross-scope copy)", v)
		}
		if hasWarning(re, wl.WarnConflictingFamilies) {
			t.Error("editing TrackTotal left a stale cross-scope TrackTotal conflict")
		}
	})

	// The same slash split applies to DISC=n/total (DiscNumber + DiscTotal), so editing
	// one component must keep the other - the drop predicate handles both numbering keys.
	t.Run("edit DiscNumber keeps DiscTotal", func(t *testing.T) {
		data := buildMatroska("matroska", "T", mkEl(idTags, concat(
			mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA"))),
			mkEl(idTag, concat(
				mkEl(idTargets, concat(mkUint(idTgtTypeVal, 30), mkUint(idTagTrackUID, 7))),
				mkSimple("DISC", "1/2"))),
		)))
		out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.DiscNumber, "3"))
		re := mustParseBytes(t, out)
		dn, _ := re.Get(tag.DiscNumber)
		dt, _ := re.Get(tag.DiscTotal)
		if len(dn) != 1 || dn[0] != "3" || len(dt) != 1 || dt[0] != "2" {
			t.Errorf("Set(DiscNumber,3) on DISC=1/2: DiscNumber=%v DiscTotal=%v, want [3] and [2]", dn, dt)
		}
	})
}

// TestMatroskaWriteTitleOnlyDropsScopedTitleTag: a title-only edit reaches a TITLE
// SimpleTag carried at another scope (which also projects into the canonical Title)
// and removes it, so the projection reads the single new title - the cross-scope
// removal contract applies to Title, not just SimpleTag-only keys. The unrelated
// tag sharing that group survives the forced Tags re-render.
func TestMatroskaWriteTitleOnlyDropsScopedTitleTag(t *testing.T) {
	tags := mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, concat(mkUint(idTgtTypeVal, 50), mkUint(idTagTrackUID, 7))),
		mkSimple("TITLE", "OldTrackTitle"), mkSimple("ARTIST", "TrackArtist"))))
	data := buildMatroska("matroska", "X", tags)

	if v, _ := mustParseBytes(t, data).Get(tag.Title); len(v) != 2 {
		t.Fatalf("setup: want Title=[Info.Title, track TITLE], got %v", v)
	}

	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Title, "NewX"))

	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Title); len(v) != 1 || v[0] != "NewX" {
		t.Errorf("title-only edit left a stale scoped TITLE: Get(Title)=%v, want [NewX]", v)
	}
	// The unrelated track ARTIST sharing the group survives the forced re-render.
	if v, _ := re.Get(tag.Artist); len(v) != 1 || v[0] != "TrackArtist" {
		t.Errorf("title edit dropped an unrelated scoped tag: ARTIST=%v", v)
	}
}

// TestMatroskaStripEncoderMultiScopePreservesEssence: the report's exact repro on
// the real transcoded fixture - clearing ENCODER reaches both the muxer (album) and
// codec (track) stamp, and the audio essence is byte-identical.
func TestMatroskaStripEncoderMultiScopePreservesEssence(t *testing.T) {
	src := readFixture(t, sampleWebM)
	if v, _ := mustParseBytes(t, src).Get(tag.Encoder); len(v) != 2 {
		t.Fatalf("setup: want 2 ENCODER values in sample.webm, got %v", v)
	}

	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().Clear(tag.Encoder))

	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Encoder); len(v) != 0 {
		t.Errorf("ENCODER survived clear across scopes: %v", v)
	}
	if hasWarning(re, wl.WarnConflictingFamilies) {
		t.Error("conflicting-families warning persists after multi-scope clear")
	}
	essenceUnchanged(t, src, out)
}

// TestMatroskaWriteMultipleTagsConsolidated: a file with two Tags masters is
// rewritten to a single Tags element (no doubled tag tree).
func TestMatroskaWriteMultipleTagsConsolidated(t *testing.T) {
	tags := concat(
		mkEl(idTags, mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA")))),
		mkEl(idTags, mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("COMPOSER", "CC")))),
	)
	data := buildMatroska("matroska", "T", tags)
	if got := countSegChildren(t, data, idTags); got != 2 {
		t.Fatalf("setup: want 2 Tags elements, got %d", got)
	}
	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Genre, "Jazz"))
	if got := countSegChildren(t, out, idTags); got != 1 {
		t.Errorf("output has %d Tags elements, want 1 (consolidated)", got)
	}
	f := mustParseBytes(t, out).Fields()
	if f.Artists[0] != "AA" || len(f.Composers) == 0 || f.Composers[0] != "CC" || f.Genres[0] != "Jazz" {
		t.Errorf("round-trip lost a value: artist=%v composer=%v genre=%v", f.Artists, f.Composers, f.Genres)
	}
}

// TestMatroskaWriteMultipleSeekHeadRefused: a linked (multi-)SeekHead layout is
// refused rather than copied with stale offsets.
func TestMatroskaWriteMultipleSeekHeadRefused(t *testing.T) {
	seek := mkEl(idSeekHead, nil)
	// Include a cluster so the file has audio essence: otherwise Editor.Prepare's
	// no-audio guard (H1) preempts the multi-SeekHead refusal this test exercises.
	seg := concat(seek, seek, mkAudioCluster(), mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA")))))
	data := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, seg))
	_, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "X").Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("multi-SeekHead edit err = %v, want ErrUnsupportedTag", err)
	}
}

// TestMatroskaWriteMultiValueTitleNotNoOp: adding a 2nd Title value is detected as
// a change (not a silent no-op), even though Info.Title stores only the first.
func TestMatroskaWriteMultiValueTitleNotNoOp(t *testing.T) {
	data := buildMatroska("matroska", "A", mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA")))))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "A", "B").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Report().NoOp {
		t.Error("adding a 2nd Title value should not be a no-op")
	}
}

// TestMatroskaWriteCoverRoleNormalized: the returned document's picture matches a
// fresh parse - a non-front-cover role normalizes to Other (Matroska names only
// cover/small_cover), not the input role.
func TestMatroskaWriteCoverRoleNormalized(t *testing.T) {
	src := readFixture(t, sampleMKA)
	out, outDoc := saveMatroska(t, src, mustParseBytes(t, src).Edit().
		ClearPictures().
		AddPicture(wl.Picture{Type: wl.PicBackCover, MIME: "image/png", Data: tinyPNG()}))
	got := outDoc.Pictures()
	reparsed := mustParseBytes(t, out).Pictures()
	if len(got) != 1 || len(reparsed) != 1 {
		t.Fatalf("pictures: returned %d, reparsed %d", len(got), len(reparsed))
	}
	if got[0].Type != reparsed[0].Type {
		t.Errorf("returned-doc role %v != reparse role %v", got[0].Type, reparsed[0].Type)
	}
	if got[0].Type != wl.PicOther {
		t.Errorf("back cover should normalize to Other on the wire, got %v", got[0].Type)
	}
}

// TestMatroskaBackCoverWarnsRoleLoss verifies that a non-front cover written to
// Matroska warns about role loss. Only cover/small_cover round-trip the front role;
// descriptions are preserved. A plain front cover does not warn, and the
// capability no longer advertises plain "lossless" pictures.
func TestMatroskaBackCoverWarnsRoleLoss(t *testing.T) {
	src := readFixture(t, sampleMKA)

	back := prepareWith(t, src, func(e *wl.Editor) {
		e.ClearPictures()
		e.AddPicture(wl.Picture{Type: wl.PicBackCover, MIME: "image/png", Description: "BackDesc", Data: tinyPNG()})
	})
	if !planWarns(t, back, wl.WarnPictureMetadataDropped) {
		t.Errorf("a back cover should warn picture-metadata-dropped; got %v", back.Report().Warnings)
	}

	front := prepareWith(t, src, func(e *wl.Editor) {
		e.ClearPictures()
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()})
	})
	if planWarns(t, front, wl.WarnPictureMetadataDropped) {
		t.Errorf("a plain front cover must not warn role loss; got %v", front.Report().Warnings)
	}

	// A PicOther picture already round-trips as Other (Matroska's small_cover), so it loses
	// no role and must not warn.
	other := prepareWith(t, src, func(e *wl.Editor) {
		e.ClearPictures()
		e.AddPicture(wl.Picture{Type: wl.PicOther, MIME: "image/png", Data: tinyPNG()})
	})
	if planWarns(t, other, wl.WarnPictureMetadataDropped) {
		t.Errorf("a PicOther picture round-trips as Other on Matroska; must not warn; got %v", other.Report().Warnings)
	}

	if fid := wl.CapabilitiesFor(wl.FormatMatroska).Pictures.Fidelity; fid == "lossless" {
		t.Errorf("Matroska pictures fidelity = %q, want it to disclose the non-front role loss", fid)
	}
}

// TestMatroskaWriteNoInfoTitleRefused: a Title edit on a (malformed) file with no
// Info element is refused cleanly rather than silently dropped or corrupting.
func TestMatroskaWriteNoInfoTitleRefused(t *testing.T) {
	data := buildMatroska("matroska", "", mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "A")))))
	_, err := mustParseBytes(t, data).Edit().Set(tag.Title, "nope").Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("no-Info Title edit err = %v, want ErrUnsupportedTag", err)
	}
	// A non-title tag edit on the same file is still fine.
	if _, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "B").Prepare(); err != nil {
		t.Errorf("tag edit on a no-Info file failed: %v", err)
	}
}

// TestMatroskaWebMCaseInsensitive: a cover write is refused even when the DocType
// is upper/mixed case ("WEBM"), matching the reader's case-insensitive check.
func TestMatroskaWebMCaseInsensitive(t *testing.T) {
	data := buildMatroska("WEBM", "Title", nil)
	_, err := mustParseBytes(t, data).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}).Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("cover into WEBM err = %v, want ErrUnsupportedTag", err)
	}
}

// TestMatroskaWebMRefusesCover confirms cover writes to WebM are refused (the
// Attachments element is outside the WebM subset), while a plain tag write is OK.
func TestMatroskaWebMRefusesCover(t *testing.T) {
	src := readFixture(t, sampleWebM)
	_, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}).Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("WebM cover Prepare err = %v, want ErrUnsupportedTag", err)
	}
	// A tag write to the same WebM file is fine.
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Artist, "WebM New"))
	if f := mustParseBytes(t, out).Fields(); f.Artists[0] != "WebM New" {
		t.Errorf("WebM tag write Artist = %v", f.Artists)
	}
}

// TestMatroskaWebMCapabilityFileAware: Document.Capabilities is file-aware - a
// parsed WebM file reports cover write unsupported (Attachments is outside the
// WebM subset) while a parsed Matroska file reports it AccessFull. This is what
// lets a transfer drop a cover onto WebM up front instead of advertising it
// carried and then failing at Plan, restoring report==result.
func TestMatroskaWebMCapabilityFileAware(t *testing.T) {
	webm := mustParseFile(t, sampleWebM).Capabilities()
	if webm.Pictures.Write != wl.AccessNone {
		t.Errorf("WebM Pictures.Write = %v, want AccessNone", webm.Pictures.Write)
	}
	// Tags stay fully writable on WebM - only attachments are gated.
	if webm.GenericField.Write != wl.AccessFull {
		t.Errorf("WebM tag write = %v, want AccessFull", webm.GenericField.Write)
	}
	if mka := mustParseFile(t, sampleMKA).Capabilities(); mka.Pictures.Write != wl.AccessFull {
		t.Errorf("Matroska Pictures.Write = %v, want AccessFull", mka.Pictures.Write)
	}
}
