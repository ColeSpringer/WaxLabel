package core

import "testing"

// TestPictureLossNonCoverRoleAndDescription: APE keeps front/back roles; other roles and descriptions are lossy.
func TestPictureLossNonCoverRoleAndDescription(t *testing.T) {
	loss := PictureLossNonCoverRoleAndDescription
	for _, c := range []struct {
		name string
		p    Picture
		want bool
	}{
		{"plain front carries", Picture{Type: PicFrontCover}, false},
		{"plain back carries", Picture{Type: PicBackCover}, false},
		{"described front is lossy", Picture{Type: PicFrontCover, Description: "d"}, true},
		{"described back is lossy", Picture{Type: PicBackCover, Description: "d"}, true},
		{"artist is lossy", Picture{Type: PicArtist}, true},
		{"other is lossy", Picture{Type: PicOther}, true},
	} {
		if got := PicturesLoseMetadata([]Picture{c.p}, loss); got != c.want {
			t.Errorf("%s: PicturesLoseMetadata = %v, want %v", c.name, got, c.want)
		}
	}
}
