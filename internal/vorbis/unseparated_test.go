package vorbis

import (
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// commentListWithRaw renders a comment list by hand so an entry with no "=" can be placed
// in it. RenderCommentList would compose one from a Name and a Value, which is exactly the
// composition the Unseparated flag exists to avoid.
func commentListWithRaw(vendor string, entries ...string) []byte {
	le := func(n int) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(n))
		return b
	}
	out := append(le(len(vendor)), vendor...)
	out = append(out, le(len(entries))...)
	for _, e := range entries {
		out = append(out, le(len(e))...)
		out = append(out, e...)
	}
	return out
}

// TestUnseparatedEntryRoundTripsVerbatim: an entry with no "=" is well framed, so dropping
// it destroyed bytes the walk had located perfectly well. It must survive parse and render
// byte for byte, which is what stops the next rewrite from erasing it.
func TestUnseparatedEntryRoundTripsVerbatim(t *testing.T) {
	body := commentListWithRaw("vend", "TITLE=Song", "noequalshere", "ARTIST=Band")
	vendor, cs, n, err := ParseCommentList(body, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(body)) {
		t.Errorf("consumed = %d, want %d", n, len(body))
	}
	if len(cs) != 3 {
		t.Fatalf("got %d comments, want 3: %+v", len(cs), cs)
	}
	bad := cs[1]
	if !bad.Unseparated || bad.Name != "" || bad.Value != "noequalshere" {
		t.Errorf("unseparated entry = %+v, want the raw bytes under an empty name", bad)
	}
	if out := RenderCommentList(vendor, cs); !slices.Equal(out, body) {
		t.Errorf("re-render changed the list:\n got %q\nwant %q", out, body)
	}
}

// TestUnseparatedEntryStaysOutOfEveryProjection: the entry has no name, so nothing can key
// off it. Each projector skips it explicitly rather than relying on an empty string
// happening to miss every predicate.
func TestUnseparatedEntryStaysOutOfEveryProjection(t *testing.T) {
	cs := []Comment{
		{Name: "TITLE", Value: "Song"},
		{Value: "noequalshere", Unseparated: true},
	}
	ts, fams := Project(cs)
	if ts.Len() != 1 {
		t.Errorf("tags = %v, want only TITLE", ts)
	}
	for _, f := range fams {
		if f.Key == "" {
			t.Errorf("an empty key reached the family view: %+v", f)
		}
	}
	if chs := ProjectChapters(cs); len(chs) != 0 {
		t.Errorf("chapters = %v, want none", chs)
	}
	if sets, ws := ProjectSyncedLyricsReport(cs); len(sets) != 0 || len(ws) != 0 {
		t.Errorf("synced lyrics = %v, warnings = %v, want none", sets, ws)
	}
	if ws := EncoderNoise("vend", cs); len(ws) != 0 {
		t.Errorf("encoder noise = %v, want none", ws)
	}
}

// TestUnseparatedEntryWarnsMalformed: the entry is preserved but unreadable, so it must be
// reported the way an unrepresentable key is - the observable consequences are identical.
func TestUnseparatedEntryWarnsMalformed(t *testing.T) {
	ws := InvalidKeyWarnings([]Comment{
		{Name: "TITLE", Value: "Song"},
		{Value: "noequalshere", Unseparated: true},
	})
	if len(ws) != 1 || ws[0].Code != core.WarnMalformedTagEntry {
		t.Fatalf("warnings = %v, want one malformed-tag-entry", ws)
	}
	if got := ws[0].Message; !strings.Contains(got, "noequalshere") {
		t.Errorf("the warning does not quote the entry: %q", got)
	}
}

// TestRebuildPreservesUnseparatedEntry: an unrelated edit rewrites the list, so the entry
// has to be carried through the rebuild as well as the renderer.
func TestRebuildPreservesUnseparatedEntry(t *testing.T) {
	orig := []Comment{
		{Name: "TITLE", Value: "Song"},
		{Value: "noequalshere", Unseparated: true},
	}
	edited, _ := Project(orig)
	edited.Set(tag.Artist, "Band")
	out, _ := Rebuild(orig, edited, map[tag.Key]bool{tag.Artist: true}, nil, false, nil, false)
	found := false
	for _, cm := range out {
		if cm.Unseparated && cm.Value == "noequalshere" {
			found = true
		}
	}
	if !found {
		t.Errorf("the rebuild dropped the unseparated entry: %+v", out)
	}
}
