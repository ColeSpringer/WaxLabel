package id3

import "testing"

// wrapTag wraps a frame region in an ID3v2 header of the given version and flags.
func wrapTag(major, flags byte, body []byte) []byte {
	out := append([]byte("ID3"), major, 0, flags)
	n := len(body)
	out = append(out, byte((n>>21)&0x7f), byte((n>>14)&0x7f), byte((n>>7)&0x7f), byte(n&0x7f))
	return append(out, body...)
}

// rawFrame renders one frame header of the given version's geometry: v2.2 uses a
// 3-character id and a 3-byte size, v2.3 a 4-character id and a plain 4-byte size, v2.4
// the same with a sync-safe size. The widths are the whole point of the test below.
func rawFrame(major byte, id string, body []byte) []byte {
	n := len(body)
	switch major {
	case 2:
		return append(append([]byte(id), byte(n>>16), byte(n>>8), byte(n)), body...)
	case 4:
		hdr := append([]byte(id), byte((n>>21)&0x7f), byte((n>>14)&0x7f), byte((n>>7)&0x7f), byte(n&0x7f), 0, 0)
		return append(hdr, body...)
	default:
		hdr := append([]byte(id), byte(n>>24), byte(n>>16), byte(n>>8), byte(n), 0, 0)
		return append(hdr, body...)
	}
}

// TestTagPaddingMeasuresTheSourceRegion: padding is measured off the parsed region, not
// derived by re-rendering the frames. Deriving it goes wrong wherever the on-disk shape
// differs from what the writer emits - a v2.2 tag's 6-byte frame headers most of all,
// since RenderedSize assumes the 10-byte v2.3/v2.4 header and would over-count the frames
// by 4 bytes each. Document.Padding reports this number, so a derived one would put a
// figure in dump --json that is nowhere in the file.
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

// TestRenderFrontTagRestampsPadding: the tag a rewrite produces must carry the padding the
// rewrite sized, not the source's. Both feed Document.Padding - the source tag when a file
// is read, the rebuilt one when a plan's result is inspected - so inheriting the source's
// number would make a post-write document describe a region that no longer exists.
func TestRenderFrontTagRestampsPadding(t *testing.T) {
	src, err := ParseTag(wrapTag(3, 0, append(rawFrame(3, "TIT2", []byte("\x00Old")), make([]byte, 500)...)), 0)
	if err != nil {
		t.Fatalf("ParseTag: %v", err)
	}
	if src.Padding() != 500 {
		t.Fatalf("setup: source padding = %d, want 500", src.Padding())
	}
	rebuilt := src.WithFrames(src.Frames(), 64)
	if got := rebuilt.Padding(); got != 64 {
		t.Errorf("rebuilt padding = %d, want the 64 the rewrite sized", got)
	}
	if got := src.Padding(); got != 500 {
		t.Errorf("WithFrames mutated the source tag's padding: %d", got)
	}
}
