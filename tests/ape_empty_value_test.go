package waxlabel_test

import (
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// apeStoreFixtures is a file of each format whose tag store is an APE tag.
var apeStoreFixtures = []struct{ name, path string }{
	{"wavpack", sampleWV},
	{"monkeys-audio", sampleAPE},
	{"musepack", "../testdata/chapters.mpc"},
}

// An empty value ahead of or between the values of an APEv2 item is stored and reads back. One at
// the end cannot be told from the terminator some taggers write, so it is dropped with a warning, a
// copy grades it lossy, and values all empty leave one empty item.
func TestAPEEmptyValuesWithinAMultiValue(t *testing.T) {
	for _, f := range apeStoreFixtures {
		for _, c := range []struct {
			name       string
			vals, want []string
			dropped    bool
		}{
			{"interior", []string{"A", "", "B"}, []string{"A", "", "B"}, false},
			{"leading", []string{"", "B"}, []string{"", "B"}, false},
			{"trailing", []string{"A", ""}, []string{"A"}, true},
			{"all empty", []string{"", ""}, []string{""}, true},
		} {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				src := readFixture(t, f.path)
				plan, err := mustParseBytes(t, src).Edit().Set(tag.Artist, c.vals...).Prepare()
				if err != nil {
					t.Fatalf("prepare: %v", err)
				}
				if got := planWarns(t, plan, wl.WarnValueDropped); got != c.dropped {
					t.Errorf("value-dropped warned = %v, want %v: %+v", got, c.dropped, plan.Report().Warnings)
				}
				out := applyToBytes(t, src, plan)
				if got, _ := mustParseBytes(t, out).Tags().Get(tag.Artist); !slices.Equal(got, c.want) {
					t.Errorf("ARTIST = %q, want %q", got, c.want)
				}
				// Written again, the values the file now holds change nothing.
				again, err := mustParseBytes(t, out).Edit().Set(tag.Artist, c.want...).Prepare()
				if err != nil || !again.IsNoOp() {
					t.Errorf("setting the stored values again: noOp=%v err=%v, want a no-op", again != nil && again.IsNoOp(), err)
				}
			})
		}
	}
}

// A copy into an APE format grades a key whose last value is empty lossy, and one whose empty
// values are stored whole carried.
func TestAPETrailingEmptyValueGradesLossy(t *testing.T) {
	flacSrc := readFixture(t, sampleFLAC)
	plan, err := mustParseBytes(t, flacSrc).Edit().Set(tag.Artist, "A", "").Set(tag.Composer, "", "C").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	src := mustParseBytes(t, applyToBytes(t, flacSrc, plan))
	if got, _ := src.Tags().Get(tag.Artist); !slices.Equal(got, []string{"A", ""}) {
		t.Fatalf("source ARTIST = %q, want the empty value held", got)
	}
	report, err := src.PlanTransfer(wl.FormatWavPack)
	if err != nil {
		t.Fatal(err)
	}
	grades := map[tag.Key]wl.Disposition{}
	for _, it := range report.Items {
		if it.Kind == wl.TransferField {
			grades[it.Key] = it.Disposition
		}
	}
	if grades[tag.Artist] != wl.Lossy || grades[tag.Composer] != wl.Carried {
		t.Errorf("grades: ARTIST %v, COMPOSER %v, want lossy and carried", grades[tag.Artist], grades[tag.Composer])
	}
}

// An APEv2 item may hold a zero-length value, so `set KEY=` must store an empty item, not remove
// the key, as every other writable format does. Removal is the job of a clear, which is the
// zero-length value *slice*.
func TestAPEStoresExplicitlyEmptyValue(t *testing.T) {
	for _, c := range apeStoreFixtures {
		t.Run(c.name, func(t *testing.T) {
			src := readFixture(t, c.path)
			if _, ok := mustParseBytes(t, src).Tags().Get(tag.Title); !ok {
				t.Skipf("fixture %s carries no TITLE to empty", c.path)
			}

			plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "").Prepare()
			if err != nil {
				t.Fatalf("set an empty TITLE: %v", err)
			}
			re := mustParseBytes(t, applyToBytes(t, src, plan))
			vals, ok := re.Tags().Get(tag.Title)
			if !ok {
				t.Fatal("an explicitly empty TITLE was removed rather than stored")
			}
			if len(vals) != 1 || vals[0] != "" {
				t.Errorf("stored TITLE = %q, want one empty value", vals)
			}

			// The control: a clear still removes the item, so the two cases stay distinct.
			clearPlan, err := mustParseBytes(t, src).Edit().Clear(tag.Title).Prepare()
			if err != nil {
				t.Fatalf("clear TITLE: %v", err)
			}
			if _, ok := mustParseBytes(t, applyToBytes(t, src, clearPlan)).Tags().Get(tag.Title); ok {
				t.Error("--clear should remove the item, not leave it empty")
			}
		})
	}
}
