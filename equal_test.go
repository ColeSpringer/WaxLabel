package waxlabel_test

import (
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

func TestEqualPictures(t *testing.T) {
	t.Parallel()
	base := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Description: "cover", Width: 10, Height: 10, Data: []byte{1, 2, 3}}
	clone := base

	cases := []struct {
		name string
		a, b []wl.Picture
		want bool
	}{
		{"both nil", nil, nil, true},
		{"identical", []wl.Picture{base}, []wl.Picture{clone}, true},
		{"length differs", []wl.Picture{base}, nil, false},
		{"data differs", []wl.Picture{base}, []wl.Picture{{Type: base.Type, MIME: base.MIME, Description: base.Description, Width: 10, Height: 10, Data: []byte{9}}}, false},
		{"type differs", []wl.Picture{base}, []wl.Picture{{Type: wl.PicBackCover, MIME: base.MIME, Description: base.Description, Width: 10, Height: 10, Data: base.Data}}, false},
		// Dimensions are part of identity — the precision the CLI-local diff lacked.
		{"width differs", []wl.Picture{base}, []wl.Picture{{Type: base.Type, MIME: base.MIME, Description: base.Description, Width: 11, Height: 10, Data: base.Data}}, false},
	}
	for _, tc := range cases {
		if got := wl.EqualPictures(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: EqualPictures = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEqualChapters(t *testing.T) {
	t.Parallel()
	base := wl.Chapter{Start: time.Second, End: 2 * time.Second, Title: "Intro"}

	cases := []struct {
		name string
		a, b []wl.Chapter
		want bool
	}{
		{"both nil", nil, nil, true},
		{"identical", []wl.Chapter{base}, []wl.Chapter{{Start: time.Second, End: 2 * time.Second, Title: "Intro"}}, true},
		{"length differs", []wl.Chapter{base}, nil, false},
		{"start differs", []wl.Chapter{base}, []wl.Chapter{{Start: 0, End: 2 * time.Second, Title: "Intro"}}, false},
		{"end differs", []wl.Chapter{base}, []wl.Chapter{{Start: time.Second, End: 3 * time.Second, Title: "Intro"}}, false},
		{"title differs", []wl.Chapter{base}, []wl.Chapter{{Start: time.Second, End: 2 * time.Second, Title: "Outro"}}, false},
	}
	for _, tc := range cases {
		if got := wl.EqualChapters(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: EqualChapters = %v, want %v", tc.name, got, tc.want)
		}
	}
}
