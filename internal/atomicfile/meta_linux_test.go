package atomicfile

import (
	"errors"
	"syscall"
	"testing"
)

// TestXattrReadFailedRead: an attribute that disappears between sizing and reading is an error,
// not a slice of the read's -1 return.
func TestXattrReadFailedRead(t *testing.T) {
	calls := 0
	got, err := xattrRead(func(b []byte) (int, error) {
		if calls++; calls == 1 {
			return 4, nil
		}
		return -1, syscall.ENODATA
	})
	if !errors.Is(err, syscall.ENODATA) || got != nil {
		t.Errorf("xattrRead = (%q, %v), want (nil, ENODATA)", got, err)
	}
}
