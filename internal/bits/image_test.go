package bits

import "testing"

func TestSniffImage(t *testing.T) {
	// 1x1 RGBA PNG header (IHDR only; pixel data omitted, not needed).
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, // width 1
		0x00, 0x00, 0x00, 0x01, // height 1
		0x08, 0x06, 0x00, 0x00, 0x00, // bitdepth 8, colortype 6 (RGBA)
	}
	// 3x5 GIF89a, GCT depth 8.
	gif := append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0x77, 0x00, 0x00)
	// 3x5 baseline JPEG: SOI then SOF0 (precision 8, 3 components).
	jpeg := []byte{
		0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08,
		0x00, 0x05, // height 5
		0x00, 0x03, // width 3
		0x03, // components
		0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}
	// Same JPEG but with 0xFF fill bytes before the SOF marker, which the
	// sniffer must skip rather than mistake for a marker.
	jpegFill := []byte{
		0xFF, 0xD8, 0xFF, 0xFF, 0xFF, 0xC0, 0x00, 0x11, 0x08,
		0x00, 0x05, 0x00, 0x03, 0x03,
		0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}

	cases := []struct {
		name string
		data []byte
		want ImageInfo
	}{
		{"png", png, ImageInfo{MIME: "image/png", Width: 1, Height: 1, Depth: 32}},
		{"gif", gif, ImageInfo{MIME: "image/gif", Width: 3, Height: 5, Depth: 8}},
		{"jpeg", jpeg, ImageInfo{MIME: "image/jpeg", Width: 3, Height: 5, Depth: 24}},
		{"jpeg-fill", jpegFill, ImageInfo{MIME: "image/jpeg", Width: 3, Height: 5, Depth: 24}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SniffImage(tc.data)
			if !ok {
				t.Fatalf("SniffImage(%s) not recognized", tc.name)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}

	if _, ok := SniffImage([]byte("not an image")); ok {
		t.Error("expected unrecognized data to return ok=false")
	}
}
