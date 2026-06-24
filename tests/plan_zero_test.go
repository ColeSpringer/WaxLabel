package waxlabel_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestZeroPlanIsTotal checks that an uninitialized plan, nil or hand-built, returns
// clean errors or zero values instead of panicking.
func TestZeroPlanIsTotal(t *testing.T) {
	ctx := context.Background()
	var nilPlan *wl.Plan
	cases := map[string]*wl.Plan{"nil": nilPlan, "zero": {}}

	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			// Execute returns a clean ErrInvalidData, with no panic.
			doc, res, err := p.Execute(ctx, wl.SaveBack())
			if !errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("Execute err = %v, want ErrInvalidData", err)
			}
			if doc != nil || res.Committed {
				t.Errorf("Execute returned doc=%v res=%+v, want zero", doc, res)
			}

			// Read methods return zero values.
			if got := p.Report(); !reflect.DeepEqual(got, wl.WriteReport{}) {
				t.Errorf("Report() = %+v, want zero WriteReport", got)
			}
			if p.IsNoOp() {
				t.Error("IsNoOp() = true, want false for an uninitialized plan")
			}
			if got := p.Changes(); got != nil {
				t.Errorf("Changes() = %v, want nil", got)
			}
			// String must not print a misleading all-zeros rewrite report.
			if got := p.String(); got != "<uninitialized plan>" {
				t.Errorf("String() = %q, want the uninitialized sentinel", got)
			}
		})
	}
}
