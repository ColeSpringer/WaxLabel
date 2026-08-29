package waxlabel_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// id3v1Tag is a minimal 128-byte ID3v1 trailer.
func id3v1Tag() []byte {
	return append([]byte("TAG"), make([]byte, 125)...)
}

// withTail returns data with the given regions appended.
func withTail(data []byte, tails ...[]byte) []byte {
	out := append([]byte(nil), data...)
	for _, tail := range tails {
		out = append(out, tail...)
	}
	return out
}

// TestFLACJunkExcludedFromEssenceDigest checks the point of carving junk out
// of the audio region: a junk-appended rip carries the same audio-essence
// identity as its clean twin, so the two dedup-match. Excluding the junk
// changed the byte extent for such files, which is why the extent name is
// flac-frames-v2.
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

// TestFLACJunkSurvivesEdit checks the write side of the carve-out: an edit
// copies the junk verbatim between the audio and a kept ID3v1 trailer, a
// chained edit on the returned document keeps it too, and the written file
// still dedup-matches the clean original.
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

	// A chained edit on the returned document (no re-parse) must carry the
	// junk region too.
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

// TestFLACTailFindings drives the FLAC frame-tail walk through Parse on the
// real fixture: appended bytes surface as a trailing region, missing audio as
// a truncation, and a clean file as neither. A trailing ID3v1 tag is carved
// off first, so junk wedged between the audio and the tag is still counted
// exactly.
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

// TestFLACJunkInNativeView checks that a carved trailing region shows in the
// native view like Ogg's, so dump --native accounts for every byte.
func TestFLACJunkInNativeView(t *testing.T) {
	doc := mustParseBytes(t, withTail(readFixture(t, sampleFLAC), make([]byte, 512)))
	for _, e := range doc.Native().Describe() {
		if e.Kind == "trailing bytes" && e.Size == 512 {
			return
		}
	}
	t.Errorf("native view lacks a 512-byte trailing bytes entry: %+v", doc.Native().Describe())
}
