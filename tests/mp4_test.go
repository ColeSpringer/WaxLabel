package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

const (
	sampleMP4 = "../testdata/sample.m4a"
	notagsMP4 = "../testdata/notags.m4a"
	sampleM4B = "../testdata/sample_chapters.m4b" // ffmpeg-authored: chpl + a QuickTime chapter track
)

// The ffmpeg-written M4B fixture (chpl plus a QuickTime chapter text track), read without
// ffmpeg.
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
	// A faststart file puts moov before mdat, so writing chapters shifts the audio mdat by the
	// moov delta (the chunk-offset fixup) while the chapter samples go in a fresh mdat at
	// end-of-file. ffmpeg must accept it and the audio must decode unchanged.
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

// ffmpeg references the chapter track it writes from the video track and the audio track alike.
// A clear takes it out of both, so ffmpeg finds no reference to a track that is gone, and the
// video stream stays.
func TestMP4DifferentialChapterClearOnVideoFile(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()
	meta := filepath.Join(dir, "chapters.txt")
	if err := os.WriteFile(meta, []byte(";FFMETADATA1\n"+
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=500\ntitle=One\n"+
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=500\nEND=1000\ntitle=Two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := ffmpegEncode(t, filepath.Join(dir, "va.mp4"),
		"-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-i", meta, "-map", "0:v", "-map", "1:a", "-map_chapters", "2", "-c:v", "mpeg4", "-c:a", "aac")
	doc := mustParseFile(t, path)
	if chs := doc.Chapters(); len(chs) != 2 || chs[1].Title != "Two" {
		t.Fatalf("setup: chapters %+v, want One and Two", chs)
	}
	plan, err := doc.Edit().ClearChapters().Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	chapters, log := ffprobeChapters(t, path)
	if len(chapters) != 0 || log != "" {
		t.Errorf("after the clear ffprobe reads %d chapter(s) and logs %q; want none and nothing", len(chapters), log)
	}
	streams, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe streams: %v", err)
	}
	if got := strings.Fields(string(streams)); !slices.Equal(got, []string{"mpeg4", "aac"}) {
		t.Errorf("streams after the clear: %q, want mpeg4 and aac", got)
	}
}

// ffmpeg takes the last "chap" list it reads for the whole movie. A new chapter track is not
// named from the thumbnail track itself, whose list would be that last one and hide the
// thumbnails.
func TestMP4DifferentialThumbnailTrackKeepsItsRole(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	path := ffmpegEncode(t, filepath.Join(t.TempDir(), "av.mp4"),
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x64:rate=5",
		"-map", "0:a", "-map", "1:v", "-c:a", "aac", "-c:v", "mpeg4")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The audio track, the first, names the video track, the second, as its thumbnails. ffmpeg
	// writes the moov last, so growing it moves no sample.
	moov, moovLen := mp4FindBox(t, data, 0, len(data), "moov")
	trak, trakLen := mp4FindBox(t, data, moov+8, moov+moovLen, "trak")
	if moov+moovLen != len(data) {
		t.Fatal("setup: the moov does not end the file")
	}
	named := mp4AfterTkhd(data[trak:trak+trakLen], testfile.MP4TrefChap(2))
	data = slices.Concat(data[:trak], named, data[trak+trakLen:])
	binary.BigEndian.PutUint32(data[moov:], uint32(moovLen+len(named)-trakLen))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	thumbnails := func() string {
		out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error", "-select_streams", "v:0",
			"-show_entries", "stream_disposition=attached_pic,timed_thumbnails", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatalf("ffprobe: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := thumbnails(); got != "1,1" {
		t.Fatalf("setup: ffprobe reads the video track's thumbnail flags as %q, want 1,1", got)
	}
	plan, err := mustParseFile(t, path).Edit().SetChapters(wl.Chapter{Title: "One"}, wl.Chapter{Start: 500 * time.Millisecond, Title: "Two"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	if chapters, log := ffprobeChapters(t, path); len(chapters) != 2 || chapters[1].Title != "Two" || log != "" || thumbnails() != "1,1" {
		t.Errorf("after the write ffprobe reads chapters %+v, logs %q and thumbnail flags %q; want One and Two, nothing and 1,1", chapters, log, thumbnails())
	}
}

// ffmpeg reads a chapter track's UTF-16 titles as WaxLabel does, and after a chapter is added
// both still read every title.
func TestMP4DifferentialUTF16ChapterTitles(t *testing.T) {
	requireTool(t, "ffprobe")
	titles := []string{"Caf\u00e9", "\u65e5\u672c\u8a9e", "Plain"}
	utf8File := rewriteChapters(t, readFixture(t, sampleM4B), func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(chaptersFromTitles(titles...)...)
	})
	read := func(path string) (ours, theirs []string) {
		doc := mustParseFile(t, path)
		for _, ch := range doc.Chapters() {
			ours = append(ours, ch.Title)
		}
		if hasWarning(doc, wl.WarnChapterSourceConflict) {
			t.Errorf("%s: the chpl and the chapter track disagree", filepath.Base(path))
		}
		chapters, log := ffprobeChapters(t, path)
		for _, ch := range chapters {
			theirs = append(theirs, ch.Title)
		}
		if log != "" {
			t.Errorf("%s: ffprobe logs %q", filepath.Base(path), log)
		}
		return ours, theirs
	}
	for name, order := range map[string]binary.AppendByteOrder{"be": binary.BigEndian, "le": binary.LittleEndian} {
		path := filepath.Join(t.TempDir(), name+".m4b")
		if err := os.WriteFile(path, mp4WidenChapterTitles(t, utf8File, order), 0o644); err != nil {
			t.Fatal(err)
		}
		if ours, theirs := read(path); !slices.Equal(ours, titles) || !slices.Equal(theirs, titles) {
			t.Fatalf("%s: titles %q, ffprobe %q; want %q from both", name, ours, theirs, titles)
		}
		doc := mustParseFile(t, path)
		plan, err := doc.Edit().SetChapters(append(doc.Chapters(), wl.Chapter{Start: 8 * time.Second, Title: "Added"})...).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		want := append(slices.Clone(titles), "Added")
		if ours, theirs := read(path); !slices.Equal(ours, want) || !slices.Equal(theirs, want) {
			t.Errorf("%s after a chapter is added: titles %q, ffprobe %q; want %q from both", name, ours, theirs, want)
		}
	}
}

// ffmpeg takes the chapter track's media duration for the last chapter's end, so a first chapter
// that starts late must not cut that short of where the chapter ends.
func TestMP4DifferentialLateFirstChapter(t *testing.T) {
	requireTool(t, "ffprobe")
	for name, c := range map[string]struct {
		last wl.Chapter
		end  time.Duration
	}{
		"open last chapter ends with the movie": {wl.Chapter{Start: 5 * time.Second, Title: "B"}, 9 * time.Second},
		"last chapter with an end":              {wl.Chapter{Start: 5 * time.Second, End: 8 * time.Second, Title: "B"}, 8 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			path := copyToTemp(t, sampleM4B)
			plan, err := mustParseFile(t, path).Edit().SetChapters(wl.Chapter{Start: 2 * time.Second, Title: "A"}, c.last).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
				t.Fatal(err)
			}
			want := []probeChapter{{2 * time.Second, 5 * time.Second, "A"}, {5 * time.Second, c.end, "B"}}
			if got, log := ffprobeChapters(t, path); !slices.Equal(got, want) || log != "" {
				t.Errorf("ffprobe reads %+v and logs %q; want %+v", got, log, want)
			}
			if chs := mustParseFile(t, path).Chapters(); len(chs) != 2 || chs[0].Start != 2*time.Second || chs[1].End != c.end {
				t.Errorf("chapters %+v; want the first at 2 s and the last ending at %v", chs, c.end)
			}
		})
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

// The ffmpeg-written fixture, read without ffmpeg, and an edit round-tripped through it.
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

// realPNG encodes a small, fully decodable PNG (unlike the truncated testfile.PNG sniff stub), so ffmpeg
// can treat an embedded cover as a real attached-picture stream when remuxing.
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

// genM4A creates an AAC-in-MP4 file with ffmpeg and the given metadata, the acquired-file case
// (ffmpeg also stamps an "encoder=Lavf" note).
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
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).
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

	// Remux with stream copy: ffmpeg fully demuxes the atoms and audio, failing if the
	// moov/stco/mdat layout is malformed.
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
	// Editing tags must not disturb the audio: the decoded PCM of the output must match
	// the decoded PCM of the original byte-for-byte.
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

// Editing QuickTime chapters in a shared mdat leaves the audio-essence digest unchanged:
// front-loaded chapter samples are excluded from the digest.
func TestMP4ChapterEditKeepsEssenceDigest(t *testing.T) {
	ctx := context.Background()
	before, err := mustParseFile(t, sampleM4B).HashAudioEssence(ctx)
	if err != nil {
		t.Fatalf("HashAudioEssence: %v", err)
	}
	if before.ExtentVersion != "mp4-samples-v4" || len(before.Sum) == 0 {
		t.Fatalf("digest = %s (version %q), want mp4-samples-v4 with a non-empty sum", before, before.ExtentVersion)
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

// MP4 duration uses the audio track's own edit-list-trimmed length, not the raw mdhd duration that
// includes AAC encoder priming. Bitrate is recomputed from the trimmed duration.
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

// ffmpeg output at 96 kHz, where the sample entry's 16.16 rate field cannot hold the rate. The
// .m4a leg carries a bare cookie on a v0 entry and the .mov leg a wave-wrapped one on a
// QuickTime v2 entry; both must report the codec configuration's geometry.
func TestMP4DifferentialFFmpegHiResALAC(t *testing.T) {
	requireTool(t, "ffmpeg")
	dir := t.TempDir()

	// Each leg encodes inside its own subtest, so a skip for a codec this ffmpeg cannot
	// write lands on the leg rather than on the whole table.
	encode := func(t *testing.T, name string, codecArgs ...string) string {
		t.Helper()
		return ffmpegSine(t, filepath.Join(dir, name), 2, 96000, codecArgs...)
	}

	for _, c := range []struct {
		name     string
		file     string
		codec    string
		bitDepth int
	}{
		{"m4a", "hires.m4a", "ALAC", 24},
		{"mov", "hires.mov", "ALAC", 24},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := encode(t, c.file, "-sample_fmt", "s32p", "-c:a", "alac")
			tr := mustParseFile(t, path).Properties().Tracks[0]
			if tr.SampleRate != 96000 || tr.Channels != 2 {
				t.Errorf("track = %d Hz / %d ch, want 96000/2", tr.SampleRate, tr.Channels)
			}
			if tr.Codec != c.codec || tr.BitsPerSample != c.bitDepth {
				t.Errorf("codec = %q / %d bit, want %s / %d", tr.Codec, tr.BitsPerSample, c.codec, c.bitDepth)
			}
		})
	}

	// ffmpeg 8 muxes FLAC into MP4 without the experimental flag; older builds need it.
	t.Run("flac", func(t *testing.T) {
		path := encode(t, "hires.mp4", "-c:a", "flac", "-strict", "experimental")
		tr := mustParseFile(t, path).Properties().Tracks[0]
		if tr.SampleRate != 96000 || tr.Channels != 2 || tr.Codec != "FLAC" {
			t.Errorf("track = %d Hz / %d ch / %q, want 96000/2/FLAC", tr.SampleRate, tr.Channels, tr.Codec)
		}
	})
}

// A plan reports an offset table shift only for a table an entry of which moves: none when the
// audio sits ahead of the moov, and never the table of a chapter track the write replaces.
func TestMP4OffsetShiftReportedOnlyWhenAnEntryMoves(t *testing.T) {
	long := strings.Repeat("Longer ", 40)
	title := func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, long) }
	chapters := func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("A", "B")...) }
	// An audio track holding the highest track id leaves none for a chapter track, so chapters go
	// to the chpl alone.
	chplOnly := func(moovLast bool) []byte {
		audio := testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 64))
		if moovLast {
			moov := testfile.MP4Atom("moov", testfile.MP4SoundTrak(-1, uint32(len(testfile.MP4Ftyp())+8)))
			return slices.Concat(testfile.MP4Ftyp(), audio, moov)
		}
		moov := testfile.MP4Atom("moov", testfile.MP4SoundTrak(-1, 0))
		at := len(testfile.MP4Ftyp()) + len(moov) + 8
		return slices.Concat(testfile.MP4Ftyp(), testfile.MP4Atom("moov", testfile.MP4SoundTrak(-1, uint32(at))), audio)
	}
	udtaText := testfile.MP4AssembleUdta(mp4UdtaText("\xa9nam", mp4QTTextEntry(langUnd, "T")))
	for _, c := range []struct {
		name string
		data []byte
		edit func(*wl.Editor) *wl.Editor
		want string
	}{
		{"ilst grow, moov last", readFixture(t, sampleMP4), title, ""},
		{"ilst grow, moov first", testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T")), title, "1 offset table shift(s)"},
		{"ilst creation, moov first", testfile.MP4AssembleUdta(), title, "1 offset table shift(s)"},
		{"udta text, moov last", readFixture(t, "../testdata/mp3.mov"), title, ""},
		{"udta text, moov first", udtaText, title, "1 offset table shift(s)"},
		{"chapter track, moov last", readFixture(t, sampleM4B), chapters, ""},
		{"chapter track, moov first", testfile.MP4QTFile([]int{0, 3000}, []string{"X", "Y"}), chapters, "1 offset table shift(s)"},
		{"chpl alone, moov last", chplOnly(true), chapters, ""},
		{"chpl alone, moov first", chplOnly(false), chapters, "1 offset table shift(s)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan, err := c.edit(mustParseBytes(t, c.data).Edit()).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			ops, got := plan.Report().Operations, ""
			if i := slices.IndexFunc(ops, func(op string) bool { return strings.Contains(op, "offset table shift") }); i >= 0 {
				got = ops[i]
			}
			if got != c.want {
				t.Errorf("operations %q, want the shift line %q", ops, c.want)
			}
			if out := applyToBytes(t, c.data, plan); !essenceOf(t, out).Equal(essenceOf(t, c.data)) {
				t.Error("the write changed the audio digest")
			}
		})
	}
}

// Of two moov atoms, as in two files joined end to end, the first is read and a write leaves the
// second file's bytes as they are.
func TestMP4SecondMoovWarnsFirstWins(t *testing.T) {
	second := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "Second"))
	data := slices.Concat(testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "First")), second)
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnDuplicateTagBlock) || doc.Fields().Title != "First" || nativeDuplicates(doc) != 1 {
		t.Fatalf("warnings %v, title %q, %d marked duplicate; want duplicate-tag-block, First and 1",
			warningCodes(doc), doc.Fields().Title, nativeDuplicates(doc))
	}
	plan, err := doc.Edit().Set(tag.Title, strings.Repeat("Grown ", 400)).Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if re := mustParseBytes(t, out); !strings.HasPrefix(re.Fields().Title, "Grown ") || !bytes.HasSuffix(out, second) {
		t.Errorf("title %q, second file kept %v", re.Fields().Title, bytes.HasSuffix(out, second))
	}
}
