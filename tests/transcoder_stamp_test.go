package waxlabel_test

import (
	"bytes"
	"context"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestLavcStampReported: the ffmpeg codec stamp names the actual codec rather than only the
// muxer, but it still describes the transcode that produced the file, so it is an inherited
// encoder wherever a muxer stamp already was. This covers the read/report side across the
// formats that carry ENCODER differently: a Vorbis comment (FLAC), an ID3 TSSE (MP3), an MP4
// "\xa9too" atom, and a WAV ISFT item.
func TestLavcStampReported(t *testing.T) {
	const stamp = "Lavc61.19.101 libopus"
	for _, fixture := range []string{sampleFLAC, sampleMP3, sampleMP4, sampleWAV} {
		t.Run(fixture, func(t *testing.T) {
			path := copyToTemp(t, fixture)
			doc := mustParseFile(t, path)
			plan, err := doc.Edit().Set(tag.Encoder, stamp).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
				t.Fatal(err)
			}
			re := mustParseFile(t, path)
			found := false
			for _, f := range re.Lint() {
				if f.Code == "inherited-encoder" {
					found = true
				}
			}
			if !found {
				t.Errorf("a %q ENCODER must report inherited-encoder; findings = %+v", stamp, re.Lint())
			}
		})
	}
}

// TestLavcStampStripped is the write side of the widening: --strip-encoder /
// WithStripEncoderStamp judges the WAV ISFT item on its own bytes, so a codec stamp there is
// now dropped like a muxer stamp already was. (The option never touches a canonical ENCODER
// value; lint --fix does that, and TestLintFixClearsMatroskaStampPair covers it.)
func TestLavcStampStripped(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Keep"}, [2]string{"ISFT", "Lavc61.19.101 libopus"}), wavData(400))
	doc := mustParseBytes(t, data)
	if v, _ := doc.Tags().Get(tag.Encoder); len(v) != 1 || v[0] != "Lavc61.19.101 libopus" {
		t.Fatalf("setup: ENCODER = %v, want the ISFT stamp", v)
	}
	plan, err := doc.Edit().Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("stripping a stamped ISFT is not a no-op")
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("Lavc")) {
		t.Error("the codec stamp survived --strip-encoder")
	}
	re := mustParseBytes(t, out)
	if re.Tags().Has(tag.Encoder) {
		t.Errorf("ENCODER = %+v, want the stamped ISFT gone", re.Tags())
	}
	if got := re.Fields().Title; got != "Keep" {
		t.Errorf("TITLE = %q, want the untouched Keep", got)
	}
}
