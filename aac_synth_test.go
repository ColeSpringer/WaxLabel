package waxlabel_test

import (
	"context"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// adtsStream builds a synthetic raw-AAC (ADTS) stream: `frames` AAC-LC frames at
// 44.1 kHz with the given channel configuration, each a 7-byte fixed header (no
// CRC) plus payloadPerFrame zero payload bytes. It is a real, detectable ADTS
// stream - enough to drive detection and the verbatim-copy write path without an
// encoder.
func adtsStream(chanConfig, frames, payloadPerFrame int) []byte {
	const hdr = 7 // ADTS fixed header without CRC
	frameLen := hdr + payloadPerFrame
	mk := func() []byte {
		b := make([]byte, frameLen)
		b[0] = 0xFF                                             // syncword high
		b[1] = 0xF1                                             // sync low | MPEG-4 | layer 00 | no CRC
		b[2] = 0x50 | byte((chanConfig>>2)&1)                   // profile=LC(1), sfIndex=4(44100), chan hi bit
		b[3] = byte((chanConfig&3)<<6) | byte((frameLen>>11)&3) // chan lo bits | frame_length hi
		b[4] = byte((frameLen >> 3) & 0xFF)                     // frame_length mid
		b[5] = byte((frameLen & 7) << 5)                        // frame_length lo | buffer fullness
		return b                                                // b[6] and payload stay zero
	}
	var out []byte
	for range frames {
		out = append(out, mk()...)
	}
	return out
}

// TestAACBareDetectAndCreateTags drives a bare synthetic ADTS stream through
// detection and the "create an ID3v2 tag where none existed" write path.
func TestAACBareDetectAndCreateTags(t *testing.T) {
	data := adtsStream(2, 50, 200) // stereo
	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatAAC {
		t.Fatalf("format = %v, want AAC", doc.Format())
	}
	if doc.Tags().Len() != 0 {
		t.Fatalf("bare ADTS should have no tags, got %d", doc.Tags().Len())
	}
	if tr := doc.Properties().First(); tr.Channels != 2 || tr.SampleRate != 44100 || tr.Codec != "AAC" || tr.CodecProfile != "AAC LC" {
		t.Errorf("track = %+v, want 2ch/44100/AAC (profile AAC LC)", tr)
	}

	plan, err := doc.Edit().Set(tag.Title, "From Scratch").Set(tag.Artist, "Synth").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("adding tags to a tagless file is not a no-op")
	}
	out := applyToBytes(t, data, plan)
	// A brand-new AAC tag is ID3v2.4 (the plan's default for raw AAC).
	if string(out[:3]) != "ID3" || out[3] != 4 {
		t.Errorf("new tag should be ID3v2.4, got %q ver %d", out[:3], out[3])
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "From Scratch" {
		t.Errorf("title = %q", re.Fields().Title)
	}
	if a := re.Fields().Artists; len(a) != 1 || a[0] != "Synth" {
		t.Errorf("artists = %v", a)
	}
}

// TestAACFrontID3Detection covers detectPastLeadingID3: a front ID3v2 tag is
// sniffed as MP3, but the ADTS stream just past it must reclaim the file as AAC.
func TestAACFrontID3Detection(t *testing.T) {
	// id3v2/textFrame are defined in mp3_synth_test.go (same _test package).
	data := append(id3v2(4, textFrame(4, "TIT2", "Tagged ADTS")), adtsStream(2, 20, 200)...)
	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatAAC {
		t.Fatalf("ID3-prefixed ADTS detected as %v, want AAC", doc.Format())
	}
	if doc.Fields().Title != "Tagged ADTS" {
		t.Errorf("title = %q", doc.Fields().Title)
	}
	// A tag edit preserves the ADTS bytes verbatim (essence stable).
	before := essenceOf(t, data)
	plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if after := essenceOf(t, out); !before.Equal(after) {
		t.Error("ADTS essence changed across a tag edit")
	}
}

// TestAACMP3MutualExclusivity confirms the layer-bit split: an ADTS stream is
// AAC (layer 00, which MP3 frame decoding rejects), and a real MPEG stream stays
// MP3 (never misread as AAC).
func TestAACMP3MutualExclusivity(t *testing.T) {
	if doc := mustParseBytes(t, adtsStream(2, 8, 200)); doc.Format() != wl.FormatAAC {
		t.Errorf("ADTS stream detected as %v, want AAC", doc.Format())
	}
	if doc := mustParseFile(t, notagsMP3); doc.Format() != wl.FormatMP3 {
		t.Errorf("MP3 detected as %v, want MP3", doc.Format())
	}
}

// TestAACExtensionDoesNotOverrideSniff locks the signature-only front-ID3 peek:
// a file named .aac that is a leading ID3 followed by non-ADTS bytes must NOT be
// reclassified to AAC by its extension alone - the sniffed leading ID3 (MP3)
// stands, since only a real signature behind the tag may override it. (The
// positive ID3+ADTS->AAC case is covered by TestAACFrontID3Detection, which uses a
// path-less source, so it already resolves by signature.)
func TestAACExtensionDoesNotOverrideSniff(t *testing.T) {
	data := append(id3v2(4, textFrame(4, "TIT2", "x")), 0, 1, 2, 3, 4, 5, 6, 7) // no ADTS sync
	path := writeTempFile(t, "garbage.aac", data)
	doc, err := wl.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Format() != wl.FormatMP3 {
		t.Errorf("ID3 + non-ADTS named .aac = %v, want MP3 (extension must not override the sniff)", doc.Format())
	}
	if doc.Fields().Title != "x" {
		t.Errorf("front ID3 should still be read: title = %q", doc.Fields().Title)
	}
}
