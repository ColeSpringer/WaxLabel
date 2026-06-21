package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// trackProps wraps a single audio track in a Properties for audioLine tests.
func trackProps(container string, t wl.AudioTrack) wl.Properties {
	return wl.Properties{Container: container, Tracks: []wl.AudioTrack{t}}
}

func TestAudioLineBitDepthOnlyForLossless(t *testing.T) {
	// audioLine receives the canonical codec name (CanonicalCodec runs at parse). A
	// lossy codec carrying a stored bit depth must not advertise "16-bit".
	lossy := audioLine(trackProps("MP4", wl.AudioTrack{Codec: "AAC", SampleRate: 44100, Channels: 2, BitsPerSample: 16, Bitrate: 128000}))
	if strings.Contains(lossy, "16-bit") {
		t.Errorf("lossy audio line should omit bit depth: %q", lossy)
	}
	// A lossless codec keeps it.
	flac := audioLine(trackProps("FLAC", wl.AudioTrack{Codec: "FLAC", SampleRate: 44100, Channels: 2, BitsPerSample: 24}))
	if !strings.Contains(flac, "24-bit") {
		t.Errorf("lossless audio line should keep bit depth: %q", flac)
	}
}

func TestAudioLineCodecUnknown(t *testing.T) {
	// No codec identified: name the container and say so, not a bare "MATROSKA".
	line := audioLine(trackProps("Matroska", wl.AudioTrack{SampleRate: 48000, Channels: 2}))
	if !strings.Contains(line, "Matroska (codec unknown)") {
		t.Errorf("unidentified codec line = %q, want \"Matroska (codec unknown)\"", line)
	}
}

func TestAudioLineSubKbpsOmitted(t *testing.T) {
	// A truncated file's collapsed sub-1-kbps average must not print as "0 kbps".
	line := audioLine(trackProps("WAV", wl.AudioTrack{Codec: "PCM", SampleRate: 44100, Channels: 2, BitsPerSample: 16, Bitrate: 12}))
	if strings.Contains(line, "kbps") {
		t.Errorf("sub-1-kbps bitrate should be omitted: %q", line)
	}
}

func TestAudioLineBitrateDroppedAtZeroDuration(t *testing.T) {
	// M1: a header-only file (empty.wav: zero samples, so zero duration) carries a
	// header-derived rate×ch×depth bitrate (705 kbps) that is meaningless without
	// playtime. The truthful header facts stay; the bogus kbps is dropped.
	zero := audioLine(trackProps("WAV", wl.AudioTrack{
		Codec: "PCM", SampleRate: 44100, Channels: 1, BitsPerSample: 16, Bitrate: 705600, Duration: 0,
	}))
	if strings.Contains(zero, "kbps") {
		t.Errorf("zero-duration bitrate should be omitted: %q", zero)
	}
	for _, want := range []string{"PCM", "44100 Hz", "1 ch", "16-bit"} {
		if !strings.Contains(zero, want) {
			t.Errorf("zero-duration line should keep header fact %q: %q", want, zero)
		}
	}
	// A real stream (non-zero duration) keeps its bitrate.
	real := audioLine(trackProps("WAV", wl.AudioTrack{
		Codec: "PCM", SampleRate: 44100, Channels: 1, BitsPerSample: 16, Bitrate: 705600, Duration: time.Second,
	}))
	if !strings.Contains(real, "705 kbps") {
		t.Errorf("a real stream should keep its bitrate: %q", real)
	}
}

func TestBitDepthMeaningful(t *testing.T) {
	// bitDepthMeaningful receives the canonical codec name. Codecs that carry a real
	// stored sample width keep their depth - the PCM family, the lossless codecs
	// (incl. Matroska WavPack/TTA/MLP), and the companded/ADPCM forms.
	for _, c := range []string{
		"FLAC", "ALAC", "PCM", "PCM (extensible)", "IEEE float", "IEEE float64",
		"A-law", "mu-law", "IMA ADPCM", "WAVPACK4", "TTA1", "MLP",
	} {
		if !bitDepthMeaningful(c) {
			t.Errorf("bitDepthMeaningful(%q) = false, want true", c)
		}
	}
	// Lossy/perceptual codecs decode to PCM at an arbitrary depth, so any stored depth
	// is meaningless and must be suppressed.
	for _, c := range []string{"AAC", "MP3", "MP2", "MP1", "Opus", "Vorbis", "AC-3", "E-AC-3", "MPC"} {
		if bitDepthMeaningful(c) {
			t.Errorf("bitDepthMeaningful(%q) = true, want false", c)
		}
	}
}

// TestRenderLintSanitizes (#3): a finding whose message or key is file-derived
// (the encoder-noise message carries the raw inherited stamp; a custom-key finding
// carries the raw field name) is escaped on render, so lint cannot leak control
// bytes to the terminal.
func TestRenderLintSanitizes(t *testing.T) {
	findings := []wl.Finding{
		{Severity: wl.LintWarning, Code: "encoder-noise", Message: "inherited encoder stamp: Lavf\x1bX"},
		{Severity: wl.LintInfo, Code: "custom-key", Message: "custom field, not a known key", Key: tag.Key("BAD\x1bKEY")},
	}
	var buf bytes.Buffer
	renderLint(&buf, "f.flac", findings)
	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Errorf("renderLint leaked a raw ESC:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf("renderLint should escape control bytes (message and key):\n%s", out)
	}
}

func TestDisplayName(t *testing.T) {
	if got := displayName("-"); got != "<stdin>" {
		t.Errorf("displayName(%q) = %q, want <stdin>", "-", got)
	}
	if got := displayName("song.flac"); got != "song.flac" {
		t.Errorf("displayName(%q) = %q, want unchanged", "song.flac", got)
	}
}

func TestRenderTagsEmptyValue(t *testing.T) {
	ts := tag.NewTagSet()
	ts.Set(tag.Title, "") // present, empty string value
	var buf bytes.Buffer
	renderTags(&buf, ts)
	if !strings.Contains(buf.String(), "(empty value)") {
		t.Errorf("renderTags should label an empty value; got:\n%s", buf.String())
	}
}

// TestRenderTagsSanitizes (R1): an embedded ESC/CR in a tag value is shown as a
// visible escape, never a raw control byte that could drive the terminal.
func TestRenderTagsSanitizes(t *testing.T) {
	ts := tag.NewTagSet()
	ts.Set(tag.Title, "a\x1b[31mX\rY") // ANSI CSI + mid-line CR (the report's repro)
	var buf bytes.Buffer
	renderTags(&buf, ts)
	out := buf.String()
	if strings.ContainsAny(out, "\x1b\r") {
		t.Errorf("renderTags leaked a raw control byte:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) || !strings.Contains(out, `\x0d`) {
		t.Errorf("renderTags should show escaped \\x1b and \\x0d; got:\n%q", out)
	}
}

// TestRenderTagsSingleValuedConflict (L5): a known single-valued key holding two
// values - the [conflicting-families] merge surfacing as duplicate rows - flags
// each row with "(conflict)" so they tie back to the warning. A legitimately
// multi-valued key given two values is not flagged.
func TestRenderTagsSingleValuedConflict(t *testing.T) {
	ts := tag.NewTagSet()
	ts.Set(tag.Encoder, "Lavf58", "Lavf59") // ENCODER is single-valued
	var buf bytes.Buffer
	renderTags(&buf, ts)
	out := buf.String()
	if got := strings.Count(out, "(conflict)"); got != 2 {
		t.Errorf("expected both single-valued duplicate rows flagged (conflict); got %d in:\n%s", got, out)
	}

	multi := tag.NewTagSet()
	multi.Set(tag.Artist, "A", "B") // ARTIST is multi-valued: no conflict
	buf.Reset()
	renderTags(&buf, multi)
	if strings.Contains(buf.String(), "(conflict)") {
		t.Errorf("multi-valued key should not be flagged as a conflict:\n%s", buf.String())
	}
}

// TestRenderTagsMultiLineAligns: a legitimate multi-line value (lyrics) keeps its
// line breaks and indents continuation lines, so sanitizing did not flatten it.
func TestRenderTagsMultiLineAligns(t *testing.T) {
	ts := tag.NewTagSet()
	ts.Set(tag.Lyrics, "line one\nline two")
	var buf bytes.Buffer
	renderTags(&buf, ts)
	out := buf.String()
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Errorf("multi-line value not rendered:\n%s", out)
	}
	if strings.Contains(out, "\nline two") {
		t.Errorf("continuation line should be indented, not at column 0:\n%q", out)
	}
}

// TestRenderPicturesDescriptionSingleEscaped (R1): p.Description prints via %q,
// which already escapes control chars; it must not also be run through
// SanitizeText (that would double-escape \x1b into \\x1b).
func TestRenderPicturesDescriptionSingleEscaped(t *testing.T) {
	var buf bytes.Buffer
	renderPictures(&buf, []wl.Picture{{
		Type:        wl.PicFrontCover,
		MIME:        "image/png",
		Description: "desc\x1bX",
		Data:        []byte("xx"),
	}})
	out := buf.String()
	if strings.Contains(out, `\\x1b`) {
		t.Errorf("description should be single-escaped via %%q, got double-escape:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf("description control char should be escaped by %%q:\n%q", out)
	}
}

// TestRenderTagsKeyCountHeader (U2): the header counts keys explicitly, with
// singular/plural agreement.
func TestRenderTagsKeyCountHeader(t *testing.T) {
	two := tag.NewTagSet()
	two.Set(tag.Title, "T")
	two.Set(tag.Artist, "A")
	var buf bytes.Buffer
	renderTags(&buf, two)
	if !strings.Contains(buf.String(), "tags (2 keys):") {
		t.Errorf("want 'tags (2 keys):' header; got:\n%s", buf.String())
	}
	one := tag.NewTagSet()
	one.Set(tag.Title, "T")
	buf.Reset()
	renderTags(&buf, one)
	if !strings.Contains(buf.String(), "tags (1 key):") {
		t.Errorf("want 'tags (1 key):' header; got:\n%s", buf.String())
	}
}

// TestAudioLineOmittedForDegenerate (M4): a record carrying only a bare codec
// name with no technical detail drops the audio line; a real stream still renders
// one. The "container (codec unknown)" signal is kept even without properties,
// since it tells the user the container parsed but the codec was not identified.
func TestAudioLineOmittedForDegenerate(t *testing.T) {
	if line := audioLine(trackProps("", wl.AudioTrack{Codec: "MPEG Audio"})); line != "" {
		t.Errorf("bare-codec audioLine = %q, want empty", line)
	}
	if line := audioLine(wl.Properties{Container: "Matroska"}); line != "Matroska (codec unknown)" {
		t.Errorf("container-only audioLine = %q, want the codec-unknown signal kept", line)
	}
	if line := audioLine(trackProps("FLAC", wl.AudioTrack{Codec: "FLAC", SampleRate: 44100, Channels: 2})); line == "" {
		t.Error("a real stream (sample rate present) should still render an audio line")
	}
}
