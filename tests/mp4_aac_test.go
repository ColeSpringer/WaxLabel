package waxlabel_test

import (
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// mp4ZeroConfig returns a copy of an MP4 with the esds descriptor payload zeroed, so the
// codec configuration is unreadable while every other byte - the sample entry the digest
// salts with included - stays exactly as it was.
func mp4ZeroConfig(t *testing.T, data []byte) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	body := mp4BoxPayload(t, out, "esds")
	if len(body) == 0 {
		t.Fatal("no esds box in the file")
	}
	clear(body)
	return out
}

// mp4ASC decodes an AudioSpecificConfig written as hex, the form ffprobe's extradata dump,
// a Matroska CodecPrivate dump, and every specification example use.
func mp4ASC(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad config hex %q: %v", s, err)
	}
	return b
}

// mp4AACFile assembles a one-track MP4 whose mp4a entry declares the given geometry and
// carries the given AudioSpecificConfig, with the media timescale set independently so a
// test can withhold the second witness the implicit-SBR rule needs.
func mp4AACFile(entryRate, entryChannels, timescale int, esds []byte) []byte {
	return mp4AssembleStsd(mp4Stsd(mp4StsdEntry("mp4a", entryChannels, 16, entryRate, esds)),
		nil, nil, nil, timescale)
}

// TestMP4AACSampleRateFromConfig: a 96 kHz AAC's rate does not fit the sample entry's
// 16.16 field, so ffmpeg writes 0 there and the esds AudioSpecificConfig carries it. The
// essence digest is unaffected: it salts with the raw entry values, so two files differing
// only in the config hash the same.
func TestMP4AACSampleRateFromConfig(t *testing.T) {
	t.Parallel()
	build := func(asc string) []byte { return mp4AACFile(0, 2, 44100, mp4Esds(mp4ASC(t, asc))) }

	tr := mustParseBytes(t, build("1010")).Properties().Tracks[0]
	if tr.SampleRate != 96000 || tr.Channels != 2 {
		t.Errorf("track = %d Hz / %d ch, want 96000/2", tr.SampleRate, tr.Channels)
	}
	if tr.Codec != "AAC" || tr.CodecProfile != "AAC LC" {
		t.Errorf("codec = %q/%q, want AAC with profile AAC LC", tr.Codec, tr.CodecProfile)
	}

	digest := essenceOf(t, build("1010"))
	if digest.ExtentVersion != "mp4-mdat-v3" {
		t.Errorf("extent = %q, want mp4-mdat-v3", digest.ExtentVersion)
	}
	if other := essenceOf(t, build("1190")); !digest.Equal(other) {
		t.Errorf("digest changed with the config: %s vs %s (the config is outside the salt)", digest, other)
	}
}

// TestMP4AACSBRShapes: every SBR signalling a config can carry maps to the geometry a
// player produces, including the tail that explicitly denies SBR and the downsampled shape
// whose extension rate must not be doubled.
func TestMP4AACSBRShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		asc       string
		entryRate int
		rate      int
		channels  int
		profile   string
	}{
		{"lc 96k, entry cannot hold it", "1010", 0, 96000, 2, "AAC LC"},
		{"lc 96k with a tail denying sbr", "101056e500", 0, 96000, 2, "AAC LC"},
		{"lc 44100 mono", "120856e500", 44100, 44100, 1, "AAC LC"},
		{"explicit 24-bit rate", "178061a810", 0, 50000, 2, "AAC LC"},
		{"backward-compatible sbr", "119056e580", 48000, 96000, 2, "HE-AAC"},
		{"tail denies sbr, core rate wins", "131056e500", 24000, 24000, 2, "AAC LC"},
		{"backward-compatible sbr and ps", "130856e59d4880", 24000, 48000, 2, "HE-AAC v2"},
		{"hierarchical sbr", "2b920800", 22050, 44100, 2, "HE-AAC"},
		{"hierarchical ps", "eb098800", 24000, 48000, 2, "HE-AAC v2"},
		{"downsampled sbr does not double", "29918800", 48000, 48000, 2, "HE-AAC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := mp4AACFile(tc.entryRate, 2, 44100, mp4Esds(mp4ASC(t, tc.asc)))
			tr := mustParseBytes(t, data).Properties().Tracks[0]
			if tr.SampleRate != tc.rate || tr.Channels != tc.channels || tr.CodecProfile != tc.profile {
				t.Errorf("track = %d Hz / %d ch / %q, want %d/%d/%q",
					tr.SampleRate, tr.Channels, tr.CodecProfile, tc.rate, tc.channels, tc.profile)
			}
			if tr.Codec != "AAC" {
				t.Errorf("Codec = %q, want AAC", tr.Codec)
			}
		})
	}
}

// TestMP4AACImplicitSBRKeepsEntryRate: an implicitly signalled HE-AAC stream copied into
// MP4 carries a core-rate config, but the muxer decoded it and wrote the played rate into
// the sample entry. An entry landing on exactly double the core rate is the signal, and it
// holds whatever media timescale the muxer chose: requiring the timescale to match made a
// real ffmpeg remux report half its rate under MP4Box and DASH timescales.
func TestMP4AACImplicitSBRKeepsEntryRate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                     string
		asc                      string
		entryRate, entryChannels int
		timescale                int
		rate, channels           int
		profile                  string
	}{
		{"entry doubles the core rate", "1390", 44100, 2, 44100, 44100, 2, "HE-AAC"},
		{"mono core played as stereo", "1308", 48000, 2, 48000, 48000, 2, "HE-AAC v2"},
		// The timescale is the muxer's own unit, not a statement about the rate: 1000 is
		// MP4Box's default and 90000 is DASH's.
		{"timescale need not agree", "1390", 44100, 2, 1000, 44100, 2, "HE-AAC"},
		{"timescale absent entirely", "1390", 44100, 2, 0, 44100, 2, "HE-AAC"},
		{"muxer did not decode the stream", "1390", 22050, 2, 22050, 22050, 2, "AAC LC"},
		{"entry rate is a truncated shift", "1010", 30464, 2, 44100, 96000, 2, "AAC LC"},
		{"config denies sbr outright", "131056e500", 48000, 2, 48000, 24000, 2, "AAC LC"},
		{"entry holds no rate at all", "101056e500", 0, 2, 44100, 96000, 2, "AAC LC"},
		// HE-AAC is SBR over AAC LC, so only an AAC LC core can be implicitly signalled. A
		// low-delay track whose rates happen to sit 2:1 apart keeps its own object type.
		{"low-delay core is not relabeled", "bb10", 48000, 2, 48000, 24000, 2, "AAC LD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := mp4AACFile(tc.entryRate, tc.entryChannels, tc.timescale, mp4Esds(mp4ASC(t, tc.asc)))
			tr := mustParseBytes(t, data).Properties().Tracks[0]
			if tr.SampleRate != tc.rate || tr.Channels != tc.channels || tr.CodecProfile != tc.profile {
				t.Errorf("track = %d Hz / %d ch / %q, want %d/%d/%q",
					tr.SampleRate, tr.Channels, tr.CodecProfile, tc.rate, tc.channels, tc.profile)
			}
		})
	}
}

// TestMP4AACWaveWrappedEsds: QuickTime nests the codec configuration in a wave wrapper
// beside a 12-byte child literally named mp4a, so the scan has to match the box name and
// descend rather than stop at the first four-cc that looks right.
func TestMP4AACWaveWrappedEsds(t *testing.T) {
	t.Parallel()
	wave := mp4Wave(
		mp4Atom("frma", []byte("mp4a")),
		mp4Atom("mp4a", make([]byte, 4)),
		mp4EsdsShort(mp4ASC(t, "1010")),
	)
	v1 := mp4AssembleStsd(mp4Stsd(mp4StsdEntryV1("mp4a", 2, 16, 0, wave)), nil, nil, nil, 44100)
	if tr := mustParseBytes(t, v1).Properties().Tracks[0]; tr.SampleRate != 96000 || tr.CodecProfile != "AAC LC" {
		t.Errorf("v1 entry: %d Hz / %q, want 96000/\"AAC LC\"", tr.SampleRate, tr.CodecProfile)
	}
	v2 := mp4AssembleStsd(mp4Stsd(mp4StsdEntryV2("mp4a", 96000, 2, 0, wave)), nil, nil, nil, 44100)
	if tr := mustParseBytes(t, v2).Properties().Tracks[0]; tr.SampleRate != 96000 || tr.CodecProfile != "AAC LC" {
		t.Errorf("v2 entry: %d Hz / %q, want 96000/\"AAC LC\"", tr.SampleRate, tr.CodecProfile)
	}
}

// TestMP4EsdsMalformedKeepsEntry: every way an esds can be unreadable leaves the sample
// entry's own geometry and four-cc standing rather than reporting a partial decode.
func TestMP4EsdsMalformedKeepsEntry(t *testing.T) {
	t.Parallel()
	asc := mp4ASC(t, "1010") // would report 96000 if it were read
	// The well-formed nest, so each case can break exactly one thing in it.
	dsi := mp4DescrShort(0x05, asc)
	decoderCfg := mp4DescrShort(0x04, slices.Concat([]byte{0x40, 0x15}, make([]byte, 11), dsi))
	esBody := slices.Concat([]byte{0, 1, 0}, decoderCfg) // ES_ID, no optional fields
	esds := func(descr []byte) []byte { return mp4Atom("esds", append([]byte{0, 0, 0, 0}, descr...)) }

	cases := []struct {
		name string
		esds []byte
	}{
		{"config too short to decode", mp4Esds(asc[:1])},
		{"no DecoderSpecificInfo at all", mp4Esds(nil)},
		{"ES length over-declared", esds(slices.Concat([]byte{0x03, byte(len(esBody) + 8)}, esBody))},
		{"ES length under-declared", esds(slices.Concat([]byte{0x03, 2}, esBody))},
		{"a fifth length byte", esds(slices.Concat([]byte{0x03, 0x80, 0x80, 0x80, 0x80}, esBody))},
		{"box too small for its FullBox header", mp4Atom("esds", make([]byte, 3))},
		{"DecoderConfig body too short", esds(mp4DescrShort(0x03,
			slices.Concat([]byte{0, 1, 0}, mp4DescrShort(0x04, make([]byte, 12)))))},
		{"object type indication carrying no config", mp4EsdsRaw(0xE1, asc, mp4DescrShort)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := mustParseBytes(t, mp4AACFile(44100, 2, 44100, tc.esds)).Properties().Tracks[0]
			if tr.SampleRate != 44100 || tr.Channels != 2 {
				t.Errorf("track = %d Hz / %d ch, want the entry values 44100/2", tr.SampleRate, tr.Channels)
			}
			if tr.Codec != "AAC" || tr.CodecProfile != "mp4a" {
				t.Errorf("codec = %q/%q, want AAC with the four-cc as profile", tr.Codec, tr.CodecProfile)
			}
		})
	}
}

// TestMP4EsdsMP3ObjectType: an MPEG-1 or MPEG-2 audio objectTypeIndication carries no
// AudioSpecificConfig, and calling the track AAC because the four-cc says mp4a is wrong.
func TestMP4EsdsMP3ObjectType(t *testing.T) {
	t.Parallel()
	for _, oti := range []byte{0x69, 0x6B} {
		data := mp4AACFile(44100, 2, 44100, mp4EsdsRaw(oti, nil, mp4DescrShort))
		tr := mustParseBytes(t, data).Properties().Tracks[0]
		if tr.Codec != "MP3" || tr.CodecProfile != "" {
			t.Errorf("oti %#x: codec = %q/%q, want MP3 with no profile", oti, tr.Codec, tr.CodecProfile)
		}
		if tr.SampleRate != 44100 || tr.Channels != 2 {
			t.Errorf("oti %#x: track = %d Hz / %d ch, want the entry values 44100/2", oti, tr.SampleRate, tr.Channels)
		}
	}
}

// TestMP4EsdsProgramConfigElement: channelConfiguration 0 puts the layout in a
// variable-length element this decoder does not walk, so the config supplies the rate and
// the entry keeps the channel count.
func TestMP4EsdsProgramConfigElement(t *testing.T) {
	t.Parallel()
	data := mp4AACFile(48000, 6, 48000, mp4Esds(mp4ASC(t, "1180")))
	tr := mustParseBytes(t, data).Properties().Tracks[0]
	if tr.SampleRate != 48000 || tr.Channels != 6 || tr.CodecProfile != "AAC LC" {
		t.Errorf("track = %d Hz / %d ch / %q, want 48000/6/\"AAC LC\"", tr.SampleRate, tr.Channels, tr.CodecProfile)
	}
}

// TestMP4ExistingAACFixturesUnchanged: reading the esds must not move the geometry or the
// digest of the AAC files that already parsed correctly; only the profile gains detail.
func TestMP4ExistingAACFixturesUnchanged(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"../testdata/sample.m4a", "../testdata/notags.m4a", "../testdata/sample_chapters.m4b"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tr := mustParseBytes(t, data).Properties().Tracks[0]
			if tr.SampleRate != 44100 || tr.Channels != 1 {
				t.Errorf("track = %d Hz / %d ch, want 44100/1", tr.SampleRate, tr.Channels)
			}
			if tr.Codec != "AAC" || tr.CodecProfile != "AAC LC" {
				t.Errorf("codec = %q/%q, want AAC with profile AAC LC", tr.Codec, tr.CodecProfile)
			}
			digest := essenceOf(t, data)
			if digest.ExtentVersion != "mp4-mdat-v3" {
				t.Errorf("extent = %q, want mp4-mdat-v3", digest.ExtentVersion)
			}
			if other := essenceOf(t, mp4ZeroConfig(t, data)); !digest.Equal(other) {
				t.Errorf("digest changed with the config bytes: %s vs %s (the salt is the raw entry)", digest, other)
			}
		})
	}
}

// TestMP4HEAACFixtures: real fdk-aac output, one file per explicit SBR shape.
func TestMP4HEAACFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path     string
		rate     int
		channels int
		profile  string
	}{
		{"../testdata/heaac_v1.m4a", 44100, 2, "HE-AAC"},
		{"../testdata/heaac_v2.m4a", 48000, 2, "HE-AAC v2"},
		{"../testdata/heaac_ds.m4a", 48000, 2, "HE-AAC"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			tr := mustParseFile(t, tc.path).Properties().Tracks[0]
			if tr.SampleRate != tc.rate || tr.Channels != tc.channels || tr.CodecProfile != tc.profile {
				t.Errorf("track = %d Hz / %d ch / %q, want %d/%d/%q",
					tr.SampleRate, tr.Channels, tr.CodecProfile, tc.rate, tc.channels, tc.profile)
			}
			if tr.Codec != "AAC" {
				t.Errorf("Codec = %q, want AAC", tr.Codec)
			}
		})
	}
}

// TestMP4DifferentialFFmpegHiResAAC: real ffmpeg output at 96 kHz, where the sample entry's
// 16.16 rate field holds 0 and only the esds config carries the rate. Both the .m4a and the
// QuickTime .mov shape must agree with ffprobe.
func TestMP4DifferentialFFmpegHiResAAC(t *testing.T) {
	t.Parallel()
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	required := os.Getenv("WAXLABEL_REQUIRE_FFMPEG") != ""
	dir := t.TempDir()

	// t is a parameter, not a capture: each leg encodes inside its own subtest, so a
	// Fatal or Skip must land on the T of the goroutine running it.
	encode := func(t *testing.T, name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi",
			"-i", "sine=frequency=1000:duration=1", "-ac", "2", "-ar", "96000", "-c:a", "aac", "-y", path}
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			if required {
				t.Fatalf("ffmpeg %s: %v\n%s", name, err, out)
			}
			t.Skipf("ffmpeg cannot encode %s here: %v\n%s", name, err, out)
		}
		return path
	}
	for _, name := range []string{"hires.m4a", "hires.mov"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := encode(t, name)
			tr := mustParseFile(t, path).Properties().Tracks[0]
			rate, channels, _ := ffprobeAudio(t, path)
			if rate != 96000 || channels != 2 {
				t.Fatalf("ffprobe says %d Hz / %d ch, want 96000/2", rate, channels)
			}
			if tr.SampleRate != rate || tr.Channels != channels {
				t.Errorf("track = %d Hz / %d ch, want ffprobe's %d/%d", tr.SampleRate, tr.Channels, rate, channels)
			}
		})
	}
}

// TestMP4DifferentialFFprobeSBRShapes: every explicit SBR signalling, synthesized, read back
// by ffprobe. Only the rate and channel count are compared: a frameless file has no profile
// for ffprobe to report.
func TestMP4DifferentialFFprobeSBRShapes(t *testing.T) {
	t.Parallel()
	requireTool(t, "ffprobe")
	dir := t.TempDir()
	cases := []struct {
		name      string
		asc       string
		entryRate int
	}{
		{"lc96k", "1010", 0},
		{"bwcompat_sbr", "119056e580", 48000},
		{"bwcompat_no_sbr", "101056e500", 0},
		{"bwcompat_sbr_ps", "130856e59d4880", 24000},
		{"hierarchical_sbr", "2b920800", 22050},
		{"hierarchical_ps", "eb098800", 24000},
		{"downsampled_sbr", "29918800", 48000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, tc.name+".m4a")
			data := mp4AACFile(tc.entryRate, 2, 44100, mp4Esds(mp4ASC(t, tc.asc)))
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			tr := mustParseBytes(t, data).Properties().Tracks[0]
			rate, channels, _ := ffprobeAudio(t, path)
			if tr.SampleRate != rate || tr.Channels != channels {
				t.Errorf("track = %d Hz / %d ch, want ffprobe's %d/%d", tr.SampleRate, tr.Channels, rate, channels)
			}
		})
	}
}

// TestMP4DifferentialHEAACFixtures: real fdk-aac files, where ffprobe's profile is a second
// check on the object type we name.
func TestMP4DifferentialHEAACFixtures(t *testing.T) {
	t.Parallel()
	requireTool(t, "ffprobe")
	// ffprobe spells the profiles its own way; the mapping is the contract, not the text.
	profiles := map[string]string{"HE-AAC": "HE-AAC", "HE-AACv2": "HE-AAC v2", "LC": "AAC LC"}
	for _, path := range []string{"../testdata/heaac_v1.m4a", "../testdata/heaac_v2.m4a", "../testdata/heaac_ds.m4a"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			tr := mustParseFile(t, path).Properties().Tracks[0]
			rate, channels, profile := ffprobeAudio(t, path)
			if tr.SampleRate != rate || tr.Channels != channels {
				t.Errorf("track = %d Hz / %d ch, want ffprobe's %d/%d", tr.SampleRate, tr.Channels, rate, channels)
			}
			want, ok := profiles[profile]
			if !ok {
				t.Fatalf("ffprobe reported an unmapped profile %q", profile)
			}
			if tr.CodecProfile != want {
				t.Errorf("CodecProfile = %q, want %q (ffprobe says %q)", tr.CodecProfile, want, profile)
			}
		})
	}
}

// TestMP4DifferentialImplicitRemux: ffmpeg copying an implicitly signalled HE-AAC ADTS
// stream into MP4 decodes it and writes the played geometry into the entry and the media
// timescale. The profile is not compared: ffprobe's comes from decoding the frames, which
// no header parse can match.
func TestMP4DifferentialImplicitRemux(t *testing.T) {
	t.Parallel()
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	required := os.Getenv("WAXLABEL_REQUIRE_FFMPEG") != ""
	dir := t.TempDir()
	cases := []struct {
		src            string
		rate, channels int
		profile        string
	}{
		{"../testdata/heaac_v1.aac", 44100, 2, "HE-AAC"},
		{"../testdata/heaac_v2.aac", 48000, 2, "HE-AAC v2"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, filepath.Base(tc.src)+".m4a")
			out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
				"-i", tc.src, "-c", "copy", "-y", path).CombinedOutput()
			if err != nil {
				if required {
					t.Fatalf("ffmpeg remux: %v\n%s", err, out)
				}
				t.Skipf("ffmpeg cannot remux here: %v\n%s", err, out)
			}
			tr := mustParseFile(t, path).Properties().Tracks[0]
			rate, channels, _ := ffprobeAudio(t, path)
			if rate != tc.rate || channels != tc.channels {
				t.Fatalf("ffprobe says %d Hz / %d ch, want %d/%d", rate, channels, tc.rate, tc.channels)
			}
			if tr.SampleRate != rate || tr.Channels != channels {
				t.Errorf("track = %d Hz / %d ch, want ffprobe's %d/%d", tr.SampleRate, tr.Channels, rate, channels)
			}
			if tr.CodecProfile != tc.profile {
				t.Errorf("CodecProfile = %q, want %q", tr.CodecProfile, tc.profile)
			}
		})
	}
}
