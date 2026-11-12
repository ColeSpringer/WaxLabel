package waxlabel_test

import (
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
)

// 96 kHz ALAC's rate does not fit the sample entry's 16.16 field, so the magic cookie carries it.
// The essence digest is unaffected: it salts with the raw entry values, so two files differing only
// in the cookie hash the same.
func TestMP4ALACSampleRateFromCookie(t *testing.T) {
	build := func(cookieRate int) []byte {
		return testfile.MP4AssembleStsd(testfile.MP4Stsd(testfile.MP4StsdEntry("alac", 2, 16, 0, testfile.MP4AlacCookie(cookieRate, 2, 24))), nil, nil, nil, 44100)
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
		t.Errorf("extent = %q, want mp4-mdat-v3: the file holds no sample tables", digest.ExtentVersion)
	}
	if other := essenceOf(t, build(44100)); !digest.Equal(other) {
		t.Errorf("digest changed with the cookie rate: %s vs %s (the cookie is outside the salt)", digest, other)
	}
}

// The FLAC-in-ISOBMFF spec makes dfLa's STREAMINFO authoritative, so a 96 kHz FLAC-in-MP4
// reports that geometry rather than the entry's.
func TestMP4FLACSampleRateFromStreamInfo(t *testing.T) {
	si := mp4StreamInfo(96000, 2, 24, 4096, 4096, 480000)
	data := testfile.MP4AssembleStsd(testfile.MP4Stsd(testfile.MP4StsdEntry("fLaC", 2, 16, 0, mp4DfLa(si))), nil, nil, nil, 44100)
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

// A QuickTime version 2 sound entry carries a float64 rate that a hi-res .mov needs. Its
// geometry stays out of the digest salt, which is zero for such an entry.
func TestMP4V2SoundEntryGeometry(t *testing.T) {
	build := func(rate float64) []byte {
		return testfile.MP4AssembleStsd(testfile.MP4Stsd(mp4StsdEntryV2("lpcm", rate, 2, 24)), nil, nil, nil, 44100)
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

// The stsd prefix is read whole or refused, never cut to the caller's allocation limit: a cut read
// would report other geometry, and salt another digest, for the same bytes.
func TestMP4StsdPrefixReadWholeOrRefused(t *testing.T) {
	// A second, padded entry pushes the stsd payload past the prefix a small limit holds.
	stsd := testfile.MP4Stsd(
		testfile.MP4StsdEntry("alac", 2, 16, 0, testfile.MP4AlacCookie(96000, 2, 24)),
		testfile.MP4StsdEntry("mp4a", 2, 16, 44100, testfile.MP4Atom("pad", make([]byte, 500))),
	)
	data := testfile.MP4AssembleStsd(stsd, nil, nil, nil, 44100)

	for _, limit := range []int64{400, 64} {
		_, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: limit}))
		if !errors.Is(err, waxerr.ErrSizeTooLarge) {
			t.Errorf("limit %d: err = %v, want declared size too large", limit, err)
		}
	}
	doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: 1 << 20}))
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Properties().First().Codec; got != "ALAC" {
		t.Errorf("Codec = %q, want ALAC", got)
	}
	got, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	if want := essenceOf(t, data); !got.Equal(want) {
		t.Errorf("digest = %s, want the default limit's %s", got, want)
	}
}
