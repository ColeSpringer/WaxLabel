package id3

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// wrapTag wraps a frame region in an ID3v2 header of the given version and flags.
func wrapTag(major, flags byte, body []byte) []byte {
	return slices.Concat([]byte("ID3"), []byte{major, 0, flags}, testfile.Syncsafe(len(body)), body)
}

// rawFrame renders one frame header of the given version's geometry: v2.2 uses a
// 3-character id and a 3-byte size, v2.3 a 4-character id and a plain 4-byte size, v2.4
// the same with a sync-safe size.
func rawFrame(major byte, id string, body []byte) []byte {
	if major == 2 {
		n := len(body)
		return append(append([]byte(id), byte(n>>16), byte(n>>8), byte(n)), body...)
	}
	return testfile.ID3Frame(major, id, body)
}

// TestTagPaddingMeasuresTheSourceRegion checks padding is measured off the parsed region,
// not derived by re-rendering: a v2.2 tag's 6-byte frame headers would make RenderedSize
// over-count by 4 bytes per frame, and Document.Padding reports this number.
func TestTagPaddingMeasuresTheSourceRegion(t *testing.T) {
	const pad = 200
	cases := []struct {
		name  string
		major byte
		id    string
	}{
		{"v2.2 six-byte frame headers", 2, "TT2"},
		{"v2.3", 3, "TIT2"},
		{"v2.4", 4, "TIT2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := append(rawFrame(c.major, c.id, []byte("\x00Title")), make([]byte, pad)...)
			tg, err := ParseTag(wrapTag(c.major, 0, body), 0)
			if err != nil {
				t.Fatalf("ParseTag: %v", err)
			}
			if len(tg.Frames()) != 1 {
				t.Fatalf("frames = %d, want 1", len(tg.Frames()))
			}
			if got := tg.Padding(); got != pad {
				t.Errorf("Padding() = %d, want %d", got, pad)
			}
			if got := FrontTagPadding(tg); got != pad {
				t.Errorf("FrontTagPadding = %d, want %d", got, pad)
			}
		})
	}

	t.Run("footer excluded", func(t *testing.T) {
		// A v2.4 footer trails the frame region and is not covered by the declared size,
		// so counting its bytes as padding would overstate the slack by 10.
		body := append(rawFrame(4, "TIT2", []byte("\x00Title")), make([]byte, pad)...)
		data := wrapTag(4, hdrFooter, body)
		data = append(data, "3DI"...)
		data = append(data, 4, 0, 0, 0, 0, 0, 0)
		tg, err := ParseTag(data, 0)
		if err != nil {
			t.Fatalf("ParseTag: %v", err)
		}
		if got := tg.Padding(); got != pad {
			t.Errorf("Padding() = %d, want %d", got, pad)
		}
	})

	t.Run("no tag", func(t *testing.T) {
		if got := FrontTagPadding(nil); got != 0 {
			t.Errorf("FrontTagPadding(nil) = %d, want 0", got)
		}
	})
}
