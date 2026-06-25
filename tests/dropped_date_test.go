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

// hasKeyedValueReduced reports whether ws carries a value-reduced warning naming key.
func hasKeyedValueReduced(ws []wl.Warning, key tag.Key) bool {
	for _, w := range ws {
		if w.Code == wl.WarnValueReduced && slices.Contains(w.Keys, key) {
			return true
		}
	}
	return false
}

// TestID3v23MonthOnlyDateReduced verifies that a YYYY-MM RECORDINGDATE carries month precision a
// v2.3 TDAT cannot represent (TDAT needs a full DDMM), so only TYER (the year) is written
// and the month is silently lost. The writer must raise a value-reduced warning naming the
// key unless the full value remains the file's authoritative projection. Because rendering
// "2021-03" leaves TYER="2021" unchanged, the write is a no-op, so the warning also exercises
// the no-op carry-forward (DowngradeNoOp).
func TestID3v23MonthOnlyDateReduced(t *testing.T) {
	// Pure v2.3 MP3, no other container: the authoritative value reduces to "2021", so warn.
	mp3 := append(id3v2(3, textFrame(3, "TYER", "2021")), mp3Audio(t)...)
	mplan := prepareWith(t, mp3, func(e *wl.Editor) { e.Set(tag.RecordingDate, "2021-03") })
	if !hasKeyedValueReduced(mplan.Report().Warnings, tag.RecordingDate) {
		t.Errorf("v2.3 YYYY-MM RECORDINGDATE must warn value-reduced; got %v", mplan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, mp3, mplan)).Tags().First(tag.RecordingDate); v != "2021" {
		t.Errorf("v2.3 month-only date should reduce to the year; round-trip = %q, want 2021", v)
	}

	// WAV carrying a preserved v2.3 id3 chunk: id3 wins the read precedence, so the
	// authoritative projection is the reduced "2021" even though INFO ICRD physically
	// holds "2021-03" - suppressing here would reintroduce the silent loss this test guards
	// (dump would show "2021" with no warning), so it must warn.
	wavID3Preserved := wavFile(wavFmtPCM(), wavID3(id3v2(3, textFrame(3, "TYER", "2021"))), wavData(400))
	wplan := prepareWith(t, wavID3Preserved, func(e *wl.Editor) { e.Set(tag.RecordingDate, "2021-03") })
	if !hasKeyedValueReduced(wplan.Report().Warnings, tag.RecordingDate) {
		t.Errorf("WAV with a preserved v2.3 id3 chunk reduces to the year authoritatively; must warn; got %v", wplan.Report().Warnings)
	}

	// WAV with no id3 chunk: RECORDINGDATE writes only the native INFO ICRD (verbatim), so
	// the full "2021-03" is the authoritative projection, so nothing was lost and no warning
	// should be raised.
	wavInfoOnly := wavFile(wavFmtPCM(), wavData(400))
	wiplan := prepareWith(t, wavInfoOnly, func(e *wl.Editor) { e.Set(tag.RecordingDate, "2021-03") })
	if hasKeyedValueReduced(wiplan.Report().Warnings, tag.RecordingDate) {
		t.Errorf("INFO-only WAV keeps the full 2021-03, so it must not warn value-reduced; got %v", wiplan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, wavInfoOnly, wiplan)).Tags().First(tag.RecordingDate); v != "2021-03" {
		t.Errorf("INFO-only WAV should round-trip the full month; got %q, want 2021-03", v)
	}

	// v2.4 (AAC's from-scratch default) stores the full date in TDRC: no reduction, no warning.
	aac := adtsStream(2, 20, 200)
	aplan := prepareWith(t, aac, func(e *wl.Editor) { e.Set(tag.RecordingDate, "2021-03") })
	if hasKeyedValueReduced(aplan.Report().Warnings, tag.RecordingDate) {
		t.Errorf("v2.4 stores the full date in TDRC, so it must not warn value-reduced; got %v", aplan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, aac, aplan)).Tags().First(tag.RecordingDate); v != "2021-03" {
		t.Errorf("v2.4 RECORDINGDATE round-trip = %q, want 2021-03", v)
	}
}

// TestID3v23OriginalDateReduced verifies that ORIGINALDATE on a v2.3 tag is written to
// TORY, which holds the year only, so a YYYY-MM(-DD) value loses its sub-year precision.
// Every ID3-backed codec (MP3, AAC, AIFF, WAV) must raise a value-reduced warning naming
// the key. Each fixture carries a preserved v2.3 tag so the write version is v2.3, and the
// round-trip confirms the real reduction.
func TestID3v23OriginalDateReduced(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"mp3", append(id3v2(3, textFrame(3, "TIT2", "T")), mp3Audio(t)...)},
		{"aac", append(id3v2(3, textFrame(3, "TIT2", "T")), adtsStream(2, 20, 200)...)},
		{"aiff", aiffFile("AIFF", stdCOMM(), aiffSSND(400), aiffID3(id3v2(3, textFrame(3, "TIT2", "T"))))},
		{"wav", wavFile(wavFmtPCM(), wavID3(id3v2(3, textFrame(3, "TIT2", "T"))), wavData(400))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := prepareWith(t, c.data, func(e *wl.Editor) { e.Set(tag.OriginalDate, "2021-03") })
			if !hasKeyedValueReduced(plan.Report().Warnings, tag.OriginalDate) {
				t.Errorf("v2.3 ORIGINALDATE month precision must warn value-reduced; got %v", plan.Report().Warnings)
			}
			if v, _ := mustParseBytes(t, applyToBytes(t, c.data, plan)).Tags().First(tag.OriginalDate); v != "2021" {
				t.Errorf("v2.3 ORIGINALDATE should reduce to the year; round-trip = %q, want 2021", v)
			}
		})
	}

	// v2.4 (a fresh AAC tag's default) stores the full date in TDOR: no reduction, no warning.
	aac := adtsStream(2, 20, 200)
	aplan := prepareWith(t, aac, func(e *wl.Editor) { e.Set(tag.OriginalDate, "2021-03") })
	if hasKeyedValueReduced(aplan.Report().Warnings, tag.OriginalDate) {
		t.Errorf("v2.4 stores the full ORIGINALDATE in TDOR, so it must not warn value-reduced; got %v", aplan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, aac, aplan)).Tags().First(tag.OriginalDate); v != "2021-03" {
		t.Errorf("v2.4 ORIGINALDATE round-trip = %q, want 2021-03", v)
	}
}

// TestID3v23NonCanonicalDateReduced verifies that a v2.3 TDAT needs a canonical YYYY-MM-DD,
// so a non-canonical partial date ("2021-3", "2021-03-1") drops its month/day to the year
// just as the canonical "2021-03" does - and the tool stores values verbatim (no
// normalization), so these forms are reachable and must warn value-reduced too. A bare year
// is fully stored in TYER and must not warn.
func TestID3v23NonCanonicalDateReduced(t *testing.T) {
	mp3 := append(id3v2(3, textFrame(3, "TIT2", "T")), mp3Audio(t)...)
	for _, v := range []string{"2021-3", "2021-03-1"} {
		plan := prepareWith(t, mp3, func(e *wl.Editor) { e.Set(tag.RecordingDate, v) })
		if !hasKeyedValueReduced(plan.Report().Warnings, tag.RecordingDate) {
			t.Errorf("v2.3 non-canonical %q must warn value-reduced; got %v", v, plan.Report().Warnings)
		}
		if rt, _ := mustParseBytes(t, applyToBytes(t, mp3, plan)).Tags().First(tag.RecordingDate); rt != "2021" {
			t.Errorf("non-canonical %q should reduce to the year; round-trip = %q, want 2021", v, rt)
		}
	}
	// A bare year is fully stored in TYER - no reduction, no warning.
	yearPlan := prepareWith(t, mp3, func(e *wl.Editor) { e.Set(tag.RecordingDate, "2021") })
	if hasKeyedValueReduced(yearPlan.Report().Warnings, tag.RecordingDate) {
		t.Errorf("a bare year must not warn value-reduced; got %v", yearPlan.Report().Warnings)
	}
}

// TestID3v23SubDayDateReduced verifies that an hour with no minute
// ("2021-03-15T10") has a full date (TYER+TDAT render) but TIME needs a full HH:MM, so the
// hour is silently dropped on a v2.3 write - a sub-day reduction the month-only check missed.
// It must warn value-reduced and round-trip to the date; a full date-time renders losslessly
// (TYER+TDAT+TIME) and must not warn.
func TestID3v23SubDayDateReduced(t *testing.T) {
	mp3 := append(id3v2(3, textFrame(3, "TIT2", "T")), mp3Audio(t)...)

	plan := prepareWith(t, mp3, func(e *wl.Editor) { e.Set(tag.RecordingDate, "2021-03-15T10") })
	if !hasKeyedValueReduced(plan.Report().Warnings, tag.RecordingDate) {
		t.Errorf("v2.3 date+hour-without-minute must warn value-reduced; got %v", plan.Report().Warnings)
	}
	if rt, _ := mustParseBytes(t, applyToBytes(t, mp3, plan)).Tags().First(tag.RecordingDate); rt != "2021-03-15" {
		t.Errorf("date+hour should reduce to the date; round-trip = %q, want 2021-03-15", rt)
	}

	full := prepareWith(t, mp3, func(e *wl.Editor) { e.Set(tag.RecordingDate, "2021-03-15T10:30") })
	if hasKeyedValueReduced(full.Report().Warnings, tag.RecordingDate) {
		t.Errorf("a full date-time renders losslessly (TYER+TDAT+TIME), so it must not warn; got %v", full.Report().Warnings)
	}
}

// TestID3v23DroppedDateWarns: every ID3-backed format (MP3, AAC, AIFF, WAV)
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

// TestWavDateRetainedInInfoNoWarning verifies that WAV writes a date to both the native
// LIST/INFO (ICRD = RecordingDate) and, when one is preserved, a v2.3 id3 chunk. The id3
// TYER drops a no-year date, but ICRD keeps it verbatim - so the value round-trips and
// must not raise a value-dropped warning (which --strict would escalate to exit 2 on a
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

// TestID3v24DateStoredNoWarning: a v2.4 tag (AAC's from-scratch default) stores
// the date as a free TDRC string, so an unrepresentable value is kept, not dropped - and
// must not raise a false value-dropped warning. This is the other half of the contract:
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

// TestID3v23DroppedDateWarnsOnNoOp: when the dropped date is the only edit and
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
