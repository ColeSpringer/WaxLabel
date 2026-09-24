package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// ISFT projects to ENCODER, so dump reports the software stamp ffprobe shows as encoder=.
func TestWAVISFTReadsAsEncoder(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Song"}, [2]string{"ISFT", "Lavf61.7.100"}), wavData(400))
	doc := mustParseBytes(t, data)
	if v, _ := doc.Get(tag.Encoder); !slices.Equal(v, []string{"Lavf61.7.100"}) {
		t.Errorf("ENCODER = %v, want [Lavf61.7.100] from ISFT", v)
	}
	// The stamp is still noise; mapping it changes where it lives, not what it is.
	if !hasWarning(doc, wl.WarnInheritedEncoder) {
		t.Error("a mapped ISFT stamp should still flag inherited-encoder")
	}
}

// ENCODER's INFO home is used on write: setting it on a LIST/INFO-only WAV updates the ISFT item
// instead of spawning an id3 chunk.
func TestWAVEncoderWritesISFTNotID3(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Song"}), wavData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Encoder, "MyTagger 1.0").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("id3 ")) {
		t.Error("an ENCODER edit should not create an id3 chunk")
	}
	if !bytes.Contains(out, []byte("ISFT")) {
		t.Error("ENCODER should be written to the ISFT INFO item")
	}
	if v, _ := mustParseBytes(t, out).Get(tag.Encoder); !slices.Equal(v, []string{"MyTagger 1.0"}) {
		t.Errorf("round-trip ENCODER = %v, want [MyTagger 1.0]", v)
	}
}

// WithStripEncoderStamp alone (no patch) removes the ISFT item instead of re-rendering it from
// the value it just dropped.
func TestWAVStripEncoderStampRemovesISFT(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Song"}, [2]string{"ISFT", "Lavf61.7.100"}), wavData(400))
	plan, err := mustParseBytes(t, data).Edit().Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("stripping a present ISFT stamp must not be a no-op")
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("ISFT")) {
		t.Error("the ISFT stamp item survived the strip")
	}
	re := mustParseBytes(t, out)
	if v, ok := re.Get(tag.Encoder); ok {
		t.Errorf("ENCODER = %v, want absent after the strip", v)
	}
	if re.Fields().Title != "Song" {
		t.Errorf("title = %q, want Song (the strip must not disturb other items)", re.Fields().Title)
	}
}

// the strip must not collide with an explicit edit: the CLI turns WithStripEncoderStamp on for any
// ENCODER edit, so a strip that judged the canonical value instead of the item would filter the
// user's own --set ENCODER=Lavf... out of the only container that could hold it and write nothing.
func TestWAVAuthoredStampValueIsWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		info []byte
	}{
		{"no INFO chunk", nil},
		{"stamped ISFT already present", wavInfo([2]string{"ISFT", "Lavf61.7.100"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := []([]byte){wavFmtPCM()}
			if tc.info != nil {
				chunks = append(chunks, tc.info)
			}
			data := wavFile(append(chunks, wavData(400))...)
			plan, err := mustParseBytes(t, data).Edit().Set(tag.Encoder, "Lavf62.3.100").
				Prepare(wl.WithStripEncoderStamp())
			if err != nil {
				t.Fatal(err)
			}
			if plan.IsNoOp() {
				t.Fatal("an explicit ENCODER edit must not collapse into a no-op")
			}
			if v, _ := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tag.Encoder); !slices.Equal(v, []string{"Lavf62.3.100"}) {
				t.Errorf("ENCODER = %v, want [Lavf62.3.100]: the strip must not discard a value the edit authored", v)
			}
		})
	}
}

// with an id3 chunk present the canonical ENCODER is that chunk's TSSE, so a strip that judged it
// would delete a clean user ISFT because of a stamp in another container, and would leave a stamped
// ISFT in place when the TSSE is clean. The second case is unfixable: lint --fix has no canonical
// value to clear and the plan collapses to a no-op.
func TestWAVStripJudgesTheItemNotTheCanonicalValue(t *testing.T) {
	t.Run("clean ISFT survives a stamped TSSE", func(t *testing.T) {
		data := wavFile(wavFmtPCM(), wavInfo([2]string{"ISFT", "Sound Forge 10"}),
			wavID3(id3v2(4, textFrame(4, "TSSE", "Lavf61.7.100"))), wavData(400))
		plan, err := mustParseBytes(t, data).Edit().Prepare(wl.WithStripEncoderStamp())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(applyToBytes(t, data, plan), []byte("Sound Forge 10")) {
			t.Error("the option deleted a clean user ISFT because another container held a stamp")
		}
	})
	t.Run("stamped ISFT goes despite a clean TSSE", func(t *testing.T) {
		data := wavFile(wavFmtPCM(), wavInfo([2]string{"ISFT", "Lavf61.7.100"}),
			wavID3(id3v2(4, textFrame(4, "TSSE", "My Editor 1.0"))), wavData(400))
		doc := mustParseBytes(t, data)
		if !hasWarning(doc, wl.WarnInheritedEncoder) {
			t.Fatal("setup: the stamped ISFT should flag inherited-encoder")
		}
		fix := doc.PlanLintFix()
		plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
		if err != nil {
			t.Fatal(err)
		}
		if plan.IsNoOp() {
			t.Fatal("lint --fix collapsed to a no-op; the file could never be cleaned")
		}
		if re := mustParseBytes(t, applyToBytes(t, data, plan)); hasWarning(re, wl.WarnInheritedEncoder) {
			t.Error("the stamped ISFT survived lint --fix")
		}
	})
}

// an edit INFO cannot represent forces an id3 chunk. The canonical ENCODER must not reach it from
// the ISFT item, or WaxLabel would author a second copy of the noise it lints against.
func TestWAVUnrelatedEditDoesNotPromoteStamp(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Song"}, [2]string{"ISFT", "Lavf61.7.100"}), wavData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.DiscNumber, "1").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("id3 ")) {
		t.Fatal("setup: a disc number has no INFO slot and should force an id3 chunk")
	}
	if bytes.Contains(out, []byte("TSSE")) {
		t.Error("the inherited stamp was promoted into the newly created id3 chunk")
	}
	re := mustParseBytes(t, out)
	if n := countWarnings(re, wl.WarnInheritedEncoder); n != 1 {
		t.Errorf("inherited-encoder warnings = %d, want 1 (the stamp must not be duplicated)", n)
	}
	// The ISFT itself is untouched: refusing to author the stamp elsewhere is not a reason
	// to delete the one the file came with.
	if !bytes.Contains(out, []byte("Lavf61.7.100")) {
		t.Error("the original ISFT stamp was deleted")
	}
}

// countWarnings counts a document's warnings carrying the given code.
func countWarnings(doc *wl.Document, code wl.WarningCode) int {
	n := 0
	for _, w := range doc.Warnings() {
		if w.Code == code {
			n++
		}
	}
	return n
}

// replacing a stamped ISFT is a rewrite the change list already describes, not a strip.
func TestWAVSetEncoderOverStampIsNotAStrip(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"ISFT", "Lavf61.7.100"}), wavData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Encoder, "MyTagger 1.0").Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Report().Operations, "ISFT encoder stamp strip") {
		t.Errorf("replacing a stamp reported it as stripped: %v", plan.Report().Operations)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tag.Encoder); !slices.Equal(v, []string{"MyTagger 1.0"}) {
		t.Errorf("ENCODER = %v, want [MyTagger 1.0]", v)
	}
}

// write side of the IPRT="4/9" split: the read path splits the pair into TRACKNUMBER and
// TRACKTOTAL, and the write recombines it into the one item it came from, so an INFO-only file is
// not restructured into an id3 chunk to hold the total the read had just derived.
func TestWAVSlashedTrackStaysInInfo(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Old"}, [2]string{"IPRT", "4/9"}), wavData(400))
	doc := mustParseBytes(t, data)
	if got := doc.Fields().TrackNumber; got != 4 {
		t.Fatalf("track = %d, want 4", got)
	}
	if v, _ := doc.Get(tag.TrackTotal); !slices.Equal(v, []string{"9"}) {
		t.Fatalf("TRACKTOTAL = %v, want [9]", v)
	}
	plan, err := doc.Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("id3 ")) {
		t.Error("a slashed IPRT should not force an id3 chunk")
	}
	if !bytes.Contains(out, append([]byte("4/9"), 0)) {
		t.Error("IPRT should be rewritten in the slash form it was read from")
	}
	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.TrackTotal); !slices.Equal(v, []string{"9"}) {
		t.Errorf("round-trip TRACKTOTAL = %v, want [9]", v)
	}
	if re.Fields().TrackNumber != 4 {
		t.Errorf("round-trip track = %d, want 4", re.Fields().TrackNumber)
	}
}

// boundary of the pair recombination: RIFF INFO has no disc identifier, so a disc number has no
// item to go in and promotes the file to an id3 chunk.
func TestWAVDiscTotalStillForcesID3(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Song"}), wavData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.DiscNumber, "1").Set(tag.DiscTotal, "2").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("id3 ")) {
		t.Error("a disc number has no INFO identifier and should force an id3 chunk")
	}
	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.DiscTotal); !slices.Equal(v, []string{"2"}) {
		t.Errorf("DISCTOTAL = %v, want [2]", v)
	}
}

// other boundary: TRACKTOTAL is representable only as the tail of an IPRT, so without a track
// number there is no item to write it into.
func TestWAVTrackTotalAloneForcesID3(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Song"}), wavData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.TrackTotal, "9").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("id3 ")) {
		t.Error("a bare TRACKTOTAL has no INFO item to ride on and should force an id3 chunk")
	}
	if v, _ := mustParseBytes(t, out).Get(tag.TrackTotal); !slices.Equal(v, []string{"9"}) {
		t.Errorf("TRACKTOTAL = %v, want [9]", v)
	}
}

// the join must survive the read: a non-numeric track number composes to "A1/9", which reads back
// as one literal value with the total lost, so the pair is not representable in INFO and the total
// must not be written there.
func TestWAVSlashedTrackRejectsNonNumericPair(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Song"}, [2]string{"IPRT", "A1"}), wavData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.TrackTotal, "9").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnValueDropped); !ok {
		t.Errorf("the unstorable total was dropped silently: %v", plan.Report().Warnings)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, append([]byte("A1/9"), 0)) {
		t.Error("the track number was corrupted into a slash pair the read path cannot split back")
	}
	re := mustParseBytes(t, out)
	if got := re.Fields().TrackNumber; got != 0 {
		t.Errorf("TRACKNUMBER parsed as %d; the literal should stay unparseable", got)
	}
	if v, _ := re.Get(tag.TrackNumber); !slices.Equal(v, []string{"A1"}) {
		t.Errorf("TRACKNUMBER = %v, want [A1] untouched", v)
	}
}
