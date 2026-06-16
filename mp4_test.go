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

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

const (
	sampleMP4 = "testdata/sample.m4a"
	notagsMP4 = "testdata/notags.m4a"
)

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
	if len(f.Genre) != 1 || f.Genre[0] != "Jazz" {
		t.Errorf("fixture genre = %v", f.Genre)
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
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
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
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
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
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
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
