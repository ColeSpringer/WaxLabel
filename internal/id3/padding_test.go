package id3

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
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

// A tag reused in place keeps its region size when a frame carries a grouping byte: the size the
// padding is cut to counts that byte, so the rendered tag neither grows nor shrinks.
func TestReuseInPlaceCountsTheGroupByte(t *testing.T) {
	for _, major := range []byte{3, 4} {
		group := byte(0x20)
		if major == 4 {
			group = 0x40
		}
		body := append(testfile.ID3FrameFlags(major, "TPE1", [2]byte{0, group}, []byte("\x07\x00Grouped")), make([]byte, 100)...)
		src := wrapTag(major, 0, body)
		tg, err := ParseTag(src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ft := RenderFrontTag(FrontTagEdit{SrcTag: tg, Front: tg, Version: major, Frames: tg.Frames(),
			Padding: core.DefaultPadding, SrcTagLen: int64(len(src))})
		if len(ft.Bytes) != len(src) || ft.Padding != 100 {
			t.Errorf("v2.%d: rendered %d bytes with %d of padding, want the %d-byte region with 100", major, len(ft.Bytes), ft.Padding, len(src))
		}
	}
}

// With bytes between the front tags and the first frame, a tag left without frames is still
// written, padded as the policy sizes any tag; the operation is named only when the tag held
// something.
func TestRenderFrontTagKeepsAnEmptyTagAheadOfAGap(t *testing.T) {
	held, err := ParseTag(wrapTag(4, 0, rawFrame(4, "TIT2", []byte("\x00T"))), 0)
	if err != nil {
		t.Fatal(err)
	}
	none, emptied := core.PaddingPolicy{}, []string{"ID3v2 emptied (kept: 3 byte(s) precede the first frame)"}
	for _, c := range []struct {
		name   string
		front  *Tag
		gap    int64
		policy core.PaddingPolicy
		bytes  int
		ops    []string
	}{
		{"a tag that held a frame keeps its room", held, 3, core.DefaultPadding, 22, emptied},
		{"no padding asked for", held, 3, none, 10, emptied},
		{"padding asked for", held, 3, core.PaddingPolicy{Target: 100, Min: 100, ReuseInPlace: true}, 110, emptied},
		{"a tag already empty", NewEmpty(4), 3, core.DefaultPadding, 22, nil},
		{"no gap", held, 0, core.DefaultPadding, 0, []string{"ID3v2 removal"}},
		{"no tag", nil, 3, core.DefaultPadding, 0, nil},
	} {
		ft := RenderFrontTag(FrontTagEdit{SrcTag: NewEmpty(4), Front: c.front, Version: 4, Padding: c.policy,
			SrcTagLen: 22, Gap: c.gap, TagsChanged: true})
		if len(ft.Bytes) != c.bytes || !slices.Equal(ft.Operations, c.ops) || (ft.Tag != nil) != (c.bytes > 0) || ft.Padding != int64(max(c.bytes-10, 0)) {
			t.Errorf("%s: %d byte(s), padding %d, operations %v; want %d and %v", c.name, len(ft.Bytes), ft.Padding, ft.Operations, c.bytes, c.ops)
		}
	}
}
