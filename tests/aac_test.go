package waxlabel_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

const (
	sampleAAC = "../testdata/sample.aac" // ffmpeg-authored: front ID3v2 + ADTS
	notagsAAC = "../testdata/notags.aac" // bare ADTS, no ID3
	heaacAAC  = "../testdata/heaac_v1.aac"
)

func TestAACParse(t *testing.T) {
	doc := mustParseFile(t, sampleAAC)
	if doc.Format() != wl.FormatAAC {
		t.Fatalf("format = %v, want AAC", doc.Format())
	}
	f := doc.Fields()
	if f.Title != "Sample Title" {
		t.Errorf("title = %q", f.Title)
	}
	if len(f.Artists) != 1 || f.Artists[0] != "Sample Artist" {
		t.Errorf("artists = %v", f.Artists)
	}
	if f.Album != "Sample Album" {
		t.Errorf("album = %q", f.Album)
	}
	if f.RecordingDate != "2021" {
		t.Errorf("date = %q, want 2021", f.RecordingDate)
	}
	if len(f.Genres) != 1 || f.Genres[0] != "Rock" {
		t.Errorf("genre = %v, want [Rock]", f.Genres)
	}
	if f.TrackNumber != 3 || f.TrackTotal != 12 {
		t.Errorf("track = %d/%d, want 3/12", f.TrackNumber, f.TrackTotal)
	}
	tr := doc.Properties().First()
	if tr.SampleRate != 44100 {
		t.Errorf("sample rate = %d, want 44100", tr.SampleRate)
	}
	if tr.Channels != 1 {
		t.Errorf("channels = %d, want 1", tr.Channels)
	}
	if tr.Codec != "AAC" {
		t.Errorf("codec = %q, want AAC", tr.Codec)
	}
	// The AAC object type is preserved as the profile under the canonical "AAC".
	if tr.CodecProfile != "AAC LC" {
		t.Errorf("codec profile = %q, want AAC LC", tr.CodecProfile)
	}
	if tr.Duration <= 0 {
		t.Errorf("duration = %v, want > 0", tr.Duration)
	}
	// ffmpeg stamps an "encoder=Lavf..." (TSSE) frame: the inherited-encoder signal.
	if !hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Errorf("expected inherited-encoder warning, got %v", doc.Warnings())
	}
}

func TestAACParseNoTags(t *testing.T) {
	doc := mustParseFile(t, notagsAAC)
	if doc.Format() != wl.FormatAAC {
		t.Fatalf("format = %v, want AAC", doc.Format())
	}
	if doc.Tags().Len() != 0 {
		t.Errorf("expected no tags, got %d", doc.Tags().Len())
	}
	tr := doc.Properties().First()
	if tr.SampleRate != 44100 {
		t.Errorf("sample rate = %d, want 44100", tr.SampleRate)
	}
	if tr.Channels != 1 {
		t.Errorf("channels = %d, want 1", tr.Channels)
	}
	if tr.Duration <= 0 {
		t.Error("duration should be > 0 even without tags (cheap first-frame estimate)")
	}
}

// TestAACEssenceStableAcrossTagEdit proves the ADTS stream is copied verbatim:
// the essence digest is unchanged across a tag edit, for both a tagged file
// (ID3 region resized) and a bare file (ID3 region created from nothing).
func TestAACEssenceStableAcrossTagEdit(t *testing.T) {
	for _, f := range []string{sampleAAC, notagsAAC} {
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
		if before.ExtentVersion != "aac-adts-v1" {
			t.Errorf("%s: extent version = %q, want aac-adts-v1", f, before.ExtentVersion)
		}
	}
}

// Write-side differential: an independent tool must read what we wrote and
// accept our audio. These skip cleanly when ffmpeg/ffprobe are absent.

func TestAACDifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	// Both fixtures: the tagged one resizes an existing ID3; the bare one has a
	// fresh ID3v2 created where there was none.
	for _, f := range []string{sampleAAC, notagsAAC} {
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

func TestAACDifferentialFFmpegDecodes(t *testing.T) {
	requireTool(t, "ffmpeg")
	for _, f := range []string{sampleAAC, notagsAAC} {
		path := copyToTemp(t, f)
		plan, err := mustParseFile(t, path).Edit().
			Set(tag.Title, "Valid AAC").
			AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}).
			Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		// Decode the audio stream: this fails loudly if our ADTS framing is broken
		// (e.g. the new ID3 ran into the first frame).
		cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
			"-i", path, "-map", "0:a", "-f", "null", "-")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: ffmpeg rejected our output: %v\n%s", f, err, out)
		}
		if got := mustParseFile(t, path).Fields().Title; got != "Valid AAC" {
			t.Errorf("%s: title after edit = %q", f, got)
		}
	}
}

// TestAACImplicitSBRDetected: ADTS carries no SBR signalling, so the frames themselves are
// parsed; the fixtures decode to the played geometry ffprobe reports, and the ADTS twin of
// an MP4 stream now reports what the MP4 does.
func TestAACImplicitSBRDetected(t *testing.T) {
	cases := []struct {
		path           string
		rate, channels int
		profile        string
	}{
		{heaacAAC, 44100, 2, "HE-AAC"},
		{"../testdata/heaac_v2.aac", 48000, 2, "HE-AAC v2"},
		{"../testdata/notags.aac", 44100, 1, "AAC LC"},
	}
	for _, c := range cases {
		tr := mustParseFile(t, c.path).Properties().Tracks[0]
		if tr.SampleRate != c.rate || tr.Channels != c.channels || tr.Codec != "AAC" || tr.CodecProfile != c.profile {
			t.Errorf("%s: %d Hz / %d ch %s/%s, want %d/%d AAC/%s", c.path, tr.SampleRate, tr.Channels, tr.Codec, tr.CodecProfile, c.rate, c.channels, c.profile)
		}
		if tr.SampleRate > 0 && tr.TotalSamples > 0 {
			want := core.SamplesToDuration(tr.TotalSamples, tr.SampleRate)
			if d := tr.Duration - want; d < -time.Millisecond || d > time.Millisecond {
				t.Errorf("%s: TotalSamples %d at %d Hz is %v, but Duration is %v", c.path, tr.TotalSamples, tr.SampleRate, want, tr.Duration)
			}
		}
	}
}

// TestAACDigestUnchangedBySBRDetection pins the essence digest of the HE-AAC fixtures to the
// values recorded before frame parsing existed, so the salt still comes from the header alone.
func TestAACDigestUnchangedBySBRDetection(t *testing.T) {
	for path, want := range map[string]string{
		heaacAAC:                   "sha256/aac-adts-v1:0a36515dc52e76b86865cd32390377203874295adc8f99744146f4c67688a719",
		"../testdata/heaac_v2.aac": "sha256/aac-adts-v1:3b5da3c46f7200e2f01f3137f2086ac3c816c92927739a0b1aa6bff848050ce3",
	} {
		got, err := mustParseFile(t, path).HashAudioEssence(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != want {
			t.Errorf("%s digest = %s, want %s", path, got, want)
		}
	}
}
