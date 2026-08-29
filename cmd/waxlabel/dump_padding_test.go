package main

import (
	"strings"
	"testing"
)

// TestDumpPaddingMatchesPlan: dump's paddingBytes is the read-side view of the region a
// write grows into, so it must equal the padding a plan reports for an in-place edit of the
// same file - on every format that reserves one, not only the one whose padding happens to
// be a describable block. Default padding options only: --padding N reports the request
// rather than what the file holds.
func TestDumpPaddingMatchesPlan(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sample.flac", "sample.mp3", "sample.aac", "sample.m4a"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := copyFixture(t, td(name))
			// Write once through waxlabel so the file carries the padding this codec lays
			// down, rather than whatever the fixture's original muxer left.
			if _, errb, code := runCLI(t, "set", f, "--set", "ARTIST=AAAAAA"); code != 0 {
				t.Fatalf("seeding write: exit = %d\n%s", code, errb)
			}
			jd := dumpJSON(t, f)
			if jd.Properties == nil || jd.Properties.PaddingBytes <= 0 {
				t.Fatalf("dump reported no paddingBytes for %s: %+v", name, jd.Properties)
			}
			// The planned edit is the same byte length as the seeded one, so it consumes
			// exactly the metadata region already on disk and the padding left over is the
			// padding the file holds now. A shorter or longer value would legitimately
			// report a different figure, and a no-op plan reports none at all.
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

// TestDumpPaddingOgg: Ogg reports the comment padding it round-trips, which is what plan
// reports too. sample.opus carries none, so the pair is 0 and the field stays omitted;
// what matters is that the two surfaces agree and neither invents a figure. An Ogg FLAC
// PADDING block is deliberately excluded - every rewrite drops it - so it must not appear
// here either, even though dump --native lists such a block.
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

// TestDumpPaddingAbsentWithoutARegion: a format that reserves no padding at all must keep
// omitting the field, so 0 never gets rendered as "no slack" for a format that simply does
// not model one.
func TestDumpPaddingAbsentWithoutARegion(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"sample.wav", "sample.aiff", "sample.mka", "sample.wv", "sample.ape"} {
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
