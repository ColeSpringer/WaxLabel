package waxlabel_test

import (
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// hasKeyedValueDropped reports whether ws carries a value-dropped warning naming key.
func hasKeyedValueDropped(ws []wl.Warning, key tag.Key) bool {
	for _, w := range ws {
		if w.Code == wl.WarnValueDropped && slices.Contains(w.Keys, key) {
			return true
		}
	}
	return false
}

// TestID3v23DroppedDateWarns (Fix 2): every ID3-backed format (MP3, AAC, AIFF, WAV)
// writes dates through the shared id3.RebuildFrames, so a v2.3 tag drops a recording or
// original date that has no numeric year (TYER/TORY hold a year, not a free string) -
// and the writer must raise a value-dropped warning naming the key rather than dropping
// it silently. Each fixture carries a v2.3 ID3 tag with RECORDINGDATE=2021 (a TYER
// frame); editing it to an unrepresentable string drops it. Before Fix 2 the value
// vanished with exit 0 and an empty warnings list on all four.
func TestID3v23DroppedDateWarns(t *testing.T) {
	// The shared piece under test is the v2.3 ID3 tag carrying RECORDINGDATE=2021 - a
	// single TYER frame (id3v2/textFrame take the major version, so id3v2(3, ...) pins
	// v2.3). Each format then needs just enough audio/native scaffolding to parse; those
	// payloads are arbitrary filler and their exact sizes do not matter:
	//   - adtsStream(2, 20, 200): 2-channel (stereo), 20 ADTS frames, 200 payload bytes each.
	//   - aiffSSND(400) / wavData(400): 400 bytes of sound-data chunk.
	//   - stdCOMM()/wavFmtPCM(): the minimal required AIFF COMM / WAV fmt chunk.
	cases := []struct {
		name string
		data []byte
	}{
		{"mp3", append(id3v2(3, textFrame(3, "TYER", "2021")), mp3Audio(t)...)},
		{"aac", append(id3v2(3, textFrame(3, "TYER", "2021")), adtsStream(2, 20, 200)...)},
		{"aiff", aiffFile("AIFF", stdCOMM(), aiffSSND(400), aiffID3(id3v2(3, textFrame(3, "TYER", "2021"))))},
		{"wav", wavFile(wavFmtPCM(), wavID3(id3v2(3, textFrame(3, "TYER", "2021"))), wavData(400))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, c.data)
			if v, _ := doc.Tags().First(tag.RecordingDate); v != "2021" {
				t.Fatalf("setup: RecordingDate = %q, want 2021 (a preserved v2.3 tag)", v)
			}
			plan, err := doc.Edit().Set(tag.RecordingDate, "Unknown Date").Prepare()
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if !hasKeyedValueDropped(plan.Report().Warnings, tag.RecordingDate) {
				t.Errorf("v2.3 unrepresentable date must warn value-dropped naming RECORDINGDATE; got %v",
					plan.Report().Warnings)
			}
		})
	}
}

// TestWavDateRetainedInInfoNoWarning (review #1): WAV writes a date to BOTH the native
// LIST/INFO (ICRD = RecordingDate) and, when one is preserved, a v2.3 id3 chunk. The id3
// TYER drops a no-year date, but ICRD keeps it verbatim - so the value round-trips and
// must NOT raise a value-dropped warning (which --strict would escalate to exit 2 on a
// faithfully-stored value). The shared emitter gates on the re-projected output, so a date
// the whole file retains is not flagged; only a date gone from every container warns.
func TestWavDateRetainedInInfoNoWarning(t *testing.T) {
	data := wavFile(wavFmtPCM(),
		wavInfo([2]string{"ICRD", "2020"}),             // native INFO date slot
		wavID3(id3v2(3, textFrame(3, "TYER", "2021"))), // preserved v2.3 id3 chunk
		wavData(400))
	doc := mustParseBytes(t, data)
	plan, err := doc.Edit().Set(tag.RecordingDate, "Unknown Date").Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if hasKeyedValueDropped(plan.Report().Warnings, tag.RecordingDate) {
		t.Errorf("date retained in ICRD must not warn value-dropped; got %v", plan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, data, plan)).Tags().First(tag.RecordingDate); v != "Unknown Date" {
		t.Errorf("date should round-trip via ICRD = %q, want %q", v, "Unknown Date")
	}
}

// TestID3v24DateStoredNoWarning (Fix 2): a v2.4 tag (AAC's from-scratch default) stores
// the date as a free TDRC string, so an unrepresentable value is kept, not dropped - and
// must NOT raise a false value-dropped warning. This is the other half of the contract:
// the warning fires only where the value is actually lost.
func TestID3v24DateStoredNoWarning(t *testing.T) {
	// A bare ADTS stream gets a brand-new ID3v2.4 tag on write (DefaultID3Version(AAC)).
	doc := mustParseBytes(t, adtsStream(2, 20, 200))
	plan, err := doc.Edit().Set(tag.RecordingDate, "Unknown Date").Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if hasKeyedValueDropped(plan.Report().Warnings, tag.RecordingDate) {
		t.Errorf("v2.4 stores the date string, so it must not warn value-dropped; got %v",
			plan.Report().Warnings)
	}
	// And the value really is stored (round-trips), proving the no-warning is correct.
	re := mustParseBytes(t, applyToBytes(t, adtsStream(2, 20, 200), plan))
	if v, _ := re.Tags().First(tag.RecordingDate); v != "Unknown Date" {
		t.Errorf("v2.4 RecordingDate round-trip = %q, want %q", v, "Unknown Date")
	}
}

// TestID3v23DroppedDateWarnsOnNoOp (Fix 2): when the dropped date is the only edit and
// the file had no date to begin with, the write produces no byte change - yet the
// value-dropped warning must still surface (and --strict still escalate) rather than
// vanish behind the no-op downgrade, exactly as the MP4 picture-metadata warning does.
// A bare ID3v2.3 MP3 (a TYER-less tag, here a tagless stream that gets a fresh v2.3 tag)
// set only to an unrepresentable date is the case.
func TestID3v23DroppedDateWarnsOnNoOp(t *testing.T) {
	doc := mustParseBytes(t, mp3Audio(t)) // tagless: a new tag is ID3v2.3 (MP3 default)
	plan, err := doc.Edit().Set(tag.RecordingDate, "Unknown Date").Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !plan.IsNoOp() {
		t.Fatalf("an unrepresentable date as the only edit on a date-less file should be a no-op write")
	}
	if !hasKeyedValueDropped(plan.Report().Warnings, tag.RecordingDate) {
		t.Errorf("the value-dropped warning must survive the no-op downgrade; got %v", plan.Report().Warnings)
	}
}
