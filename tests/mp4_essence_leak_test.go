package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
)

// mp4WithTrailingMdats builds a parseable MP4 (ftyp + audio-only moov + audio mdat) and appends
// nLeaked extra "dead" mdats after the audio, the shape left by a chapter writer that appended a
// fresh QuickTime-chapter mdat on every edit without reclaiming the prior one.
func mp4WithTrailingMdats(nLeaked int) []byte {
	audio := bytes.Repeat([]byte{0xA7}, 120)
	dead := bytes.Repeat([]byte{0xCC}, 48)
	build := func(stcoOff uint32) []byte {
		parts := [][]byte{testfile.MP4Ftyp(), mp4Moov(nil, stcoOff), testfile.MP4Atom("mdat", audio)}
		for range nLeaked {
			parts = append(parts, testfile.MP4Atom("mdat", dead))
		}
		return slices.Concat(parts...)
	}
	tmp := build(0)
	j := bytes.Index(tmp, []byte("mdat"))
	return build(uint32(j + 4)) // audio stco -> first (audio) mdat payload
}

// The audio essence is exactly the mdats that contain an audio-track chunk offset, so leaked
// dead chapter mdats (with no surviving chapter track) are excluded from the digest, which
// equals a clean single-mdat file's digest.
func TestMP4EssenceIgnoresLeakedChapterMdats(t *testing.T) {
	clean := mp4WithTrailingMdats(0)
	leaked := mp4WithTrailingMdats(3)

	// Sanity: the leak really is present (4 mdat atoms vs the clean file's 1).
	if got := bytes.Count(leaked, []byte("mdat")); got != 4 {
		t.Fatalf("leaked fixture mdat count = %d, want 4", got)
	}
	if got := bytes.Count(clean, []byte("mdat")); got != 1 {
		t.Fatalf("clean fixture mdat count = %d, want 1", got)
	}

	cleanDigest := essenceOf(t, clean)
	leakedDigest := essenceOf(t, leaked)
	if !cleanDigest.Equal(leakedDigest) {
		t.Errorf("essence differs between clean and leaked files: the dead chapter mdats were hashed as audio\n clean=%x\nleaked=%x",
			cleanDigest.Sum, leakedDigest.Sum)
	}
	// Deterministic and stable across recomputation.
	if again := essenceOf(t, leaked); !again.Equal(leakedDigest) {
		t.Errorf("essence not stable across recomputation of the leaked file")
	}
}

// mp4TwoAudioTwoMdat builds a parseable MP4 with two independent audio (soun) tracks whose
// chunks live in two separate mdats: track 1 -> mdat1, track 2 -> mdat2. It models the
// multi-track shape whose second audio track the essence digest must still cover.
func mp4TwoAudioTwoMdat(audio1, audio2 []byte) []byte {
	build := func(stco1, stco2 uint32) []byte {
		moov := testfile.MP4Atom("moov", slices.Concat(testfile.MP4SoundTrak(1, stco1), testfile.MP4SoundTrak(2, stco2)))
		return slices.Concat(testfile.MP4Ftyp(), moov, testfile.MP4Atom("mdat", audio1), testfile.MP4Atom("mdat", audio2))
	}
	tmp := build(0, 0)
	o1 := bytes.Index(tmp, []byte("mdat")) + 4           // payload of the first mdat
	o2 := bytes.Index(tmp[o1:], []byte("mdat")) + o1 + 4 // payload of the second mdat
	return build(uint32(o1), uint32(o2))
}

// With two audio tracks in two mdats, the audio-essence digest hashes both. Filtering on only
// the first track's offset table would drop the second track's mdat, so a change confined to it
// would compare equal (a verify/dedup false negative).
func TestMP4EssenceCoversSecondAudioTrack(t *testing.T) {
	base := mp4TwoAudioTwoMdat(bytes.Repeat([]byte{0x11}, 64), bytes.Repeat([]byte{0x22}, 64))
	alt := mp4TwoAudioTwoMdat(bytes.Repeat([]byte{0x11}, 64), bytes.Repeat([]byte{0x33}, 64))

	// Sanity: the two distinct mdats really are present and the files are the same size.
	if got := bytes.Count(base, []byte("mdat")); got != 2 {
		t.Fatalf("fixture mdat count = %d, want 2", got)
	}
	if len(base) != len(alt) {
		t.Fatalf("setup: fixtures differ in length (%d vs %d)", len(base), len(alt))
	}
	if d := essenceOf(t, base); d.Equal(essenceOf(t, alt)) {
		t.Error("essence digest ignored the second audio track's mdat: a change there compared equal")
	}
}

// When no chunk offset lands in an mdat every mdat is hashed whole, but never one the chapter
// track's samples fill, so a chapter write leaves that digest alone: also where the mdat it
// replaces does not end the file and so stays, as free space.
func TestMP4EssenceFallbackExcludesChapterMdat(t *testing.T) {
	// The audio track's one chunk offset points into ftyp.
	base := slices.Concat(testfile.MP4Ftyp(), mp4Moov(nil, 4), testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)))
	chaptered, err := mustParseBytes(t, base).Edit().SetChapters(chaptersFromTitles("A", "B")...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	written := applyToBytes(t, base, chaptered)
	for name, data := range map[string][]byte{
		"no chapters":                base,
		"chapter mdat ahead of free": slices.Concat(written, testfile.MP4Atom("free", nil)),
		"chapter mdat ahead of mdat": slices.Concat(written, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xC3}, 40))),
	} {
		t.Run(name, func(t *testing.T) {
			essence := essenceOf(t, data)
			out := data
			for _, titles := range [][]string{{"C", "D"}, {"E"}, nil} {
				plan, err := mustParseBytes(t, out).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare(wl.WithVerifyEssence())
				if err != nil {
					t.Fatal(err)
				}
				out = applyToBytes(t, out, plan)
				if re := mustParseBytes(t, out); len(re.Chapters()) != len(titles) || !essenceOf(t, out).Equal(essence) {
					t.Errorf("chapters %q: %d read back, digest equal %v", titles, len(re.Chapters()), essenceOf(t, out).Equal(essence))
				}
			}
		})
	}
}

// mp4AV is a file of two audio chunks interleaved with two video chunks, with the sample tables
// that place each.
func mp4AV(audio byte, video []byte, moovKids ...[]byte) []byte {
	a := [][]byte{bytes.Repeat([]byte{audio}, 100), bytes.Repeat([]byte{audio + 1}, 60)}
	if video == nil {
		return testfile.MP4AVFile(a, nil, moovKids...)
	}
	return testfile.MP4AVFile(a, [][]byte{video, bytes.Repeat(video, 3)}, moovKids...)
}

// The essence is the sound tracks' sample chunks: two files with the same audio packets hash the
// same whatever video lies between them, and with none.
func TestMP4EssenceIsTheSoundTracksChunks(t *testing.T) {
	base := mp4AV(0x11, []byte("video one"))
	digest := essenceOf(t, base)
	if digest.ExtentVersion != "mp4-samples-v4" {
		t.Fatalf("extent = %q, want mp4-samples-v4", digest.ExtentVersion)
	}
	for name, other := range map[string][]byte{
		"other video":  mp4AV(0x11, []byte("a second, longer video")),
		"no video":     mp4AV(0x11, nil),
		"tagged":       mp4AV(0x11, []byte("video one"), testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "T"))))),
		"junk in mdat": bytes.Replace(mp4AV(0x11, []byte("video one")), []byte("video one"), []byte("VIDEO ONE"), 1),
	} {
		if d := essenceOf(t, other); !d.Equal(digest) {
			t.Errorf("%s: digest %s, want %s", name, d, digest)
		}
	}
	if d := essenceOf(t, mp4AV(0x21, []byte("video one"))); d.Equal(digest) {
		t.Error("other audio hashed the same")
	}
	// Two sound tracks: both are essence.
	two := func() []byte {
		return testfile.MP4TracksFile(nil, func(audio, _, thumbs uint32) []byte {
			return slices.Concat(testfile.MP4Trak("soun", 1, slices.Concat(testfile.MP4StsdAudio(), testfile.MP4SampleTables([]uint32{audio}, [][]int{{120}}))),
				testfile.MP4Trak("soun", 2, slices.Concat(testfile.MP4StsdAudio(), testfile.MP4SampleTables([]uint32{thumbs}, [][]int{{40}}))))
		})
	}
	a, b := two(), two()
	b[len(b)-1] ^= 0xFF // the last byte of the second track's chunk
	if da, db := essenceOf(t, a), essenceOf(t, b); da.ExtentVersion != "mp4-samples-v4" || da.Equal(db) {
		t.Errorf("a change in the second sound track hashed the same (%s)", da)
	}
	if got := mustParseBytes(t, a).Properties().Tracks[0].Bitrate; got != 120*8 {
		t.Errorf("bitrate = %d, want %d: the first sound track's 120 bytes over its second", got, 120*8)
	}
}

// The bitrate is the first sound track's sample bytes over its length, not the mdat's.
func TestMP4BitrateCountsAudioSamplesOnly(t *testing.T) {
	want := mustParseBytes(t, mp4AV(0x11, nil)).Properties().Tracks[0].Bitrate
	if want != 160*8 {
		t.Fatalf("audio-only bitrate = %d, want %d: 160 bytes over the track's second", want, 160*8)
	}
	if got := mustParseBytes(t, mp4AV(0x11, bytes.Repeat([]byte{0xEE}, 5000))).Properties().Tracks[0].Bitrate; got != want {
		t.Errorf("bitrate beside a video track = %d, want %d", got, want)
	}
}

// A tag edit that moves the media and a chapter write keep the digest, and the chapter samples
// stay out of it.
func TestMP4SampleEssenceSurvivesEdits(t *testing.T) {
	data := mp4AV(0x11, []byte("video one"))
	digest := essenceOf(t, data)
	plan, err := mustParseBytes(t, data).Edit().Set("TITLE", string(bytes.Repeat([]byte("T"), 5000))).Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if d := essenceOf(t, out); !d.Equal(digest) {
		t.Errorf("digest after a tag edit %s, want %s", d, digest)
	}
	for _, titles := range [][]string{{"A", "B"}, {"C"}, nil} {
		plan, err := mustParseBytes(t, out).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare(wl.WithVerifyEssence())
		if err != nil {
			t.Fatal(err)
		}
		out = applyToBytes(t, out, plan)
		if d := essenceOf(t, out); !d.Equal(digest) || len(mustParseBytes(t, out).Chapters()) != len(titles) {
			t.Errorf("chapters %q: digest %s, want %s", titles, d, digest)
		}
	}
}

// Sample tables that cannot be read, or that place nothing in the file, leave the mdats as the
// essence under the older label.
func TestMP4EssenceFallsBackToTheMdats(t *testing.T) {
	whole := mp4AV(0x11, nil)
	cut := func(name string) []byte {
		out := slices.Clone(whole)
		copy(out[bytes.Index(out, []byte(name)):], "free")
		return out
	}
	empty := slices.Clone(whole)
	stco := bytes.Index(empty, []byte("stco"))
	for i := range 2 {
		binary.BigEndian.PutUint32(empty[stco+12+4*i:], uint32(len(empty)+100))
	}
	// A sound track with no chunk table ahead of one that has its own.
	tableless := testfile.MP4TracksFile(nil, func(audio, _, thumbs uint32) []byte {
		first := testfile.MP4SampleTables([]uint32{audio}, [][]int{{120}})
		return slices.Concat(testfile.MP4Trak("soun", 1, slices.Concat(testfile.MP4StsdAudio(), first[:bytes.Index(first, []byte("stco"))-4])),
			testfile.MP4Trak("soun", 2, slices.Concat(testfile.MP4StsdAudio(), testfile.MP4SampleTables([]uint32{thumbs}, [][]int{{40}}))))
	})
	count := func(n int) []byte {
		return bytes.Replace(whole, slices.Concat(testfile.BE32(0), testfile.BE32(4)), slices.Concat(testfile.BE32(0), testfile.BE32(n)), 1)
	}
	for name, data := range map[string][]byte{
		"no stsz": cut("stsz"), "no stsc": cut("stsc"), "chunks past the file": empty, "no stco in the first of two tracks": tableless,
		"stsz cut short": count(400), "stsz count two past its table": count(6),
		"stsc count past its table": bytes.Replace(whole, slices.Concat([]byte("stsc"), testfile.BE32(0), testfile.BE32(2)), slices.Concat([]byte("stsc"), testfile.BE32(0), testfile.BE32(3)), 1),
	} {
		d := essenceOf(t, data)
		if d.ExtentVersion != "mp4-mdat-v3" {
			t.Errorf("%s: extent = %q, want mp4-mdat-v3", name, d.ExtentVersion)
		}
		if doc := mustParseBytes(t, data); len(doc.Lint()) != 0 && name != "chunks past the file" {
			t.Errorf("%s: lint %v", name, doc.Lint())
		}
	}
	// A chunk that runs past the end of the file is cut to it.
	short := whole[:len(whole)-10]
	if d := essenceOf(t, short); d.ExtentVersion != "mp4-samples-v4" {
		t.Errorf("truncated file: extent = %q", d.ExtentVersion)
	}
}

// A track that counts frames holds more bytes than its stsz says: a frame's, or a packet's share.
// The essence is every byte of its chunks and nothing between them.
func TestMP4EssenceOfFrameCountedAudio(t *testing.T) {
	v1 := func(fourcc string, channels, bits, samplesPerPacket, bytesPerFrame int) []byte {
		e := mp4StsdEntryV1(fourcc, channels, bits, 8000)
		binary.BigEndian.PutUint32(e[36:], uint32(samplesPerPacket))
		binary.BigEndian.PutUint32(e[44:], uint32(bytesPerFrame))
		return e
	}
	for _, c := range []struct {
		name                         string
		entry                        []byte
		frames, stszSize, chunkBytes int
	}{
		{"16-bit stereo, stsz of 1", testfile.MP4StsdEntry("twos", 2, 16, 8000), 100, 1, 400},
		{"16-bit stereo, stsz of a channel's sample", testfile.MP4StsdEntry("sowt", 2, 16, 8000), 100, 2, 400},
		{"24-bit mono, stsz of the frame", testfile.MP4StsdEntry("in24", 1, 16, 8000), 100, 3, 300},
		{"mu-law stereo", testfile.MP4StsdEntry("ulaw", 2, 16, 8000), 100, 1, 200},
		{"32-bit float by its pcmC, the entry saying 16", testfile.MP4StsdEntry("fpcm", 1, 16, 8000, mp4PcmC(32)), 100, 4, 400},
		{"24-bit by its pcmC, stsz of 1", testfile.MP4StsdEntry("ipcm", 2, 16, 8000, mp4PcmC(24)), 100, 1, 600},
		{"float with no pcmC: the stsz stands", testfile.MP4StsdEntry("fpcm", 1, 16, 8000), 100, 4, 400},
		{"a version 2 entry", mp4StsdEntryV2("lpcm", 8000, 2, 24), 100, 1, 600},
		{"ima4 by its description", v1("ima4", 1, 16, 64, 34), 128, 1, 68},
		{"ima4 stereo with an empty description", v1("ima4", 2, 16, 0, 0), 128, 1, 136},
		{"MACE 6:1", testfile.MP4StsdEntry("MAC6", 1, 8, 8000), 120, 1, 20},
		{"a packet ratio from the description alone", v1("Qclp", 1, 16, 160, 35), 320, 1, 70},
		{"a chunk that ends inside a packet", v1("ima4", 1, 16, 64, 34), 100, 1, 34},
	} {
		t.Run(c.name, func(t *testing.T) {
			data, first, between, second := testfile.MP4FrameCountedFile(c.entry, c.frames, c.stszSize, c.chunkBytes)
			digest := essenceOf(t, data)
			if digest.ExtentVersion != "mp4-samples-v4" {
				t.Fatalf("extent = %q", digest.ExtentVersion)
			}
			for what, at := range map[string]int{"the first chunk's last byte": between - 1, "the second chunk's first byte": second, "the last byte": second + c.chunkBytes - 1, "the first byte": first} {
				changed := slices.Clone(data)
				changed[at] ^= 0xFF
				if essenceOf(t, changed).Equal(digest) {
					t.Errorf("a change of %s left the digest alone", what)
				}
			}
			for what, at := range map[string]int{"the byte behind the first chunk": between, "the byte ahead of the second": second - 1} {
				changed := slices.Clone(data)
				changed[at] ^= 0xFF
				if !essenceOf(t, changed).Equal(digest) {
					t.Errorf("a change of %s changed the digest", what)
				}
			}
			if got, want := mustParseBytes(t, data).Properties().Tracks[0].Bitrate, 2*c.chunkBytes*8; got != want {
				t.Errorf("bitrate = %d, want %d: both chunks over the track's second", got, want)
			}
		})
	}
	// Frames this cannot size, a codec's packets or a float of no stated width: the mdats stand.
	for name, entry := range map[string][]byte{"agsm": testfile.MP4StsdEntry("agsm", 1, 16, 8000), "fpcm with no pcmC": testfile.MP4StsdEntry("fpcm", 1, 16, 8000)} {
		data, _, _, _ := testfile.MP4FrameCountedFile(entry, 160, 1, 33)
		if d := essenceOf(t, data); d.ExtentVersion != "mp4-mdat-v3" {
			t.Errorf("%s: extent = %q, want mp4-mdat-v3", name, d.ExtentVersion)
		}
	}
	// Samples with durations of their own, or of more than one run, are sized by the stsz as it
	// stands: here a size of 2 for 16-bit stereo, which a frame count would double.
	for name, patch := range map[string]func(stts []byte){
		"a duration of 1024": func(stts []byte) { binary.BigEndian.PutUint32(stts[16:], 1024) },
		"two runs":           func(stts []byte) { binary.BigEndian.PutUint32(stts[8:], 2) },
	} {
		data, first, _, _ := testfile.MP4FrameCountedFile(testfile.MP4StsdEntry("twos", 2, 16, 8000), 100, 2, 200)
		patch(data[bytes.Index(data, []byte("stts")):])
		changed, past := slices.Clone(data), slices.Clone(data)
		changed[first+199] ^= 0xFF
		past[first+200] ^= 0xFF
		if d := essenceOf(t, data); d.ExtentVersion != "mp4-samples-v4" || essenceOf(t, changed).Equal(d) || !essenceOf(t, past).Equal(d) {
			t.Errorf("%s: extent %q, or the chunk is not the 200 bytes its stsz gives", name, d.ExtentVersion)
		}
	}
}

// A sample size that overstates a chunk does not carry the essence past the mdat that holds it:
// what lies behind is no audio, and a write may move or change it.
func TestMP4EssenceStaysInsideTheChunksMdat(t *testing.T) {
	data := slices.Concat(mp4AV(0x11, nil), testfile.MP4Atom("free", bytes.Repeat([]byte{0x33}, 400)))
	stsz := bytes.Index(data, []byte("stsz"))
	last := stsz + 4 + 12 + 3*4 // the fourth and last sample's size
	binary.BigEndian.PutUint32(data[last:], binary.BigEndian.Uint32(data[last:])+300)
	digest := essenceOf(t, data)
	if digest.ExtentVersion != "mp4-samples-v4" {
		t.Fatalf("extent = %q", digest.ExtentVersion)
	}
	changed := slices.Clone(data)
	changed[len(changed)-390] ^= 0xFF // ten bytes into the free atom's payload
	if !essenceOf(t, changed).Equal(digest) {
		t.Error("bytes behind the mdat are in the digest")
	}
	plan, err := mustParseBytes(t, data).Edit().Set("TITLE", "A title").Prepare(wl.WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	if d := essenceOf(t, applyToBytes(t, data, plan)); !d.Equal(digest) {
		t.Errorf("digest after a tag edit %s, want %s", d, digest)
	}
}

// A long track's stsz is read in pieces: the allocation limit bounds a piece, not the table.
func TestMP4EssenceUnderASmallAllocationLimit(t *testing.T) {
	// 2,000 chunks of 50 samples, the samples of a chunk 1, 2 or 3 bytes each: 400,000 bytes of
	// sample sizes, no two blocks of them alike.
	sizes := make([][]int, 2000)
	offs := make([]uint32, len(sizes))
	total := 0
	start := len(testfile.MP4Ftyp()) + 8
	for i := range sizes {
		sizes[i] = slices.Repeat([]int{1 + i%3}, 50)
		offs[i] = uint32(start + total)
		total += 50 * (1 + i%3)
	}
	moov := testfile.MP4Atom("moov", testfile.MP4Trak("soun", 1, slices.Concat(testfile.MP4StsdAudio(), testfile.MP4SampleTables(offs, sizes))))
	data := slices.Concat(testfile.MP4Ftyp(), testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, total)), moov)
	want := essenceOf(t, data)
	// A limit that is no multiple of an entry's four bytes, too, and one the 24,000-byte stsc is
	// over while the chunk offsets fit.
	for _, limit := range []int64{64 << 10, 50001, 16 << 10} {
		doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: limit, MaxDepth: 64, MaxElements: 100000}))
		if err != nil {
			t.Fatalf("parse under a limit of %d: %v", limit, err)
		}
		got, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(data)))
		if err != nil || !got.Equal(want) || got.ExtentVersion != "mp4-samples-v4" {
			t.Errorf("limit %d: digest %s (%v), want %s", limit, got, err, want)
		}
		if b := doc.Properties().Tracks[0].Bitrate; b != total*8 {
			t.Errorf("limit %d: bitrate = %d, want %d: every sample counted once", limit, b, total*8)
		}
	}
	// The essence is the whole mdat payload and no byte more.
	for _, at := range []int{start + total - 1, start + total} {
		changed := slices.Clone(data)
		changed[at] ^= 0xFF
		if same := essenceOf(t, changed).Equal(want); same != (at == start+total) {
			t.Errorf("a change at %d of %d: digest equal = %v", at-start, total, same)
		}
	}
}

// A sound trak that is no child of moov is not the movie's: its tables are not read, and the
// tracks that are still give the essence.
func TestMP4EssenceLeavesNestedTraksAlone(t *testing.T) {
	nested := testfile.MP4Trak("soun", 9, slices.Concat(testfile.MP4StsdAudio(), testfile.MP4SampleTables([]uint32{40}, [][]int{{8}})))
	data := mp4AV(0x11, nil, testfile.MP4Atom("udta", nested))
	if d := essenceOf(t, data); d.ExtentVersion != "mp4-samples-v4" || !d.Equal(essenceOf(t, mp4AV(0x11, nil))) {
		t.Errorf("digest %s, want the file's without the nested trak", d)
	}
}

// ffmpeg's frame-counted audio in QuickTime, as it writes it and with the stsz size set to 1 as
// QuickTime does: the digest is the same, the bytes are the ones ffmpeg's demuxer hands out, and
// ffmpeg still decodes the rewritten file to the same samples.
func TestMP4DifferentialFrameCountedAudio(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()
	packetBytes := func(path string) int {
		out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error", "-select_streams", "a:0",
			"-show_entries", "packet=size", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatalf("ffprobe %s: %v", path, err)
		}
		total := 0
		for line := range strings.FieldsSeq(string(out)) {
			n, err := strconv.Atoi(strings.TrimRight(line, ","))
			if err != nil {
				t.Fatalf("ffprobe packet size %q: %v", line, err)
			}
			total += n
		}
		return total
	}
	for _, codec := range []string{"pcm_s16le", "pcm_s24be", "pcm_f32be", "pcm_mulaw", "adpcm_ima_qt"} {
		t.Run(codec, func(t *testing.T) {
			path := ffmpegSine(t, filepath.Join(dir, codec+".mov"), 2, 8000, "-c:a", codec)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := essenceOf(t, data)
			stsz := bytes.Index(data, []byte("stsz"))
			if want.ExtentVersion != "mp4-samples-v4" || stsz < 0 || binary.BigEndian.Uint32(data[stsz+8:]) == 0 {
				t.Fatalf("extent %q, stsz at %d: the test needs a track with one sample size", want.ExtentVersion, stsz)
			}
			tr := mustParseBytes(t, data).Properties().Tracks[0]
			covered := int(int64(tr.Bitrate) * tr.Duration.Milliseconds() / 8000)
			if got := packetBytes(path); got == 0 || covered < got*99/100 || covered > got*101/100 {
				t.Errorf("the track's samples span %d bytes, ffprobe's packets %d", covered, got)
			}

			// ffmpeg muxes ADPCM as timed packets, which the stsz sizes as it stands.
			stts := bytes.Index(data, []byte("stts"))
			if counted := binary.BigEndian.Uint32(data[stts+8:]) == 1 && binary.BigEndian.Uint32(data[stts+16:]) == 1; counted != (codec != "adpcm_ima_qt") {
				t.Fatalf("frame-counted = %v", counted)
			} else if !counted {
				return
			}
			binary.BigEndian.PutUint32(data[stsz+8:], 1)
			one := filepath.Join(dir, codec+"-one.mov")
			if err := os.WriteFile(one, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if d := essenceOf(t, data); !d.Equal(want) {
				t.Errorf("digest with an stsz size of 1: %s, want %s", d, want)
			}
			if a, b := packetBytes(one), packetBytes(path); a != b {
				t.Errorf("ffprobe reads %d packet bytes with an stsz size of 1, %d as written", a, b)
			}
		})
	}
}

// ffmpeg's own muxing: the audio packets of one encode beside a picture, beside another and alone
// hash the same, and the bitrate is the audio stream's, as ffprobe computes it.
func TestMP4DifferentialAudioBesideVideo(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()
	first := ffmpegEncode(t, filepath.Join(dir, "first.mp4"), "-f", "lavfi", "-i", "testsrc=size=64x64:rate=10:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:v", "mpeg4", "-c:a", "aac")
	other := ffmpegEncode(t, filepath.Join(dir, "other.mp4"), "-f", "lavfi", "-i", "color=c=red:size=96x96:rate=5:duration=2",
		"-i", first, "-map", "0:v", "-map", "1:a", "-c:v", "mpeg4", "-c:a", "copy")
	alone := ffmpegEncode(t, filepath.Join(dir, "alone.m4a"), "-i", first, "-map", "0:a", "-c:a", "copy")
	hash := func(path string) wl.AudioDigest {
		d, err := mustParseFile(t, path).HashAudioEssence(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	want := hash(first)
	if want.ExtentVersion != "mp4-samples-v4" {
		t.Fatalf("extent = %q", want.ExtentVersion)
	}
	for _, path := range []string{other, alone} {
		if d := hash(path); !d.Equal(want) {
			t.Errorf("%s: digest %s, want %s", filepath.Base(path), d, want)
		}
		probe := ffprobeStream(t, path)
		got := mustParseFile(t, path).Properties().Tracks[0].Bitrate
		if probe.BitRate == 0 || math.Abs(float64(got-probe.BitRate)) > 0.03*float64(probe.BitRate) {
			t.Errorf("%s: bitrate %d, ffprobe's audio stream %d", filepath.Base(path), got, probe.BitRate)
		}
	}
}
