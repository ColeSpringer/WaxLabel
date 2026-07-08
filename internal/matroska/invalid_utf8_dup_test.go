package matroska

import (
	"context"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// segBytesDoc wraps a Segment body with the given DocType ("matroska" or "webm"),
// so a single scenario can be exercised as both container flavors (the tag write
// path is DocType-agnostic).
func segBytesDoc(docType string, body []byte) []byte {
	out := encElement(idEBML, stringElement(idDocType, docType))
	return append(out, encElement(idSegment, body)...)
}

// TestMatroskaInvalidUTF8TagNotDuplicatedOnEdit is a regression: a
// non-conformant file can hold invalid UTF-8 in an album-scope SimpleTag. The
// canonical TagSet sanitizes that value, but the write path's subtraction sets were
// built from the raw bytes, so the value never folded against the canonical one, was
// never subtracted, and got re-emitted flat - growing by one copy on every unrelated
// edit. Sanitizing the subtraction source keeps the count flat; a valid-UTF-8 control
// stays unaffected.
func TestMatroskaInvalidUTF8TagNotDuplicatedOnEdit(t *testing.T) {
	for _, docType := range []string{"matroska", "webm"} {
		t.Run(docType, func(t *testing.T) {
			targets := encElement(idTargets, uintElement(idTgtTypeVal, 50)) // album scope
			badArtist := encElement(idSimpleTag, cat(
				stringElement(idTagName, "ARTIST"),
				stringElement(idTagString, "bad\xff\xfeval"), // invalid UTF-8
			))
			composer := encElement(idSimpleTag, cat(
				stringElement(idTagName, "COMPOSER"),
				stringElement(idTagString, "Bach"), // valid-UTF-8 control
			))
			tags := encElement(idTags, encElement(idTag, cat(targets, badArtist, composer)))
			src := segBytesDoc(docType, cat(mkInfo("Title"), tags, emptyCluster()))

			// Sanity: the corrupted ARTIST projects (sanitized) to a single value.
			if got, _ := parseMKA(t, src).Tags.Get(tag.Artist); len(got) != 1 {
				t.Fatalf("setup: ARTIST projects to %d values, want 1", len(got))
			}

			// Several unrelated ALBUM edits, each applied to the previous output.
			cur := src
			for i, album := range []string{"One", "Two", "Three", "Four"} {
				b := parseMKA(t, cur)
				ed := b.Clone()
				ed.Tags.Set(tag.Album, album)
				plan, err := Codec{}.Plan(context.Background(), b, ed, core.DefaultWriteOptions())
				if err != nil {
					t.Fatalf("edit %d Plan: %v", i+1, err)
				}
				cur = renderPlan(t, cur, plan)

				re := parseMKA(t, cur)
				if got, _ := re.Tags.Get(tag.Artist); len(got) != 1 {
					t.Fatalf("after %d edit(s): ARTIST projects to %d values %q, want 1 (invalid-UTF-8 tag duplicated)", i+1, len(got), got)
				}
				if got, _ := re.Tags.Get(tag.Composer); len(got) != 1 {
					t.Errorf("after %d edit(s): valid-UTF-8 COMPOSER control projects to %d values, want 1", i+1, len(got))
				}
				if got, _ := re.Tags.First(tag.Album); got != album {
					t.Errorf("after %d edit(s): ALBUM = %q, want %q", i+1, got, album)
				}
			}
		})
	}
}
