package waxlabel_test

import (
	"context"
	"errors"
	"os"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// TrackID participates in equality while the zero value keeps the original digest string format.
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

// TestHashRefusesAReplacedFile: a document's digests describe the file it parsed; a different file
// now at the path is refused rather than hashed with the parsed file's layout.
func TestHashRefusesAReplacedFile(t *testing.T) {
	ctx := context.Background()
	other := readFixture(t, "../testdata/notags.flac")
	for name, hash := range map[string]func(*wl.Document) error{
		"HashAudioEssence": func(d *wl.Document) error { _, err := d.HashAudioEssence(ctx); return err },
		"HashFile":         func(d *wl.Document) error { _, err := d.HashFile(ctx); return err },
	} {
		t.Run(name, func(t *testing.T) {
			path := copyToTemp(t, sampleFLAC)
			doc := mustParseFile(t, path)
			next := path + ".next"
			if err := os.WriteFile(next, other, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(next, path); err != nil {
				t.Fatal(err)
			}
			if err := hash(doc); !errors.Is(err, waxerr.ErrSourceChanged) {
				t.Errorf("%s after the file was replaced: err = %v, want ErrSourceChanged", name, err)
			}
		})
	}
}
