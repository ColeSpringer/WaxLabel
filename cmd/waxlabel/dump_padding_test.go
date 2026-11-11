package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// TestDumpPaddingMatchesPlan: dump paddingBytes must match plan padding for same-length edit.
func TestDumpPaddingMatchesPlan(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sample.flac", "sample.mp3", "sample.aac", "sample.m4a"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := copyFixture(t, td(name))
			// Seed via waxlabel so padding reflects this codec, not the original muxer.
			if _, errb, code := runCLI(t, "set", f, "--set", "ARTIST=AAAAAA"); code != 0 {
				t.Fatalf("seeding write: exit = %d\n%s", code, errb)
			}
			jd := dumpJSON(t, f)
			if jd.Properties == nil || jd.Properties.PaddingBytes <= 0 {
				t.Fatalf("dump reported no paddingBytes for %s: %+v", name, jd.Properties)
			}
			// Same-length edit reuses metadata region; leftover padding matches dump.
			out, _, code := runCLI(t, "--json", "plan", f, "--set", "ARTIST=BBBBBB")
			if code != 0 {
				t.Fatalf("plan exit = %d\n%s", code, out)
			}
			jr := decodeJSONList[jsonReport](t, out)
			if len(jr) != 1 {
				t.Fatalf("want one plan report, got %d: %s", len(jr), out)
			}
			if jd.Properties.PaddingBytes != jr[0].PaddingAfter {
				t.Errorf("dump paddingBytes = %d, plan padding = %d; they describe the same region",
					jd.Properties.PaddingBytes, jr[0].PaddingAfter)
			}
		})
	}
}

// TestDumpPaddingOgg: dump and plan agree on Ogg comment padding (not Ogg FLAC PADDING block).
func TestDumpPaddingOgg(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sample.opus", "sample.ogg", "sample.oga"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := copyFixture(t, td(name))
			jd := dumpJSON(t, f)
			out, _, code := runCLI(t, "--json", "plan", f, "--set", "ARTIST=Padded")
			if code != 0 {
				t.Fatalf("plan exit = %d\n%s", code, out)
			}
			jr := decodeJSONList[jsonReport](t, out)
			if len(jr) != 1 {
				t.Fatalf("want one plan report, got %d: %s", len(jr), out)
			}
			var got int64
			if jd.Properties != nil {
				got = jd.Properties.PaddingBytes
			}
			if got != jr[0].PaddingAfter {
				t.Errorf("dump paddingBytes = %d, plan padding = %d", got, jr[0].PaddingAfter)
			}
		})
	}
}

// TestDumpPaddingAbsentWithoutARegion: files with no padding region omit paddingBytes. (sample.aiff
// is not one: its ID3 chunk holds padding, which TestUncontrollablePaddingIsLabelledPreserved covers.)
func TestDumpPaddingAbsentWithoutARegion(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sample.wav", "notags.aiff", "sample.mka", "sample.wv", "sample.ape"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, _, code := runCLI(t, "--json", "dump", td(name))
			if code != 0 {
				t.Fatalf("dump exit = %d\n%s", code, out)
			}
			if strings.Contains(out, "paddingBytes") {
				t.Errorf("%s has no padding region; dump should omit paddingBytes:\n%s", name, out)
			}
		})
	}
}

// TestNoPaddingOpLineNamesTheFilesPadding: the operation line of a padding-only edit starts from
// the padding dump reports, not from the region with its block header.
func TestNoPaddingOpLineNamesTheFilesPadding(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, td("notags.flac"))
	if got := dumpJSON(t, f).Properties.PaddingBytes; got != 8192 {
		t.Fatalf("the fixture holds %d bytes of padding, want 8192", got)
	}
	out, _, code := runCLI(t, "plan", f, "--no-padding")
	if code != 0 || !strings.Contains(out, "padding 8192 -> 0\n") {
		t.Errorf("exit %d, want the line \"padding 8192 -> 0\":\n%s", code, out)
	}
}

// TestNoOpPlanReportsTheFilesPadding: a plan with nothing to do reports the padding the file keeps,
// as dump does.
func TestNoOpPlanReportsTheFilesPadding(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sample.flac", "sample.mp3", "sample.aac", "sample.m4a"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := copyFixture(t, td(name))
			if _, errb, code := runCLI(t, "set", f, "--set", "ARTIST=AAAAAA"); code != 0 {
				t.Fatalf("seeding write: exit = %d\n%s", code, errb)
			}
			want := dumpJSON(t, f).Properties.PaddingBytes
			out, _, code := runCLI(t, "--json", "plan", f)
			jr := decodeJSONList[jsonReport](t, out)
			if code != 0 || len(jr) != 1 || !jr[0].NoOp {
				t.Fatalf("exit %d, want one no-op report: %s", code, out)
			}
			if want <= 0 || jr[0].PaddingAfter != want {
				t.Errorf("no-op plan paddingAfter = %d, dump paddingBytes = %d", jr[0].PaddingAfter, want)
			}
		})
	}
}

// paddedOpus is an Opus stream whose comment header ends in pad bytes of padding.
func paddedOpus(t *testing.T, pad int) string {
	t.Helper()
	const serial = 0x4F505553
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xBB, 0, 0, 0, 0, 0}
	tags := slices.Concat([]byte("OpusTags"), testfile.VorbisComments("libopus 1.4", "TITLE=Song"), make([]byte, pad))
	return writeTempFile(t, "padded.opus", slices.Concat(
		testfile.OggPage(0x02, 0, serial, 0, head),
		testfile.OggPage(0, 0, serial, 1, tags),
		testfile.OggPage(0, 960, serial, 2, []byte("AUDIOPKT!!")),
	))
}

// TestUncontrollablePaddingIsLabelledPreserved: padding no option sizes (an Opus comment header's,
// a WAV or AIFF ID3 chunk's) shows in dump and in the plan as preserved, without the hint to the
// flags that would not change it.
func TestUncontrollablePaddingIsLabelledPreserved(t *testing.T) {
	t.Parallel()
	id3 := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Song"), make([]byte, 300))
	for name, f := range map[string]string{
		"Opus": paddedOpus(t, 300),
		"WAV":  writeTempFile(t, "padded.wav", testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVChunk("id3 ", id3), testfile.WAVData(4000))),
		"AIFF": writeTempFile(t, "padded.aiff", testfile.AIFF("AIFF", testfile.COMM(2, 1000, 16, 44100),
			testfile.SSNDOffset(0, nil, make([]byte, 4000)), testfile.AIFFChunk("ID3 ", id3))),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := dumpJSON(t, f).Properties.PaddingBytes; got != 300 {
				t.Errorf("dump paddingBytes = %d, want 300", got)
			}
			// A title of the same length leaves the padding as it was.
			out, _, code := runCLI(t, "plan", f, "--set", "TITLE=Tune")
			if code != 0 || !strings.Contains(out, "  padding: 300 B (preserved)\n") || strings.Contains(out, "--padding") {
				t.Errorf("exit %d, want \"padding: 300 B (preserved)\" and no flag hint:\n%s", code, out)
			}
			out, _, _ = runCLI(t, "--json", "plan", f, "--set", "TITLE=Tune")
			if jr := decodeJSONList[jsonReport](t, out); len(jr) != 1 || jr[0].PaddingAfter != 300 {
				t.Errorf("plan paddingAfter is not 300: %s", out)
			}
		})
	}
}
