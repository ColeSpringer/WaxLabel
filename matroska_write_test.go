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
	if f.Artists[0] != "AA" || len(f.Composer) == 0 || f.Composer[0] != "CC" || f.Genre[0] != "Jazz" {
		t.Errorf("round-trip lost a value: artist=%v composer=%v genre=%v", f.Artists, f.Composer, f.Genre)
	}
}

// TestMatroskaWriteMultipleSeekHeadRefused: a linked (multi-)SeekHead layout is
// refused rather than copied with stale offsets.
func TestMatroskaWriteMultipleSeekHeadRefused(t *testing.T) {
	seek := mkEl(idSeekHead, nil)
	seg := concat(seek, seek, mkEl(idTags, mkEl(idTag, concat(
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
