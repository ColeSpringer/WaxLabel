package waxlabel_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	_ "github.com/colespringer/waxlabel" // register the codecs (populates core's registry)
	"github.com/colespringer/waxlabel/internal/core"
)

// TestContentDetectionCoversFixtures checks that every valid testdata fixture is
// recognized from leading bytes alone. A failure names the fixture that would become
// unsupported under content-only detection. ADTS/AAC and ID3-less MP3 still carry
// leading signatures: an ADTS sync or an MPEG/ID3 header. The codecs are registered
// transitively through the waxlabel import above.
func TestContentDetectionCoversFixtures(t *testing.T) {
	dir := "../testdata"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		seen++
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			header := make([]byte, 64)
			n, _ := f.Read(header)
			if _, ok := core.Detect("", header[:n]); !ok {
				t.Errorf("content-only Detect failed: removing the extension fallback regresses %s to unsupported (exit 3)", name)
			}
		})
	}
	if seen == 0 {
		t.Fatal("no fixtures found; content detection was not exercised")
	}
}

// TestSkipsLeadingID3Set checks the formats whose parsers read past a leading ID3v2 tag:
// MP3, FLAC, and raw AAC. Other inner signatures past ID3 should be reported unsupported.
func TestSkipsLeadingID3Set(t *testing.T) {
	want := map[core.Format]bool{
		core.FormatMP3: true, core.FormatFLAC: true, core.FormatAAC: true,
		core.FormatMP4: false, core.FormatMatroska: false, core.FormatWAV: false,
		core.FormatAIFF: false, core.FormatOggVorbis: false, core.FormatOggOpus: false,
	}
	for f, w := range want {
		c, ok := core.ForFormat(f)
		if !ok {
			t.Fatalf("no codec registered for %s", f)
		}
		if got := c.SkipsLeadingID3(); got != w {
			t.Errorf("%s.SkipsLeadingID3() = %v, want %v", f, got, w)
		}
	}
}

// TestDetectsMP4BehindLeadingFreeBox: mp4.Sniff tested "ftyp" at a fixed offset, so a file
// whose writer emitted a free/skip/wide box first was unsupported (exit 3) even though the
// parser handles it - walkAtoms is generic over top-level atoms and only moov is required.
// The bound is the 64-byte detection window: an ftyp past it stays unidentified.
func TestDetectsMP4BehindLeadingFreeBox(t *testing.T) {
	ftyp := []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00M4A mp42isom")
	box := func(name string, n int) []byte {
		h := []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
		return append(append(h, name...), make([]byte, n-8)...)
	}
	header := func(b []byte) []byte {
		h := make([]byte, 64)
		return h[:copy(h, b)]
	}
	for _, tc := range []struct {
		name   string
		header []byte
		want   bool
	}{
		{"bare ftyp", header(ftyp), true},
		{"free then ftyp", header(append(box("free", 8), ftyp...)), true},
		{"skip then ftyp", header(append(box("skip", 16), ftyp...)), true},
		{"wide then ftyp", header(append(box("wide", 8), ftyp...)), true},
		{"two boxes then ftyp", header(slices.Concat(box("free", 8), box("skip", 8), ftyp)), true},
		// A leading box that does not fit inside the detection window hides the ftyp
		// behind it, and an unrecognized leading box is not stepped over at all.
		{"free past the window", header(append(box("free", 128), ftyp...)), false},
		{"unknown leading box", header(append(box("junk", 8), ftyp...)), false},
		// A size below 8 is the "extends to EOF" / "64-bit size follows" form; neither
		// names a next box inside the window.
		{"free with a to-EOF size", header(append([]byte("\x00\x00\x00\x00free"), ftyp...)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := core.Detect("", tc.header)
			if ok != tc.want {
				t.Fatalf("Detect ok = %v, want %v", ok, tc.want)
			}
			if ok && c.Format() != core.FormatMP4 {
				t.Errorf("format = %v, want MP4", c.Format())
			}
		})
	}
}
