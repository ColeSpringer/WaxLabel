package waxlabel_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestOpusSetOutputGainRoundTrip: an output-gain edit patches the OpusHead alone. Only the
// first page changes, the tags survive, and the essence digest is unaffected because the
// gain is masked out of the hashed configuration.
func TestOpusSetOutputGainRoundTrip(t *testing.T) {
	src := readFixture(t, sampleOpus)
	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().SetOutputGain(-896).Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.IsNoOp() {
		t.Fatal("a gain change must not plan a no-op")
	}
	ch, ok := changeFor(plan.Changes(), "output gain")
	if !ok {
		t.Fatalf("no output-gain change line: %v", plan.Changes())
	}
	if !slices.Equal(ch.New, []string{"-3.50 dB"}) || !slices.Equal(ch.Old, []string{"0.00 dB"}) {
		t.Errorf("change = %v -> %v, want 0.00 dB -> -3.50 dB", ch.Old, ch.New)
	}

	out := applyToBytes(t, src, plan)
	reparsed := mustParseBytes(t, out)
	if got := reparsed.Properties().First().OutputGain; got != -896 {
		t.Errorf("re-parsed OutputGain = %d, want -896", got)
	}
	if got, want := reparsed.Fields().Title, doc.Fields().Title; got != want {
		t.Errorf("TITLE = %q, want %q (untouched)", got, want)
	}
	page0 := oggPageLen(t, src)
	if !bytes.Equal(out[oggPageLen(t, out):], src[page0:]) {
		t.Error("everything after page 0 must be byte-identical to the input")
	}

	before := essenceOf(t, src)
	after := essenceOf(t, out)
	if before.ExtentVersion != "ogg-opus-packets-v2" {
		t.Errorf("extent = %q, want ogg-opus-packets-v2", before.ExtentVersion)
	}
	if !before.Equal(after) {
		t.Errorf("essence digest changed with the gain: %s vs %s", before, after)
	}
}

// oggPageLen returns the byte length of the Ogg page starting at b[0].
func oggPageLen(t *testing.T, b []byte) int {
	t.Helper()
	if len(b) < 27 || string(b[:4]) != "OggS" {
		t.Fatal("not an Ogg page")
	}
	n := int(b[26])
	total := 27 + n
	for _, seg := range b[27 : 27+n] {
		total += int(seg)
	}
	return total
}

// TestOutputGainSameValueNoOp: setting the gain the file already carries changes nothing.
func TestOutputGainSameValueNoOp(t *testing.T) {
	plan, err := mustParseFile(t, sampleOpus).Edit().SetOutputGain(0).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !plan.IsNoOp() {
		t.Errorf("setting the existing gain should be a no-op; changes = %v", plan.Changes())
	}
}

// TestOutputGainRangeRefused: the gain is a signed 16-bit Q7.8 field, so a value outside
// it cannot be written.
func TestOutputGainRangeRefused(t *testing.T) {
	for _, gain := range []int{32768, -32769, 1 << 20} {
		_, err := mustParseFile(t, sampleOpus).Edit().SetOutputGain(gain).Prepare()
		if !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("SetOutputGain(%d) = %v, want ErrInvalidData", gain, err)
		}
	}
}

// TestOutputGainRefusedOnNonOpus: a format with no output-gain store refuses the edit, or
// under the drop option applies the rest of the edit and reports the discard.
func TestOutputGainRefusedOnNonOpus(t *testing.T) {
	for _, fixture := range []string{sampleFLAC, sampleMP3, sampleOgg} {
		t.Run(fixture, func(t *testing.T) {
			_, err := mustParseFile(t, fixture).Edit().SetOutputGain(-896).Prepare()
			if !errors.Is(err, waxerr.ErrUnsupportedTag) {
				t.Fatalf("Prepare = %v, want ErrUnsupportedTag", err)
			}

			plan, err := mustParseFile(t, fixture).Edit().SetOutputGain(-896).Prepare(wl.WithAllowUnsupportedDrop())
			if err != nil {
				t.Fatalf("Prepare with the drop option: %v", err)
			}
			if !plan.IsNoOp() {
				t.Errorf("a dropped gain leaves nothing to write; changes = %v", plan.Changes())
			}
			if !reportHasWarning(plan.Report().Warnings, wl.WarnOutputGainUnsupported) {
				t.Errorf("expected an output-gain-unsupported warning; got %v", plan.Report().Warnings)
			}

			// A mixed edit still lands the part the format can store.
			src := readFixture(t, fixture)
			mixed, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Still Written").SetOutputGain(-896).
				Prepare(wl.WithAllowUnsupportedDrop())
			if err != nil {
				t.Fatalf("mixed edit: %v", err)
			}
			if got := mustParseBytes(t, applyToBytes(t, src, mixed)).Fields().Title; got != "Still Written" {
				t.Errorf("TITLE = %q, want it written despite the dropped gain", got)
			}
		})
	}
}

// TestOutputGainRefusedOnReadOnly: a file that cannot be written at all refuses with the
// codec's own reason, never a silent no-op, even under the drop option.
func TestOutputGainRefusedOnReadOnly(t *testing.T) {
	for _, opts := range [][]wl.WriteOption{nil, {wl.WithAllowUnsupportedDrop()}} {
		_, err := mustParseFile(t, sampleWMA).Edit().SetOutputGain(-896).Prepare(opts...)
		if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
			t.Errorf("WMA gain edit = %v, want ErrUnsupportedFormat", err)
		}
		_, err = mustParseBytes(t, mp4Fragmented("Fragged")).Edit().SetOutputGain(-896).Prepare(opts...)
		if !errors.Is(err, waxerr.ErrFragmented) {
			t.Errorf("fragmented MP4 gain edit = %v, want ErrFragmented", err)
		}
	}
}

// TestOutputGainZeroOnNonOpusIsNoOp: setting the gain a gainless format already reports
// changes nothing, so it is not refused.
func TestOutputGainZeroOnNonOpusIsNoOp(t *testing.T) {
	plan, err := mustParseFile(t, sampleFLAC).Edit().SetOutputGain(0).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !plan.IsNoOp() {
		t.Errorf("gain 0 on a gainless format should be a no-op; changes = %v", plan.Changes())
	}
}

// TestOutputGainR128Warning: RFC 7845 applies the R128 tags on top of the header gain, so
// leaving them untouched while the header moves is an advisory. Touching them in the same
// edit silences it.
func TestOutputGainR128Warning(t *testing.T) {
	withTags := writeBack(t, sampleOpus, func(e *wl.Editor) {
		e.Set("R128_TRACK_GAIN", "-896")
		e.Set("R128_ALBUM_GAIN", "-512")
	})

	plan, err := mustParseBytes(t, withTags).Edit().SetOutputGain(-896).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !reportHasWarning(plan.Report().Warnings, wl.WarnOutputGainR128Tags) {
		t.Fatalf("expected an R128 advisory; got %v", plan.Report().Warnings)
	}
	var keys []tag.Key
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnOutputGainR128Tags {
			keys = append(keys, w.Keys...)
		}
	}
	if !slices.Contains(keys, "R128_TRACK_GAIN") || !slices.Contains(keys, "R128_ALBUM_GAIN") {
		t.Errorf("advisory keys = %v, want both R128 keys", keys)
	}

	updated, err := mustParseBytes(t, withTags).Edit().SetOutputGain(-896).
		Set("R128_TRACK_GAIN", "0").Clear("R128_ALBUM_GAIN").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if reportHasWarning(updated.Report().Warnings, wl.WarnOutputGainR128Tags) {
		t.Errorf("an edit that updates or clears the R128 tags must not warn; got %v", updated.Report().Warnings)
	}

	only, err := mustParseBytes(t, withTags).Edit().Set("R128_TRACK_GAIN", "-100").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if reportHasWarning(only.Report().Warnings, wl.WarnOutputGainR128Tags) {
		t.Errorf("an R128-only edit changes no header gain and must not warn; got %v", only.Report().Warnings)
	}
}

// TestOutputGainNotCarriedByTransfer: the gain describes the destination's own audio, so a
// metadata copy never carries it.
func TestOutputGainNotCarriedByTransfer(t *testing.T) {
	srcBytes := writeBack(t, sampleOpus, func(e *wl.Editor) { e.SetOutputGain(-896) })
	src := mustParseBytes(t, srcBytes)
	if src.Properties().First().OutputGain != -896 {
		t.Fatalf("setup: source gain = %d, want -896", src.Properties().First().OutputGain)
	}
	dstBytes := readFixture(t, "../testdata/notags.opus")

	plan, _, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Properties().First().OutputGain; got != 0 {
		t.Errorf("destination gain = %d, want its own 0", got)
	}
}
