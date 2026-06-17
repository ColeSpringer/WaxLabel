package waxlabel_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// The write-side differential is the real proof of interoperability: after we
// edit and save a file, an independent tool (ffmpeg/ffprobe) must read back the
// values we wrote, and must accept our output as a valid FLAC stream. These
// tests skip cleanly when the tools are absent.

func TestDifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().
		Set(tag.Title, "Differential Title").
		Set(tag.Album, "Differential Album").
		Set(tag.RecordingDate, "2023-05").
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
	tags := probe.Format.Tags

	checks := map[string]string{
		"title":      "Differential Title",
		"album":      "Differential Album",
		"date":       "2023-05",
		"CUSTOM_TAG": "custom-value",
	}
	for k, want := range checks {
		if got := lookupCI(tags, k); got != want {
			t.Errorf("ffprobe tag %q = %q, want %q (all tags: %v)", k, got, want, tags)
		}
	}
}

func TestDifferentialFFmpegAcceptsOurOutput(t *testing.T) {
	requireTool(t, "ffmpeg")
	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, "Valid FLAC").AddPicture(wl.Picture{
		Type: wl.PicFrontCover, Data: tinyPNG(),
	}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	// Remux through ffmpeg with stream copy: this fully demuxes our metadata and
	// audio and fails loudly if anything is malformed.
	remux := path + ".remux.flac"
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-c", "copy", "-y", remux)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected our output: %v\n%s", err, out)
	}

	// And we can read the remuxed file back, with our title intact.
	if got := mustParseFile(t, remux).Fields().Title; got != "Valid FLAC" {
		t.Errorf("after ffmpeg remux, Title = %q, want Valid FLAC", got)
	}
}

// requireTool guards a differential test on the presence of an external CLI
// (ffprobe/ffmpeg). When the tool is missing it skips, so the suite stays green
// on machines without ffmpeg — unless WAXLABEL_REQUIRE_FFMPEG is set (as the CI
// differential job does), in which case a missing tool is a hard failure, so a
// broken ffmpeg install can't silently turn the write-side differential gate
// green. The env var covers both binaries, since ffprobe ships with ffmpeg.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err == nil {
		return
	}
	if os.Getenv("WAXLABEL_REQUIRE_FFMPEG") != "" {
		t.Fatalf("%s not found in PATH, but WAXLABEL_REQUIRE_FFMPEG is set "+
			"(it requires the full ffmpeg suite: ffmpeg and ffprobe)", name)
	}
	t.Skipf("%s not available", name)
}

// lookupCI looks up a key case-insensitively (ffmpeg lowercases standard Vorbis
// keys but preserves custom ones).
func lookupCI(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	for k, v := range m {
		if equalFold(k, key) {
			return v
		}
	}
	return ""
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
