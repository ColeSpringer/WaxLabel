package waxlabel_test

import (
	"os"
	"path/filepath"
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
