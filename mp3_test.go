package waxlabel_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

const (
	sampleMP3   = "testdata/sample.mp3"
	sampleMP324 = "testdata/sample24.mp3"
	notagsMP3   = "testdata/notags.mp3"
)

func TestMP3Parse(t *testing.T) {
	cases := []struct {
		path  string
		date  string
		genre string
		hasV1 bool
	}{
		{sampleMP3, "2021", "Rock", true},
		{sampleMP324, "2021-06-15", "Jazz", false},
	}
	for _, c := range cases {
		doc := mustParseFile(t, c.path)
		if doc.Format() != wl.FormatMP3 {
			t.Errorf("%s: format = %v, want MP3", c.path, doc.Format())
		}
		f := doc.Fields()
		if f.Title != "Sample Title" {
			t.Errorf("%s: title = %q", c.path, f.Title)
		}
		if len(f.Artists) != 1 || f.Artists[0] != "Sample Artist" {
			t.Errorf("%s: artists = %v", c.path, f.Artists)
		}
		if f.Album != "Sample Album" {
			t.Errorf("%s: album = %q", c.path, f.Album)
		}
		if f.RecordingDate != c.date {
			t.Errorf("%s: date = %q, want %q", c.path, f.RecordingDate, c.date)
		}
		if len(f.Genre) != 1 || f.Genre[0] != c.genre {
			t.Errorf("%s: genre = %v, want %q", c.path, f.Genre, c.genre)
		}
		if f.TrackNumber != 3 || f.TrackTotal != 12 {
			t.Errorf("%s: track = %d/%d, want 3/12", c.path, f.TrackNumber, f.TrackTotal)
		}
		tr := doc.Properties().First()
		if tr.SampleRate != 44100 {
			t.Errorf("%s: sample rate = %d, want 44100", c.path, tr.SampleRate)
		}
		if tr.Duration <= 0 {
			t.Errorf("%s: duration = %v, want > 0", c.path, tr.Duration)
		}
		if !hasWarning(doc, wl.WarnInheritedEncoder) {
			t.Errorf("%s: expected inherited-encoder warning, got %v", c.path, doc.Warnings())
		}
		if c.hasV1 && !hasWarning(doc, wl.WarnTrailingID3v1) {
			t.Errorf("%s: expected trailing-id3v1 warning", c.path)
		}
		if len(doc.Families()) == 0 {
			t.Errorf("%s: families should be non-empty", c.path)
		}
	}
}

func TestMP3ParseNoTags(t *testing.T) {
	doc := mustParseFile(t, notagsMP3)
	if doc.Format() != wl.FormatMP3 {
		t.Fatalf("format = %v, want MP3", doc.Format())
	}
	if doc.Tags().Len() != 0 {
		t.Errorf("expected no tags, got %d", doc.Tags().Len())
	}
	if doc.Properties().First().SampleRate != 44100 {
		t.Errorf("sample rate = %d, want 44100", doc.Properties().First().SampleRate)
	}
	if doc.Properties().Duration() <= 0 {
		t.Errorf("duration should be > 0 even without tags")
	}
}

func TestMP3EssenceStableAcrossTagEdit(t *testing.T) {
	for _, f := range []string{sampleMP3, sampleMP324, notagsMP3} {
		src := readFixture(t, f)
		before := essenceOf(t, src)

		plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Edited").Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		out := applyToBytes(t, src, plan)
		if after := essenceOf(t, out); !before.Equal(after) {
			t.Errorf("%s: audio essence changed across a tag edit", f)
		}
		got := mustParseBytes(t, out)
		if got.Fields().Title != "Edited" {
			t.Errorf("%s: title = %q after edit", f, got.Fields().Title)
		}
		if before.ExtentVersion != "mp3-frames-v1" {
			t.Errorf("%s: extent version = %q", f, before.ExtentVersion)
		}
	}
}

// Write-side differential: an independent tool must read what we wrote and
// accept our audio. These skip cleanly when ffmpeg/ffprobe are absent.

func TestMP3DifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	for _, f := range []string{sampleMP3, sampleMP324} {
		path := copyToTemp(t, f)
		plan, err := mustParseFile(t, path).Edit().
			Set(tag.Title, "Differential Title").
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
			"-show_entries", "format_tags", "-of", "json", path).Output()
		if err != nil {
			t.Fatalf("%s: ffprobe: %v", f, err)
		}
		var probe struct {
			Format struct {
				Tags map[string]string `json:"tags"`
			} `json:"format"`
		}
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("%s: parse ffprobe json: %v\n%s", f, err, out)
		}
		for k, want := range map[string]string{
			"title": "Differential Title", "album": "Differential Album", "CUSTOM_TAG": "custom-value",
		} {
			if got := lookupCI(probe.Format.Tags, k); got != want {
				t.Errorf("%s: ffprobe tag %q = %q, want %q (all: %v)", f, k, got, want, probe.Format.Tags)
			}
		}
	}
}

func TestMP3DifferentialFFmpegDecodes(t *testing.T) {
	requireTool(t, "ffmpeg")
	for _, f := range []string{sampleMP3, sampleMP324} {
		path := copyToTemp(t, f)
		plan, err := mustParseFile(t, path).Edit().
			Set(tag.Title, "Valid MP3").
			AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}).
			Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		// Decode the audio stream: this fails loudly if our framing is broken.
		cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
			"-i", path, "-map", "0:a", "-f", "null", "-")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: ffmpeg rejected our output: %v\n%s", f, err, out)
		}
		if got := mustParseFile(t, path).Fields().Title; got != "Valid MP3" {
			t.Errorf("%s: title after edit = %q", f, got)
		}
	}
}
