package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// TestAudioDigestTrackID checks that TrackID participates in equality while the zero value
// keeps the original digest string format.
func TestAudioDigestTrackID(t *testing.T) {
	base := wl.AudioDigest{Algorithm: "sha256", ExtentVersion: "v1", Sum: []byte{0xAB, 0xCD}}

	t1 := base
	t1.TrackID = 1
	if base.Equal(t1) {
		t.Error("digests with different TrackID must not be Equal")
	}
	if !base.Equal(wl.AudioDigest{Algorithm: "sha256", ExtentVersion: "v1", Sum: []byte{0xAB, 0xCD}}) {
		t.Error("identical digests (TrackID 0) must be Equal")
	}

	if got := base.String(); got != "sha256/v1:abcd" {
		t.Errorf("String (TrackID 0) = %q, want sha256/v1:abcd (format unchanged)", got)
	}
	if got := t1.String(); got != "sha256/v1#1:abcd" {
		t.Errorf("String (TrackID 1) = %q, want sha256/v1#1:abcd", got)
	}
}
