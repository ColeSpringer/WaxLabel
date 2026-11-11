package waxlabel_test

import (
	"os"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
)

// A native key the canonical vocabulary cannot represent is preserved on disk but never reaches the
// tag set, so without a warning it is absent from dump, lint and diff while copy reports a lossless
// carry. Every format that holds string keys must warn.
func TestUnprojectableKeyReportedEverywhere(t *testing.T) {
	for _, tc := range []struct {
		name string
		data func(*testing.T) []byte
	}{
		{"vorbis comment", func(t *testing.T) []byte {
			return testfile.TaggedFLAC("ref", "MOOD~X=calm", "TITLE=Song")
		}},
		{"apev2 item", func(t *testing.T) []byte {
			return wavPackWithAPE(t, [2]string{"MOOD~X", "calm"}, [2]string{"Title", "Song"})
		}},
		{"id3 txxx description", func(t *testing.T) []byte {
			return mp3WithFrames(t, txxxFrame(4, "MOOD~X", "calm"), testfile.TextFrame(4, "TIT2", "Song"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParseBytes(t, tc.data(t))
			if !hasWarning(doc, wl.WarnInvalidTagKey) {
				t.Fatalf("an unprojectable key went unreported; warnings = %v", doc.Warnings())
			}
			if !lintHasCode(doc, "invalid-tag-key") {
				t.Errorf("lint did not promote the warning: %v", doc.Lint())
			}
			var named bool
			for _, w := range doc.Warnings() {
				if w.Code == wl.WarnInvalidTagKey && strings.Contains(w.Message, "MOOD~X") {
					named = true
				}
			}
			if !named {
				t.Errorf("the warning does not name the key: %v", doc.Warnings())
			}
			// The value the file can represent still projects: the warning is scoped to the
			// key that failed.
			if doc.Fields().Title != "Song" {
				t.Errorf("title = %q, want Song", doc.Fields().Title)
			}
		})
	}
}

// A fixture whose keys all project must not draw the warning, or it would fire on every ordinary
// file.
func TestRepresentableKeysStayQuiet(t *testing.T) {
	for _, f := range []string{"sample.flac", "sample.mp3", "sample.wv", "sample.mka", "sample.m4a", "sample.wma"} {
		data, err := os.ReadFile("../testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if doc := mustParseBytes(t, data); hasWarning(doc, wl.WarnInvalidTagKey) {
			t.Errorf("%s: reported an unprojectable key it does not have: %v", f, doc.Warnings())
		}
	}
}

// wavPackWithAPE appends an APEv2 tag holding the given items to the tagless WavPack fixture.
func wavPackWithAPE(t *testing.T, items ...[2]string) []byte {
	t.Helper()
	audio, err := os.ReadFile("../testdata/notags.wv")
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, it := range items {
		body = append(body, testfile.LE32(len(it[1]))...)
		body = append(body, testfile.LE32(0)...)
		body = append(body, it[0]...)
		body = append(body, 0)
		body = append(body, it[1]...)
	}
	size := len(body) + 32
	rec := func(flags uint32) []byte {
		b := append([]byte("APETAGEX"), testfile.LE32(2000)...)
		b = append(b, testfile.LE32(size)...)
		b = append(b, testfile.LE32(len(items))...)
		b = append(b, testfile.LE32(int(flags))...)
		return append(b, make([]byte, 8)...)
	}
	const hasHeader, isHeader = 1 << 31, 1 << 29
	out := append(audio, rec(hasHeader|isHeader)...)
	out = append(out, body...)
	return append(out, rec(hasHeader)...)
}

// An APE item that is not valid UTF-8 reads as Latin-1, and lint reports it as dump does.
func TestLintReportsInvalidTextInAPE(t *testing.T) {
	doc := mustParseBytes(t, wavPackWithAPE(t, [2]string{"Title", "caf\xe9"}))
	if !hasWarning(doc, wl.WarnInvalidText) {
		t.Fatalf("no invalid-text warning in %v", doc.Warnings())
	}
	if !lintHasCode(doc, "invalid-text") {
		t.Errorf("lint does not report invalid-text: %v", doc.Lint())
	}
}
