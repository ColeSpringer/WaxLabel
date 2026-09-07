package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
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

// applyPlan executes a prepared plan against src and returns the written bytes.
func applyPlan(t *testing.T, plan *wl.Plan, src []byte) []byte {
	t.Helper()
	var buf writerTo
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(src))); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return buf.b
}

// changesMention reports whether the change list names key.
func changesMention(changes []tag.Change, key tag.Key) bool {
	return slices.ContainsFunc(changes, func(c tag.Change) bool { return c.Key == key })
}

// r128Tagged writes an Opus file carrying both R128 loudness tags at the given values.
func r128Tagged(t *testing.T, track, album string) []byte {
	t.Helper()
	return writeBack(t, sampleOpus, func(e *wl.Editor) {
		e.Set("R128_TRACK_GAIN", track)
		e.Set("R128_ALBUM_GAIN", album)
	})
}

// r128WarningKeys returns the keys every R128 advisory in the report names.
func r128WarningKeys(warnings []wl.Warning) []tag.Key {
	var keys []tag.Key
	for _, w := range warnings {
		if w.Code == wl.WarnOutputGainR128Tags {
			keys = append(keys, w.Keys...)
		}
	}
	return keys
}

// TestOutputGainRebasesR128Tags: RFC 7845 applies the R128 tags on top of the header gain,
// so a header change subtracts the same delta from each of them. The loudness a compliant
// player produces is then unchanged, and there is nothing to warn about.
func TestOutputGainRebasesR128Tags(t *testing.T) {
	withTags := r128Tagged(t, "-896", "-512")
	plan, err := mustParseBytes(t, withTags).Edit().SetOutputGain(-896).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if reportHasWarning(plan.Report().Warnings, wl.WarnOutputGainR128Tags) {
		t.Errorf("a rebase leaves nothing to advise about; got %v", plan.Report().Warnings)
	}
	changes := plan.Changes()
	for _, want := range []tag.Key{"R128_TRACK_GAIN", "R128_ALBUM_GAIN"} {
		if !changesMention(changes, want) {
			t.Errorf("Changes() should list %s; got %v", want, changes)
		}
	}

	out := applyPlan(t, plan, withTags)
	doc := mustParseBytes(t, out)
	if got := doc.Properties().First().OutputGain; got != -896 {
		t.Errorf("OutputGain = %d, want -896", got)
	}
	// header + tag is what a player applies, and it must be the same as before the edit.
	for _, c := range []struct {
		key  tag.Key
		want string
	}{{"R128_TRACK_GAIN", "0"}, {"R128_ALBUM_GAIN", "384"}} {
		if got, _ := doc.Tags().First(c.key); got != c.want {
			t.Errorf("%s = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestOutputGainR128ExplicitOpsWin: a Set or Clear of an R128 key in the same edit is the
// caller's own intent, which the rebase must not overwrite.
func TestOutputGainR128ExplicitOpsWin(t *testing.T) {
	withTags := r128Tagged(t, "-896", "-512")
	plan, err := mustParseBytes(t, withTags).Edit().SetOutputGain(-896).
		Set("R128_TRACK_GAIN", "0").Clear("R128_ALBUM_GAIN").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if reportHasWarning(plan.Report().Warnings, wl.WarnOutputGainR128Tags) {
		t.Errorf("an explicit op says what the tag should be; got %v", plan.Report().Warnings)
	}
	doc := mustParseBytes(t, applyPlan(t, plan, withTags))
	if got, _ := doc.Tags().First("R128_TRACK_GAIN"); got != "0" {
		t.Errorf("R128_TRACK_GAIN = %q, want the explicit 0", got)
	}
	if doc.Tags().Has("R128_ALBUM_GAIN") {
		t.Error("R128_ALBUM_GAIN should have been cleared")
	}
}

// TestOutputGainR128OnlyEditDoesNotRebase: with no header change there is no delta, so an
// edit that only touches the tags leaves every other value alone.
func TestOutputGainR128OnlyEditDoesNotRebase(t *testing.T) {
	withTags := r128Tagged(t, "-896", "-512")
	plan, err := mustParseBytes(t, withTags).Edit().Set("R128_TRACK_GAIN", "-100").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if reportHasWarning(plan.Report().Warnings, wl.WarnOutputGainR128Tags) {
		t.Errorf("an R128-only edit changes no header gain and must not warn; got %v", plan.Report().Warnings)
	}
	doc := mustParseBytes(t, applyPlan(t, plan, withTags))
	if got, _ := doc.Tags().First("R128_ALBUM_GAIN"); got != "-512" {
		t.Errorf("R128_ALBUM_GAIN = %q, want the untouched -512", got)
	}
}

// TestOutputGainR128MalformedWarns: a value that is not a Q7.8 integer cannot be rebased, so
// it is kept and advised about. Surrounding whitespace is not malformed - those values trim.
func TestOutputGainR128MalformedWarns(t *testing.T) {
	withTags := writeBack(t, sampleOpus, func(e *wl.Editor) {
		e.Set("R128_TRACK_GAIN", "abc")
		e.Set("R128_ALBUM_GAIN", " -573 ")
	})
	plan, err := mustParseBytes(t, withTags).Edit().SetOutputGain(-896).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if keys := r128WarningKeys(plan.Report().Warnings); !slices.Equal(keys, []tag.Key{"R128_TRACK_GAIN"}) {
		t.Errorf("advisory keys = %v, want just the malformed R128_TRACK_GAIN", keys)
	}
	doc := mustParseBytes(t, applyPlan(t, plan, withTags))
	if got, _ := doc.Tags().First("R128_TRACK_GAIN"); got != "abc" {
		t.Errorf("R128_TRACK_GAIN = %q, want the unrebasable value kept", got)
	}
	if got, _ := doc.Tags().First("R128_ALBUM_GAIN"); got != "323" {
		t.Errorf("R128_ALBUM_GAIN = %q, want -573 rebased by -896 to 323", got)
	}
}

// TestOutputGainR128RebaseOverflowRefused: the R128 fields are signed 16-bit, so a rebase
// that would leave the range refuses the edit rather than storing a number the field cannot
// hold. It is a write refusal, not a corrupt file: the value on disk is legal and the gain
// edit is legal, so ErrInvalidData (which the CLI renders as "corrupt or violates its
// format") would be a false report. The message names the way out.
func TestOutputGainR128RebaseOverflowRefused(t *testing.T) {
	withTags := writeBack(t, sampleOpus, func(e *wl.Editor) { e.Set("R128_TRACK_GAIN", "-32000") })
	_, err := mustParseBytes(t, withTags).Edit().SetOutputGain(3000).Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("Prepare error = %v, want ErrUnsupportedTag", err)
	}
	if errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("a legal value and a legal edit must not report a corrupt file: %v", err)
	}
	if !strings.Contains(err.Error(), "--keep-r128") {
		t.Errorf("the refusal should name the escape hatch: %v", err)
	}
	// -32768 is a legal RFC 7845 value, so it must not be the reason an unrelated edit fails
	// until the rebase actually leaves the range.
	legal := writeBack(t, sampleOpus, func(e *wl.Editor) { e.Set("R128_TRACK_GAIN", "-32768") })
	if _, err := mustParseBytes(t, legal).Edit().SetOutputGain(-256).Set(tag.Title, "x").Prepare(); err != nil {
		t.Errorf("a rebase that stays in range must not fail: %v", err)
	}
}

// TestOutputGainR128MultiValueIsAtomic: a key is rebased whole or not at all, so the
// advisory's "not rebased" is true of every value under it rather than half of them.
func TestOutputGainR128MultiValueIsAtomic(t *testing.T) {
	withTags := writeBack(t, sampleOpus, func(e *wl.Editor) {
		e.Set("R128_TRACK_GAIN", "-896", "abc", "-512")
	})
	plan, err := mustParseBytes(t, withTags).Edit().SetOutputGain(-896).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !reportHasWarning(plan.Report().Warnings, wl.WarnOutputGainR128Tags) {
		t.Fatalf("expected the unrebasable-value advisory; got %v", plan.Report().Warnings)
	}
	got, _ := mustParseBytes(t, applyPlan(t, plan, withTags)).Tags().Get("R128_TRACK_GAIN")
	if want := []string{"-896", "abc", "-512"}; !slices.Equal(got, want) {
		t.Errorf("R128_TRACK_GAIN = %v, want every value kept as found %v", got, want)
	}
}

// TestOutputGainR128NotRebasedWhenGainDropped: no header moved, so the tags stay put.
func TestOutputGainR128NotRebasedWhenGainDropped(t *testing.T) {
	withTags := writeBack(t, "../testdata/sample.mp3", func(e *wl.Editor) {
		e.Set("R128_TRACK_GAIN", "-896")
	})
	plan, err := mustParseBytes(t, withTags).Edit().SetOutputGain(-512).
		Prepare(wl.WithAllowUnsupportedDrop())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !reportHasWarning(plan.Report().Warnings, wl.WarnOutputGainUnsupported) {
		t.Fatalf("expected the gain drop warning; got %v", plan.Report().Warnings)
	}
	if reportHasWarning(plan.Report().Warnings, wl.WarnOutputGainR128Tags) {
		t.Errorf("a dropped gain moves no header, so nothing to rebase; got %v", plan.Report().Warnings)
	}
	doc := mustParseBytes(t, applyPlan(t, plan, withTags))
	if got, _ := doc.Tags().First("R128_TRACK_GAIN"); got != "-896" {
		t.Errorf("R128_TRACK_GAIN = %q, want the untouched -896", got)
	}
}

// TestOutputGainKeepR128Gains: the opt-out leaves both values as found and says so, for a
// caller who knows the stored figures are stale.
func TestOutputGainKeepR128Gains(t *testing.T) {
	withTags := r128Tagged(t, "-896", "-512")
	plan, err := mustParseBytes(t, withTags).Edit().SetOutputGain(-896).Prepare(wl.WithKeepR128Gains())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	keys := r128WarningKeys(plan.Report().Warnings)
	if !slices.Contains(keys, "R128_TRACK_GAIN") || !slices.Contains(keys, "R128_ALBUM_GAIN") {
		t.Errorf("advisory keys = %v, want both R128 keys", keys)
	}
	for _, k := range []tag.Key{"R128_TRACK_GAIN", "R128_ALBUM_GAIN"} {
		if changesMention(plan.Changes(), k) {
			t.Errorf("Changes() should list only the gain, not %s; got %v", k, plan.Changes())
		}
	}
	doc := mustParseBytes(t, applyPlan(t, plan, withTags))
	for _, c := range []struct {
		key  tag.Key
		want string
	}{{"R128_TRACK_GAIN", "-896"}, {"R128_ALBUM_GAIN", "-512"}} {
		if got, _ := doc.Tags().First(c.key); got != c.want {
			t.Errorf("%s = %q, want the kept %q", c.key, got, c.want)
		}
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
