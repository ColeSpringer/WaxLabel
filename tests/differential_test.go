package waxlabel_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// Write-side differential: after an edit is saved, an independent tool (ffmpeg/ffprobe) must read
// back the written values and accept the output as a valid FLAC stream.

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
		Type: wl.PicFrontCover, Data: testfile.PNG(),
	}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	// A stream-copy remux demuxes the metadata and audio in full and fails if
	// anything is malformed.
	remux := path + ".remux.flac"
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-c", "copy", "-y", remux)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected our output: %v\n%s", err, out)
	}

	// The remuxed file reads back with the title intact.
	if got := mustParseFile(t, remux).Fields().Title; got != "Valid FLAC" {
		t.Errorf("after ffmpeg remux, Title = %q, want Valid FLAC", got)
	}
}

// requireTool skips a differential test, or fails it under WAXLABEL_REQUIRE_FFMPEG, when an
// external CLI (ffprobe/ffmpeg) is absent.
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

// ffmpegRequired reports whether a differential leg that cannot run here is a failure rather than a
// skip: the CI differential job sets it, so a broken ffmpeg cannot turn the gate green by skipping.
func ffmpegRequired() bool { return os.Getenv("WAXLABEL_REQUIRE_FFMPEG") != "" }

// ffmpegEncode runs ffmpeg with args and writes path, returning it. An encode ffmpeg cannot do here
// (a codec this build lacks) skips the leg, or fails it under WAXLABEL_REQUIRE_FFMPEG.
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

// probeStream is what ffprobe makes of a file's first audio stream: every field the differential
// tests compare, from one invocation.
type probeStream struct {
	CodecName     string
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
		"-show_entries", "stream=codec_name,sample_rate,channels,profile,bits_per_sample,duration_ts,duration,bit_rate,time_base",
		"-of", "json", path).Output()
	if err != nil {
		// ffprobe says why on stderr ("missing mandatory atoms", an unreadable config); without it a
		// failure here is only an exit status.
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
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
	codecName, _ := fields["codec_name"].(string)
	p := probeStream{
		CodecName:     codecName,
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

// probeChapter is one chapter as ffprobe reads it, its times in seconds.
type probeChapter struct {
	Start, End time.Duration
	Title      string
}

// ffprobeChapters lists a file's chapters, with whatever ffprobe logs at error level while
// reading it.
func ffprobeChapters(t *testing.T, path string) (chapters []probeChapter, log string) {
	t.Helper()
	cmd := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error", "-show_chapters", "-of", "json", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v\n%s", path, err, stderr.String())
	}
	var probe struct {
		Chapters []struct {
			StartTime string `json:"start_time"`
			EndTime   string `json:"end_time"`
			Tags      struct {
				Title string `json:"title"`
			} `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("ffprobe %s: %v\n%s", path, err, out)
	}
	seconds := func(v string) time.Duration {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("ffprobe %s: chapter time %q: %v", path, v, err)
		}
		return time.Duration(math.Round(f * float64(time.Second)))
	}
	for _, c := range probe.Chapters {
		chapters = append(chapters, probeChapter{Start: seconds(c.StartTime), End: seconds(c.EndTime), Title: c.Tags.Title})
	}
	return chapters, stderr.String()
}

// ffmpegSine encodes one second of a 1 kHz sine at the given geometry, with codecArgs
// naming the codec and any option it needs, and returns path.
func ffmpegSine(t *testing.T, path string, channels, rate int, codecArgs ...string) string {
	t.Helper()
	args := []string{"-f", "lavfi", "-i", "sine=frequency=1000:duration=1",
		"-ac", strconv.Itoa(channels), "-ar", strconv.Itoa(rate)}
	return ffmpegEncode(t, path, append(args, codecArgs...)...)
}

// ffprobePackets counts the packets of a file's first audio stream, which reads the whole of it.
func ffprobePackets(t *testing.T, path string) int {
	t.Helper()
	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error", "-select_streams", "a:0",
		"-count_packets", "-show_entries", "stream=nb_read_packets", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("ffprobe %s: packet count %q: %v", path, out, err)
	}
	return n
}

// ffmpegDecodedSamples decodes a file's first audio stream and counts the samples per channel
// that come out, the length a player plays.
func ffmpegDecodedSamples(t *testing.T, path string, channels int) int {
	t.Helper()
	pcm, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path,
		"-map", "0:a:0", "-f", "s16le", "-c:a", "pcm_s16le", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg decode %s: %v", path, err)
	}
	return len(pcm) / 2 / channels
}

// An MP3's length is what ffmpeg decodes: with the Info header libmp3lame writes, the frames less
// the encoder's delay and padding; with no header, the bytes at a constant bitrate, or the frames
// themselves when the bitrate varies. ffprobe's own duration is no witness for the first: ffmpeg
// 6.1 leaves the delay and padding in it, 8.0 does not.
func TestDifferentialMP3Duration(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	for _, c := range []struct {
		name     string
		channels int
		rate     int
		args     []string
	}{
		{"cbr 128k 44.1 kHz", 2, 44100, []string{"-c:a", "libmp3lame", "-b:a", "128k"}},
		{"cbr 8k 8 kHz", 1, 8000, []string{"-c:a", "libmp3lame", "-b:a", "8k"}},
		{"cbr 128k without a header", 2, 44100, []string{"-c:a", "libmp3lame", "-b:a", "128k", "-write_xing", "0"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := ffmpegSine(t, filepath.Join(t.TempDir(), "sine.mp3"), c.channels, c.rate, c.args...)
			decoded := ffmpegDecodedSamples(t, path, c.channels)
			got, want := mustParseFile(t, path).Properties().Duration(), time.Duration(decoded)*time.Second/time.Duration(c.rate)
			if d := got - want; d < -time.Millisecond || d > time.Millisecond {
				t.Errorf("duration %v, ffmpeg decodes %d samples: %v", got, decoded, want)
			}
		})
	}
	t.Run("vbr without a header", func(t *testing.T) {
		// A swept tone makes the encoder vary its frames; the stream is long enough to pass 64 KiB.
		path := ffmpegEncode(t, filepath.Join(t.TempDir(), "sweep.mp3"), "-f", "lavfi", "-i",
			"aevalsrc=sin(2*PI*t*(200+1500*t)):d=8:s=44100", "-ac", "2", "-c:a", "libmp3lame", "-q:a", "2", "-write_xing", "0")
		tr := mustParseFile(t, path).Properties().First()
		if want := uint64(ffprobePackets(t, path)) * 1152; tr.TotalSamples != want {
			t.Errorf("TotalSamples %d, want ffprobe's packet count times 1152 = %d", tr.TotalSamples, want)
		}
	})
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
