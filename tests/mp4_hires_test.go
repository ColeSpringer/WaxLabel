package waxlabel_test

import (
	"context"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// TestMP4ALACSampleRateFromCookie: a 96 kHz ALAC's rate does not fit the sample entry's
// 16.16 field, so the magic cookie carries it. The essence digest is unaffected: it salts
// with the raw entry values, so two files differing only in the cookie hash the same.
func TestMP4ALACSampleRateFromCookie(t *testing.T) {
	build := func(cookieRate int) []byte {
		return mp4AssembleStsd(mp4Stsd(mp4StsdEntry("alac", 2, 16, 0, mp4AlacCookie(cookieRate, 2, 24))), nil, nil, nil)
	}
	doc := mustParseBytes(t, build(96000))
	tr := doc.Properties().Tracks[0]
	if tr.SampleRate != 96000 || tr.Channels != 2 || tr.BitsPerSample != 24 {
		t.Errorf("track = %d Hz / %d ch / %d bit, want 96000/2/24", tr.SampleRate, tr.Channels, tr.BitsPerSample)
	}
	if tr.Codec != "ALAC" {
		t.Errorf("Codec = %q, want ALAC", tr.Codec)
	}

	digest := essenceOf(t, build(96000))
	if digest.ExtentVersion != "mp4-mdat-v3" {
		t.Errorf("extent = %q, want mp4-mdat-v3", digest.ExtentVersion)
	}
	if other := essenceOf(t, build(44100)); !digest.Equal(other) {
		t.Errorf("digest changed with the cookie rate: %s vs %s (the cookie is outside the salt)", digest, other)
	}
}

// TestMP4FLACSampleRateFromStreamInfo: the FLAC-in-ISOBMFF spec makes dfLa's STREAMINFO
// authoritative, so a 96 kHz FLAC-in-MP4 reports its real geometry rather than the entry's.
func TestMP4FLACSampleRateFromStreamInfo(t *testing.T) {
	si := mp4StreamInfo(96000, 2, 24, 4096, 4096, 480000)
	data := mp4AssembleStsd(mp4Stsd(mp4StsdEntry("fLaC", 2, 16, 0, mp4DfLa(si))), nil, nil, nil)
	tr := mustParseBytes(t, data).Properties().Tracks[0]
	if tr.SampleRate != 96000 || tr.Channels != 2 || tr.BitsPerSample != 24 {
		t.Errorf("track = %d Hz / %d ch / %d bit, want 96000/2/24", tr.SampleRate, tr.Channels, tr.BitsPerSample)
	}
	if tr.Codec != "FLAC" || tr.CodecProfile != "fLaC" {
		t.Errorf("codec = %q/%q, want FLAC with profile fLaC", tr.Codec, tr.CodecProfile)
	}
	if tr.MinBlockSize != 4096 || tr.MaxBlockSize != 4096 {
		t.Errorf("block sizes = %d/%d, want 4096/4096", tr.MinBlockSize, tr.MaxBlockSize)
	}
}

// TestMP4V2SoundEntryGeometry: a QuickTime version 2 sound entry carries a float64 rate
// that a hi-res .mov needs. Its geometry stays out of the digest salt, which has always
// been zero for such an entry.
func TestMP4V2SoundEntryGeometry(t *testing.T) {
	build := func(rate float64) []byte {
		return mp4AssembleStsd(mp4Stsd(mp4StsdEntryV2("lpcm", rate, 2, 24)), nil, nil, nil)
	}
	tr := mustParseBytes(t, build(96000)).Properties().Tracks[0]
	if tr.SampleRate != 96000 || tr.Channels != 2 || tr.BitsPerSample != 24 {
		t.Errorf("track = %d Hz / %d ch / %d bit, want 96000/2/24", tr.SampleRate, tr.Channels, tr.BitsPerSample)
	}

	digest := essenceOf(t, build(96000))
	if other := essenceOf(t, build(48000)); !digest.Equal(other) {
		t.Errorf("digest changed with the v2 rate: %s vs %s (v2 geometry is outside the salt)", digest, other)
	}
}

// TestMP4StsdPrefixIndependentOfAllocLimit: the stsd prefix read is clamped to the
// caller's allocation limit rather than refused by it, so the same bytes report the same
// geometry and hash to the same digest whatever limit the caller set.
func TestMP4StsdPrefixIndependentOfAllocLimit(t *testing.T) {
	// A second, padded entry pushes the stsd payload past the prefix a small limit allows.
	stsd := mp4Stsd(
		mp4StsdEntry("alac", 2, 16, 0, mp4AlacCookie(96000, 2, 24)),
		mp4StsdEntry("mp4a", 2, 16, 44100, mp4Atom("pad", make([]byte, 500))),
	)
	data := mp4AssembleStsd(stsd, nil, nil, nil)

	want := essenceOf(t, data)
	for _, limit := range []int64{1 << 20, 400, 64} {
		doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: limit}))
		if err != nil {
			t.Fatalf("limit %d: parse: %v", limit, err)
		}
		if got := doc.Properties().First().Codec; got != "ALAC" {
			t.Errorf("limit %d: Codec = %q, want ALAC", limit, got)
		}
		got, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(data)))
		if err != nil {
			t.Fatalf("limit %d: hash: %v", limit, err)
		}
		if !got.Equal(want) {
			t.Errorf("limit %d: digest = %s, want %s (the salt must not depend on the limit)", limit, got, want)
		}
	}
}
