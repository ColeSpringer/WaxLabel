package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

const (
	sampleMKA   = "testdata/sample.mka"
	sampleWebM  = "testdata/sample.webm"
	notagsMKA   = "testdata/notags.mka"
	chaptersMKA = "testdata/chapters.mka"
)

// TestMatroskaReadsSampleFixture exercises the committed real-ffmpeg fixture
// without needing ffmpeg at test time: the title comes from Info.Title, the
// numbering is split from ffmpeg's "n/total", and the date maps to RecordingDate.
func TestMatroskaReadsSampleFixture(t *testing.T) {
	doc := mustParseFile(t, sampleMKA)
	if doc.Format() != wl.FormatMatroska {
		t.Fatalf("Format = %v, want Matroska", doc.Format())
	}
	f := doc.Fields()
	if f.Title != "Sample Title" { // from Info.Title, not a TITLE tag
		t.Errorf("Title = %q, want Sample Title", f.Title)
	}
	if len(f.Artists) != 1 || f.Artists[0] != "Sample Artist" {
		t.Errorf("Artists = %v", f.Artists)
	}
	if f.Album != "Sample Album" || f.AlbumArtist != "Sample AlbumArtist" {
		t.Errorf("Album=%q AlbumArtist=%q", f.Album, f.AlbumArtist)
	}
	if f.TrackNumber != 2 || f.TrackTotal != 10 {
		t.Errorf("track = %d/%d, want 2/10", f.TrackNumber, f.TrackTotal)
	}
	if f.DiscNumber != 1 || f.DiscTotal != 1 {
		t.Errorf("disc = %d/%d, want 1/1", f.DiscNumber, f.DiscTotal)
	}
	if len(f.Genre) != 1 || f.Genre[0] != "Jazz" {
		t.Errorf("Genre = %v", f.Genre)
	}
	if f.RecordingDate != "2021" {
		t.Errorf("RecordingDate = %q, want 2021", f.RecordingDate)
	}
	if len(f.Composer) != 1 || f.Composer[0] != "Some Composer" {
		t.Errorf("Composer = %v", f.Composer)
	}
	if f.Comment != "hello" {
		t.Errorf("Comment = %q, want hello", f.Comment)
	}
}

// TestMatroskaProperties checks the audio geometry read from Segment.Tracks.
func TestMatroskaProperties(t *testing.T) {
	pr := mustParseFile(t, sampleMKA).Properties()
	if pr.Container != "Matroska" {
		t.Errorf("Container = %q, want Matroska", pr.Container)
	}
	if len(pr.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(pr.Tracks))
	}
	tr := pr.Tracks[0]
	if tr.Codec != "FLAC" || tr.SampleRate != 44100 || tr.Channels != 2 || tr.BitsPerSample != 16 {
		t.Errorf("track = %+v", tr)
	}
	if tr.Duration <= 0 {
		t.Errorf("Duration = %s, want > 0", tr.Duration)
	}
}

// TestMatroskaCoverArt confirms the cover image is read from an AttachedFile.
func TestMatroskaCoverArt(t *testing.T) {
	pics := mustParseFile(t, sampleMKA).Pictures()
	if len(pics) != 1 {
		t.Fatalf("pictures = %d, want 1", len(pics))
	}
	p := pics[0]
	if p.Type != wl.PicFrontCover {
		t.Errorf("type = %v, want front cover", p.Type)
	}
	if p.MIME != "image/png" || p.Width != 32 || p.Height != 32 {
		t.Errorf("pic = mime %q %dx%d", p.MIME, p.Width, p.Height)
	}
	if len(p.Data) == 0 {
		t.Error("picture data is empty")
	}
}

// TestMatroskaWebMFixture reads the committed Opus/WebM fixture. ffmpeg places
// the title in Info.Title here too, and stamps two ENCODER values at different
// targets, which must surface as a scope conflict.
func TestMatroskaWebMFixture(t *testing.T) {
	doc := mustParseFile(t, sampleWebM)
	f := doc.Fields()
	if f.Title != "WebM Title" || f.Album != "WebM Album" {
		t.Errorf("Title=%q Album=%q", f.Title, f.Album)
	}
	if len(f.Artists) != 1 || f.Artists[0] != "WebM Artist" {
		t.Errorf("Artists = %v", f.Artists)
	}
	if pr := doc.Properties(); pr.Container != "WebM" || pr.First().Codec != "Opus" {
		t.Errorf("container=%q codec=%q", pr.Container, pr.First().Codec)
	}

	// Two ENCODER tags at the segment and track targets disagree -> conflict.
	var encoderScopes []wl.Scope
	conflict := false
	for _, fv := range doc.Families() {
		if fv.Key == tag.EncodedBy {
			encoderScopes = append(encoderScopes, fv.Scope)
			if !fv.Selected {
				conflict = true
			}
		}
	}
	if len(encoderScopes) != 2 || !conflict {
		t.Errorf("ENCODEDBY family entries = %v (conflict=%v), want 2 conflicting", encoderScopes, conflict)
	}
}

// TestMatroskaNoTags reads the metadata-stripped fixture: ffmpeg still stamps the
// Lavf encoder, so the descriptive fields are empty but the inherited-encoder
// warning fires - the "sparse, not blank" acquired case.
func TestMatroskaNoTags(t *testing.T) {
	doc := mustParseFile(t, notagsMKA)
	f := doc.Fields()
	if f.Title != "" || f.Album != "" || len(f.Artists) != 0 {
		t.Errorf("expected no descriptive tags, got title=%q album=%q artists=%v", f.Title, f.Album, f.Artists)
	}
	if !hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Errorf("expected inherited-encoder warning, got %v", doc.Warnings())
	}
}

// TestMatroskaWritable confirms Matroska is tag-writable: it is Implemented and
// Writable, and capabilities report full tag/picture/chapter read and write.
func TestMatroskaWritable(t *testing.T) {
	if !wl.FormatMatroska.Implemented() {
		t.Error("Matroska should be Implemented")
	}
	if !wl.FormatMatroska.Writable() {
		t.Error("Matroska should be Writable")
	}
	doc := mustParseFile(t, sampleMKA)
	caps := doc.Capabilities()
	if caps.ReadOnly || caps.Format != wl.FormatMatroska {
		t.Errorf("caps = %+v, want writable Matroska", caps)
	}
	if caps.GenericField.Write != wl.AccessFull || caps.Pictures.Write != wl.AccessFull {
		t.Error("tag and picture write capabilities should be AccessFull")
	}
	if caps.GenericField.Read != wl.AccessFull {
		t.Error("read capability should be AccessFull")
	}
	if caps.Chapters.Read != wl.AccessFull || caps.Chapters.Write != wl.AccessFull {
		t.Error("chapters should be read+write Full")
	}
}

// TestMatroskaAudioEssence checks the audio-essence digest: it is non-empty,
// carries the Matroska extent version, and is deterministic across two parses.
func TestMatroskaAudioEssence(t *testing.T) {
	d1, err := mustParseFile(t, sampleMKA).HashAudioEssence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d1.ExtentVersion != "matroska-clusters-v1" || len(d1.Sum) == 0 {
		t.Errorf("digest = %s (version %q)", d1, d1.ExtentVersion)
	}
	d2, err := mustParseFile(t, sampleMKA).HashAudioEssence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !d1.Equal(d2) {
		t.Errorf("essence digest not deterministic: %s vs %s", d1, d2)
	}
}

// TestMatroskaScopeResolution synthesizes a file with album, track, edition, and
// chapter targets plus pass-through and technical tag names, checking the
// scope-aware projection and that the right things are and are not projected.
func TestMatroskaScopeResolution(t *testing.T) {
	tags := mkEl(idTags, concat(
		// Album-level (TargetTypeValue 50): ARTIST.
		mkEl(idTag, concat(
			mkEl(idTargets, mkUint(idTgtTypeVal, 50)),
			mkSimple("ARTIST", "Album Artist"),
		)),
		// Track-level (TargetTypeValue 30 + a track UID): TITLE + a split number +
		// a MusicBrainz pass-through + a technical tag that must be ignored.
		mkEl(idTag, concat(
			mkEl(idTargets, concat(mkUint(idTgtTypeVal, 30), mkUint(idTagTrackUID, 7))),
			mkSimple("TITLE", "Track Title"),
			mkSimple("PART_NUMBER", "3/12"),
			mkSimple("MUSICBRAINZ_ALBUMID", "abc-123"),
			mkSimple("DURATION", "00:00:09"),
		)),
		// Edition-level (TargetTypeValue 60).
		mkEl(idTag, concat(
			mkEl(idTargets, mkUint(idTgtTypeVal, 60)),
			mkSimple("LABEL", "Some Label"),
		)),
		// Chapter-level (a chapter UID forces chapter scope regardless of level).
		mkEl(idTag, concat(
			mkEl(idTargets, mkUint(idTagChapUID, 99)),
			mkSimple("COMMENT", "Chapter note"),
		)),
	))
	doc := mustParseBytes(t, buildMatroska("matroska", "", tags))

	wantScope := map[tag.Key]wl.Scope{
		tag.Artist:      wl.ScopeAlbum,
		tag.Title:       wl.ScopeTrack,
		tag.TrackNumber: wl.ScopeTrack,
		tag.MBReleaseID: wl.ScopeTrack,
		tag.Label:       wl.ScopeEdition,
		tag.Comment:     wl.ScopeChapter,
	}
	gotScope := map[tag.Key]wl.Scope{}
	for _, fv := range doc.Families() {
		gotScope[fv.Key] = fv.Scope
	}
	for k, want := range wantScope {
		if gotScope[k] != want {
			t.Errorf("scope[%s] = %v, want %v", k, gotScope[k], want)
		}
	}

	// Pass-through and split worked; the technical DURATION tag did not project.
	if v, _ := doc.Get(tag.MBReleaseID); len(v) != 1 || v[0] != "abc-123" {
		t.Errorf("MBReleaseID = %v", v)
	}
	f := doc.Fields()
	if f.TrackNumber != 3 || f.TrackTotal != 12 {
		t.Errorf("track = %d/%d, want 3/12", f.TrackNumber, f.TrackTotal)
	}
	if _, ok := doc.Get(tag.MustKey("DURATION")); ok {
		t.Error("technical DURATION tag should not be projected")
	}
}

// TestMatroskaTargetTypeString confirms a Targets element that carries only the
// informational TargetType string (no numeric TargetTypeValue) still scopes
// correctly - the Picard / hand-authored case.
func TestMatroskaTargetTypeString(t *testing.T) {
	tags := mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, mkStr(idTgtType, "TRACK")),
		mkSimple("ARTIST", "Track-scoped Artist"),
	)))
	doc := mustParseBytes(t, buildMatroska("matroska", "", tags))
	for _, fv := range doc.Families() {
		if fv.Key == tag.Artist && fv.Scope != wl.ScopeTrack {
			t.Errorf("ARTIST scope = %v, want track (from TargetType string)", fv.Scope)
		}
	}
}

// TestMatroskaUnknownSizeSegment confirms an unknown-size Segment (the streamed
// form) is still walked to end-of-file without panicking.
func TestMatroskaUnknownSizeSegment(t *testing.T) {
	tags := mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, nil),
		mkSimple("ARTIST", "Streamed Artist"),
	)))
	info := mkEl(idInfo, mkStr(idSegTitle, "Streamed"))
	body := concat(info, tags)
	// Segment with an explicit "unknown size" VINT (all ones, 1-byte form: 0xFF).
	seg := concat(idToBytes(idSegment), []byte{0xFF}, body)
	file := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), seg)

	doc := mustParseBytes(t, file)
	if doc.Fields().Title != "Streamed" {
		t.Errorf("Title = %q, want Streamed", doc.Fields().Title)
	}
	if v, _ := doc.Get(tag.Artist); len(v) != 1 || v[0] != "Streamed Artist" {
		t.Errorf("Artist = %v", v)
	}
}

// TestMatroskaUnknownSizeClusterTrailingTags confirms that Tags placed *after* an
// unknown-size (streamed) Cluster are still read: the parser resolves the
// cluster's end by skipping its children, rather than treating it as running to
// the segment end and dropping everything after it.
func TestMatroskaUnknownSizeClusterTrailingTags(t *testing.T) {
	info := mkEl(idInfo, mkStr(idSegTitle, "Streamed"))
	tracks := mkEl(idTracks, mkEl(idTrackEntry, concat(mkUint(idTrackType, 2), mkStr(idCodecID, "A_OPUS"))))
	// A Cluster with an explicit unknown-size VINT (0xFF), holding a couple of
	// definite-size children, followed by a Tags element at the segment level.
	clusterBody := concat(mkUint(idTimestamp, 0), mkEl(idSimpleBlock, make([]byte, 4)))
	cluster := concat(idToBytes(idCluster), []byte{0xFF}, clusterBody)
	tags := mkEl(idTags, mkEl(idTag, concat(mkEl(idTargets, nil), mkSimple("ARTIST", "Trailing Artist"))))
	seg := concat(info, tracks, cluster, tags)
	file := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, seg))

	doc := mustParseBytes(t, file)
	if v, _ := doc.Get(tag.Artist); len(v) != 1 || v[0] != "Trailing Artist" {
		t.Errorf("Artist after unknown-size cluster = %v, want [Trailing Artist]", v)
	}
}

// TestMatroskaHostileGeometry confirms a NaN/Inf sampling frequency and absurd
// integer geometry do not poison the track properties with garbage values.
func TestMatroskaHostileGeometry(t *testing.T) {
	var inf [8]byte
	binary.BigEndian.PutUint64(inf[:], math.Float64bits(math.Inf(1)))
	audio := mkEl(idAudio, concat(
		mkUint(idChannels, 0xFFFFFFFFFFFFFFFF),
		mkEl(idSampFreq, inf[:]),
		mkUint(idBitDepth, 0xFFFFFFFFFFFFFFFF),
	))
	track := mkEl(idTrackEntry, concat(mkUint(idTrackType, 2), mkStr(idCodecID, "A_OPUS"), audio))
	file := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, mkEl(idTracks, track)))

	tr := mustParseBytes(t, file).Properties().First()
	if tr.Channels != 0 || tr.SampleRate != 0 || tr.BitsPerSample != 0 {
		t.Errorf("hostile geometry not guarded: %+v", tr)
	}
}

// TestMatroskaNestedTagPreserved confirms a nested sub-tag is preserved in the
// native view even though only top-level SimpleTags project to the canonical set.
func TestMatroskaNestedTagPreserved(t *testing.T) {
	tags := mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, nil),
		concat(mkSimpleNested("PART_NUMBER", "5", mkSimple("TOTAL_PARTS", "20"))),
	)))
	doc := mustParseBytes(t, buildMatroska("matroska", "", tags))
	if doc.Fields().TrackNumber != 5 {
		t.Errorf("TrackNumber = %d, want 5", doc.Fields().TrackNumber)
	}
	// The nested TOTAL_PARTS is not projected (top-level only), but is preserved.
	native := doc.Native()
	if native == nil {
		t.Fatal("Native() is nil")
	}
	found := false
	for _, e := range native.Describe() {
		if e.Kind == "      TOTAL_PARTS" || e.Note == "20" {
			found = true
		}
	}
	if !found {
		t.Errorf("nested TOTAL_PARTS not found in native describe: %+v", native.Describe())
	}
}

// TestMatroskaRejectsOverLimitTag confirms a tag value larger than the configured
// MaxAllocBytes fails the parse with ErrSizeTooLarge rather than being silently
// dropped - the alloc limit is surfaced, not bypassed.
func TestMatroskaRejectsOverLimitTag(t *testing.T) {
	big := make([]byte, 4096)
	for i := range big {
		big[i] = 'x'
	}
	tags := mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, nil),
		mkEl(idSimpleTag, concat(mkStr(idTagName, "ARTIST"), mkEl(idTagString, big))),
	)))
	file := buildMatroska("matroska", "", tags)

	_, err := wl.Parse(context.Background(), wl.BytesSource(file),
		wl.WithLimits(wl.Limits{MaxAllocBytes: 1024, MaxDepth: 64}))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("Parse err = %v, want ErrSizeTooLarge", err)
	}
	// The same file parses fine with a generous limit.
	if _, err := wl.Parse(context.Background(), wl.BytesSource(file)); err != nil {
		t.Errorf("Parse with default limit: %v", err)
	}
}

// TestMatroskaNaNDuration confirms a NaN Duration float does not become a garbage
// track duration (the float->int64 conversion of NaN is implementation-defined).
func TestMatroskaNaNDuration(t *testing.T) {
	var nan [8]byte
	binary.BigEndian.PutUint64(nan[:], math.Float64bits(math.NaN()))
	info := mkEl(idInfo, concat(
		mkStr(idSegTitle, "NaN dur"),
		mkEl(idDuration, nan[:]),
	))
	tracks := mkEl(idTracks, mkEl(idTrackEntry, concat(
		mkUint(idTrackType, 2), mkStr(idCodecID, "A_OPUS"),
	)))
	file := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, concat(info, tracks)))

	doc := mustParseBytes(t, file)
	if d := doc.Properties().First().Duration; d != 0 {
		t.Errorf("Duration from NaN = %s, want 0", d)
	}
}

// TestMatroskaMultiTrackDuration confirms the segment duration is applied to every
// audio track, not just the first.
func TestMatroskaMultiTrackDuration(t *testing.T) {
	var dur [8]byte // Duration 1000.0 (x 1ms TimestampScale => 1s)
	binary.BigEndian.PutUint64(dur[:], math.Float64bits(1000))
	info := mkEl(idInfo, mkEl(idDuration, dur[:]))
	mkTrack := func(codec string) []byte {
		return mkEl(idTrackEntry, concat(mkUint(idTrackType, 2), mkStr(idCodecID, codec)))
	}
	tracks := mkEl(idTracks, concat(mkTrack("A_FLAC"), mkTrack("A_OPUS")))
	file := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, concat(info, tracks)))

	pr := mustParseBytes(t, file).Properties()
	if len(pr.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(pr.Tracks))
	}
	for i, tr := range pr.Tracks {
		if tr.Duration <= 0 {
			t.Errorf("track %d duration = %s, want > 0", i, tr.Duration)
		}
	}
}

// TestMatroskaPictureTypeNaming confirms cover-art role detection follows the
// Matroska convention and does not misfire on substrings like "background".
func TestMatroskaPictureTypeNaming(t *testing.T) {
	cases := []struct {
		name string
		want wl.PictureType
	}{
		{"cover.png", wl.PicFrontCover},
		{"small_cover.png", wl.PicOther},
		{"background.png", wl.PicOther}, // must NOT be a back cover
	}
	for _, c := range cases {
		att := mkEl(idAttachments, mkEl(idAttached, concat(
			mkStr(idFileName, c.name),
			mkStr(idFileMime, "image/png"),
			mkEl(idFileData, tinyPNG()),
		)))
		file := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, att))
		pics := mustParseBytes(t, file).Pictures()
		if len(pics) != 1 {
			t.Fatalf("%s: pictures = %d, want 1", c.name, len(pics))
		}
		if pics[0].Type != c.want {
			t.Errorf("%s: type = %v, want %v", c.name, pics[0].Type, c.want)
		}
	}
}

// TestMatroskaDifferentialFFmpeg is the read-side differential: ffmpeg writes a
// fresh file and our parser must read back exactly the tags ffmpeg was given.
func TestMatroskaDifferentialFFmpeg(t *testing.T) {
	requireTool(t, "ffmpeg")
	dir := t.TempDir()
	srcWav := filepath.Join(dir, "src.wav")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-ac", "2", "-ar", "44100",
		srcWav).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg src: %v\n%s", err, out)
	}
	path := filepath.Join(dir, "diff.mka")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-i", srcWav,
		"-metadata", "title=Diff Title", "-metadata", "artist=Diff Artist",
		"-metadata", "album=Diff Album", "-metadata", "genre=Metal",
		"-metadata", "track=4/9", "-c:a", "flac", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg mux: %v\n%s", err, out)
	}

	f := mustParseFile(t, path).Fields()
	if f.Title != "Diff Title" || f.Album != "Diff Album" {
		t.Errorf("Title=%q Album=%q", f.Title, f.Album)
	}
	if len(f.Artists) != 1 || f.Artists[0] != "Diff Artist" {
		t.Errorf("Artists = %v", f.Artists)
	}
	if len(f.Genre) != 1 || f.Genre[0] != "Metal" {
		t.Errorf("Genre = %v", f.Genre)
	}
	if f.TrackNumber != 4 || f.TrackTotal != 9 {
		t.Errorf("track = %d/%d, want 4/9", f.TrackNumber, f.TrackTotal)
	}
}

// FuzzMatroskaParse asserts the Matroska reader and writer never panic and never
// corrupt the essence on whatever they accept. It forces EBML detection by
// keeping the magic prefix, so arbitrary mutations exercise the codec itself
// (unknown-size elements, truncated VINTs, hostile lengths) rather than being
// routed elsewhere. A no-op write must reproduce the input; a Title edit either
// refuses cleanly (a layout the writer does not handle) or re-parses to the new
// title. Run with: go test -run x -fuzz FuzzMatroskaParse
func FuzzMatroskaParse(f *testing.F) {
	const magic = "\x1a\x45\xdf\xa3"
	for _, p := range []string{sampleMKA, sampleWebM, notagsMKA} {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(magic))
	f.Add([]byte(magic + "\x80\x18\x53\x80\x67\xff")) // header + unknown-size Segment
	f.Add([]byte(magic + "\xff"))                     // unknown-size header
	// Regression: an Info whose malformed "CRC-32" child has a junk size that
	// clamps to 4 bytes must not be mistaken for a real CRC - a title edit on it
	// once wrote a title that a re-parse could not read back.
	f.Add([]byte("\x810\x18S\x80gA0\x15I\xa9f\xc9\xbf0000000"))
	ctx := context.Background()
	f.Fuzz(func(t *testing.T, data []byte) {
		// Ensure the EBML magic leads so detection lands on Matroska.
		if len(data) < 4 || string(data[:4]) != magic {
			data = append([]byte(magic), data...)
		}
		doc, err := wl.Parse(ctx, wl.BytesSource(data))
		if err != nil {
			return // rejecting malformed input is fine; panicking is not
		}
		_ = doc.Fields()
		_ = doc.Properties()
		_ = doc.Pictures()
		_ = doc.Families()
		_ = doc.Warnings()
		_ = doc.Inspect()
		if _, err := doc.HashAudioEssence(ctx); err != nil {
			_ = err // hashing may fail on a degenerate extent; it must not panic
		}

		// A no-op write must reproduce the exact input bytes.
		if plan, err := doc.Edit().Prepare(); err == nil {
			var out bytes.Buffer
			if _, _, err := plan.Execute(ctx, wl.WriteTo(&out, wl.BytesSource(data))); err != nil {
				t.Fatalf("no-op write failed: %v", err)
			}
			if plan.IsNoOp() && !bytes.Equal(out.Bytes(), data) {
				t.Fatalf("no-op write changed bytes")
			}
		}

		// A Title edit must round-trip or refuse cleanly (a layout the writer does
		// not yet handle - no Void/overflow/etc. surfaces ErrUnsupportedTag).
		plan, err := doc.Edit().Set(tag.Title, "fuzz").Prepare()
		if err != nil {
			if errors.Is(err, waxerr.ErrUnsupportedTag) || errors.Is(err, waxerr.ErrInvalidData) {
				return
			}
			t.Fatalf("title edit prepare failed: %v", err)
		}
		var out bytes.Buffer
		if _, _, err := plan.Execute(ctx, wl.WriteTo(&out, wl.BytesSource(data))); err != nil {
			t.Fatalf("title edit write failed: %v", err)
		}
		re, err := wl.Parse(ctx, wl.BytesSource(out.Bytes()))
		if err != nil {
			t.Fatalf("re-parse of edited output failed: %v", err)
		}
		if got := re.Fields().Title; got != "fuzz" {
			t.Fatalf("edited title = %q, want fuzz", got)
		}
	})
}

// Test helpers: a minimal EBML writer.

// Element IDs needed by the synth tests (mirroring the unexported codec consts).
const (
	idEBML        = 0x1A45DFA3
	idDocType     = 0x4282
	idSegment     = 0x18538067
	idInfo        = 0x1549A966
	idDuration    = 0x4489
	idSegTitle    = 0x7BA9
	idTracks      = 0x1654AE6B
	idTrackEntry  = 0xAE
	idTrackType   = 0x83
	idCodecID     = 0x86
	idAudio       = 0xE1
	idChannels    = 0x9F
	idSampFreq    = 0xB5
	idBitDepth    = 0x6264
	idCluster     = 0x1F43B675
	idTimestamp   = 0xE7
	idSimpleBlock = 0xA3
	idTags        = 0x1254C367
	idTag         = 0x7373
	idTargets     = 0x63C0
	idTgtTypeVal  = 0x68CA
	idTgtType     = 0x63CA
	idTagTrackUID = 0x63C5
	idTagChapUID  = 0x63C4
	idSimpleTag   = 0x67C8
	idTagName     = 0x45A3
	idTagString   = 0x4487
	idAttachments = 0x1941A469
	idAttached    = 0x61A7
	idFileName    = 0x466E
	idFileMime    = 0x4660
	idFileData    = 0x465C
	// Chapters synth IDs (used by matroska_chapter_test.go).
	idChapters      = 0x1043A770
	idEditionEntry  = 0x45B9
	idEditionUID    = 0x45BC
	idEditionFlagDf = 0x45DB
	idChapterAtom   = 0xB6
	idChapterUID    = 0x73C4
	idChapTimeStart = 0x91
	idChapTimeEnd   = 0x92
	idChapDisplay   = 0x80
	idChapString    = 0x85
)

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// idToBytes emits an EBML element ID's bytes (the ID already carries its
// length-descriptor bits, so the byte count follows its magnitude).
func idToBytes(id uint64) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

// sizeVINT encodes a data size as a full 8-byte VINT (marker 0x01), which is
// always valid regardless of the value - the parser accepts any VINT length.
func sizeVINT(n int) []byte {
	v := uint64(n)
	return []byte{0x01, byte(v >> 48), byte(v >> 40), byte(v >> 32), byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

func mkEl(id uint64, payload []byte) []byte {
	return concat(idToBytes(id), sizeVINT(len(payload)), payload)
}

func mkStr(id uint64, s string) []byte { return mkEl(id, []byte(s)) }

func mkUint(id uint64, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return mkEl(id, b[:])
}

func mkSimple(name, value string) []byte {
	return mkEl(idSimpleTag, concat(mkStr(idTagName, name), mkStr(idTagString, value)))
}

func mkSimpleNested(name, value string, sub ...[]byte) []byte {
	return mkEl(idSimpleTag, concat(mkStr(idTagName, name), mkStr(idTagString, value), concat(sub...)))
}

// buildMatroska assembles a minimal definite-size file: EBML header + Segment
// containing an optional Info (with title) and the given Tags bytes.
func buildMatroska(docType, title string, tags []byte) []byte {
	var seg []byte
	if title != "" {
		seg = append(seg, mkEl(idInfo, mkStr(idSegTitle, title))...)
	}
	seg = append(seg, tags...)
	return concat(mkEl(idEBML, mkStr(idDocType, docType)), mkEl(idSegment, seg))
}
