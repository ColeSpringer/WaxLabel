package core

import "testing"

// TestPictureLossDescriptionOnly: APE keeps every role the ID3 table names; a description is lost,
// and a role outside the table.
func TestPictureLossDescriptionOnly(t *testing.T) {
	loss := PictureLossDescriptionOnly
	for _, c := range []struct {
		name string
		p    Picture
		want bool
	}{
		{"plain front carries", Picture{Type: PicFrontCover}, false},
		{"plain back carries", Picture{Type: PicBackCover}, false},
		{"artist carries", Picture{Type: PicArtist}, false},
		{"other carries", Picture{Type: PicOther}, false},
		{"publisher logo carries", Picture{Type: PicPublisherLogo}, false},
		{"described front is lossy", Picture{Type: PicFrontCover, Description: "d"}, true},
		{"described artist is lossy", Picture{Type: PicArtist, Description: "d"}, true},
		{"unnamed role is lossy", Picture{Type: PicPublisherLogo + 1}, true},
	} {
		if got := PicturesLoseMetadata([]Picture{c.p}, loss); got != c.want {
			t.Errorf("%s: PicturesLoseMetadata = %v, want %v", c.name, got, c.want)
		}
	}
}
