package waxlabel_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// writableFixtures is one parsed fixture per writable format, for the cross-format
// boundary assertions below.
var writableFixtures = []string{
	sampleFLAC, sampleOgg, sampleOpus, sampleMP3, sampleWAV, sampleMP4, sampleAAC, sampleMKA, sampleAIFF,
}

// --- D1: present-but-empty (zero-length) collapses to absent; IsNoOp is honest ---

// TestZeroLengthEditIsNoOp proves a Set/Add of no values on an absent key is a true
// no-op across every writable format: the key collapses to absent before planning, so
// IsNoOp reports true and Changes is empty rather than minting a phantom rewrite (#3).
func TestZeroLengthEditIsNoOp(t *testing.T) {
	absent := tag.MustKey("WAXTEST_ABSENT")
	for _, f := range writableFixtures {
		for _, op := range []struct {
			name string
			edit func(*wl.Editor)
		}{
			{"Set", func(e *wl.Editor) { e.Set(absent) }},
			{"Add", func(e *wl.Editor) { e.Add(absent) }},
		} {
			plan, err := edit(t, f, op.edit)
			if err != nil {
				t.Fatalf("%s %s: prepare: %v", f, op.name, err)
			}
			if !plan.IsNoOp() {
				t.Errorf("%s: %s of a zero-length absent key should be a no-op", f, op.name)
			}
			if n := len(plan.Changes()); n != 0 {
				t.Errorf("%s: %s of a zero-length absent key produced %d changes, want 0", f, op.name, n)
			}
		}
	}
}

// TestZeroLengthSaveBackWritesNothing confirms the honest no-op reaches SaveBack: it
// commits nothing and leaves the file's bytes and mtime untouched (before D1 the
// phantom change rewrote the file and bumped its mtime).
func TestZeroLengthSaveBackWritesNothing(t *testing.T) {
	absent := tag.MustKey("WAXTEST_ABSENT")
	for _, f := range []string{sampleFLAC, sampleWAV, sampleAIFF, sampleMP3, sampleMP4} {
		path := copyToTemp(t, f)
		beforeBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		beforeStat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		doc := mustParseFile(t, path)
		plan, err := doc.Edit().Set(absent).Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		_, res, err := plan.Execute(context.Background(), wl.SaveBack())
		if err != nil {
			t.Fatalf("%s: save: %v", f, err)
		}
		if res.Committed {
			t.Errorf("%s: a zero-length no-op SaveBack reported Committed", f)
		}
		afterBytes, _ := os.ReadFile(path)
		afterStat, _ := os.Stat(path)
		if string(beforeBytes) != string(afterBytes) {
			t.Errorf("%s: bytes changed on a no-op SaveBack", f)
		}
		if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
			t.Errorf("%s: mtime changed on a no-op SaveBack", f)
		}
	}
}

// TestEmptyStringValueNotNormalized proves the D1 normalization is scoped strictly to
// zero-length: a present empty-string value ([""], what `set KEY=` produces) is left
// intact and, on a format that stores it (FLAC/Vorbis), is a real change that round-trips
// as a present empty value rather than collapsing to absent.
func TestEmptyStringValueNotNormalized(t *testing.T) {
	key := tag.MustKey("WAXTEST_EMPTYSTR")
	src := readFixture(t, sampleFLAC)
	plan, err := mustParseBytes(t, src).Edit().Set(key, "").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal(`Set(K, "") on an absent key must not be a no-op on FLAC (it adds a present empty value)`)
	}
	out := applyToBytes(t, src, plan)
	vals, ok := mustParseBytes(t, out).Get(key)
	if !ok || len(vals) != 1 || vals[0] != "" {
		t.Errorf(`present empty-string value did not round-trip: got %v (present=%v), want one empty string`, vals, ok)
	}
}

// --- D2: zero-value Document / nil sources are safe ---

// TestZeroValueDocumentSafe exercises every public Document method on both a nil
// *Document and an uninitialized &Document{}: the read accessors return the safe zero
// value (no nil-media panic), and the edit/transfer entry points report the
// uninitialized state as an error rather than panicking.
func TestZeroValueDocumentSafe(t *testing.T) {
	check := func(label string, d *wl.Document) {
		if got := d.Format(); got != wl.FormatUnknown {
			t.Errorf("%s: Format = %v, want unknown", label, got)
		}
		if d.Tags().Len() != 0 {
			t.Errorf("%s: Tags not empty", label)
		}
		if _, ok := d.Get(tag.Title); ok {
			t.Errorf("%s: Get reported present", label)
		}
		_ = d.Fields()
		_ = d.Properties()
		if d.Pictures() != nil || d.Chapters() != nil || d.Families() != nil || d.Warnings() != nil {
			t.Errorf("%s: a slice accessor returned non-nil", label)
		}
		if d.Native() != nil {
			t.Errorf("%s: Native not nil", label)
		}
		_ = d.Identity()
		if !d.Capabilities().ReadOnly {
			t.Errorf("%s: Capabilities not read-only", label)
		}
		if d.Inspect().Format != wl.FormatUnknown {
			t.Errorf("%s: Inspect format not unknown", label)
		}
		if d.Lint() != nil {
			t.Errorf("%s: Lint not nil", label)
		}
		if fix := d.PlanLintFix(); fix.Patch.Len() != 0 || len(fix.Options) != 0 {
			t.Errorf("%s: PlanLintFix not empty", label)
		}
		if _, err := d.Edit().Prepare(); err == nil {
			t.Errorf("%s: Edit().Prepare() should error on an uninitialized document", label)
		}
		if _, err := d.PlanTransfer(wl.FormatFLAC); err == nil {
			t.Errorf("%s: PlanTransfer should error on an uninitialized document", label)
		}
		if _, _, err := d.PrepareTransfer(&wl.Document{}); err == nil {
			t.Errorf("%s: PrepareTransfer should error on an uninitialized document", label)
		}
	}
	check("nil", nil)
	check("zero", &wl.Document{})
}

// TestTransferCarryNoSingleValuedWarning proves a faithful transfer carry does not
// raise the single-valued-multi warning for a source whose single-valued key
// legitimately holds several values - the copy must not flag metadata the user
// authored none of (the carry suppresses it, like the chapter sanity checks).
func TestTransferCarryNoSingleValuedWarning(t *testing.T) {
	base := readFixture(t, sampleFLAC)
	// Build a source whose single-valued ENCODER holds two values (a conflict state a
	// real file can carry), by writing them and reparsing.
	multiPlan, err := mustParseBytes(t, base).Edit().Set(tag.Encoder, "a", "b").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	source := mustParseBytes(t, applyToBytes(t, base, multiPlan))
	if v, _ := source.Get(tag.Encoder); len(v) != 2 {
		t.Fatalf("setup: source ENCODER = %v, want two values", v)
	}

	dst := mustParseBytes(t, readFixture(t, sampleFLAC))
	plan, _, err := source.PrepareTransfer(dst)
	if err != nil {
		t.Fatal(err)
	}
	if reportHasWarning(plan.Report().Warnings, wl.WarnSingleValuedMulti) {
		t.Error("a faithful transfer carry should not raise the single-valued-multi warning")
	}
}

// TestParseNilSourceRejected confirms a nil reader is a clean error, not a panic.
func TestParseNilSourceRejected(t *testing.T) {
	if _, err := wl.Parse(context.Background(), nil); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("Parse(nil) err = %v, want ErrInvalidData", err)
	}
	if _, err := wl.OpenSource(context.Background(), nil); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("OpenSource(nil) err = %v, want ErrInvalidData", err)
	}
}

// --- D3: ErrNeedsFile for a path-less SaveBack ---

// TestSaveBackNeedsFile confirms a document parsed without a path (Parse) cannot
// SaveBack and reports the typed ErrNeedsFile sentinel.
func TestSaveBackNeedsFile(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	doc, err := wl.Parse(context.Background(), wl.BytesSource(src))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := doc.Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); !errors.Is(err, waxerr.ErrNeedsFile) {
		t.Errorf("SaveBack on a path-less document err = %v, want ErrNeedsFile", err)
	}
}

// --- D5: single-valued-multi plan-report warning ---

// TestSingleValuedMultiWarning checks a known single-valued key given several values
// raises the WarnSingleValuedMulti plan warning (so it flows into the report and JSON),
// while a single value does not.
func TestSingleValuedMultiWarning(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	multi, err := mustParseBytes(t, src).Edit().Set(tag.Encoder, "a", "b").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasWarning(multi.Report().Warnings, wl.WarnSingleValuedMulti) {
		t.Errorf("expected a single-valued-multi warning; got %v", multi.Report().Warnings)
	}
	single, err := mustParseBytes(t, src).Edit().Set(tag.Encoder, "only").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if reportHasWarning(single.Report().Warnings, wl.WarnSingleValuedMulti) {
		t.Error("a single value should not raise the single-valued-multi warning")
	}
}

// --- E1: WAV ISFT stamp strip (library, via WithStripEncoderStamp) ---

// TestWAVStripEncoderStampDropsEmptyLIST builds a WAV whose only INFO item is a
// transcoder ISFT stamp, strips it, and verifies the stamp is gone and the now-empty
// LIST chunk is dropped entirely (not emitted bodyless).
func TestWAVStripEncoderStampDropsEmptyLIST(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"ISFT", "Lavf61.7.100"}), wavData(400))
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Fatal("setup: expected an inherited-encoder warning for the ISFT stamp")
	}
	plan, err := doc.Edit().Clear(tag.Encoder).Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("stripping the only ISFT stamp must not be a no-op")
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if hasWarning(re, wl.WarnInheritedEncoder) {
		t.Error("the ISFT transcoder stamp survived the strip")
	}
	for _, e := range re.Native().Describe() {
		if strings.HasPrefix(e.Kind, "LIST") {
			t.Errorf("the emptied LIST chunk was not dropped: %+v", e)
		}
	}
}

// TestWAVStripEncoderStampKeepsOtherInfo confirms a strip drops only the transcoder
// ISFT, preserving the other INFO items.
func TestWAVStripEncoderStampKeepsOtherInfo(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Keep Me"}, [2]string{"ISFT", "Lavf61.7.100"}), wavData(400))
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.Encoder).Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if re.Fields().Title != "Keep Me" {
		t.Errorf("a non-stamp INFO item was dropped: title = %q", re.Fields().Title)
	}
	if hasWarning(re, wl.WarnInheritedEncoder) {
		t.Error("the ISFT transcoder stamp survived the strip")
	}
}

// TestWAVStripEncoderStampLeavesUserISFT confirms the strip is gated on
// IsTranscoderStamp: a user's own ISFT (not a transcoder stamp) is preserved, so a
// strip of a file without a transcoder stamp is a no-op.
func TestWAVStripEncoderStampLeavesUserISFT(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"ISFT", "My Editor 1.0"}), wavData(400))
	doc := mustParseBytes(t, data)
	if hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Fatal("a non-transcoder ISFT should not warn")
	}
	plan, err := doc.Edit().Clear(tag.Encoder).Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Error("stripping with no transcoder stamp present should be a no-op")
	}
}

// --- E2: AAC (ADTS) duration/bitrate via the frame walk ---

// adtsStreamRDB is adtsStream with number_of_raw_data_blocks set on every frame, so a
// frame carries rdb+1 raw data blocks (rdb+1)*1024 samples.
func adtsStreamRDB(chanConfig, frames, payloadPerFrame, rdb int) []byte {
	out := adtsStream(chanConfig, frames, payloadPerFrame)
	frameLen := 7 + payloadPerFrame
	for i := 0; i < frames; i++ {
		out[i*frameLen+6] = byte(rdb & 0x03)
	}
	return out
}

// TestAACFrameWalkSampleCount checks the ADTS walk counts samples per frame correctly,
// including the multi-block case: a frame with number_of_raw_data_blocks=1 holds two
// 1024-sample blocks, so it counts as 2048 samples, not a flat 1024.
func TestAACFrameWalkSampleCount(t *testing.T) {
	single := mustParseBytes(t, adtsStream(2, 10, 100)).Properties().First().TotalSamples
	if single != 10*1024 {
		t.Errorf("single-block stream: TotalSamples = %d, want %d", single, 10*1024)
	}
	multi := mustParseBytes(t, adtsStreamRDB(2, 10, 100, 1)).Properties().First().TotalSamples
	if multi != 10*2048 {
		t.Errorf("multi-block stream: TotalSamples = %d, want %d (>1024/frame)", multi, 10*2048)
	}
}

// TestAACFixtureDurationAccurate checks the walk yields a duration and average bitrate
// close to ffprobe's ground truth for sample.aac (~1.547s, ~122 kbps) - far tighter
// than the old first-frame estimate, which was tens of percent off on VBR.
func TestAACFixtureDurationAccurate(t *testing.T) {
	tr := mustParseFile(t, sampleAAC).Properties().First()
	if tr.TotalSamples != 67584 { // 66 frames x 1024 samples, deterministic for the fixture
		t.Errorf("TotalSamples = %d, want 67584", tr.TotalSamples)
	}
	if s := tr.Duration.Seconds(); s < 1.45 || s > 1.65 {
		t.Errorf("duration = %.3fs, want ~1.53s (ground truth ~1.547s)", s)
	}
	if tr.Bitrate < 118000 || tr.Bitrate > 128000 {
		t.Errorf("bitrate = %d, want ~122000 (ground truth ~122629)", tr.Bitrate)
	}
}

// TestAACTruncatedSingleFrameZeroDuration confirms a stream too short to hold one
// whole frame parses without panic and reports zero duration/bitrate/samples (the
// honest answer for an unplayable fragment) while keeping the static config.
func TestAACTruncatedSingleFrameZeroDuration(t *testing.T) {
	full := adtsStream(2, 1, 200) // one 207-byte frame
	doc := mustParseBytes(t, full[:100])
	if doc.Format() != wl.FormatAAC {
		t.Fatalf("truncated ADTS detected as %v, want AAC", doc.Format())
	}
	tr := doc.Properties().First()
	if tr.SampleRate != 44100 || tr.Channels != 2 {
		t.Errorf("static config lost on a fragment: rate=%d ch=%d", tr.SampleRate, tr.Channels)
	}
	if tr.TotalSamples != 0 || tr.Duration != 0 || tr.Bitrate != 0 {
		t.Errorf("a sub-frame fragment should report zero duration/bitrate/samples; got samples=%d dur=%v bitrate=%d",
			tr.TotalSamples, tr.Duration, tr.Bitrate)
	}
}

// failAfterSource serves data from a byte slice but fails every ReadAt at or beyond
// failAt, simulating a mid-stream I/O error (or a concurrent truncate) during the
// ADTS frame walk.
type failAfterSource struct {
	data   []byte
	failAt int64
}

func (s failAfterSource) ReadAt(p []byte, off int64) (int, error) {
	if off >= s.failAt {
		return 0, errors.New("simulated disk failure")
	}
	n := copy(p, s.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (s failAfterSource) Size() int64 { return int64(len(s.data)) }

// TestAACWalkPropagatesIOError confirms a genuine read error during the ADTS walk
// fails the parse rather than being swallowed as a benign EOF (which would return a
// silently short duration/bitrate). The stream is sized past one 64 KiB window so
// the walk must read into the failing region.
func TestAACWalkPropagatesIOError(t *testing.T) {
	data := adtsStream(2, 400, 200) // ~82 KB: forces a second walk window past 64 KiB
	src := failAfterSource{data: data, failAt: 64 << 10}
	if _, err := wl.Parse(context.Background(), src); err == nil {
		t.Error("a read error during the ADTS walk should fail the parse, not be swallowed")
	}
}

// --- E5: lint conflicting-families deduped per key ---

// TestLintConflictingFamiliesDedup confirms a key whose sources disagree is reported
// once even when several of its family entries are unselected (sample.webm carries two
// unselected ENCODER entries that must collapse to a single finding).
func TestLintConflictingFamiliesDedup(t *testing.T) {
	n := 0
	for _, f := range mustParseFile(t, sampleWebM).Lint() {
		if f.Code == "conflicting-families" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("conflicting-families findings = %d, want exactly 1 (deduped per key)", n)
	}
}

// --- G: fresh-tag id3 version policy (MP3 v2.3; AAC/WAV/AIFF v2.4) ---

// TestFreshID3VersionPolicy forces a fresh id3 tag (a custom key has no native home in
// WAV/AIFF and creates an id3 frame in MP3/AAC) and confirms the from-scratch version
// follows the single policy: MP3 stays v2.3 for legacy-hardware compatibility, while
// every other id3-bearing format defaults to v2.4. The version shows in the native
// view's Kind (MP3/AAC) or Note (the WAV/AIFF id3 chunk), so both are scanned.
func TestFreshID3VersionPolicy(t *testing.T) {
	cases := []struct{ fixture, wantVer string }{
		{notagsMP3, "ID3v2.3"},  // legacy hardware reads MP3 id3 directly
		{notagsWAV, "ID3v2.4"},  // software-only readers
		{notagsAIFF, "ID3v2.4"}, // plain AIFF
		{sampleAIFC, "ID3v2.4"}, // AIFF-C, consistent with plain AIFF
		{notagsAAC, "ID3v2.4"},  // raw ADTS, sole store
	}
	for _, c := range cases {
		src := readFixture(t, c.fixture)
		plan, err := mustParseBytes(t, src).Edit().Set(tag.MustKey("WAXTEST_CUSTOM"), "v").Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", c.fixture, err)
		}
		re := mustParseBytes(t, applyToBytes(t, src, plan))
		found, ok := false, false
		for _, e := range re.Native().Describe() {
			label := e.Kind + " " + e.Note
			if strings.Contains(label, "ID3v2") {
				found = true
				if strings.Contains(label, c.wantVer) {
					ok = true
				}
			}
		}
		if !found {
			t.Errorf("%s: no fresh id3 tag was created by a custom-key edit", c.fixture)
		} else if !ok {
			t.Errorf("%s: fresh id3 version is not %s", c.fixture, c.wantVer)
		}
	}
}

// edit parses fixture f, applies edit, and prepares a plan.
func edit(t *testing.T, f string, apply func(*wl.Editor)) (*wl.Plan, error) {
	t.Helper()
	ed := mustParseFile(t, f).Edit()
	apply(ed)
	return ed.Prepare()
}
