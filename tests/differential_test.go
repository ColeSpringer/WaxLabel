package waxlabel_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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
// on machines without ffmpeg - unless WAXLABEL_REQUIRE_FFMPEG is set (as the CI
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

// ffmpegRequired reports whether a differential leg that cannot run here is a failure
// rather than a skip: the CI differential job sets it, so a broken ffmpeg cannot turn the
// gate green by skipping.
func ffmpegRequired() bool { return os.Getenv("WAXLABEL_REQUIRE_FFMPEG") != "" }

// ffmpegEncode runs ffmpeg with args and writes path, returning it. An encode ffmpeg
// cannot do here (a codec this build lacks) skips the leg, or fails it under
// WAXLABEL_REQUIRE_FFMPEG. t is the T of the leg, so a Fatal or Skip lands on the
// goroutine running it.
func ffmpegEncode(t *testing.T, path string, args ...string) string {
	t.Helper()
	full := append([]string{"-hide_banner", "-loglevel", "error"}, args...)
	if out, err := exec.Command("ffmpeg", append(full, "-y", path)...).CombinedOutput(); err != nil {
		if ffmpegRequired() {
			t.Fatalf("ffmpeg %s: %v\n%s", filepath.Base(path), err, out)
		}
		t.Skipf("ffmpeg cannot encode %s here: %v\n%s", filepath.Base(path), err, out)
	}
	return path
}

// probeStream is what ffprobe makes of a file's first audio stream: every field the
// differential tests compare, from one invocation. ffprobe writes most numbers as strings
// in its JSON and leaves a field out, or writes "N/A", when the demuxer does not set it,
// so an optional number reads as 0 when absent and Profile is empty then. SampleRate and
// Channels are never absent for a stream ffprobe reads, so a witness missing them is an
// error, not a 0 a comparison could pass on. TimeBase is the unit DurationTS counts in.
type probeStream struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
	BitRate       int
	DurationTS    int64
	Duration      time.Duration
	Profile       string
	TimeBase      string
}

// ffprobeStream is the independent witness for every property this library derives
// rather than copies: a rate a codec configuration declares, a width a fourcc fixes, a
// length a packet count implies.
func ffprobeStream(t *testing.T, path string) probeStream {
	t.Helper()
	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=sample_rate,channels,profile,bits_per_sample,duration_ts,duration,bit_rate,time_base",
		"-of", "json", path).Output()
	if err != nil {
		// ffprobe says why on stderr - "missing mandatory atoms", an unreadable config -
		// and without it a failure here is just an exit status.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("ffprobe %s: %v\n%s", path, err, ee.Stderr)
		}
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var probe struct {
		Streams []map[string]any `json:"streams"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("ffprobe %s: %v\n%s", path, err, out)
	}
	if len(probe.Streams) == 0 {
		t.Fatalf("ffprobe %s: no audio stream\n%s", path, out)
	}
	fields := probe.Streams[0]
	number := func(field string) float64 {
		switch v := fields[field].(type) {
		case nil:
			return 0
		case float64:
			return v
		case string:
			if v == "" || v == "N/A" {
				return 0
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				t.Fatalf("ffprobe %s: %s %q: %v", path, field, v, err)
			}
			return f
		default:
			t.Fatalf("ffprobe %s: %s is %T, want a number or string", path, field, v)
			return 0
		}
	}
	profile, _ := fields["profile"].(string)
	timeBase, _ := fields["time_base"].(string)
	p := probeStream{
		SampleRate:    int(number("sample_rate")),
		Channels:      int(number("channels")),
		BitsPerSample: int(number("bits_per_sample")),
		BitRate:       int(number("bit_rate")),
		DurationTS:    int64(number("duration_ts")),
		Duration:      time.Duration(number("duration") * float64(time.Second)),
		Profile:       profile,
		TimeBase:      timeBase,
	}
	if p.SampleRate == 0 || p.Channels == 0 {
		t.Fatalf("ffprobe %s: no sample rate or channel count for the audio stream\n%s", path, out)
	}
	return p
}

// ffmpegSine encodes one second of a 1 kHz sine at the given geometry with codecArgs
// naming the codec (and any option it needs), the source every differential encode here
// starts from, and returns path.
func ffmpegSine(t *testing.T, path string, channels, rate int, codecArgs ...string) string {
	t.Helper()
	args := []string{"-f", "lavfi", "-i", "sine=frequency=1000:duration=1",
		"-ac", strconv.Itoa(channels), "-ar", strconv.Itoa(rate)}
	return ffmpegEncode(t, path, append(args, codecArgs...)...)
}

// ffprobeAudio reports the played sample rate, channel count and profile name of a
// file's first audio stream.
func ffprobeAudio(t *testing.T, path string) (rate, channels int, profile string) {
	t.Helper()
	s := ffprobeStream(t, path)
	return s.SampleRate, s.Channels, s.Profile
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
