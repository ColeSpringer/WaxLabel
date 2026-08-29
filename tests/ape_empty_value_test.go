package waxlabel_test

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestAPEStoresExplicitlyEmptyValue: an APEv2 item may hold a zero-length value, so
// `set KEY=` must store an empty item rather than removing the key - the behaviour every
// other writable format already has. Removal stays the job of a clear, which is the
// zero-length value *slice*.
func TestAPEStoresExplicitlyEmptyValue(t *testing.T) {
	for _, c := range []struct{ name, path string }{
		{"wavpack", sampleWV},
		{"monkeys-audio", sampleAPE},
	} {
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
