package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// id3v1Tag is a minimal 128-byte ID3v1 trailer.
func id3v1Tag() []byte { return testfile.ID3v1("", "", "", "", "", 0) }

// withTail returns data with the given regions appended.
func withTail(data []byte, tails ...[]byte) []byte {
	out := append([]byte(nil), data...)
	for _, tail := range tails {
		out = append(out, tail...)
	}
	return out
}

// A junk-appended rip carries the same audio-essence identity as its clean twin, so the two
// dedup-match.
func TestFLACJunkExcludedFromEssenceDigest(t *testing.T) {
	clean := readFixture(t, sampleFLAC)
	junk := withTail(clean, make([]byte, 512))

	base := essenceOf(t, clean)
	if base.ExtentVersion != "flac-frames-v2" {
		t.Errorf("ExtentVersion = %q, want flac-frames-v2", base.ExtentVersion)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"appended junk", junk},
		{"junk then ID3v1 trailer", withTail(junk, id3v1Tag())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := essenceOf(t, tc.data); !got.Equal(base) {
				t.Errorf("essence differs from the clean twin:\n  clean %s\n  got   %s", base, got)
			}
		})
	}
}

// Write side: an edit copies the junk verbatim between the audio and a kept ID3v1 trailer, a
// chained edit on the returned document keeps it too, and the written file still dedup-matches
// the clean original.
func TestFLACJunkSurvivesEdit(t *testing.T) {
	ctx := context.Background()
	clean := readFixture(t, sampleFLAC)
	junkBytes := bytes.Repeat([]byte{0x4A}, 512)
	src := withTail(clean, junkBytes, id3v1Tag())

	junkAndTagIntact := func(out []byte) bool {
		tail := out[len(out)-128-512:]
		return bytes.Equal(tail[:512], junkBytes) && bytes.Equal(tail[512:512+3], []byte("TAG"))
	}

	doc := mustParseBytes(t, src)
	plan, err := doc.Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	var w writerTo
	res, _, err := plan.Execute(ctx, wl.WriteTo(&w, wl.BytesSource(src)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := w.b

	if !junkAndTagIntact(out) {
		t.Fatalf("junk or ID3v1 trailer not preserved verbatim at the tail")
	}
	re := mustParseBytes(t, out)
	if !hasWarning(re, wl.WarnTrailingBytes) || !hasWarning(re, wl.WarnTrailingID3v1) {
		t.Errorf("re-parse lost a tail warning; got %v", re.Warnings())
	}
	if !essenceOf(t, out).Equal(essenceOf(t, clean)) {
		t.Error("edited junk file no longer dedup-matches the clean original")
	}

	// A chained edit on the returned document must carry the junk region too.
	plan2, err := res.Edit().Set(tag.Album, "Again").Prepare()
	if err != nil {
		t.Fatalf("Prepare(chained): %v", err)
	}
	var w2 writerTo
	if _, _, err := plan2.Execute(ctx, wl.WriteTo(&w2, wl.BytesSource(out))); err != nil {
		t.Fatalf("Execute(chained): %v", err)
	}
	if !junkAndTagIntact(w2.b) {
		t.Fatalf("chained edit dropped the junk or the ID3v1 trailer")
	}
}

// FLAC frame-tail walk through Parse on the real fixture: appended bytes surface as a trailing
// region, missing audio as a truncation, and a clean file as neither.
func TestFLACTailFindings(t *testing.T) {
	clean := readFixture(t, sampleFLAC)
	junk := withTail(clean, make([]byte, 512))

	for _, tc := range []struct {
		name      string
		data      []byte
		want      []wl.WarningCode
		wantNot   []wl.WarningCode
		wantInMsg string
	}{
		{
			name:    "clean fixture",
			data:    clean,
			wantNot: []wl.WarningCode{wl.WarnTrailingBytes, wl.WarnTruncatedAudio},
		},
		{
			name:      "appended junk",
			data:      junk,
			want:      []wl.WarningCode{wl.WarnTrailingBytes},
			wantNot:   []wl.WarningCode{wl.WarnTruncatedAudio},
			wantInMsg: "512 byte(s) after the FLAC stream",
		},
		{
			name:      "junk then ID3v1 trailer",
			data:      withTail(junk, id3v1Tag()),
			want:      []wl.WarningCode{wl.WarnTrailingBytes, wl.WarnTrailingID3v1},
			wantNot:   []wl.WarningCode{wl.WarnTruncatedAudio},
			wantInMsg: "512 byte(s) after the FLAC stream",
		},
		{
			name:    "truncated audio",
			data:    clean[:len(clean)-5000],
			want:    []wl.WarningCode{wl.WarnTruncatedAudio},
			wantNot: []wl.WarningCode{wl.WarnTrailingBytes},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParseBytes(t, tc.data)
			for _, code := range tc.want {
				if !hasWarning(doc, code) {
					t.Errorf("missing %v warning; got %v", code, doc.Warnings())
				}
			}
			for _, code := range tc.wantNot {
				if hasWarning(doc, code) {
					t.Errorf("unexpected %v warning; got %v", code, doc.Warnings())
				}
			}
			if tc.wantInMsg == "" {
				return
			}
			found := false
			for _, w := range doc.Warnings() {
				found = found || strings.Contains(w.Message, tc.wantInMsg)
			}
			if !found {
				t.Errorf("no warning message contains %q; got %v", tc.wantInMsg, doc.Warnings())
			}
		})
	}
}

func TestFLACJunkInNativeView(t *testing.T) {
	doc := mustParseBytes(t, withTail(readFixture(t, sampleFLAC), make([]byte, 512)))
	for _, e := range doc.Native().Describe() {
		if e.Kind == "trailing bytes" && e.Size == 512 {
			return
		}
	}
	t.Errorf("native view lacks a 512-byte trailing bytes entry: %+v", doc.Native().Describe())
}

// Zeros appended past the 4 MiB the frame walk's window reaches leave its end the last frame's: the
// walk measures the run back from the tags first, so the zeros are reported, kept out of the essence
// and the bitrate, and the digest is the clean file's.
func TestFLACZerosBeyondTheWindowCap(t *testing.T) {
	clean := readFixture(t, sampleFLAC)
	want := mustParseBytes(t, clean)
	for _, n := range []int{5 << 20, 8 << 20} {
		t.Run(fmt.Sprintf("%d MiB", n>>20), func(t *testing.T) {
			data := withTail(clean, make([]byte, n), id3v1Tag())
			doc := mustParseBytes(t, data)
			if got, w := doc.Properties().First().Bitrate, want.Properties().First().Bitrate; got != w {
				t.Errorf("bitrate = %d, want the clean file's %d", got, w)
			}
			if w, ok := docWarning(doc, wl.WarnTrailingBytes); !ok || !strings.HasPrefix(w.Message, fmt.Sprintf("%d byte(s) after the FLAC stream", n)) {
				t.Errorf("trailing-bytes warning = %q (present %v), want it to count the zeros", w.Message, ok)
			}
			if hasWarning(doc, wl.WarnTruncatedAudio) {
				t.Errorf("a whole stream reads as truncated: %v", doc.Warnings())
			}
			if !essenceOf(t, data).Equal(essenceOf(t, clean)) {
				t.Error("the zeros changed the digest")
			}
		})
	}
}

// A run of zeros at least as long as the scan bound, the allocation limit, has no start the walk can
// find: the zeros stay in the essence, which a warning says.
func TestFLACZeroRunPastTheScanBound(t *testing.T) {
	clean := readFixture(t, sampleFLAC)
	data := withTail(clean, make([]byte, 2<<20))
	doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: 1 << 20}))
	if err != nil {
		t.Fatal(err)
	}
	w, ok := docWarning(doc, wl.WarnTrailingBytes)
	if want := "1048576 byte(s) at the end of the FLAC stream are zeros in a run at least as long as the 1048576-byte scan bound"; !ok || !strings.HasPrefix(w.Message, want) {
		t.Errorf("trailing-bytes warning = %q (present %v), want it to begin %q", w.Message, ok, want)
	}
	if got, clean := doc.Properties().First().Bitrate, mustParseBytes(t, clean).Properties().First().Bitrate; got <= clean {
		t.Errorf("bitrate = %d, want the zeros counted over the clean file's %d", got, clean)
	}
}

// lastFLACFrame is where the last frame of notags.flac or sample.flac opens: frame 9, its header
// coding the short last block.
func lastFLACFrame(t *testing.T, clean []byte) int {
	t.Helper()
	i := bytes.LastIndex(clean, []byte{0xFF, 0xF8, 0x79, 0x88, 0x09})
	if i < 0 {
		t.Fatal("setup: no last frame header")
	}
	return i
}

// checksumZeros lists where, counted from the end of clean, the checksum of clean's last frame run on
// through tail comes to zero.
func checksumZeros(t *testing.T, clean, tail []byte) []int {
	t.Helper()
	var at []int
	crc := testfile.FLACFrameCRC(0, clean[lastFLACFrame(t, clean):])
	for i, b := range tail {
		if crc = testfile.FLACFrameCRC(crc, []byte{b}); crc == 0 {
			at = append(at, i+1)
		}
	}
	return at
}

// Tails after the FLAC frames, every tag in them legacy. Each parses with the clean file's tags and
// digest and its APE tag in the family view; a strip under the essence check leaves the clean file
// and the junk, and an edit under it keeps the tail; the digest is the clean file's throughout. A tag
// the frames run on past ends the audio, the frames after it kept in the essence, and one the frame
// checksum would place the end inside is read: a validated tag wins over the end the walk claims.
func TestFLACTailShapes(t *testing.T) {
	clean := readFixture(t, notagsFLAC)
	last := lastFLACFrame(t, clean)
	between := headedAPETag(t, "Title", "Between Frames")
	behind := headedAPETag(t, "Title", "Behind Junk")
	// 42 bytes into the tag is inside its first item's key, where no zero runs on.
	steered := testfile.FLACSteeringJunk(clean[last:], behind, 42)
	if got := checksumZeros(t, clean, slices.Concat(steered, behind, trailerJunk(300))); !slices.Equal(got, []int{len(steered) + 42}) {
		t.Fatalf("setup: the checksum comes to zero %v bytes past the frames, want only inside the tag", got)
	}
	if got := checksumZeros(t, clean, slices.Concat(steered, trailerJunk(300))); len(got) > 0 {
		t.Fatalf("setup: without the tag the checksum comes to zero %v bytes past the frames", got)
	}
	stuck := trailerJunk(300)
	for _, c := range []struct {
		name   string
		data   []byte   // the file
		strip  []byte   // what a strip leaves
		native []string // the native view's entries past the metadata blocks
		warn   []string // how its trailing-bytes warnings begin, in order
		title  string   // the APE tag's title in the family view
		ops    []string // what a strip reports doing with the tags; an edit reports their preservation
	}{
		{"a stacked tail with junk", slices.Concat(clean, apeTag(map[string]string{"Title": "Stacked"}), stuck,
			testfile.Lyrics3v2([2]string{"LYR", "words"}), stackedV1()), slices.Concat(clean, stuck),
			[]string{"APEv2", "trailing bytes", "Lyrics3v2", "ID3v1"}, []string{"300 byte(s) after the FLAC stream belong to no tag"}, "Stacked",
			[]string{"trailing APEv2 strip", "trailing Lyrics3v2 strip", "trailing ID3v1 strip (2 stacked tags)"}},
		{"an APE tag between frames", slices.Concat(clean[:last], between, clean[last:]), clean,
			[]string{"APEv2", "FLAC frames"}, []string{fmt.Sprintf("%d byte(s) after an APE tag are audio", len(clean)-last)}, "Between Frames",
			[]string{"trailing APEv2 strip"}},
		{"a tag after junk the frame checksum would end inside", slices.Concat(clean, steered, behind, stuck),
			slices.Concat(clean, steered, stuck), []string{"trailing bytes", "APEv2", "trailing bytes"},
			[]string{"8 byte(s) after the FLAC stream belong to no tag", "300 byte(s) after the FLAC stream belong to no tag"}, "Behind Junk",
			[]string{"trailing APEv2 strip"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			essence := essenceOf(t, clean)
			doc := mustParseBytes(t, c.data)
			var native []string
			for _, e := range doc.Native().Describe() {
				switch e.Kind {
				case "STREAMINFO", "VORBIS_COMMENT", "PADDING":
				default:
					native = append(native, e.Kind)
				}
			}
			if !slices.Equal(native, c.native) {
				t.Errorf("native view = %q, want %q", native, c.native)
			}
			var warns []string
			for _, w := range doc.Warnings() {
				if w.Code == wl.WarnTrailingBytes {
					warns = append(warns, w.Message)
				}
			}
			if len(warns) != len(c.warn) {
				t.Errorf("trailing-bytes warnings = %q, want %d", warns, len(c.warn))
			} else {
				for i, want := range c.warn {
					if !strings.HasPrefix(warns[i], want) {
						t.Errorf("trailing-bytes warning %q does not begin %q", warns[i], want)
					}
				}
			}
			if !hasWarning(doc, wl.WarnLegacyAPE) || hasWarning(doc, wl.WarnTruncatedAudio) {
				t.Errorf("warnings = %v, want legacy-ape and no truncation", doc.Warnings())
			}
			var titles []string
			for _, f := range doc.Families() {
				if f.Family == wl.FamilyAPEv2 && f.Key == tag.Title {
					titles = append(titles, f.Values...)
				}
			}
			if !slices.Equal(titles, []string{c.title}) {
				t.Errorf("APE titles in the family view = %q, want %q", titles, c.title)
			}
			if !doc.Tags().Equal(mustParseBytes(t, clean).Tags()) || !essenceOf(t, c.data).Equal(essence) {
				t.Error("the tail changed the tags or the digest")
			}
			for name, p := range map[string]struct {
				opts []wl.WriteOption
				edit func(*wl.Editor)
				want func(out []byte) bool
				verb string
			}{
				"strip": {[]wl.WriteOption{wl.WithLegacyPolicy(wl.LegacyStrip), wl.WithVerifyEssence()}, func(*wl.Editor) {},
					func(out []byte) bool { return bytes.Equal(out, c.strip) }, "strip"},
				"edit": {[]wl.WriteOption{wl.WithVerifyEssence()}, func(e *wl.Editor) { e.Set(tag.Artist, "Edited") },
					func(out []byte) bool { return bytes.HasSuffix(out, c.data[flacAudioStart(t, clean):]) }, "preservation"},
			} {
				e := doc.Edit()
				p.edit(e)
				plan, err := e.Prepare(p.opts...)
				if err != nil {
					t.Fatalf("%s: prepare: %v", name, err)
				}
				for _, op := range c.ops {
					if op = strings.Replace(op, " strip", " "+p.verb, 1); !slices.Contains(plan.Report().Operations, op) {
						t.Errorf("%s operations = %q, want %q among them", name, plan.Report().Operations, op)
					}
				}
				var w writerTo
				if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(c.data))); err != nil {
					t.Errorf("%s under the essence check: %v", name, err)
					continue
				}
				if !p.want(w.b) {
					t.Errorf("%s did not leave the bytes expected", name)
				}
				if !essenceOf(t, w.b).Equal(essence) {
					t.Errorf("%s changed the digest", name)
				}
			}
		})
	}
}

// flacAudioStart is where the frames of clean open: its first frame header.
func flacAudioStart(t *testing.T, clean []byte) int {
	t.Helper()
	i := bytes.Index(clean, []byte{0xFF, 0xF8, 0x59, 0x88, 0x00})
	if i < 0 {
		t.Fatal("setup: no first frame header")
	}
	return i
}

// A read the frame walk makes inside the file that fails is an I/O fault: the parse fails with it
// rather than leave the audio end unknown, which would give the digest of another layout.
func TestFLACUnreadableTailFailsTheParse(t *testing.T) {
	clean := readFixture(t, sampleFLAC)
	src := &testfile.Faulty{Src: wl.BytesSource(clean), Fails: testfile.Covering(int64(lastFLACFrame(t, clean)))}
	if _, err := wl.Parse(context.Background(), src); !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want the read's own failure", err)
	}
}

// When a tag sits before the tags at the end of the file, the frame walk is asked again over the file
// as a strip leaves it, and its own warnings are that asking's: a stream cut short behind zeros past
// the window cap and a tag reads as truncated, a zero run longer than the scan bound behind a tag is
// warned as such, and a tag inside the last frame, which the asking over the file takes for a cut,
// warns nothing, the frame whole without it.
func TestFLACWalkWarningsComeFromTheLastAsking(t *testing.T) {
	clean := readFixture(t, notagsFLAC)
	last := lastFLACFrame(t, clean)
	split := headedAPETag(t, "Title", "Split")
	zeros, after := make([]byte, 5<<20), make([]byte, 1<<10)
	for _, c := range []struct {
		name  string
		data  []byte
		limit int64
		want  []string // how the walk's warnings begin, in order
	}{
		{"a cut stream behind zeros and a tag", slices.Concat(clean[:last], zeros, split, after), 0,
			[]string{"STREAMINFO declares more audio than the file holds"}},
		{"zeros past the scan bound behind a tag", slices.Concat(clean, zeros, split, after), 4 << 20,
			[]string{"4194304 byte(s) at the end of the FLAC stream are zeros in a run at least as long as the 4194304-byte scan bound"}},
		{"a tag inside the last frame", slices.Concat(clean[:last+300], split, clean[last+300:]), 0, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, err := wl.Parse(context.Background(), wl.BytesSource(c.data), wl.WithLimits(wl.Limits{MaxAllocBytes: c.limit}))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, w := range doc.Warnings() {
				if w.Code == wl.WarnTruncatedAudio || w.Code == wl.WarnTrailingBytes && strings.Contains(w.Message, "scan bound") {
					got = append(got, w.Message)
				}
			}
			if len(got) != len(c.want) {
				t.Fatalf("the walk's warnings = %q, want %d", got, len(c.want))
			}
			for i, want := range c.want {
				if !strings.HasPrefix(got[i], want) {
					t.Errorf("warning %q does not begin %q", got[i], want)
				}
			}
			if !hasFamily(doc, wl.FamilyAPEv2, tag.Title) {
				t.Error("the tag before the end was not read")
			}
		})
	}
}
