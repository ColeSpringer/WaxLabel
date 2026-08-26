package waxlabel_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestMatroskaTechnicalNameMixedEditDropsWithWarning: an edit that changes a real
// key and also supplies a reserved technical name writes the real change, does not
// emit the technical element, and carries a keyed value-dropped warning.
func TestMatroskaTechnicalNameMixedEditDropsWithWarning(t *testing.T) {
	data := readFixture(t, sampleMKA)
	before := bytes.Count(data, []byte("DURATION"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "New Artist").Set(tag.Key("DURATION"), "x").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !hasKeyedValueDropped(plan.Report().Warnings, tag.Key("DURATION")) {
		t.Errorf("no keyed value-dropped warning for DURATION; got %v", plan.Report().Warnings)
	}
	var buf writerTo
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(data))); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := bytes.Count(buf.b, []byte("DURATION")); got != before {
		t.Errorf("DURATION occurrences changed %d -> %d, want unchanged (not written)", before, got)
	}
	if v, _ := mustParseBytes(t, buf.b).Get(tag.Artist); len(v) != 1 || v[0] != "New Artist" {
		t.Errorf("ARTIST = %v, want the real edit applied", v)
	}
}

// TestMatroskaTechnicalNearMissRoundTrips: names adjacent to the reserved set stay
// ordinary custom fields and round-trip.
func TestMatroskaTechnicalNearMissRoundTrips(t *testing.T) {
	data := readFixture(t, sampleMKA)
	_, re := saveMatroska(t, data, mustParseBytes(t, data).Edit().Set(tag.Key("DURATION_X"), "v"))
	if v, _ := re.Get(tag.Key("DURATION_X")); len(v) != 1 || v[0] != "v" {
		t.Errorf("DURATION_X = %v, want [v]", v)
	}
}

// TestMatroskaTechnicalNameSetIsCleanNoOp: setting only reserved technical names
// produces an honest no-op plan carrying a keyed value-dropped warning, and
// executing it changes no bytes; the file never grows an element nothing reads.
func TestMatroskaTechnicalNameSetIsCleanNoOp(t *testing.T) {
	for _, fixture := range []string{sampleMKA, sampleWebM} {
		for _, key := range []string{"DURATION", "BPS", "NUMBER_OF_FRAMES", "_STATISTICS_WRITING_APP"} {
			t.Run(filepath.Base(fixture)+"/"+key, func(t *testing.T) {
				data := readFixture(t, fixture)
				plan, err := mustParseBytes(t, data).Edit().Set(tag.Key(key), "x").Prepare()
				if err != nil {
					t.Fatalf("Prepare: %v", err)
				}
				if !plan.IsNoOp() {
					t.Errorf("IsNoOp() = false, want a clean no-op; operations: %v", plan.Report().Operations)
				}
				if ch := plan.Changes(); len(ch) != 0 {
					t.Errorf("Changes() = %v, want empty", ch)
				}
				if !hasKeyedValueDropped(plan.Report().Warnings, tag.Key(key)) {
					t.Errorf("no keyed value-dropped warning; got %v", plan.Report().Warnings)
				}
				var buf writerTo
				if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(data))); err != nil {
					t.Fatalf("Execute: %v", err)
				}
				if !bytes.Equal(buf.b, data) {
					t.Errorf("no-op write changed bytes")
				}
			})
		}
	}
}
