package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

const (
	sampleOgg  = "../testdata/sample.ogg"
	sampleOpus = "../testdata/sample.opus"
	notagsOgg  = "../testdata/notags.ogg"
)

// pattern returns n deterministic bytes - a stand-in cover payload large enough
// to push the comment header past one page when needed.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*131 + 7)
	}
	return b
}

// essenceOf parses in-memory bytes and returns the encoded-essence digest.
func essenceOf(t *testing.T, data []byte) wl.AudioDigest {
	t.Helper()
	doc := mustParseBytes(t, data)
	d, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(data)))
	if err != nil {
		t.Fatalf("hash essence: %v", err)
	}
	return d
}

func TestOggParse(t *testing.T) {
	cases := []struct {
		path    string
		format  wl.Format
		codec   string
		rate    int
		chans   int
		title   string
		hasDate bool
	}{
		{sampleOgg, wl.FormatOggVorbis, "Vorbis", 44100, 2, "Sample Title", true},
		{sampleOpus, wl.FormatOggOpus, "Opus", 48000, 2, "Sample Title", true},
	}
	for _, c := range cases {
		doc := mustParseFile(t, c.path)
		if doc.Format() != c.format {
			t.Errorf("%s: format = %v, want %v", c.path, doc.Format(), c.format)
		}
		tr := doc.Properties().First()
		if tr.Codec != c.codec || tr.SampleRate != c.rate || tr.Channels != c.chans {
			t.Errorf("%s: track = %+v, want codec %s rate %d chans %d", c.path, tr, c.codec, c.rate, c.chans)
		}
		if tr.Duration <= 0 {
			t.Errorf("%s: duration = %v, want > 0", c.path, tr.Duration)
		}
		if doc.Fields().Title != c.title {
			t.Errorf("%s: title = %q, want %q", c.path, doc.Fields().Title, c.title)
		}
		if c.hasDate && doc.Fields().RecordingDate != "2021" {
			t.Errorf("%s: DATE should map to RecordingDate=2021, got %q", c.path, doc.Fields().RecordingDate)
		}
		// ffmpeg-encoded fixtures carry an "encoder=Lavf..." vendor stamp.
		if !hasWarning(doc, wl.WarnInheritedEncoder) {
			t.Errorf("%s: expected inherited-encoder warning", c.path)
		}
		if len(doc.Families()) == 0 {
			t.Errorf("%s: Families() should be non-empty", c.path)
		}
	}
}

// TestOggRoundTripPreservesEssence is the core invariant: editing tags must not
// disturb the audio packet payloads (the essence), and the values must read back.
func TestOggRoundTripPreservesEssence(t *testing.T) {
	for _, f := range []string{sampleOgg, sampleOpus} {
		src := readFixture(t, f)
		before := essenceOf(t, src)

		plan, err := mustParseBytes(t, src).Edit().
			Set(tag.Title, "Changed Title").
			Set(tag.Artist, "First", "Second").
			Set(tag.Key("CUSTOM_X"), "cval").
			Clear(tag.Genre).
			Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		out := applyToBytes(t, src, plan)

		if after := essenceOf(t, out); !before.Equal(after) {
			t.Errorf("%s: audio essence changed across a tag edit", f)
		}
		got := mustParseBytes(t, out)
		if got.Fields().Title != "Changed Title" {
			t.Errorf("%s: title = %q", f, got.Fields().Title)
		}
		if a := got.Fields().Artists; len(a) != 2 || a[0] != "First" || a[1] != "Second" {
			t.Errorf("%s: artists = %v", f, a)
		}
		if v, ok := got.Get(tag.Key("CUSTOM_X")); !ok || v[0] != "cval" {
			t.Errorf("%s: custom key not round-tripped: %v", f, v)
		}
		if _, ok := got.Get(tag.Genre); ok {
			t.Errorf("%s: GENRE should have been cleared", f)
		}
	}
}

func TestOggNoOpWritesNothing(t *testing.T) {
	for _, f := range []string{sampleOgg, sampleOpus} {
		path := copyToTemp(t, f)
		doc := mustParseFile(t, path)
		plan, err := doc.Edit().Set(tag.Title, doc.Fields().Title).Prepare() // same value
		if err != nil {
			t.Fatal(err)
		}
		if !plan.IsNoOp() {
			t.Errorf("%s: re-setting the same title should be a no-op", f)
		}
		_, res, err := plan.Execute(context.Background(), wl.SaveBack())
		if err != nil {
			t.Fatal(err)
		}
		if res.Committed {
			t.Errorf("%s: a no-op SaveBack must not write", f)
		}
	}
}

// TestOggCoverSmall adds and removes a small cover (one comment page, no
// renumber) and confirms the picture round-trips and the essence is intact.
func TestOggCoverSmall(t *testing.T) {
	for _, f := range []string{sampleOgg, sampleOpus} {
		src := readFixture(t, f)
		before := essenceOf(t, src)

		plan, err := mustParseBytes(t, src).Edit().
			AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, src, plan)
		if after := essenceOf(t, out); !before.Equal(after) {
			t.Errorf("%s: essence changed adding a small cover", f)
		}
		got := mustParseBytes(t, out)
		if len(got.Pictures()) != 1 || got.Pictures()[0].Type != wl.PicFrontCover {
			t.Fatalf("%s: expected one front-cover picture, got %+v", f, got.Pictures())
		}

		// Remove it again.
		plan2, _ := got.Edit().ClearPictures().Prepare()
		out2 := applyToBytes(t, out, plan2)
		if len(mustParseBytes(t, out2).Pictures()) != 0 {
			t.Errorf("%s: ClearPictures should remove the cover", f)
		}
		if after := essenceOf(t, out2); !before.Equal(after) {
			t.Errorf("%s: essence changed removing the cover", f)
		}
	}
}

// TestOggCoverRenumberPreservesEssence adds a cover large enough that the comment
// header spills onto another page, forcing the audio-page renumber path. The
// audio essence must still be byte-identical and the picture must survive.
func TestOggCoverRenumberPreservesEssence(t *testing.T) {
	cover := pattern(70000) // base64 ~93 KiB > one 65025-byte page body
	for _, f := range []string{sampleOgg, sampleOpus} {
		src := readFixture(t, f)
		before := essenceOf(t, src)

		plan, err := mustParseBytes(t, src).Edit().
			AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: cover}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(plan.Report().Operations, func(op string) bool {
			return strings.Contains(op, "renumber")
		}) {
			t.Fatalf("%s: expected a renumber operation, got %v", f, plan.Report().Operations)
		}
		out := applyToBytes(t, src, plan)
		if after := essenceOf(t, out); !before.Equal(after) {
			t.Errorf("%s: essence changed after a renumbering cover add", f)
		}
		got := mustParseBytes(t, out)
		pics := got.Pictures()
		if len(pics) != 1 || !slices.Equal(pics[0].Data, cover) {
			t.Errorf("%s: cover did not round-trip through the renumber path", f)
		}
		if got.Fields().Title != "Sample Title" {
			t.Errorf("%s: tags disturbed by cover add: title %q", f, got.Fields().Title)
		}
	}
}

// TestOggOpusR128NotMappedToReplayGain guards the plan's "Opus R128 distinct from
// ReplayGain" rule: an R128_* tag passes through as its own canonical key and is
// never folded into the ReplayGain keys.
// TestOggSaveBackVerifyEssence exercises the SaveBack path with WithVerifyEssence
// - which re-reads the written file and re-hashes its essence (verifyOutput) -
// together with a renumbering cover add, so the buffered file write, the renumber
// loop, and output verification are all covered end to end.
func TestOggSaveBackVerifyEssence(t *testing.T) {
	for _, f := range []string{sampleOgg, sampleOpus} {
		path := copyToTemp(t, f)
		plan, err := mustParseFile(t, path).Edit().
			Set(tag.Album, "Verified").
			AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: pattern(70000)}).
			Prepare(wl.WithVerifyEssence())
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		_, res, err := plan.Execute(context.Background(), wl.SaveBack())
		if err != nil {
			t.Fatalf("%s: SaveBack with verify failed: %v", f, err)
		}
		if !res.Committed {
			t.Errorf("%s: expected the write to be committed", f)
		}
		got := mustParseFile(t, path)
		if got.Fields().Album != "Verified" {
			t.Errorf("%s: album = %q", f, got.Fields().Album)
		}
		if len(got.Pictures()) != 1 {
			t.Errorf("%s: pictures = %d, want 1", f, len(got.Pictures()))
		}
	}
}

func TestOggOpusR128NotMappedToReplayGain(t *testing.T) {
	src := readFixture(t, sampleOpus)
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Key("R128_TRACK_GAIN"), "-2048").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	got := mustParseBytes(t, applyToBytes(t, src, plan))
	if v, ok := got.Get(tag.Key("R128_TRACK_GAIN")); !ok || v[0] != "-2048" {
		t.Errorf("R128_TRACK_GAIN not preserved as its own key: %v", v)
	}
	if _, ok := got.Get(tag.ReplayGainTrackGain); ok {
		t.Error("R128 gain must not be mapped to REPLAYGAIN_TRACK_GAIN")
	}
}

// TestOggChainedReadBestEffortWriteRefused checks that a chained/multiplexed
// stream is read best-effort (first stream's tags, with a warning) but refused on
// write per the plan.
func TestOggChainedReadBestEffortWriteRefused(t *testing.T) {
	chained := append(slices.Clone(readFixture(t, sampleOgg)), readFixture(t, notagsOgg)...)
	doc := mustParseBytes(t, chained)

	if !hasWarning(doc, wl.WarnChainedStream) {
		t.Error("expected a chained-stream warning")
	}
	if doc.Fields().Title != "Sample Title" {
		t.Errorf("best-effort read should still surface the first stream's title, got %q", doc.Fields().Title)
	}
	if _, err := doc.Edit().Set(tag.Title, "nope").Prepare(); !errors.Is(err, waxerr.ErrChainedStream) {
		t.Errorf("writing a chained stream should fail with ErrChainedStream, got %v", err)
	}
}

// TestOggPreservesTrailingBytes confirms bytes after the last Ogg page (recorded
// by length and copied from the source, never buffered) survive a rewrite.
func TestOggPreservesTrailingBytes(t *testing.T) {
	junk := []byte("TRAILING-JUNK-PRESERVE-ME")
	for _, f := range []string{sampleOgg, sampleOpus} {
		src := append(slices.Clone(readFixture(t, f)), junk...)
		before := essenceOf(t, src)

		plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Trailer").Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		out := applyToBytes(t, src, plan)

		if !bytes.HasSuffix(out, junk) {
			t.Errorf("%s: trailing bytes were not preserved on rewrite", f)
		}
		if after := essenceOf(t, out); !before.Equal(after) {
			t.Errorf("%s: essence changed with trailing bytes present", f)
		}
		if got := mustParseBytes(t, out); got.Fields().Title != "Trailer" {
			t.Errorf("%s: title = %q", f, got.Fields().Title)
		}
	}
}

func TestOggParseRespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := wl.Parse(ctx, wl.BytesSource(readFixture(t, sampleOgg))); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled parse: got %v, want context.Canceled", err)
	}
}

func TestOggExtensionRouting(t *testing.T) {
	// .opus is Opus; .ogg sniffs to Vorbis. Detection is by content, so this also
	// confirms the codec signatures are told apart.
	if got := mustParseFile(t, sampleOpus).Format(); got != wl.FormatOggOpus {
		t.Errorf("sample.opus parsed as %v", got)
	}
	if got := mustParseFile(t, sampleOgg).Format(); got != wl.FormatOggVorbis {
		t.Errorf("sample.ogg parsed as %v", got)
	}
}

// Write-side differential: an independent tool must read back what we wrote
// and accept our audio. These skip cleanly when ffmpeg/ffprobe are absent.

func TestOggDifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	for _, f := range []string{sampleOgg, sampleOpus} {
		path := copyToTemp(t, f)
		plan, err := mustParseFile(t, path).Edit().
			Set(tag.Title, "Differential Title").
			Set(tag.Key("CUSTOM_TAG"), "custom-value").
			Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}

		// Ogg Vorbis/Opus carry tags at the stream level.
		out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
			"-show_entries", "stream_tags", "-of", "json", path).Output()
		if err != nil {
			t.Fatalf("%s: ffprobe: %v", f, err)
		}
		var probe struct {
			Streams []struct {
				Tags map[string]string `json:"tags"`
			} `json:"streams"`
		}
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("%s: parse ffprobe json: %v\n%s", f, err, out)
		}
		var tags map[string]string
		for _, s := range probe.Streams {
			if len(s.Tags) > 0 {
				tags = s.Tags
				break
			}
		}
		for k, want := range map[string]string{"title": "Differential Title", "CUSTOM_TAG": "custom-value"} {
			if got := lookupCI(tags, k); got != want {
				t.Errorf("%s: ffprobe tag %q = %q, want %q (all: %v)", f, k, got, want, tags)
			}
		}
	}
}

func TestOggDifferentialFFmpegDecodesRenumbered(t *testing.T) {
	requireTool(t, "ffmpeg")
	cover := pattern(70000) // forces the renumber path
	for _, f := range []string{sampleOgg, sampleOpus} {
		path := copyToTemp(t, f)
		plan, err := mustParseFile(t, path).Edit().
			Set(tag.Title, "Valid Ogg").
			AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: cover}).
			Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}

		// Fully demux+decode the audio stream: this fails loudly if our pages,
		// lacing, sequence numbers, or CRCs are malformed. (Audio-only mapping
		// avoids ffmpeg's ogg muxer refusing the attached-picture stream.)
		cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
			"-i", path, "-map", "0:a", "-f", "null", "-")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: ffmpeg rejected our renumbered output: %v\n%s", f, err, out)
		}
		if got := mustParseFile(t, path).Fields().Title; got != "Valid Ogg" {
			t.Errorf("%s: title after edit = %q", f, got)
		}
	}
}
