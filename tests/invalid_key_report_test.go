package waxlabel_test

import (
	"os"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// TestUnprojectableKeyReportedEverywhere: a native key the canonical vocabulary cannot
// represent is preserved on disk but never reaches the tag set, so without a warning it is
// absent from dump, lint and diff while copy reports a clean lossless carry. Every format
// that holds string keys must say so, not just the one that always did.
func TestUnprojectableKeyReportedEverywhere(t *testing.T) {
	for _, tc := range []struct {
		name string
		data func(*testing.T) []byte
	}{
		{"vorbis comment", func(t *testing.T) []byte {
			return flacWithVendor("ref", "MOOD~X=calm", "TITLE=Song")
		}},
		{"apev2 item", func(t *testing.T) []byte {
			return wavPackWithAPE(t, [2]string{"MOOD~X", "calm"}, [2]string{"Title", "Song"})
		}},
		{"id3 txxx description", func(t *testing.T) []byte {
			return mp3WithFrames(t, txxxFrame(4, "MOOD~X", "calm"), textFrame(4, "TIT2", "Song"))
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
			// The value the file CAN represent still projects, so the warning is scoped to
			// the key that failed rather than shutting the whole projection down.
			if doc.Fields().Title != "Song" {
				t.Errorf("title = %q, want Song", doc.Fields().Title)
			}
		})
	}
}

// TestRepresentableKeysStayQuiet is the negative: a fixture whose keys all project must not
// draw the warning, or it would fire on every ordinary file.
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
		body = append(body, apeLE32(len(it[1]))...)
		body = append(body, apeLE32(0)...)
		body = append(body, it[0]...)
		body = append(body, 0)
		body = append(body, it[1]...)
	}
	size := len(body) + 32
	rec := func(flags uint32) []byte {
		b := append([]byte("APETAGEX"), apeLE32(2000)...)
		b = append(b, apeLE32(size)...)
		b = append(b, apeLE32(len(items))...)
		b = append(b, apeLE32(int(flags))...)
		return append(b, make([]byte, 8)...)
	}
	const hasHeader, isHeader = 1 << 31, 1 << 29
	out := append(audio, rec(hasHeader|isHeader)...)
	out = append(out, body...)
	return append(out, rec(hasHeader)...)
}

func apeLE32(n int) []byte {
	return []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
}
