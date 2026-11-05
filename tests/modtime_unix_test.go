//go:build unix

package waxlabel_test

import (
	"context"
	"syscall"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// setSecNsec stores t in a Timespec's fields, whatever their width; ok is false when t's seconds
// do not fit.
func setSecNsec[S, N ~int32 | ~int64](sec *S, nsec *N, t time.Time) bool {
	*sec, *nsec = S(t.Unix()), N(t.Nanosecond())
	return int64(*sec) == t.Unix()
}

// setFarModTime sets path's mtime to t, outside os.Chtimes' nanosecond range, or skips when this
// platform or filesystem cannot hold it.
func setFarModTime(t *testing.T, path string, when time.Time) {
	t.Helper()
	var ts syscall.Timespec
	if !setSecNsec(&ts.Sec, &ts.Nsec, when) {
		t.Skipf("%v does not fit this platform's timespec", when)
	}
	if err := syscall.UtimesNano(path, []syscall.Timespec{ts, ts}); err != nil {
		t.Skipf("filesystem rejected %v: %v", when, err)
	}
	if got := statMod(t, path); !got.Equal(when) {
		t.Skipf("filesystem stored %v as %v", when, got)
	}
}

// TestPreserveModTimeFarDates: dates outside the int64-nanosecond range survive WithPreserveModTime.
func TestPreserveModTimeFarDates(t *testing.T) {
	for _, when := range []time.Time{
		time.Date(1600, 3, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2300, 3, 1, 12, 0, 0, 500, time.UTC),
	} {
		t.Run(when.Format("2006"), func(t *testing.T) {
			path := copyToTemp(t, sampleFLAC)
			setFarModTime(t, path, when)
			plan, _ := mustParseFile(t, path).Edit().Set(tag.Title, "far").Prepare(wl.WithPreserveModTime())
			if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
				t.Fatal(err)
			}
			if got := statMod(t, path); !got.Equal(when) {
				t.Errorf("WithPreserveModTime: mtime = %v, want %v", got, when)
			}
		})
	}
}
