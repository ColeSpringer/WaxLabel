package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

const (
	sampleMP4 = "../testdata/sample.m4a"
	notagsMP4 = "../testdata/notags.m4a"
	sampleM4B = "../testdata/sample_chapters.m4b" // ffmpeg-authored: chpl + a QuickTime chapter track
)

// TestMP4ReadsChapterFixture reads the committed real-ffmpeg M4B (chpl plus a
// QuickTime chapter text track) without needing ffmpeg at test time.
func TestMP4ReadsChapterFixture(t *testing.T) {
	doc := mustParseFile(t, sampleM4B)
	chs := doc.Chapters()
	if len(chs) != 3 {
		t.Fatalf("fixture chapters = %d, want 3", len(chs))
	}
	wantTitles := []string{"Opening Credits", "Chapter One", "Chapter Two"}
	for i, want := range wantTitles {
		if chs[i].Title != want {
			t.Errorf("chapter %d title = %q, want %q", i, chs[i].Title, want)
		}
	}
	if chs[1].Start != 3*time.Second {
		t.Errorf("chapter 1 start = %v, want 3s", chs[1].Start)
	}
	// The fixture's chpl and QuickTime track agree, so there must be no conflict.
	if hasWarning(doc, wl.WarnChapterSourceConflict) {
		t.Errorf("fixture chapter sources should agree; warnings = %v", doc.Warnings())
	}
	// Both representations show in the native view.
	kinds := map[string]bool{}
	for _, e := range doc.Native().Describe() {
		kinds[e.Kind] = true
	}
	if !kinds["moov.udta.chpl"] || !kinds["moov chapter track"] {
		t.Errorf("native view missing a chapter representation: %v", kinds)
	}
}

func TestMP4DifferentialChaptersChpl(t *testing.T) {
	requireTool(t, "ffprobe")
	// Writing chapters to a chapterless file produces a chpl that ffprobe reads
	// back (there is no competing QuickTime track here).
	path := genM4A(t, map[string]string{"title": "Book"})
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "Intro Chapter"},
		wl.Chapter{Start: 4 * time.Second, Title: "Second Chapter"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_chapters", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var probe struct {
		Chapters []struct {
			StartTime string `json:"start_time"`
			Tags      struct {
				Title string `json:"title"`
			} `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("parse ffprobe json: %v\n%s", err, out)
	}
	if len(probe.Chapters) != 2 {
		t.Fatalf("ffprobe saw %d chapters, want 2: %s", len(probe.Chapters), out)
	}
	if probe.Chapters[0].Tags.Title != "Intro Chapter" || probe.Chapters[1].Tags.Title != "Second Chapter" {
		t.Errorf("ffprobe chapter titles = %q, %q", probe.Chapters[0].Tags.Title, probe.Chapters[1].Tags.Title)
	}
}

func TestMP4DifferentialQTChapterTrack(t *testing.T) {
	requireTool(t, "ffprobe")
	requireTool(t, "ffmpeg")
	// A chapter edit builds a QuickTime chapter text track that ffmpeg reads.
	// ffprobe must list the chapter text stream and read the
	// chapters with End times (which only the QuickTime track carries, not the
	// chpl), and ffmpeg -c copy must demux and remux the whole layout - proving the
	// new trak, tref, and appended mdat are structurally valid.
	path := genM4A(t, map[string]string{"title": "Book"})
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "Opening"},
		wl.Chapter{Start: 500 * time.Millisecond, Title: "Closing"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	// The chapter text track shows up as a stream.
	streams, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_entries", "stream=codec_tag_string", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe streams: %v", err)
	}
	if !bytes.Contains(streams, []byte("text")) {
		t.Errorf("no QuickTime chapter text stream in output: %s", streams)
	}

	// ffprobe reads the chapters with End times from the QuickTime track.
	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_chapters", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe chapters: %v", err)
	}
	var probe struct {
		Chapters []struct {
			EndTime string `json:"end_time"` // seconds, independent of the track time_base
			Tags    struct {
				Title string `json:"title"`
			} `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("parse ffprobe json: %v\n%s", err, out)
	}
	if len(probe.Chapters) != 2 {
		t.Fatalf("ffprobe saw %d chapters, want 2: %s", len(probe.Chapters), out)
	}
	if probe.Chapters[0].Tags.Title != "Opening" || probe.Chapters[1].Tags.Title != "Closing" {
		t.Errorf("ffprobe titles = %q, %q", probe.Chapters[0].Tags.Title, probe.Chapters[1].Tags.Title)
	}
	// The QuickTime track ends chapter 0 at chapter 1's start (0.5 s). The chapter track's
	// media timescale is a fixed 90,000 (see chapterMediaTimescale), so ffprobe reports the
	// raw units in time_base 1/90000; end_time is the timescale-independent value in seconds.
	if probe.Chapters[0].EndTime != "0.500000" {
		t.Errorf("ffprobe chapter 0 end_time = %q, want \"0.500000\"", probe.Chapters[0].EndTime)
	}

	// A stream-copy remux fully demuxes the chapter track, tref, and appended mdat.
	remux := filepath.Join(t.TempDir(), "remux.m4a")
	if o, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-c", "copy", "-y", remux).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected our QuickTime chapter output: %v\n%s", err, o)
	}
	if chs := mustParseFile(t, remux).Chapters(); len(chs) != 2 || chs[1].Title != "Closing" {
		t.Errorf("chapters lost through an ffmpeg remux: %+v", chs)
	}
}

func TestMP4DifferentialQTChapterFaststart(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	// A faststart file puts moov before mdat, so writing chapters shifts the audio
	// mdat by the moov delta (the existing chunk-offset fixup) while the chapter
	// samples still land in a fresh mdat at end-of-file. ffmpeg must accept it and
	// the audio must decode unchanged - the stronger offset path.
	base := genM4A(t, map[string]string{"title": "FS"})
	path := filepath.Join(t.TempDir(), "fast.m4a")
	if o, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", base, "-c", "copy", "-movflags", "+faststart", "-y", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg faststart: %v\n%s", err, o)
	}
	pcmBefore := decodePCM(t, path)

	plan, err := mustParseFile(t, path).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "A"},
		wl.Chapter{Start: 500 * time.Millisecond, Title: "B"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	remux := filepath.Join(t.TempDir(), "fsremux.m4a")
	if o, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-c", "copy", "-y", remux).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected our faststart chapter output: %v\n%s", err, o)
	}
	if pcmAfter := decodePCM(t, path); !bytes.Equal(pcmBefore, pcmAfter) {
		t.Error("decoded audio changed after a faststart chapter edit (mdat shift wrong?)")
	}
	if chs := mustParseFile(t, path).Chapters(); len(chs) != 2 || chs[0].Title != "A" {
		t.Errorf("chapters wrong after faststart edit: %+v", chs)
	}
}

func TestMP4DifferentialChapterAudioUnchanged(t *testing.T) {
	requireTool(t, "ffmpeg")
	// A chapter edit must leave the audio bit-identical and produce output ffmpeg
	// accepts on a stream-copy remux.
	path := genM4A(t, map[string]string{"title": "Book"})
	pcmBefore := decodePCM(t, path)

	doc := mustParseFile(t, path)
	plan, err := doc.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "One"},
		wl.Chapter{Start: 2 * time.Second, Title: "Two"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	remux := filepath.Join(t.TempDir(), "remux.m4a")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-c", "copy", "-y", remux).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected our chapter output: %v\n%s", err, out)
	}
	pcmAfter := decodePCM(t, path)
	if len(pcmBefore) == 0 || len(pcmAfter) != len(pcmBefore) {
		t.Fatalf("PCM length changed: %d -> %d", len(pcmBefore), len(pcmAfter))
	}
	for i := range pcmBefore {
		if pcmBefore[i] != pcmAfter[i] {
			t.Fatalf("decoded audio differs at byte %d after a chapter edit", i)
		}
	}
}

// TestMP4ReadsSampleFixture exercises the committed real-ffmpeg fixture without
// needing ffmpeg at test time, and round-trips an edit through it.
func TestMP4ReadsSampleFixture(t *testing.T) {
	doc := mustParseFile(t, sampleMP4)
	f := doc.Fields()
	if f.Title != "Sample Title" || f.Album != "Sample Album" {
		t.Errorf("fixture tags: title=%q album=%q", f.Title, f.Album)
	}
	if len(f.Artists) != 1 || f.Artists[0] != "Sample Artist" {
		t.Errorf("fixture artists = %v", f.Artists)
	}
	if f.TrackNumber != 2 || f.TrackTotal != 10 {
		t.Errorf("fixture track = %d/%d, want 2/10", f.TrackNumber, f.TrackTotal)
	}
	if len(f.Genres) != 1 || f.Genres[0] != "Jazz" {
		t.Errorf("fixture genre = %v", f.Genres)
	}
	// Bitrate is derived from the audio-essence byte total and the track duration;
	// a real fixture with audio must report a positive average.
	if br := doc.Properties().First().Bitrate; br <= 0 {
		t.Errorf("MP4 bitrate = %d, want > 0", br)
	}

	path := copyToTemp(t, sampleMP4)
	plan, err := mustParseFile(t, path).Edit().Set(tag.Title, "Edited Fixture Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	if got := mustParseFile(t, path).Fields().Title; got != "Edited Fixture Title" {
		t.Errorf("title after save-back = %q", got)
	}
}

// realPNG encodes a small, fully decodable PNG (unlike the truncated tinyPNG
// sniff stub), so ffmpeg can treat an embedded cover as a real attached-picture
// stream when remuxing.
func realPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{uint8(x * 32), uint8(y * 32), 0x80, 0xFF})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// genM4A creates a real AAC-in-MP4 file with ffmpeg, with the given metadata. It
// is the realistic acquired-file case (ffmpeg also stamps an "encoder=Lavf"
// note). Tests skip cleanly when ffmpeg is absent.
func genM4A(t *testing.T, meta map[string]string) string {
	t.Helper()
	requireTool(t, "ffmpeg")
	path := filepath.Join(t.TempDir(), "gen.m4a")
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "sine=frequency=440:duration=1", "-c:a", "aac"}
	for k, v := range meta {
		args = append(args, "-metadata", k+"="+v)
	}
	args = append(args, "-y", path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg generate: %v\n%s", err, out)
	}
	return path
}

func TestMP4ReadsRealFFmpegFile(t *testing.T) {
	path := genM4A(t, map[string]string{"title": "Real Title", "artist": "Real Artist", "album": "Real Album"})
	doc := mustParseFile(t, path)
	if doc.Format() != wl.FormatMP4 {
		t.Fatalf("format = %v, want MP4", doc.Format())
	}
	f := doc.Fields()
	if f.Title != "Real Title" {
		t.Errorf("Title = %q", f.Title)
	}
	if len(f.Artists) != 1 || f.Artists[0] != "Real Artist" {
		t.Errorf("Artists = %v", f.Artists)
	}
	if f.Album != "Real Album" {
		t.Errorf("Album = %q", f.Album)
	}
	// ffmpeg stamps "Lavf..." into the encoder atom: surface it as inherited.
	if !hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Errorf("expected an inherited-encoder warning on an ffmpeg file; got %v", doc.Warnings())
	}
	if d := doc.Properties().Duration(); d <= 0 {
		t.Errorf("duration = %v, want > 0", d)
	}
}

func TestMP4DifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	path := genM4A(t, map[string]string{"title": "Old Title"})
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().
		Set(tag.Title, "Differential Title").
		Set(tag.Album, "Differential Album").
		Set(tag.Composer, "Differential Composer").
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_entries", "format_tags", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var probe struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("parse ffprobe json: %v\n%s", err, out)
	}
	checks := map[string]string{
		"title":    "Differential Title",
		"album":    "Differential Album",
		"composer": "Differential Composer",
	}
	for k, want := range checks {
		if got := lookupCI(probe.Format.Tags, k); got != want {
			t.Errorf("ffprobe tag %q = %q, want %q (all: %v)", k, got, want, probe.Format.Tags)
		}
	}
}

func TestMP4DifferentialFFmpegAcceptsOurOutput(t *testing.T) {
	path := genM4A(t, map[string]string{"title": "Before"})
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, "After Remux").
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: realPNG(t)}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	// Remux with stream copy: ffmpeg fully demuxes our atoms and audio, failing
	// loudly if the moov/stco/mdat layout is malformed.
	remux := filepath.Join(t.TempDir(), "remux.m4a")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-c", "copy", "-y", remux)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected our output: %v\n%s", err, out)
	}
	if got := mustParseFile(t, remux).Fields().Title; got != "After Remux" {
		t.Errorf("after ffmpeg remux, Title = %q, want After Remux", got)
	}
}

func TestMP4DifferentialDecodeUnchanged(t *testing.T) {
	requireTool(t, "ffmpeg")
	// Editing tags must not disturb the audio: the decoded PCM of our output must
	// match the decoded PCM of the original byte-for-byte.
	path := genM4A(t, map[string]string{"title": "Pre"})
	pcmBefore := decodePCM(t, path)

	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, "A Longer Title That Grows The Metadata Region A Lot").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	pcmAfter := decodePCM(t, path)
	if len(pcmBefore) == 0 || len(pcmAfter) != len(pcmBefore) {
		t.Fatalf("PCM length changed: %d -> %d", len(pcmBefore), len(pcmAfter))
	}
	for i := range pcmBefore {
		if pcmBefore[i] != pcmAfter[i] {
			t.Fatalf("decoded audio differs at byte %d after a tag edit", i)
		}
	}
}

// decodePCM decodes a file to raw PCM via ffmpeg for an audio-integrity check.
func decodePCM(t *testing.T, path string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), filepath.Base(path)+".pcm")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-f", "s16le", "-ac", "2", "-ar", "44100", "-y", out)
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg decode: %v\n%s", err, o)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestMP4ChapterEditKeepsEssenceDigest checks that editing QuickTime chapters in a shared
// mdat leaves the audio-essence digest unchanged. Front-loaded chapter samples are
// excluded from the digest, so retitling chapters does not change the audio fingerprint.
func TestMP4ChapterEditKeepsEssenceDigest(t *testing.T) {
	ctx := context.Background()
	before, err := mustParseFile(t, sampleM4B).HashAudioEssence(ctx)
	if err != nil {
		t.Fatalf("HashAudioEssence: %v", err)
	}
	if before.ExtentVersion != "mp4-mdat-v3" || len(before.Sum) == 0 {
		t.Fatalf("digest = %s (version %q), want mp4-mdat-v3 with a non-empty sum", before, before.ExtentVersion)
	}

	path := copyToTemp(t, sampleM4B)
	plan, err := mustParseFile(t, path).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "Retitled One"},
		wl.Chapter{Start: 3 * time.Second, Title: "Retitled Two"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	after, err := mustParseFile(t, path).HashAudioEssence(ctx)
	if err != nil {
		t.Fatalf("HashAudioEssence after: %v", err)
	}
	if !before.Equal(after) {
		t.Errorf("chapter edit changed the essence digest:\n before=%s\n after =%s", before, after)
	}
}

// TestMP4DurationIsEditListTrimmed checks that MP4 duration uses the audio track's own
// edit-list-trimmed length, not the raw mdhd duration that includes AAC encoder priming.
// Bitrate is recomputed from the trimmed duration.
func TestMP4DurationIsEditListTrimmed(t *testing.T) {
	tr := mustParseFile(t, sampleMP4).Properties().First()
	// sample.m4a trims to ~1.000s; the raw mdhd is ~1.023s and is excluded.
	if tr.Duration < 995*time.Millisecond || tr.Duration > 1010*time.Millisecond {
		t.Errorf("duration = %v, want ~1.000s (edit-list-trimmed), not the ~1.023s raw mdhd", tr.Duration)
	}
	if tr.Bitrate <= 0 {
		t.Error("bitrate should be recomputed (positive) from the trimmed duration")
	}
}
