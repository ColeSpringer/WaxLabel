package waxlabel_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// mkChapters builds n chapters one second apart, each with a distinct title.
func mkChapters(n int) []wl.Chapter {
	chs := make([]wl.Chapter, n)
	for i := range chs {
		chs[i] = wl.Chapter{Start: time.Duration(i) * time.Second, Title: "Ch"}
	}
	return chs
}

// TestVorbisChapterCapEnforced: the CHAPTERxxx convention is a 3-digit namespace, and the
// writer already numbers a 1000-entry list from 0 so it fits (CHAPTER000..CHAPTER999). One
// more needs a 4-digit key that no other reader recognizes, so it is a refused write
// (ErrUnsupportedTag) rather than a silently unreadable file. FLAC and Ogg share the
// convention and therefore the cap.
func TestVorbisChapterCapEnforced(t *testing.T) {
	for _, c := range []struct{ name, path string }{
		{"flac", sampleFLAC},
		{"ogg", sampleOgg},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseFile(t, c.path)
			if _, err := doc.Edit().SetChapters(mkChapters(1001)...).Prepare(); !errors.Is(err, waxerr.ErrUnsupportedTag) {
				t.Errorf("1001 chapters: err = %v, want ErrUnsupportedTag", err)
			}
			if _, err := doc.Edit().SetChapters(mkChapters(1000)...).Prepare(); err != nil {
				t.Errorf("1000 chapters: err = %v, want nil", err)
			}
		})
	}
}

// TestVorbisChapterCapStaysInNamespace checks the cap is set where the numbering actually
// runs out: a full 1000-chapter write must produce only 3-digit keys, which is the whole
// reason 1000 and not 999 is the limit.
func TestVorbisChapterCapStaysInNamespace(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	plan, err := mustParseBytes(t, src).Edit().SetChapters(mkChapters(1000)...).Prepare()
	if err != nil {
		t.Fatalf("1000 chapters on FLAC: %v", err)
	}
	out := applyToBytes(t, src, plan)
	re := mustParseBytes(t, out)
	if got := len(re.Chapters()); got != 1000 {
		t.Errorf("chapters after write = %d, want 1000", got)
	}
	// The comment list lives inside the VORBIS_COMMENT block body, so the written bytes are
	// where a key that outgrew the namespace would show up.
	if i := strings.Index(string(out), "CHAPTER1000"); i >= 0 {
		t.Errorf("a 1000-chapter list produced a 4-digit CHAPTER1000 key at offset %d", i)
	}
	if !strings.Contains(string(out), "CHAPTER000=") || !strings.Contains(string(out), "CHAPTER999=") {
		t.Error("a 1000-chapter list should number CHAPTER000..CHAPTER999")
	}
}
