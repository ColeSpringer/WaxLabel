package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestMP3OriginalDateValueReduced checks that ID3v2.3 warns when ORIGINALDATE loses
// month and day precision. A year-only value is already in the stored form, and ID3v2.4
// keeps the full date in TDOR, so neither case warns.
func TestMP3OriginalDateValueReduced(t *testing.T) {
	v23 := append(id3v2(3, textFrame(3, "TIT2", "T")), mp3Audio(t)...)
	v24 := append(id3v2(4, textFrame(4, "TIT2", "T")), mp3Audio(t)...)

	// v2.3 full date is truncated to year.
	full := prepareWith(t, v23, func(e *wl.Editor) { e.Set(tag.OriginalDate, "2019-06-07") })
	if !planWarns(t, full, wl.WarnValueReduced) {
		t.Errorf("v2.3 full ORIGINALDATE should warn value-reduced; got %v", full.Report().Warnings)
	}

	// v2.3 year-only value is already in the stored form.
	year := prepareWith(t, v23, func(e *wl.Editor) { e.Set(tag.OriginalDate, "2019") })
	if planWarns(t, year, wl.WarnValueReduced) {
		t.Errorf("v2.3 year-only ORIGINALDATE must not warn value-reduced; got %v", year.Report().Warnings)
	}

	// v2.4 full date is lossless in TDOR.
	v24full := prepareWith(t, v24, func(e *wl.Editor) { e.Set(tag.OriginalDate, "2019-06-07") })
	if planWarns(t, v24full, wl.WarnValueReduced) {
		t.Errorf("v2.4 full ORIGINALDATE must not warn value-reduced; got %v", v24full.Report().Warnings)
	}

	// An empty set reduces nothing, even if the format omits the field.
	empty := prepareWith(t, v23, func(e *wl.Editor) { e.Set(tag.OriginalDate, "") })
	if planWarns(t, empty, wl.WarnValueReduced) {
		t.Errorf("v2.3 empty ORIGINALDATE must not warn value-reduced; got %v", empty.Report().Warnings)
	}
}
