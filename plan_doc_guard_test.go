package waxlabel

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestPlanWithNilDocIsTotal checks the internal partial-plan case: an inner write plan
// exists, but the source document is nil. Editor.Prepare does not build this today, but
// Plan methods should still return clean zero values or errors rather than dereferencing
// p.doc. This test lives in the internal package because only it can build that shape.
func TestPlanWithNilDocIsTotal(t *testing.T) {
	p := &Plan{plan: &core.WritePlan{}} // inner plan present, doc nil

	if _, _, err := p.Execute(context.Background(), SaveBack()); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("Execute err = %v, want ErrInvalidData (no panic on nil doc dispatch)", err)
	}
	if got := p.String(); got != "<uninitialized plan>" {
		t.Errorf("String() = %q, want the uninitialized sentinel", got)
	}
	if got := p.Changes(); got != nil {
		t.Errorf("Changes() = %v, want nil", got)
	}
	if p.IsNoOp() {
		t.Error("IsNoOp() = true, want false for a doc-less plan")
	}
	if got := p.Report(); !reflect.DeepEqual(got, WriteReport{}) {
		t.Errorf("Report() = %+v, want zero WriteReport", got)
	}
}
