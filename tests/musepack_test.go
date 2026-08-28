package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Musepack synthesis. ffmpeg has no Musepack encoder, so both stream versions are
// built here rather than shipped as binary fixtures; ffprobe's mpc7 and mpc8
// decoders still read them back, which is what makes the differential test possible.

// mpcVarlen encodes Musepack's variable-length number: seven bits per byte,
// big-endian, high bit set on every byte but the last.
func mpcVarlen(n uint64) []byte {
	if n == 0 {
		return []byte{0}
	}
	var groups []byte
	for n > 0 {
		groups = append(groups, byte(n&0x7F))
		n >>= 7
	}
	slices.Reverse(groups)
	out := make([]byte, len(groups))
	for i, g := range groups {
		out[i] = g
		if i < len(groups)-1 {
			out[i] |= 0x80
		}
	}
	return out
}

// mpcPacket frames an SV8 packet. Its declared size covers the key and the size
// field as well as the payload, so the length is solved for rather than computed.
func mpcPacket(key string, payload []byte) []byte {
	total := 2 + 1 + len(payload)
	for {
		s := mpcVarlen(uint64(total))
		if 2+len(s)+len(payload) == total {
			return append(append([]byte(key), s...), payload...)
		}
		total = 2 + len(s) + len(payload)
	}
}

// mpcSV8 builds an SV8 file: the MPCK magic, an SH stream header, a stub audio
// packet, and a stream-end packet.
func mpcSV8(samples, silence uint64, rateIndex, channels int) []byte {
	sh := []byte{0, 0, 0, 0, 8} // CRC, then stream version
	sh = append(sh, mpcVarlen(samples)...)
	sh = append(sh, mpcVarlen(silence)...)
	sh = append(sh, byte(rateIndex<<5)|20)       // rate index + max used bands
	sh = append(sh, byte((channels-1)<<4), 0x00) // channels, mid/side, block frames
	out := append([]byte("MPCK"), mpcPacket("SH", sh)...)
	out = append(out, mpcPacket("AP", make([]byte, 64))...)
	return append(out, mpcPacket("SE", nil)...)
}

// mpcSV7 builds an SV7 file: the fixed 24-byte header plus stub frames.
func mpcSV7(frames uint32, rateIndex int) []byte {
	b := make([]byte, 24)
	copy(b[0:3], "MP+")
	b[3] = 0x07
	binary.LittleEndian.PutUint32(b[4:8], frames)
	b[10] = byte(rateIndex)
	return append(b, make([]byte, 512)...)
}

func TestMusepackParseBothVersions(t *testing.T) {
	for _, c := range []struct {
		name    string
		data    []byte
		rate    int
		chans   int
		samples uint64
		profile string
	}{
		{"SV8", mpcSV8(44100, 0, 0, 2), 44100, 2, 44100, "Musepack SV8"},
		{"SV8 mono 48k", mpcSV8(24000, 0, 1, 1), 48000, 1, 24000, "Musepack SV8"},
		{"SV8 minus beginning silence", mpcSV8(44100, 100, 0, 2), 44100, 2, 44000, "Musepack SV8"},
		// SV7 stores a frame count, not a sample count; every decoder reports whole frames.
		{"SV7", mpcSV7(100, 0), 44100, 2, 100 * 1152, "Musepack SV7"},
		{"SV7 37800 Hz", mpcSV7(50, 2), 37800, 2, 50 * 1152, "Musepack SV7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, c.data)
			if doc.Format() != wl.FormatMusepack {
				t.Fatalf("format = %v, want Musepack", doc.Format())
			}
			tr := doc.Properties().First()
			if tr.SampleRate != c.rate || tr.Channels != c.chans || tr.TotalSamples != c.samples {
				t.Errorf("track = %+v, want %d Hz, %d ch, %d samples", tr, c.rate, c.chans, c.samples)
			}
			// The stream version is the profile detail; the canonical name is shared.
			if tr.Codec != "Musepack" || tr.CodecProfile != c.profile {
				t.Errorf("codec = %q profile = %q, want Musepack and %q", tr.Codec, tr.CodecProfile, c.profile)
			}
			if tr.Duration <= 0 {
				t.Errorf("duration = %v, want > 0", tr.Duration)
			}
		})
	}
}

func TestMusepackRoundTrip(t *testing.T) {
	for _, src := range [][]byte{mpcSV8(44100, 0, 0, 2), mpcSV7(100, 0)} {
		before := essenceOf(t, src)
		plan, err := mustParseBytes(t, src).Edit().
			Set(tag.Title, "Musepack Title").
			Set(tag.Artist, "First", "Second").
			Prepare()
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		out := applyToBytes(t, src, plan)
		if after := essenceOf(t, out); !before.Equal(after) {
			t.Error("audio essence changed across a tag edit")
		}
		got := mustParseBytes(t, out)
		if got.Fields().Title != "Musepack Title" {
			t.Errorf("title = %q", got.Fields().Title)
		}
		if a := got.Fields().Artists; !slices.Equal(a, []string{"First", "Second"}) {
			t.Errorf("artists = %v", a)
		}

		// And back out: clearing every key drops the tag rather than leaving an empty one.
		ed := got.Edit()
		for _, k := range got.Tags().Keys() {
			ed = ed.Clear(k)
		}
		plan2, err := ed.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out2 := applyToBytes(t, out, plan2)
		if !bytes.Equal(out2, src) {
			t.Error("clearing every key should restore the original bytes")
		}
	}
}

// TestMusepackLeadingID3Preserved covers the stray front tag some SV7 encoders
// wrote: it routes through detection, is preserved verbatim, stays legacy (never
// authoritative), and a strip removes it.
func TestMusepackLeadingID3Preserved(t *testing.T) {
	front := id3v2(4, textFrame(4, "TIT2", "Legacy Title"))
	src := append(slices.Clone(front), mpcSV7(100, 0)...)

	doc := mustParseBytes(t, src)
	if doc.Format() != wl.FormatMusepack {
		t.Fatalf("format = %v, want Musepack found past the leading ID3v2", doc.Format())
	}
	if !hasWarning(doc, wl.WarnStrayLeadingID3) {
		t.Error("expected the stray-leading-ID3 warning")
	}
	if _, ok := doc.Get(tag.Title); ok {
		t.Error("the leading ID3v2 must not be promoted into the canonical set")
	}
	legacy := false
	for _, f := range doc.Families() {
		if f.Key == tag.Title && f.Legacy {
			legacy = true
		}
	}
	if !legacy {
		t.Error("the leading ID3v2 title should surface as a legacy family value")
	}

	plan, err := doc.Edit().Set(tag.Album, "Native").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.HasPrefix(out, front) {
		t.Error("the leading ID3v2 was not preserved through an edit")
	}

	plan2, err := mustParseBytes(t, out).Edit().Set(tag.Album, "Stripped").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, out, plan2)
	if bytes.HasPrefix(out2, front) {
		t.Error("--legacy strip should have dropped the leading ID3v2")
	}
	re := mustParseBytes(t, out2)
	if re.Fields().Album != "Stripped" {
		t.Errorf("album after strip = %q", re.Fields().Album)
	}
	if hasWarning(re, wl.WarnStrayLeadingID3) {
		t.Error("the stray-leading-ID3 warning should be gone after the strip")
	}
}

func TestMusepackCoverRoundTrip(t *testing.T) {
	src := mpcSV8(44100, 0, 0, 2)
	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	pics := mustParseBytes(t, out).Pictures()
	if len(pics) != 1 || !bytes.Equal(pics[0].Data, tinyPNG()) {
		t.Fatalf("pictures = %+v, want one front cover with the original bytes", pics)
	}
}

func TestMusepackMalformedRejected(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"SV7 truncated header", []byte("MP+\x07\x01\x00")},
		{"SV8 with no SH packet", append([]byte("MPCK"), mpcPacket("AP", make([]byte, 8))...)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := wl.Parse(context.Background(), wl.BytesSource(c.data)); !errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("err = %v, want ErrInvalidData", err)
			}
		})
	}
}

// TestMusepackUnsupportedSV7VersionRefused: a version byte outside the two SV7
// spellings is refused by name rather than misread.
func TestMusepackUnsupportedSV7VersionRefused(t *testing.T) {
	data := mpcSV7(100, 0)
	data[3] = 0x05
	// Sniff rejects the unknown version too, so this reads as an unidentifiable file
	// rather than a Musepack one; either way it must not be misparsed as SV7.
	if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); err == nil {
		t.Error("an unknown SV7 stream version should not parse")
	}
}

// TestMusepackDifferentialFFprobeReadsOurTags is the independent read-back proof.
// ffmpeg cannot encode Musepack, but its mpc7 and mpc8 decoders read both the stream
// and the APEv2 trailer, so ffprobe is still the oracle.
func TestMusepackDifferentialFFprobeReadsOurTags(t *testing.T) {
	requireTool(t, "ffprobe")
	for _, c := range []struct {
		name   string
		demux  string
		data   []byte
		expect time.Duration
	}{
		{"sv7", "mpc", mpcSV7(100, 0), 0},
		{"sv8", "mpc8", mpcSV8(44100, 0, 0, 2), 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := writeTempFile(t, "x.mpc", c.data)
			plan, err := mustParseFile(t, path).Edit().
				Set(tag.Title, "Differential Title").
				Set(tag.Key("CUSTOM_TAG"), "custom-value").
				Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
				t.Fatal(err)
			}
			// The demuxer is named explicitly: ffmpeg's probe scores the SV7 demuxer above
			// the SV8 one for a .mpc name, so an SV8 file needs to be routed by hand.
			out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
				"-f", c.demux, "-show_entries", "format_tags", "-of", "json", path).Output()
			if err != nil {
				t.Fatalf("ffprobe: %v", err)
			}
			var probe struct {
				Format struct {
					Tags map[string]string `json:"tags"`
				} `json:"format"`
			}
			if err := json.Unmarshal(out, &probe); err != nil {
				t.Fatalf("parse ffprobe json: %v\n%s", err, out)
			}
			for k, want := range map[string]string{"title": "Differential Title", "CUSTOM_TAG": "custom-value"} {
				if got := lookupCI(probe.Format.Tags, k); got != want {
					t.Errorf("ffprobe tag %q = %q, want %q (all: %v)", k, got, want, probe.Format.Tags)
				}
			}
		})
	}
}
