package mpeg4audio

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// adtsFrame is one frame located inside an ADTS stream: its raw data block and the header
// fields the block is parsed under.
type adtsFrame struct {
	block []byte
	cfg   FrameConfig
}

// walkADTS splits an ADTS stream into its frames. It decodes only the seven fixed header
// bytes it needs, so the test does not depend on the aac package (which depends on this one).
// A frame carrying more than one raw data block, or a profile other than AAC LC, is skipped:
// the parser under test does not claim those.
func walkADTS(t *testing.T, data []byte) []adtsFrame {
	t.Helper()
	return walkADTSBytes(data)
}

// walkADTSBytes is walkADTS without a testing.T, so the fuzz seeder shares it. A stream it
// cannot follow yields the frames it read before losing sync.
func walkADTSBytes(data []byte) []adtsFrame {
	var out []adtsFrame
	off := 0
	for off+7 <= len(data) && !(data[off] == 0xFF && data[off+1]&0xF0 == 0xF0 && data[off+1]&0x06 == 0) {
		off++ // a fixture may carry a front ID3 tag before the first frame
	}
	for off+7 <= len(data) {
		b := data[off:]
		if b[0] != 0xFF || b[1]&0xF0 != 0xF0 || b[1]&0x06 != 0 {
			break
		}
		profile := int(b[2] >> 6)
		sfIndex := int(b[2] >> 2 & 0x0F)
		chanConfig := int(b[2]&0x01)<<2 | int(b[3]>>6)
		frameLength := int(b[3]&0x03)<<11 | int(b[4])<<3 | int(b[5]>>5)
		rawBlocks := int(b[6] & 0x03)
		if frameLength < 7 || off+frameLength > len(data) {
			break
		}
		hdrLen := 7
		if b[1]&0x01 == 0 { // protection_absent clear: a two-byte CRC follows the header
			hdrLen = 9
		}
		if rawBlocks == 0 && profile+1 == 2 && hdrLen <= frameLength {
			out = append(out, adtsFrame{
				block: b[hdrLen:frameLength],
				cfg:   FrameConfig{ObjectType: profile + 1, SampleRateIdx: sfIndex, ChannelConfig: chanConfig},
			})
		}
		off += frameLength
	}
	return out
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := readFixtureFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readFixtureFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("..", "..", "testdata", name))
}

// TestParseRawDataBlockFixtures walks every frame of the checked-in AAC streams. A parse
// failure means the syntax walk or a codebook is wrong: the block must end exactly on its
// END element, so a misread anywhere lands the reader somewhere else.
func TestParseRawDataBlockFixtures(t *testing.T) {
	cases := []struct {
		file       string
		wantSBR    bool
		wantSCESBR bool // the SBR payload of a single channel element is captured
	}{
		{"heaac_v1.aac", true, false}, // stereo core: a channel pair element
		{"heaac_v2.aac", true, true},  // mono core plus parametric stereo
		{"notags.aac", false, false},
		{"sample.aac", false, false},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			frames := walkADTS(t, readFixture(t, c.file))
			if len(frames) == 0 {
				t.Fatal("no AAC LC frames found")
			}
			for i, f := range frames {
				info, err := ParseRawDataBlock(f.block, f.cfg)
				if err != nil {
					t.Fatalf("frame %d of %d: %v", i, len(frames), err)
				}
				if info.SBR != c.wantSBR {
					t.Fatalf("frame %d: SBR = %v, want %v", i, info.SBR, c.wantSBR)
				}
				if (info.SBRPayload != nil) != c.wantSCESBR {
					t.Fatalf("frame %d: SBR payload present = %v, want %v", i, info.SBRPayload != nil, c.wantSCESBR)
				}
			}
		})
	}
}

// TestParseRawDataBlockRejectsTruncated: every prefix of a valid block fails rather than
// panicking or reporting a block it did not read to the end.
func TestParseRawDataBlockRejectsTruncated(t *testing.T) {
	frames := walkADTS(t, readFixture(t, "notags.aac"))
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	f := frames[0]
	for n := range len(f.block) {
		if _, err := ParseRawDataBlock(f.block[:n], f.cfg); err == nil {
			t.Errorf("prefix of %d bytes parsed as a whole block", n)
		}
	}
}

// TestParseRawDataBlockRejectsOtherObjectTypes: only AAC LC is walked, and anything else
// abstains rather than guessing.
func TestParseRawDataBlockRejectsOtherObjectTypes(t *testing.T) {
	frames := walkADTS(t, readFixture(t, "notags.aac"))
	cfg := frames[0].cfg
	cfg.ObjectType = 1
	if _, err := ParseRawDataBlock(frames[0].block, cfg); !errors.Is(err, ErrUnsupported) {
		t.Errorf("AAC Main should be unsupported, got %v", err)
	}
}

// TestParseRawDataBlockLCCorpus generates AAC LC streams across the sampling rates, channel
// counts and bitrates a real encoder produces, so the walk meets every codebook, both window
// sequences, TNS and pulse data rather than only what the fixtures happen to contain.
func TestParseRawDataBlockLCCorpus(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	for _, rate := range []int{8000, 16000, 22050, 24000, 32000, 44100, 48000} {
		for _, channels := range []int{1, 2} {
			for _, bitrate := range []string{"24k", "64k", "160k"} {
				name := fmt.Sprintf("%d_%dch_%s", rate, channels, bitrate)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					out := filepath.Join(dir, name+".aac")
					cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y",
						"-f", "lavfi", "-i", fmt.Sprintf("anoisesrc=d=3:c=pink:r=%d:a=0.4", rate),
						"-ac", fmt.Sprint(channels), "-c:a", "aac", "-b:a", bitrate, out)
					// ffmpeg is installed (checked above), so a failure here is a real one:
					// skipping would quietly drop a rate or bitrate from the corpus.
					if err := cmd.Run(); err != nil {
						t.Fatalf("ffmpeg could not encode %s: %v", name, err)
					}
					data, err := os.ReadFile(out)
					if err != nil {
						t.Fatalf("no output for %s: %v", name, err)
					}
					frames := walkADTS(t, data)
					if len(frames) == 0 {
						t.Fatalf("%s encoded no AAC LC frames", name)
					}
					for i, f := range frames {
						info, err := ParseRawDataBlock(f.block, f.cfg)
						if err != nil {
							t.Fatalf("frame %d of %d: %v", i, len(frames), err)
						}
						if info.SBR {
							t.Fatalf("frame %d reports SBR in a plain AAC LC stream", i)
						}
					}
				})
			}
		}
	}
}
