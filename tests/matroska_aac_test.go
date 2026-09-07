package waxlabel_test

import (
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mkFloat builds an EBML 8-byte float element, the form the two frequency elements take.
func mkFloat(id uint64, f float64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(f))
	return mkEl(id, b[:])
}

// mkAudioTrackFile assembles a one-track Matroska around the given CodecID, Audio children,
// and CodecPrivate. A cluster carries the essence so the file can be hashed.
func mkAudioTrackFile(codecID string, private []byte, audioKids ...[]byte) []byte {
	entry := concat(mkUint(idTrackType, 2), mkStr(idCodecID, codecID), mkEl(idAudio, concat(audioKids...)))
	if private != nil {
		entry = append(entry, mkEl(idCodecPrivate, private)...)
	}
	seg := concat(mkAudioCluster(), mkEl(idTracks, mkEl(idTrackEntry, entry)))
	return concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, seg))
}

// TestMatroskaOutputSamplingFrequency: an SBR track declares the core rate in
// SamplingFrequency and the played rate in OutputSamplingFrequency, and it is the played
// one a listener hears. The digest salt stays on SamplingFrequency, so adding the output
// element must not move it.
func TestMatroskaOutputSamplingFrequency(t *testing.T) {
	t.Parallel()
	asc := mp4ASC(t, "2b920800") // hierarchical SBR: core 22050, extension 44100
	withOutput := mkAudioTrackFile("A_AAC/MPEG4/LC/SBR", asc,
		mkFloat(idSampFreq, 22050), mkFloat(idOutSampFreq, 44100), mkUint(idChannels, 2))
	without := mkAudioTrackFile("A_AAC/MPEG4/LC/SBR", asc,
		mkFloat(idSampFreq, 22050), mkUint(idChannels, 2))

	tr := mustParseBytes(t, withOutput).Properties().First()
	if tr.SampleRate != 44100 || tr.Channels != 2 {
		t.Errorf("track = %d Hz / %d ch, want the played 44100/2", tr.SampleRate, tr.Channels)
	}
	if tr.Codec != "AAC" || tr.CodecProfile != "HE-AAC" {
		t.Errorf("codec = %q/%q, want AAC with profile HE-AAC", tr.Codec, tr.CodecProfile)
	}
	if digest, other := essenceOf(t, withOutput), essenceOf(t, without); !digest.Equal(other) {
		t.Errorf("digest changed with OutputSamplingFrequency: %s vs %s (the salt is SamplingFrequency)", digest, other)
	}
}

// TestMatroskaAACRateFromCodecPrivate: a muxer that omits OutputSamplingFrequency still
// declares SBR in the CodecPrivate, which is enough to report the played rate.
func TestMatroskaAACRateFromCodecPrivate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		asc          string
		coreRate     float64
		coreChannels uint64
		rate         int
		channels     int
		profile      string
	}{
		{"hierarchical sbr", "2b920800", 22050, 2, 44100, 2, "HE-AAC"},
		{"hierarchical ps", "eb098800", 24000, 1, 48000, 2, "HE-AAC v2"},
		{"downsampled sbr does not double", "29918800", 48000, 2, 48000, 2, "HE-AAC"},
		{"tail denies sbr", "120856e500", 44100, 1, 44100, 1, "AAC LC"},
		{"no sbr signalling at all", "1390", 22050, 2, 22050, 2, "AAC LC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := mkAudioTrackFile("A_AAC", mp4ASC(t, tc.asc),
				mkFloat(idSampFreq, tc.coreRate), mkUint(idChannels, tc.coreChannels))
			tr := mustParseBytes(t, data).Properties().First()
			if tr.SampleRate != tc.rate || tr.CodecProfile != tc.profile {
				t.Errorf("track = %d Hz / %q, want %d/%q", tr.SampleRate, tr.CodecProfile, tc.rate, tc.profile)
			}
			// A played rate taken from the config has to bring its channel count with it, or
			// a parametric-stereo track reads as "HE-AAC v2" over one channel.
			if tr.Channels != tc.channels {
				t.Errorf("Channels = %d, want %d", tr.Channels, tc.channels)
			}
		})
	}
}

// TestMatroskaAACCodecIDSBRSurvivesSilentConfig: a CodecPrivate that says nothing about SBR
// does not contradict the CodecID's /SBR suffix, so the suffix stands. Letting the config win
// made the same file report "AAC LC" with a CodecPrivate and "HE-AAC" without one - adding
// information made the answer worse.
func TestMatroskaAACCodecIDSBRSurvivesSilentConfig(t *testing.T) {
	t.Parallel()
	kids := []([]byte){mkFloat(idSampFreq, 22050), mkUint(idChannels, 2)}
	silent := mkAudioTrackFile("A_AAC/MPEG4/LC/SBR", mp4ASC(t, "1390"), kids...)
	none := mkAudioTrackFile("A_AAC/MPEG4/LC/SBR", nil, kids...)
	for name, data := range map[string][]byte{"silent config": silent, "no config": none} {
		if got := mustParseBytes(t, data).Properties().First().CodecProfile; got != "HE-AAC" {
			t.Errorf("%s: CodecProfile = %q, want HE-AAC from the CodecID suffix", name, got)
		}
	}
	// A config that does address SBR outranks the suffix, which a muxer may have left stale.
	stale := mkAudioTrackFile("A_AAC/MPEG4/LC/SBR", mp4ASC(t, "120856e500"), kids...)
	if got := mustParseBytes(t, stale).Properties().First().CodecProfile; got != "AAC LC" {
		t.Errorf("CodecProfile = %q, want AAC LC (the config denies SBR)", got)
	}
}

// TestMatroskaAACProfileFallsBackToCodecID: a config that names nothing more specific than
// the codec leaves the CodecID suffix as the best description available.
func TestMatroskaAACProfileFallsBackToCodecID(t *testing.T) {
	t.Parallel()
	// Object type 17 (error-resilient AAC LC): a real type with no distinguishing name here.
	data := mkAudioTrackFile("A_AAC/MPEG4/LC", mp4ASC(t, "8a20"),
		mkFloat(idSampFreq, 44100), mkUint(idChannels, 2))
	tr := mustParseBytes(t, data).Properties().First()
	if tr.Codec != "AAC" || tr.CodecProfile != "AAC LC" {
		t.Errorf("codec = %q/%q, want AAC with the CodecID's AAC LC", tr.Codec, tr.CodecProfile)
	}
	if tr.SampleRate != 44100 {
		t.Errorf("SampleRate = %d, want 44100", tr.SampleRate)
	}
}

// TestMatroskaOutputSamplingFrequencyBeatsCodecPrivate: the container's own declaration is
// the muxer's considered answer, so a config that disagrees does not override it.
func TestMatroskaOutputSamplingFrequencyBeatsCodecPrivate(t *testing.T) {
	t.Parallel()
	data := mkAudioTrackFile("A_AAC", mp4ASC(t, "2b920800"),
		mkFloat(idSampFreq, 22050), mkFloat(idOutSampFreq, 32000), mkUint(idChannels, 2))
	if tr := mustParseBytes(t, data).Properties().First(); tr.SampleRate != 32000 {
		t.Errorf("SampleRate = %d, want the declared 32000", tr.SampleRate)
	}
}

// TestMatroskaHostileOutputSamplingFrequency: a NaN, infinite, or negative output rate is
// not a rate, and must leave the core rate standing rather than poisoning the track.
func TestMatroskaHostileOutputSamplingFrequency(t *testing.T) {
	t.Parallel()
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -44100, 0, math.MaxInt32} {
		data := mkAudioTrackFile("A_FLAC", nil,
			mkFloat(idSampFreq, 22050), mkFloat(idOutSampFreq, f), mkUint(idChannels, 2))
		if tr := mustParseBytes(t, data).Properties().First(); tr.SampleRate != 22050 {
			t.Errorf("output rate %v: SampleRate = %d, want 22050", f, tr.SampleRate)
		}
	}
}

// TestMatroskaAACProfileFromCodecID: mkvmerge and older ffmpeg builds write no CodecPrivate
// at all, and then the CodecID suffix is the only thing naming the object type.
func TestMatroskaAACProfileFromCodecID(t *testing.T) {
	t.Parallel()
	cases := []struct{ codecID, profile string }{
		{"A_AAC/MPEG4/LC/SBR", "HE-AAC"},
		{"A_AAC/MPEG2/LC/SBR", "HE-AAC"},
		{"A_AAC/MPEG4/MAIN", "AAC Main"},
		{"A_AAC/MPEG4/LC", "AAC LC"},
		{"A_AAC/MPEG4/SSR", "AAC SSR"},
		{"A_AAC/MPEG4/LTP", "AAC LTP"},
		{"A_AAC/MPEG2/LC", "AAC LC"},
		{"A_AAC", ""}, // nothing to say beyond the canonical name
	}
	for _, tc := range cases {
		t.Run(tc.codecID, func(t *testing.T) {
			t.Parallel()
			data := mkAudioTrackFile(tc.codecID, nil, mkFloat(idSampFreq, 44100), mkUint(idChannels, 2))
			tr := mustParseBytes(t, data).Properties().First()
			if tr.Codec != "AAC" || tr.CodecProfile != tc.profile {
				t.Errorf("codec = %q/%q, want AAC/%q", tr.Codec, tr.CodecProfile, tc.profile)
			}
			if tr.SampleRate != 44100 {
				t.Errorf("SampleRate = %d, want 44100 (no config to read)", tr.SampleRate)
			}
		})
	}
}

// TestMatroskaLargeCodecPrivateReadsPrefixOnly: the config decoder consumes a couple of
// dozen bytes, so a CodecPrivate declaring far more is read as a bounded prefix rather than
// allocated whole. The answer must be the same as for the short form.
func TestMatroskaLargeCodecPrivateReadsPrefixOnly(t *testing.T) {
	t.Parallel()
	asc := mp4ASC(t, "2b920800")
	padded := append(append([]byte(nil), asc...), make([]byte, 4<<20)...)
	kids := []([]byte){mkFloat(idSampFreq, 22050), mkUint(idChannels, 2)}
	short := mustParseBytes(t, mkAudioTrackFile("A_AAC", asc, kids...)).Properties().First()
	long := mustParseBytes(t, mkAudioTrackFile("A_AAC", padded, kids...)).Properties().First()
	if short.SampleRate != long.SampleRate || short.CodecProfile != long.CodecProfile {
		t.Errorf("padded config read as %d Hz / %q, want the short form's %d / %q",
			long.SampleRate, long.CodecProfile, short.SampleRate, short.CodecProfile)
	}
	if long.SampleRate != 44100 || long.CodecProfile != "HE-AAC" {
		t.Errorf("track = %d Hz / %q, want 44100/HE-AAC", long.SampleRate, long.CodecProfile)
	}
}

// TestMatroskaNonAACCodecPrivateIgnored: only an AAC track's CodecPrivate is an
// AudioSpecificConfig; a FLAC one must not be read as though it were.
func TestMatroskaNonAACCodecPrivateIgnored(t *testing.T) {
	t.Parallel()
	data := mkAudioTrackFile("A_FLAC", mp4ASC(t, "2b920800"),
		mkFloat(idSampFreq, 44100), mkUint(idChannels, 2), mkUint(idBitDepth, 16))
	tr := mustParseBytes(t, data).Properties().First()
	if tr.Codec != "FLAC" || tr.CodecProfile != "" || tr.SampleRate != 44100 {
		t.Errorf("track = %q/%q at %d Hz, want FLAC with no profile at 44100", tr.Codec, tr.CodecProfile, tr.SampleRate)
	}
}

// TestMatroskaDifferentialHEAACRemux: ffmpeg copying an HE-AAC track into Matroska writes
// the core rate in SamplingFrequency and the played one in OutputSamplingFrequency. Reading
// only the first reports half the rate ffprobe does.
func TestMatroskaDifferentialHEAACRemux(t *testing.T) {
	t.Parallel()
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	required := os.Getenv("WAXLABEL_REQUIRE_FFMPEG") != ""

	path := filepath.Join(t.TempDir(), "heaac.mka")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", "../testdata/heaac_v1.m4a", "-c", "copy", "-y", path).CombinedOutput()
	if err != nil {
		if required {
			t.Fatalf("ffmpeg remux: %v\n%s", err, out)
		}
		t.Skipf("ffmpeg cannot remux here: %v\n%s", err, out)
	}
	tr := mustParseFile(t, path).Properties().First()
	rate, channels, _ := ffprobeAudio(t, path)
	if rate != 44100 || channels != 2 {
		t.Fatalf("ffprobe says %d Hz / %d ch, want 44100/2", rate, channels)
	}
	if tr.SampleRate != rate || tr.Channels != channels {
		t.Errorf("track = %d Hz / %d ch, want ffprobe's %d/%d", tr.SampleRate, tr.Channels, rate, channels)
	}
	if tr.CodecProfile != "HE-AAC" {
		t.Errorf("CodecProfile = %q, want HE-AAC", tr.CodecProfile)
	}
}
