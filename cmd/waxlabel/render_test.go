package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// trackProps wraps one audio track in Properties for audioLine tests.
func trackProps(container string, t wl.AudioTrack) wl.Properties {
	return wl.Properties{Container: container, Tracks: []wl.AudioTrack{t}}
}

func TestAudioLineBitDepthOnlyForLossless(t *testing.T) {
	// CanonicalCodec runs at parse; lossy codec must not show stored bit depth.
	lossy := audioLine(trackProps("MP4", wl.AudioTrack{Codec: "AAC", SampleRate: 44100, Channels: 2, BitsPerSample: 16, Bitrate: 128000}))
	if strings.Contains(lossy, "16-bit") {
		t.Errorf("lossy audio line should omit bit depth: %q", lossy)
	}
	// Lossless codec keeps bit depth.
	flac := audioLine(trackProps("FLAC", wl.AudioTrack{Codec: "FLAC", SampleRate: 44100, Channels: 2, BitsPerSample: 24}))
	if !strings.Contains(flac, "24-bit") {
		t.Errorf("lossless audio line should keep bit depth: %q", flac)
	}
}

func TestAudioLineCodecUnknown(t *testing.T) {
	// Unidentified codec: name container, not bare "MATROSKA".
	line := audioLine(trackProps("Matroska", wl.AudioTrack{SampleRate: 48000, Channels: 2}))
	if !strings.Contains(line, "Matroska (codec unknown)") {
		t.Errorf("unidentified codec line = %q, want \"Matroska (codec unknown)\"", line)
	}
}

func TestAudioLineSubKbpsOmitted(t *testing.T) {
	// Sub-1-kbps average must not print as "0 kbps".
	line := audioLine(trackProps("WAV", wl.AudioTrack{Codec: "PCM", SampleRate: 44100, Channels: 2, BitsPerSample: 16, Bitrate: 12}))
	if strings.Contains(line, "kbps") {
		t.Errorf("sub-1-kbps bitrate should be omitted: %q", line)
	}
}

func TestAudioLineBitrateDroppedAtZeroDuration(t *testing.T) {
	// Zero duration: header-derived kbps is meaningless; drop kbps, keep header facts.
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
	// Non-zero duration keeps bitrate.
	real := audioLine(trackProps("WAV", wl.AudioTrack{
		Codec: "PCM", SampleRate: 44100, Channels: 1, BitsPerSample: 16, Bitrate: 705600, Duration: time.Second,
	}))
	if !strings.Contains(real, "705 kbps") {
		t.Errorf("a real stream should keep its bitrate: %q", real)
	}
}

func TestBitDepthMeaningful(t *testing.T) {
	// PCM family, lossless codecs, companded/ADPCM: stored width is meaningful.
	for _, c := range []string{
		"FLAC", "ALAC", "PCM", "PCM (extensible)", "IEEE float", "IEEE float64",
		"A-law", "mu-law", "IMA ADPCM", "WAVPACK4", "TTA1", "MLP",
		"WMA Lossless", // only WMA variant with stored decode width
	} {
		if !bitDepthMeaningful(c) {
			t.Errorf("bitDepthMeaningful(%q) = false, want true", c)
		}
	}
	// Lossy codecs: stored depth is meaningless.
	for _, c := range []string{"AAC", "MP3", "MP2", "MP1", "Opus", "Vorbis", "AC-3", "E-AC-3", "MPC",
		"Musepack", "WMA v1", "WMA v2", "WMA Pro", "WMA Voice"} {
		if bitDepthMeaningful(c) {
			t.Errorf("bitDepthMeaningful(%q) = true, want false", c)
		}
	}
}

// TestRenderLintSanitizes: file-derived finding message/key escaped so lint cannot leak control bytes.
func TestRenderLintSanitizes(t *testing.T) {
	findings := []wl.Finding{
		{Severity: wl.LintWarning, Code: "inherited-encoder", Message: "inherited encoder stamp: Lavf\x1bX"},
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

// TestRenderTagsSanitizes: ESC/CR in tag values shown escaped, never raw on terminal.
func TestRenderTagsSanitizes(t *testing.T) {
	ts := tag.NewTagSet()
	ts.Set(tag.Title, "a\x1b[31mX\rY") // ANSI CSI + mid-line CR
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

// TestRenderTagsDuplicateVsConflict: differing values on single-valued key are (conflict);
// identical folded values are (duplicate), excluded from header count; multi-valued keys unflagged.
func TestRenderTagsDuplicateVsConflict(t *testing.T) {
	// Differing values on single-valued key: both rows (conflict).
	conflict := tag.NewTagSet()
	conflict.Set(tag.Encoder, "Lavf58", "Lavf59") // ENCODER is single-valued
	var buf bytes.Buffer
	renderTags(&buf, conflict)
	out := buf.String()
	if got := strings.Count(out, "(conflict)"); got != 2 {
		t.Errorf("expected both differing rows flagged (conflict); got %d in:\n%s", got, out)
	}
	if !strings.Contains(out, "in conflict") {
		t.Errorf("header should count the conflict:\n%s", out)
	}

	// Identical folded values: (duplicate), not counted as conflict.
	dup := tag.NewTagSet()
	dup.Set(tag.Encoder, "Lavf58", "lavf58 ") // same value, case/space-insensitively
	buf.Reset()
	renderTags(&buf, dup)
	out = buf.String()
	if got := strings.Count(out, "(duplicate)"); got != 2 {
		t.Errorf("expected both identical rows flagged (duplicate); got %d in:\n%s", got, out)
	}
	if strings.Contains(out, "(conflict)") || strings.Contains(out, "in conflict") {
		t.Errorf("identical duplicate values must not read as a conflict:\n%s", out)
	}

	// Multi-valued key: neither flag.
	multi := tag.NewTagSet()
	multi.Set(tag.Artist, "A", "B") // ARTIST is multi-valued
	buf.Reset()
	renderTags(&buf, multi)
	if got := buf.String(); strings.Contains(got, "(conflict)") || strings.Contains(got, "(duplicate)") {
		t.Errorf("multi-valued key should not be flagged:\n%s", got)
	}
}

// TestRenderTagsMultiLineAligns: prose keys keep line breaks with indented continuations.
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

// TestRenderPicturesDescriptionSingleEscaped: %q escapes controls; must not double-escape via SanitizeText.
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

// TestPictureRowUnknownDims: unknown dims render "--", not "?"; known dims render WxH.
func TestPictureRowUnknownDims(t *testing.T) {
	unknown := pictureRow(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: []byte("xx")})
	if !strings.Contains(unknown, "--") {
		t.Errorf("unknown dimensions should render \"--\":\n%q", unknown)
	}
	if strings.Contains(unknown, "?") {
		t.Errorf("unknown dimensions should not render \"?\":\n%q", unknown)
	}
	known := pictureRow(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Width: 64, Height: 48, Data: []byte("xx")})
	if !strings.Contains(known, "64x48") {
		t.Errorf("known dimensions should render WxH:\n%q", known)
	}
}

// TestPictureRowDepthColors: depth and indexed palette size in trailing size column.
func TestPictureRowDepthColors(t *testing.T) {
	truecolor := pictureRow(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Width: 64, Height: 48, Depth: 24, Data: []byte("xx")})
	if !strings.Contains(truecolor, "(24-bit)") || strings.Contains(truecolor, "colors") {
		t.Errorf("non-indexed depth should render \"(24-bit)\" with no color count:\n%q", truecolor)
	}
	indexed := pictureRow(wl.Picture{Type: wl.PicFrontCover, MIME: "image/gif", Width: 4, Height: 4, Depth: 8, Colors: 256, Data: []byte("xx")})
	if !strings.Contains(indexed, "(8-bit, 256 colors)") {
		t.Errorf("indexed image should render depth and palette size:\n%q", indexed)
	}
	unknown := pictureRow(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: []byte("xx")})
	if strings.Contains(unknown, "-bit") {
		t.Errorf("unknown depth should render no depth annotation:\n%q", unknown)
	}
}

// TestRenderTagsKeyCountHeader: header uses singular/plural key count.
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

// TestAudioLineOmittedForDegenerate: bare codec with no detail omits line; container-only keeps
// "codec unknown"; real stream still renders.
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

// TestNativeSizeZeroPadding: zero PADDING is "0 B"; zero structural entry blank; counts keep units.
func TestNativeSizeZeroPadding(t *testing.T) {
	if got := nativeSize(wl.NativeEntry{Kind: "PADDING", Size: 0}); got != "0 B" {
		t.Errorf("nativeSize(0-length PADDING) = %q, want \"0 B\"", got)
	}
	if got := nativeSize(wl.NativeEntry{Kind: "EBML header", Size: 0}); got != "" {
		t.Errorf("nativeSize(0-size structural entry) = %q, want blank", got)
	}
	if got := nativeSize(wl.NativeEntry{Kind: "PADDING", Size: 8192}); got == "" || got == "0 B" {
		t.Errorf("nativeSize(non-empty PADDING) should be a real byte count, got %q", got)
	}
	if got := nativeSize(wl.NativeEntry{Kind: "VORBIS_COMMENT", Size: 3, Unit: "tags"}); got != "3 tags" {
		t.Errorf("nativeSize(count) = %q, want \"3 tags\"", got)
	}
}

// TestDumpNativeNoPaddingOmitsBlock: --no-padding leaves no PADDING block (valid FLAC).
func TestDumpNativeNoPaddingOmitsBlock(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", f, "--set", "TITLE=X", "--no-padding"); code != 0 {
		t.Fatalf("set --no-padding exit = %d", code)
	}
	out, _, code := runCLI(t, "dump", "--native", f)
	if code != 0 {
		t.Fatalf("dump --native exit = %d", code)
	}
	if strings.Contains(out, "PADDING") {
		t.Errorf("--no-padding should leave no PADDING block, but one appears:\n%s", out)
	}
}

// TestAudioLineOutputGain: non-zero header gain shown; zero omitted.
func TestAudioLineOutputGain(t *testing.T) {
	with := audioLine(trackProps("Ogg", wl.AudioTrack{Codec: "Opus", SampleRate: 48000, Channels: 2, OutputGain: -896}))
	if !strings.Contains(with, "gain -3.50 dB") {
		t.Errorf("audio line = %q, want it to name the output gain", with)
	}
	without := audioLine(trackProps("Ogg", wl.AudioTrack{Codec: "Opus", SampleRate: 48000, Channels: 2}))
	if strings.Contains(without, "gain") {
		t.Errorf("audio line = %q, want no gain at 0", without)
	}
}

// TestRenderTagsEscapesNewlineOutsideProseKeys: newline is content only for prose keys; elsewhere \x0a.
func TestRenderTagsEscapesNewlineOutsideProseKeys(t *testing.T) {
	ts := tag.NewTagSet()
	ts.Set(tag.Title, "Real Title\n    ARTIST  Forged Artist")
	ts.Set(tag.Comment, "para one\npara two")
	ts.Set(tag.Key("CUSTOM"), "a\nb")
	var buf bytes.Buffer
	renderTags(&buf, ts)
	out := buf.String()
	if !strings.Contains(out, `Real Title\x0a    ARTIST  Forged Artist`) {
		t.Errorf("TITLE newline not escaped:\n%s", out)
	}
	if !strings.Contains(out, `a\x0ab`) {
		t.Errorf("custom key newline not escaped:\n%s", out)
	}
	if !strings.Contains(out, "para one\n") || strings.Contains(out, `para one\x0a`) {
		t.Errorf("COMMENT should keep its line break:\n%s", out)
	}
}
